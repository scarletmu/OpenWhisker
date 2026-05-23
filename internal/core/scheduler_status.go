package core

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type SchedulerStatusService struct {
	store        *storage.Store
	vaultRoot    string
	vaultProfile profile.VaultProfile
}

type SchedulerStatus struct {
	ProfileID  string                   `json:"profile_id"`
	Enabled    bool                     `json:"enabled"`
	Discovered int                      `json:"discovered"`
	Schedules  []SchedulerScheduleView  `json:"schedules,omitempty"`
	Runtimes   []model.SchedulerRuntime `json:"runtimes,omitempty"`
	RecentRuns []model.SchedulerRun     `json:"recent_runs,omitempty"`
}

type SchedulerScheduleView struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	Enabled       bool       `json:"enabled"`
	EnabledInFile bool       `json:"enabled_in_file"`
	Override      *bool      `json:"override,omitempty"`
	CronExpr      string     `json:"cron_expr"`
	Timezone      string     `json:"timezone"`
	RegistryPath  string     `json:"registry_path"`
	SkillDir      string     `json:"skill_dir"`
	SkillPath     string     `json:"skill_path"`
	LastRunAt     *time.Time `json:"last_run_at,omitempty"`
	NextRunAt     *time.Time `json:"next_run_at,omitempty"`
}

type SchedulerScheduleUpdateResult struct {
	ScheduleID string `json:"schedule_id"`
	Enabled    bool   `json:"enabled"`
}

func NewSchedulerStatusService(store *storage.Store, vaultRoot string, vaultProfile profile.VaultProfile) SchedulerStatusService {
	return SchedulerStatusService{
		store:        store,
		vaultRoot:    vaultRoot,
		vaultProfile: vaultProfile,
	}
}

func (s SchedulerStatusService) Status(limit int) (SchedulerStatus, error) {
	status := SchedulerStatus{
		ProfileID: s.vaultProfile.ID,
		Enabled:   s.vaultProfile.Scheduler.Enabled,
	}
	if s.vaultProfile.Scheduler.Enabled {
		schedules, err := scheduler.LoadRegistry(s.vaultRoot, s.vaultProfile)
		if err != nil {
			return SchedulerStatus{}, err
		}
		status.Discovered = len(schedules)
		status.Schedules, err = s.scheduleViews(schedules)
		if err != nil {
			return SchedulerStatus{}, err
		}
	}
	runtimes, err := s.store.ListSchedulerRuntimes(limit)
	if err != nil {
		return SchedulerStatus{}, err
	}
	runs, err := s.store.ListRecentSchedulerRuns(limit)
	if err != nil {
		return SchedulerStatus{}, err
	}
	status.Runtimes = runtimes
	status.RecentRuns = runs
	return status, nil
}

func (s SchedulerStatusService) Runs(limit int) ([]model.SchedulerRun, error) {
	return s.store.ListRecentSchedulerRuns(limit)
}

func (s SchedulerStatusService) Schedules() ([]SchedulerScheduleView, error) {
	schedules, err := scheduler.LoadRegistry(s.vaultRoot, s.vaultProfile)
	if err != nil {
		return nil, err
	}
	return s.scheduleViews(schedules)
}

func (s SchedulerStatusService) SetScheduleEnabled(scheduleID string, enabled bool) (SchedulerScheduleUpdateResult, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return SchedulerScheduleUpdateResult{}, fmt.Errorf("schedule id is required")
	}
	schedules, err := scheduler.LoadRegistry(s.vaultRoot, s.vaultProfile)
	if err != nil {
		return SchedulerScheduleUpdateResult{}, err
	}
	found := false
	for _, schedule := range schedules {
		if schedule.ID == scheduleID {
			found = true
			break
		}
	}
	if !found {
		return SchedulerScheduleUpdateResult{}, fmt.Errorf("scheduler schedule %q not found", scheduleID)
	}
	if err := s.store.SaveSchedulerEnabledOverride(scheduleID, enabled, time.Now().UTC()); err != nil {
		return SchedulerScheduleUpdateResult{}, err
	}
	return SchedulerScheduleUpdateResult{ScheduleID: scheduleID, Enabled: enabled}, nil
}

func RenderSchedulerStatus(status SchedulerStatus) string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Scheduler status: profile=%s enabled=%t discovered=%d", status.ProfileID, status.Enabled, status.Discovered))
	if len(status.Schedules) > 0 {
		lines = append(lines, "Schedules:")
		for _, schedule := range status.Schedules {
			lines = append(lines, renderSchedulerScheduleLine(schedule))
		}
	}
	if len(status.Runtimes) == 0 {
		lines = append(lines, "No scheduler runtimes yet.")
	} else {
		lines = append(lines, "Runtimes:")
		for _, runtime := range status.Runtimes {
			lines = append(lines, fmt.Sprintf("- %s next=%s last=%s skill=%s", runtime.ScheduleID, renderSchedulerOptionalTime(runtime.NextRunAt), renderSchedulerOptionalTime(runtime.LastRunAt), runtime.SkillDir))
		}
	}
	if len(status.RecentRuns) == 0 {
		lines = append(lines, "No scheduler runs yet.")
	} else {
		lines = append(lines, "Recent runs:")
		for _, run := range status.RecentRuns {
			lines = append(lines, renderSchedulerRunLine(run))
		}
	}
	return strings.Join(lines, "\n")
}

func RenderSchedulerRuns(runs []model.SchedulerRun) string {
	if len(runs) == 0 {
		return "No scheduler runs yet."
	}
	lines := []string{"Recent scheduler runs:"}
	for _, run := range runs {
		lines = append(lines, renderSchedulerRunLine(run))
	}
	return strings.Join(lines, "\n")
}

func RenderSchedulerSchedules(schedules []SchedulerScheduleView) string {
	if len(schedules) == 0 {
		return "No scheduler schedules discovered."
	}
	lines := []string{"Scheduler schedules:"}
	for _, schedule := range schedules {
		lines = append(lines, renderSchedulerScheduleLine(schedule))
	}
	return strings.Join(lines, "\n")
}

func (s SchedulerStatusService) scheduleViews(schedules []scheduler.ScheduledSkill) ([]SchedulerScheduleView, error) {
	overrides, err := s.store.GetSchedulerEnabledOverrides()
	if err != nil {
		return nil, err
	}
	var views []SchedulerScheduleView
	for _, schedule := range schedules {
		enabledInFile := schedule.Enabled
		var override *bool
		if value, ok := overrides[schedule.ID]; ok {
			copied := value
			override = &copied
		}
		enabled := schedule.Enabled
		if override != nil {
			enabled = *override
		}
		view := SchedulerScheduleView{
			ID:            schedule.ID,
			Name:          schedule.Name,
			Enabled:       enabled,
			EnabledInFile: enabledInFile,
			Override:      override,
			CronExpr:      schedule.CronExpr,
			Timezone:      schedule.Timezone,
			RegistryPath:  schedule.RegistryPath,
			SkillDir:      schedule.SkillDir,
			SkillPath:     schedule.SkillPath,
		}
		runtime, err := s.store.GetSchedulerRuntime(schedule.ID)
		if err != nil {
			if err != sql.ErrNoRows {
				return nil, err
			}
		} else {
			view.LastRunAt = runtime.LastRunAt
			view.NextRunAt = runtime.NextRunAt
		}
		views = append(views, view)
	}
	return views, nil
}

func schedulerSchedulesWithOverrides(store *storage.Store, schedules []scheduler.ScheduledSkill) ([]scheduler.ScheduledSkill, error) {
	overrides, err := store.GetSchedulerEnabledOverrides()
	if err != nil {
		return nil, err
	}
	for i := range schedules {
		if enabled, ok := overrides[schedules[i].ID]; ok {
			schedules[i].Enabled = enabled
		}
	}
	return schedules, nil
}

func renderSchedulerScheduleLine(schedule SchedulerScheduleView) string {
	override := ""
	if schedule.Override != nil {
		override = fmt.Sprintf(" override=%t", *schedule.Override)
	}
	return fmt.Sprintf("- %s enabled=%t%s cron=%q next=%s skill=%s", schedule.ID, schedule.Enabled, override, schedule.CronExpr, renderSchedulerOptionalTime(schedule.NextRunAt), schedule.SkillDir)
}

func renderSchedulerRunLine(run model.SchedulerRun) string {
	line := fmt.Sprintf("- %s schedule=%s status=%s started=%s finished=%s", run.ID, run.ScheduleID, run.Status, renderSchedulerTime(run.StartedAt), renderSchedulerOptionalTime(run.FinishedAt))
	if strings.TrimSpace(run.Error) != "" {
		line += " error=" + strings.TrimSpace(run.Error)
	}
	return line
}

func renderSchedulerTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func renderSchedulerOptionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return renderSchedulerTime(*value)
}
