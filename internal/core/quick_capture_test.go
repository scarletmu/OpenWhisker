package core

import (
	"context"
	"database/sql"
	"errors"
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

// fakeEnrichHook records Enqueue calls so tests can assert the §4 background
// tagging hook fires for quick-capture writes.
type fakeEnrichHook struct {
	calls []enrichEnqueueCall
	err   error
}

type enrichEnqueueCall struct {
	rawPath     string
	parentJobID string
}

func (f *fakeEnrichHook) Enqueue(rawPath, parentJobID string) error {
	f.calls = append(f.calls, enrichEnqueueCall{rawPath: rawPath, parentJobID: parentJobID})
	return f.err
}

// TestCaptureInboxEnqueuesEnrich proves §4: a successful quick capture schedules
// a background enrich (tagging) job for the day file, keyed by the capture's job
// id, so tagging is event-driven rather than waiting for the periodic scan.
func TestCaptureInboxEnqueuesEnrich(t *testing.T) {
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
	at := time.Date(2026, 6, 14, 9, 0, 0, 0, loc)
	hook := &fakeEnrichHook{}
	svc := quickCaptureService(t, store, vaultRoot, at, loc).WithEnrichHook(hook)

	res, err := svc.CaptureInbox(context.Background(), CaptureInboxRequest{Text: "要打标签的速记", Source: "matrix:@me"})
	if err != nil {
		t.Fatalf("CaptureInbox() error = %v", err)
	}
	if len(hook.calls) != 1 {
		t.Fatalf("enrich Enqueue called %d times, want 1", len(hook.calls))
	}
	if hook.calls[0].rawPath != res.TargetPath {
		t.Fatalf("enrich rawPath = %q, want day file %q", hook.calls[0].rawPath, res.TargetPath)
	}
	if hook.calls[0].parentJobID != res.JobID {
		t.Fatalf("enrich parentJobID = %q, want capture job %q", hook.calls[0].parentJobID, res.JobID)
	}
	// A nil hook must not panic and must still capture (legacy / CLI path).
	if _, err := quickCaptureService(t, store, vaultRoot, at, loc).
		CaptureInbox(context.Background(), CaptureInboxRequest{Text: "无 hook 也要能记", Source: "cli"}); err != nil {
		t.Fatalf("CaptureInbox() without hook error = %v", err)
	}
}

// TestInboxCommandsListUndoSearch proves §5: the three inbox operations over a
// per-day quick-capture file — list today, undo the last block (hash-guarded
// rewrite), and keyword search across day files.
func TestInboxCommandsListUndoSearch(t *testing.T) {
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
	day := time.Date(2026, 6, 15, 10, 0, 0, 0, loc)
	for i, text := range []string{"研究 docker 网络", "买牛奶", "看一篇关于 raft 的论文"} {
		at := day.Add(time.Duration(i) * time.Minute)
		if _, err := quickCaptureService(t, store, vaultRoot, at, loc).
			CaptureInbox(context.Background(), CaptureInboxRequest{Text: text, Source: "matrix:@me"}); err != nil {
			t.Fatalf("capture %d: %v", i, err)
		}
	}

	// 看今天: all three entries, in order.
	entries, _, err := quickCaptureService(t, store, vaultRoot, day, loc).ListInboxDay(day)
	if err != nil {
		t.Fatalf("ListInboxDay() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("ListInboxDay() = %d entries, want 3", len(entries))
	}
	if entries[0].Text != "研究 docker 网络" || entries[2].Text != "看一篇关于 raft 的论文" {
		t.Fatalf("entry text mismatch: %+v", entries)
	}

	// 找一下 docker: only the matching entry, from the text inbox.
	hits, err := quickCaptureService(t, store, vaultRoot, day, loc).SearchInbox("docker", 10)
	if err != nil {
		t.Fatalf("SearchInbox() error = %v", err)
	}
	if len(hits) != 1 || hits[0].Entry.Text != "研究 docker 网络" {
		t.Fatalf("SearchInbox(docker) = %+v, want single docker hit", hits)
	}

	// 撤回上一条: removes the newest block; the file keeps the first two.
	removed, ok, err := quickCaptureService(t, store, vaultRoot, day, loc).UndoLastInbox(context.Background(), day)
	if err != nil || !ok {
		t.Fatalf("UndoLastInbox() ok=%v err=%v", ok, err)
	}
	if removed.Text != "看一篇关于 raft 的论文" {
		t.Fatalf("undo removed %q, want the raft entry", removed.Text)
	}
	after, _, err := quickCaptureService(t, store, vaultRoot, day, loc).ListInboxDay(day)
	if err != nil {
		t.Fatalf("ListInboxDay() after undo error = %v", err)
	}
	if len(after) != 2 {
		t.Fatalf("after undo = %d entries, want 2", len(after))
	}
	// A subsequent capture re-appends cleanly (block index continues from the
	// surviving highest index).
	if _, err := quickCaptureService(t, store, vaultRoot, day.Add(time.Hour), loc).
		CaptureInbox(context.Background(), CaptureInboxRequest{Text: "撤回后再记一条", Source: "matrix:@me"}); err != nil {
		t.Fatalf("re-capture after undo: %v", err)
	}
	final, _, err := quickCaptureService(t, store, vaultRoot, day, loc).ListInboxDay(day)
	if err != nil {
		t.Fatalf("ListInboxDay() final error = %v", err)
	}
	if len(final) != 3 || final[2].Text != "撤回后再记一条" {
		t.Fatalf("after re-capture = %+v, want 3 entries ending with the new one", final)
	}
}

// TestHandleTextRoutesInboxCommands proves the §5 commands reach their handlers
// through HandleText in quick-capture mode, and that a near-miss phrase is still
// captured rather than swallowed as a command.
func TestHandleTextRoutesInboxCommands(t *testing.T) {
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

	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{IntentRouterMode: "quick-capture"})
	send := func(eventID, text string) AdapterResponse {
		t.Helper()
		resp, err := service.HandleText(context.Background(), AdapterRequest{
			Adapter: model.AdapterMatrix, EventID: eventID, Sender: "@user:example.test", Text: text,
		})
		if err != nil {
			t.Fatalf("HandleText(%q) error = %v", text, err)
		}
		return resp
	}

	send("$c1", "买杯咖啡")
	send("$c2", "读一下 quick-capture 设计")

	today := send("$today", "看今天")
	if !strings.Contains(today.Body, "今天记了 2 条") {
		t.Fatalf("看今天 body = %q", today.Body)
	}
	find := send("$find", "找一下 quick-capture")
	if !strings.Contains(find.Body, "quick-capture") || !strings.Contains(find.Body, "找到 1 条") {
		t.Fatalf("找一下 body = %q", find.Body)
	}
	undo := send("$undo", "撤回")
	if !strings.Contains(undo.Body, "已撤回") {
		t.Fatalf("撤回 body = %q", undo.Body)
	}
	// A near-miss phrase is an ordinary capture, not a command.
	cap := send("$c3", "今天天气不错")
	if cap.Body != "已记录" {
		t.Fatalf("near-miss capture body = %q, want 已记录", cap.Body)
	}
}

type fakeClipCapturer struct {
	urls []string
	err  error
}

func (f *fakeClipCapturer) Clip(_ context.Context, rawURL, _, _ string) (string, string, error) {
	f.urls = append(f.urls, rawURL)
	if f.err != nil {
		return "", "", f.err
	}
	return "Raw/Sources/clip.md", "job_clip", nil
}

// TestHandleTextRoutesBareURLToClip proves §3.2 routing: a whole-message URL is
// clipped (ack 已记录，剪藏中), a URL mixed with words is captured as text, and a
// rejected clip falls back to text capture so nothing is lost.
func TestHandleTextRoutesBareURLToClip(t *testing.T) {
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

	clip := &fakeClipCapturer{}
	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{
		IntentRouterMode: "quick-capture",
		ClipCapturer:     clip,
	})
	send := func(id, text string) AdapterResponse {
		t.Helper()
		resp, err := service.HandleText(context.Background(), AdapterRequest{
			Adapter: model.AdapterMatrix, EventID: id, Sender: "@user:example.test", Text: text,
		})
		if err != nil {
			t.Fatalf("HandleText(%q) error = %v", text, err)
		}
		return resp
	}

	clipResp := send("$u1", "https://example.com/article")
	if clipResp.Body != "已记录，剪藏中" {
		t.Fatalf("bare URL body = %q, want 已记录，剪藏中", clipResp.Body)
	}
	if len(clip.urls) != 1 || clip.urls[0] != "https://example.com/article" {
		t.Fatalf("clip urls = %v, want one", clip.urls)
	}

	mixed := send("$u2", "看看这个 https://example.com/x")
	if mixed.Body != "已记录" {
		t.Fatalf("mixed text+URL body = %q, want plain capture 已记录", mixed.Body)
	}
	if len(clip.urls) != 1 {
		t.Fatalf("mixed message should not clip; urls = %v", clip.urls)
	}

	// A capturer that rejects the URL → fall back to text capture.
	clip.err = errors.New("blocked")
	fallback := send("$u3", "https://10.0.0.1/internal")
	if fallback.Body != "已记录" {
		t.Fatalf("rejected clip fallback body = %q, want text-capture 已记录", fallback.Body)
	}
}

// TestQuickCaptureRetiresOldFlowCommands proves §6: organize/approve/diff/reject
// are removed from the IM path in quick-capture mode (redirected), while
// operational commands still work.
func TestQuickCaptureRetiresOldFlowCommands(t *testing.T) {
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

	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{IntentRouterMode: "quick-capture"})
	send := func(id, text string) AdapterResponse {
		t.Helper()
		resp, err := service.HandleText(context.Background(), AdapterRequest{
			Adapter: model.AdapterMatrix, EventID: id, Sender: "@user:example.test", Text: text,
		})
		if err != nil {
			t.Fatalf("HandleText(%q) error = %v", text, err)
		}
		return resp
	}

	for _, cmd := range []string{"/organize last", "/approve plan_x", "/diff plan_x", "/reject plan_x"} {
		resp := send("$retire-"+cmd, cmd)
		if resp.Status != "command_retired" {
			t.Fatalf("%q status = %q, want command_retired", cmd, resp.Status)
		}
	}
	// Operational command still works.
	jobs := send("$ops", "/jobs")
	if !strings.Contains(jobs.Body, "No jobs yet.") && !strings.Contains(jobs.Body, "Recent jobs:") {
		t.Fatalf("/jobs body = %q, want a jobs listing", jobs.Body)
	}
}

func TestInboxCommandKindClassification(t *testing.T) {
	cases := []struct {
		text     string
		wantKind string
		wantArg  string
	}{
		{"看今天", "today", ""},
		{"/today", "today", ""},
		{"撤回上一条", "undo", ""},
		{"/undo", "undo", ""},
		{"找一下 docker", "find", "docker"},
		{"找一下：raft", "find", "raft"},
		{"/find 网络", "find", "网络"},
		{"搜索", "find", ""},          // bare command word → prompt for a keyword
		{"今天去爬山，风很大", "", ""},      // ordinary capture, not a command
		{"撤回了一个错误的部署", "", ""},     // not exactly "撤回"
		{"搜索引擎的原理很有趣", "", ""},     // glued prefix → capture, not a search
		{"找一下午饭吃什么", "", ""},       // glued prefix → capture, not a search
		{"随手记一条", "", ""},
	}
	for _, tc := range cases {
		kind, arg := inboxCommandKind(tc.text)
		if kind != tc.wantKind || arg != tc.wantArg {
			t.Errorf("inboxCommandKind(%q) = (%q,%q), want (%q,%q)", tc.text, kind, arg, tc.wantKind, tc.wantArg)
		}
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}
