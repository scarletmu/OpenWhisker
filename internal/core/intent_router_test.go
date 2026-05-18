package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestIntentRouterHybridUsesModelAfterRulesMiss(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "new_bucket",
			CaptureAction:   "create",
			BucketRelation:  "new_topic",
			PayloadText:     "hybrid model capture",
			ConfidenceLabel: "high",
			Confidence:      0.93,
		},
	})
	defer cleanup()

	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "帮我记一下 hybrid model capture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != model.JobStatusDone || !strings.Contains(response.Body, "识别为：创建当前记录组") {
		t.Fatalf("response = %+v, want model-backed raw bucket create", response)
	}
	bucket, err := service.store.ActiveCaptureBucket("matrix:room:user")
	if err != nil {
		t.Fatal(err)
	}
	if bucket.AppendCount != 1 || bucket.SourceKey != "matrix:room:user" {
		t.Fatalf("bucket = %+v, want active source-scoped bucket", bucket)
	}
}

func TestIntentRouterModelAppendRequiresActiveSameTopicBucket(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "active_bucket",
			CaptureAction:   "append",
			BucketRelation:  "same_topic",
			PayloadText:     "append without bucket",
			ConfidenceLabel: "high",
			Confidence:      0.95,
		},
	})
	defer cleanup()

	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "这个也加进去",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "unclear" {
		t.Fatalf("response = %+v, want fail-closed unclear without active bucket", response)
	}

	created, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "记录一下：第一条",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != model.JobStatusDone {
		t.Fatalf("created response = %+v, want done", created)
	}
	service.intentClassifier = fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "active_bucket",
			CaptureAction:   "append",
			BucketRelation:  "new_topic",
			PayloadText:     "wrong topic append",
			ConfidenceLabel: "high",
			Confidence:      0.95,
		},
	}
	response, err = service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "另外一个话题也加进去",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "unclear" {
		t.Fatalf("response = %+v, want fail-closed unclear for non same_topic append", response)
	}
	bucket, err := service.store.ActiveCaptureBucket("matrix:room:user")
	if err != nil {
		t.Fatal(err)
	}
	if bucket.AppendCount != 1 {
		t.Fatalf("append count = %d, want unchanged 1", bucket.AppendCount)
	}
}

func TestIntentRouterModelApproveRequiresConfidenceAndSourceScopedPendingPlan(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{})
	defer cleanup()

	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:owner",
		Text:      "记录一下：需要整理的材料",
	}); err != nil {
		t.Fatal(err)
	}
	organized, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:owner",
		Text:      "整理刚才",
	})
	if err != nil {
		t.Fatal(err)
	}
	if organized.Status != model.PlanStatusAwaitingApproval {
		t.Fatalf("organized response = %+v, want awaiting approval", organized)
	}

	service.intentClassifier = fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "approve_request",
			Target:          "none",
			CaptureAction:   "none",
			BucketRelation:  "unclear",
			ConfidenceLabel: "high",
			Confidence:      0.84,
		},
	}
	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:owner",
		Text:      "看起来可以",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "unclear" {
		t.Fatalf("response = %+v, want unclear below approve confidence threshold", response)
	}
	plan, err := service.store.GetPlan(organized.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusAwaitingApproval {
		t.Fatalf("plan status = %q, want still awaiting approval", plan.Status)
	}

	service.intentClassifier = fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "approve_request",
			Target:          "none",
			CaptureAction:   "none",
			BucketRelation:  "unclear",
			ConfidenceLabel: "high",
			Confidence:      0.96,
		},
	}
	response, err = service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:other",
		Text:      "批准这个计划",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "no_pending_plan" {
		t.Fatalf("response = %+v, want source-scoped no pending plan", response)
	}
	plan, err = service.store.GetPlan(organized.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusAwaitingApproval {
		t.Fatalf("plan status = %q, want still awaiting approval after other source approve", plan.Status)
	}
}

func TestIntentRouterWritesPrivacySafeAuditJSONL(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "new_bucket",
			CaptureAction:   "create",
			BucketRelation:  "new_topic",
			PayloadText:     "audit payload",
			ConfidenceLabel: "high",
			Confidence:      0.93,
		},
	})
	defer cleanup()
	auditPath := filepath.Join(t.TempDir(), "intent-router.jsonl")
	t.Setenv("OPENWHISKER_INTENT_AUDIT_FILE", auditPath)

	_, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		EventID:   "$intent-audit",
		Sender:    "@user:example.test",
		SourceKey: "matrix:room:user",
		Text:      "帮我记一下 audit payload",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(lines))
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatal(err)
	}
	if event["classifier_used"] != true || event["accepted_intent"] != "raw_create" {
		t.Fatalf("audit event = %+v, want accepted classifier result", event)
	}
	if event["executed_action"] != "created" {
		t.Fatalf("audit event executed_action = %v, want created", event["executed_action"])
	}
	if _, ok := event["source_key"]; ok {
		t.Fatalf("audit event leaks source_key: %+v", event)
	}
	if _, ok := event["message"]; ok {
		t.Fatalf("audit event leaks message text: %+v", event)
	}
}

func TestIntentRouterAuditExecutedActionRejectedNoActiveBucket(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{})
	defer cleanup()
	auditPath := filepath.Join(t.TempDir(), "intent-router.jsonl")
	t.Setenv("OPENWHISKER_INTENT_AUDIT_FILE", auditPath)

	// Rules-driven raw_append with no active bucket: dispatcher reaches
	// handleIntentRawAppend which returns no_active_bucket status. The audit row
	// should record accepted=true (router adopted the intent) but
	// executed_action=rejected_no_active_bucket (handler refused to write).
	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "补充：第一条补充",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1; lines=%v", len(lines), lines)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatal(err)
	}
	if event["accepted"] != true || event["accepted_intent"] != "raw_append" {
		t.Fatalf("audit event = %+v, want accepted raw_append", event)
	}
	if event["executed_action"] != "rejected_no_active_bucket" {
		t.Fatalf("executed_action = %v, want rejected_no_active_bucket; event=%+v", event["executed_action"], event)
	}
}

func TestIntentRouterClassifierMediumCreatesPendingClarification(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{})
	defer cleanup()
	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "记录一下：第一条材料",
	}); err != nil {
		t.Fatal(err)
	}

	service.intentClassifier = fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "active_bucket",
			CaptureAction:   "append",
			BucketRelation:  "unclear",
			PayloadText:     "另一段材料",
			ConfidenceLabel: "medium",
			Confidence:      0.55,
		},
	}
	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "另一段材料，可能相关",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "clarification_requested" {
		t.Fatalf("response = %+v, want clarification_requested", response)
	}
	for _, want := range []string{"1. 补充到上一组", "2. 新建一组", "3. 取消"} {
		if !strings.Contains(response.Body, want) {
			t.Fatalf("response body = %q, missing %q", response.Body, want)
		}
	}
	pending, err := service.store.ActivePendingClarification("matrix:room:user", time.Now().UTC())
	if err != nil {
		t.Fatalf("expected pending clarification, got err = %v", err)
	}
	if pending.OriginalMessage != "另一段材料，可能相关" {
		t.Fatalf("original_message = %q, want preserved user text", pending.OriginalMessage)
	}
	if len(pending.CandidateActions) != 3 {
		t.Fatalf("candidates = %+v, want 3", pending.CandidateActions)
	}
}

func TestIntentRouterReplyResolvesPendingClarificationAndAppends(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{})
	defer cleanup()
	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "记录一下：第一条材料",
	}); err != nil {
		t.Fatal(err)
	}
	service.intentClassifier = fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "active_bucket",
			CaptureAction:   "append",
			BucketRelation:  "unclear",
			ConfidenceLabel: "medium",
			Confidence:      0.55,
		},
	}
	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "继续这条线索的另一段",
	}); err != nil {
		t.Fatal(err)
	}

	service.intentClassifier = fakeIntentClassifier{}
	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != model.JobStatusDone || !strings.Contains(response.Body, "追加当前记录组") {
		t.Fatalf("reply response = %+v, want bucket append", response)
	}
	bucket, err := service.store.ActiveCaptureBucket("matrix:room:user")
	if err != nil {
		t.Fatal(err)
	}
	if bucket.AppendCount != 2 {
		t.Fatalf("append count = %d, want 2 after clarification append", bucket.AppendCount)
	}
	if _, err := service.store.ActivePendingClarification("matrix:room:user", time.Now().UTC()); err == nil {
		t.Fatalf("expected pending clarification resolved, but it still active")
	}
}

func TestIntentRouterNewMessageAutoCancelsPendingClarification(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{})
	defer cleanup()
	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "记录一下：第一条材料",
	}); err != nil {
		t.Fatal(err)
	}
	service.intentClassifier = fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "active_bucket",
			CaptureAction:   "append",
			BucketRelation:  "unclear",
			ConfidenceLabel: "medium",
			Confidence:      0.55,
		},
	}
	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "可能相关的另一段",
	}); err != nil {
		t.Fatal(err)
	}

	service.intentClassifier = fakeIntentClassifier{}
	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "结束记录",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Body, "结束当前记录组") {
		t.Fatalf("response = %+v, want bucket close after auto-cancel", response)
	}
	if _, err := service.store.ActivePendingClarification("matrix:room:user", time.Now().UTC()); err == nil {
		t.Fatalf("expected pending clarification auto-cancelled, but still active")
	}
}

func newIntentRouterTestService(t *testing.T, classifier IntentClassifier) (AdapterService, func()) {
	t.Helper()
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{
		IntentRouterMode: "hybrid",
		IntentClassifier: classifier,
	})
	return service, func() {
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

type fakeIntentClassifier struct {
	result IntentClassifierResult
	err    error
	calls  int
}

func (c fakeIntentClassifier) ClassifyIntent(context.Context, IntentClassifierRequest) (IntentClassifierResult, error) {
	return c.result, c.err
}
