// Package agentdispatch is the thin glue that adapts agent.AgentRunner to
// core.AgentDispatcher. It exists in its own package only to break the
// import cycle that would otherwise form between internal/core and
// internal/agent (agent already imports core for RawOrganizer / IntentClassifier
// contracts, so core cannot import agent).
//
// Hosts (cmd/openwhisker, future server entry points) construct a Dispatcher
// and inject it into core.NewSchedulerServiceWithOptions; SchedulerService
// then routes tool-calling skills through the AgentRunner without ever
// touching the agent package directly.
package agentdispatch

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/scarletmu/openwhisker/internal/agent"
	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
	"github.com/scarletmu/openwhisker/internal/vault/linkindex"
)

// Dispatcher implements core.AgentDispatcher by delegating to an AgentRunner.
// Store is required for DispatchAdHoc (creates+finishes its own agent_runs
// row). DispatchScheduler does not use Store — the SchedulerService manages
// that row.
type Dispatcher struct {
	Runner    *agent.AgentRunner
	LinkIndex *linkindex.Index
	Store     *storage.Store
}

// DispatchScheduler is called by the scheduler service when a Skill is
// engine=tool-calling. Returns a marshaled trace alongside the result; the
// scheduler persists it to agent_runs.tool_trace_json.
func (d Dispatcher) DispatchScheduler(ctx context.Context, req core.AgentDispatchRequest) (core.AgentDispatchResult, error) {
	if d.Runner == nil {
		return core.AgentDispatchResult{}, errNoRunner
	}
	result, err := d.Runner.Run(ctx, agent.AgentRunRequest{
		Skill:       req.Skill,
		Query:       "",
		TriggerKind: model.AgentTriggerKindScheduler,
		Now:         time.Now().UTC(),
		LinkIndex:   d.LinkIndex,
	})
	if err != nil {
		return core.AgentDispatchResult{}, err
	}
	out := core.AgentDispatchResult{
		Result: scheduler.SkillRunResult{
			ScheduleID: req.Skill.ID,
			SkillID:    result.SkillID,
			Title:      result.Title,
			Summary:    result.Summary,
			Payload:    result.Payload,
		},
		Status: result.Status,
		Error:  result.Error,
	}
	if trace, err := json.Marshal(result.Trace); err == nil {
		out.TraceJSON = string(trace)
	}
	return out, nil
}

// DispatchAdHoc runs an agent skill on Matrix or CLI input. The dispatcher
// owns the agent_runs row lifecycle here (no surrounding scheduler tick
// frames it). Returns the run id so callers can render `[trace: <run_id>]`
// references inline with the user response.
func (d Dispatcher) DispatchAdHoc(ctx context.Context, req core.AgentAdHocRequest) (core.AgentAdHocResult, error) {
	if d.Runner == nil {
		return core.AgentAdHocResult{}, errNoRunner
	}
	if d.Store == nil {
		return core.AgentAdHocResult{}, errNoStore
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	// Create the agent_runs row up front so it is visible in `openwhisker
	// agent runs list` even mid-run. RuntimeID is left empty for ad-hoc.
	runRow := model.SchedulerRun{
		ID:          model.NewID("agentrun"),
		ScheduleID:  req.Skill.ID,
		RuntimeID:   "",
		SkillDir:    req.Skill.SkillDir,
		SkillPath:   req.Skill.SkillPath,
		Status:      model.SchedulerRunStatusRunning,
		StartedAt:   now,
		TriggerKind: req.TriggerKind,
	}
	if err := d.Store.CreateSchedulerRun(runRow); err != nil {
		return core.AgentAdHocResult{}, err
	}

	result, runErr := d.Runner.Run(ctx, agent.AgentRunRequest{
		Skill:       req.Skill,
		Query:       req.Query,
		TriggerKind: req.TriggerKind,
		Now:         now,
		LinkIndex:   d.LinkIndex,
		DebugWriter: toIOWriter(req.DebugWriter),
	})
	finishedAt := time.Now().UTC()

	out := core.AgentAdHocResult{RunID: runRow.ID}
	if runErr != nil {
		out.Status = model.SchedulerRunStatusFailed
		out.Error = runErr.Error()
		_ = d.Store.FinishAgentRun(runRow.ID, out.Status, "", out.Error, "", "", finishedAt)
		return out, runErr
	}
	if trace, err := json.Marshal(result.Trace); err == nil {
		out.TraceJSON = string(trace)
	}
	out.Status = result.Status
	out.Error = result.Error
	out.Result = scheduler.SkillRunResult{
		ScheduleID: req.Skill.ID,
		SkillID:    result.SkillID,
		Title:      result.Title,
		Summary:    result.Summary,
		Payload:    result.Payload,
	}
	resultJSON, _ := json.Marshal(out.Result)
	_ = d.Store.FinishAgentRun(runRow.ID, out.Status, string(resultJSON), out.Error, "", out.TraceJSON, finishedAt)
	return out, nil
}

// toIOWriter bridges the parameterless interface used in AgentAdHocRequest to
// the io.Writer the agent runner expects. Returns the untyped-nil io.Writer
// when w is nil, so engine code that does `if w == nil { return }` sees the
// interface as nil (a typed-nil wrapper would not compare equal to nil).
func toIOWriter(w interface{ Write(p []byte) (int, error) }) io.Writer {
	if w == nil {
		return nil
	}
	if iw, ok := w.(io.Writer); ok {
		return iw
	}
	return &writerAdapter{inner: w}
}

type writerAdapter struct {
	inner interface {
		Write(p []byte) (int, error)
	}
}

func (a *writerAdapter) Write(p []byte) (int, error) { return a.inner.Write(p) }

// errNoRunner / errNoStore are returned when the Dispatcher was constructed
// without required fields — a configuration bug at the call site.
var (
	errNoRunner = errStr("agentdispatch.Dispatcher: AgentRunner is nil")
	errNoStore  = errStr("agentdispatch.Dispatcher: Store is nil (required for DispatchAdHoc)")
)

type errStr string

func (e errStr) Error() string { return string(e) }
