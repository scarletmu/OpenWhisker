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
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestSchedulerTickRunsDueScheduleAndEnqueuesOutbox(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCoreScheduleSkill(t, vaultRoot, "daily", "30 8 * * *")
	store, err := storage.Open(filepath.Join(t.TempDir(), "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Date(2026, 5, 23, 8, 30, 10, 0, time.UTC)
	result, err := NewSchedulerServiceWithOptions(store, vaultRoot, SchedulerServiceOptions{
		VaultProfile: testSchedulerProfile(),
		Runner:       fakeSchedulerRunner{},
		Now:          func() time.Time { return now },
	}).Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Discovered != 1 || result.Due != 1 || result.Completed != 1 {
		t.Fatalf("tick result = %+v, want one completed due run", result)
	}
	outbox, err := store.ListPendingOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 1 {
		t.Fatalf("outbox count = %d, want 1", len(outbox))
	}
	if !strings.Contains(outbox[0].Body, "Daily Briefing") || !strings.Contains(outbox[0].Body, "Scheduler/Skills/daily/SKILL.md") {
		t.Fatalf("outbox body = %q", outbox[0].Body)
	}
	if outbox[0].Actor != model.OutboxActorScheduler {
		t.Fatalf("outbox actor = %q, want scheduler", outbox[0].Actor)
	}
	runs, err := store.ListSchedulerRuns("daily", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != model.SchedulerRunStatusDone || runs[0].OutboxMessageID == "" {
		t.Fatalf("runs = %+v, want done run with outbox id", runs)
	}
}

func TestSchedulerTickSkipsWhenPreviousRunIsStillRunning(t *testing.T) {
	vaultRoot := t.TempDir()
	writeCoreScheduleSkill(t, vaultRoot, "daily", "30 8 * * *")
	store, err := storage.Open(filepath.Join(t.TempDir(), "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 23, 8, 30, 0, 0, time.UTC)
	schedules, err := scheduler.LoadRegistry(vaultRoot, testSchedulerProfile())
	if err != nil {
		t.Fatal(err)
	}
	nextRunAt := now
	runtime := model.SchedulerRuntime{
		ID:           "schedrt_test",
		ScheduleID:   "daily",
		RegistryPath: schedules[0].RegistryPath,
		RegistryHash: schedules[0].RegistryHash,
		SkillDir:     schedules[0].SkillDir,
		SkillPath:    schedules[0].SkillPath,
		NextRunAt:    &nextRunAt,
		UpdatedAt:    now,
	}
	if err := store.SaveSchedulerRuntime(runtime); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSchedulerRun(model.SchedulerRun{
		ID:         "schedrun_running",
		ScheduleID: "daily",
		RuntimeID:  runtime.ID,
		SkillDir:   runtime.SkillDir,
		SkillPath:  runtime.SkillPath,
		Status:     model.SchedulerRunStatusRunning,
		StartedAt:  now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	result, err := NewSchedulerServiceWithOptions(store, vaultRoot, SchedulerServiceOptions{
		VaultProfile: testSchedulerProfile(),
		Runner:       fakeSchedulerRunner{},
		Now:          func() time.Time { return now },
	}).Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || len(result.Runs) != 1 || result.Runs[0].Status != model.SchedulerRunStatusSkipped {
		t.Fatalf("tick result = %+v, want skipped run", result)
	}
	outbox, err := store.ListPendingOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(outbox) != 0 {
		t.Fatalf("outbox count = %d, want no skip outbox", len(outbox))
	}
}

type fakeSchedulerRunner struct{}

func (fakeSchedulerRunner) Run(_ context.Context, req scheduler.SkillRunRequest) (scheduler.SkillRunResult, error) {
	payload, _ := json.Marshal(map[string]string{"ok": "true"})
	return scheduler.SkillRunResult{
		ScheduleID: req.Schedule.ID,
		SkillID:    req.Schedule.ID,
		Title:      "Daily Briefing",
		Summary:    "Daily briefing summary.",
		Payload:    payload,
	}, nil
}

func testSchedulerProfile() profile.VaultProfile {
	return profile.VaultProfile{
		ID: "test",
		Scheduler: profile.SchedulerProfile{
			Enabled:             true,
			RegistryPaths:       []string{"Scheduler/Skills/*/SCHEDULE.md"},
			ScheduledSkillRoots: []string{"Scheduler/Skills/"},
			DefaultDelivery:     []string{"outbox"},
		},
	}
}

func writeCoreScheduleSkill(t *testing.T, vaultRoot, id, cronExpr string) {
	t.Helper()
	dir := filepath.Join(vaultRoot, "Scheduler", "Skills", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	schedule := `---
id: ` + id + `
name: ` + id + `
enabled: true
cron_expr: "` + cronExpr + `"
timezone: UTC
delivery:
  - outbox
skill_config:
  detail: normal
---

# ` + id + `
`
	if err := os.WriteFile(filepath.Join(dir, "SCHEDULE.md"), []byte(schedule), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# "+id+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
