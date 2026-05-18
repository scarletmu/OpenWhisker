package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

func TestOpenAIIntentClassifierBuildsMinimalStructuredRequest(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"intent": "raw_capture",
		"target": "active_bucket",
		"capture_action": "append",
		"bucket_relation": "same_topic",
		"payload_text": "补充 RPC retry 的一个边界条件",
		"additional_payload_text": "",
		"confidence_label": "high",
		"confidence": 0.91,
		"reason": "user asks to add to the current capture"
	}`}
	started := time.Date(2026, 5, 16, 10, 0, 0, 0, time.UTC)
	req := core.IntentClassifierRequest{
		Message:    "这个也补进去：RPC retry 的边界条件",
		SourceKind: model.AdapterMatrix,
		ActiveBucket: &core.IntentActiveBucketSummary{
			Status:      model.CaptureBucketStatusActive,
			StartedAt:   started,
			UpdatedAt:   started.Add(2 * time.Minute),
			TopicHint:   "RPC retry",
			Excerpt:     "已有关于 RPC retry 的记录",
			AppendCount: 2,
		},
		PendingPlanCount: 1,
	}

	result, err := (OpenAIIntentClassifier{Client: client}).ClassifyIntent(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Intent != "raw_capture" || result.CaptureAction != "append" || result.BucketRelation != "same_topic" {
		t.Fatalf("classifier result = %+v, want append same_topic raw_capture", result)
	}
	if client.request.MaxOutputTokens != 700 || client.request.Store {
		t.Fatalf("request token/store = %d/%v, want 700/false", client.request.MaxOutputTokens, client.request.Store)
	}
	if client.request.Text.Format.Type != "json_object" {
		t.Fatalf("response format = %+v, want json_object (no server-side schema)", client.request.Text.Format)
	}
	if !strings.Contains(client.request.Instructions, "JSON object") ||
		!strings.Contains(client.request.Instructions, "raw_capture") ||
		!strings.Contains(client.request.Instructions, "confidence_label") {
		t.Fatalf("instructions = %q, want enum list and JSON guidance in prompt", client.request.Instructions)
	}
	var sent core.IntentClassifierRequest
	if err := json.Unmarshal([]byte(client.request.Input), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Message != req.Message || sent.SourceKind != model.AdapterMatrix || sent.PendingPlanCount != 1 {
		t.Fatalf("sent request = %+v, want minimal classifier input", sent)
	}
	if sent.ActiveBucket == nil || sent.ActiveBucket.Excerpt != "已有关于 RPC retry 的记录" {
		t.Fatalf("sent active bucket = %+v, want summary only", sent.ActiveBucket)
	}
	if strings.Contains(client.request.Input, "source_key") ||
		strings.Contains(client.request.Input, "room_id") ||
		strings.Contains(client.request.Input, "before_hash") {
		t.Fatalf("classifier input leaks forbidden execution context: %s", client.request.Input)
	}
}

func TestOpenAIIntentClassifierRetriesOnceOnEmptyContent(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{
		"",
		`{
			"intent": "raw_capture",
			"target": "active_bucket",
			"capture_action": "append",
			"bucket_relation": "same_topic",
			"payload_text": "ok",
			"additional_payload_text": "",
			"confidence_label": "high",
			"confidence": 0.9,
			"reason": "ok"
		}`,
	}}

	result, err := (OpenAIIntentClassifier{Client: client}).ClassifyIntent(context.Background(), core.IntentClassifierRequest{Message: "hi"})
	if err != nil {
		t.Fatalf("ClassifyIntent err = %v, want success after retry", err)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, want exactly 2", client.calls)
	}
	if result.Intent != "raw_capture" || result.ConfidenceLabel != "high" {
		t.Fatalf("result = %+v, want valid raw_capture/high", result)
	}
}

func TestOpenAIIntentClassifierFailsAfterTwoEmptyContents(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{"", "   "}}

	_, err := (OpenAIIntentClassifier{Client: client}).ClassifyIntent(context.Background(), core.IntentClassifierRequest{Message: "hi"})
	if err == nil || !strings.Contains(err.Error(), "empty content after one retry") {
		t.Fatalf("err = %v, want empty-content-after-retry error", err)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, want exactly 2", client.calls)
	}
}

type sequenceCompatibleClient struct {
	outputs []string
	calls   int
}

func (c *sequenceCompatibleClient) CreateResponse(_ context.Context, _ openAIResponseRequest) (string, error) {
	if c.calls >= len(c.outputs) {
		return "", nil
	}
	out := c.outputs[c.calls]
	c.calls++
	return out, nil
}

func TestOpenAIIntentClassifierRejectsInvalidStructuredOutput(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"intent": "shell_command",
		"target": "none",
		"capture_action": "none",
		"bucket_relation": "unclear",
		"payload_text": "",
		"additional_payload_text": "",
		"confidence_label": "high",
		"confidence": 0.99,
		"reason": "invalid"
	}`}

	_, err := (OpenAIIntentClassifier{Client: client}).ClassifyIntent(context.Background(), core.IntentClassifierRequest{})
	if err == nil || !strings.Contains(err.Error(), "invalid intent") {
		t.Fatalf("ClassifyIntent error = %v, want invalid intent validation error", err)
	}
}
