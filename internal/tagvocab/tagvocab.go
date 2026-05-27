// Package tagvocab is the Phase 7 minimum viable known-tag vocabulary service.
//
// Scope: derive the set of vault-native `topic/*` / `skill/*` tags from
// frontmatter in Knowledge/, Interview/, Life/, and expose two reads —
// KnownTags(prefix) and HasTag(tag). The enrich agent uses it to constrain
// what the LLM is allowed to write into a Raw's frontmatter `tags`.
//
// Per Phase 7 decision 5/6 the vault is the only truth source: this package
// never invents tags and never writes to the vault. The on-disk truth lives
// in the vault, and `memory_known_tags` is a sqlite cache.
//
// Phase 8 supersedes this package by migrating it into `internal/memory/`
// alongside Recall(); the API shape (KnownTags / HasTag / Rescan / Record)
// is the minimum surface that Phase 8 needs to keep stable.
package tagvocab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/scarletmu/openwhisker/internal/storage"
)

// Service is the read+maintenance facade. Construct one per daemon process.
type Service struct {
	store      *storage.Store
	vaultRoot  string
	scanDirs   []string
	prefixes   []string // tag prefixes considered "known vocabulary" (topic/, skill/)

	mu      sync.RWMutex
	cache   map[string]struct{} // lower-cased tag set, populated by Rescan / Record
	ready   bool
	readyCh chan struct{}
}

// DefaultScanDirs are the Phase 7 vault subtrees that contribute to the
// vocabulary. Raw/ and Archive/ and Meta/ are deliberately excluded — raw
// captures are not authoritative tag sources, archived material should not
// resurrect tags, and Meta/ is rules/templates.
var DefaultScanDirs = []string{"Knowledge", "Interview", "Life"}

// DefaultPrefixes restricts the vocabulary to the two prefixes Phase 7
// enrich is allowed to write. Other prefixes (type/, status/, raw/,
// interview/) have their own rules and are not part of the "controlled
// vocabulary that enrich may pick from".
var DefaultPrefixes = []string{"topic/", "skill/"}

// NewService constructs a Service. The cache starts empty and is ready=false
// until RescanAll completes.
func NewService(store *storage.Store, vaultRoot string, scanDirs, prefixes []string) *Service {
	if len(scanDirs) == 0 {
		scanDirs = DefaultScanDirs
	}
	if len(prefixes) == 0 {
		prefixes = DefaultPrefixes
	}
	return &Service{
		store:     store,
		vaultRoot: vaultRoot,
		scanDirs:  scanDirs,
		prefixes:  prefixes,
		cache:     map[string]struct{}{},
		readyCh:   make(chan struct{}),
	}
}

// RescanAll walks scanDirs under vaultRoot and rebuilds the in-memory cache
// from frontmatter `tags`. It also upserts every observed tag into the
// sqlite cache so cold-start daemons can serve KnownTags before the rescan
// finishes (via a hot cache backfill at construction time). The previously-
// persisted tags are first loaded so "only-increment" semantics survive
// across restarts.
func (s *Service) RescanAll(ctx context.Context) error {
	persisted, err := s.loadFromStore()
	if err != nil {
		return fmt.Errorf("tagvocab: load persisted tags: %w", err)
	}
	now := time.Now().UTC()
	observed, err := s.walkVault(ctx)
	if err != nil {
		return fmt.Errorf("tagvocab: walk vault: %w", err)
	}
	merged := make(map[string]struct{}, len(persisted)+len(observed))
	for tag := range persisted {
		merged[tag] = struct{}{}
	}
	for tag := range observed {
		merged[tag] = struct{}{}
		if err := s.store.UpsertKnownTag(tag, now); err != nil {
			return fmt.Errorf("tagvocab: persist tag %q: %w", tag, err)
		}
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

// Ready reports whether RescanAll has completed at least once.
func (s *Service) Ready() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready
}

// WaitReady blocks until RescanAll has completed or ctx is cancelled. Enrich
// uses this before each job so it never runs against a stale vocabulary.
func (s *Service) WaitReady(ctx context.Context) error {
	s.mu.RLock()
	if s.ready {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()
	select {
	case <-s.readyCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// KnownTags returns the cached vocabulary filtered by prefix, sorted. An
// empty prefix returns every cached tag.
func (s *Service) KnownTags(prefix string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.cache))
	for tag := range s.cache {
		if prefix == "" || strings.HasPrefix(tag, prefix) {
			out = append(out, tag)
		}
	}
	sortStrings(out)
	return out
}

// HasTag is the hot-path check used by policy field_guard. Case-insensitive
// match against the cached set.
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

// Record is the executor-side hook: after a successful Apply that touched
// frontmatter `tags`, the new tag set goes through here so the in-memory and
// sqlite caches stay in sync without waiting for the next daemon restart.
func (s *Service) Record(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	now := time.Now().UTC()
	s.mu.Lock()
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
	s.mu.Unlock()
	for _, tag := range added {
		if err := s.store.UpsertKnownTag(tag, now); err != nil {
			return fmt.Errorf("tagvocab: persist new tag %q: %w", tag, err)
		}
	}
	return nil
}

// loadFromStore seeds the cache from sqlite before a rescan, so the
// only-increment guarantee survives daemon restarts even when the vault is
// rescan-incomplete.
func (s *Service) loadFromStore() (map[string]struct{}, error) {
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

// walkVault returns the set of `topic/*` / `skill/*` tags observed in
// frontmatter under scanDirs. Skips hidden directories, symlinks, anything
// outside .md files, and frontmatter-less notes.
func (s *Service) walkVault(ctx context.Context) (map[string]struct{}, error) {
	if strings.TrimSpace(s.vaultRoot) == "" {
		return nil, errors.New("vault root is required")
	}
	out := map[string]struct{}{}
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
			for _, tag := range ExtractFrontmatterTags(string(data)) {
				clean := s.normalize(tag)
				if clean == "" {
					continue
				}
				out[clean] = struct{}{}
			}
			return nil
		})
		if walkErr != nil {
			return nil, walkErr
		}
	}
	return out, nil
}

// normalize keeps a tag only if it matches one of the configured controlled
// prefixes. Whitespace is trimmed; case is folded to lowercase so the
// vocabulary is case-insensitive.
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

// ExtractFrontmatterTags parses the YAML frontmatter and returns every
// value under the top-level `tags:` key. Supports both flow style
// `tags: [a, b]` and block style `tags:\n  - a\n  - b`. Unknown formats
// produce nothing rather than erroring — the walker tolerates frontmatter
// noise across the vault.
func ExtractFrontmatterTags(content string) []string {
	content = strings.TrimLeft(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") {
		return nil
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	fm := rest[:end]
	lines := strings.Split(fm, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "tags:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trim, "tags:"))
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			inner := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			for _, item := range strings.Split(inner, ",") {
				if v := cleanScalar(item); v != "" {
					out = append(out, v)
				}
			}
			continue
		}
		if value != "" {
			if v := cleanScalar(value); v != "" {
				out = append(out, v)
			}
			continue
		}
		// Block style — walk subsequent lines that start with "  - ".
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			indent := len(next) - len(strings.TrimLeft(next, " \t"))
			trimmed := strings.TrimSpace(next)
			if indent == 0 && trimmed != "" {
				break
			}
			if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
				if trimmed == "" {
					continue
				}
				break
			}
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if v := cleanScalar(item); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func cleanScalar(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return strings.TrimSpace(s)
}

func sortStrings(values []string) {
	// Tiny insertion sort — vocabulary slices are small (typically <500
	// entries) and importing sort would be the only reason to depend on it
	// from this hot read path.
	for i := 1; i < len(values); i++ {
		j := i
		for j > 0 && values[j-1] > values[j] {
			values[j-1], values[j] = values[j], values[j-1]
			j--
		}
	}
}
