package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/markdown"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/profile"
)

// parseExpanderSourceTracePaths extracts raw / processed note paths recorded
// inside the target Knowledge note's YAML frontmatter under the openwhisker
// nested block. It recognizes the scalar fields raw_path / processed_path
// written by the Raw Organizer (see knowledge-draft-schema.md), and also
// tolerates list fields raw_paths / processed_paths so a human-authored
// Knowledge note aggregating several raw sources still resolves its source
// trace. The returned slice preserves declaration order and is deduplicated.
func parseExpanderSourceTracePaths(content string) []string {
	block, _, found := markdown.SplitFrontmatter(content)
	if !found {
		return nil
	}
	lines := strings.Split(block, "\n")
	inOpenwhisker := false
	openwhiskerIndent := -1
	var (
		results  []string
		seen     = map[string]struct{}{}
		listKey  string
		listSeen bool
	)
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		raw = strings.Trim(raw, `"'`)
		if raw == "" {
			return
		}
		if _, ok := seen[raw]; ok {
			return
		}
		seen[raw] = struct{}{}
		results = append(results, raw)
	}
	for _, line := range lines {
		trimmedLeft := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmedLeft)
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !inOpenwhisker {
			if indent == 0 && strings.HasPrefix(trimmedLeft, "openwhisker:") {
				inOpenwhisker = true
				openwhiskerIndent = -1
				listSeen = false
			}
			continue
		}
		if indent == 0 {
			// left the openwhisker block at a new top-level key
			break
		}
		if openwhiskerIndent < 0 {
			openwhiskerIndent = indent
		}
		if indent == openwhiskerIndent {
			listSeen = false
			listKey = ""
			rest := trimmedLeft
			switch {
			case strings.HasPrefix(rest, "raw_path:"):
				add(strings.TrimPrefix(rest, "raw_path:"))
			case strings.HasPrefix(rest, "processed_path:"):
				add(strings.TrimPrefix(rest, "processed_path:"))
			case strings.HasPrefix(rest, "raw_paths:"):
				listKey = "raw_paths"
				listSeen = true
			case strings.HasPrefix(rest, "processed_paths:"):
				listKey = "processed_paths"
				listSeen = true
			}
			continue
		}
		if listSeen && (listKey == "raw_paths" || listKey == "processed_paths") && strings.HasPrefix(trimmedLeft, "- ") {
			add(strings.TrimPrefix(trimmedLeft, "- "))
		}
	}
	return results
}

func buildKnowledgeExpanderContext(ctx context.Context, vaultRoot, targetPath, contextMode string, conventions policy.Conventions) (KnowledgeExpanderContext, error) {
	if vaultRoot == "" {
		return KnowledgeExpanderContext{}, errors.New("vault root is required")
	}
	targetFullPath, err := executor.ResolveVaultPath(vaultRoot, targetPath)
	if err != nil {
		return KnowledgeExpanderContext{}, err
	}
	noteContent, err := os.ReadFile(targetFullPath)
	if err != nil {
		return KnowledgeExpanderContext{}, fmt.Errorf("read knowledge note %s: %w", targetPath, err)
	}
	context := KnowledgeExpanderContext{
		TargetPath:    targetPath,
		TargetContent: string(noteContent),
	}
	for _, relPath := range parseExpanderSourceTracePaths(string(noteContent)) {
		if relPath == "" || relPath == targetPath {
			continue
		}
		fullPath, err := executor.ResolveVaultPath(vaultRoot, relPath)
		if err != nil {
			continue
		}
		content, err := os.ReadFile(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return KnowledgeExpanderContext{}, fmt.Errorf("read knowledge expander related note %s: %w", relPath, err)
		}
		context.RelatedNotes = append(context.RelatedNotes, VaultContextDocument{
			Path:    relPath,
			Content: string(content),
		})
	}
	for _, doc := range profile.KnowledgeExpanderContextDocuments(conventions) {
		context.Documents = append(context.Documents, VaultContextDocument{
			Path:    doc.Path,
			Content: doc.Content,
		})
	}
	if normalizeContextMode(contextMode) == ContextModeMinimal {
		return context, nil
	}
	docs, err := appendVaultContextDocs(ctx, vaultRoot, []string{
		"AGENTS.md",
		"Meta/README.md",
		"Meta/Tagging.md",
		"Knowledge/AGENTS.md",
	}, context.Documents)
	if err != nil {
		return KnowledgeExpanderContext{}, err
	}
	context.Documents = docs
	return context, nil
}

// appendVaultContextDocs reads each relPath under vaultRoot and appends it to
// dst as a VaultContextDocument, skipping files that don't exist. It honors
// ctx cancellation between reads.
func appendVaultContextDocs(ctx context.Context, vaultRoot string, relPaths []string, dst []VaultContextDocument) ([]VaultContextDocument, error) {
	for _, relPath := range relPaths {
		if err := ctx.Err(); err != nil {
			return dst, err
		}
		fullPath, err := executor.ResolveVaultPath(vaultRoot, relPath)
		if err != nil {
			return dst, err
		}
		content, err := os.ReadFile(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return dst, fmt.Errorf("read vault context %s: %w", relPath, err)
		}
		dst = append(dst, VaultContextDocument{Path: relPath, Content: string(content)})
	}
	return dst, nil
}

func buildRawOrganizerContext(ctx context.Context, vaultRoot, rawPath, contextMode string, conventions policy.Conventions) (RawOrganizerContext, error) {
	if vaultRoot == "" {
		return RawOrganizerContext{}, errors.New("vault root is required")
	}
	rawFullPath, err := executor.ResolveVaultPath(vaultRoot, rawPath)
	if err != nil {
		return RawOrganizerContext{}, err
	}
	rawNote, err := os.ReadFile(rawFullPath)
	if err != nil {
		return RawOrganizerContext{}, fmt.Errorf("read raw note %s: %w", rawPath, err)
	}
	context := RawOrganizerContext{
		RawPath: rawPath,
		RawNote: string(rawNote),
	}
	for _, doc := range profile.ContextDocuments(conventions) {
		context.Documents = append(context.Documents, VaultContextDocument{
			Path:    doc.Path,
			Content: doc.Content,
		})
	}
	if normalizeContextMode(contextMode) == ContextModeMinimal {
		return context, nil
	}
	docs, err := appendVaultContextDocs(ctx, vaultRoot, []string{
		"AGENTS.md",
		"Meta/README.md",
		"Meta/Tagging.md",
		"Raw/AGENTS.md",
		"Knowledge/AGENTS.md",
	}, context.Documents)
	if err != nil {
		return RawOrganizerContext{}, err
	}
	context.Documents = docs
	return context, nil
}
