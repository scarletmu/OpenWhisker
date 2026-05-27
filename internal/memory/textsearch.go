package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// TextSearcher is the read-only literal-search interface Recall Pass 2
// needs. It is intentionally a narrower contract than the Phase 6
// `vault_text_search` tool: no scope_subset validation, no Skill, no trace
// summary. The daemon provides an fsTextSearcher; tests pass fakes.
type TextSearcher interface {
	Search(ctx context.Context, query string, scopeDirs []string, maxHits int) ([]TextHit, error)
}

// TextHit is one matching .md file with the first line containing the
// query needle (case-insensitive). Caller may use Path + Line in the
// RecallItem.Excerpt slot.
type TextHit struct {
	Path string
	Line string
}

const (
	// DefaultTextSearchHits is the cap fsTextSearcher applies when callers
	// pass maxHits <= 0. Mirrors Phase 6 vault_text_search.
	DefaultTextSearchHits   = 50
	textSearchMinQueryRunes = 2
)

// DefaultTextSearchScopeDirs is the set of vault subtrees fsTextSearcher
// scans when the caller does not provide explicit ScopeDirs. Mirrors the
// Phase 8 "default exclude Archive/" convention while keeping Phase 7
// enrichment paths (Raw/) reachable.
var DefaultTextSearchScopeDirs = []string{"Knowledge", "Interview", "Life", "Raw"}

// NewFSTextSearcher returns a TextSearcher that walks the vault on disk
// each call. v1 has no persistent text index; a future Pass-4 vector or
// SQLite FTS index can replace this implementation without changing the
// Recall API.
func NewFSTextSearcher(vaultRoot string, defaultScopeDirs []string) TextSearcher {
	scope := defaultScopeDirs
	if len(scope) == 0 {
		scope = DefaultTextSearchScopeDirs
	}
	return &fsTextSearcher{vaultRoot: vaultRoot, defaultScope: scope}
}

type fsTextSearcher struct {
	vaultRoot    string
	defaultScope []string
}

func (f *fsTextSearcher) Search(ctx context.Context, query string, callerScope []string, maxHits int) ([]TextHit, error) {
	needle := strings.ToLower(strings.TrimSpace(query))
	if len([]rune(needle)) < textSearchMinQueryRunes {
		return nil, nil
	}
	scope := callerScope
	if len(scope) == 0 {
		scope = f.defaultScope
	}
	if maxHits <= 0 {
		maxHits = DefaultTextSearchHits
	}
	out := make([]TextHit, 0, 16)
	for _, dir := range scope {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		absRoot := filepath.Join(f.vaultRoot, filepath.FromSlash(strings.TrimSuffix(dir, "/")))
		if _, err := os.Stat(absRoot); os.IsNotExist(err) {
			continue
		}
		walkErr := filepath.Walk(absRoot, func(absPath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				if os.IsPermission(walkErr) {
					return nil
				}
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if info.IsDir() {
				name := info.Name()
				if strings.HasPrefix(name, ".") && name != "." {
					return filepath.SkipDir
				}
				if name == "Archive" {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(absPath), ".md") {
				return nil
			}
			rel, err := filepath.Rel(f.vaultRoot, absPath)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			data, err := os.ReadFile(absPath)
			if err != nil {
				return nil
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(strings.ToLower(line), needle) {
					out = append(out, TextHit{Path: rel, Line: strings.TrimSpace(line)})
					break
				}
			}
			if len(out) >= maxHits {
				return filepath.SkipAll
			}
			return nil
		})
		if walkErr != nil && walkErr != filepath.SkipAll {
			return out, walkErr
		}
		if len(out) >= maxHits {
			break
		}
	}
	return out, nil
}
