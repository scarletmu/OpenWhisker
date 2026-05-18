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

func TestIntentRouterClassifierMediumWithoutActiveBucketOffersTwoCandidates(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "new_bucket",
			CaptureAction:   "create",
			BucketRelation:  "unclear",
			PayloadText:     "可能是要记录",
			ConfidenceLabel: "medium",
			Confidence:      0.5,
		},
	})
	defer cleanup()

	response, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "这句话也许该记录下来",
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "clarification_requested" {
		t.Fatalf("response = %+v, want clarification_requested", response)
	}
	if !strings.Contains(response.Body, "1. 新建一组") || !strings.Contains(response.Body, "2. 取消") {
		t.Fatalf("response body = %q, want 2 candidates (new/cancel)", response.Body)
	}
	if strings.Contains(response.Body, "补充到上一组") {
		t.Fatalf("response body = %q, must not offer append without active bucket", response.Body)
	}
	pending, err := service.store.ActivePendingClarification("matrix:room:user", time.Now().UTC())
	if err != nil {
		t.Fatalf("expected pending clarification, got err = %v", err)
	}
	if len(pending.CandidateActions) != 2 {
		t.Fatalf("candidates = %+v, want 2", pending.CandidateActions)
	}
	if pending.CandidateActions[0].Action != model.ClarificationActionRawCreate ||
		pending.CandidateActions[1].Action != model.ClarificationActionCancel {
		t.Fatalf("candidates = %+v, want [raw_create, cancel]", pending.CandidateActions)
	}
}

func TestIntentRouterClarificationReplyCreatesBucketWhenNoActiveBucket(t *testing.T) {
	service, cleanup := newIntentRouterTestService(t, fakeIntentClassifier{
		result: IntentClassifierResult{
			Intent:          "raw_capture",
			Target:          "new_bucket",
			CaptureAction:   "create",
			BucketRelation:  "unclear",
			ConfidenceLabel: "medium",
			Confidence:      0.5,
		},
	})
	defer cleanup()

	if _, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter:   model.AdapterMatrix,
		SourceKey: "matrix:room:user",
		Text:      "也许该记下这一段",
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
	if response.Status != model.JobStatusDone || !strings.Contains(response.Body, "创建当前记录组") {
		t.Fatalf("reply response = %+v, want bucket create", response)
	}
	bucket, err := service.store.ActiveCaptureBucket("matrix:room:user")
	if err != nil {
		t.Fatal(err)
	}
	if bucket.AppendCount != 1 {
		t.Fatalf("append count = %d, want 1 for fresh create", bucket.AppendCount)
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

func TestClassifyIntentRulesCoversCommonPhrasings(t *testing.T) {
	type expect struct {
		intent  string
		payload string
		target  string
	}
	cases := map[string]expect{
		"记一下：今天的灵感": {intent: "raw_create", payload: "今天的灵感"},
		"写下来：一个想法":  {intent: "raw_create", payload: "一个想法"},
		"存一下：会议要点":  {intent: "raw_create", payload: "会议要点"},
		"再加：还有一点":   {intent: "raw_append", payload: "还有一点"},
		"再补充：另一个角度": {intent: "raw_append", payload: "另一个角度"},
		"后面还有：补一句":  {intent: "raw_append", payload: "补一句"},
		"另外：单独说明":   {intent: "raw_append", payload: "单独说明"},
		"结束这组":      {intent: "raw_close"},
		"就这些":       {intent: "raw_close"},
		"到这里":       {intent: "raw_close"},
		"整理一下":      {intent: "organize", target: "active"},
		"处理一下":      {intent: "organize", target: "active"},
		"整理今天的":     {intent: "organize", target: "today"},
		"看一下":       {intent: "diff"},
		"看看":        {intent: "diff"},
		"看一下 diff":  {intent: "diff"},
		"同意":        {intent: "approve"},
		"通过":        {intent: "approve"},
		"写吧":        {intent: "approve"},
		"驳回":        {intent: "reject"},
		"这版重来":      {intent: "reject"},
	}
	for in, want := range cases {
		got := classifyIntentRules(in)
		if got.intent != want.intent {
			t.Errorf("classifyIntentRules(%q).intent = %q, want %q", in, got.intent, want.intent)
			continue
		}
		if want.payload != "" && got.payload != want.payload {
			t.Errorf("classifyIntentRules(%q).payload = %q, want %q", in, got.payload, want.payload)
		}
		if want.target != "" && got.target != want.target {
			t.Errorf("classifyIntentRules(%q).target = %q, want %q", in, got.target, want.target)
		}
	}

	misses := []string{"", "你好", "今天天气怎么样", "随便聊聊", "记录一下", "补充："}
	for _, in := range misses {
		got := classifyIntentRules(in)
		if got.intent != "" {
			t.Errorf("classifyIntentRules(%q).intent = %q, want empty", in, got.intent)
		}
	}
}

func TestParseCandidateNumberAcceptsCommonVariants(t *testing.T) {
	cases := map[string]int{
		"1":     1,
		"1.":    1,
		"1。":    1,
		"1)":    1,
		"1）":    1,
		"(1)":   1,
		"（1）":   1,
		"[1]":   1,
		"【1】":   1,
		"①":     1,
		"一":     1,
		"选1":    1,
		"我选 1":  1,
		"我选 1.": 1,
		"第 1":   1,
		"第1.":   1,
		"要 1":   1,
		"选项1":   1,
		"2":     2,
		"2、":    2,
		"②":     2,
		"二":     2,
		"3":     3,
		"3.":    3,
		"③":     3,
		"4":     4,
		"④":     4,
		" 1 ":   1,
		"\t1\n": 1,
		"我选 3。": 3,
	}
	for in, want := range cases {
		got, ok := parseCandidateNumber(in)
		if !ok || got != want {
			t.Errorf("parseCandidateNumber(%q) = (%d, %v), want (%d, true)", in, got, ok, want)
		}
	}

	rejects := []string{"", "5", "0", "选", "我选 5", "10", "1a", "选项", "abc"}
	for _, in := range rejects {
		if got, ok := parseCandidateNumber(in); ok {
			t.Errorf("parseCandidateNumber(%q) = (%d, true), want rejection", in, got)
		}
	}
}

func TestLabelMatchesReplyKnownSynonyms(t *testing.T) {
	appendCand := model.CandidateAction{Action: model.ClarificationActionRawAppend, Label: "补充到上一组"}
	createCand := model.CandidateAction{Action: model.ClarificationActionRawCreate, Label: "新建一组"}
	cancelCand := model.CandidateAction{Action: model.ClarificationActionCancel, Label: "取消"}

	appendOK := []string{"补充", "补充上", "加上", "加上去", "并入", "加到上一组", "补到上一组", "加进上一组", "合并", "接着上一组", "续上", "接着上面"}
	for _, s := range appendOK {
		if !labelMatchesReply(s, appendCand) {
			t.Errorf("labelMatchesReply(%q, append) = false, want true", s)
		}
	}

	createOK := []string{"新建", "新建一组", "新的一组", "另开", "另开一组", "另起", "另起一组", "新开", "新开一组", "单独一组", "单独开一组"}
	for _, s := range createOK {
		if !labelMatchesReply(s, createCand) {
			t.Errorf("labelMatchesReply(%q, create) = false, want true", s)
		}
	}

	cancelOK := []string{"取消", "算了", "都不要", "都不用", "都不写", "先不处理"}
	for _, s := range cancelOK {
		if !labelMatchesReply(s, cancelCand) {
			t.Errorf("labelMatchesReply(%q, cancel) = false, want true", s)
		}
	}

	if labelMatchesReply("补充", createCand) {
		t.Errorf("append phrase should not match create candidate")
	}
	if labelMatchesReply("新建", appendCand) {
		t.Errorf("create phrase should not match append candidate")
	}
	if labelMatchesReply("不相关的话", appendCand) || labelMatchesReply("不相关的话", createCand) || labelMatchesReply("不相关的话", cancelCand) {
		t.Errorf("unrelated text should match none")
	}
}

func TestMatchClarificationReplyHandlesNumberAndPhraseAndCancel(t *testing.T) {
	pending := model.PendingClarification{
		ID: "clar_x",
		CandidateActions: []model.CandidateAction{
			{Action: model.ClarificationActionRawAppend, Label: "补充到上一组"},
			{Action: model.ClarificationActionRawCreate, Label: "新建一组"},
			{Action: model.ClarificationActionCancel, Label: "取消"},
		},
	}
	cases := map[string]string{
		"1":     model.ClarificationActionRawAppend,
		"1)":    model.ClarificationActionRawAppend,
		"我选 1.": model.ClarificationActionRawAppend,
		"加上去":   model.ClarificationActionRawAppend,
		"补充":    model.ClarificationActionRawAppend,
		"2":     model.ClarificationActionRawCreate,
		"②":     model.ClarificationActionRawCreate,
		"另起一组":  model.ClarificationActionRawCreate,
		"3":     model.ClarificationActionCancel,
		"取消":    model.ClarificationActionCancel,
		"算了":    model.ClarificationActionCancel,
		"都不用":   model.ClarificationActionCancel,
	}
	for in, want := range cases {
		_, action, ok := matchClarificationReply(in, pending)
		if !ok || action != want {
			t.Errorf("matchClarificationReply(%q) = (%q, %v), want (%q, true)", in, action, ok, want)
		}
	}

	rejects := []string{"", "5", "随便", "不知道", "明天再说"}
	for _, in := range rejects {
		if _, action, ok := matchClarificationReply(in, pending); ok {
			t.Errorf("matchClarificationReply(%q) = (%q, true), want rejection", in, action)
		}
	}
}
