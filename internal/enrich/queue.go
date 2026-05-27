// Package enrich implements Phase 7 inbox enrichment: an async pass that
// gives newly-captured Raw notes vault-native `topic/*` / `skill/*` tags by
// running a tool-calling agent against the vault and writing the result
// back through the `rewrite_note` policy guard.
//
// Module layout (per Phase 7 design):
//
//   - queue.go : the thin connector between sqlite enrich_jobs (DAO lives
//                in internal/storage) and the rest of the package.
//   - enrich.go: one-job orchestration (vocab → hash guard → agent run →
//                plan → policy → executor).
//   - worker.go: daemon goroutine that drains the queue serially.
//   - scan.go  : the periodic Raw/Inbox/ sweep that backfills missed jobs.
package enrich

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// Queue is the durable enrich job queue, backed by the sqlite enrich_jobs
// table. The struct is a thin facade over storage.Store so callers don't
// need to think about table names or SQL — and so the queue can later be
// swapped for an in-process implementation in tests without touching call
// sites.
type Queue struct {
	store *storage.Store
	now   func() time.Time
}

// NewQueue constructs a Queue bound to the given store. The default clock
// is time.Now; tests override via WithClock.
func NewQueue(store *storage.Store) *Queue {
	return &Queue{store: store, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock returns q with its clock replaced. Used by tests so deterministic
// timestamps land in the enrich_jobs table.
func (q *Queue) WithClock(now func() time.Time) *Queue {
	if now != nil {
		q.now = now
	}
	return q
}

// Enqueue records (or refreshes) a pending enrich job for rawPath. The
// underlying upsert never demotes a done / running / attempts_exhausted row
// back to pending — that protection is enforced in SQL so concurrent callers
// (scan tick + IngestRaw hook firing on the same path) cannot race.
//
// Empty parentJobID is allowed: scan-driven enqueues leave it blank because
// no IngestRaw triggered them.
func (q *Queue) Enqueue(rawPath, parentJobID string) error {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return errors.New("enrich queue: raw_path is required")
	}
	return q.store.EnqueueEnrichJob(rawPath, strings.TrimSpace(parentJobID), q.now())
}

// Reset forces a row back to pending and clears attempts. Used by the
// `openwhisker enrich <rawJobID>` manual retry path. Inserts a fresh row
// when the raw_path was never tracked.
func (q *Queue) Reset(rawPath string) error {
	return q.store.ResetEnrichJob(strings.TrimSpace(rawPath), q.now())
}

// Claim atomically picks the next runnable job and flips its state to
// running. Returns (false, nil) when the queue is empty so the worker can
// sleep without treating "empty" as an error.
func (q *Queue) Claim() (storage.EnrichJob, bool, error) {
	job, err := q.store.ClaimNextEnrichJob(q.now())
	if err != nil {
		// sql.ErrNoRows is the standard "queue empty" signal. We pattern
		// match on the error string rather than importing sql to keep the
		// queue's import surface narrow.
		if err.Error() == "sql: no rows in result set" {
			return storage.EnrichJob{}, false, nil
		}
		return storage.EnrichJob{}, false, fmt.Errorf("enrich queue: claim: %w", err)
	}
	return job, true, nil
}

// Finish writes the terminal (or retriable) state for the job. state=done
// is the only state that does not bump the attempts counter.
func (q *Queue) Finish(rawPath, state, errText string) error {
	return q.store.FinishEnrichJob(rawPath, state, errText, q.now())
}

// Get returns the row for rawPath. Returns (EnrichJob{}, false, nil) when
// the row is absent so callers can branch on existence without inspecting
// sentinel errors.
func (q *Queue) Get(rawPath string) (storage.EnrichJob, bool, error) {
	job, err := q.store.GetEnrichJob(strings.TrimSpace(rawPath))
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return storage.EnrichJob{}, false, nil
		}
		return storage.EnrichJob{}, false, fmt.Errorf("enrich queue: get: %w", err)
	}
	return job, true, nil
}

// State helpers — re-exporting model constants under a smaller name keeps
// callers inside this package from having to import internal/model just to
// branch on the state machine.
const (
	StatePending          = model.EnrichJobStatePending
	StateRunning          = model.EnrichJobStateRunning
	StateDone             = model.EnrichJobStateDone
	StateSkippedConcEdit  = model.EnrichJobStateSkippedConcEdit
	StateFailed           = model.EnrichJobStateFailed
	StateAttemptsExceeded = model.EnrichJobStateAttemptsExceeded
)
