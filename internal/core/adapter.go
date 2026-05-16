package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type AdapterService struct {
	store            *storage.Store
	vaultRoot        string
	planOpts         PlanServiceOptions
	intentRouterMode string
	intentClassifier IntentClassifier
	now              func() time.Time
}

type AdapterServiceOptions struct {
	PlanOptions      PlanServiceOptions
	IntentRouterMode string
	IntentClassifier IntentClassifier
}

type AdapterRequest struct {
	Adapter   string `json:"adapter"`
	EventID   string `json:"event_id"`
	Sender    string `json:"sender"`
	SourceKey string `json:"source_key,omitempty"`
	Text      string `json:"text"`
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
		store:            store,
		vaultRoot:        vaultRoot,
		planOpts:         opts.PlanOptions,
		intentRouterMode: normalizeIntentRouterMode(opts.IntentRouterMode),
		intentClassifier: opts.IntentClassifier,
		now:              func() time.Time { return time.Now().UTC() },
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
	if req.SourceKey == "" {
		req.SourceKey = adapterSourceKey(req)
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
	if s.intentRouterMode != "off" && !strings.HasPrefix(req.Text, "/") {
		return s.handleIntentText(ctx, req)
	}
	return s.handleAdapterCommand(ctx, req)
}

func (s AdapterService) handleAdapterCommand(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
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
	result, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).IngestRaw(ctx, IngestRawRequest{
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
		var noDiff NoPreparedDiffError
		if errors.As(err, &noDiff) {
			return AdapterResponse{
				Status: "no_diff",
				PlanID: noDiff.PlanID,
				Body:   renderNoPreparedDiff(noDiff),
			}, nil
		}
		return AdapterResponse{}, err
	}
	plan, err := s.store.GetPlan(diff.PlanID)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     "ok",
		PlanID:     diff.PlanID,
		Body:       renderAdapterDiff(plan, diff),
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

func (s AdapterService) intentPlanService() PlanService {
	opts := s.planOpts
	opts.SuppressOutbox = true
	return NewPlanServiceWithOptions(s.store, s.vaultRoot, opts)
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

func adapterSourceKey(req AdapterRequest) string {
	if strings.TrimSpace(req.SourceKey) != "" {
		return strings.TrimSpace(req.SourceKey)
	}
	if req.Sender == "" {
		return req.Adapter
	}
	return req.Adapter + ":" + req.Sender
}

func adapterSourceKind(adapter string) string {
	switch adapter {
	case model.AdapterMatrix:
		return "matrix"
	case "":
		return "unknown"
	default:
		return adapter
	}
}

func resultStatusSummary(status string, paths []string) string {
	if len(paths) == 0 {
		return status
	}
	return status + " for " + strings.Join(paths, ", ")
}

func renderNoPreparedDiff(err NoPreparedDiffError) string {
	if err.RiskLevel == model.RiskLow && err.Status == model.PlanStatusApplied {
		return fmt.Sprintf("Plan %s is a low-risk raw capture that was already auto-applied, so it has no approval diff.\nRun /organize last first, then use /diff <plan_id> on the approval plan.", err.PlanID)
	}
	return fmt.Sprintf("Plan %s has no prepared approval diff. Current status: %s.", err.PlanID, err.Status)
}

func renderAdapterDiff(plan model.VaultPlan, diff *model.VaultDiff) string {
	lines := []string{
		"## 审批预览",
		"",
		fmt.Sprintf("- Plan: %s", diff.PlanID),
		fmt.Sprintf("- 摘要: %s", diff.Summary),
	}
	if len(plan.SourceRefs) > 0 {
		lines = append(lines, fmt.Sprintf("- 来源: %s", strings.Join(plan.SourceRefs, ", ")))
	}
	if created := renderCreatedNotePreview(plan.Operations); created != "" {
		lines = append(lines, "", created)
	}
	if moved := renderRawMovePreview(plan.Operations); moved != "" {
		lines = append(lines, "", moved)
	}
	if created := renderCreatedOperationSummary(plan.Operations); created != "" {
		lines = append(lines, "", "## 将执行", created)
	}
	lines = append(lines,
		"",
		"## 审批动作",
		fmt.Sprintf("- 批准：//approve %s", diff.PlanID),
		fmt.Sprintf("- 拒绝：//reject %s", diff.PlanID),
	)
	return strings.Join(lines, "\n")
}

func renderCreatedNotePreview(operations []model.VaultOperation) string {
	var sections []string
	for _, op := range operations {
		if op.Type != model.OperationCreateNote && op.Type != model.OperationAppendNote && op.Type != model.OperationWriteAgentReport {
			continue
		}
		content, ok := operationMarkdownContent(op)
		if !ok {
			continue
		}
		doc := summarizeMarkdownDocument(content, op.TargetPath)
		heading := "## 将写入的知识草稿"
		if op.Type == model.OperationAppendNote {
			heading = "## 将追加到笔记"
		} else if op.Type == model.OperationWriteAgentReport {
			heading = "## 将写入的 Agent 报告"
		}
		lines := []string{
			heading,
			"",
			fmt.Sprintf("- 路径: %s", op.TargetPath),
			fmt.Sprintf("- 标题: %s", doc.Title),
		}
		if len(doc.Tags) > 0 {
			lines = append(lines, fmt.Sprintf("- 标签: %s", strings.Join(doc.Tags, ", ")))
		}
		if doc.Source != "" {
			lines = append(lines, fmt.Sprintf("- 来源: %s", doc.Source))
		}
		if doc.BodyPreview != "" {
			lines = append(lines, "", "### 正文预览", "", doc.BodyPreview)
		}
		if len(doc.ReviewItems) > 0 {
			lines = append(lines, "", "### 待核查")
			for _, item := range doc.ReviewItems {
				lines = append(lines, "- "+item)
			}
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func renderRawMovePreview(operations []model.VaultOperation) string {
	var lines []string
	for _, op := range operations {
		if op.Type != model.OperationMoveNote {
			continue
		}
		var payload model.MoveNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			continue
		}
		lines = append(lines,
			"## 将移动的 Raw",
			"",
			fmt.Sprintf("%s", op.TargetPath),
			fmt.Sprintf("-> %s", payload.DestinationPath),
		)
		if note := summarizeProcessingNote(payload.ProcessingNote); note != "" {
			lines = append(lines, "", "处理记录会追加：", note)
		}
	}
	return strings.Join(lines, "\n")
}

func renderCreatedOperationSummary(operations []model.VaultOperation) string {
	var lines []string
	for _, op := range operations {
		switch op.Type {
		case model.OperationCreateNote:
			lines = append(lines, "- 创建笔记: "+op.TargetPath)
		case model.OperationWriteAgentReport:
			lines = append(lines, "- 创建 agent report: "+op.TargetPath)
		case model.OperationAppendNote:
			lines = append(lines, "- 追加内容: "+op.TargetPath)
		case model.OperationMoveNote:
			var payload model.MoveNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err == nil && payload.DestinationPath != "" {
				lines = append(lines, fmt.Sprintf("- 移动笔记: %s -> %s", op.TargetPath, payload.DestinationPath))
			} else {
				lines = append(lines, "- 移动笔记: "+op.TargetPath)
			}
		default:
			lines = append(lines, "- "+op.Type+": "+op.TargetPath)
		}
	}
	return strings.Join(lines, "\n")
}

func operationMarkdownContent(op model.VaultOperation) (string, bool) {
	switch op.Type {
	case model.OperationCreateNote, model.OperationWriteAgentReport:
		var payload model.CreateNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil || strings.TrimSpace(payload.Content) == "" {
			return "", false
		}
		return payload.Content, true
	case model.OperationAppendNote:
		var payload model.AppendNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil || strings.TrimSpace(payload.Content) == "" {
			return "", false
		}
		return payload.Content, true
	default:
		return "", false
	}
}

type markdownDocumentSummary struct {
	Title       string
	Tags        []string
	Source      string
	BodyPreview string
	ReviewItems []string
}

func summarizeMarkdownDocument(markdown, fallbackPath string) markdownDocumentSummary {
	frontmatter, body := splitFrontmatter(markdown)
	bodyLines := visibleMarkdownLines(body)
	return markdownDocumentSummary{
		Title:       firstMarkdownTitle(bodyLines, fallbackTitle(fallbackPath)),
		Tags:        extractFrontmatterTags(frontmatter),
		Source:      firstSourceLine(bodyLines),
		BodyPreview: markdownPreview(bodyLines, 10),
		ReviewItems: reviewItems(bodyLines),
	}
}

func splitFrontmatter(markdown string) (map[string]string, string) {
	lines := strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n")
	frontmatter := map[string]string{}
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return frontmatter, markdown
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
		line := strings.TrimSpace(lines[i])
		if key, value, ok := strings.Cut(line, ":"); ok {
			frontmatter[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	if end == -1 {
		return frontmatter, markdown
	}
	return frontmatter, strings.Join(lines[end+1:], "\n")
}

func visibleMarkdownLines(markdown string) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "openwhisker_") || strings.Contains(lower, "before_hash") || strings.Contains(lower, "after_hash") {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return out
}

func firstMarkdownTitle(lines []string, fallback string) string {
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
			continue
		}
		out = append(out, trimmed)
		if len(out) > 0 {
			return truncateRunes(strings.Join(out, " "), 80)
		}
	}
	return fallback
}

func fallbackTitle(path string) string {
	name := path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(name, ".md")
}

func extractFrontmatterTags(frontmatter map[string]string) []string {
	raw := strings.TrimSpace(frontmatter["tags"])
	if raw == "" {
		return nil
	}
	raw = strings.Trim(raw, "[]")
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var tags []string
	for _, part := range parts {
		tag := strings.Trim(strings.TrimSpace(part), `"'`)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

func firstSourceLine(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "[[Raw/") || strings.Contains(trimmed, "来源") {
			return strings.TrimPrefix(trimmed, "- ")
		}
		if strings.Contains(lower, "raw path before approval") {
			return strings.TrimPrefix(trimmed, "- ")
		}
	}
	return ""
}

func markdownPreview(lines []string, maxLines int) string {
	var out []string
	inReviewSection := false
	inTraceSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(trimmed, "## ") {
			heading := strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
			headingLower := strings.ToLower(heading)
			inTraceSection = headingLower == "source" || strings.Contains(heading, "来源") || strings.Contains(headingLower, "trace")
			if inTraceSection {
				continue
			}
		}
		if inTraceSection && strings.HasPrefix(trimmed, "#") {
			inTraceSection = false
		}
		if inTraceSection {
			continue
		}
		if strings.Contains(trimmed, "待核查") || strings.Contains(lower, "review") {
			inReviewSection = true
			continue
		}
		if inReviewSection && strings.HasPrefix(trimmed, "#") {
			inReviewSection = false
		}
		if inReviewSection {
			continue
		}
		if trimmed == "" && len(out) == 0 {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") && len(out) == 0 {
			continue
		}
		out = append(out, truncateRunes(line, 180))
		if len(out) >= maxLines {
			break
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func reviewItems(lines []string) []string {
	var items []string
	inReviewSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "待核查") || strings.Contains(lower, "needs review") || strings.Contains(lower, "needs-review") {
			inReviewSection = true
			if strings.HasPrefix(trimmed, "- ") {
				items = append(items, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
			}
			continue
		}
		if inReviewSection && strings.HasPrefix(trimmed, "#") {
			break
		}
		if inReviewSection && strings.HasPrefix(trimmed, "- ") {
			items = append(items, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	if len(items) > 5 {
		return items[:5]
	}
	return items
}

func summarizeProcessingNote(note string) string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(note, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "## ") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "openwhisker_") || strings.Contains(lower, "before_hash") || strings.Contains(lower, "after_hash") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			out = append(out, trimmed)
		}
		if len(out) >= 5 {
			break
		}
	}
	return strings.Join(out, "\n")
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}
