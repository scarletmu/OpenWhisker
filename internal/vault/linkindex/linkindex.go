// Package linkindex builds an in-memory wikilink/backlink map over a vault
// tree and keeps it current via fsnotify.
//
// Two consumption modes:
//
//   - Daemon: call BuildAsync to spin up a background scan, then Watch to
//     enable fsnotify-driven incremental updates. Queries against an
//     un-built index return ErrNotReady so tools can degrade gracefully.
//
//   - CLI (`openwhisker ask`, `scheduler tick`): call Build for a synchronous
//     full scan; the process is one-shot so no watcher is needed.
//
// The index is the data backing vault_outlinks / vault_backlinks tools in the
// Phase 6 ToolCallingEngine.
package linkindex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/scarletmu/openwhisker/internal/markdown"
)

// ErrNotReady is returned by Outlinks / Backlinks when the index has not
// finished its initial build. Tool implementations should surface this as a
// link_index_not_ready error so the LLM can fall back to text search.
var ErrNotReady = errors.New("link index not ready")

// SourceKind tags the origin of a ResolvedLink so consumers can distinguish
// frontmatter-declared "strong" relationships from incidental body wikilinks.
type SourceKind string

const (
	SourceKindWikilink           SourceKind = "wikilink"
	SourceKindFrontmatterRelated SourceKind = "frontmatter_related"
)

// ResolvedLink describes one outgoing edge from a note. TargetPath is empty
// and Resolved is false when the link text cannot be matched to a vault
// file (e.g. Obsidian alias the v1 resolver does not understand).
type ResolvedLink struct {
	TargetPath   string     `json:"target_path,omitempty"`
	SourceKind   SourceKind `json:"source_kind"`
	OriginalText string     `json:"original_text"`
	Resolved     bool       `json:"resolved"`
}

// Index is the public face of the package. Build/Watch mutate it under mu;
// Outlinks/Backlinks/Snapshot read it under RLock.
type Index struct {
	vaultRoot string
	readRoots []string // forward-slash, trailing "/"

	mu         sync.RWMutex
	outlinks   map[string][]ResolvedLink // key: vault-relative notePath
	backlinks  map[string][]string       // key: vault-relative notePath
	notePaths  map[string]bool           // set of .md paths seen during scan
	stemIndex  map[string][]string       // lowercase stem → list of note paths (for unqualified wikilink resolution)
	generation int64                     // bumped on every mutation (atomic ops)
	ready      atomic.Bool
	buildErr   atomic.Value // wraps error

	// watcher-specific state
	watcher      *fsnotify.Watcher
	stopWatch    chan struct{}
	pendingPaths map[string]struct{}
	pendingMu    sync.Mutex
}

// New returns an empty Index rooted at vaultRoot. The index is not populated
// until Build or BuildAsync runs. readRoots is the optional restriction set
// (forward-slash paths ending in "/"); empty means "scan the entire vault".
func New(vaultRoot string, readRoots []string) *Index {
	roots := normalizeRoots(readRoots)
	return &Index{
		vaultRoot:    vaultRoot,
		readRoots:    roots,
		outlinks:     map[string][]ResolvedLink{},
		backlinks:    map[string][]string{},
		notePaths:    map[string]bool{},
		stemIndex:    map[string][]string{},
		pendingPaths: map[string]struct{}{},
	}
}

// Ready reports whether the initial build has completed (successfully or with
// a recoverable partial result). Watchers may still be running; this only
// reflects the first full scan.
func (ix *Index) Ready() bool {
	return ix.ready.Load()
}

// BuildErr returns the most recent build error (nil if none). A non-nil error
// does not necessarily mean the index is empty — partial scans surface their
// best-effort state.
func (ix *Index) BuildErr() error {
	v := ix.buildErr.Load()
	if v == nil {
		return nil
	}
	err, _ := v.(error)
	return err
}

// Generation returns the monotonic counter incremented on every mutation. The
// agent runtime stores this in trace.link_index_version so auditors can tell
// whether a single run straddled an index update.
func (ix *Index) Generation() int64 {
	return atomic.LoadInt64(&ix.generation)
}

// Stats returns a snapshot of the current note/link counts. Cheap; reads under
// RLock. Useful for ready-log lines and tests.
type Stats struct {
	Notes      int
	OutEdges   int
	BackEdges  int
	Generation int64
}

func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var outEdges, backEdges int
	for _, links := range ix.outlinks {
		outEdges += len(links)
	}
	for _, srcs := range ix.backlinks {
		backEdges += len(srcs)
	}
	return Stats{
		Notes:      len(ix.notePaths),
		OutEdges:   outEdges,
		BackEdges:  backEdges,
		Generation: atomic.LoadInt64(&ix.generation),
	}
}

// Outlinks returns the outgoing edges for notePath. Caller must not mutate
// the returned slice. Returns ErrNotReady if the index has not built yet.
func (ix *Index) Outlinks(notePath string) ([]ResolvedLink, error) {
	if !ix.Ready() {
		return nil, ErrNotReady
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	notePath = normalizeNotePath(notePath)
	links := ix.outlinks[notePath]
	if links == nil {
		return []ResolvedLink{}, nil
	}
	// Return a defensive copy so callers can't accidentally mutate shared state.
	out := make([]ResolvedLink, len(links))
	copy(out, links)
	return out, nil
}

// Backlinks returns the source notes that link to notePath. Caller must not
// mutate the returned slice. Returns ErrNotReady if the index has not built.
func (ix *Index) Backlinks(notePath string) ([]string, error) {
	if !ix.Ready() {
		return nil, ErrNotReady
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	notePath = normalizeNotePath(notePath)
	sources := ix.backlinks[notePath]
	if sources == nil {
		return []string{}, nil
	}
	out := make([]string, len(sources))
	copy(out, sources)
	return out, nil
}

// Build performs a synchronous full scan of the vault and marks the index
// ready on completion. Safe to call multiple times — each invocation rebuilds
// from scratch. Use this from CLI processes (one-shot, no watcher needed).
func (ix *Index) Build(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	outlinks := map[string][]ResolvedLink{}
	notePaths := map[string]bool{}
	stems := map[string][]string{}

	err := filepath.Walk(ix.vaultRoot, func(absPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			// Permission denied on a subtree should not kill the whole scan.
			if os.IsPermission(walkErr) {
				return nil
			}
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := relInsideVault(ix.vaultRoot, absPath)
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if shouldSkipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(rel), ".md") {
			return nil
		}
		if !ix.pathInReadRoots(rel) {
			return nil
		}
		links, err := parseNoteLinks(absPath)
		if err != nil {
			// One bad file should not poison the whole index.
			return nil
		}
		notePaths[rel] = true
		stem := strings.ToLower(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)))
		stems[stem] = append(stems[stem], rel)
		outlinks[rel] = links
		return nil
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		ix.buildErr.Store(err)
		// Continue: we may still have partial data worth installing.
	}

	// Resolve targets now that we have the full note set.
	for source, links := range outlinks {
		for i := range links {
			if links[i].Resolved {
				continue
			}
			resolved, ok := resolveTarget(links[i].OriginalText, source, notePaths, stems)
			if ok {
				links[i].TargetPath = resolved
				links[i].Resolved = true
			}
		}
		outlinks[source] = links
	}

	backlinks := buildBacklinks(outlinks)

	ix.mu.Lock()
	ix.outlinks = outlinks
	ix.backlinks = backlinks
	ix.notePaths = notePaths
	ix.stemIndex = stems
	ix.mu.Unlock()
	atomic.AddInt64(&ix.generation, 1)
	ix.ready.Store(true)
	return err
}

// BuildAsync kicks off Build in a goroutine. Returns immediately. Watch
// readiness via Ready(); inspect failures via BuildErr().
func (ix *Index) BuildAsync(ctx context.Context, done func(stats Stats, elapsed time.Duration, err error)) {
	go func() {
		start := time.Now()
		err := ix.Build(ctx)
		stats := ix.Stats()
		if done != nil {
			done(stats, time.Since(start), err)
		}
	}()
}

// Watch starts the fsnotify-based incremental updater. Must be called after
// Ready() (typically inside the done callback of BuildAsync) so the watcher
// sees a consistent baseline. Cancel ctx to stop the watcher.
//
// Events are coalesced via a 200ms debounce: editors that save via "tmp file
// → rename" produce a burst of fsnotify events that should result in one
// re-parse, not several.
func (ix *Index) Watch(ctx context.Context) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("link index watcher: %w", err)
	}
	ix.watcher = watcher
	ix.stopWatch = make(chan struct{})

	// Register every directory under vaultRoot that is not a skipped subtree.
	walkErr := filepath.Walk(ix.vaultRoot, func(absPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				return nil
			}
			return walkErr
		}
		if !info.IsDir() {
			return nil
		}
		rel, err := relInsideVault(ix.vaultRoot, absPath)
		if err != nil {
			return nil
		}
		if shouldSkipDir(rel) {
			return filepath.SkipDir
		}
		return watcher.Add(absPath)
	})
	if walkErr != nil {
		_ = watcher.Close()
		return fmt.Errorf("link index watcher register: %w", walkErr)
	}

	go ix.runWatchLoop(ctx)
	return nil
}

func (ix *Index) runWatchLoop(ctx context.Context) {
	defer ix.watcher.Close()
	const debounceWindow = 200 * time.Millisecond
	var (
		timer     *time.Timer
		timerC    <-chan time.Time
		debouncedFlush = func() {
			ix.flushPending()
			timerC = nil
		}
	)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ix.stopWatch:
			return
		case ev, ok := <-ix.watcher.Events:
			if !ok {
				return
			}
			if shouldIgnoreEvent(ev) {
				continue
			}
			ix.markPending(ev.Name)
			if timer == nil {
				timer = time.NewTimer(debounceWindow)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					// Drain if already fired; ignore the value.
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(debounceWindow)
				timerC = timer.C
			}
		case err, ok := <-ix.watcher.Errors:
			if !ok {
				return
			}
			// On overflow / FD exhaustion, mark unready and trigger a full
			// rebuild. Until that succeeds, vault_outlinks / vault_backlinks
			// will surface ErrNotReady to the LLM.
			fmt.Fprintf(os.Stderr, "link index watcher error: %v; triggering rebuild\n", err)
			ix.ready.Store(false)
			ix.buildErr.Store(err)
			go func() { _ = ix.Build(ctx) }()
		case <-timerC:
			debouncedFlush()
			timer = nil
		}
	}
}

// Close stops the watcher (if running). Safe to call multiple times.
func (ix *Index) Close() error {
	if ix.stopWatch != nil {
		select {
		case <-ix.stopWatch:
		default:
			close(ix.stopWatch)
		}
	}
	return nil
}

func (ix *Index) markPending(absPath string) {
	rel, err := relInsideVault(ix.vaultRoot, absPath)
	if err != nil {
		return
	}
	// We only care about .md files for content changes; directory creates need
	// to enroll the new dir in the watcher.
	ix.pendingMu.Lock()
	ix.pendingPaths[rel] = struct{}{}
	ix.pendingMu.Unlock()

	info, statErr := os.Stat(absPath)
	if statErr == nil && info.IsDir() && !shouldSkipDir(rel) {
		_ = ix.watcher.Add(absPath)
	}
}

func (ix *Index) flushPending() {
	ix.pendingMu.Lock()
	pending := ix.pendingPaths
	ix.pendingPaths = map[string]struct{}{}
	ix.pendingMu.Unlock()
	if len(pending) == 0 {
		return
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()

	mutated := false
	for rel := range pending {
		if !strings.EqualFold(filepath.Ext(rel), ".md") {
			continue
		}
		abs := filepath.Join(ix.vaultRoot, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		switch {
		case errors.Is(err, os.ErrNotExist):
			// Deleted (or moved-away half of a rename).
			delete(ix.outlinks, rel)
			delete(ix.notePaths, rel)
			stem := strings.ToLower(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)))
			ix.stemIndex[stem] = removeString(ix.stemIndex[stem], rel)
			if len(ix.stemIndex[stem]) == 0 {
				delete(ix.stemIndex, stem)
			}
			mutated = true
		case err == nil && info.Mode().IsRegular() && ix.pathInReadRoots(rel):
			links, parseErr := parseNoteLinks(abs)
			if parseErr != nil {
				continue
			}
			// Resolve under current stem index.
			for i := range links {
				if links[i].Resolved {
					continue
				}
				if target, ok := resolveTarget(links[i].OriginalText, rel, ix.notePaths, ix.stemIndex); ok {
					links[i].TargetPath = target
					links[i].Resolved = true
				}
			}
			ix.outlinks[rel] = links
			ix.notePaths[rel] = true
			stem := strings.ToLower(strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel)))
			if !containsString(ix.stemIndex[stem], rel) {
				ix.stemIndex[stem] = append(ix.stemIndex[stem], rel)
			}
			mutated = true
		}
	}
	if mutated {
		ix.backlinks = buildBacklinks(ix.outlinks)
		atomic.AddInt64(&ix.generation, 1)
	}
}

// pathInReadRoots reports whether rel is under any registered readRoot. An
// empty readRoots set means "no restriction".
func (ix *Index) pathInReadRoots(rel string) bool {
	if len(ix.readRoots) == 0 {
		return true
	}
	rel = strings.TrimRight(rel, "/") + "/"
	for _, root := range ix.readRoots {
		if strings.HasPrefix(rel, root) {
			return true
		}
	}
	// Notes inside the read roots come through above; tolerate the exact-
	// match case for dir paths the caller passed in.
	relNoSlash := strings.TrimRight(rel, "/")
	for _, root := range ix.readRoots {
		if strings.TrimRight(root, "/") == relNoSlash {
			return true
		}
	}
	return false
}

// ---- helpers (no Index receiver) ----

// reWikilink matches Obsidian wikilink syntax: [[Target]], [[Target|alias]],
// [[Target#heading]], [[Target.md]]. Embed-style ![[Target]] is also matched
// since the link semantically still depends on Target.
var reWikilink = regexp.MustCompile(`!?\[\[([^\[\]\n]+?)\]\]`)

// parseNoteLinks reads a .md file and extracts its outlinks. Returns links
// in encounter order, with body wikilinks before frontmatter "related:" so
// later resolution iteration is deterministic.
func parseNoteLinks(absPath string) ([]ResolvedLink, error) {
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	content := string(data)
	frontmatter, body := splitFrontmatter(content)
	out := []ResolvedLink{}

	for _, m := range reWikilink.FindAllStringSubmatch(body, -1) {
		raw := strings.TrimSpace(m[1])
		if raw == "" {
			continue
		}
		out = append(out, ResolvedLink{
			SourceKind:   SourceKindWikilink,
			OriginalText: raw,
		})
	}
	for _, target := range extractFrontmatterRelated(frontmatter) {
		out = append(out, ResolvedLink{
			SourceKind:   SourceKindFrontmatterRelated,
			OriginalText: target,
		})
	}
	return out, nil
}

// splitFrontmatter returns the YAML frontmatter block (without the --- fences)
// and the body. If the file has no frontmatter, frontmatter is empty. The
// boundary rule lives in internal/markdown, the single source of truth.
func splitFrontmatter(content string) (string, string) {
	block, body, _ := markdown.SplitFrontmatter(content)
	return block, body
}

// extractFrontmatterRelated returns the values under the frontmatter "related:"
// key (inline or block list), with surrounding [[...]] and quotes stripped to
// match wikilink-text format.
func extractFrontmatterRelated(frontmatter string) []string {
	var out []string
	for _, item := range markdown.Parse(frontmatter).List("related") {
		if cleaned := cleanRelatedTarget(item); cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}

func cleanRelatedTarget(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	// Strip matching quote pair.
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[[")
	value = strings.TrimSuffix(value, "]]")
	return strings.TrimSpace(value)
}

// resolveTarget resolves a wikilink text to a vault-relative path. Strategy:
//   1. Strip alias (|...) and heading (#...) suffixes — they are display only.
//   2. If the text contains "/", treat as a relative path; otherwise treat
//      as a bare filename to match against stemIndex.
//   3. If exactly one stem match exists, return it. Multiple matches yield
//      Resolved=false (unresolved aliases / ambiguous names are common in
//      Obsidian and not Phase 6's problem to solve).
//   4. Phase 6 first version intentionally does not resolve Obsidian aliases.
func resolveTarget(text, source string, notePaths map[string]bool, stems map[string][]string) (string, bool) {
	t := text
	if idx := strings.Index(t, "|"); idx >= 0 {
		t = t[:idx]
	}
	if idx := strings.Index(t, "#"); idx >= 0 {
		t = t[:idx]
	}
	t = strings.TrimSpace(t)
	if t == "" {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(t), ".md") {
		t += ".md"
	}
	t = strings.TrimPrefix(filepath.ToSlash(t), "/")

	// Exact path?
	if notePaths[t] {
		return t, true
	}

	// Bare filename → look up in stem index.
	if !strings.Contains(t, "/") {
		stem := strings.ToLower(strings.TrimSuffix(t, ".md"))
		matches := stems[stem]
		if len(matches) == 1 {
			return matches[0], true
		}
		// Multiple ambiguous matches: leave unresolved rather than pick one
		// arbitrarily. The LLM can fall back to vault_text_search.
		return "", false
	}

	// Path-style link but file does not exist in index → unresolved.
	return "", false
}

// buildBacklinks inverts the outlinks map.
func buildBacklinks(outlinks map[string][]ResolvedLink) map[string][]string {
	back := map[string][]string{}
	for source, links := range outlinks {
		seenTargets := map[string]bool{}
		for _, link := range links {
			if !link.Resolved || link.TargetPath == "" {
				continue
			}
			if seenTargets[link.TargetPath] {
				continue
			}
			seenTargets[link.TargetPath] = true
			back[link.TargetPath] = append(back[link.TargetPath], source)
		}
	}
	return back
}

// relInsideVault returns the vault-relative forward-slash path, or an error
// if absPath escapes vaultRoot. Symlinks anywhere along absPath are resolved
// before the containment check so a symlink whose target lives outside
// vaultRoot is rejected — Phase 6 hard constraint #6 (no indirect read of
// vault-external files via the link graph).
func relInsideVault(vaultRoot, absPath string) (string, error) {
	rel, err := filepath.Rel(vaultRoot, absPath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		return "", nil
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", errors.New("path escapes vault root")
	}
	// Symlink-aware containment check. EvalSymlinks resolves every component;
	// if the file is missing (race with delete) we accept the lexical result
	// because the next Stat() will surface the not-exists case cleanly.
	resolvedRoot, rootErr := filepath.EvalSymlinks(vaultRoot)
	if rootErr != nil {
		resolvedRoot = filepath.Clean(vaultRoot)
	}
	resolved, evalErr := filepath.EvalSymlinks(absPath)
	if evalErr != nil {
		// Most often os.ErrNotExist (transient between fsnotify and stat).
		// Fall through to the lexical result.
		return rel, nil
	}
	resolvedAbs, absErr := filepath.Abs(resolved)
	if absErr != nil {
		resolvedAbs = resolved
	}
	rootWithSep := strings.TrimRight(resolvedRoot, string(os.PathSeparator)) + string(os.PathSeparator)
	if resolvedAbs != resolvedRoot && !strings.HasPrefix(resolvedAbs, rootWithSep) {
		return "", errors.New("path escapes vault root via symlink")
	}
	return rel, nil
}

// shouldSkipDir matches the vault's hard-skip directories (forbidden prefixes
// per Phase 6 hard constraint #9, plus common noise). Empty rel ("." → "")
// means vault root itself, never skipped.
func shouldSkipDir(rel string) bool {
	if rel == "" {
		return false
	}
	base := filepath.Base(rel)
	switch base {
	case ".obsidian", ".git", ".trash", "node_modules":
		return true
	}
	if strings.HasPrefix(base, ".") && base != "." && base != ".." {
		// Skip any other hidden directory.
		return true
	}
	return false
}

// shouldIgnoreEvent filters fsnotify events we never want to react to (chmod,
// hidden file noise, exact-match forbidden prefixes).
func shouldIgnoreEvent(ev fsnotify.Event) bool {
	if ev.Op == fsnotify.Chmod {
		return true
	}
	base := filepath.Base(ev.Name)
	if base == ".DS_Store" {
		return true
	}
	return false
}

func normalizeRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(filepath.ToSlash(root))
		if root == "" {
			continue
		}
		root = strings.TrimRight(root, "/") + "/"
		out = append(out, root)
	}
	return out
}

func normalizeNotePath(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(path)), "/")
}

func containsString(list []string, needle string) bool {
	for _, s := range list {
		if s == needle {
			return true
		}
	}
	return false
}

func removeString(list []string, needle string) []string {
	out := list[:0]
	for _, s := range list {
		if s != needle {
			out = append(out, s)
		}
	}
	return out
}
