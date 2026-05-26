// Implementations of the Phase 6 read-only vault tools exposed to the
// ToolCallingEngine. Every tool re-validates its arguments against the
// Skill's vault_scope (and the universal forbidden-prefix blacklist) before
// touching the filesystem — never trust the LLM-provided path.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"

	"github.com/scarletmu/openwhisker/internal/sanitize"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/vault/linkindex"
)

const (
	// SubmitResultToolName is the engine-internal terminator. Not subject to
	// vault_tools whitelist; always present in the catalog.
	SubmitResultToolName = "submit_result"

	defaultReadVaultNoteBytes = 128 * 1024
	hardReadVaultNoteBytes    = 256 * 1024
	maxListVaultDirEntries    = 50
	maxTextSearchHits         = 50
	textSearchMinQueryLen     = 2
	traceSafeSummaryBytes     = 200
)

// ToolError is returned by tool implementations for recoverable errors. The
// engine wraps the message into a tool-role message so the LLM can see why
// the call failed and try again.
type ToolError struct {
	Code    string // machine-readable, e.g. "out_of_scope"
	Message string
}

func (e *ToolError) Error() string { return e.Message }

func newToolError(code, msg string) *ToolError { return &ToolError{Code: code, Message: msg} }

// ToolInvocation is one parsed function call from the model. The engine
// passes this to ToolExecutor.Execute after validating the tool name is in
// the Skill's whitelist (or is the implicit submit_result).
type ToolInvocation struct {
	Name string
	Args json.RawMessage
}

// ToolResult is what a tool returns. Content is the JSON string sent back to
// the model as the tool-role message; SafeSummary (≤200 bytes, sanitized) is
// what gets stored in the trace.
type ToolResult struct {
	Content      string
	Bytes        int
	Truncated    bool
	SafeSummary  string
	ResultSHA256 string
}

// VaultToolExecutor implements the 5 Phase 6 read-only vault tools. The
// concrete file paths come from the Skill's vault_scope; LinkIndex is
// optional but required for vault_outlinks / vault_backlinks.
type VaultToolExecutor struct {
	VaultRoot         string
	Skill             scheduler.ScheduledSkill
	LinkIndex         *linkindex.Index
	ReadVaultNoteCap  int // 0 → defaultReadVaultNoteBytes
}

// Execute dispatches based on the tool name. The engine has already verified
// name ∈ Skill.VaultTools (or name == SubmitResultToolName); we still
// re-check here as defense in depth.
func (e VaultToolExecutor) Execute(ctx context.Context, inv ToolInvocation) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	switch inv.Name {
	case "list_vault_dir":
		return e.listVaultDir(inv.Args)
	case "read_vault_note":
		return e.readVaultNote(inv.Args)
	case "vault_outlinks":
		return e.vaultOutlinks(inv.Args)
	case "vault_backlinks":
		return e.vaultBacklinks(inv.Args)
	case "vault_text_search":
		return e.vaultTextSearch(ctx, inv.Args)
	default:
		return ToolResult{}, newToolError("unknown_tool", fmt.Sprintf("tool %q is not implemented", inv.Name))
	}
}

// ---- tool: list_vault_dir ----

type listVaultDirArgs struct {
	Path string `json:"path"`
}

func (e VaultToolExecutor) listVaultDir(rawArgs json.RawMessage) (ToolResult, error) {
	var args listVaultDirArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return ToolResult{}, newToolError("bad_args", "expected {path: string}")
	}
	rel, scopeErr := e.resolveAndCheckPath(args.Path)
	if scopeErr != nil {
		return ToolResult{}, scopeErr
	}
	abs := filepath.Join(e.VaultRoot, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		return ToolResult{}, newToolError("not_found", err.Error())
	}
	if !info.IsDir() {
		return ToolResult{}, newToolError("not_a_directory", fmt.Sprintf("%s is not a directory", rel))
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return ToolResult{}, newToolError("io", err.Error())
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			// Mirror Phase 5 behavior: symlinks are visible but flagged so
			// the LLM does not try to read them.
			names = append(names, name+"@")
			continue
		}
		if entry.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Strings(names)
	truncated := len(names) > maxListVaultDirEntries
	if truncated {
		names = names[:maxListVaultDirEntries]
	}
	payload := map[string]any{
		"path":      rel,
		"entries":   names,
		"truncated": truncated,
	}
	return e.finalize(payload)
}

// ---- tool: read_vault_note ----

type readVaultNoteArgs struct {
	Path string `json:"path"`
}

func (e VaultToolExecutor) readVaultNote(rawArgs json.RawMessage) (ToolResult, error) {
	var args readVaultNoteArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return ToolResult{}, newToolError("bad_args", "expected {path: string}")
	}
	rel, scopeErr := e.resolveAndCheckPath(args.Path)
	if scopeErr != nil {
		return ToolResult{}, scopeErr
	}
	abs := filepath.Join(e.VaultRoot, filepath.FromSlash(rel))
	capBytes := e.ReadVaultNoteCap
	if capBytes <= 0 {
		capBytes = defaultReadVaultNoteBytes
	}
	if capBytes > hardReadVaultNoteBytes {
		capBytes = hardReadVaultNoteBytes
	}
	file, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ToolResult{}, newToolError("io", err.Error())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ToolResult{}, newToolError("io", err.Error())
	}
	if !info.Mode().IsRegular() {
		return ToolResult{}, newToolError("not_a_file", fmt.Sprintf("%s is not a regular file", rel))
	}
	buf := make([]byte, capBytes+1)
	n, _ := file.Read(buf)
	truncated := n > capBytes
	if truncated {
		n = capBytes
	}
	content := string(buf[:n])
	payload := map[string]any{
		"path":      rel,
		"content":   content,
		"truncated": truncated,
		"bytes":     n,
	}
	return e.finalize(payload)
}

// ---- tool: vault_outlinks ----

type vaultOutlinksArgs struct {
	Path string `json:"path"`
}

func (e VaultToolExecutor) vaultOutlinks(rawArgs json.RawMessage) (ToolResult, error) {
	if e.LinkIndex == nil {
		return ToolResult{}, newToolError("link_index_not_ready", "link index is not configured for this run")
	}
	var args vaultOutlinksArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return ToolResult{}, newToolError("bad_args", "expected {path: string}")
	}
	rel, scopeErr := e.resolveAndCheckPath(args.Path)
	if scopeErr != nil {
		return ToolResult{}, scopeErr
	}
	links, err := e.LinkIndex.Outlinks(rel)
	if err != nil {
		if errors.Is(err, linkindex.ErrNotReady) {
			return ToolResult{}, newToolError("link_index_not_ready", "link index has not finished building; try vault_text_search instead")
		}
		return ToolResult{}, newToolError("io", err.Error())
	}
	wikilinks := []map[string]any{}
	related := []map[string]any{}
	for _, l := range links {
		entry := map[string]any{
			"target_path":   l.TargetPath,
			"original_text": l.OriginalText,
			"resolved":      l.Resolved,
		}
		switch l.SourceKind {
		case linkindex.SourceKindWikilink:
			wikilinks = append(wikilinks, entry)
		case linkindex.SourceKindFrontmatterRelated:
			related = append(related, entry)
		}
	}
	payload := map[string]any{
		"path":                rel,
		"wikilinks":           wikilinks,
		"frontmatter_related": related,
		"index_generation":    e.LinkIndex.Generation(),
	}
	return e.finalize(payload)
}

// ---- tool: vault_backlinks ----

type vaultBacklinksArgs struct {
	Path string `json:"path"`
}

func (e VaultToolExecutor) vaultBacklinks(rawArgs json.RawMessage) (ToolResult, error) {
	if e.LinkIndex == nil {
		return ToolResult{}, newToolError("link_index_not_ready", "link index is not configured for this run")
	}
	var args vaultBacklinksArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return ToolResult{}, newToolError("bad_args", "expected {path: string}")
	}
	rel, scopeErr := e.resolveAndCheckPath(args.Path)
	if scopeErr != nil {
		return ToolResult{}, scopeErr
	}
	sources, err := e.LinkIndex.Backlinks(rel)
	if err != nil {
		if errors.Is(err, linkindex.ErrNotReady) {
			return ToolResult{}, newToolError("link_index_not_ready", "link index has not finished building; try vault_text_search instead")
		}
		return ToolResult{}, newToolError("io", err.Error())
	}
	// Drop any backlink source that the Skill cannot read (defense in depth:
	// the index is scoped, but the read_roots are configured separately and
	// could conceivably diverge).
	filtered := sources[:0]
	for _, src := range sources {
		if e.pathInScope(src) == nil {
			filtered = append(filtered, src)
		}
	}
	payload := map[string]any{
		"path":             rel,
		"sources":          filtered,
		"index_generation": e.LinkIndex.Generation(),
	}
	return e.finalize(payload)
}

// ---- tool: vault_text_search ----

type vaultTextSearchArgs struct {
	Query       string   `json:"query"`
	ScopeSubset []string `json:"scope_subset,omitempty"`
}

func (e VaultToolExecutor) vaultTextSearch(ctx context.Context, rawArgs json.RawMessage) (ToolResult, error) {
	var args vaultTextSearchArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return ToolResult{}, newToolError("bad_args", "expected {query: string, scope_subset?: []string}")
	}
	query := strings.TrimSpace(args.Query)
	if len(query) < textSearchMinQueryLen {
		return ToolResult{}, newToolError("query_too_short", fmt.Sprintf("query must be at least %d characters", textSearchMinQueryLen))
	}
	// Validate scope_subset is a strict subset of Skill.VaultScope and is not
	// caught by the forbidden-prefix blacklist. We replace the working scope
	// with the *cleaned* entries (not the LLM-supplied raw strings) so the
	// walk and the post-walk containsAnyPrefix check use a normalized form.
	scope := e.Skill.VaultScope
	if len(args.ScopeSubset) > 0 {
		cleaned := make([]string, 0, len(args.ScopeSubset))
		for _, s := range args.ScopeSubset {
			trimmed := strings.TrimSpace(s)
			if trimmed == "" {
				return ToolResult{}, newToolError("bad_scope", "scope_subset entry is empty")
			}
			if scheduler.IsForbiddenScopePath(trimmed) {
				return ToolResult{}, newToolError("forbidden_prefix", fmt.Sprintf("%q falls under a forbidden prefix", s))
			}
			cleanS, err := scheduler.CleanRelativeDirExport(strings.TrimSuffix(trimmed, "/"))
			if err != nil {
				return ToolResult{}, newToolError("bad_scope", err.Error())
			}
			if cleanS == "" {
				return ToolResult{}, newToolError("bad_scope", "scope_subset entry resolved to vault root")
			}
			if err := e.pathInScope(cleanS); err != nil {
				return ToolResult{}, newToolError("scope_violation", fmt.Sprintf("%q is not within the Skill's vault_scope", s))
			}
			cleaned = append(cleaned, cleanS)
		}
		scope = cleaned
	}

	needle := strings.ToLower(query)
	type hit struct {
		Path string `json:"path"`
		Line string `json:"line"`
	}
	hits := make([]hit, 0, 16)
	walkErr := error(nil)

	for _, root := range scope {
		if err := ctx.Err(); err != nil {
			return ToolResult{}, err
		}
		absRoot := filepath.Join(e.VaultRoot, filepath.FromSlash(strings.TrimSuffix(root, "/")))
		walkErr = filepath.Walk(absRoot, func(absPath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				if os.IsPermission(walkErr) {
					return nil
				}
				return walkErr
			}
			// Poll the deadline on every entry so large vaults can't outrun
			// the wall-clock budget by burning the walk loop.
			if err := ctx.Err(); err != nil {
				return err
			}
			if info.IsDir() {
				if scheduler.IsForbiddenScopePath(filepath.Base(absPath)) {
					return filepath.SkipDir
				}
				return nil
			}
			// Reject symlinks outright: read_vault_note uses O_NOFOLLOW, and
			// vault_text_search must not be the asymmetric escape hatch that
			// would let the LLM read files outside the vault via a symlink.
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			if !strings.EqualFold(filepath.Ext(absPath), ".md") {
				return nil
			}
			rel, err := filepath.Rel(e.VaultRoot, absPath)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			// Belt-and-braces: the cleaned scope already excludes forbidden
			// prefixes, but walks under a permissive ancestor could surface
			// a forbidden subtree we haven't pruned yet (e.g. a `.git/` deep
			// inside an allowed root).
			if scheduler.IsForbiddenScopePath(rel) {
				return nil
			}
			if !containsAnyPrefix(rel, scope) {
				return nil
			}
			// Re-stat without following symlinks so we never open a symlink
			// the directory entry didn't already flag (race window between
			// readdir and now).
			lstat, statErr := os.Lstat(absPath)
			if statErr != nil || lstat.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			data, err := os.ReadFile(absPath)
			if err != nil {
				return nil
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(strings.ToLower(line), needle) {
					hits = append(hits, hit{Path: rel, Line: strings.TrimSpace(line)})
					break
				}
			}
			if len(hits) >= maxTextSearchHits {
				return filepath.SkipAll
			}
			return nil
		})
		if walkErr != nil {
			break
		}
	}
	if walkErr != nil && !errors.Is(walkErr, filepath.SkipAll) {
		return ToolResult{}, newToolError("io", walkErr.Error())
	}
	truncated := len(hits) >= maxTextSearchHits
	if truncated {
		hits = hits[:maxTextSearchHits]
	}
	payload := map[string]any{
		"query":     query,
		"hits":      hits,
		"truncated": truncated,
	}
	return e.finalize(payload)
}

// ---- helpers ----

// resolveAndCheckPath cleans a tool-provided path string and verifies it
// falls inside the Skill's vault_scope and outside the global forbidden
// prefixes. Returns the vault-relative path (forward slashes).
func (e VaultToolExecutor) resolveAndCheckPath(raw string) (string, *ToolError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", newToolError("bad_args", "path is required")
	}
	if scheduler.IsForbiddenScopePath(raw) {
		return "", newToolError("forbidden_prefix", fmt.Sprintf("%q falls under a forbidden prefix", raw))
	}
	rel, err := scheduler.CleanRelativePatternExport(raw)
	if err != nil {
		return "", newToolError("bad_path", err.Error())
	}
	if rel == "" {
		return "", newToolError("bad_path", "path is empty")
	}
	if err := e.pathInScope(rel); err != nil {
		return "", newToolError("out_of_scope", err.Error())
	}
	return rel, nil
}

func (e VaultToolExecutor) pathInScope(rel string) error {
	rel = strings.TrimRight(filepath.ToSlash(rel), "/")
	if rel == "" {
		return errors.New("empty path")
	}
	// Allow exact-match of a scope root (so list_vault_dir("Raw/") works).
	for _, root := range e.Skill.VaultScope {
		if strings.TrimRight(root, "/") == rel {
			return nil
		}
		rootSlash := strings.TrimRight(root, "/") + "/"
		if strings.HasPrefix(rel+"/", rootSlash) {
			return nil
		}
	}
	return fmt.Errorf("%s is not within the Skill's vault_scope", rel)
}

func containsAnyPrefix(rel string, roots []string) bool {
	rel = strings.TrimRight(filepath.ToSlash(rel), "/") + "/"
	for _, root := range roots {
		root = strings.TrimRight(root, "/") + "/"
		if strings.HasPrefix(rel, root) {
			return true
		}
	}
	return false
}

// finalize encodes the result payload, builds the safe summary, and computes
// the SHA-256 for trace deduplication.
func (e VaultToolExecutor) finalize(payload map[string]any) (ToolResult, error) {
	content, err := json.Marshal(payload)
	if err != nil {
		return ToolResult{}, newToolError("encode", err.Error())
	}
	sum := sha256.Sum256(content)
	return ToolResult{
		Content:      string(content),
		Bytes:        len(content),
		Truncated:    asBool(payload["truncated"]),
		SafeSummary:  buildTraceSafeSummary(payload),
		ResultSHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func asBool(v any) bool {
	b, _ := v.(bool)
	return b
}

// buildTraceSafeSummary produces a ≤200-byte sanitized description suitable
// for the agent_runs.tool_trace_json column. Never includes raw file content.
func buildTraceSafeSummary(payload map[string]any) string {
	var b strings.Builder
	for _, key := range []string{"path", "query"} {
		if v, ok := payload[key].(string); ok && v != "" {
			fmt.Fprintf(&b, "%s=%s ", key, v)
		}
	}
	if entries, ok := payload["entries"].([]string); ok {
		fmt.Fprintf(&b, "entries=%d ", len(entries))
	}
	if hits, ok := payload["hits"]; ok {
		switch v := hits.(type) {
		case []map[string]any:
			fmt.Fprintf(&b, "hits=%d ", len(v))
		}
	}
	if bytes, ok := payload["bytes"].(int); ok {
		fmt.Fprintf(&b, "bytes=%d ", bytes)
	}
	if asBool(payload["truncated"]) {
		fmt.Fprintf(&b, "truncated ")
	}
	out := strings.TrimSpace(sanitize.FreeText(b.String()))
	if len(out) > traceSafeSummaryBytes {
		// Truncate on a rune boundary so we never emit a half-codepoint.
		runes := []rune(out)
		for i := len(runes); i > 0; i-- {
			candidate := string(runes[:i])
			if len(candidate) <= traceSafeSummaryBytes {
				out = candidate
				break
			}
		}
	}
	return strings.TrimRightFunc(out, unicode.IsSpace)
}

// SubmitResultToolDescriptor describes the engine-internal terminator tool.
// It accepts a free-form payload object so individual Skills can shape it as
// their authors see fit.
func SubmitResultToolDescriptor() ChatTool {
	return ChatTool{
		Type: "function",
		Function: ChatToolFunction2{
			Name:        SubmitResultToolName,
			Description: "Submit the final result of this run. Calling this terminates the agent loop. The host writes title/summary/payload to the outbox.",
			Parameters: map[string]any{
				"type":     "object",
				"required": []string{"title", "summary"},
				"properties": map[string]any{
					"title":   map[string]any{"type": "string"},
					"summary": map[string]any{"type": "string"},
					"payload": map[string]any{"type": "object", "description": "Skill-defined structured output (optional)."},
				},
			},
		},
	}
}

// VaultToolDescriptors returns the ChatTool descriptors for the names listed
// in the Skill's vault_tools whitelist. Tools not in the catalog are skipped
// silently (the loader already rejected those at registry time).
func VaultToolDescriptors(names []string) []ChatTool {
	out := make([]ChatTool, 0, len(names))
	for _, name := range names {
		switch name {
		case "list_vault_dir":
			out = append(out, ChatTool{Type: "function", Function: ChatToolFunction2{
				Name:        "list_vault_dir",
				Description: "List entries (file and subdirectory names) under a vault directory. Returns up to 50 sorted entries; symlinks are flagged with @ and not followed.",
				Parameters: map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Vault-relative directory path within the Skill's vault_scope."},
					},
				},
			}})
		case "read_vault_note":
			out = append(out, ChatTool{Type: "function", Function: ChatToolFunction2{
				Name:        "read_vault_note",
				Description: "Read the full content of a vault .md file. Returns up to 128 KiB; if larger, truncated=true.",
				Parameters: map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string", "description": "Vault-relative file path within the Skill's vault_scope."},
					},
				},
			}})
		case "vault_outlinks":
			out = append(out, ChatTool{Type: "function", Function: ChatToolFunction2{
				Name:        "vault_outlinks",
				Description: "Return outgoing wikilinks and frontmatter related: entries for the given note. Returns two arrays (wikilinks and frontmatter_related). Unresolved aliases are present with resolved=false.",
				Parameters: map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			}})
		case "vault_backlinks":
			out = append(out, ChatTool{Type: "function", Function: ChatToolFunction2{
				Name:        "vault_backlinks",
				Description: "Return all vault notes that link to the given note via wikilinks or frontmatter related. Requires the link index; if not ready, returns link_index_not_ready and the LLM should fall back to vault_text_search.",
				Parameters: map[string]any{
					"type":     "object",
					"required": []string{"path"},
					"properties": map[string]any{
						"path": map[string]any{"type": "string"},
					},
				},
			}})
		case "vault_text_search":
			out = append(out, ChatTool{Type: "function", Function: ChatToolFunction2{
				Name:        "vault_text_search",
				Description: "Case-insensitive literal text search over .md files in the Skill's vault_scope (or scope_subset if narrower). Returns up to 50 hits with one matching line per file. Query must be at least 2 characters.",
				Parameters: map[string]any{
					"type":     "object",
					"required": []string{"query"},
					"properties": map[string]any{
						"query":        map[string]any{"type": "string"},
						"scope_subset": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional subset of vault_scope to search within."},
					},
				},
			}})
		}
	}
	return out
}
