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

type intentRuleResult struct {
	intent        string
	displayAction string
	payload       string
	target        string
}

type intentAuditEvent struct {
	TS              time.Time `json:"ts"`
	Mode            string    `json:"mode"`
	SourceKind      string    `json:"source_kind"`
	RulesIntent     string    `json:"rules_intent,omitempty"`
	ClassifierUsed  bool      `json:"classifier_used"`
	ModelIntent     string    `json:"model_intent,omitempty"`
	Target          string    `json:"target,omitempty"`
	CaptureAction   string    `json:"capture_action,omitempty"`
	BucketRelation  string    `json:"bucket_relation,omitempty"`
	ConfidenceLabel string    `json:"confidence_label,omitempty"`
	Confidence      float64   `json:"confidence,omitempty"`
	AcceptedIntent  string    `json:"accepted_intent,omitempty"`
	Accepted        bool      `json:"accepted"`
	Error           string    `json:"error,omitempty"`
}

func (s AdapterService) handleIntentText(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
	result := s.classifyIntent(ctx, req)
	switch result.intent {
	case "raw_create":
		return s.handleIntentRawCreate(ctx, req, result)
	case "raw_append":
		return s.handleIntentRawAppend(ctx, req, result)
	case "raw_close":
		return s.handleIntentRawClose(req, result)
	case "organize":
		return s.handleIntentOrganize(ctx, req, result)
	case "diff":
		return s.handleIntentDiff(result, req.SourceKey)
	case "approve":
		return s.handleIntentApprove(ctx, result, req.SourceKey)
	case "reject":
		return s.handleIntentReject(result, req.SourceKey)
	default:
		return AdapterResponse{
			Status: "unclear",
			Body:   "无法安全判断这条消息要记录、整理还是审批；未写入 Raw，也未执行任何计划。请使用“记录一下：...”或 slash 命令。",
		}, nil
	}
}

func (s AdapterService) classifyIntent(ctx context.Context, req AdapterRequest) intentRuleResult {
	result := classifyIntentRules(req.Text)
	if result.intent != "" && result.intent != "unclear" {
		s.writeIntentAudit(intentAuditEvent{
			TS:             s.now(),
			Mode:           s.intentRouterMode,
			SourceKind:     adapterSourceKind(req.Adapter),
			RulesIntent:    result.intent,
			AcceptedIntent: result.intent,
			Accepted:       true,
		})
		return result
	}
	if s.intentRouterMode != intentRouterModeHybrid || s.intentClassifier == nil {
		s.writeIntentAudit(intentAuditEvent{
			TS:          s.now(),
			Mode:        s.intentRouterMode,
			SourceKind:  adapterSourceKind(req.Adapter),
			RulesIntent: result.intent,
			Accepted:    false,
		})
		return result
	}
	classified, err := s.intentClassifier.ClassifyIntent(ctx, s.intentClassifierRequest(req))
	if err != nil {
		s.writeIntentAudit(intentAuditEvent{
			TS:             s.now(),
			Mode:           s.intentRouterMode,
			SourceKind:     adapterSourceKind(req.Adapter),
			RulesIntent:    result.intent,
			ClassifierUsed: true,
			Accepted:       false,
			Error:          err.Error(),
		})
		return result
	}
	mapped := s.modelIntentToRuleResult(req, classified)
	s.writeIntentAudit(intentAuditEvent{
		TS:              s.now(),
		Mode:            s.intentRouterMode,
		SourceKind:      adapterSourceKind(req.Adapter),
		RulesIntent:     result.intent,
		ClassifierUsed:  true,
		ModelIntent:     classified.Intent,
		Target:          classified.Target,
		CaptureAction:   classified.CaptureAction,
		BucketRelation:  classified.BucketRelation,
		ConfidenceLabel: classified.ConfidenceLabel,
		Confidence:      classified.Confidence,
		AcceptedIntent:  mapped.intent,
		Accepted:        mapped.intent != "",
	})
	return mapped
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
	if payload, ok := stripIntentPrefix(trimmed, []string{"记录一下：", "记录一下:", "开始记录：", "开始记录:", "帮我收一下这个材料：", "帮我收一下这个材料:"}); ok {
		return intentRuleResult{intent: "raw_create", displayAction: "创建当前记录组", payload: payload}
	}
	if payload, ok := stripIntentPrefix(trimmed, []string{"补充：", "补充:", "继续：", "继续:", "还有：", "还有:"}); ok {
		return intentRuleResult{intent: "raw_append", displayAction: "追加当前记录组", payload: payload}
	}
	if exactAny(trimmed, "结束记录", "这组结束", "先到这里") {
		return intentRuleResult{intent: "raw_close", displayAction: "结束当前记录组"}
	}
	if exactAny(trimmed, "整理刚才", "处理这组", "整理这组", "处理刚才") {
		return intentRuleResult{intent: "organize", displayAction: "整理当前记录组", target: "active"}
	}
	if exactAny(trimmed, "处理今天", "整理今天") {
		return intentRuleResult{intent: "organize", displayAction: "整理今天当前 source 的 Raw", target: "today"}
	}
	if exactAny(trimmed, "预览一下", "预览", "看看diff", "diff") {
		return intentRuleResult{intent: "diff", displayAction: "预览当前 plan"}
	}
	if exactAny(strings.ToLower(trimmed), "写进去", "确认写入", "批准", "批准这个", "approve", "apply") {
		return intentRuleResult{intent: "approve", displayAction: "批准当前 plan"}
	}
	lower := strings.ToLower(trimmed)
	if exactAny(lower, "先不写", "不写", "不要写", "拒绝", "reject", "这版不行") {
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
