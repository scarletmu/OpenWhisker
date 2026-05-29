package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/agent"
	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/memory"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// fakeRunner implements the AgentRunner interface for tests.
type fakeRunner struct {
	calls   int32
	result  agent.AgentRunResult
	err     error
	hook    func(req agent.AgentRunRequest) // observe request mid-run
}

func (f *fakeRunner) Run(ctx context.Context, req agent.AgentRunRequest) (agent.AgentRunResult, error) {
	atomic.AddInt32(&f.calls, 1)
	if f.hook != nil {
		f.hook(req)
	}
	if f.err != nil {
		return agent.AgentRunResult{}, f.err
	}
	return f.result, nil
}

func payloadJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return json.RawMessage(b)
}

const fixtureRawNote = `---
openwhisker_job_id: job-fixture
openwhisker_job_type: ingest_raw
source: cli
source_key: smoke
created_at: 2026-05-27T00:00:00Z
status: raw
---

# Raw Capture

## Source Trace

- Job: job-fixture
- Source: cli
- Captured at: 2026-05-27T00:00:00Z

## Raw Text

` + "```text\nA short snippet about Go concurrency primitives.\n```" + `
`

type harness struct {
	t        *testing.T
	vault    string
	store    *storage.Store
	queue    *Queue
	vocab    *memory.Service
	runner   *fakeRunner
	svc      *Service
	now      time.Time
	rawPath  string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tempVault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tempVault, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatalf("mkdir inbox: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tempVault, "Knowledge"), 0o755); err != nil {
		t.Fatalf("mkdir knowledge: %v", err)
	}
	// Seed vocab: at minimum one topic/skill tag the agent can return.
	if err := os.WriteFile(filepath.Join(tempVault, "Knowledge", "go-concurrency.md"),
		[]byte("---\ntags:\n  - topic/concurrency\n  - skill/golang\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("seed vocab: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "ow.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	vocab, err := memory.NewService(memory.Config{Store: store, VaultRoot: tempVault})
	if err != nil {
		t.Fatalf("new memory service: %v", err)
	}
	if err := vocab.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan vocab: %v", err)
	}

	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	queue := NewQueue(store).WithClock(func() time.Time { return now })
	runner := &fakeRunner{}
	svc, err := NewService(Config{
		Store:     store,
		Queue:     queue,
		VaultRoot: tempVault,
		Runner:    runner,
		Vocab:     vocab,
		Executor:  executor.NewDirectFS(tempVault, store),
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	rawPath := "Raw/Inbox/job-fixture.md"
	absRaw := filepath.Join(tempVault, filepath.FromSlash(rawPath))
	if err := os.WriteFile(absRaw, []byte(fixtureRawNote), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}

	return &harness{
		t:       t,
		vault:   tempVault,
		store:   store,
		queue:   queue,
		vocab:   vocab,
		runner:  runner,
		svc:     svc,
		now:     now,
		rawPath: rawPath,
	}
}

// newScanner builds a scanner whose clock is anchored to real wall time rather
// than the harness's frozen logical clock (h.now). Fixture files are written by
// t.TempDir with real OS mtimes, so a frozen past clock would put every fixture
// "in the future" relative to the quiet-window cutoff and the scanner would skip
// them all — a time bomb that went off once wall-clock time passed h.now. With a
// real-time cutoff one hour ahead, fixtures written moments ago always satisfy
// the Before(cutoff) check. WithQuietWindow(0) keeps the cutoff at the clock.
func (h *harness) newScanner() *Scanner {
	scanCutoffClock := time.Now().UTC().Add(time.Hour)
	return NewScanner(h.queue, h.vault, policy.DefaultConventions()).
		WithQuietWindow(0).
		WithClock(func() time.Time { return scanCutoffClock })
}

func (h *harness) claimAndRun(t *testing.T) RunOutcome {
	t.Helper()
	job, ok, err := h.queue.Claim()
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatal("queue empty after enqueue")
	}
	return h.svc.RunOne(context.Background(), job)
}

func (h *harness) readRaw(t *testing.T) string {
	t.Helper()
	abs := filepath.Join(h.vault, filepath.FromSlash(h.rawPath))
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	return string(data)
}

func mustEnqueue(t *testing.T, q *Queue, rawPath, parent string) {
	t.Helper()
	if err := q.Enqueue(rawPath, parent); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

func TestEnrichHappyPath(t *testing.T) {
	h := newHarness(t)
	h.runner.result = agent.AgentRunResult{
		Status: model.SchedulerRunStatusDone,
		Title:  "ok",
		Summary: "tags applied",
		SkillID: "openwhisker:inbox-enrich",
		Payload: payloadJSON(t, EnrichResult{
			Tags:    []string{"topic/concurrency", "skill/golang"},
			Related: []string{"[[Knowledge/go-concurrency]]"},
		}),
	}
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")

	out := h.claimAndRun(t)
	if out.Err != nil {
		t.Fatalf("happy run errored: %v", out.Err)
	}
	if out.State != StateDone {
		t.Fatalf("state=%s want %s", out.State, StateDone)
	}

	after := h.readRaw(t)
	if !strings.Contains(after, "topic/concurrency") || !strings.Contains(after, "skill/golang") {
		t.Errorf("expected tags written, got:\n%s", after)
	}
	if !strings.Contains(after, "openwhisker_enriched_at:") {
		t.Errorf("expected enriched_at, got:\n%s", after)
	}
	// Body must be byte-identical (body_guard).
	if !strings.Contains(after, "A short snippet about Go concurrency primitives.") {
		t.Errorf("body mutated:\n%s", after)
	}
}

func TestEnrichRejectsUnknownTopicTag(t *testing.T) {
	h := newHarness(t)
	h.runner.result = agent.AgentRunResult{
		Status:  model.SchedulerRunStatusDone,
		Title:   "ok",
		Summary: "tags applied",
		SkillID: "openwhisker:inbox-enrich",
		Payload: payloadJSON(t, EnrichResult{
			Tags: []string{"topic/not-in-vocab"},
		}),
	}
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
	out := h.claimAndRun(t)
	if out.State != StateFailed {
		t.Fatalf("state=%s want %s (err=%v)", out.State, StateFailed, out.Err)
	}
	// Raw should be unchanged.
	if h.readRaw(t) != fixtureRawNote {
		t.Errorf("raw mutated despite policy reject:\n%s", h.readRaw(t))
	}
	// Job row attempts should have bumped.
	row, _, _ := h.queue.Get(h.rawPath)
	if row.Attempts != 1 {
		t.Errorf("attempts=%d want 1", row.Attempts)
	}
}

func TestEnrichConcurrentEditDrops(t *testing.T) {
	h := newHarness(t)
	h.runner.result = agent.AgentRunResult{
		Status:  model.SchedulerRunStatusDone,
		Title:   "ok",
		Summary: "tags applied",
		SkillID: "openwhisker:inbox-enrich",
		Payload: payloadJSON(t, EnrichResult{Tags: []string{"topic/concurrency"}}),
	}
	// Mutate the raw file mid-run by overriding the agent hook.
	h.runner.hook = func(req agent.AgentRunRequest) {
		abs := filepath.Join(h.vault, filepath.FromSlash(h.rawPath))
		_ = os.WriteFile(abs, []byte(fixtureRawNote+"\nUSER EDIT\n"), 0o644)
	}
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
	out := h.claimAndRun(t)
	if out.State != StateSkippedConcEdit {
		t.Fatalf("state=%s want %s (err=%v)", out.State, StateSkippedConcEdit, out.Err)
	}
	// Raw should retain the user edit, not the enrich output.
	after := h.readRaw(t)
	if !strings.Contains(after, "USER EDIT") {
		t.Errorf("user edit lost:\n%s", after)
	}
	if strings.Contains(after, "openwhisker_enriched_at") {
		t.Errorf("enrich applied despite hash mismatch:\n%s", after)
	}
}

func TestEnrichBucketFilePresentIsSkippedByAlreadyEnrichedGuard(t *testing.T) {
	// Bucket paths are excluded by IngestRaw at enqueue time; scan also
	// filters them. This test exercises the orchestrator's own "already
	// enriched" / missing-file guards so the safety net stays intact.
	h := newHarness(t)
	// Mark the file as already enriched.
	abs := filepath.Join(h.vault, filepath.FromSlash(h.rawPath))
	already := strings.Replace(fixtureRawNote, "status: raw\n", "status: raw\nopenwhisker_enriched_at: 2026-05-27T00:00:00Z\n", 1)
	if err := os.WriteFile(abs, []byte(already), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
	out := h.claimAndRun(t)
	if out.State != StateDone {
		t.Fatalf("state=%s want %s (err=%v)", out.State, StateDone, out.Err)
	}
	if atomic.LoadInt32(&h.runner.calls) != 0 {
		t.Errorf("agent was called even though enriched_at was present")
	}
}

func TestEnrichMissingFileTerminatesAsDone(t *testing.T) {
	h := newHarness(t)
	abs := filepath.Join(h.vault, filepath.FromSlash(h.rawPath))
	if err := os.Remove(abs); err != nil {
		t.Fatalf("rm: %v", err)
	}
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
	out := h.claimAndRun(t)
	if out.State != StateDone {
		t.Fatalf("state=%s want %s (err=%v)", out.State, StateDone, out.Err)
	}
	if atomic.LoadInt32(&h.runner.calls) != 0 {
		t.Errorf("agent was called for missing file")
	}
}

func TestEnrichAgentFailureRecordsFailed(t *testing.T) {
	h := newHarness(t)
	h.runner.err = errors.New("LLM exploded")
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
	out := h.claimAndRun(t)
	if out.State != StateFailed {
		t.Fatalf("state=%s want %s (err=%v)", out.State, StateFailed, out.Err)
	}
	if h.readRaw(t) != fixtureRawNote {
		t.Errorf("raw mutated on agent failure")
	}
}

func TestEnrichQueuePersistsAcrossReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ow.db")
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	now := time.Date(2026, 5, 27, 12, 0, 0, 0, time.UTC)
	q := NewQueue(store).WithClock(func() time.Time { return now })
	if err := q.Enqueue("Raw/Inbox/persist.md", "job-p"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	_ = store.Close()

	store2, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	q2 := NewQueue(store2).WithClock(func() time.Time { return now })
	job, ok, err := q2.Claim()
	if err != nil {
		t.Fatalf("claim after reopen: %v", err)
	}
	if !ok {
		t.Fatal("queue empty after reopen — persistence broken")
	}
	if job.RawPath != "Raw/Inbox/persist.md" || job.ParentJobID != "job-p" {
		t.Errorf("wrong job: %+v", job)
	}
}

func TestEnrichAttemptsCeilingTransitions(t *testing.T) {
	h := newHarness(t)
	h.runner.err = errors.New("flaky")
	// Burn through attempts. Each iteration simulates the scan path
	// re-enqueuing the failed row, which the SQL upsert flips back to
	// pending until attempts hits the ceiling.
	for i := 0; i < model.EnrichAttemptsMax; i++ {
		mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
		job, ok, err := h.queue.Claim()
		if err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("queue empty before ceiling (i=%d)", i)
		}
		h.svc.RunOne(context.Background(), job)
	}
	row, ok, err := h.queue.Get(h.rawPath)
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if row.State != StateAttemptsExceeded {
		t.Errorf("state=%s want %s (attempts=%d)", row.State, StateAttemptsExceeded, row.Attempts)
	}
	// Another scan-style enqueue must not revive an exhausted row.
	mustEnqueue(t, h.queue, h.rawPath, "job-fixture")
	_, ok2, _ := h.queue.Claim()
	if ok2 {
		t.Error("queue still serves attempts_exhausted row after re-enqueue")
	}
}

func TestEnrichResultToleratesStringRouteSuggestion(t *testing.T) {
	// Some providers (e.g. deepseek-chat) emit route_suggestion as a bare
	// string instead of the {target_dir, confidence, reason} object. That must
	// not hard-fail the whole decode and silently drop the tags/related the run
	// produced.
	raw := []byte(`{"tags":["topic/concurrency"],"related":["[[X]]"],"route_suggestion":"Knowledge/Systems"}`)
	var er EnrichResult
	if err := json.Unmarshal(raw, &er); err != nil {
		t.Fatalf("string route_suggestion should decode leniently, got: %v", err)
	}
	if len(er.Tags) != 1 || er.Tags[0] != "topic/concurrency" {
		t.Errorf("tags lost: %+v", er.Tags)
	}
	if er.RouteSuggestion == nil {
		t.Fatal("route_suggestion should be non-nil")
	}
	// A string carries no confidence, so it stays at 0 — below the persist
	// threshold — and is never written to frontmatter.
	if er.RouteSuggestion.Confidence != 0 {
		t.Errorf("string form must carry zero confidence, got %v", er.RouteSuggestion.Confidence)
	}
	if er.RouteSuggestion.Reason != "Knowledge/Systems" {
		t.Errorf("reason = %q, want the raw string", er.RouteSuggestion.Reason)
	}
}

func TestEnrichResultStillDecodesObjectRouteSuggestion(t *testing.T) {
	raw := []byte(`{"route_suggestion":{"target_dir":"Knowledge/Systems","confidence":0.9,"reason":"clearly systems"}}`)
	var er EnrichResult
	if err := json.Unmarshal(raw, &er); err != nil {
		t.Fatalf("object route_suggestion: %v", err)
	}
	if er.RouteSuggestion == nil {
		t.Fatal("route_suggestion should be non-nil")
	}
	if er.RouteSuggestion.TargetDir != "Knowledge/Systems" || er.RouteSuggestion.Confidence != 0.9 {
		t.Errorf("object form decoded wrong: %+v", er.RouteSuggestion)
	}
}

func TestEnrichScanEnqueuesEligibleFiles(t *testing.T) {
	h := newHarness(t)
	scanner := h.newScanner()
	if err := scanner.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	job, ok, err := h.queue.Claim()
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !ok {
		t.Fatal("scan did not enqueue eligible Raw note")
	}
	if job.RawPath != h.rawPath {
		t.Errorf("wrong path: %s", job.RawPath)
	}
}

func TestEnrichScanSkipsBucketAndEnriched(t *testing.T) {
	h := newHarness(t)
	// Add a bucket file and an already-enriched file.
	bucketPath := filepath.Join(h.vault, "Raw", "Inbox", "abc_bucket.md")
	if err := os.WriteFile(bucketPath, []byte("---\ntags:\n  - raw/bucket\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("bucket: %v", err)
	}
	enrichedPath := filepath.Join(h.vault, "Raw", "Inbox", "enriched.md")
	if err := os.WriteFile(enrichedPath, []byte("---\nopenwhisker_enriched_at: 2026-05-27T00:00:00Z\n---\nbody\n"), 0o644); err != nil {
		t.Fatalf("enriched: %v", err)
	}
	scanner := h.newScanner()
	if err := scanner.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
	// Only the original fixture file should have been enqueued.
	seen := map[string]bool{}
	for {
		job, ok, err := h.queue.Claim()
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if !ok {
			break
		}
		seen[job.RawPath] = true
	}
	if !seen[h.rawPath] {
		t.Error("scan missed eligible file")
	}
	for k := range seen {
		if strings.HasSuffix(k, "_bucket.md") {
			t.Errorf("scan enqueued bucket file: %s", k)
		}
		if strings.HasSuffix(k, "enriched.md") {
			t.Errorf("scan enqueued already-enriched file: %s", k)
		}
	}
}
