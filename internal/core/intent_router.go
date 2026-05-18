package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
)

const (
	intentRouterModeHybrid = "hybrid"
	intentRouterModeRules  = "rules"
	intentRouterModeOff    = "off"
)

const (
	intentExecutedCreated                  = "created"
	intentExecutedAppended                 = "appended"
	intentExecutedClosed                   = "closed"
	intentExecutedOrganizedPendingApproval = "organized_pending_approval"
	intentExecutedDiffShown                = "diff_shown"
	intentExecutedApproved                 = "approved"
	intentExecutedPlanRejected             = "plan_rejected"
	intentExecutedRejectedNoActiveBucket   = "rejected_no_active_bucket"
	intentExecutedRejectedNoPendingPlan    = "rejected_no_pending_plan"
	intentExecutedRejectedAmbiguousPlan    = "rejected_ambiguous_pending_plan"
	intentExecutedClarificationRequested   = "clarification_requested"
	intentExecutedClarificationCancelled   = "clarification_cancelled"
	intentExecutedNotExecuted              = "not_executed"
	intentExecutedFailed                   = "failed"
)

const (
	intentClarificationTTL     = 5 * time.Minute
	intentClarificationRequest = "clarification_request"
)

type intentRuleResult struct {
	intent        string
	displayAction string
	payload       string
	target        string
	clarification *clarificationProposal
}

type clarificationProposal struct {
	questionType string
	candidates   []model.CandidateAction
}

type intentAuditEvent struct {
	TS                      time.Time `json:"ts"`
	Mode                    string    `json:"mode"`
	SourceKind              string    `json:"source_kind"`
	RulesIntent             string    `json:"rules_intent,omitempty"`
	ClassifierUsed          bool      `json:"classifier_used"`
	ModelIntent             string    `json:"model_intent,omitempty"`
	Target                  string    `json:"target,omitempty"`
	CaptureAction           string    `json:"capture_action,omitempty"`
	BucketRelation          string    `json:"bucket_relation,omitempty"`
	ConfidenceLabel         string    `json:"confidence_label,omitempty"`
	Confidence              float64   `json:"confidence,omitempty"`
	AcceptedIntent          string    `json:"accepted_intent,omitempty"`
	Accepted                bool      `json:"accepted"`
	ExecutedAction          string    `json:"executed_action,omitempty"`
	ClarificationID         string    `json:"clarification_id,omitempty"`
	ClarificationResolution string    `json:"clarification_resolution,omitempty"`
	Error                   string    `json:"error,omitempty"`
}

type intentClassification struct {
	result intentRuleResult
	audit  intentAuditEvent
}

func (s AdapterService) handleIntentText(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
	classification := s.classifyIntent(ctx, req)
	response, executedAction, err := s.dispatchIntent(ctx, req, classification.result)
	classification.audit.ExecutedAction = executedAction
	if err != nil && classification.audit.Error == "" {
		classification.audit.Error = err.Error()
	}
	s.writeIntentAudit(classification.audit)
	return response, err
}

func (s AdapterService) dispatchIntent(ctx context.Context, req AdapterRequest, result intentRuleResult) (AdapterResponse, string, error) {
	switch result.intent {
	case intentClarificationRequest:
		resp, err := s.handleIntentClarificationRequest(req, result)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		return resp, intentExecutedClarificationRequested, nil
	case "clarification_cancel":
		return AdapterResponse{
			Status: "clarification_cancelled",
			Body:   "已取消上一次的待确认动作。",
		}, intentExecutedClarificationCancelled, nil
	case "raw_create":
		resp, err := s.handleIntentRawCreate(ctx, req, result)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		return resp, intentExecutedCreated, nil
	case "raw_append":
		resp, err := s.handleIntentRawAppend(ctx, req, result)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		if resp.Status == "no_active_bucket" {
			return resp, intentExecutedRejectedNoActiveBucket, nil
		}
		return resp, intentExecutedAppended, nil
	case "raw_close":
		resp, err := s.handleIntentRawClose(req, result)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		if resp.Status == "no_active_bucket" {
			return resp, intentExecutedRejectedNoActiveBucket, nil
		}
		return resp, intentExecutedClosed, nil
	case "organize":
		resp, err := s.handleIntentOrganize(ctx, req, result)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		return resp, intentExecutedOrganizedPendingApproval, nil
	case "diff":
		resp, err := s.handleIntentDiff(result, req.SourceKey)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		switch resp.Status {
		case "no_pending_plan":
			return resp, intentExecutedRejectedNoPendingPlan, nil
		case "ambiguous_pending_plan":
			return resp, intentExecutedRejectedAmbiguousPlan, nil
		}
		return resp, intentExecutedDiffShown, nil
	case "approve":
		resp, err := s.handleIntentApprove(ctx, result, req.SourceKey)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		switch resp.Status {
		case "no_pending_plan":
			return resp, intentExecutedRejectedNoPendingPlan, nil
		case "ambiguous_pending_plan":
			return resp, intentExecutedRejectedAmbiguousPlan, nil
		}
		return resp, intentExecutedApproved, nil
	case "reject":
		resp, err := s.handleIntentReject(result, req.SourceKey)
		if err != nil {
			return resp, intentExecutedFailed, err
		}
		switch resp.Status {
		case "no_pending_plan":
			return resp, intentExecutedRejectedNoPendingPlan, nil
		case "ambiguous_pending_plan":
			return resp, intentExecutedRejectedAmbiguousPlan, nil
		}
		return resp, intentExecutedPlanRejected, nil
	default:
		return AdapterResponse{
			Status: "unclear",
			Body:   "无法安全判断这条消息要记录、整理还是审批；未写入 Raw，也未执行任何计划。请使用“记录一下：...”或 slash 命令。",
		}, intentExecutedNotExecuted, nil
	}
}

func (s AdapterService) classifyIntent(ctx context.Context, req AdapterRequest) intentClassification {
	audit := intentAuditEvent{
		TS:         s.now(),
		Mode:       s.intentRouterMode,
		SourceKind: adapterSourceKind(req.Adapter),
	}
	if _, err := s.store.ExpirePendingClarifications(req.SourceKey, s.now()); err != nil {
		audit.Error = err.Error()
	}
	if pending, ok, err := s.pendingClarification(req.SourceKey); err != nil {
		audit.Error = err.Error()
	} else if ok {
		if matched, action, ok := matchClarificationReply(req.Text, pending); ok {
			audit.ClarificationID = pending.ID
			audit.ClarificationResolution = "resolved"
			if action == model.ClarificationActionCancel {
				_ = s.store.UpdatePendingClarificationStatus(pending.ID, model.PendingClarificationStatusCancelled, s.now())
				audit.ClarificationResolution = "cancelled_by_user"
				audit.AcceptedIntent = ""
				audit.Accepted = false
				return intentClassification{
					result: intentRuleResult{intent: "clarification_cancel", displayAction: matched.Label},
					audit:  audit,
				}
			}
			_ = s.store.UpdatePendingClarificationStatus(pending.ID, model.PendingClarificationStatusResolved, s.now())
			result := buildResultFromClarificationAction(action, pending.OriginalMessage)
			audit.AcceptedIntent = result.intent
			audit.Accepted = result.intent != ""
			return intentClassification{result: result, audit: audit}
		}
		_ = s.store.UpdatePendingClarificationStatus(pending.ID, model.PendingClarificationStatusCancelled, s.now())
		audit.ClarificationID = pending.ID
		audit.ClarificationResolution = "cancelled_superseded"
	}
	result := classifyIntentRules(req.Text)
	audit.RulesIntent = result.intent
	if result.intent != "" && result.intent != "unclear" {
		audit.AcceptedIntent = result.intent
		audit.Accepted = true
		return intentClassification{result: result, audit: audit}
	}
	if s.intentRouterMode != intentRouterModeHybrid || s.intentClassifier == nil {
		return intentClassification{result: result, audit: audit}
	}
	audit.ClassifierUsed = true
	classified, err := s.intentClassifier.ClassifyIntent(ctx, s.intentClassifierRequest(req))
	if err != nil {
		audit.Error = err.Error()
		return intentClassification{result: result, audit: audit}
	}
	audit.ModelIntent = classified.Intent
	audit.Target = classified.Target
	audit.CaptureAction = classified.CaptureAction
	audit.BucketRelation = classified.BucketRelation
	audit.ConfidenceLabel = classified.ConfidenceLabel
	audit.Confidence = classified.Confidence
	mapped := s.modelIntentToRuleResult(req, classified)
	if mapped.intent != "" {
		audit.AcceptedIntent = mapped.intent
		audit.Accepted = true
		return intentClassification{result: mapped, audit: audit}
	}
	if classified.ConfidenceLabel == "medium" {
		if proposal, ok := s.synthesizeClarification(req, classified); ok {
			result := intentRuleResult{
				intent:        intentClarificationRequest,
				displayAction: "请确认这条消息的处理方式",
				payload:       strings.TrimSpace(req.Text),
				clarification: &proposal,
			}
			audit.AcceptedIntent = intentClarificationRequest
			audit.Accepted = true
			return intentClassification{result: result, audit: audit}
		}
	}
	audit.AcceptedIntent = ""
	audit.Accepted = false
	return intentClassification{result: intentRuleResult{}, audit: audit}
}

func (s AdapterService) pendingClarification(sourceKey string) (model.PendingClarification, bool, error) {
	pending, err := s.store.ActivePendingClarification(sourceKey, s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return model.PendingClarification{}, false, nil
	}
	if err != nil {
		return model.PendingClarification{}, false, err
	}
	return pending, true, nil
}

func buildResultFromClarificationAction(action, originalMessage string) intentRuleResult {
	payload := strings.TrimSpace(originalMessage)
	switch action {
	case model.ClarificationActionRawAppend:
		return intentRuleResult{intent: "raw_append", displayAction: "追加当前记录组", payload: payload, target: "active_bucket"}
	case model.ClarificationActionRawCreate:
		return intentRuleResult{intent: "raw_create", displayAction: "创建当前记录组", payload: payload, target: "new_bucket"}
	}
	return intentRuleResult{}
}

func matchClarificationReply(text string, pending model.PendingClarification) (model.CandidateAction, string, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return model.CandidateAction{}, "", false
	}
	if idx, ok := parseCandidateNumber(trimmed); ok {
		if idx >= 1 && idx <= len(pending.CandidateActions) {
			c := pending.CandidateActions[idx-1]
			return c, c.Action, true
		}
	}
	lower := strings.ToLower(trimmed)
	if exactAny(lower, "取消", "算了", "cancel", "不用", "都不要", "都不用", "都不写", "先不处理") {
		return model.CandidateAction{Action: model.ClarificationActionCancel, Label: "取消"}, model.ClarificationActionCancel, true
	}
	for _, c := range pending.CandidateActions {
		if labelMatchesReply(trimmed, c) {
			return c, c.Action, true
		}
	}
	return model.CandidateAction{}, "", false
}

func parseCandidateNumber(text string) (int, bool) {
	t := strings.TrimSpace(text)
	const punctuation = ".．、 。,，:：;；!！?？)）】]"
	const openers = "(（[【"
	t = strings.Trim(t, punctuation)
	t = strings.TrimLeft(t, openers)
	for _, prefix := range []string{"我选", "选项", "选", "要", "第"} {
		if after, ok := strings.CutPrefix(t, prefix); ok {
			t = strings.TrimSpace(after)
			break
		}
	}
	t = strings.Trim(t, punctuation)
	switch t {
	case "1", "①", "一":
		return 1, true
	case "2", "②", "二":
		return 2, true
	case "3", "③", "三":
		return 3, true
	case "4", "④", "四":
		return 4, true
	}
	return 0, false
}

func labelMatchesReply(text string, c model.CandidateAction) bool {
	switch c.Action {
	case model.ClarificationActionRawAppend:
		return exactAny(text,
			"补充", "补充上", "并入", "加上", "加上去",
			"加到上一组", "补到上一组", "加进上一组",
			"合并", "接着上一组", "续上", "接着上面",
		)
	case model.ClarificationActionRawCreate:
		return exactAny(text,
			"新建", "新建一组", "新的一组",
			"另开", "另开一组", "另起", "另起一组",
			"新开", "新开一组", "单独一组", "单独开一组",
		)
	case model.ClarificationActionCancel:
		return exactAny(text, "取消", "算了", "都不要", "都不用", "都不写", "先不处理")
	}
	return false
}

func (s AdapterService) synthesizeClarification(req AdapterRequest, classified IntentClassifierResult) (clarificationProposal, bool) {
	if strings.TrimSpace(req.Text) == "" {
		return clarificationProposal{}, false
	}
	if len(req.Text) > model.PendingClarificationOriginalMax {
		return clarificationProposal{}, false
	}
	if classified.Intent != "raw_capture" {
		return clarificationProposal{}, false
	}
	bucket, hasActive, err := s.activeBucket(req.SourceKey)
	if err != nil {
		return clarificationProposal{}, false
	}
	activeBucket := hasActive && bucket.Status == model.CaptureBucketStatusActive
	candidates := []model.CandidateAction{}
	if activeBucket {
		candidates = append(candidates, model.CandidateAction{Action: model.ClarificationActionRawAppend, Label: "补充到上一组"})
	}
	candidates = append(candidates,
		model.CandidateAction{Action: model.ClarificationActionRawCreate, Label: "新建一组"},
		model.CandidateAction{Action: model.ClarificationActionCancel, Label: "取消"},
	)
	return clarificationProposal{
		questionType: model.ClarificationQuestionBucketRelation,
		candidates:   candidates,
	}, true
}

func (s AdapterService) intentClassifierRequest(req AdapterRequest) IntentClassifierRequest {
	out := IntentClassifierRequest{
		Message:    req.Text,
		SourceKind: adapterSourceKind(req.Adapter),
	}
	if bucket, ok, err := s.activeBucket(req.SourceKey); err == nil && ok {
		out.ActiveBucket = &IntentActiveBucketSummary{
			Status:      bucket.Status,
			StartedAt:   bucket.StartedAt,
			UpdatedAt:   bucket.UpdatedAt,
			TopicHint:   bucket.TopicHint,
			Excerpt:     bucket.Excerpt,
			AppendCount: bucket.AppendCount,
		}
	}
	if plans, err := s.store.ListAwaitingApprovalPlansBySourceKey(req.SourceKey); err == nil {
		out.PendingPlanCount = len(plans)
	}
	return out
}

func (s AdapterService) modelIntentToRuleResult(req AdapterRequest, classified IntentClassifierResult) intentRuleResult {
	if classified.ConfidenceLabel != "high" {
		return intentRuleResult{}
	}
	payload := strings.TrimSpace(classified.PayloadText)
	if payload == "" {
		payload = strings.TrimSpace(req.Text)
	}
	activeBucket, hasActiveBucket, err := s.activeBucket(req.SourceKey)
	if err != nil {
		return intentRuleResult{}
	}
	switch classified.Intent {
	case "raw_capture":
		switch classified.CaptureAction {
		case "create":
			if !hasActiveBucket || classified.BucketRelation == "new_topic" {
				return intentRuleResult{intent: "raw_create", displayAction: "创建当前记录组", payload: payload, target: "new_bucket"}
			}
		case "append":
			if hasActiveBucket && activeBucket.Status == model.CaptureBucketStatusActive && classified.BucketRelation == "same_topic" {
				return intentRuleResult{intent: "raw_append", displayAction: "追加当前记录组", payload: payload, target: "active_bucket"}
			}
		case "close":
			if hasActiveBucket {
				return intentRuleResult{intent: "raw_close", displayAction: "结束当前记录组", target: "active_bucket"}
			}
		}
	case "organize_request":
		if classified.Target == "today" {
			return intentRuleResult{intent: "organize", displayAction: "处理今天", target: "today"}
		}
		return intentRuleResult{intent: "organize", displayAction: "整理刚才", target: "active"}
	case "diff_request":
		return intentRuleResult{intent: "diff", displayAction: "预览当前计划"}
	case "approve_request":
		if classified.Confidence >= 0.85 {
			return intentRuleResult{intent: "approve", displayAction: "批准当前计划"}
		}
	case "reject_request":
		return intentRuleResult{intent: "reject", displayAction: "拒绝当前计划"}
	}
	return intentRuleResult{}
}

func (s AdapterService) writeIntentAudit(event intentAuditEvent) {
	path := strings.TrimSpace(os.Getenv("OPENWHISKER_INTENT_AUDIT_FILE"))
	if path == "" {
		return
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(data, '\n'))
}

func (s AdapterService) handleIntentClarificationRequest(req AdapterRequest, result intentRuleResult) (AdapterResponse, error) {
	if result.clarification == nil || len(result.clarification.candidates) == 0 {
		return AdapterResponse{}, fmt.Errorf("clarification proposal missing")
	}
	now := s.now()
	clarification := model.PendingClarification{
		ID:                 model.NewID("clar"),
		SourceKey:          req.SourceKey,
		QuestionType:       result.clarification.questionType,
		OriginalMessage:    result.payload,
		OriginalReceivedAt: now,
		CandidateActions:   result.clarification.candidates,
		Status:             model.PendingClarificationStatusPending,
		CreatedAt:          now,
		ExpiresAt:          now.Add(intentClarificationTTL),
	}
	if err := s.store.SavePendingClarification(clarification); err != nil {
		return AdapterResponse{}, err
	}
	var lines []string
	lines = append(lines, "刚才那条不太确定要怎么处理，想确认下：")
	lines = append(lines, "")
	for i, c := range clarification.CandidateActions {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, c.Label))
	}
	lines = append(lines, "")
	lines = append(lines, "回数字或对应短语都行。")
	return AdapterResponse{
		Status: "clarification_requested",
		Body:   strings.Join(lines, "\n"),
	}, nil
}

func (s AdapterService) handleIntentRawCreate(ctx context.Context, req AdapterRequest, result intentRuleResult) (AdapterResponse, error) {
	bucket, ingest, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).CreateRawBucket(ctx, CreateRawBucketRequest{
		Text:           result.payload,
		Source:         adapterSource(req),
		SourceKey:      req.SourceKey,
		SuppressOutbox: true,
	})
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: model.JobStatusDone,
		JobID:  ingest.JobID,
		PlanID: ingest.PlanID,
		Body:   fmt.Sprintf("识别为：创建当前记录组\n\nRaw capture saved to %s\n\n- Bucket: %s", ingest.TargetPath, bucket.ID),
	}, nil
}

func (s AdapterService) handleIntentRawAppend(ctx context.Context, req AdapterRequest, result intentRuleResult) (AdapterResponse, error) {
	bucket, ok, err := s.activeBucket(req.SourceKey)
	if err != nil {
		return AdapterResponse{}, err
	}
	if !ok {
		return AdapterResponse{
			Status: "no_active_bucket",
			Body:   "识别为：补充当前记录组\n\n当前没有 active bucket。请先使用“记录一下：...”开一条新记录。",
		}, nil
	}
	updated, ingest, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).AppendRawBucket(ctx, AppendRawBucketRequest{
		SourceKey: req.SourceKey,
		BucketID:  bucket.ID,
		Text:      result.payload,
		Source:    adapterSource(req),
	})
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: model.JobStatusDone,
		JobID:  ingest.JobID,
		PlanID: ingest.PlanID,
		Body: fmt.Sprintf("识别为：追加当前记录组\n\nRaw capture appended to %s\n\n- Bucket: %s\n- 输入数: %d",
			ingest.TargetPath, updated.ID, updated.AppendCount),
	}, nil
}

func (s AdapterService) handleIntentRawClose(req AdapterRequest, result intentRuleResult) (AdapterResponse, error) {
	bucket, ok, err := s.activeBucket(req.SourceKey)
	if err != nil {
		return AdapterResponse{}, err
	}
	if !ok {
		return AdapterResponse{Status: "no_active_bucket", Body: "识别为：结束当前记录组\n\n当前没有 active bucket。"}, nil
	}
	closed, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).CloseRawBucket(CloseRawBucketRequest{
		SourceKey: req.SourceKey,
		BucketID:  bucket.ID,
		Reason:    "closed by intent router",
	})
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: closed.Status,
		Body:   fmt.Sprintf("识别为：结束当前记录组\n\nBucket %s closed.", closed.ID),
	}, nil
}

func (s AdapterService) handleIntentOrganize(ctx context.Context, req AdapterRequest, result intentRuleResult) (AdapterResponse, error) {
	var organized OrganizeRawResult
	var err error
	planService := s.intentPlanService()
	if result.target == "today" {
		organized, err = planService.OrganizeTodayForSource(ctx, req.SourceKey, s.now())
	} else if bucket, ok, activeErr := s.activeBucket(req.SourceKey); activeErr != nil {
		return AdapterResponse{}, activeErr
	} else if ok {
		organized, err = planService.OrganizeCaptureBucket(ctx, req.SourceKey, bucket.ID)
	} else {
		organized, err = planService.OrganizeSourceLast(ctx, req.SourceKey)
	}
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: organized.Status,
		JobID:  organized.JobID,
		PlanID: organized.PlanID,
		Body: fmt.Sprintf("识别为：%s\n\nPlan %s awaits approval: %s",
			result.displayAction, organized.PlanID, resultStatusSummary(organized.Status, organized.TargetPaths)),
	}, nil
}

func (s AdapterService) handleIntentDiff(result intentRuleResult, sourceKey string) (AdapterResponse, error) {
	plan, response, ok, err := s.resolveUniquePendingPlan(sourceKey, "预览当前 source 下唯一待审批 plan")
	if err != nil || !ok {
		return response, err
	}
	response, err = s.handleDiff(plan.ID)
	if err != nil {
		return AdapterResponse{}, err
	}
	response.Body = fmt.Sprintf("识别为：%s\n\n%s", result.displayAction, response.Body)
	return response, nil
}

func (s AdapterService) handleIntentApprove(ctx context.Context, result intentRuleResult, sourceKey string) (AdapterResponse, error) {
	plan, response, ok, err := s.resolveUniquePendingPlan(sourceKey, "批准当前 source 下唯一待审批 plan")
	if err != nil || !ok {
		return response, err
	}
	approved, err := s.intentPlanService().Approve(ctx, plan.ID)
	if err != nil {
		return AdapterResponse{}, err
	}
	response = AdapterResponse{
		Status: approved.Status,
		JobID:  approved.JobID,
		PlanID: approved.PlanID,
		Body:   fmt.Sprintf("Plan %s applied.", approved.PlanID),
	}
	response.Body = fmt.Sprintf("识别为：%s\n\n%s", result.displayAction, response.Body)
	return response, nil
}

func (s AdapterService) handleIntentReject(result intentRuleResult, sourceKey string) (AdapterResponse, error) {
	plan, response, ok, err := s.resolveUniquePendingPlan(sourceKey, "拒绝当前 source 下唯一待审批 plan")
	if err != nil || !ok {
		return response, err
	}
	rejected, err := s.intentPlanService().Reject(plan.ID, "rejected by intent router")
	if err != nil {
		return AdapterResponse{}, err
	}
	response = AdapterResponse{
		Status: rejected.Status,
		JobID:  rejected.JobID,
		PlanID: rejected.PlanID,
		Body:   fmt.Sprintf("Plan %s rejected.", rejected.PlanID),
	}
	response.Body = fmt.Sprintf("识别为：%s\n\n%s", result.displayAction, response.Body)
	return response, nil
}

func (s AdapterService) activeBucket(sourceKey string) (model.CaptureBucket, bool, error) {
	bucket, err := s.store.ActiveCaptureBucket(sourceKey)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CaptureBucket{}, false, nil
	}
	if err != nil {
		return model.CaptureBucket{}, false, err
	}
	return bucket, true, nil
}

func (s AdapterService) resolveUniquePendingPlan(sourceKey, action string) (model.VaultPlan, AdapterResponse, bool, error) {
	plans, err := s.store.ListAwaitingApprovalPlansBySourceKey(sourceKey)
	if err != nil {
		return model.VaultPlan{}, AdapterResponse{}, false, err
	}
	switch len(plans) {
	case 0:
		return model.VaultPlan{}, AdapterResponse{
			Status: "no_pending_plan",
			Body:   "识别为：" + action + "\n\n当前 source 没有待审批 plan，未执行。",
		}, false, nil
	case 1:
		return plans[0], AdapterResponse{}, true, nil
	default:
		return model.VaultPlan{}, AdapterResponse{
			Status: "ambiguous_pending_plan",
			Body:   fmt.Sprintf("识别为：%s\n\n当前 source 有 %d 个待审批 plan。请使用显式 slash 命令指定 plan_id。", action, len(plans)),
		}, false, nil
	}
}

func classifyIntentRules(text string) intentRuleResult {
	trimmed := strings.TrimSpace(text)
	if payload, ok := stripIntentPrefix(trimmed, []string{
		"记录一下：", "记录一下:",
		"开始记录：", "开始记录:",
		"帮我收一下这个材料：", "帮我收一下这个材料:",
		"记一下：", "记一下:",
		"写下来：", "写下来:",
		"存一下：", "存一下:",
		"记下：", "记下:",
	}); ok {
		return intentRuleResult{intent: "raw_create", displayAction: "创建当前记录组", payload: payload}
	}
	if payload, ok := stripIntentPrefix(trimmed, []string{
		"补充：", "补充:",
		"继续：", "继续:",
		"还有：", "还有:",
		"再加：", "再加:",
		"再补充：", "再补充:",
		"后面还有：", "后面还有:",
		"另外：", "另外:",
	}); ok {
		return intentRuleResult{intent: "raw_append", displayAction: "追加当前记录组", payload: payload}
	}
	if exactAny(trimmed,
		"结束记录", "这组结束", "先到这里",
		"结束这组", "到这里", "就这些", "先这些",
	) {
		return intentRuleResult{intent: "raw_close", displayAction: "结束当前记录组"}
	}
	if exactAny(trimmed,
		"整理刚才", "处理这组", "整理这组", "处理刚才",
		"整理一下", "处理一下", "组织一下",
	) {
		return intentRuleResult{intent: "organize", displayAction: "整理当前记录组", target: "active"}
	}
	if exactAny(trimmed, "处理今天", "整理今天", "整理今天的", "处理今天的") {
		return intentRuleResult{intent: "organize", displayAction: "整理今天当前 source 的 Raw", target: "today"}
	}
	if exactAny(strings.ToLower(trimmed),
		"预览一下", "预览", "看看diff", "diff",
		"看一下", "看看", "看下", "看一下 diff", "看看 diff", "看下 diff",
	) {
		return intentRuleResult{intent: "diff", displayAction: "预览当前 plan"}
	}
	if exactAny(strings.ToLower(trimmed),
		"写进去", "确认写入", "批准", "批准这个", "approve", "apply",
		"同意", "确认", "通过", "可以写", "写吧",
	) {
		return intentRuleResult{intent: "approve", displayAction: "批准当前 plan"}
	}
	lower := strings.ToLower(trimmed)
	if exactAny(lower,
		"先不写", "不写", "不要写", "拒绝", "reject", "这版不行",
		"驳回", "撤回", "这版重来", "这版不要",
	) {
		return intentRuleResult{intent: "reject", displayAction: "拒绝当前 plan"}
	}
	return intentRuleResult{}
}

func stripIntentPrefix(text string, prefixes []string) (string, bool) {
	for _, prefix := range prefixes {
		if strings.HasPrefix(text, prefix) {
			payload := strings.TrimSpace(strings.TrimPrefix(text, prefix))
			return payload, payload != ""
		}
	}
	return "", false
}

func exactAny(value string, candidates ...string) bool {
	value = strings.TrimSpace(value)
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func normalizeIntentRouterMode(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = strings.ToLower(strings.TrimSpace(os.Getenv("OPENWHISKER_INTENT_ROUTER")))
	}
	switch value {
	case intentRouterModeOff:
		return intentRouterModeOff
	case intentRouterModeRules:
		return intentRouterModeRules
	case "", intentRouterModeHybrid:
		return intentRouterModeHybrid
	default:
		return intentRouterModeHybrid
	}
}
