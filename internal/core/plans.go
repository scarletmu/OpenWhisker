package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type PlanService struct {
	store      *storage.Store
	checker    policy.Checker
	executor   executor.DirectFS
	syncClient executor.SyncClient
	syncMode   string
	vaultRoot  string
	organizer  RawOrganizer
	now        func() time.Time
}

type PlanServiceOptions struct {
	SyncMode    string
	SyncBackend string
	SyncClient  executor.SyncClient
	Organizer   RawOrganizer
}

type RawOrganizer interface {
	OrganizeRaw(context.Context, RawOrganizerRequest) (model.VaultPlan, error)
}

type RawTodayOrganizer interface {
	OrganizeRawToday(context.Context, RawTodayOrganizerRequest) (model.VaultPlan, error)
}

type RawOrganizerRequest struct {
	Job          model.WikiJob
	RawJob       model.WikiJob
	RawPath      string
	VaultRoot    string
	VaultContext RawOrganizerContext
	Now          time.Time
}

type RawTodayOrganizerRequest struct {
	Job          model.WikiJob
	RawJobs      []model.WikiJob
	RawPaths     []string
	VaultRoot    string
	VaultContext []RawOrganizerContext
	Day          time.Time
	Now          time.Time
}

type RawOrganizerContext struct {
	RawPath   string
	RawNote   string
	Documents []VaultContextDocument
}

type VaultContextDocument struct {
	Path    string
	Content string
}

type OrganizeRawResult struct {
	JobID       string           `json:"job_id"`
	PlanID      string           `json:"plan_id"`
	Status      string           `json:"status"`
	RawJobIDs   []string         `json:"raw_job_ids,omitempty"`
	TargetPaths []string         `json:"target_paths"`
	Diff        *model.VaultDiff `json:"diff,omitempty"`
	Messages    []string         `json:"messages"`
}

type PlanActionResult struct {
	JobID      string            `json:"job_id"`
	PlanID     string            `json:"plan_id"`
	Status     string            `json:"status"`
	SyncBefore *model.SyncResult `json:"sync_before,omitempty"`
	SyncAfter  *model.SyncResult `json:"sync_after,omitempty"`
	Messages   []string          `json:"messages"`
}

func NewPlanService(store *storage.Store, vaultRoot string) PlanService {
	return NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		SyncMode:   model.SyncModeOff,
		SyncClient: executor.NoopSyncClient{},
	})
}

func NewPlanServiceWithOptions(store *storage.Store, vaultRoot string, opts PlanServiceOptions) PlanService {
	syncMode := opts.SyncMode
	if syncMode == "" || syncMode == model.SyncModeAuto {
		syncMode = model.SyncModeOff
	}
	syncClient := opts.SyncClient
	if syncClient == nil {
		if syncMode == model.SyncModeOn {
			syncClient = executor.HeadlessSyncClient{}
		} else {
			syncClient = executor.NoopSyncClient{}
		}
	}
	return PlanService{
		store:      store,
		checker:    policy.NewChecker(),
		executor:   executor.NewDirectFS(vaultRoot, store),
		syncClient: syncClient,
		syncMode:   syncMode,
		vaultRoot:  vaultRoot,
		organizer:  defaultRawOrganizer(opts.Organizer),
		now:        func() time.Time { return time.Now().UTC() },
	}
}

func (s PlanService) OrganizeLast(ctx context.Context) (OrganizeRawResult, error) {
	rawJob, err := s.store.LatestDoneIngestRawJob()
	if err != nil {
		return OrganizeRawResult{}, err
	}
	rawPath := rawTargetPath(rawJob)
	if rawPath == "" {
		return OrganizeRawResult{}, fmt.Errorf("raw job %s has no target path", rawJob.ID)
	}

	now := s.now()
	inputJSON, err := json.Marshal(map[string]string{
		"raw_job_id": rawJob.ID,
		"raw_path":   rawPath,
		"planner":    "deterministic_phase_2",
	})
	if err != nil {
		return OrganizeRawResult{}, err
	}
	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeOrganizeRaw,
		Status:    model.JobStatusPending,
		Source:    "cli",
		InputJSON: string(inputJSON),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return OrganizeRawResult{}, err
	}

	vaultContext, err := buildRawOrganizerContext(ctx, s.vaultRoot, rawPath)
	if err != nil {
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	plan, err := s.organizer.OrganizeRaw(ctx, RawOrganizerRequest{
		Job:          job,
		RawJob:       rawJob,
		RawPath:      rawPath,
		VaultRoot:    s.vaultRoot,
		VaultContext: vaultContext,
		Now:          now,
	})
	if err != nil {
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	if err := s.checker.CheckForApproval(plan); err != nil {
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	if err := s.store.SavePlan(plan); err != nil {
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	prepared, err := s.executor.Prepare(ctx, plan)
	if err != nil {
		_ = s.store.MarkPlanFailed(plan.ID, model.PlanStatusFailed, err.Error())
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	prepared.Status = model.PlanStatusAwaitingApproval
	preparedAt := s.now()
	prepared.PreparedAt = &preparedAt
	if err := s.store.UpdatePlanPrepared(prepared); err != nil {
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	if err := s.store.UpdateJobStatus(job.ID, model.JobStatusAwaitingApproval, "", ""); err != nil {
		return OrganizeRawResult{}, err
	}
	if err := s.store.AddOutboxMessage(model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     job.ID,
		Kind:      model.OutboxKindApproval,
		Body:      fmt.Sprintf("Plan %s awaits approval: %s", prepared.ID, prepared.Summary),
		Status:    model.OutboxStatusPending,
		CreatedAt: s.now(),
	}); err != nil {
		return OrganizeRawResult{}, err
	}
	return OrganizeRawResult{
		JobID:       job.ID,
		PlanID:      prepared.ID,
		Status:      prepared.Status,
		TargetPaths: prepared.TargetPaths,
		Diff:        prepared.Diff,
		Messages:    []string{"plan prepared and awaiting approval"},
	}, nil
}

func (s PlanService) OrganizeToday(ctx context.Context, day time.Time) (OrganizeRawResult, error) {
	day = normalizeDay(day)
	start, end := dayBounds(day)
	dayLabel := day.Format("2006-01-02")
	rawJobs, err := s.store.ListDoneIngestRawJobsCreatedBetween(start, end, 100)
	if err != nil {
		return OrganizeRawResult{}, err
	}
	rawJobs, rawPaths, err := s.filterExistingInboxRawJobs(rawJobs)
	if err != nil {
		return OrganizeRawResult{}, err
	}
	if len(rawJobs) == 0 {
		return OrganizeRawResult{}, fmt.Errorf("no unprocessed raw captures found for %s", dayLabel)
	}
	now := s.now()
	rawJobIDs := jobIDs(rawJobs)
	inputJSON, err := json.Marshal(map[string]any{
		"date":        dayLabel,
		"raw_job_ids": rawJobIDs,
		"raw_paths":   rawPaths,
		"planner":     "raw_today_grouped",
	})
	if err != nil {
		return OrganizeRawResult{}, err
	}
	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeOrganizeRawToday,
		Status:    model.JobStatusPending,
		Source:    "cli",
		InputJSON: string(inputJSON),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return OrganizeRawResult{}, err
	}
	var contexts []RawOrganizerContext
	for _, rawPath := range rawPaths {
		vaultContext, err := buildRawOrganizerContext(ctx, s.vaultRoot, rawPath)
		if err != nil {
			_ = s.failPlanJob(job.ID, err)
			return OrganizeRawResult{}, err
		}
		contexts = append(contexts, vaultContext)
	}
	organizer, ok := s.organizer.(RawTodayOrganizer)
	if !ok {
		organizer = deterministicRawOrganizer{}
	}
	plan, err := organizer.OrganizeRawToday(ctx, RawTodayOrganizerRequest{
		Job:          job,
		RawJobs:      rawJobs,
		RawPaths:     rawPaths,
		VaultRoot:    s.vaultRoot,
		VaultContext: contexts,
		Day:          day,
		Now:          now,
	})
	if err != nil {
		_ = s.failPlanJob(job.ID, err)
		return OrganizeRawResult{}, err
	}
	prepared, err := s.prepareApprovalPlan(ctx, job.ID, plan)
	if err != nil {
		return OrganizeRawResult{}, err
	}
	return OrganizeRawResult{
		JobID:       job.ID,
		PlanID:      prepared.ID,
		Status:      prepared.Status,
		RawJobIDs:   rawJobIDs,
		TargetPaths: prepared.TargetPaths,
		Diff:        prepared.Diff,
		Messages:    []string{"grouped plan prepared and awaiting approval"},
	}, nil
}

func (s PlanService) prepareApprovalPlan(ctx context.Context, jobID string, plan model.VaultPlan) (model.VaultPlan, error) {
	if err := s.checker.CheckForApproval(plan); err != nil {
		_ = s.failPlanJob(jobID, err)
		return model.VaultPlan{}, err
	}
	if err := s.store.SavePlan(plan); err != nil {
		_ = s.failPlanJob(jobID, err)
		return model.VaultPlan{}, err
	}
	prepared, err := s.executor.Prepare(ctx, plan)
	if err != nil {
		_ = s.store.MarkPlanFailed(plan.ID, model.PlanStatusFailed, err.Error())
		_ = s.failPlanJob(jobID, err)
		return model.VaultPlan{}, err
	}
	prepared.Status = model.PlanStatusAwaitingApproval
	preparedAt := s.now()
	prepared.PreparedAt = &preparedAt
	if err := s.store.UpdatePlanPrepared(prepared); err != nil {
		_ = s.failPlanJob(jobID, err)
		return model.VaultPlan{}, err
	}
	if err := s.store.UpdateJobStatus(jobID, model.JobStatusAwaitingApproval, "", ""); err != nil {
		return model.VaultPlan{}, err
	}
	if err := s.store.AddOutboxMessage(model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     jobID,
		Kind:      model.OutboxKindApproval,
		Body:      fmt.Sprintf("Plan %s awaits approval: %s", prepared.ID, prepared.Summary),
		Status:    model.OutboxStatusPending,
		CreatedAt: s.now(),
	}); err != nil {
		return model.VaultPlan{}, err
	}
	return prepared, nil
}

func (s PlanService) Diff(identifier string) (*model.VaultDiff, error) {
	plan, err := s.resolvePlan(identifier)
	if err != nil {
		return nil, err
	}
	if plan.Diff == nil {
		return nil, fmt.Errorf("plan %s has no prepared diff", plan.ID)
	}
	return plan.Diff, nil
}

func (s PlanService) Approve(ctx context.Context, identifier string) (PlanActionResult, error) {
	plan, err := s.resolvePlan(identifier)
	if err != nil {
		return PlanActionResult{}, err
	}
	if plan.Status != model.PlanStatusAwaitingApproval {
		return PlanActionResult{}, fmt.Errorf("plan %s status is %q, want awaiting_approval", plan.ID, plan.Status)
	}
	approvedAt := s.now()
	if err := s.store.ApprovePlan(plan.ID, approvedAt); err != nil {
		return PlanActionResult{}, err
	}
	plan.Status = model.PlanStatusApproved
	plan.ApprovedAt = &approvedAt
	if err := s.checker.CheckApproved(plan); err != nil {
		_ = s.store.MarkPlanFailed(plan.ID, model.PlanStatusFailed, err.Error())
		return PlanActionResult{}, err
	}
	if err := s.store.UpdateJobStatus(plan.JobID, model.JobStatusApplying, "", ""); err != nil {
		return PlanActionResult{}, err
	}
	if err := s.store.UpdatePlanStatus(plan.ID, model.PlanStatusApplying, nil); err != nil {
		return PlanActionResult{}, err
	}
	plan.Status = model.PlanStatusApplying
	var syncBefore *model.SyncResult
	if s.syncMode == model.SyncModeOn {
		result, err := s.syncClient.Sync(ctx, s.vaultRoot, model.SyncPhaseBefore)
		syncBefore = &result
		if err != nil {
			_ = s.store.MarkPlanFailed(plan.ID, model.PlanStatusFailed, err.Error())
			resultJSON, _ := json.Marshal(model.VaultApplyResult{SyncBefore: syncBefore})
			_ = s.store.UpdateJobStatus(plan.JobID, model.JobStatusFailed, string(resultJSON), err.Error())
			_ = s.store.AddOutboxMessage(model.OutboxMessage{
				ID:        model.NewID("out"),
				JobID:     plan.JobID,
				Kind:      model.OutboxKindError,
				Body:      fmt.Sprintf("Pre-sync failed for plan %s: %s", plan.ID, err.Error()),
				Status:    model.OutboxStatusPending,
				CreatedAt: s.now(),
			})
			return PlanActionResult{}, err
		}
	}
	applyResult, err := s.executor.Apply(ctx, plan)
	applyResult.SyncBefore = syncBefore
	if err != nil {
		status := model.PlanStatusFailed
		kind := model.OutboxKindError
		if executor.IsConflict(err) {
			status = model.PlanStatusConflict
			kind = model.OutboxKindConflict
		}
		_ = s.store.MarkPlanFailed(plan.ID, status, err.Error())
		_ = s.store.UpdateJobStatus(plan.JobID, model.JobStatusFailed, "", err.Error())
		_ = s.store.AddOutboxMessage(model.OutboxMessage{
			ID:        model.NewID("out"),
			JobID:     plan.JobID,
			Kind:      kind,
			Body:      err.Error(),
			Status:    model.OutboxStatusPending,
			CreatedAt: s.now(),
		})
		return PlanActionResult{}, err
	}
	var syncAfter *model.SyncResult
	var warning string
	if s.syncMode == model.SyncModeOn {
		result, err := s.syncClient.Sync(ctx, s.vaultRoot, model.SyncPhaseAfter)
		syncAfter = &result
		if err != nil {
			warning = err.Error()
			syncAfter.Warning = warning
		}
		applyResult.SyncAfter = syncAfter
	}
	appliedAt := s.now()
	if err := s.store.UpdatePlanStatus(plan.ID, model.PlanStatusApplied, &appliedAt); err != nil {
		return PlanActionResult{}, err
	}
	resultJSON, _ := json.Marshal(applyResult)
	if err := s.store.UpdateJobStatus(plan.JobID, model.JobStatusDone, string(resultJSON), ""); err != nil {
		return PlanActionResult{}, err
	}
	if err := s.store.AddOutboxMessage(model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     plan.JobID,
		Kind:      model.OutboxKindResult,
		Body:      planAppliedMessage(plan.ID, warning),
		Status:    model.OutboxStatusPending,
		CreatedAt: s.now(),
	}); err != nil {
		return PlanActionResult{}, err
	}
	messages := []string{"plan approved and applied"}
	if warning != "" {
		messages = append(messages, "post-sync warning: "+warning)
	}
	return PlanActionResult{
		JobID:      plan.JobID,
		PlanID:     plan.ID,
		Status:     model.PlanStatusApplied,
		SyncBefore: syncBefore,
		SyncAfter:  syncAfter,
		Messages:   messages,
	}, nil
}

func (s PlanService) Reject(identifier, reason string) (PlanActionResult, error) {
	plan, err := s.resolvePlan(identifier)
	if err != nil {
		return PlanActionResult{}, err
	}
	if plan.Status != model.PlanStatusAwaitingApproval {
		return PlanActionResult{}, fmt.Errorf("plan %s status is %q, want awaiting_approval", plan.ID, plan.Status)
	}
	if strings.TrimSpace(reason) == "" {
		reason = "rejected by user"
	}
	rejectedAt := s.now()
	if err := s.store.RejectPlan(plan.ID, reason, rejectedAt); err != nil {
		return PlanActionResult{}, err
	}
	if err := s.store.UpdateJobStatus(plan.JobID, model.JobStatusRejected, "", reason); err != nil {
		return PlanActionResult{}, err
	}
	if err := s.store.AddOutboxMessage(model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     plan.JobID,
		Kind:      model.OutboxKindRejected,
		Body:      fmt.Sprintf("Plan %s rejected: %s", plan.ID, reason),
		Status:    model.OutboxStatusPending,
		CreatedAt: s.now(),
	}); err != nil {
		return PlanActionResult{}, err
	}
	return PlanActionResult{
		JobID:    plan.JobID,
		PlanID:   plan.ID,
		Status:   model.PlanStatusRejected,
		Messages: []string{"plan rejected"},
	}, nil
}

type deterministicRawOrganizer struct{}

func defaultRawOrganizer(organizer RawOrganizer) RawOrganizer {
	if organizer != nil {
		return organizer
	}
	return deterministicRawOrganizer{}
}

func (deterministicRawOrganizer) OrganizeRaw(ctx context.Context, req RawOrganizerRequest) (model.VaultPlan, error) {
	if err := ctx.Err(); err != nil {
		return model.VaultPlan{}, err
	}
	return buildOrganizePlan(req.Job, req.RawJob, req.RawPath, req.Now)
}

func (deterministicRawOrganizer) OrganizeRawToday(ctx context.Context, req RawTodayOrganizerRequest) (model.VaultPlan, error) {
	if err := ctx.Err(); err != nil {
		return model.VaultPlan{}, err
	}
	return buildOrganizeTodayPlan(req.Job, req.RawJobs, req.RawPaths, req.Day, req.Now, "Deterministic Raw Organizer grouped today's raw captures into one review-needed Knowledge draft.", renderTodayKnowledgeDraft(req, "Today's Raw Capture Review", "Deterministic grouped draft for today's raw captures. Review each source before promoting this into durable Knowledge."))
}

func buildOrganizePlan(job, rawJob model.WikiJob, rawPath string, now time.Time) (model.VaultPlan, error) {
	base := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))
	knowledgePath := "Knowledge/Drafts/" + base + ".md"
	processedPath := "Raw/Processed/" + base + ".md"
	createPayload, err := json.Marshal(model.CreateNotePayload{
		Content: renderKnowledgeDraft(job, rawJob, rawPath, processedPath, now),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	movePayload, err := json.Marshal(model.MoveNotePayload{
		DestinationPath: processedPath,
		ProcessingNote:  renderProcessedRawNote(job, rawJob, rawPath, processedPath, []string{knowledgePath}, now, "Deterministic Raw Organizer created one review-needed Knowledge draft."),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	operations := []model.VaultOperation{
		{
			ID:          model.NewID("op"),
			Type:        model.OperationCreateNote,
			TargetPath:  knowledgePath,
			PayloadJSON: string(createPayload),
			Reason:      "Create a reviewed Knowledge draft that links back to the raw source.",
			RiskLevel:   model.RiskMedium,
		},
		{
			ID:          model.NewID("op"),
			Type:        model.OperationMoveNote,
			TargetPath:  rawPath,
			PayloadJSON: string(movePayload),
			Reason:      "Mark the raw capture as processed only after the Knowledge draft is approved.",
			RiskLevel:   model.RiskMedium,
		},
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          "organize raw into Knowledge draft",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          "Create one Knowledge draft and move the raw capture to Raw/Processed.",
		SourceRefs:       []string{rawJob.ID, rawPath},
		TargetPaths:      []string{knowledgePath, rawPath, processedPath},
		Operations:       operations,
		Status:           model.PlanStatusProposed,
		CreatedAt:        now,
	}, nil
}

func buildOrganizeTodayPlan(job model.WikiJob, rawJobs []model.WikiJob, rawPaths []string, day, now time.Time, processingNote, content string) (model.VaultPlan, error) {
	if len(rawJobs) == 0 || len(rawJobs) != len(rawPaths) {
		return model.VaultPlan{}, errors.New("raw today plan requires matching raw jobs and paths")
	}
	date := day.Format("2006-01-02")
	knowledgePath := fmt.Sprintf("Knowledge/Drafts/%s-raw-review-%s.md", date, job.ID)
	var processedPaths []string
	for _, rawPath := range rawPaths {
		base := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))
		processedPaths = append(processedPaths, "Raw/Processed/"+base+".md")
	}
	createPayload, err := json.Marshal(model.CreateNotePayload{Content: content})
	if err != nil {
		return model.VaultPlan{}, err
	}
	operations := []model.VaultOperation{{
		ID:          model.NewID("op"),
		Type:        model.OperationCreateNote,
		TargetPath:  knowledgePath,
		PayloadJSON: string(createPayload),
		Reason:      "Create a grouped Knowledge draft for today's raw captures with source traceability.",
		RiskLevel:   model.RiskMedium,
	}}
	for i, rawPath := range rawPaths {
		movePayload, err := json.Marshal(model.MoveNotePayload{
			DestinationPath: processedPaths[i],
			ProcessingNote:  renderProcessedRawNote(job, rawJobs[i], rawPath, processedPaths[i], []string{knowledgePath}, now, processingNote),
		})
		if err != nil {
			return model.VaultPlan{}, err
		}
		operations = append(operations, model.VaultOperation{
			ID:          model.NewID("op"),
			Type:        model.OperationMoveNote,
			TargetPath:  rawPath,
			PayloadJSON: string(movePayload),
			Reason:      "Mark a raw capture as processed after the grouped Knowledge draft is approved.",
			RiskLevel:   model.RiskMedium,
		})
	}
	targetPaths := append([]string{knowledgePath}, rawPaths...)
	targetPaths = append(targetPaths, processedPaths...)
	sourceRefs := append([]string{}, jobIDs(rawJobs)...)
	sourceRefs = append(sourceRefs, rawPaths...)
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            job.ID,
		Purpose:          "organize today's raw captures into a grouped Knowledge draft",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          fmt.Sprintf("Create one grouped Knowledge draft for %d raw captures from %s.", len(rawJobs), date),
		SourceRefs:       sourceRefs,
		TargetPaths:      targetPaths,
		Operations:       operations,
		Status:           model.PlanStatusProposed,
		CreatedAt:        now,
	}, nil
}

func (s PlanService) resolvePlan(identifier string) (model.VaultPlan, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return model.VaultPlan{}, errors.New("plan id or job id is required")
	}
	plan, err := s.store.GetPlan(identifier)
	if err == nil {
		return plan, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.VaultPlan{}, err
	}
	return s.store.GetPlanByJobID(identifier)
}

func (s PlanService) failPlanJob(jobID string, err error) error {
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

func planAppliedMessage(planID, warning string) string {
	if warning == "" {
		return fmt.Sprintf("Plan %s applied", planID)
	}
	return fmt.Sprintf("Plan %s applied; post-sync warning: %s", planID, warning)
}

func rawTargetPath(job model.WikiJob) string {
	var result IngestRawResult
	if err := json.Unmarshal([]byte(job.ResultJSON), &result); err == nil && result.TargetPath != "" {
		return result.TargetPath
	}
	return "Raw/Inbox/" + job.ID + ".md"
}

func buildRawOrganizerContext(ctx context.Context, vaultRoot, rawPath string) (RawOrganizerContext, error) {
	if vaultRoot == "" {
		return RawOrganizerContext{}, errors.New("vault root is required")
	}
	rawFullPath, err := executor.ResolveVaultPath(vaultRoot, rawPath)
	if err != nil {
		return RawOrganizerContext{}, err
	}
	rawNote, err := os.ReadFile(rawFullPath)
	if err != nil {
		return RawOrganizerContext{}, fmt.Errorf("read raw note %s: %w", rawPath, err)
	}
	context := RawOrganizerContext{
		RawPath: rawPath,
		RawNote: string(rawNote),
	}
	for _, relPath := range []string{
		"AGENTS.md",
		"Meta/README.md",
		"Meta/Tagging.md",
		"Raw/AGENTS.md",
		"Knowledge/AGENTS.md",
	} {
		if err := ctx.Err(); err != nil {
			return RawOrganizerContext{}, err
		}
		fullPath, err := executor.ResolveVaultPath(vaultRoot, relPath)
		if err != nil {
			return RawOrganizerContext{}, err
		}
		content, err := os.ReadFile(fullPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return RawOrganizerContext{}, fmt.Errorf("read vault context %s: %w", relPath, err)
		}
		context.Documents = append(context.Documents, VaultContextDocument{
			Path:    relPath,
			Content: string(content),
		})
	}
	return context, nil
}

func (s PlanService) filterExistingInboxRawJobs(rawJobs []model.WikiJob) ([]model.WikiJob, []string, error) {
	var outJobs []model.WikiJob
	var outPaths []string
	for _, rawJob := range rawJobs {
		rawPath := rawTargetPath(rawJob)
		if rawPath == "" || !strings.HasPrefix(rawPath, "Raw/Inbox/") {
			continue
		}
		fullPath, err := executor.ResolveVaultPath(s.vaultRoot, rawPath)
		if err != nil {
			return nil, nil, err
		}
		if _, err := os.Stat(fullPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, nil, err
		}
		outJobs = append(outJobs, rawJob)
		outPaths = append(outPaths, rawPath)
	}
	return outJobs, outPaths, nil
}

func normalizeDay(day time.Time) time.Time {
	if day.IsZero() {
		day = time.Now()
	}
	location := day.Location()
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, location)
}

func dayBounds(day time.Time) (time.Time, time.Time) {
	start := normalizeDay(day)
	return start.UTC(), start.AddDate(0, 0, 1).UTC()
}

func jobIDs(jobs []model.WikiJob) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}

func renderKnowledgeDraft(job, rawJob model.WikiJob, rawPath, processedPath string, createdAt time.Time) string {
	return fmt.Sprintf(`---
openwhisker_job_id: %s
openwhisker_job_type: %s
source_raw_job_id: %s
source_raw_path: %s
source_processed_path: %s
status: draft
needs_review: true
created_at: %s
tags:
  - knowledge/draft
  - review/needed
---

# Knowledge Draft from %s

## Source

- Raw job: %s
- Raw path before approval: %s
- Raw path after approval: %s

## Draft

This deterministic Phase 2 draft preserves traceability and proves the approval-before-write path. Replace this section with LLM-backed organization in a later phase.
`, job.ID, job.Type, rawJob.ID, rawPath, processedPath, createdAt.Format(time.RFC3339), rawJob.ID,
		rawJob.ID, rawPath, processedPath)
}

func renderTodayKnowledgeDraft(req RawTodayOrganizerRequest, title, body string) string {
	var rawJobLines, rawPathLines, processedPathLines, sourceLines []string
	for i, rawJob := range req.RawJobs {
		rawPath := req.RawPaths[i]
		processedPath := "Raw/Processed/" + strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath)) + ".md"
		rawJobLines = append(rawJobLines, "  - "+rawJob.ID)
		rawPathLines = append(rawPathLines, "  - "+rawPath)
		processedPathLines = append(processedPathLines, "  - "+processedPath)
		sourceLines = append(sourceLines, fmt.Sprintf("- %s: %s -> %s", rawJob.ID, rawPath, processedPath))
	}
	firstProcessed := "Raw/Processed/unknown.md"
	if len(req.RawPaths) > 0 {
		firstProcessed = "Raw/Processed/" + strings.TrimSuffix(filepath.Base(req.RawPaths[0]), filepath.Ext(req.RawPaths[0])) + ".md"
	}
	return fmt.Sprintf(`---
openwhisker_job_id: %s
openwhisker_job_type: %s
source_raw_job_id: batch
source_raw_path: Raw/Inbox
source_processed_path: %s
source_raw_job_ids:
%s
source_raw_paths:
%s
source_processed_paths:
%s
status: draft
needs_review: true
created_at: %s
tags:
  - knowledge/draft
  - review/needed
---

# %s

## 摘要

%s

## Sources

%s

## 待核查

- 核对每条 raw 输入是否应该进入同一个 Knowledge draft。
- 核对是否需要拆分成多个主题笔记。
`, req.Job.ID, req.Job.Type, firstProcessed, strings.Join(rawJobLines, "\n"), strings.Join(rawPathLines, "\n"),
		strings.Join(processedPathLines, "\n"), req.Now.Format(time.RFC3339), strings.TrimSpace(title),
		strings.TrimSpace(body), strings.Join(sourceLines, "\n"))
}

func renderProcessedRawNote(job, rawJob model.WikiJob, rawPath, processedPath string, outputPaths []string, processedAt time.Time, note string) string {
	var outputLines []string
	for _, path := range outputPaths {
		path = strings.TrimSpace(path)
		if path != "" {
			outputLines = append(outputLines, "- "+path)
		}
	}
	if len(outputLines) == 0 {
		outputLines = []string{"- 待补充"}
	}
	return fmt.Sprintf(`

---

## OpenWhisker Processing

- Plan job: %s
- Raw job: %s
- Raw path before approval: %s
- Raw path after approval: %s
- Processed at: %s

### Outputs

%s

### Processing note

%s
`, job.ID, rawJob.ID, rawPath, processedPath, processedAt.Format(time.RFC3339), strings.Join(outputLines, "\n"), strings.TrimSpace(note))
}
