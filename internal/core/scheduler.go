package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// AgentDispatcher is the Phase 6 hook for routing tool-calling skill runs to
// the AgentRunner. Defined as an interface here to keep internal/core free of
// internal/agent imports (agent depends on core for some types).
type AgentDispatcher interface {
	// DispatchScheduler runs an agent skill in scheduler context. Used by
	// SchedulerService.runSchedule when the Skill has engine=tool-calling.
	// The caller (SchedulerService) owns the agent_runs row lifecycle.
	DispatchScheduler(ctx context.Context, req AgentDispatchRequest) (AgentDispatchResult, error)

	// DispatchAdHoc runs an agent skill in ad-hoc context (Matrix @-mention
	// or CLI `openwhisker ask`). Unlike DispatchScheduler, the dispatcher
	// owns the agent_runs row lifecycle here — there is no surrounding
	// scheduler tick to wrap it. Returns the run id so callers can render
	// `[trace: <run_id>]` references.
	DispatchAdHoc(ctx context.Context, req AgentAdHocRequest) (AgentAdHocResult, error)
}

// AgentDispatchRequest is the host-agnostic scheduler input to AgentDispatcher.
type AgentDispatchRequest struct {
	Skill scheduler.ScheduledSkill
	Now   time.Time
}

// AgentDispatchResult is what AgentDispatcher returns to the SchedulerService.
type AgentDispatchResult struct {
	Result    scheduler.SkillRunResult
	Status    string // model.SchedulerRunStatusDone | Partial | Failed
	Error     string
	TraceJSON string
}

// AgentAdHocRequest is the ad-hoc trigger input (Matrix bot or CLI ask).
// DebugWriter, if non-nil, gets the full LLM message stream as ndjson.
type AgentAdHocRequest struct {
	Skill       scheduler.ScheduledSkill
	Query       string
	TriggerKind string // model.AgentTriggerKindAdhocMatrix | AdhocCLI
	Now         time.Time
	DebugWriter interface{ Write(p []byte) (int, error) }
}

// AgentAdHocResult includes the run id so callers can show audit pointers.
type AgentAdHocResult struct {
	RunID     string
	Result    scheduler.SkillRunResult
	Status    string
	Error     string
	TraceJSON string
}

// SkillLookup is the registry-hit shape AdapterService needs to dispatch an
// @-mentioned ad-hoc query. Implementations typically wrap scheduler.LoadRegistry.
type SkillLookup interface {
	Find(skillID string) (scheduler.ScheduledSkill, error)
}

type SchedulerService struct {
	store        *storage.Store
	vaultRoot    string
	vaultProfile profile.VaultProfile
	runner       scheduler.SkillRunner
	now          func() time.Time

	// Phase 6: optional AgentDispatcher used when a schedule's Skill has
	// engine=tool-calling. nil = tool-calling skills run via the legacy
	// runner (which will produce a non-tool-calling fallback result).
	agentDispatcher AgentDispatcher
}

type SchedulerServiceOptions struct {
	VaultProfile    profile.VaultProfile
	Runner          scheduler.SkillRunner
	Now             func() time.Time
	AgentDispatcher AgentDispatcher
}

type SchedulerTickResult struct {
	ProfileID  string                   `json:"profile_id"`
	Enabled    bool                     `json:"enabled"`
	Discovered int                      `json:"discovered"`
	Due        int                      `json:"due"`
	Started    int                      `json:"started"`
	Skipped    int                      `json:"skipped"`
	Failed     int                      `json:"failed"`
	Completed  int                      `json:"completed"`
	Runs       []SchedulerTickRunResult `json:"runs,omitempty"`
}

type SchedulerTickRunResult struct {
	RunID           string `json:"run_id"`
	ScheduleID      string `json:"schedule_id"`
	Status          string `json:"status"`
	OutboxMessageID string `json:"outbox_message_id,omitempty"`
	Error           string `json:"error,omitempty"`
}

func NewSchedulerServiceWithOptions(store *storage.Store, vaultRoot string, opts SchedulerServiceOptions) SchedulerService {
	runner := opts.Runner
	if runner == nil {
		runner = scheduler.StaticSkillRunner{VaultRoot: vaultRoot}
	}
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return SchedulerService{
		store:           store,
		vaultRoot:       vaultRoot,
		vaultProfile:    opts.VaultProfile,
		runner:          runner,
		now:             now,
		agentDispatcher: opts.AgentDispatcher,
	}
}

func (s SchedulerService) Tick(ctx context.Context) (SchedulerTickResult, error) {
	result := SchedulerTickResult{
		ProfileID: s.vaultProfile.ID,
		Enabled:   s.vaultProfile.Scheduler.Enabled,
	}
	if !s.vaultProfile.Scheduler.Enabled {
		return result, nil
	}
	now := s.now().UTC()
	schedules, err := scheduler.LoadRegistry(s.vaultRoot, s.vaultProfile)
	if err != nil {
		return result, err
	}
	schedules, err = schedulerSchedulesWithOverrides(s.store, schedules)
	if err != nil {
		return result, err
	}
	result.Discovered = len(schedules)
	for _, schedule := range schedules {
		// Ad-hoc-only Skills (SKILL.md without a sibling SCHEDULE.md) appear
		// in the registry so the @-mention path can resolve them, but they
		// have no cron expression and must not be reconciled by the tick.
		if !schedule.HasSchedule {
			continue
		}
		runtime, due, err := s.reconcileSchedule(schedule, now)
		if err != nil {
			return result, err
		}
		if !schedule.Enabled || !due {
			continue
		}
		result.Due++
		running, err := s.store.HasRunningSchedulerRun(schedule.ID)
		if err != nil {
			return result, err
		}
		if running {
			runResult, err := s.skipSchedule(schedule, runtime, now)
			if err != nil {
				return result, err
			}
			result.Skipped++
			result.Runs = append(result.Runs, runResult)
			continue
		}
		runResult, err := s.runSchedule(ctx, schedule, runtime, now)
		if err != nil {
			return result, err
		}
		result.Started++
		switch runResult.Status {
		case model.SchedulerRunStatusDone:
			result.Completed++
		case model.SchedulerRunStatusFailed:
			result.Failed++
		}
		result.Runs = append(result.Runs, runResult)
	}
	return result, nil
}

func (s SchedulerService) reconcileSchedule(schedule scheduler.ScheduledSkill, now time.Time) (model.SchedulerRuntime, bool, error) {
	cronSchedule, err := scheduler.ParseCron(schedule.CronExpr)
	if err != nil {
		return model.SchedulerRuntime{}, false, err
	}
	loc, err := schedulerLocation(schedule.Timezone)
	if err != nil {
		return model.SchedulerRuntime{}, false, err
	}
	nextAfterNow, err := cronSchedule.NextAfter(now, loc)
	if err != nil {
		return model.SchedulerRuntime{}, false, err
	}
	runtime, err := s.store.GetSchedulerRuntime(schedule.ID)
	if err != nil {
		if err != sql.ErrNoRows {
			return model.SchedulerRuntime{}, false, err
		}
		nextRunAt := nextAfterNow
		if currentWindow, ok := cronSchedule.CurrentWindow(now, loc); ok {
			nextRunAt = currentWindow
		}
		runtime = model.SchedulerRuntime{
			ID:           model.NewID("schedrt"),
			ScheduleID:   schedule.ID,
			RegistryPath: schedule.RegistryPath,
			RegistryHash: schedule.RegistryHash,
			SkillDir:     schedule.SkillDir,
			SkillPath:    schedule.SkillPath,
			NextRunAt:    &nextRunAt,
			UpdatedAt:    now,
		}
		if err := s.store.SaveSchedulerRuntime(runtime); err != nil {
			return model.SchedulerRuntime{}, false, err
		}
		return runtime, schedule.Enabled && !nextRunAt.After(now), nil
	}
	if runtime.RegistryHash != schedule.RegistryHash || runtime.RegistryPath != schedule.RegistryPath ||
		runtime.SkillDir != schedule.SkillDir || runtime.SkillPath != schedule.SkillPath {
		runtime.RegistryPath = schedule.RegistryPath
		runtime.RegistryHash = schedule.RegistryHash
		runtime.SkillDir = schedule.SkillDir
		runtime.SkillPath = schedule.SkillPath
		runtime.NextRunAt = &nextAfterNow
	}
	if runtime.NextRunAt == nil {
		runtime.NextRunAt = &nextAfterNow
	}
	runtime.UpdatedAt = now
	if err := s.store.SaveSchedulerRuntime(runtime); err != nil {
		return model.SchedulerRuntime{}, false, err
	}
	return runtime, runtime.NextRunAt != nil && !runtime.NextRunAt.After(now), nil
}

func (s SchedulerService) skipSchedule(schedule scheduler.ScheduledSkill, runtime model.SchedulerRuntime, now time.Time) (SchedulerTickRunResult, error) {
	run := model.SchedulerRun{
		ID:         model.NewID("schedrun"),
		ScheduleID: schedule.ID,
		RuntimeID:  runtime.ID,
		SkillDir:   schedule.SkillDir,
		SkillPath:  schedule.SkillPath,
		Status:     model.SchedulerRunStatusSkipped,
		StartedAt:  now,
		Error:      "previous scheduler run is still running",
	}
	finishedAt := now
	run.FinishedAt = &finishedAt
	if err := s.store.CreateSchedulerRun(run); err != nil {
		return SchedulerTickRunResult{}, err
	}
	if err := s.advanceRuntime(runtime, schedule, now); err != nil {
		return SchedulerTickRunResult{}, err
	}
	return SchedulerTickRunResult{
		RunID:      run.ID,
		ScheduleID: schedule.ID,
		Status:     model.SchedulerRunStatusSkipped,
		Error:      run.Error,
	}, nil
}

func (s SchedulerService) runSchedule(ctx context.Context, schedule scheduler.ScheduledSkill, runtime model.SchedulerRuntime, now time.Time) (SchedulerTickRunResult, error) {
	job := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeSchedulerRun,
		Status:    model.JobStatusApplying,
		Source:    "scheduler",
		SourceKey: schedule.ID,
		InputJSON: schedulerJobInputJSON(schedule),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateJob(job); err != nil {
		return SchedulerTickRunResult{}, err
	}
	run := model.SchedulerRun{
		ID:          model.NewID("schedrun"),
		ScheduleID:  schedule.ID,
		RuntimeID:   runtime.ID,
		SkillDir:    schedule.SkillDir,
		SkillPath:   schedule.SkillPath,
		Status:      model.SchedulerRunStatusRunning,
		StartedAt:   now,
		TriggerKind: model.AgentTriggerKindScheduler,
	}
	if err := s.store.CreateSchedulerRun(run); err != nil {
		_ = s.store.UpdateJobStatus(job.ID, model.JobStatusFailed, "", err.Error())
		return SchedulerTickRunResult{}, err
	}

	skillResult, runErr, agentStatus, traceJSON := s.dispatchSkill(ctx, schedule, now)
	// If ctx was cancelled (SIGTERM mid-tick, daemon shutdown) prefer the
	// cancellation reason over any partial result the runner produced. A
	// StaticSkillEngine ignores ctx and would otherwise let us write a
	// "done" outbox row for work the operator killed. The cancelled run is
	// recorded so the next start can decide whether to retry.
	if ctxErr := ctx.Err(); ctxErr != nil && runErr == nil {
		runErr = ctxErr
	}
	finishedAt := s.now().UTC()
	status := model.SchedulerRunStatusDone
	outboxKind := model.OutboxKindResult
	resultJSON := ""
	errText := ""
	body := ""
	cancelled := false
	if runErr != nil {
		cancelled = errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)
		if cancelled {
			status = model.SchedulerRunStatusCancelled
		} else {
			status = model.SchedulerRunStatusFailed
		}
		outboxKind = model.OutboxKindError
		errText = safeSchedulerError(runErr)
		body = schedulerErrorOutboxBody(schedule, errText)
	} else {
		encoded, err := json.Marshal(skillResult)
		if err != nil {
			status = model.SchedulerRunStatusFailed
			outboxKind = model.OutboxKindError
			errText = safeSchedulerError(err)
			body = schedulerErrorOutboxBody(schedule, errText)
		} else {
			resultJSON = string(encoded)
			body = schedulerResultOutboxBody(schedule, run.ID, skillResult)
			// Phase 6: tool-calling runs may complete naturally but with
			// status=partial (budget forced finalize). The agent dispatcher
			// surfaces that here; legacy path leaves agentStatus == "".
			if agentStatus != "" {
				status = agentStatus
			}
		}
	}
	outbox := model.OutboxMessage{
		ID:        model.NewID("out"),
		JobID:     job.ID,
		Actor:     model.OutboxActorScheduler,
		Kind:      outboxKind,
		Body:      body,
		Status:    model.OutboxStatusPending,
		CreatedAt: finishedAt,
	}
	if err := s.store.AddOutboxMessage(outbox); err != nil {
		_ = s.store.FinishAgentRun(run.ID, model.SchedulerRunStatusFailed, resultJSON, err.Error(), "", traceJSON, finishedAt)
		_ = s.store.UpdateJobStatus(job.ID, model.JobStatusFailed, resultJSON, err.Error())
		return SchedulerTickRunResult{}, err
	}
	if err := s.store.FinishAgentRun(run.ID, status, resultJSON, errText, outbox.ID, traceJSON, finishedAt); err != nil {
		_ = s.store.UpdateJobStatus(job.ID, model.JobStatusFailed, resultJSON, err.Error())
		return SchedulerTickRunResult{}, err
	}
	jobStatus := model.JobStatusDone
	switch status {
	case model.SchedulerRunStatusFailed, model.SchedulerRunStatusCancelled:
		jobStatus = model.JobStatusFailed
	}
	if err := s.store.UpdateJobStatus(job.ID, jobStatus, resultJSON, errText); err != nil {
		return SchedulerTickRunResult{}, err
	}
	// Don't advance NextRunAt for cancelled runs — the schedule should fire
	// again at its normal cadence; treating cancellation as "completed" would
	// skip the window we just aborted.
	if !cancelled {
		if err := s.advanceRuntime(runtime, schedule, finishedAt); err != nil {
			return SchedulerTickRunResult{}, err
		}
	}
	return SchedulerTickRunResult{
		RunID:           run.ID,
		ScheduleID:      schedule.ID,
		Status:          status,
		OutboxMessageID: outbox.ID,
		Error:           errText,
	}, nil
}

// dispatchSkill routes the run to either the legacy SkillRunner (Phase 5
// single-turn) or the Phase 6 AgentRunner via the injected AgentDispatcher.
// The returned agentStatus is non-empty only when the agent path is taken;
// the legacy path implies the caller's default status logic still applies.
func (s SchedulerService) dispatchSkill(ctx context.Context, schedule scheduler.ScheduledSkill, now time.Time) (skillResult scheduler.SkillRunResult, runErr error, agentStatus string, traceJSON string) {
	if schedule.HasToolCalling() && s.agentDispatcher != nil {
		dispatched, err := s.agentDispatcher.DispatchScheduler(ctx, AgentDispatchRequest{
			Skill: schedule,
			Now:   now,
		})
		if err != nil {
			return scheduler.SkillRunResult{}, err, "", ""
		}
		traceJSON = dispatched.TraceJSON
		if dispatched.Status == model.SchedulerRunStatusFailed {
			return scheduler.SkillRunResult{}, fmt.Errorf("agent run failed: %s", dispatched.Error), "", traceJSON
		}
		return dispatched.Result, nil, dispatched.Status, traceJSON
	}
	// Legacy: SchedulerService.runner (typically StaticSkillRunner).
	result, runErr := s.runner.Run(ctx, scheduler.SkillRunRequest{Schedule: schedule, Now: now})
	return result, runErr, "", ""
}

func (s SchedulerService) advanceRuntime(runtime model.SchedulerRuntime, schedule scheduler.ScheduledSkill, now time.Time) error {
	cronSchedule, err := scheduler.ParseCron(schedule.CronExpr)
	if err != nil {
		return err
	}
	loc, err := schedulerLocation(schedule.Timezone)
	if err != nil {
		return err
	}
	nextRunAt, err := cronSchedule.NextAfter(now, loc)
	if err != nil {
		return err
	}
	lastRunAt := now
	runtime.LastRunAt = &lastRunAt
	runtime.NextRunAt = &nextRunAt
	runtime.UpdatedAt = now
	return s.store.SaveSchedulerRuntime(runtime)
}

func schedulerLocation(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" || strings.EqualFold(strings.TrimSpace(name), "utc") {
		return time.UTC, nil
	}
	return time.LoadLocation(name)
}

func schedulerJobInputJSON(schedule scheduler.ScheduledSkill) string {
	encoded, err := json.Marshal(map[string]string{
		"schedule_id":   schedule.ID,
		"registry_path": schedule.RegistryPath,
		"skill_path":    schedule.SkillPath,
	})
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func schedulerResultOutboxBody(schedule scheduler.ScheduledSkill, runID string, result scheduler.SkillRunResult) string {
	title := strings.TrimSpace(result.Title)
	if title == "" {
		title = schedule.Name
	}
	summary := strings.TrimSpace(result.Summary)
	if summary == "" {
		summary = "Scheduler run completed."
	}
	body := fmt.Sprintf("%s\n\n%s\n\nrun: %s\nschedule: %s\nskill: %s", title, summary, runID, schedule.ID, schedule.SkillPath)
	if count := suggestedRawCaptureCount(result.Payload); count > 0 {
		body += fmt.Sprintf("\n\nSuggested raw captures: %d\nConfirm with: /scheduler accept %s 1", count, runID)
	}
	return body
}

func schedulerErrorOutboxBody(schedule scheduler.ScheduledSkill, errText string) string {
	return fmt.Sprintf("Scheduler run failed: %s\n\nschedule: %s\nskill: %s", errText, schedule.ID, schedule.SkillPath)
}

func safeSchedulerError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func suggestedRawCaptureCount(payload json.RawMessage) int {
	if len(payload) == 0 {
		return 0
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(payload, &values); err != nil {
		return 0
	}
	var items []json.RawMessage
	if err := json.Unmarshal(values["suggested_raw_captures"], &items); err != nil {
		return 0
	}
	return len(items)
}
