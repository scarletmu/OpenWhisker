package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/storage"
)

func openStore(t *testing.T) *storage.Store {
	t.Helper()
	db := filepath.Join(t.TempDir(), "ow.db")
	st, err := storage.Open(db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func writeFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

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

func TestRescanAllBuildsKnownTagsAndIndex(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "Knowledge/Go/concurrency.md",
		"---\ntags:\n  - topic/concurrency\n  - skill/golang\n---\nbody\n")
	writeFile(t, vault, "Interview/projects/x.md",
		"---\ntags: [topic/system-design, interview/behavior]\n---\n")
	writeFile(t, vault, "Life/plan.md",
		"---\ntags:\n  - topic/travel\n---\n")
	writeFile(t, vault, "Raw/Inbox/job-1.md",
		"---\ntags:\n  - topic/should-not-be-included\n---\n")
	writeFile(t, vault, "Archive/old.md",
		"---\ntags:\n  - topic/archived\n---\n")

	svc, err := NewService(Config{Store: openStore(t), VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	topics := svc.KnownTags("topic/")
	wantTopics := []string{"topic/concurrency", "topic/system-design", "topic/travel"}
	if !equalSlices(topics, wantTopics) {
		t.Errorf("topic prefix got %v want %v", topics, wantTopics)
	}
	if !svc.HasTag("skill/golang") || svc.HasTag("interview/behavior") {
		t.Errorf("controlled prefix filter broken")
	}
	if svc.HasTag("topic/should-not-be-included") || svc.HasTag("topic/archived") {
		t.Errorf("Raw/ or Archive/ leaked into vocab")
	}
}

func TestRescanIsCumulativeForKnownTags(t *testing.T) {
	// Decision #6: known tags are only-increment across rescans. Vault drops a
	// tag → next rescan keeps it.
	vault := t.TempDir()
	writeFile(t, vault, "Knowledge/x.md", "---\ntags: [topic/old]\n---\n")
	svc, err := NewService(Config{Store: openStore(t), VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan1: %v", err)
	}
	writeFile(t, vault, "Knowledge/x.md", "---\ntags: [topic/new]\n---\n")
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

func TestReindexClearsAndRebuilds(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "Knowledge/x.md", "---\ntags: [topic/old]\n---\n")
	svc, err := NewService(Config{Store: openStore(t), VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	// Replace the file: drop topic/old, add topic/new
	writeFile(t, vault, "Knowledge/x.md", "---\ntags: [topic/new]\n---\n")
	if err := svc.Reindex(context.Background()); err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if svc.HasTag("topic/old") {
		t.Error("Reindex should have dropped topic/old (clear-then-rebuild semantics)")
	}
	if !svc.HasTag("topic/new") {
		t.Error("Reindex did not pick up topic/new")
	}
}

func TestRecordForNoteUpdatesIndex(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "Knowledge/x.md", "---\ntags: [topic/initial]\n---\n")
	store := openStore(t)
	svc, err := NewService(Config{Store: store, VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	// Simulate an executor Apply rewriting frontmatter: now Knowledge/x.md
	// carries [topic/initial, skill/rust].
	if err := svc.RecordForNote(context.Background(), "Knowledge/x.md", []string{"topic/initial", "skill/rust"}); err != nil {
		t.Fatalf("record for note: %v", err)
	}
	if !svc.HasTag("skill/rust") {
		t.Error("RecordForNote did not add skill/rust to known tags")
	}
	byTag, err := store.NotesForTags([]string{"topic/initial", "skill/rust"})
	if err != nil {
		t.Fatalf("notes for tags: %v", err)
	}
	if len(byTag["topic/initial"]) != 1 || byTag["topic/initial"][0] != "Knowledge/x.md" {
		t.Errorf("tag index for topic/initial: %v", byTag["topic/initial"])
	}
	if len(byTag["skill/rust"]) != 1 || byTag["skill/rust"][0] != "Knowledge/x.md" {
		t.Errorf("tag index for skill/rust: %v", byTag["skill/rust"])
	}
}

func TestOnRewriteNoteImplementsApplyObserver(t *testing.T) {
	vault := t.TempDir()
	store := openStore(t)
	svc, err := NewService(Config{Store: store, VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	// Direct call mirroring what direct_fs.go invokes after a successful
	// rewrite_note. New file content includes a topic/skill tag.
	content := "---\ntags:\n  - topic/golang\n  - skill/concurrency\n---\nbody\n"
	svc.OnRewriteNote(context.Background(), "Raw/Inbox/x.md", content)
	if !svc.HasTag("topic/golang") || !svc.HasTag("skill/concurrency") {
		t.Errorf("OnRewriteNote did not seed known tags from new content")
	}
	byTag, err := store.NotesForTags([]string{"skill/concurrency"})
	if err != nil {
		t.Fatalf("notes for tags: %v", err)
	}
	if len(byTag["skill/concurrency"]) != 1 {
		t.Errorf("OnRewriteNote did not update tag index for Raw/Inbox/x.md (got %v)", byTag)
	}
}

// Before RescanAll completes, Ready() must be false and WaitReady must
// honour ctx cancellation rather than hanging forever. After RescanAll
// runs, both should observe ready state.
func TestServiceReadyAndWaitReadyLifecycle(t *testing.T) {
	vault := t.TempDir()
	writeFile(t, vault, "Knowledge/x.md", "---\ntags: [topic/x]\n---\n")
	svc, err := NewService(Config{Store: openStore(t), VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if svc.Ready() {
		t.Fatal("Ready() should be false before RescanAll")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := svc.WaitReady(ctx); err == nil {
		t.Fatal("WaitReady should return ctx error when not yet ready")
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if !svc.Ready() {
		t.Fatal("Ready() should be true after RescanAll")
	}
	if err := svc.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady on ready service: %v", err)
	}
}

// When RescanAll has not run, Recall must mark Degraded with
// "tag_index_not_ready" rather than silently returning a stale/empty
// result (Phase 8 spec failure-semantics).
func TestRecallDegradedWhenTagIndexNotReady(t *testing.T) {
	vault := t.TempDir()
	svc, err := NewService(Config{Store: openStore(t), VaultRoot: vault})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	result, err := svc.Recall(context.Background(), RecallRequest{
		Tags:       []string{"topic/golang"},
		Limit:      10,
		CallerKind: "agent",
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if !result.Degraded {
		t.Errorf("expected Degraded=true when tag index not ready")
	}
	found := false
	for _, r := range result.DegradedReasons {
		if r == "tag_index_not_ready" {
			found = true
		}
	}
	if !found {
		t.Errorf("DegradedReasons missing tag_index_not_ready: %v", result.DegradedReasons)
	}
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
