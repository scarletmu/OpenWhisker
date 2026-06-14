package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// quickCaptureService builds an IngestService with a pinned clock and timezone
// so the day file name and block timestamps are deterministic.
func quickCaptureService(t *testing.T, store *storage.Store, vaultRoot string, at time.Time, loc *time.Location) IngestService {
	t.Helper()
	svc := NewIngestService(store, vaultRoot).WithLocation(loc)
	svc.now = func() time.Time { return at }
	return svc
}

func TestCaptureInboxCreatesThenAppendsPerDayFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	loc := time.FixedZone("CST", 8*3600)
	t0 := time.Date(2026, 6, 13, 21, 30, 0, 0, loc)
	t1 := time.Date(2026, 6, 13, 21, 35, 0, 0, loc)

	first, err := quickCaptureService(t, store, vaultRoot, t0, loc).
		CaptureInbox(context.Background(), CaptureInboxRequest{Text: "闪念一号", Source: "matrix:@me"})
	if err != nil {
		t.Fatalf("CaptureInbox() first error = %v", err)
	}
	if first.TargetPath != "Raw/Inbox/2026-06-13.md" {
		t.Fatalf("first TargetPath = %q, want Raw/Inbox/2026-06-13.md", first.TargetPath)
	}

	second, err := quickCaptureService(t, store, vaultRoot, t1, loc).
		CaptureInbox(context.Background(), CaptureInboxRequest{Text: "闪念二号", Source: "matrix:@me"})
	if err != nil {
		t.Fatalf("CaptureInbox() second error = %v", err)
	}
	if second.TargetPath != first.TargetPath {
		t.Fatalf("second TargetPath = %q, want same day file %q", second.TargetPath, first.TargetPath)
	}

	// Exactly one file for the day — appends must not spawn new files.
	entries, err := os.ReadDir(filepath.Join(vaultRoot, "Raw", "Inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "2026-06-13.md" {
		t.Fatalf("inbox dir = %v, want single 2026-06-13.md", names(entries))
	}

	content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(first.TargetPath)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	for _, want := range []string{
		"title: \"速记收件箱 2026-06-13\"",
		"openwhisker_capture: quick-inbox",
		"## 输入 1",
		"## 输入 2",
		"闪念一号",
		"闪念二号",
		"2026-06-13T21:30:00+08:00", // input 1 block timestamp
		"2026-06-13T21:35:00+08:00", // input 2 block timestamp + refreshed `updated`
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("day file missing %q:\n%s", want, got)
		}
	}
	// `updated` must advance to the latest capture; `created` stays at t0.
	if !strings.Contains(got, "created: 2026-06-13T21:30:00+08:00") {
		t.Fatalf("created stamp not preserved:\n%s", got)
	}
	if strings.Count(got, "updated: 2026-06-13T21:30:00+08:00") != 0 {
		t.Fatalf("updated stamp not refreshed away from t0:\n%s", got)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, "wiki_jobs", 2)
	assertCount(t, db, "vault_plans", 2)
	// Quick capture is silent: the ack is the response body, not an outbox row.
	assertCount(t, db, "outbox_messages", 0)
}

// TestCaptureInboxUsesLocalDayBoundary proves the day file rolls at the user's
// local midnight, not UTC: 16:30 UTC is already the next day at +08:00.
func TestCaptureInboxUsesLocalDayBoundary(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	loc := time.FixedZone("CST", 8*3600)
	utcNow := time.Date(2026, 6, 13, 16, 30, 0, 0, time.UTC) // 2026-06-14 00:30 +08:00

	res, err := quickCaptureService(t, store, vaultRoot, utcNow, loc).
		CaptureInbox(context.Background(), CaptureInboxRequest{Text: "跨夜速记", Source: "cli"})
	if err != nil {
		t.Fatalf("CaptureInbox() error = %v", err)
	}
	if res.TargetPath != "Raw/Inbox/2026-06-14.md" {
		t.Fatalf("TargetPath = %q, want Raw/Inbox/2026-06-14.md (local day)", res.TargetPath)
	}
}

func TestHandleTextQuickCaptureAcksAndWritesInbox(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{
		IntentRouterMode: "quick-capture",
	})
	resp, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$qc-1",
		Sender:  "@user:example.test",
		Text:    "随手记一条",
	})
	if err != nil {
		t.Fatalf("HandleText() error = %v", err)
	}
	if resp.Body != "已记录" {
		t.Fatalf("ack body = %q, want 已记录", resp.Body)
	}
	if resp.OutboxKind != "" {
		t.Fatalf("ack OutboxKind = %q, want empty (no second message)", resp.OutboxKind)
	}

	// One inbox file written under today's local date, containing the snippet.
	entries, err := os.ReadDir(filepath.Join(vaultRoot, "Raw", "Inbox"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("inbox dir = %v, want one day file", names(entries))
	}
	content, err := os.ReadFile(filepath.Join(vaultRoot, "Raw", "Inbox", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "随手记一条") {
		t.Fatalf("day file missing captured text:\n%s", content)
	}

	// Slash commands still reach the command parser in quick-capture mode.
	jobs, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$qc-jobs",
		Sender:  "@user:example.test",
		Text:    "/jobs",
	})
	if err != nil {
		t.Fatalf("HandleText(/jobs) error = %v", err)
	}
	if !strings.Contains(jobs.Body, "Recent jobs:") {
		t.Fatalf("/jobs body = %q, want recent jobs listing", jobs.Body)
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
