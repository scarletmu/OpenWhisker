package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type SchedulerService struct {
	store        *storage.Store
	vaultRoot    string
	vaultProfile profile.VaultProfile
	runner       scheduler.SkillRunner
	now          func() time.Time
}

type SchedulerServiceOptions struct {
	VaultProfile profile.VaultProfile
	Runner       scheduler.SkillRunner
	Now          func() time.Time
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

func NewSchedulerService(store *storage.Store, vaultRoot string, vaultProfile profile.VaultProfile) SchedulerService {
	return NewSchedulerServiceWithOptions(store, vaultRoot, SchedulerServiceOptions{VaultProfile: vaultProfile})
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
		store:        store,
		vaultRoot:    vaultRoot,
		vaultProfile: opts.VaultProfile,
		runner:       runner,
		now:          now,
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
		ID:         model.NewID("schedrun"),
		ScheduleID: schedule.ID,
		RuntimeID:  runtime.ID,
		SkillDir:   schedule.SkillDir,
		SkillPath:  schedule.SkillPath,
		Status:     model.SchedulerRunStatusRunning,
		StartedAt:  now,
	}
	if err := s.store.CreateSchedulerRun(run); err != nil {
		_ = s.store.UpdateJobStatus(job.ID, model.JobStatusFailed, "", err.Error())
		return SchedulerTickRunResult{}, err
	}

	skillResult, runErr := s.runner.Run(ctx, scheduler.SkillRunRequest{Schedule: schedule, Now: now})
	finishedAt := s.now().UTC()
	status := model.SchedulerRunStatusDone
	outboxKind := model.OutboxKindResult
	resultJSON := ""
	errText := ""
	body := ""
	if runErr != nil {
		status = model.SchedulerRunStatusFailed
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
		_ = s.store.FinishSchedulerRun(run.ID, model.SchedulerRunStatusFailed, resultJSON, err.Error(), "", finishedAt)
		_ = s.store.UpdateJobStatus(job.ID, model.JobStatusFailed, resultJSON, err.Error())
		return SchedulerTickRunResult{}, err
	}
	if err := s.store.FinishSchedulerRun(run.ID, status, resultJSON, errText, outbox.ID, finishedAt); err != nil {
		_ = s.store.UpdateJobStatus(job.ID, model.JobStatusFailed, resultJSON, err.Error())
		return SchedulerTickRunResult{}, err
	}
	jobStatus := model.JobStatusDone
	if status == model.SchedulerRunStatusFailed {
		jobStatus = model.JobStatusFailed
	}
	if err := s.store.UpdateJobStatus(job.ID, jobStatus, resultJSON, errText); err != nil {
		return SchedulerTickRunResult{}, err
	}
	if err := s.advanceRuntime(runtime, schedule, finishedAt); err != nil {
		return SchedulerTickRunResult{}, err
	}
	return SchedulerTickRunResult{
		RunID:           run.ID,
		ScheduleID:      schedule.ID,
		Status:          status,
		OutboxMessageID: outbox.ID,
		Error:           errText,
	}, nil
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
