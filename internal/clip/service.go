package clip

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/markdown"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// EnrichEnqueuer schedules the §4 background tagging pass for a finished clip
// note. nil disables tagging (the clip still lands).
type EnrichEnqueuer interface {
	Enqueue(rawPath, parentJobID string) error
}

// Service owns the §3.2 link-clip pipeline: it creates the instant skeleton
// note on capture and, in the background, fetches the page, extracts the
// article, and rewrites the note — all through the VaultPlan -> policy ->
// executor spine, never writing the vault directly.
type Service struct {
	store       *storage.Store
	conventions policy.Conventions
	checker     policy.Checker
	exec        executor.DirectFS
	vaultRoot   string
	httpClient  *http.Client
	queue       *Queue
	enrich      EnrichEnqueuer
	now         func() time.Time
	// fetch is the page fetcher; overridable in tests (the SSRF guard blocks
	// loopback, so httptest servers can't be reached through the real fetch).
	fetch func(ctx context.Context, client *http.Client, rawURL string) (string, error)
}

// Config wires a clip Service. Store and VaultRoot are required.
type Config struct {
	Store       *storage.Store
	Conventions policy.Conventions
	VaultRoot   string
	Queue       *Queue
	Enrich      EnrichEnqueuer
	HTTPClient  *http.Client // optional; defaults to the SSRF-guarded client
}

func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("clip: store is required")
	}
	conv := cfg.Conventions.Normalize()
	client := cfg.HTTPClient
	if client == nil {
		client = newSafeClient()
	}
	queue := cfg.Queue
	if queue == nil {
		queue = NewQueue(0)
	}
	return &Service{
		store:       cfg.Store,
		conventions: conv,
		checker:     policy.NewCheckerWithConventions(conv),
		exec:        executor.NewDirectFS(cfg.VaultRoot, cfg.Store),
		vaultRoot:   cfg.VaultRoot,
		httpClient:  client,
		queue:       queue,
		enrich:      cfg.Enrich,
		now:         func() time.Time { return time.Now().UTC() },
		fetch:       fetchHTML,
	}, nil
}

func newSafeClient() *http.Client { return safeClient() }

// WithClock overrides the clock; tests pin it for deterministic timestamps.
func (s *Service) WithClock(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

// WithFetch overrides the page fetcher; used by tests to feed fixture HTML
// without a network round-trip (and without tripping the loopback SSRF guard).
func (s *Service) WithFetch(fn func(ctx context.Context, client *http.Client, rawURL string) (string, error)) *Service {
	if fn != nil {
		s.fetch = fn
	}
	return s
}

// Queue returns the service's clip queue so the worker can drain it.
func (s *Service) Queue() *Queue { return s.queue }

// Clip is the front-of-house entry: it writes the skeleton note immediately and
// enqueues the background fetch. It implements the core.ClipCapturer contract
// (notePath, jobID, error) so the adapter can ack "已记录，剪藏中" at once.
func (s *Service) Clip(ctx context.Context, rawURL, source, sourceKey string) (string, string, error) {
	if err := validateClipURL(rawURL); err != nil {
		return "", "", err
	}
	now := s.now()
	notePath, err := s.allocateNotePath(rawURL, now)
	if err != nil {
		return "", "", err
	}
	inputJSON, _ := json.Marshal(map[string]string{"url": rawURL})
	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeClipWeb,
		Status:    model.JobStatusPending,
		Source:    source,
		SourceKey: sourceKey,
		InputJSON: string(inputJSON),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return "", "", err
	}
	content := renderSkeleton(rawURL, source, now)
	plan := s.createPlan(job, notePath, content, "open web-clip skeleton", "Create the web-clip placeholder note before the background fetch.", now)
	if err := s.applyPlan(ctx, job.ID, plan); err != nil {
		return "", "", err
	}
	// Hand off to the background worker. A full buffer is fine: the worker's
	// scan recovers the still-"clipping" note.
	s.queue.Enqueue(Job{NotePath: notePath, URL: rawURL, ParentJobID: job.ID})
	return notePath, job.ID, nil
}

// ProcessClip performs the background work for one job: fetch, extract, rewrite
// the note under a content-hash guard, then schedule tagging and notify. A
// fetch/extract failure is recorded into the note (status: clip-failed) and
// surfaced via outbox rather than left dangling.
func (s *Service) ProcessClip(ctx context.Context, job Job) error {
	existing, err := s.readVaultFile(job.NotePath)
	if err != nil {
		return fmt.Errorf("clip: read skeleton %s: %w", job.NotePath, err)
	}
	doc := parseFrontmatter(existing)
	source := doc.Scalar("source")
	created := parseStamp(doc.Scalar("created"), s.now())
	now := s.now()

	htmlDoc, fetchErr := s.fetch(ctx, s.httpClient, job.URL)
	var content, summary string
	failed := fetchErr != nil
	if failed {
		content = renderFailed(job.URL, source, fetchErr.Error(), created, now)
		summary = fmt.Sprintf("剪藏失败：%s", job.URL)
	} else {
		article := Extract(htmlDoc)
		content = renderClipped(job.URL, source, article.Title, article.Body, created, now)
		title := article.Title
		if title == "" {
			title = job.URL
		}
		summary = fmt.Sprintf("剪藏完成：%s → %s", title, job.NotePath)
	}

	rewriteJob := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeClipWeb,
		Status:    model.JobStatusPending,
		Source:    source,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(rewriteJob); err != nil {
		return err
	}
	plan := s.rewritePlan(rewriteJob, job.NotePath, model.ContentHash([]byte(existing)), content, now)
	if err := s.applyPlan(ctx, rewriteJob.ID, plan); err != nil {
		return err
	}

	kind := model.OutboxKindResult
	if failed {
		kind = model.OutboxKindError
	}
	_ = s.store.AddOutboxMessage(model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     rewriteJob.ID,
		Kind:      kind,
		Body:      summary,
		Status:    model.OutboxStatusPending,
		CreatedAt: s.now(),
	})

	// §4 reuse: tag the finished clip in the background. Failed clips are not
	// worth tagging.
	if !failed && s.enrich != nil {
		_ = s.enrich.Enqueue(job.NotePath, job.ParentJobID)
	}
	return fetchErr
}

// allocateNotePath builds a collision-free clip note path under RawSourcesDir.
func (s *Service) allocateNotePath(rawURL string, now time.Time) (string, error) {
	base := fmt.Sprintf("%s/%s-%s", s.conventions.RawSourcesDir, now.Format("2006-01-02"), clipSlug(rawURL))
	for n := 0; n < 100; n++ {
		candidate := base + ".md"
		if n > 0 {
			candidate = fmt.Sprintf("%s-%d.md", base, n+1)
		}
		abs, err := executor.ResolveVaultPath(s.vaultRoot, candidate)
		if err != nil {
			return "", err
		}
		if _, statErr := os.Stat(abs); os.IsNotExist(statErr) {
			return candidate, nil
		}
	}
	return "", errors.New("clip: could not allocate a unique note path")
}

func (s *Service) createPlan(job model.WikiJob, target, content, purpose, reason string, now time.Time) model.VaultPlan {
	payload, _ := json.Marshal(model.CreateNotePayload{Content: content})
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          purpose,
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Summary:          purpose,
		SourceRefs:       []string{job.ID},
		TargetPaths:      []string{target},
		Operations: []model.VaultOperation{{
			ID:          model.NewID("op"),
			Type:        model.OperationCreateNote,
			TargetPath:  target,
			PayloadJSON: string(payload),
			Reason:      reason,
			RiskLevel:   model.RiskLow,
		}},
		Status:    model.PlanStatusProposed,
		CreatedAt: now,
	}
}

func (s *Service) rewritePlan(job model.WikiJob, target, beforeHash, content string, now time.Time) model.VaultPlan {
	payload, _ := json.Marshal(model.CreateNotePayload{Content: content})
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          "write web-clip content",
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Summary:          "Rewrite the web-clip note with the fetched article under a hash guard.",
		SourceRefs:       []string{job.ID, target},
		TargetPaths:      []string{target},
		Operations: []model.VaultOperation{{
			ID:          model.NewID("op"),
			Type:        model.OperationRewriteNote,
			TargetPath:  target,
			BeforeHash:  beforeHash,
			PayloadJSON: string(payload),
			Reason:      "Replace the clip placeholder with the extracted article under a content-hash guard.",
			RiskLevel:   model.RiskLow,
		}},
		Status:    model.PlanStatusProposed,
		CreatedAt: now,
	}
}

func (s *Service) applyPlan(ctx context.Context, jobID string, plan model.VaultPlan) error {
	if err := s.store.SavePlan(plan); err != nil {
		return s.fail(jobID, err)
	}
	if err := s.checker.Check(plan); err != nil {
		return s.fail(jobID, err)
	}
	if err := s.store.UpdateJobStatus(jobID, model.JobStatusApplying, "", ""); err != nil {
		return err
	}
	if _, err := s.exec.Apply(ctx, plan); err != nil {
		_ = s.store.UpdatePlanStatus(plan.ID, model.PlanStatusFailed, nil)
		return s.fail(jobID, err)
	}
	appliedAt := s.now()
	if err := s.store.UpdatePlanStatus(plan.ID, model.PlanStatusApplied, &appliedAt); err != nil {
		return s.fail(jobID, err)
	}
	return s.store.UpdateJobStatus(jobID, model.JobStatusDone, "", "")
}

func (s *Service) fail(jobID string, cause error) error {
	_ = s.store.UpdateJobStatus(jobID, model.JobStatusFailed, "", cause.Error())
	return cause
}

func (s *Service) readVaultFile(relPath string) (string, error) {
	abs, err := executor.ResolveVaultPath(s.vaultRoot, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func parseFrontmatter(content string) markdown.Doc {
	block, _, ok := markdown.SplitFrontmatter(content)
	if !ok {
		return markdown.Parse("")
	}
	return markdown.Parse(block)
}

func parseStamp(value string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return fallback
}
