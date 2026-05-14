package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	now         func() time.Time
}

type IngestRawRequest struct {
	Text   string `json:"text"`
	Source string `json:"source"`
}

type IngestRawResult struct {
	JobID      string   `json:"job_id"`
	PlanID     string   `json:"plan_id"`
	TargetPath string   `json:"target_path"`
	AfterHash  string   `json:"after_hash"`
	Messages   []string `json:"messages"`
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
		now:         func() time.Time { return time.Now().UTC() },
	}
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
	return result, nil
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
	fence := markdownFence(text)
	return fmt.Sprintf(`---
openwhisker_job_id: %s
openwhisker_job_type: %s
source: %s
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
`, job.ID, job.Type, job.Source, createdAt.Format(time.RFC3339), job.ID, job.Source,
		createdAt.Format(time.RFC3339), fence, text, fence)
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
