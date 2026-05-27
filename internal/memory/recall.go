package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
)

// Recall source labels surfaced to callers. Stable strings; appear in
// trace + tool output.
const (
	RecallSourceTagMatch    = "tag_match"
	RecallSourceTextMatch   = "text_match"
	RecallSourceRelatedLink = "related_link"
)

// TagMode constants for RecallRequest.TagMode.
const (
	TagModeAny = "any"
	TagModeAll = "all"
)

// Source-weight constants per Phase 8 spec ("评分（v1 简单加权）"). Kept as
// package-level constants so the formulas stay explicit and reviewable.
const (
	sourceWeightTagMatch    = 1.0
	sourceWeightTextMatch   = 0.6
	sourceWeightRelatedLink = 0.4

	relatedHopDecay = 0.7

	// textHitBaseDecay maps a text hit's rank in the result list to a
	// monotone base score in (0, 1]. Hit 0 → 1.0, hit 1 → 0.5, hit 2 →
	// 0.333…, etc. Picked over "all hits = 1.0" so a single-pass query
	// still has a stable ordering when only Pass 2 contributes.
	textHitBaseDecay = 1.0
)

// DefaultArchiveScope is the subtree Recall excludes when the caller's
// RecallRequest.ScopeDirs is empty. Spec: "默认全 vault 减 Archive/".
const DefaultArchiveScope = "Archive/"

// RecallRequest is the input contract for memory.Recall. At least one of
// Query / Tags must be non-empty; both empty is rejected.
type RecallRequest struct {
	Query         string     // optional; text query
	Tags          []string   // optional; tag anchors (e.g. ["skill/golang"])
	TagMode       string     // "any" | "all"; default "any" per decision #5
	ScopeDirs     []string   // optional; empty → full vault minus Archive/
	Since         *time.Time // optional; only notes touched after this time
	Limit         int        // caller MUST set; core does not bottom-cap
	ExpandRelated bool       // false to skip Pass 3
	CallerKind    string     // "agent" | "enrich" | "router" | "scheduler"
}

// RecallItem is one hit returned by Recall. Items are sorted by Score desc;
// ties broken by Path asc.
type RecallItem struct {
	Path      string   `json:"path"`
	Score     float64  `json:"score"`
	Source    string   `json:"source"`
	Excerpt   string   `json:"excerpt,omitempty"`
	MatchedOn []string `json:"matched_on,omitempty"`
}

// RecallResult is the envelope for one Recall response. Degraded is true
// when any pass was skipped due to an unready dependency (sqlite cache,
// link index). DegradedReasons explains which.
type RecallResult struct {
	Items           []RecallItem `json:"items"`
	Degraded        bool         `json:"degraded,omitempty"`
	DegradedReasons []string     `json:"degraded_reasons,omitempty"`
}

// Recall is the unified read entry point. Layered passes:
//
//  1. tag_match — sqlite memory_tag_index lookup keyed by req.Tags. Requires
//     Service.Ready(); if not, this pass is skipped and the result is marked
//     degraded.
//  2. text_match — Service.textSearch (default fsTextSearcher) literal
//     search. Skipped if Query is empty or textSearch is nil.
//  3. related_link — LinkGraph one-hop diffusion seeded by Pass 1/2 hits.
//     Skipped (and marked degraded) when linkGraph is nil or its index has
//     not finished its initial build.
//
// Results are then ScopeDirs / Since filtered, sorted by Score desc, and
// truncated to req.Limit. A memory_recalls trace row is written at the end
// regardless of degraded state.
func (s *Service) Recall(ctx context.Context, req RecallRequest) (RecallResult, error) {
	start := s.now()
	if strings.TrimSpace(req.Query) == "" && len(nonEmpty(req.Tags)) == 0 {
		return RecallResult{}, errors.New("memory.Recall: Query or Tags is required")
	}
	tagMode := req.TagMode
	if tagMode == "" {
		tagMode = TagModeAny
	}
	if tagMode != TagModeAny && tagMode != TagModeAll {
		return RecallResult{}, fmt.Errorf("memory.Recall: invalid tag_mode %q", tagMode)
	}
	tags := nonEmpty(req.Tags)

	var degradedReasons []string

	// Per-path aggregation. tagMatched[p] = tags that hit p (for "all" mode
	// AND MatchedOn aggregation).
	items := map[string]*RecallItem{}
	tagMatched := map[string][]string{}

	tagIndexReady := s.Ready()

	// ---- Pass 1: tag_match ----
	if len(tags) > 0 {
		if tagIndexReady {
			notesByTag, err := s.store.NotesForTags(tags)
			if err != nil {
				return RecallResult{}, fmt.Errorf("memory.Recall: tag index query: %w", err)
			}
			hitCount := map[string]int{}
			for tag, paths := range notesByTag {
				for _, p := range paths {
					hitCount[p]++
					tagMatched[p] = append(tagMatched[p], tag)
				}
			}
			for path, count := range hitCount {
				if tagMode == TagModeAll && count != len(tags) {
					continue
				}
				base := float64(count) / float64(len(tags))
				items[path] = &RecallItem{
					Path:      path,
					Score:     base * sourceWeightTagMatch,
					Source:    RecallSourceTagMatch,
					MatchedOn: append([]string{}, tagMatched[path]...),
				}
			}
		} else {
			degradedReasons = append(degradedReasons, "tag_index_not_ready")
		}
	}

	// ---- Pass 2: text_match ----
	if strings.TrimSpace(req.Query) != "" {
		if s.textSearch == nil {
			degradedReasons = append(degradedReasons, "text_search_unavailable")
		} else {
			cap := req.Limit
			if cap <= 0 || cap > DefaultTextSearchHits {
				cap = DefaultTextSearchHits
			}
			hits, err := s.textSearch.Search(ctx, req.Query, req.ScopeDirs, cap)
			if err != nil {
				degradedReasons = append(degradedReasons, "text_search_error")
			}
			for i, hit := range hits {
				rankBase := textHitBaseDecay / float64(i+1)
				score := rankBase * sourceWeightTextMatch
				if existing, ok := items[hit.Path]; ok {
					if existing.Excerpt == "" {
						existing.Excerpt = hit.Line
					}
					if score > existing.Score {
						existing.Score = score
					}
					existing.MatchedOn = append(existing.MatchedOn, "text:"+truncate(hit.Line, 60))
					continue
				}
				items[hit.Path] = &RecallItem{
					Path:      hit.Path,
					Score:     score,
					Source:    RecallSourceTextMatch,
					Excerpt:   hit.Line,
					MatchedOn: []string{"text:" + truncate(hit.Line, 60)},
				}
			}
		}
	}

	// ---- Pass 3: related_link ----
	if req.ExpandRelated {
		switch {
		case s.linkGraph == nil:
			degradedReasons = append(degradedReasons, "link_graph_unavailable")
		case !s.linkGraph.Ready():
			degradedReasons = append(degradedReasons, "link_graph_not_ready")
		default:
			s.expandRelated(items)
		}
	}

	// ---- Filtering ----
	filterScope(items, req.ScopeDirs)
	if req.Since != nil {
		s.filterSince(items, *req.Since)
	}

	// ---- Sort + limit ----
	out := make([]RecallItem, 0, len(items))
	for _, item := range items {
		out = append(out, *item)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Path < out[j].Path
	})
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}

	degraded := len(degradedReasons) > 0

	// ---- Trace ----
	if s.store != nil {
		latency := s.now().Sub(start)
		tracerErr := s.store.InsertRecallTrace(
			model.NewID("rec"),
			req.CallerKind,
			truncate(strings.TrimSpace(req.Query), 200),
			strings.Join(tags, ","),
			tagMode,
			len(out),
			int(latency.Milliseconds()),
			degraded,
			start,
		)
		_ = tracerErr // trace errors must not fail Recall
	}

	return RecallResult{
		Items:           out,
		Degraded:        degraded,
		DegradedReasons: degradedReasons,
	}, nil
}

// expandRelated walks one hop from every existing item via LinkGraph in
// both directions (frontmatter related: src→dst and dst→src). New paths
// become RecallSourceRelatedLink items with seed-decayed scores. Paths
// already present from Pass 1/2 keep their primary source untouched.
func (s *Service) expandRelated(items map[string]*RecallItem) {
	type seed struct {
		path  string
		score float64
	}
	seeds := make([]seed, 0, len(items))
	for path, item := range items {
		seeds = append(seeds, seed{path: path, score: item.Score})
	}
	for _, sd := range seeds {
		neighbours := append(s.linkGraph.OutlinksRelated(sd.path),
			s.linkGraph.BacklinksRelated(sd.path)...)
		seen := map[string]bool{}
		for _, n := range neighbours {
			if seen[n] || n == sd.path {
				continue
			}
			seen[n] = true
			if _, exists := items[n]; exists {
				// already a primary hit — don't downgrade to related_link
				continue
			}
			score := sd.score * relatedHopDecay * sourceWeightRelatedLink
			items[n] = &RecallItem{
				Path:      n,
				Score:     score,
				Source:    RecallSourceRelatedLink,
				MatchedOn: []string{"via:" + sd.path},
			}
		}
	}
}

// filterScope drops items whose Path falls outside the caller's ScopeDirs.
// When ScopeDirs is empty, the v1 default is "full vault minus Archive/".
// Forward-slash prefix matching is used; both "Knowledge" and "Knowledge/"
// are accepted as scope entries.
func filterScope(items map[string]*RecallItem, scopeDirs []string) {
	if len(scopeDirs) == 0 {
		for path := range items {
			if strings.HasPrefix(path, DefaultArchiveScope) {
				delete(items, path)
			}
		}
		return
	}
	cleanedScope := make([]string, 0, len(scopeDirs))
	for _, dir := range scopeDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		cleanedScope = append(cleanedScope, strings.TrimSuffix(dir, "/")+"/")
	}
	for path := range items {
		keep := false
		for _, dir := range cleanedScope {
			if strings.HasPrefix(path+"/", dir) {
				keep = true
				break
			}
		}
		if !keep {
			delete(items, path)
		}
	}
}

// filterSince drops items whose note mtime is before since. Best-effort:
// stat errors leave the item in place (we never silently drop a hit
// because the filesystem flaked).
func (s *Service) filterSince(items map[string]*RecallItem, since time.Time) {
	for path := range items {
		abs := filepath.Join(s.vaultRoot, filepath.FromSlash(path))
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.ModTime().Before(since) {
			delete(items, path)
		}
	}
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
