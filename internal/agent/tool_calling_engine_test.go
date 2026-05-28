package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/vault/linkindex"
)

// scriptedClient returns predefined responses in order. Each Step represents
// one LLM turn the test wants to simulate.
type scriptedClient struct {
	steps []scriptedStep
	idx   int
}

type scriptedStep struct {
	resp ChatCompletionResponse
	err  error
}

func (c *scriptedClient) CreateChatCompletion(_ context.Context, _ ChatCompletionRequest) (ChatCompletionResponse, error) {
	if c.idx >= len(c.steps) {
		return ChatCompletionResponse{}, errors.New("scripted client exhausted")
	}
	step := c.steps[c.idx]
	c.idx++
	return step.resp, step.err
}

func toolCallResponse(tool, argsJSON string) ChatCompletionResponse {
	return ChatCompletionResponse{
		Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{
			{Index: 0, FinishReason: "tool_calls", Message: ChatMessage{
				Role: "assistant",
				ToolCalls: []ChatToolCall{
					{ID: "call_1", Type: "function", Function: ChatToolFunction{Name: tool, Arguments: argsJSON}},
				},
			}},
		},
	}
}

// capturingClient records every request it receives, then returns scripted
// responses in order. Used to assert what the engine actually sends back.
type capturingClient struct {
	steps    []scriptedStep
	idx      int
	requests []ChatCompletionRequest
}

func (c *capturingClient) CreateChatCompletion(_ context.Context, req ChatCompletionRequest) (ChatCompletionResponse, error) {
	c.requests = append(c.requests, req)
	if c.idx >= len(c.steps) {
		return ChatCompletionResponse{}, errors.New("capturing client exhausted")
	}
	step := c.steps[c.idx]
	c.idx++
	return step.resp, step.err
}

// toolCallResponseWithReasoning is toolCallResponse plus a thinking-mode
// reasoning_content on the assistant turn.
func toolCallResponseWithReasoning(tool, argsJSON, reasoning string) ChatCompletionResponse {
	resp := toolCallResponse(tool, argsJSON)
	resp.Choices[0].Message.ReasoningContent = reasoning
	return resp
}

// DeepSeek thinking mode requires the reasoning_content of a tool-calling
// assistant turn to be passed back in all subsequent requests, or the API
// returns 400. The engine re-appends the whole assistant message, so the field
// must survive into the next request body. See ChatMessage docs +
// internal/agent/CLAUDE.md.
func TestEngine_RoundTripsReasoningContentOnToolCallTurns(t *testing.T) {
	root := t.TempDir()
	writeVaultFile(t, root, "Knowledge/a.md", "# A\nThe answer is 42.")

	const reasoning = "The user asks for the answer; I should read Knowledge/a.md first."
	client := &capturingClient{steps: []scriptedStep{
		{resp: toolCallResponseWithReasoning("read_vault_note", `{"path":"Knowledge/a.md"}`, reasoning)},
		{resp: toolCallResponse(SubmitResultToolName, `{"title":"Done","summary":"Read note A.","payload":{"answer":42}}`)},
	}}

	runner := AgentRunner{
		VaultRoot:  root,
		ChatClient: client,
		Engine:     ToolCallingEngine{},
	}
	if _, err := runner.Run(context.Background(), AgentRunRequest{
		Skill:       testSkill([]string{"Knowledge/"}, []string{"read_vault_note"}),
		Query:       "What is the answer?",
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         time.Now(),
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(client.requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(client.requests))
	}
	second := client.requests[1]
	var found bool
	for _, m := range second.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			found = true
			if m.ReasoningContent != reasoning {
				t.Errorf("tool-call assistant reasoning_content = %q, want %q", m.ReasoningContent, reasoning)
			}
		}
	}
	if !found {
		t.Fatal("second request did not replay the tool-call assistant message")
	}
	body, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if !strings.Contains(string(body), `"reasoning_content"`) {
		t.Error("serialized request body missing reasoning_content field")
	}
}

func writeVaultFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func testSkill(scope []string, tools []string) scheduler.ScheduledSkill {
	return scheduler.ScheduledSkill{
		ID:         "test-skill",
		Name:       "Test",
		Engine:     "tool-calling",
		VaultScope: scope,
		VaultTools: tools,
		Budget: scheduler.ToolBudget{
			MaxToolCalls:        4,
			MaxTotalBytes:       100 * 1024,
			MaxWallClockSeconds: 30,
		},
		Body: "Test agent.",
	}
}

func TestEngine_HappyPath_ReadThenSubmit(t *testing.T) {
	root := t.TempDir()
	writeVaultFile(t, root, "Knowledge/a.md", "# A\nThe answer is 42.")

	client := &scriptedClient{steps: []scriptedStep{
		{resp: toolCallResponse("read_vault_note", `{"path":"Knowledge/a.md"}`)},
		{resp: toolCallResponse(SubmitResultToolName, `{"title":"Done","summary":"Read note A.","payload":{"answer":42}}`)},
	}}

	runner := AgentRunner{
		VaultRoot:  root,
		ChatClient: client,
		Engine:     ToolCallingEngine{},
	}
	result, err := runner.Run(context.Background(), AgentRunRequest{
		Skill:       testSkill([]string{"Knowledge/"}, []string{"read_vault_note"}),
		Query:       "What is the answer?",
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         time.Now(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != model.SchedulerRunStatusDone {
		t.Errorf("status = %q, want done", result.Status)
	}
	if result.Trace.Termination != model.AgentTraceTerminationNatural {
		t.Errorf("termination = %q, want natural", result.Trace.Termination)
	}
	if result.Title != "Done" || result.Summary != "Read note A." {
		t.Errorf("title/summary = %q / %q", result.Title, result.Summary)
	}
	if len(result.Trace.Calls) != 1 {
		t.Errorf("trace.calls = %d, want 1 (submit_result not in calls): %+v", len(result.Trace.Calls), result.Trace.Calls)
	}
	if result.Trace.Calls[0].Tool != "read_vault_note" {
		t.Errorf("first call tool = %q", result.Trace.Calls[0].Tool)
	}
}

func TestEngine_PathOutOfScopeReturnsErrorToLLM(t *testing.T) {
	root := t.TempDir()
	writeVaultFile(t, root, "Private/secret.md", "shh")
	writeVaultFile(t, root, "Knowledge/ok.md", "open")

	client := &scriptedClient{steps: []scriptedStep{
		{resp: toolCallResponse("read_vault_note", `{"path":"Private/secret.md"}`)},
		{resp: toolCallResponse(SubmitResultToolName, `{"title":"Bailing","summary":"Could not read out-of-scope file."}`)},
	}}
	runner := AgentRunner{VaultRoot: root, ChatClient: client, Engine: ToolCallingEngine{}}
	result, err := runner.Run(context.Background(), AgentRunRequest{
		Skill:       testSkill([]string{"Knowledge/"}, []string{"read_vault_note"}),
		Query:       "try a thing",
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         time.Now(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != model.SchedulerRunStatusDone {
		t.Errorf("status = %q, want done (LLM recovered)", result.Status)
	}
	if len(result.Trace.Calls) != 1 {
		t.Fatalf("trace.calls = %d", len(result.Trace.Calls))
	}
	if result.Trace.Calls[0].Error == "" || !strings.Contains(result.Trace.Calls[0].Error, "out_of_scope") {
		// out_of_scope or out_of_scope-like wording; just check it's non-empty
		if result.Trace.Calls[0].Error == "" {
			t.Errorf("expected non-empty error on out-of-scope tool call: %+v", result.Trace.Calls[0])
		}
	}
}

func TestEngine_ProtocolFailureAfterTwoFreeTexts(t *testing.T) {
	root := t.TempDir()
	writeVaultFile(t, root, "Knowledge/a.md", "")
	client := &scriptedClient{steps: []scriptedStep{
		{resp: ChatCompletionResponse{Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{{Message: ChatMessage{Role: "assistant", Content: "I would like to read a note"}}}}},
		{resp: ChatCompletionResponse{Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{{Message: ChatMessage{Role: "assistant", Content: "again, just text"}}}}},
	}}
	runner := AgentRunner{VaultRoot: root, ChatClient: client, Engine: ToolCallingEngine{}}
	result, _ := runner.Run(context.Background(), AgentRunRequest{
		Skill:       testSkill([]string{"Knowledge/"}, []string{"read_vault_note"}),
		Query:       "noop",
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         time.Now(),
	})
	if result.Status != model.SchedulerRunStatusFailed {
		t.Errorf("status = %q, want failed", result.Status)
	}
	if result.Trace.Termination != model.AgentTraceTerminationProtocolFailed {
		t.Errorf("termination = %q, want protocol_failed", result.Trace.Termination)
	}
}

func TestEngine_BudgetCallExhaustionTriggersForceFinalize(t *testing.T) {
	root := t.TempDir()
	writeVaultFile(t, root, "Knowledge/a.md", "small")
	// Build a skill with max_tool_calls=1 so the first vault call exhausts.
	skill := testSkill([]string{"Knowledge/"}, []string{"read_vault_note"})
	skill.Budget.MaxToolCalls = 1

	client := &scriptedClient{steps: []scriptedStep{
		{resp: toolCallResponse("read_vault_note", `{"path":"Knowledge/a.md"}`)},
		// After force-finalize is engaged, the model is expected to call
		// submit_result. We simulate exactly that.
		{resp: toolCallResponse(SubmitResultToolName, `{"title":"Done","summary":"Ran out of tool budget."}`)},
	}}
	runner := AgentRunner{VaultRoot: root, ChatClient: client, Engine: ToolCallingEngine{}}
	result, err := runner.Run(context.Background(), AgentRunRequest{
		Skill: skill, Query: "do thing", TriggerKind: model.AgentTriggerKindAdhocCLI, Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != model.SchedulerRunStatusPartial {
		t.Errorf("status = %q, want partial", result.Status)
	}
	if result.Trace.Termination != model.AgentTraceTerminationCallCountExceeded {
		t.Errorf("termination = %q, want call_count_exceeded", result.Trace.Termination)
	}
}

func TestEngine_VaultBacklinksReturnsNotReady(t *testing.T) {
	root := t.TempDir()
	writeVaultFile(t, root, "Knowledge/a.md", "")
	// Build an Index but do NOT call Build, so it stays not-ready.
	ix := linkindex.New(root, nil)
	client := &scriptedClient{steps: []scriptedStep{
		{resp: toolCallResponse("vault_backlinks", `{"path":"Knowledge/a.md"}`)},
		{resp: toolCallResponse(SubmitResultToolName, `{"title":"Bail","summary":"Index not ready"}`)},
	}}
	runner := AgentRunner{VaultRoot: root, ChatClient: client, Engine: ToolCallingEngine{}}
	result, _ := runner.Run(context.Background(), AgentRunRequest{
		Skill:       testSkill([]string{"Knowledge/"}, []string{"vault_backlinks"}),
		Query:       "anything",
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         time.Now(),
		LinkIndex:   ix,
	})
	if len(result.Trace.Calls) != 1 {
		t.Fatalf("calls = %d", len(result.Trace.Calls))
	}
	errText := result.Trace.Calls[0].Error
	if !strings.Contains(errText, "link index") || !strings.Contains(errText, "not") {
		t.Errorf("expected link-index-not-ready error, got %q", errText)
	}
}

func TestSubmitResultParse(t *testing.T) {
	title, summary, payload, err := parseSubmitResult(`{"title":"T","summary":"S","payload":{"x":1}}`)
	if err != nil {
		t.Fatalf("parseSubmitResult: %v", err)
	}
	if title != "T" || summary != "S" {
		t.Errorf("title/summary = %q/%q", title, summary)
	}
	var p map[string]int
	if err := json.Unmarshal(payload, &p); err != nil || p["x"] != 1 {
		t.Errorf("payload = %s, %v", string(payload), err)
	}

	if _, _, _, err := parseSubmitResult(`{"title":"","summary":"S"}`); err == nil {
		t.Errorf("expected empty title to fail")
	}
}
