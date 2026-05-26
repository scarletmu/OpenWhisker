// AgentRunner orchestrates one Phase 6 agent run end-to-end:
//
//   - Pre-fetches external info sources (Phase 5 compat) and stitches the
//     initial prompt + tool catalog.
//   - Hands the request to ToolCallingEngine for the multi-turn loop.
//   - Captures the resulting AgentTrace and AgentRunResult.
//   - Optionally streams the raw LLM messages to a debug writer (ndjson).
//
// Hosts (Scheduler / Matrix / CLI) construct an AgentRunner per process and
// dispatch every run through it. The result is host-agnostic; the caller
// formats it for its native output channel.
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
	"github.com/scarletmu/openwhisker/internal/vault/linkindex"
)

// AgentRunRequest is the host-agnostic input. Query is "" when scheduler tick
// is the trigger; the loop substitutes a stock instruction in that case.
type AgentRunRequest struct {
	Skill        scheduler.ScheduledSkill
	Query        string
	TriggerKind  string // model.AgentTriggerKindScheduler / AdhocMatrix / AdhocCLI
	Now          time.Time
	ExternalInfo []scheduler.ExternalInfoItem
	LinkIndex    *linkindex.Index
	DebugWriter  io.Writer // ndjson of full message stream, optional
}

// AgentRunResult is what AgentRunner returns. Status maps onto the standard
// model.SchedulerRunStatus* values: ok/done, partial, failed.
type AgentRunResult struct {
	SkillID string
	Title   string
	Summary string
	Payload json.RawMessage
	Status  string // model.SchedulerRunStatusDone | Partial | Failed
	Error   string
	Trace   AgentTrace
}

// AgentTrace is persisted as agent_runs.tool_trace_json (sanitized).
type AgentTrace struct {
	Termination      string                `json:"termination"`
	LinkIndexVersion int64                 `json:"link_index_version,omitempty"`
	Calls            []AgentToolCallRecord `json:"calls,omitempty"`
	LLMTotalTokens   int                   `json:"llm_total_tokens,omitempty"`
}

// AgentToolCallRecord is one row of the tool call timeline.
type AgentToolCallRecord struct {
	Seq                 int                 `json:"seq"`
	Tool                string              `json:"tool"`
	Args                json.RawMessage     `json:"args,omitempty"`
	ResultBytes         int                 `json:"result_bytes"`
	ResultSummarySHA256 string              `json:"result_summary_sha256,omitempty"`
	ResultSummary       string              `json:"result_summary,omitempty"`
	DurationMs          int64               `json:"duration_ms"`
	Truncated           bool                `json:"truncated,omitempty"`
	Error               string              `json:"error,omitempty"`
	BudgetAfter         AgentBudgetSnapshot `json:"budget_after"`
}

// AgentBudgetSnapshot captures remaining budget after a tool call. The host
// also uses it for the [budget] system-message line on the next turn.
type AgentBudgetSnapshot struct {
	ToolCallsLeft int `json:"tool_calls_left"`
	BytesLeft     int `json:"bytes_left"`
	WallClockLeft int `json:"wall_clock_left"`
}

// AgentRunner is the entry point hosts call. ChatClient supplies the LLM
// transport (defaults to OpenAIClient in production; tests inject fakes).
type AgentRunner struct {
	VaultRoot   string
	ChatClient  ChatCompletionClient
	ChatModel   string // empty → client default (OpenAIClient.Model)
	Engine      ToolCallingEngine
	Adapters    map[string]scheduler.ExternalInfoAdapter
	// MaxToolCallRetriesOnProtocolError caps the number of consecutive
	// "free-text reply with no tool_call" turns the engine tolerates before
	// giving up (default 2 per spec).
	MaxToolCallRetriesOnProtocolError int
}

// Run executes one full agent loop. The returned AgentRunResult always
// reflects best-effort state — even on failure the Trace is populated so the
// caller can persist it.
func (r AgentRunner) Run(ctx context.Context, req AgentRunRequest) (AgentRunResult, error) {
	if strings.TrimSpace(r.VaultRoot) == "" {
		return AgentRunResult{}, errors.New("AgentRunner: VaultRoot is required")
	}
	if r.ChatClient == nil {
		return AgentRunResult{}, errors.New("AgentRunner: ChatClient is required")
	}
	if !req.Skill.HasToolCalling() {
		return AgentRunResult{}, errors.New("AgentRunner: Skill is not tool-calling")
	}
	if req.TriggerKind == "" {
		req.TriggerKind = model.AgentTriggerKindScheduler
	}
	if req.Now.IsZero() {
		req.Now = time.Now().UTC()
	}
	if r.MaxToolCallRetriesOnProtocolError == 0 {
		r.MaxToolCallRetriesOnProtocolError = 2
	}

	// Pre-fetch external info sources (Phase 5 behavior). Fail soft — surface
	// the error in the trace but let the engine still attempt the run.
	externalInfo := req.ExternalInfo
	if len(externalInfo) == 0 && len(req.Skill.ExternalSources) > 0 {
		var fetchErr error
		externalInfo, fetchErr = readExternalInfoForRun(ctx, r.Adapters, req.Skill, req.Now)
		if fetchErr != nil {
			return AgentRunResult{
				SkillID: req.Skill.ID,
				Status:  model.SchedulerRunStatusFailed,
				Error:   sanitize.FreeText(fetchErr.Error()),
				Trace:   AgentTrace{Termination: model.AgentTraceTerminationError},
			}, nil
		}
	}
	req.ExternalInfo = externalInfo

	tools := append([]ChatTool{}, VaultToolDescriptors(req.Skill.VaultTools)...)
	tools = append(tools, SubmitResultToolDescriptor())

	executor := VaultToolExecutor{
		VaultRoot: r.VaultRoot,
		Skill:     req.Skill,
		LinkIndex: req.LinkIndex,
	}

	engineCfg := EngineConfig{
		ChatClient:                       r.ChatClient,
		Model:                            r.ChatModel,
		ToolCatalog:                      tools,
		Executor:                         executor,
		MaxProtocolViolations:            r.MaxToolCallRetriesOnProtocolError,
		DebugWriter:                      req.DebugWriter,
	}

	deadline := req.Now.Add(time.Duration(req.Skill.Budget.MaxWallClockSeconds) * time.Second)
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	result := r.Engine.Run(runCtx, engineCfg, req)
	result.SkillID = req.Skill.ID
	if result.Trace.LinkIndexVersion == 0 && req.LinkIndex != nil {
		result.Trace.LinkIndexVersion = req.LinkIndex.Generation()
	}
	return result, nil
}

// readExternalInfoForRun mirrors StaticSkillRunner.readExternalInfo so the
// pre-fetch behavior stays identical between the legacy static engine and the
// new ToolCallingEngine. Pulled into a helper so neither runner has to import
// the other.
func readExternalInfoForRun(ctx context.Context, adapters map[string]scheduler.ExternalInfoAdapter, skill scheduler.ScheduledSkill, now time.Time) ([]scheduler.ExternalInfoItem, error) {
	var items []scheduler.ExternalInfoItem
	for _, source := range skill.ExternalSources {
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		adapter, ok := adapters[source]
		if !ok || adapter == nil {
			return nil, fmt.Errorf("scheduler external info adapter %q is not configured", source)
		}
		item, err := adapter.ReadInfo(ctx, scheduler.ExternalInfoRequest{
			Source:   source,
			Schedule: skill,
			Now:      now,
		})
		if err != nil {
			return nil, fmt.Errorf("read external info %q: %w", source, err)
		}
		if item.Source == "" {
			item.Source = source
		}
		if item.FetchedAt.IsZero() {
			item.FetchedAt = now.UTC()
		}
		items = append(items, item)
	}
	return items, nil
}
