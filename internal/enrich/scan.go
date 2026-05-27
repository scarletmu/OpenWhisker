package enrich

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/policy"
)

// Phase 7 hardcoded constants (decision 10). Move to yaml only after the
// first round of demo data shows the values need tuning.
const (
	ScanInterval      = 5 * time.Minute
	ScanQuietWindow   = 10 * time.Minute
	ScanInboxSubdir   = "Raw/Inbox"
)

// Scanner is the periodic Raw/Inbox/ sweeper. It backfills the queue with
// notes the event-driven enqueue path missed — for example, files dropped
// directly into Raw/Inbox/ by the user, or notes whose openwhisker_enriched_at
// was wiped manually.
type Scanner struct {
	queue       *Queue
	vaultRoot   string
	conventions policy.Conventions
	interval    time.Duration
	quietWindow time.Duration
	logger      io.Writer
	now         func() time.Time
}

// NewScanner constructs a Scanner with default Phase 7 constants. The
// conventions argument provides the configured Raw/Inbox/ subpath (so a
// non-default vault profile is honored).
func NewScanner(queue *Queue, vaultRoot string, conventions policy.Conventions) *Scanner {
	return &Scanner{
		queue:       queue,
		vaultRoot:   vaultRoot,
		conventions: conventions,
		interval:    ScanInterval,
		quietWindow: ScanQuietWindow,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// WithInterval overrides the scan tick. Tests use a short interval to
// exercise the loop without a 5-minute wait.
func (s *Scanner) WithInterval(d time.Duration) *Scanner {
	if d > 0 {
		s.interval = d
	}
	return s
}

// WithQuietWindow overrides the mtime quiet window. Tests use 0 so they
// can enqueue files whose mtime is "now".
func (s *Scanner) WithQuietWindow(d time.Duration) *Scanner {
	s.quietWindow = d
	return s
}

// WithLogger redirects scan diagnostics.
func (s *Scanner) WithLogger(w io.Writer) *Scanner {
	s.logger = w
	return s
}

// WithClock overrides the clock; used by tests.
func (s *Scanner) WithClock(now func() time.Time) *Scanner {
	if now != nil {
		s.now = now
	}
	return s
}

// Run runs Tick() every interval until ctx is cancelled. The first tick
// fires immediately on entry, so a fresh daemon does not wait one full
// interval before processing the backlog.
func (s *Scanner) Run(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Tick(ctx); err != nil {
			s.logf("enrich scan: tick error: %v", err)
		}
		if !sleep(ctx, s.interval) {
			return ctx.Err()
		}
	}
}

// Tick is one scan pass. Exposed (not just used by Run) so the manual
// `openwhisker enrich --scan` subcommand can trigger a one-shot sweep.
func (s *Scanner) Tick(ctx context.Context) error {
	inboxDir := s.conventions.RawInboxDir
	if strings.TrimSpace(inboxDir) == "" {
		inboxDir = ScanInboxSubdir
	}
	absRoot := filepath.Join(s.vaultRoot, filepath.FromSlash(inboxDir))
	info, err := os.Stat(absRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat inbox: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("inbox %s is not a directory", absRoot)
	}
	cutoff := s.now().Add(-s.quietWindow)
	enqueued := 0
	err = filepath.Walk(absRoot, func(p string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				return nil
			}
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if fi.IsDir() {
			if strings.HasPrefix(fi.Name(), ".") && p != absRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		if strings.HasSuffix(fi.Name(), "_bucket.md") {
			return nil
		}
		// mtime quiet window: skip files the user is likely still editing.
		if !fi.ModTime().UTC().Before(cutoff) {
			return nil
		}
		rel, err := filepath.Rel(s.vaultRoot, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		// attempts ceiling: skip files that already burned through all retries.
		if existing, ok, qErr := s.queue.Get(rel); qErr == nil && ok {
			if existing.Attempts >= 5 || existing.State == StateRunning || existing.State == StateDone || existing.State == StateAttemptsExceeded {
				return nil
			}
		}

		// Read the file just enough to check the enriched_at marker and the
		// raw/bucket tag. Cheap (frontmatter is the file head); avoids
		// requeueing already-enriched notes.
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		content := string(data)
		if hasFrontmatterKey(content, "openwhisker_enriched_at") {
			return nil
		}
		if isBucketPath(rel, content) {
			return nil
		}
		if err := s.queue.Enqueue(rel, ""); err != nil {
			s.logf("enrich scan: enqueue %s: %v", rel, err)
			return nil
		}
		enqueued++
		return nil
	})
	if err != nil {
		return err
	}
	if enqueued > 0 {
		s.logf("enrich scan: enqueued=%d", enqueued)
	}
	return nil
}

func (s *Scanner) logf(format string, args ...any) {
	if s.logger == nil {
		return
	}
	fmt.Fprintf(s.logger, format+"\n", args...)
}
