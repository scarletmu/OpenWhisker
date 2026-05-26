package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/sanitize"
	"github.com/scarletmu/openwhisker/internal/scheduler"
)

// ToolCallingEngine is the Phase 6 multi-turn LLM driver. It is intentionally
// the only place that speaks OpenAI Chat Completions tool-call wire format;
// hosts and tools never touch the protocol directly.
type ToolCallingEngine struct {
	// PerCallMaxTokens caps the model's output per turn. Defaults to client
	// default (OpenAIClient.MaxOutput) when zero.
	PerCallMaxTokens int
}

// EngineConfig is the per-run configuration AgentRunner passes in.
type EngineConfig struct {
	ChatClient            ChatCompletionClient
	Model                 string
	ToolCatalog           []ChatTool
	Executor              VaultToolExecutor
	MaxProtocolViolations int
	DebugWriter           io.Writer
}

// Run executes the multi-turn loop. It always returns a populated
// AgentRunResult (even on failure); the caller persists trace/result either
// way so audit history is consistent.
func (e ToolCallingEngine) Run(ctx context.Context, cfg EngineConfig, req AgentRunRequest) AgentRunResult {
	now := time.Now()
	budget := req.Skill.Budget
	result := AgentRunResult{
		SkillID: req.Skill.ID,
		Trace: AgentTrace{
			Termination: model.AgentTraceTerminationError, // overwritten on natural exit
		},
	}

	messages := buildInitialMessages(req)
	writeDebug(cfg.DebugWriter, "init", messages, nil)

	protocolViolations := 0
	forceFinalize := false
	seq := 0

	for {
		if err := ctx.Err(); err != nil {
			result.Trace.Termination = model.AgentTraceTerminationWallClockExceeded
			result.Status = model.SchedulerRunStatusFailed
			result.Error = sanitize.FreeText("agent wall clock exceeded")
			return result
		}

		// Refresh the budget line before every LLM call.
		messages = setBudgetSystemNote(messages, budgetSnapshot(budget, now), forceFinalize)

		chatReq := ChatCompletionRequest{
			Model:     cfg.Model,
			Messages:  messages,
			Tools:     cfg.ToolCatalog,
			ToolChoice: "auto",
			MaxTokens: e.PerCallMaxTokens,
		}
		if forceFinalize {
			// Per spec, when in force-finalize mode bias the model toward
			// submit_result. The "required" choice is honored by OpenAI and
			// DeepSeek; older providers may ignore it but the system note
			// above is the load-bearing instruction.
			chatReq.ToolChoice = map[string]any{
				"type": "function",
				"function": map[string]any{"name": SubmitResultToolName},
			}
		}

		writeDebug(cfg.DebugWriter, "llm_request", chatReq.Messages, chatReq.Tools)
		resp, err := cfg.ChatClient.CreateChatCompletion(ctx, chatReq)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				result.Trace.Termination = model.AgentTraceTerminationWallClockExceeded
			} else {
				result.Trace.Termination = model.AgentTraceTerminationError
			}
			result.Status = model.SchedulerRunStatusFailed
			result.Error = sanitize.FreeText(err.Error())
			return result
		}
		result.Trace.LLMTotalTokens += resp.Usage.TotalTokens
		if len(resp.Choices) == 0 {
			result.Trace.Termination = model.AgentTraceTerminationProtocolFailed
			result.Status = model.SchedulerRunStatusFailed
			result.Error = "model returned no choices"
			return result
		}
		assistant := resp.Choices[0].Message
		writeDebug(cfg.DebugWriter, "llm_response", []ChatMessage{assistant}, nil)

		// Free-text reply (no tool_calls) is a protocol violation. Try once
		// more, then give up.
		if len(assistant.ToolCalls) == 0 {
			protocolViolations++
			if protocolViolations >= cfg.MaxProtocolViolations {
				result.Trace.Termination = model.AgentTraceTerminationProtocolFailed
				result.Status = model.SchedulerRunStatusFailed
				result.Error = "model returned free text without invoking submit_result"
				return result
			}
			// Append a reminder; loop.
			messages = append(messages, assistant)
			messages = append(messages, ChatMessage{
				Role:    "system",
				Content: "Reminder: this run requires structured output. Call the submit_result tool with your final answer; do not reply with free text.",
			})
			continue
		}
		protocolViolations = 0

		// Append the assistant's tool_calls message exactly as received so
		// the next request preserves the tool_call_id pairing.
		messages = append(messages, assistant)

		// Dispatch each tool_call. submit_result terminates the loop.
		finalized := false
		var finalCallErr error
		for _, tc := range assistant.ToolCalls {
			if tc.Function.Name == SubmitResultToolName {
				title, summary, payload, err := parseSubmitResult(tc.Function.Arguments)
				if err != nil {
					protocolViolations++
					if protocolViolations >= cfg.MaxProtocolViolations {
						result.Trace.Termination = model.AgentTraceTerminationProtocolFailed
						result.Status = model.SchedulerRunStatusFailed
						result.Error = "submit_result arguments invalid: " + sanitize.FreeText(err.Error())
						return result
					}
					messages = append(messages, ChatMessage{
						Role:       "tool",
						ToolCallID: tc.ID,
						Content:    `{"error":"submit_result arguments invalid: ` + escapeJSON(err.Error()) + `"}`,
					})
					continue
				}
				result.Title = title
				result.Summary = sanitize.FreeText(summary)
				result.Payload = payload
				if forceFinalize {
					result.Status = model.SchedulerRunStatusPartial
				} else {
					result.Status = model.SchedulerRunStatusDone
					result.Trace.Termination = model.AgentTraceTerminationNatural
				}
				finalized = true
				break
			}

			// Vault tool. Validate name in whitelist, args.path in scope, etc.
			if !toolNameAllowed(tc.Function.Name, req.Skill.VaultTools) {
				record := AgentToolCallRecord{
					Seq:         nextSeq(&seq),
					Tool:        tc.Function.Name,
					Args:        json.RawMessage(tc.Function.Arguments),
					Error:       fmt.Sprintf("tool %q is not in the Skill's vault_tools whitelist", tc.Function.Name),
					BudgetAfter: budgetSnapshot(budget, now),
				}
				result.Trace.Calls = append(result.Trace.Calls, record)
				messages = append(messages, ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    `{"error":"tool not allowed for this Skill"}`,
				})
				continue
			}

			callStart := time.Now()
			toolResult, toolErr := cfg.Executor.Execute(ctx, ToolInvocation{
				Name: tc.Function.Name,
				Args: json.RawMessage(tc.Function.Arguments),
			})
			duration := time.Since(callStart)

			// Charge tool_calls budget regardless of outcome (prevents
			// pathological retry loops on a bad input).
			budget.MaxToolCalls -= 1
			budget.MaxTotalBytes -= toolResult.Bytes
			snapshot := budgetSnapshot(budget, now)

			record := AgentToolCallRecord{
				Seq:                 nextSeq(&seq),
				Tool:                tc.Function.Name,
				Args:                sanitizedArgsForTrace(tc.Function.Arguments),
				ResultBytes:         toolResult.Bytes,
				ResultSummarySHA256: toolResult.ResultSHA256,
				ResultSummary:       toolResult.SafeSummary,
				DurationMs:          duration.Milliseconds(),
				Truncated:           toolResult.Truncated,
				BudgetAfter:         snapshot,
			}
			if toolErr != nil {
				record.Error = sanitize.FreeText(toolErr.Error())
				result.Trace.Calls = append(result.Trace.Calls, record)
				messages = append(messages, ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    `{"error":"` + escapeJSON(toolErr.Error()) + `"}`,
				})
				continue
			}
			result.Trace.Calls = append(result.Trace.Calls, record)
			messages = append(messages, ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    toolResult.Content,
			})

			// Budget gate after the call.
			if budget.MaxToolCalls <= 0 || budget.MaxTotalBytes <= 0 {
				forceFinalize = true
				if budget.MaxToolCalls <= 0 {
					result.Trace.Termination = model.AgentTraceTerminationCallCountExceeded
				} else {
					result.Trace.Termination = model.AgentTraceTerminationBytesExceeded
				}
			}
		}
		_ = finalCallErr
		if finalized {
			return result
		}
		// Wall clock check between turns. Budget remaining seconds is computed
		// fresh each loop.
		if remaining := time.Until(deadlineFromContext(ctx)); remaining <= 0 {
			result.Trace.Termination = model.AgentTraceTerminationWallClockExceeded
			result.Status = model.SchedulerRunStatusFailed
			result.Error = "wall clock exceeded between turns"
			return result
		}
	}
}

// ---- helpers ----

func buildInitialMessages(req AgentRunRequest) []ChatMessage {
	skill := req.Skill
	var systemBody strings.Builder
	fmt.Fprintf(&systemBody, "You are running as vault Skill \"%s\" inside OpenWhisker Agent Runtime.\n", skill.ID)
	fmt.Fprintf(&systemBody, "Trigger kind: %s\n", req.TriggerKind)
	fmt.Fprintf(&systemBody, "Skill definition (verbatim from SKILL.md body):\n\n%s\n\n", strings.TrimSpace(skill.Body))
	fmt.Fprintf(&systemBody, "Vault scope (the only paths you may access via tools):\n")
	for _, root := range skill.VaultScope {
		fmt.Fprintf(&systemBody, "- %s\n", root)
	}
	fmt.Fprintf(&systemBody, "\nAvailable tools: %s.\n", strings.Join(skill.VaultTools, ", "))
	fmt.Fprintf(&systemBody, "\nTo finalize, call the submit_result tool with title/summary/payload. Do not produce a final natural-language message; the only way to terminate is submit_result.\n")
	// Note: the [budget] line is appended/refreshed by setBudgetSystemNote on
	// every turn, so it is intentionally NOT included here.

	messages := []ChatMessage{
		{Role: "system", Content: systemBody.String()},
	}

	if req.Query == "" {
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: "Now produce the scheduled output as defined by the skill.",
		})
	} else {
		messages = append(messages, ChatMessage{
			Role:    "user",
			Content: req.Query,
		})
	}

	// Phase 5 backwards-compat: include pre-fetched external info as a system
	// note. The LLM has no fetch_external_feed tool, so this is the only
	// channel for RSS/etc. content.
	if len(req.ExternalInfo) > 0 {
		var ext strings.Builder
		for _, item := range req.ExternalInfo {
			fmt.Fprintf(&ext, "## External info: %s\n", item.Source)
			if item.Summary != "" {
				fmt.Fprintf(&ext, "Summary: %s\n", item.Summary)
			}
			if len(item.Payload) > 0 {
				fmt.Fprintf(&ext, "Payload JSON: %s\n", string(item.Payload))
			}
			ext.WriteString("\n")
		}
		messages = append(messages, ChatMessage{
			Role:    "system",
			Content: "Pre-fetched external information (read-only context, NOT a tool result):\n\n" + ext.String(),
		})
	}
	return messages
}

func setBudgetSystemNote(messages []ChatMessage, snap AgentBudgetSnapshot, force bool) []ChatMessage {
	// The note lives at the tail of the message list so the most recent
	// budget always appears in the model's effective context. We strip any
	// previous [budget] note before appending the fresh one.
	out := make([]ChatMessage, 0, len(messages)+1)
	for _, m := range messages {
		if m.Role == "system" && strings.HasPrefix(m.Content, "[budget]") {
			continue
		}
		out = append(out, m)
	}
	note := fmt.Sprintf("[budget] tool_calls_left=%d, bytes_left=%d, wall_clock_left=%ds",
		snap.ToolCallsLeft, snap.BytesLeft, snap.WallClockLeft)
	if force {
		note += "\nYou must call submit_result now with what you have."
	}
	out = append(out, ChatMessage{Role: "system", Content: note})
	return out
}

func budgetSnapshot(b scheduler.ToolBudget, runStart time.Time) AgentBudgetSnapshot {
	wc := b.MaxWallClockSeconds - int(time.Since(runStart).Seconds())
	if wc < 0 {
		wc = 0
	}
	return AgentBudgetSnapshot{
		ToolCallsLeft: maxInt(b.MaxToolCalls, 0),
		BytesLeft:     maxInt(b.MaxTotalBytes, 0),
		WallClockLeft: wc,
	}
}

func parseSubmitResult(rawArgs string) (title, summary string, payload json.RawMessage, err error) {
	if strings.TrimSpace(rawArgs) == "" {
		err = errors.New("submit_result arguments are empty")
		return
	}
	var parsed struct {
		Title   string          `json:"title"`
		Summary string          `json:"summary"`
		Payload json.RawMessage `json:"payload"`
	}
	if jsonErr := json.Unmarshal([]byte(rawArgs), &parsed); jsonErr != nil {
		err = fmt.Errorf("submit_result arguments not valid JSON: %w", jsonErr)
		return
	}
	if strings.TrimSpace(parsed.Title) == "" {
		err = errors.New("submit_result requires non-empty title")
		return
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		err = errors.New("submit_result requires non-empty summary")
		return
	}
	if len(parsed.Payload) == 0 {
		parsed.Payload = json.RawMessage(`{}`)
	}
	return parsed.Title, parsed.Summary, parsed.Payload, nil
}

func toolNameAllowed(name string, allowed []string) bool {
	for _, a := range allowed {
		if a == name {
			return true
		}
	}
	return false
}

func sanitizedArgsForTrace(raw string) json.RawMessage {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	// Re-serialize through json.Marshal of the sanitized form so the trace
	// stays valid JSON.
	clean := sanitize.FreeText(raw)
	if json.Valid([]byte(clean)) {
		return json.RawMessage(clean)
	}
	encoded, _ := json.Marshal(clean)
	return encoded
}

func nextSeq(p *int) int {
	*p++
	return *p
}

func deadlineFromContext(ctx context.Context) time.Time {
	d, ok := ctx.Deadline()
	if !ok {
		return time.Now().Add(24 * time.Hour) // effectively unlimited
	}
	return d
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func escapeJSON(s string) string {
	encoded, _ := json.Marshal(s)
	out := string(encoded)
	// Strip surrounding quotes; we're inserting into a larger JSON literal.
	if len(out) >= 2 && out[0] == '"' && out[len(out)-1] == '"' {
		return out[1 : len(out)-1]
	}
	return out
}

func writeDebug(w io.Writer, eventKind string, messages []ChatMessage, tools []ChatTool) {
	if w == nil {
		return
	}
	rec := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"event": eventKind,
	}
	if messages != nil {
		rec["messages"] = messages
	}
	if tools != nil {
		rec["tools"] = tools
	}
	encoded, _ := json.Marshal(rec)
	_, _ = w.Write(encoded)
	_, _ = w.Write([]byte("\n"))
}
