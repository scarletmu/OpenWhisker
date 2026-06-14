package clip

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/executor"
)

// DefaultScanInterval is how often the worker re-scans RawSourcesDir for notes
// stuck in status: clipping (recovery after a dropped-queue or daemon restart).
const DefaultScanInterval = 2 * time.Minute

// Worker drains the clip queue serially and periodically rescans for stragglers.
// One worker per vault: clipping is I/O bound and serial keeps outbound fetch
// volume predictable.
type Worker struct {
	svc          *Service
	logger       io.Writer
	scanInterval time.Duration
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithLogger redirects worker diagnostics; nil silences them.
func WithLogger(w io.Writer) WorkerOption {
	return func(wk *Worker) { wk.logger = w }
}

// WithScanInterval overrides the straggler-rescan cadence.
func WithScanInterval(d time.Duration) WorkerOption {
	return func(wk *Worker) {
		if d > 0 {
			wk.scanInterval = d
		}
	}
}

func NewWorker(svc *Service, opts ...WorkerOption) *Worker {
	w := &Worker{svc: svc, scanInterval: DefaultScanInterval}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// Run drains the queue until ctx is cancelled, recovering stragglers on entry
// and on every scan tick.
func (w *Worker) Run(ctx context.Context) error {
	w.scanOnce(ctx)
	ticker := time.NewTicker(w.scanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case job := <-w.svc.Queue().C():
			if err := w.svc.ProcessClip(ctx, job); err != nil {
				w.logf("clip worker: %s: %v", job.NotePath, err)
			} else {
				w.logf("clip worker: clipped %s", job.NotePath)
			}
		case <-ticker.C:
			w.scanOnce(ctx)
		}
	}
}

// scanOnce sweeps RawSourcesDir and re-enqueues any note still in status:
// clipping (a clip that never completed). Idempotent: completed notes are
// status: clipped / clip-failed and are skipped.
func (w *Worker) scanOnce(ctx context.Context) {
	dirAbs, err := executor.ResolveVaultPath(w.svc.vaultRoot, w.svc.conventions.RawSourcesDir)
	if err != nil {
		return
	}
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		return // dir absent yet → nothing to recover
	}
	recovered := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return
		}
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		rel := w.svc.conventions.RawSourcesDir + "/" + e.Name()
		content, err := w.svc.readVaultFile(rel)
		if err != nil {
			continue
		}
		doc := parseFrontmatter(content)
		if doc.Scalar("status") != statusClipping {
			continue
		}
		url := doc.Scalar("url")
		if strings.TrimSpace(url) == "" {
			continue
		}
		if w.svc.Queue().Enqueue(Job{NotePath: rel, URL: url}) {
			recovered++
		}
	}
	if recovered > 0 {
		w.logf("clip worker: recovered %d straggler clip(s)", recovered)
	}
}

func (w *Worker) logf(format string, args ...any) {
	if w.logger == nil {
		return
	}
	fmt.Fprintf(w.logger, format+"\n", args...)
}
