package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type IngestService struct {
	store       *storage.Store
	checker     policy.Checker
	executor    executor.DirectFS
	conventions policy.Conventions
	vaultRoot   string
	now         func() time.Time
	enrichHook  EnrichEnqueuer
}

// EnrichEnqueuer is the dependency-inverted interface IngestService uses to
// schedule Phase 7 enrich jobs without importing internal/enrich (which
// would create a cycle: enrich → core → enrich). cmd wires the concrete
// enrich.Queue into the service at construction time. nil disables the
// hook (tests and legacy paths).
type EnrichEnqueuer interface {
	Enqueue(rawPath, parentJobID string) error
}

type IngestRawRequest struct {
	Text           string `json:"text"`
	Source         string `json:"source"`
	SourceKey      string `json:"source_key,omitempty"`
	BucketID       string `json:"bucket_id,omitempty"`
	SuppressOutbox bool   `json:"suppress_outbox,omitempty"`
	// NoEnrich is set by the Matrix /no-enrich one-shot toggle. Other
	// callers leave it false; the enrich hook also respects bucket-id and
	// source_key blocklists configured on the enqueuer.
	NoEnrich bool `json:"no_enrich,omitempty"`
}

type IngestRawResult struct {
	JobID      string   `json:"job_id"`
	PlanID     string   `json:"plan_id"`
	TargetPath string   `json:"target_path"`
	AfterHash  string   `json:"after_hash"`
	Messages   []string `json:"messages"`
}

type CreateRawBucketRequest struct {
	Text           string `json:"text"`
	Source         string `json:"source"`
	SourceKey      string `json:"source_key"`
	SuppressOutbox bool   `json:"suppress_outbox,omitempty"`
}

type AppendRawBucketRequest struct {
	SourceKey string `json:"source_key"`
	BucketID  string `json:"bucket_id"`
	Text      string `json:"text"`
	Source    string `json:"source"`
}

type CloseRawBucketRequest struct {
	SourceKey string
	BucketID  string
	Reason    string
}

func NewIngestService(store *storage.Store, vaultRoot string) IngestService {
	return NewIngestServiceWithConventions(store, vaultRoot, policy.DefaultConventions())
}

func NewIngestServiceWithConventions(store *storage.Store, vaultRoot string, conventions policy.Conventions) IngestService {
	conventions = conventions.Normalize()
	return IngestService{
		store:       store,
		checker:     policy.NewCheckerWithConventions(conventions),
		executor:    executor.NewDirectFS(vaultRoot, store),
		conventions: conventions,
		vaultRoot:   vaultRoot,
		now:         func() time.Time { return time.Now().UTC() },
	}
}

// WithEnrichHook attaches a Phase 7 enrich enqueuer. The hook fires after a
// successful low-risk Raw capture; bucket-related ingestions (BucketID set)
// and explicit /no-enrich requests are skipped.
func (s IngestService) WithEnrichHook(h EnrichEnqueuer) IngestService {
	s.enrichHook = h
	return s
}

func (s IngestService) IngestRaw(ctx context.Context, req IngestRawRequest) (IngestRawResult, error) {
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return IngestRawResult{}, errors.New("raw text is required")
	}
	if req.Source == "" {
		req.Source = "cli"
	}

	now := s.now()
	inputJSON, err := json.Marshal(req)
	if err != nil {
		return IngestRawResult{}, err
	}
	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeIngestRaw,
		Status:    model.JobStatusPending,
		Source:    req.Source,
		SourceKey: req.SourceKey,
		InputJSON: string(inputJSON),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return IngestRawResult{}, err
	}

	plan, err := s.buildRawPlan(job, req.Text, now)
	if err != nil {
		_ = s.failJob(job.ID, err)
		return IngestRawResult{}, err
	}
	if err := s.store.SavePlan(plan); err != nil {
		_ = s.failJob(job.ID, err)
		return IngestRawResult{}, err
	}
	if err := s.checker.Check(plan); err != nil {
		_ = s.failJob(job.ID, err)
		return IngestRawResult{}, err
	}
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusApplying, "", ""); err != nil {
		return IngestRawResult{}, err
	}
	applyResult, err := s.executor.Apply(ctx, plan)
	if err != nil {
		_ = s.store.UpdatePlanStatus(plan.ID, model.PlanStatusFailed, nil)
		_ = s.failJob(job.ID, err)
		return IngestRawResult{}, err
	}
	appliedAt := s.now()
	if err := s.store.UpdatePlanStatus(plan.ID, model.PlanStatusApplied, &appliedAt); err != nil {
		_ = s.failJob(job.ID, err)
		return IngestRawResult{}, err
	}

	result := IngestRawResult{
		JobID:    job.ID,
		PlanID:   plan.ID,
		Messages: []string{"raw capture applied"},
	}
	if len(applyResult.AppliedOperations) > 0 {
		result.TargetPath = applyResult.AppliedOperations[0].TargetPath
		result.AfterHash = applyResult.AppliedOperations[0].AfterHash
	}
	resultJSON, _ := json.Marshal(result)
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusDone, string(resultJSON), ""); err != nil {
		return IngestRawResult{}, err
	}
	if !req.SuppressOutbox {
		if err := s.store.AddOutboxMessage(model.OutboxMessage{
			ID:        model.NewID("out"),
			JobID:     job.ID,
			Kind:      model.OutboxKindResult,
			Body:      fmt.Sprintf("Raw capture saved to %s", result.TargetPath),
			Status:    model.OutboxStatusPending,
			CreatedAt: s.now(),
		}); err != nil {
			return IngestRawResult{}, err
		}
	}
	// Phase 7: enqueue an enrich job after Apply + outbox. CaptureBucket
	// ingestions (BucketID set) and explicit /no-enrich requests are
	// skipped (decision 2 + Matrix one-shot). Errors are logged via outbox
	// but never roll back the successful Raw capture.
	if s.enrichHook != nil && !req.NoEnrich && strings.TrimSpace(req.BucketID) == "" && strings.TrimSpace(result.TargetPath) != "" {
		if hookErr := s.enrichHook.Enqueue(result.TargetPath, job.ID); hookErr != nil {
			_ = s.store.AddOutboxMessage(model.OutboxMessage{
				ID:        model.NewID("out"),
				JobID:     job.ID,
				Kind:      model.OutboxKindError,
				Body:      fmt.Sprintf("enrich enqueue: %v", hookErr),
				Status:    model.OutboxStatusPending,
				CreatedAt: s.now(),
			})
		}
	}
	return result, nil
}

func (s IngestService) CreateRawBucket(ctx context.Context, req CreateRawBucketRequest) (model.CaptureBucket, IngestRawResult, error) {
	req.Text = strings.TrimSpace(req.Text)
	req.SourceKey = strings.TrimSpace(req.SourceKey)
	if req.Text == "" {
		return model.CaptureBucket{}, IngestRawResult{}, errors.New("raw text is required")
	}
	if req.SourceKey == "" {
		return model.CaptureBucket{}, IngestRawResult{}, errors.New("source_key is required")
	}
	now := s.now()
	bucketID := model.NewID("bucket")
	result, err := s.IngestRaw(ctx, IngestRawRequest{
		Text:           req.Text,
		Source:         req.Source,
		SourceKey:      req.SourceKey,
		BucketID:       bucketID,
		SuppressOutbox: req.SuppressOutbox,
	})
	if err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	bucket := model.CaptureBucket{
		ID:                     bucketID,
		SourceKey:              req.SourceKey,
		RawJobID:               result.JobID,
		RawPlanID:              result.PlanID,
		RawPath:                result.TargetPath,
		Status:                 model.CaptureBucketStatusActive,
		TopicHint:              topicHint(req.Text),
		Excerpt:                excerpt(req.Text),
		AppendCount:            1,
		RawHashAfterLastAppend: result.AfterHash,
		StartedAt:              now,
		UpdatedAt:              now,
		ExpiresAt:              now.Add(captureBucketTTL()),
	}
	if err := s.store.SaveCaptureBucket(bucket); err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	return bucket, result, nil
}

func (s IngestService) AppendRawBucket(ctx context.Context, req AppendRawBucketRequest) (model.CaptureBucket, IngestRawResult, error) {
	req.Text = strings.TrimSpace(req.Text)
	req.SourceKey = strings.TrimSpace(req.SourceKey)
	req.BucketID = strings.TrimSpace(req.BucketID)
	if req.Text == "" {
		return model.CaptureBucket{}, IngestRawResult{}, errors.New("raw text is required")
	}
	if req.SourceKey == "" || req.BucketID == "" {
		return model.CaptureBucket{}, IngestRawResult{}, errors.New("source_key and bucket_id are required")
	}
	bucket, err := s.store.GetCaptureBucket(req.SourceKey, req.BucketID)
	if err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	now := s.now()
	if bucket.Status != model.CaptureBucketStatusActive {
		return model.CaptureBucket{}, IngestRawResult{}, fmt.Errorf("bucket %s is %s, cannot append", bucket.ID, bucket.Status)
	}
	if !bucket.ExpiresAt.IsZero() && now.After(bucket.ExpiresAt) {
		bucket.Status = model.CaptureBucketStatusExpired
		bucket.UpdatedAt = now
		closedAt := now
		bucket.ClosedAt = &closedAt
		bucket.CloseReason = "ttl expired"
		_ = s.store.SaveCaptureBucket(bucket)
		return model.CaptureBucket{}, IngestRawResult{}, fmt.Errorf("bucket %s expired", bucket.ID)
	}
	current, err := s.readVaultFile(bucket.RawPath)
	if err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	if currentHash := sha256String(current); currentHash != bucket.RawHashAfterLastAppend {
		bucket.Status = model.CaptureBucketStatusHashMismatch
		bucket.UpdatedAt = now
		closedAt := now
		bucket.ClosedAt = &closedAt
		bucket.CloseReason = "raw hash mismatch"
		_ = s.store.SaveCaptureBucket(bucket)
		return model.CaptureBucket{}, IngestRawResult{}, fmt.Errorf("bucket %s raw hash mismatch", bucket.ID)
	}
	inputJSON, err := json.Marshal(req)
	if err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeAppendRaw,
		Status:    model.JobStatusPending,
		Source:    coalesceSource(req.Source, "adapter"),
		SourceKey: req.SourceKey,
		InputJSON: string(inputJSON),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	appendBlock := renderRawBucketAppendBlock(bucket.AppendCount+1, req.Text, job.Source, now)
	nextContent := rewriteRawBucketUpdated(current, now) + appendBlock
	payload, err := json.Marshal(model.CreateNotePayload{Content: nextContent})
	if err != nil {
		_ = s.failJob(job.ID, err)
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	plan := model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          "append raw bucket text",
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Summary:          "Rewrite one active raw bucket with updated metadata and one appended source block.",
		SourceRefs:       []string{bucket.RawJobID, bucket.RawPath, bucket.ID},
		TargetPaths:      []string{bucket.RawPath},
		Operations: []model.VaultOperation{{
			ID:          model.NewID("op"),
			Type:        model.OperationRewriteNote,
			TargetPath:  bucket.RawPath,
			BeforeHash:  bucket.RawHashAfterLastAppend,
			PayloadJSON: string(payload),
			Reason:      "Append text to the active raw bucket and update raw metadata under a source-scoped hash guard.",
			RiskLevel:   model.RiskLow,
		}},
		Status:    model.PlanStatusProposed,
		CreatedAt: now,
	}
	if err := s.store.SavePlan(plan); err != nil {
		_ = s.failJob(job.ID, err)
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	if err := s.checker.Check(plan); err != nil {
		_ = s.failJob(job.ID, err)
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusApplying, "", ""); err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	applyResult, err := s.executor.Apply(ctx, plan)
	if err != nil {
		_ = s.store.UpdatePlanStatus(plan.ID, model.PlanStatusFailed, nil)
		_ = s.failJob(job.ID, err)
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	appliedAt := s.now()
	if err := s.store.UpdatePlanStatus(plan.ID, model.PlanStatusApplied, &appliedAt); err != nil {
		_ = s.failJob(job.ID, err)
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	result := IngestRawResult{
		JobID:      job.ID,
		PlanID:     plan.ID,
		TargetPath: bucket.RawPath,
		Messages:   []string{"raw bucket appended"},
	}
	if len(applyResult.AppliedOperations) > 0 {
		result.AfterHash = applyResult.AppliedOperations[0].AfterHash
	}
	bucket.AppendCount++
	bucket.Excerpt = excerpt(bucket.Excerpt + "\n" + req.Text)
	bucket.RawHashAfterLastAppend = result.AfterHash
	bucket.UpdatedAt = appliedAt
	bucket.ExpiresAt = appliedAt.Add(captureBucketTTL())
	if err := s.store.SaveCaptureBucket(bucket); err != nil {
		_ = s.failJob(job.ID, err)
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	resultJSON, _ := json.Marshal(result)
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusDone, string(resultJSON), ""); err != nil {
		return model.CaptureBucket{}, IngestRawResult{}, err
	}
	return bucket, result, nil
}

func (s IngestService) CloseRawBucket(req CloseRawBucketRequest) (model.CaptureBucket, error) {
	req.SourceKey = strings.TrimSpace(req.SourceKey)
	req.BucketID = strings.TrimSpace(req.BucketID)
	if req.SourceKey == "" || req.BucketID == "" {
		return model.CaptureBucket{}, errors.New("source_key and bucket_id are required")
	}
	bucket, err := s.store.GetCaptureBucket(req.SourceKey, req.BucketID)
	if err != nil {
		return model.CaptureBucket{}, err
	}
	if bucket.Status != model.CaptureBucketStatusActive {
		return bucket, nil
	}
	now := s.now()
	bucket.Status = model.CaptureBucketStatusClosed
	bucket.UpdatedAt = now
	bucket.CloseReason = strings.TrimSpace(req.Reason)
	if bucket.CloseReason == "" {
		bucket.CloseReason = "closed by user"
	}
	bucket.ClosedAt = &now
	return bucket, s.store.SaveCaptureBucket(bucket)
}

func (s IngestService) buildRawPlan(job model.WikiJob, text string, now time.Time) (model.VaultPlan, error) {
	targetPath := fmt.Sprintf("%s/%s.md", s.conventions.RawInboxDir, job.ID)
	payload, err := json.Marshal(model.CreateNotePayload{
		Content: renderRawNote(job, text, now),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	op := model.VaultOperation{
		ID:          model.NewID("op"),
		Type:        model.OperationCreateNote,
		TargetPath:  targetPath,
		PayloadJSON: string(payload),
		Reason:      "Capture raw text into the low-risk inbox before any knowledge processing.",
		RiskLevel:   model.RiskLow,
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          "ingest raw text",
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Summary:          "Create one raw inbox note from CLI input.",
		SourceRefs:       []string{job.ID},
		TargetPaths:      []string{targetPath},
		Operations:       []model.VaultOperation{op},
		Status:           model.PlanStatusProposed,
		CreatedAt:        now,
	}, nil
}

func (s IngestService) readVaultFile(relPath string) (string, error) {
	fullPath, err := executor.ResolveVaultPath(s.vaultRoot, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s IngestService) failJob(jobID string, err error) error {
	_ = s.store.AddOutboxMessage(model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     jobID,
		Kind:      model.OutboxKindError,
		Body:      err.Error(),
		Status:    model.OutboxStatusPending,
		CreatedAt: s.now(),
	})
	return s.store.UpdateJobStatus(jobID, model.JobStatusFailed, "", err.Error())
}

func renderRawNote(job model.WikiJob, text string, createdAt time.Time) string {
	var req IngestRawRequest
	if err := json.Unmarshal([]byte(job.InputJSON), &req); err == nil && strings.TrimSpace(req.BucketID) != "" {
		return renderRawBucketNote(job, req.BucketID, text, createdAt)
	}
	fence := markdownFence(text)
	return fmt.Sprintf(`---
openwhisker_job_id: %s
openwhisker_job_type: %s
source: %s
source_key: %s
created_at: %s
status: raw
---

# Raw Capture

## Source Trace

- Job: %s
- Source: %s
- Captured at: %s

## Raw Text

%stext
%s
%s
`, job.ID, job.Type, job.Source, job.SourceKey, createdAt.Format(time.RFC3339), job.ID, job.Source,
		createdAt.Format(time.RFC3339), fence, text, fence)
}

func renderRawBucketNote(job model.WikiJob, bucketID, text string, createdAt time.Time) string {
	title := "Raw Capture - " + createdAt.Format("2006-01-02 15:04")
	return fmt.Sprintf(`---
title: %q
status: inbox
source: %s
source_key: %q
created: %s
updated: %s
openwhisker_bucket_id: %s
openwhisker_job_id: %s
---

# %s
%s`, title, job.Source, job.SourceKey, createdAt.Format(time.RFC3339), createdAt.Format(time.RFC3339),
		bucketID, job.ID, title, renderRawBucketAppendBlock(1, text, job.Source, createdAt))
}

func renderRawBucketAppendBlock(index int, text, source string, createdAt time.Time) string {
	fence := markdownFence(text)
	return fmt.Sprintf(`
## 输入 %d

- 类型：text
- 时间：%s
- 来源：%s

%stext
%s
%s
`, index, createdAt.Format(time.RFC3339), source, fence, text, fence)
}

func rewriteRawBucketUpdated(content string, updatedAt time.Time) string {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	closing := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			closing = i
			break
		}
	}
	if closing == -1 {
		return content
	}
	updatedLine := "updated: " + updatedAt.Format(time.RFC3339) + "\n"
	for i := 1; i < closing; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "updated:") {
			lines[i] = updatedLine
			return strings.Join(lines, "")
		}
	}
	next := make([]string, 0, len(lines)+1)
	next = append(next, lines[:closing]...)
	next = append(next, updatedLine)
	next = append(next, lines[closing:]...)
	return strings.Join(next, "")
}

func captureBucketTTL() time.Duration {
	value := strings.TrimSpace(os.Getenv("OPENWHISKER_CAPTURE_BUCKET_TTL"))
	if value == "" {
		return 15 * time.Minute
	}
	ttl, err := time.ParseDuration(value)
	if err != nil || ttl <= 0 {
		return 15 * time.Minute
	}
	return ttl
}

func coalesceSource(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func topicHint(text string) string {
	return truncateCaptureRunes(strings.TrimSpace(firstLine(text)), 40)
}

func excerpt(text string) string {
	return truncateCaptureRunes(strings.TrimSpace(text), 200)
}

func firstLine(text string) string {
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			return strings.TrimSpace(line)
		}
	}
	return strings.TrimSpace(text)
}

func truncateCaptureRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

func sha256String(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func markdownFence(text string) string {
	longest := 2
	current := 0
	for _, r := range text {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	return strings.Repeat("`", longest+1)
}
