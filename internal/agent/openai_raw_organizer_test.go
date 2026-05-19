package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

func TestOpenAIRawOrganizerBuildsMediumRiskPlan(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"title": "Phase4 记忆模型",
		"raw_kind": "concept-seed",
		"summary": "整理 Phase4 agent 记忆模型。",
		"draft_body": "## 核心观点\n\nagent 的长期记忆来自 vault，不来自模型内部状态。",
		"review_items": ["确认是否需要补充 Matrix 入口说明。"]
	}`}
	req := core.RawOrganizerRequest{
		Job:     model.WikiJob{ID: "job_plan", Type: model.JobTypeOrganizeRaw},
		RawJob:  model.WikiJob{ID: "job_raw"},
		RawPath: "Raw/Inbox/job_raw.md",
		VaultContext: core.RawOrganizerContext{
			RawPath: "Raw/Inbox/job_raw.md",
			RawNote: "# Raw Capture\n\nPhase4 memory",
		},
		Now: time.Date(2026, 5, 13, 1, 2, 3, 0, time.UTC),
	}

	plan, err := (OpenAIRawOrganizer{Client: client}).OrganizeRaw(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RiskLevel != model.RiskMedium || !plan.RequiresApproval {
		t.Fatalf("plan risk/approval = %q/%v, want medium/true", plan.RiskLevel, plan.RequiresApproval)
	}
	if plan.Summary != "整理 Phase4 agent 记忆模型。" {
		t.Fatalf("summary = %q", plan.Summary)
	}
	if len(plan.Operations) != 2 {
		t.Fatalf("operation count = %d, want 2", len(plan.Operations))
	}
	if plan.Operations[0].TargetPath != "Knowledge/Drafts/job_raw.md" {
		t.Fatalf("knowledge target = %q", plan.Operations[0].TargetPath)
	}
	if !strings.Contains(plan.Operations[0].PayloadJSON, "agent 的长期记忆来自 vault") {
		t.Fatalf("create payload = %s, want LLM draft body", plan.Operations[0].PayloadJSON)
	}
	if !strings.Contains(plan.Operations[1].PayloadJSON, "Raw/Processed/job_raw.md") ||
		!strings.Contains(plan.Operations[1].PayloadJSON, "Knowledge/Drafts/job_raw.md") ||
		!strings.Contains(plan.Operations[1].PayloadJSON, "确认是否需要补充 Matrix 入口说明") {
		t.Fatalf("move payload = %s, want processing note with output and review trace", plan.Operations[1].PayloadJSON)
	}
	if client.request.Text.Format.Type != "json_object" {
		t.Fatalf("response format = %+v, want json_object", client.request.Text.Format)
	}
	if !strings.Contains(client.request.Instructions, "JSON object MUST contain exactly these fields") {
		t.Fatalf("instructions missing inline schema guidance: %q", client.request.Instructions)
	}
	if !strings.Contains(client.request.Input, "Phase4 memory") {
		t.Fatalf("input = %q, want raw note context", client.request.Input)
	}
}

func TestOpenAIRawOrganizerRetriesOnceOnEmptyContent(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{
		"",
		`{
			"title": "ok",
			"raw_kind": "concept-seed",
			"summary": "ok",
			"draft_body": "ok",
			"review_items": []
		}`,
	}}
	req := core.RawOrganizerRequest{
		Job:     model.WikiJob{ID: "job_plan", Type: model.JobTypeOrganizeRaw},
		RawJob:  model.WikiJob{ID: "job_raw"},
		RawPath: "Raw/Inbox/job_raw.md",
		Now:     time.Date(2026, 5, 19, 1, 2, 3, 0, time.UTC),
	}
	if _, err := (OpenAIRawOrganizer{Client: client}).OrganizeRaw(context.Background(), req); err != nil {
		t.Fatalf("OrganizeRaw err = %v, want success after retry", err)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, want exactly 2", client.calls)
	}
}

func TestOpenAIRawOrganizerFailsAfterTwoEmptyContents(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{"", "   "}}
	_, err := (OpenAIRawOrganizer{Client: client}).OrganizeRaw(context.Background(), core.RawOrganizerRequest{})
	if err == nil || !strings.Contains(err.Error(), "empty content after one retry") {
		t.Fatalf("err = %v, want empty-content-after-retry error", err)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, want exactly 2", client.calls)
	}
}

func TestOpenAIRawOrganizerRejectsInvalidStructuredOutput(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"title": "Bad",
		"raw_kind": "unknown",
		"summary": "bad",
		"draft_body": "bad",
		"review_items": []
	}`}
	_, err := (OpenAIRawOrganizer{Client: client}).OrganizeRaw(context.Background(), core.RawOrganizerRequest{})
	if err == nil || !strings.Contains(err.Error(), "raw_kind") {
		t.Fatalf("OrganizeRaw error = %v, want raw_kind validation error", err)
	}
}

func TestOpenAIClientCallsOpenAICompatibleChatCompletionsAPI(t *testing.T) {
	transport := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.Path != "/v1/chat/completions" {
			t.Fatalf("request = %s %s, want POST /v1/chat/completions", req.Method, req.URL.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload openAIChatCompletionRequest
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != DefaultOpenAICompatibleModel {
			t.Fatalf("model = %q, want default %q", payload.Model, DefaultOpenAICompatibleModel)
		}
		if len(payload.Messages) != 2 || payload.Messages[0].Role != "system" || payload.Messages[1].Role != "user" {
			t.Fatalf("messages = %+v, want system and user messages", payload.Messages)
		}
		if payload.ResponseFormat.Type != "json_object" || payload.ResponseFormat.JSONSchema != nil {
			t.Fatalf("response format = %+v, want bare json_object", payload.ResponseFormat)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body: io.NopCloser(strings.NewReader(`{
				"id": "chatcmpl_test",
				"choices": [{
					"message": {"role": "assistant", "content": "{\"title\":\"ok\"}"}
				}]
			}`)),
			Header: make(http.Header),
		}, nil
	})
	client := OpenAIClient{
		APIKey:     "test-key",
		BaseURL:    "https://api.openai.test/v1",
		HTTPClient: &http.Client{Transport: transport},
	}

	text, err := client.CreateResponse(context.Background(), openAIResponseRequest{
		Instructions: "test",
		Input:        "test",
		Text:         openAITextSpec{Format: rawOrganizerResponseFormat()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != `{"title":"ok"}` {
		t.Fatalf("text = %q", text)
	}
}

type fakeCompatibleClient struct {
	request openAIResponseRequest
	output  string
}

func (c *fakeCompatibleClient) CreateResponse(_ context.Context, req openAIResponseRequest) (string, error) {
	c.request = req
	return c.output, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
