package enrich

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Worker is the daemon-side goroutine that drains the enrich queue. Single
// instance per daemon; the queue's Claim transition is the serializer (per
// decision: avoid concurrent agent runs on the same vault to keep LLM
// provider rate limiting predictable and avoid two workers fighting over
// the same Raw note).
type Worker struct {
	svc      *Service
	pollIdle time.Duration
	logger   io.Writer
}

// WorkerOption configures Worker. Kept as functional opts so future
// fields (rate limit, alt logger) don't churn the constructor signature.
type WorkerOption func(*Worker)

// WithIdlePoll sets how long the worker sleeps between Claim attempts when
// the queue is empty. Defaults to 10 seconds.
func WithIdlePoll(d time.Duration) WorkerOption {
	return func(w *Worker) {
		if d > 0 {
			w.pollIdle = d
		}
	}
}

// WithLogger redirects diagnostic logs (one line per claimed job, one line
// per failure). nil silences the worker entirely.
func WithLogger(w io.Writer) WorkerOption {
	return func(x *Worker) { x.logger = w }
}

// NewWorker wraps svc with a poll loop.
func NewWorker(svc *Service, opts ...WorkerOption) *Worker {
	w := &Worker{svc: svc, pollIdle: 10 * time.Second}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Run blocks until ctx is cancelled. Each loop iteration either processes
// one job or sleeps WithIdlePoll. Returns ctx.Err() on cancellation so
// daemon shutdown plumbing can treat it as a normal exit.
func (w *Worker) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, claimed, err := w.svc.cfg.Queue.Claim()
		if err != nil {
			w.logf("enrich worker: claim error: %v", err)
			if !sleep(ctx, w.pollIdle) {
				return ctx.Err()
			}
			continue
		}
		if !claimed {
			if !sleep(ctx, w.pollIdle) {
				return ctx.Err()
			}
			continue
		}
		w.logf("enrich worker: claimed raw_path=%s attempts=%d", job.RawPath, job.Attempts)
		outcome := w.svc.RunOne(ctx, job)
		if outcome.Err != nil {
			w.logf("enrich worker: raw_path=%s state=%s err=%v", job.RawPath, outcome.State, outcome.Err)
		} else {
			w.logf("enrich worker: raw_path=%s state=%s", job.RawPath, outcome.State)
		}
	}
}

func (w *Worker) logf(format string, args ...any) {
	if w.logger == nil {
		return
	}
	fmt.Fprintf(w.logger, format+"\n", args...)
}

// sleep returns true if the timer elapsed and false if ctx fired first.
// Centralised here so worker and scan share one cancellation-safe sleep.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
