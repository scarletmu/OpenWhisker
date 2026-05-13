package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type AdapterService struct {
	store     *storage.Store
	vaultRoot string
	planOpts  PlanServiceOptions
	now       func() time.Time
}

type AdapterServiceOptions struct {
	PlanOptions PlanServiceOptions
}

type AdapterRequest struct {
	Adapter string `json:"adapter"`
	EventID string `json:"event_id"`
	Sender  string `json:"sender"`
	Text    string `json:"text"`
}

type AdapterResponse struct {
	Status     string `json:"status"`
	JobID      string `json:"job_id,omitempty"`
	PlanID     string `json:"plan_id,omitempty"`
	Body       string `json:"body"`
	Duplicate  bool   `json:"duplicate,omitempty"`
	OutboxKind string `json:"outbox_kind,omitempty"`
}

func NewAdapterService(store *storage.Store, vaultRoot string) AdapterService {
	return NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{})
}

func NewAdapterServiceWithOptions(store *storage.Store, vaultRoot string, opts AdapterServiceOptions) AdapterService {
	return AdapterService{
		store:     store,
		vaultRoot: vaultRoot,
		planOpts:  opts.PlanOptions,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (s AdapterService) HandleText(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return AdapterResponse{}, fmt.Errorf("adapter text is required")
	}
	if req.Adapter == "" {
		req.Adapter = "unknown"
	}
	if req.EventID != "" {
		requestJSON, err := json.Marshal(req)
		if err != nil {
			return AdapterResponse{}, err
		}
		recorded, err := s.store.TryRecordAdapterEvent(req.Adapter, req.EventID, string(requestJSON), s.now())
		if err != nil {
			return AdapterResponse{}, err
		}
		if !recorded {
			return AdapterResponse{
				Status:    "duplicate",
				Body:      "Duplicate event ignored.",
				Duplicate: true,
			}, nil
		}
	}

	command, args := splitAdapterCommand(req.Text)
	switch command {
	case "":
		return s.ingestRaw(ctx, req, req.Text)
	case "/raw":
		if strings.TrimSpace(args) == "" {
			return AdapterResponse{}, fmt.Errorf("/raw requires text")
		}
		return s.ingestRaw(ctx, req, args)
	case "/organize":
		return s.handleOrganize(ctx, strings.Fields(args))
	case "/diff":
		return s.handleDiff(args)
	case "/approve":
		return s.handleApprove(ctx, args)
	case "/reject":
		return s.handleReject(args)
	case "/status":
		return s.handleStatus(args)
	case "/jobs":
		return s.handleJobs(10)
	default:
		if strings.HasPrefix(command, "/") {
			return AdapterResponse{}, fmt.Errorf("unsupported adapter command %q", command)
		}
		return s.ingestRaw(ctx, req, req.Text)
	}
}

func (s AdapterService) PullOutbox(limit int) ([]model.OutboxMessage, error) {
	return s.store.ListPendingOutbox(limit)
}

func (s AdapterService) MarkOutboxDelivered(id string) error {
	return s.store.MarkOutboxDelivered(id)
}

func (s AdapterService) ingestRaw(ctx context.Context, req AdapterRequest, text string) (AdapterResponse, error) {
	result, err := NewIngestService(s.store, s.vaultRoot).IngestRaw(ctx, IngestRawRequest{
		Text:   text,
		Source: adapterSource(req),
	})
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     model.JobStatusDone,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Raw capture saved to %s", result.TargetPath),
		OutboxKind: model.OutboxKindResult,
	}, nil
}

func (s AdapterService) handleOrganize(ctx context.Context, fields []string) (AdapterResponse, error) {
	if len(fields) != 1 || (fields[0] != "last" && fields[0] != "today") {
		return AdapterResponse{}, fmt.Errorf("usage: /organize last|today")
	}
	if fields[0] == "today" {
		result, err := s.planService().OrganizeToday(ctx, s.now())
		if err != nil {
			return AdapterResponse{}, err
		}
		return AdapterResponse{
			Status:     result.Status,
			JobID:      result.JobID,
			PlanID:     result.PlanID,
			Body:       fmt.Sprintf("Plan %s awaits approval for %d raw captures: %s", result.PlanID, len(result.RawJobIDs), resultStatusSummary(result.Status, result.TargetPaths)),
			OutboxKind: model.OutboxKindApproval,
		}, nil
	}
	result, err := s.planService().OrganizeLast(ctx)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     result.Status,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Plan %s awaits approval: %s", result.PlanID, resultStatusSummary(result.Status, result.TargetPaths)),
		OutboxKind: model.OutboxKindApproval,
	}, nil
}

func (s AdapterService) handleDiff(identifier string) (AdapterResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return AdapterResponse{}, fmt.Errorf("usage: /diff <plan_id|job_id>")
	}
	diff, err := s.planService().Diff(identifier)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     "ok",
		PlanID:     diff.PlanID,
		Body:       renderAdapterDiff(diff),
		OutboxKind: model.OutboxKindDiff,
	}, nil
}

func (s AdapterService) handleApprove(ctx context.Context, identifier string) (AdapterResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return AdapterResponse{}, fmt.Errorf("usage: /approve <plan_id|job_id>")
	}
	result, err := s.planService().Approve(ctx, identifier)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     result.Status,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Plan %s applied.", result.PlanID),
		OutboxKind: model.OutboxKindResult,
	}, nil
}

func (s AdapterService) handleReject(args string) (AdapterResponse, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return AdapterResponse{}, fmt.Errorf("usage: /reject <plan_id|job_id> [reason]")
	}
	identifier := fields[0]
	reason := strings.TrimSpace(strings.TrimPrefix(args, identifier))
	result, err := s.planService().Reject(identifier, reason)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     result.Status,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Plan %s rejected.", result.PlanID),
		OutboxKind: model.OutboxKindRejected,
	}, nil
}

func (s AdapterService) handleStatus(identifier string) (AdapterResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return s.handleJobs(5)
	}
	if job, err := s.store.GetJob(identifier); err == nil {
		return AdapterResponse{
			Status: job.Status,
			JobID:  job.ID,
			Body:   fmt.Sprintf("Job %s is %s.", job.ID, job.Status),
		}, nil
	}
	plan, err := s.store.GetPlan(identifier)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: plan.Status,
		JobID:  plan.JobID,
		PlanID: plan.ID,
		Body:   fmt.Sprintf("Plan %s is %s.", plan.ID, plan.Status),
	}, nil
}

func (s AdapterService) handleJobs(limit int) (AdapterResponse, error) {
	jobs, err := s.store.ListRecentJobs(limit)
	if err != nil {
		return AdapterResponse{}, err
	}
	if len(jobs) == 0 {
		return AdapterResponse{Status: "ok", Body: "No jobs yet."}, nil
	}
	var lines []string
	for _, job := range jobs {
		lines = append(lines, fmt.Sprintf("- %s %s %s", job.ID, job.Type, job.Status))
	}
	return AdapterResponse{
		Status: "ok",
		Body:   "Recent jobs:\n" + strings.Join(lines, "\n"),
	}, nil
}

func (s AdapterService) planService() PlanService {
	return NewPlanServiceWithOptions(s.store, s.vaultRoot, s.planOpts)
}

func splitAdapterCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	command, args, _ := strings.Cut(text, " ")
	return strings.ToLower(strings.TrimSpace(command)), strings.TrimSpace(args)
}

func adapterSource(req AdapterRequest) string {
	if req.Sender == "" {
		return req.Adapter
	}
	return req.Adapter + ":" + req.Sender
}

func resultStatusSummary(status string, paths []string) string {
	if len(paths) == 0 {
		return status
	}
	return status + " for " + strings.Join(paths, ", ")
}

func renderAdapterDiff(diff *model.VaultDiff) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Diff for plan %s: %s", diff.PlanID, diff.Summary))
	for _, entry := range diff.Entries {
		lines = append(lines, fmt.Sprintf("- %s %s", entry.Type, entry.TargetPath))
		if entry.Preview != "" {
			lines = append(lines, entry.Preview)
		}
	}
	return strings.Join(lines, "\n")
}
