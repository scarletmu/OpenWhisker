// Package memory is the shared Phase 8 memory recall service. It exposes:
//
//   - KnownTags(prefix) / HasTag(tag) — vault-native controlled vocabulary
//     read by the Phase 7 enrich agent and future writers.
//   - Recall(req) — tag / text / one-hop related召回 used by the
//     `recall_memory` Phase 6 tool and future Go-side callers.
//
// Memory does not own a new truth source: frontmatter is the source, sqlite
// (`memory_tag_index` / `memory_known_tags` / `memory_recalls`) is a derived
// cache, and the `related` adjacency graph is borrowed from
// `internal/vault/linkindex`. See `docs/phases/phase-8-memory-recall.md`.
//
// This file owns the Service struct + the known-tag side (the Phase 7
// `internal/tagvocab/` package migrated in per decision #1). `recall.go`,
// `linkgraph.go`, and `textsearch.go` hold the rest.
package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scarletmu/openwhisker/internal/storage"
)

// DefaultScanDirs are the vault subtrees that contribute to the known-tag
// vocabulary AND the tag reverse index. Raw/ and Archive/ and Meta/ are
// deliberately excluded: raw captures are not authoritative tag sources,
// archived material should not resurrect tags, and Meta/ is rules/templates.
var DefaultScanDirs = []string{"Knowledge", "Interview", "Life"}

// DefaultPrefixes restricts the vocabulary to the two prefixes Phase 7
// enrich is allowed to write. Other prefixes (type/, status/, raw/,
// interview/) have their own rules and are not part of the controlled
// vocabulary memory consumers pick from.
var DefaultPrefixes = []string{"topic/", "skill/"}

// Service is the per-process facade. Construct one with NewService, then
// call RescanAll once at startup. After Ready() returns true, KnownTags /
// HasTag / Recall / Record / OnApplied are all safe to call concurrently.
type Service struct {
	store     *storage.Store
	vaultRoot string
	scanDirs  []string
	prefixes  []string

	// linkGraph is optional. Recall Pass 3 (one-hop related diffusion)
	// requires it; nil falls back to a degraded result. Daemon wires this
	// to a LinkIndexAdapter; tests pass a fake.
	linkGraph LinkGraph

	// textSearch is optional. Recall Pass 2 (text query) requires it; nil
	// makes a query-only Recall return no text hits. Daemon wires this to
	// fsTextSearcher; tests pass a fake.
	textSearch TextSearcher

	now func() time.Time

	mu      sync.RWMutex
	cache   map[string]struct{} // lower-cased known-tag set
	ready   bool
	readyCh chan struct{}
}

// Config bundles construction parameters so callers can extend memory's
// surface area without breaking the constructor signature each time.
type Config struct {
	Store      *storage.Store
	VaultRoot  string
	ScanDirs   []string // defaults to DefaultScanDirs when nil/empty
	Prefixes   []string // defaults to DefaultPrefixes when nil/empty
	LinkGraph  LinkGraph
	TextSearch TextSearcher
	Now        func() time.Time
}

// NewService validates cfg and returns a Service whose cache is empty and
// Ready() == false. Callers (daemon main, tests) own the RescanAll lifecycle.
func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("memory: Store is required")
	}
	if strings.TrimSpace(cfg.VaultRoot) == "" {
		return nil, errors.New("memory: VaultRoot is required")
	}
	scanDirs := cfg.ScanDirs
	if len(scanDirs) == 0 {
		scanDirs = DefaultScanDirs
	}
	prefixes := cfg.Prefixes
	if len(prefixes) == 0 {
		prefixes = DefaultPrefixes
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{
		store:      cfg.Store,
		vaultRoot:  cfg.VaultRoot,
		scanDirs:   scanDirs,
		prefixes:   prefixes,
		linkGraph:  cfg.LinkGraph,
		textSearch: cfg.TextSearch,
		now:        now,
		cache:      map[string]struct{}{},
		readyCh:    make(chan struct{}),
	}, nil
}

// SetLinkGraph attaches a LinkGraph after construction. Used by the daemon
// when the linkindex finishes its async build and wants to start serving
// Recall Pass 3. Safe to call concurrently with Recall — readers pick up the
// new graph on the next call.
func (s *Service) SetLinkGraph(g LinkGraph) {
	s.mu.Lock()
	s.linkGraph = g
	s.mu.Unlock()
}

// RescanAll walks scanDirs under vaultRoot once and rebuilds:
//
//   - memory_tag_index: per-note tag rows replaced via ReplaceTagIndexForNote
//     so deleted/renamed tags inside a still-existing note disappear; rows
//     for *deleted* notes survive until the next `openwhisker memory reindex`
//     (which clears the table first). This matches Phase 8 spec semantics:
//     incremental Apply hook handles live edits; full clean-up is opt-in.
//   - memory_known_tags: only-increment per decision #6; the persisted set is
//     loaded first so prior tags survive even when the vault no longer shows
//     them.
//
// After a successful rescan Ready() flips to true. WaitReady() unblocks.
func (s *Service) RescanAll(ctx context.Context) error {
	persisted, err := s.loadKnownTagsFromStore()
	if err != nil {
		return fmt.Errorf("memory: load persisted tags: %w", err)
	}
	now := s.now()
	merged := make(map[string]struct{}, len(persisted))
	for tag := range persisted {
		merged[tag] = struct{}{}
	}
	walkErr := s.walkVault(ctx, func(notePath string, tags []string) error {
		controlled := s.filterControlled(tags)
		if err := s.store.ReplaceTagIndexForNote(notePath, controlled, now); err != nil {
			return fmt.Errorf("replace tag index for %s: %w", notePath, err)
		}
		for _, tag := range controlled {
			merged[tag] = struct{}{}
			if err := s.store.UpsertKnownTag(tag, now); err != nil {
				return fmt.Errorf("upsert known tag %q: %w", tag, err)
			}
		}
		return nil
	})
	if walkErr != nil {
		return walkErr
	}
	s.mu.Lock()
	s.cache = merged
	wasReady := s.ready
	s.ready = true
	s.mu.Unlock()
	if !wasReady {
		close(s.readyCh)
	}
	return nil
}

// Reindex is the "wipe + rebuild" form used by `openwhisker memory reindex`.
// It clears both sqlite cache tables, resets the in-memory known-tag set,
// flips Ready to false, then runs RescanAll. The known_tags only-increment
// guarantee is intentionally broken here — reindex is the documented way to
// drop tags that no longer appear in the vault (decision #6).
func (s *Service) Reindex(ctx context.Context) error {
	if err := s.store.ClearTagIndex(); err != nil {
		return fmt.Errorf("clear tag index: %w", err)
	}
	if err := s.store.ClearKnownTags(); err != nil {
		return fmt.Errorf("clear known tags: %w", err)
	}
	s.mu.Lock()
	s.cache = map[string]struct{}{}
	s.ready = false
	s.readyCh = make(chan struct{})
	s.mu.Unlock()
	return s.RescanAll(ctx)
}

// Ready reports whether RescanAll has completed at least once on this
// Service instance.
func (s *Service) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// WaitReady blocks until RescanAll completes or ctx is cancelled. Enrich
// uses this so it never runs against a stale vocabulary.
func (s *Service) WaitReady(ctx context.Context) error {
	s.mu.RLock()
	ch := s.readyCh
	ready := s.ready
	s.mu.RUnlock()
	if ready {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// KnownTags returns the cached vocabulary filtered by prefix, sorted asc.
// Empty prefix returns every cached tag.
func (s *Service) KnownTags(prefix string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.cache))
	for tag := range s.cache {
		if prefix == "" || strings.HasPrefix(tag, prefix) {
			out = append(out, tag)
		}
	}
	sort.Strings(out)
	return out
}

// HasTag is the hot-path check used by enrich's policy field_guard.
// Case-insensitive match against the cached set.
func (s *Service) HasTag(tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	if tag == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.cache[tag]
	return ok
}

// Record is the hook called after a successful enrich Apply when the
// caller already knows the new tag set but has not refreshed the tag-index
// row. Updates the in-memory + sqlite known-tag cache.
//
// Most call sites should prefer OnApplied (which also touches the tag
// reverse index); Record is kept as the narrower entry the enrich service
// uses on Phase 7 paths that pre-date the executor hook.
func (s *Service) Record(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	now := s.now()
	added := s.addToCache(tags)
	for _, tag := range added {
		if err := s.store.UpsertKnownTag(tag, now); err != nil {
			return fmt.Errorf("upsert known tag %q: %w", tag, err)
		}
	}
	return nil
}

// OnRewriteNote implements executor.ApplyObserver. The executor invokes
// this after a successful rewrite_note Apply with the note's new
// content; we extract the new frontmatter tag set and refresh both the
// reverse index and the known-tag cache. Errors are swallowed because a
// side-effect cache observer must never fail an already-applied vault
// write (spec failure-semantics line).
func (s *Service) OnRewriteNote(ctx context.Context, notePath, newContent string) {
	tags := ExtractFrontmatterTags(newContent)
	_ = s.RecordForNote(ctx, notePath, tags)
}

// RecordForNote is the executor Apply hook entry point: replace this
// note's tag-index rows with the supplied set AND add any newly-seen tags
// to the known-tag cache. Best-effort errors are returned so callers can
// downgrade to warning logs rather than failing the whole apply.
func (s *Service) RecordForNote(ctx context.Context, notePath string, tags []string) error {
	controlled := s.filterControlled(tags)
	now := s.now()
	if err := s.store.ReplaceTagIndexForNote(notePath, controlled, now); err != nil {
		return fmt.Errorf("replace tag index for %s: %w", notePath, err)
	}
	added := s.addToCache(controlled)
	for _, tag := range added {
		if err := s.store.UpsertKnownTag(tag, now); err != nil {
			return fmt.Errorf("upsert known tag %q: %w", tag, err)
		}
	}
	return nil
}

// addToCache adds normalized + controlled-prefix tags to the in-memory
// known-tag cache and returns the subset that was actually new.
func (s *Service) addToCache(tags []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	added := make([]string, 0, len(tags))
	for _, tag := range tags {
		clean := s.normalize(tag)
		if clean == "" {
			continue
		}
		if _, ok := s.cache[clean]; ok {
			continue
		}
		s.cache[clean] = struct{}{}
		added = append(added, clean)
	}
	return added
}

// filterControlled keeps only tags whose normalized form passes the
// controlled-prefix check.
func (s *Service) filterControlled(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if clean := s.normalize(tag); clean != "" {
			out = append(out, clean)
		}
	}
	return out
}

func (s *Service) normalize(tag string) string {
	clean := strings.ToLower(strings.TrimSpace(tag))
	if clean == "" {
		return ""
	}
	for _, pfx := range s.prefixes {
		if strings.HasPrefix(clean, pfx) && len(clean) > len(pfx) {
			return clean
		}
	}
	return ""
}

func (s *Service) loadKnownTagsFromStore() (map[string]struct{}, error) {
	tags, err := s.store.KnownTags("")
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		clean := s.normalize(tag)
		if clean == "" {
			continue
		}
		out[clean] = struct{}{}
	}
	return out, nil
}

// walkVault enumerates .md files under each scanDir, parses frontmatter
// tags, and invokes visit(notePath, tags) per file. notePath is vault-
// relative forward-slash. Errors from visit short-circuit the walk.
func (s *Service) walkVault(ctx context.Context, visit func(notePath string, tags []string) error) error {
	if strings.TrimSpace(s.vaultRoot) == "" {
		return errors.New("memory: vault root is required")
	}
	for _, dir := range s.scanDirs {
		absRoot := filepath.Join(s.vaultRoot, filepath.FromSlash(dir))
		if _, statErr := os.Stat(absRoot); os.IsNotExist(statErr) {
			continue
		}
		walkErr := filepath.Walk(absRoot, func(p string, info os.FileInfo, walkErr error) error {
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
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			if !info.Mode().IsRegular() || !strings.EqualFold(filepath.Ext(p), ".md") {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			rel, relErr := filepath.Rel(s.vaultRoot, p)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			tags := ExtractFrontmatterTags(string(data))
			return visit(rel, tags)
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}
