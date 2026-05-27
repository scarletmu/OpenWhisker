package tagvocab

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestExtractFrontmatterTagsBlockStyle(t *testing.T) {
	doc := "---\ntitle: x\ntags:\n  - topic/database\n  - skill/golang\n  - type/knowledge\n---\nbody\n"
	got := ExtractFrontmatterTags(doc)
	want := []string{"topic/database", "skill/golang", "type/knowledge"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("got[%d]=%q want %q", i, got[i], v)
		}
	}
}

func TestExtractFrontmatterTagsFlowStyle(t *testing.T) {
	doc := "---\ntags: [topic/system-design, \"skill/python\", 'topic/observability']\n---\n"
	got := ExtractFrontmatterTags(doc)
	want := []string{"topic/system-design", "skill/python", "topic/observability"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d (%v)", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("got[%d]=%q want %q", i, got[i], v)
		}
	}
}

func TestExtractFrontmatterTagsNoFrontmatter(t *testing.T) {
	if got := ExtractFrontmatterTags("no fm here\n"); len(got) != 0 {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestRescanAndQuery(t *testing.T) {
	vault := t.TempDir()
	mustWrite(t, vault, "Knowledge/Go/concurrency.md",
		"---\ntags:\n  - topic/concurrency\n  - skill/golang\n---\nbody\n")
	mustWrite(t, vault, "Interview/projects/x.md",
		"---\ntags: [topic/system-design, interview/behavior]\n---\n")
	mustWrite(t, vault, "Life/plan.md",
		"---\ntags:\n  - topic/travel\n---\n")
	mustWrite(t, vault, "Raw/Inbox/job-1.md",
		"---\ntags:\n  - topic/should-not-be-included\n---\n")
	mustWrite(t, vault, "Archive/old.md",
		"---\ntags:\n  - topic/archived\n---\n")
	mustWrite(t, vault, "Knowledge/.hidden/x.md",
		"---\ntags: [topic/hidden]\n---\n")

	store, db := openStore(t)
	defer store.Close()
	_ = db

	svc := NewService(store, vault, nil, nil)
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if !svc.Ready() {
		t.Fatal("Ready=false after rescan")
	}

	topic := svc.KnownTags("topic/")
	wantTopics := []string{"topic/concurrency", "topic/system-design", "topic/travel"}
	if !equalSlices(topic, wantTopics) {
		t.Errorf("topic prefix got %v want %v", topic, wantTopics)
	}
	skill := svc.KnownTags("skill/")
	if !equalSlices(skill, []string{"skill/golang"}) {
		t.Errorf("skill prefix got %v want %v", skill, []string{"skill/golang"})
	}
	if svc.HasTag("topic/should-not-be-included") {
		t.Error("Raw/ contributed a tag; vocab is leaking")
	}
	if svc.HasTag("topic/archived") {
		t.Error("Archive/ contributed a tag; vocab is leaking")
	}
	if svc.HasTag("interview/behavior") {
		t.Error("non-topic/skill prefix made it into vocab")
	}
	if svc.HasTag("type/knowledge") {
		t.Error("type/ prefix is not in DefaultPrefixes; vocab should exclude")
	}
}

func TestRecordAddsTagAndPersists(t *testing.T) {
	vault := t.TempDir()
	store, _ := openStore(t)
	defer store.Close()

	svc := NewService(store, vault, nil, nil)
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if svc.HasTag("topic/new-thing") {
		t.Fatal("vocab leaked")
	}
	if err := svc.Record(context.Background(), []string{"topic/new-thing", "skill/rust", "status/needs-review"}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if !svc.HasTag("topic/new-thing") {
		t.Error("Record did not add topic/new-thing")
	}
	if !svc.HasTag("skill/rust") {
		t.Error("Record did not add skill/rust")
	}
	if svc.HasTag("status/needs-review") {
		t.Error("Record let through a non-controlled-prefix tag")
	}

	tags, err := store.KnownTags("topic/")
	if err != nil {
		t.Fatalf("store query: %v", err)
	}
	if !containsString(tags, "topic/new-thing") {
		t.Errorf("Record did not persist topic/new-thing to sqlite, got %v", tags)
	}
}

func TestRescanIsCumulative(t *testing.T) {
	// User removes a tag from the vault between rescans; we expect the cache
	// to keep it (only-increment semantics per Phase 7 spec).
	vault := t.TempDir()
	mustWrite(t, vault, "Knowledge/x.md", "---\ntags: [topic/old]\n---\n")
	store, _ := openStore(t)
	defer store.Close()

	svc := NewService(store, vault, nil, nil)
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan1: %v", err)
	}
	if !svc.HasTag("topic/old") {
		t.Fatal("first rescan missed topic/old")
	}
	mustWrite(t, vault, "Knowledge/x.md", "---\ntags: [topic/new]\n---\n")
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan2: %v", err)
	}
	if !svc.HasTag("topic/old") {
		t.Error("topic/old should survive second rescan (only-increment)")
	}
	if !svc.HasTag("topic/new") {
		t.Error("topic/new should be added by second rescan")
	}
}

// ---- helpers ----

func mustWrite(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func openStore(t *testing.T) (*storage.Store, string) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "store.db")
	st, err := storage.Open(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return st, db
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
