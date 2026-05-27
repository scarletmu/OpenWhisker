package memory

import (
	"context"
	"testing"
	"time"
)

// stubLinkGraph lets recall tests inject a predictable adjacency map
// without standing up an fsnotify-backed linkindex.Index.
type stubLinkGraph struct {
	ready bool
	out   map[string][]string
	back  map[string][]string
}

func (s stubLinkGraph) Ready() bool                                  { return s.ready }
func (s stubLinkGraph) OutlinksRelated(path string) []string         { return s.out[path] }
func (s stubLinkGraph) BacklinksRelated(path string) []string        { return s.back[path] }

// stubTextSearcher returns canned hits when query matches one of its
// rigged needles. Implements TextSearcher.
type stubTextSearcher struct {
	hits map[string][]TextHit
	err  error
}

func (s stubTextSearcher) Search(ctx context.Context, query string, scope []string, maxHits int) ([]TextHit, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := s.hits[query]
	if maxHits > 0 && len(out) > maxHits {
		out = out[:maxHits]
	}
	return out, nil
}

func newServiceWithTags(t *testing.T, tagsByNote map[string][]string) *Service {
	t.Helper()
	vault := t.TempDir()
	for path, tags := range tagsByNote {
		body := "---\ntags:\n"
		for _, tag := range tags {
			body += "  - " + tag + "\n"
		}
		body += "---\nbody\n"
		writeFile(t, vault, path, body)
	}
	svc, err := NewService(Config{Store: openStore(t), VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	return svc
}

func TestRecallRejectsEmptyQueryAndTags(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{})
	_, err := svc.Recall(context.Background(), RecallRequest{Limit: 10, CallerKind: "agent"})
	if err == nil {
		t.Fatal("expected error when both Query and Tags are empty")
	}
}

func TestRecallTagsOnly(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/a.md": {"topic/golang"},
		"Knowledge/b.md": {"topic/golang", "skill/concurrency"},
		"Knowledge/c.md": {"topic/python"},
	})
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang"},
		Limit:         10,
		ExpandRelated: false,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d: %+v", len(result.Items), result.Items)
	}
	for _, item := range result.Items {
		if item.Source != RecallSourceTagMatch {
			t.Errorf("item %s source = %s, want tag_match", item.Path, item.Source)
		}
	}
}

func TestRecallTagModeAll(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/a.md": {"topic/golang"},
		"Knowledge/b.md": {"topic/golang", "skill/concurrency"},
	})
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang", "skill/concurrency"},
		TagMode:       TagModeAll,
		Limit:         10,
		ExpandRelated: false,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Path != "Knowledge/b.md" {
		t.Fatalf("TagModeAll wrong items: %+v", result.Items)
	}
}

func TestRecallQueryOnlyWithTextSearch(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/a.md": {"topic/golang"},
	})
	svc.textSearch = stubTextSearcher{hits: map[string][]TextHit{
		"goroutine": {{Path: "Knowledge/a.md", Line: "discusses goroutines"}},
	}}
	result, err := svc.Recall(context.Background(), RecallRequest{
		Query:         "goroutine",
		Limit:         10,
		ExpandRelated: false,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Source != RecallSourceTextMatch {
		t.Fatalf("text-only recall wrong: %+v", result.Items)
	}
	if result.Items[0].Excerpt == "" {
		t.Errorf("text_match excerpt should carry the matched line")
	}
}

func TestRecallExpandRelatedBidirectional(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/seed.md": {"topic/golang"},
	})
	svc.linkGraph = stubLinkGraph{
		ready: true,
		out:   map[string][]string{"Knowledge/seed.md": {"Knowledge/forward.md"}},
		back:  map[string][]string{"Knowledge/seed.md": {"Knowledge/backward.md"}},
	}
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang"},
		Limit:         10,
		ExpandRelated: true,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	sources := map[string]string{}
	for _, item := range result.Items {
		sources[item.Path] = item.Source
	}
	if sources["Knowledge/seed.md"] != RecallSourceTagMatch {
		t.Errorf("seed should remain tag_match, got %v", sources)
	}
	if sources["Knowledge/forward.md"] != RecallSourceRelatedLink {
		t.Errorf("forward neighbour missing related_link source: %v", sources)
	}
	if sources["Knowledge/backward.md"] != RecallSourceRelatedLink {
		t.Errorf("backward neighbour missing related_link source: %v", sources)
	}
}

func TestRecallExpandRelatedDisabled(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/seed.md": {"topic/golang"},
	})
	svc.linkGraph = stubLinkGraph{
		ready: true,
		out:   map[string][]string{"Knowledge/seed.md": {"Knowledge/neighbour.md"}},
	}
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang"},
		Limit:         10,
		ExpandRelated: false,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, item := range result.Items {
		if item.Path == "Knowledge/neighbour.md" {
			t.Errorf("ExpandRelated=false should not surface neighbours")
		}
	}
}

func TestRecallDegradedWhenLinkGraphNotReady(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/seed.md": {"topic/golang"},
	})
	svc.linkGraph = stubLinkGraph{ready: false}
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang"},
		Limit:         10,
		ExpandRelated: true,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !result.Degraded {
		t.Errorf("expected Degraded=true when link graph not ready")
	}
	foundReason := false
	for _, reason := range result.DegradedReasons {
		if reason == "link_graph_not_ready" {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("DegradedReasons missing link_graph_not_ready: %v", result.DegradedReasons)
	}
}

func TestRecallExcludesArchiveByDefault(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/a.md": {"topic/golang"},
	})
	// Inject an Archive/ row directly so we can verify default filtering
	// without depending on the rescan's directory exclusion.
	if err := svc.store.ReplaceTagIndexForNote("Archive/old.md", []string{"topic/golang"}, time.Now().UTC()); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang"},
		Limit:         10,
		ExpandRelated: false,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, item := range result.Items {
		if item.Path == "Archive/old.md" {
			t.Errorf("Archive/ should be excluded by default, got %+v", item)
		}
	}
}

func TestRecallScopeDirsFilter(t *testing.T) {
	svc := newServiceWithTags(t, map[string][]string{
		"Knowledge/a.md": {"topic/golang"},
		"Interview/x.md": {"topic/golang"},
	})
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:          []string{"topic/golang"},
		ScopeDirs:     []string{"Knowledge/"},
		Limit:         10,
		ExpandRelated: false,
		CallerKind:    "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Path != "Knowledge/a.md" {
		t.Fatalf("ScopeDirs Knowledge/ should restrict: %+v", result.Items)
	}
}
