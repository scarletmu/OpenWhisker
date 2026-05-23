package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/profile"
)

func TestLoadRegistryReadsOnlyDeclaredSchedulerSkillRoot(t *testing.T) {
	vaultRoot := t.TempDir()
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/daily", scheduleMarkdown("daily", "0 8 * * *"), "# Daily")
	writeScheduleSkill(t, vaultRoot, "Skills/tui", scheduleMarkdown("tui", "0 8 * * *"), "# TUI")

	vaultProfile := profile.VaultProfile{
		ID: "test",
		Scheduler: profile.SchedulerProfile{
			Enabled:             true,
			RegistryPaths:       []string{"Scheduler/Skills/*/SCHEDULE.md"},
			ScheduledSkillRoots: []string{"Scheduler/Skills/"},
			DefaultDelivery:     []string{"outbox"},
		},
	}
	schedules, err := LoadRegistry(vaultRoot, vaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedule count = %d, want 1", len(schedules))
	}
	if schedules[0].ID != "daily" || schedules[0].SkillPath != "Scheduler/Skills/daily/SKILL.md" {
		t.Fatalf("schedule = %+v, want same-dir scheduler skill", schedules[0])
	}
}

func TestLoadRegistryRejectsScheduleOutsideSkillRoot(t *testing.T) {
	vaultRoot := t.TempDir()
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/daily", scheduleMarkdown("daily", "0 8 * * *"), "# Daily")
	vaultProfile := profile.VaultProfile{
		ID: "test",
		Scheduler: profile.SchedulerProfile{
			Enabled:             true,
			RegistryPaths:       []string{"Scheduler/Skills/*/SCHEDULE.md"},
			ScheduledSkillRoots: []string{"Scheduler/Allowed/"},
			DefaultDelivery:     []string{"outbox"},
		},
	}
	_, err := LoadRegistry(vaultRoot, vaultProfile)
	if err == nil || !strings.Contains(err.Error(), "outside scheduled_skill_roots") {
		t.Fatalf("error = %v, want root guard error", err)
	}
}

func TestLoadRegistryRequiresSameDirectorySkill(t *testing.T) {
	vaultRoot := t.TempDir()
	dir := filepath.Join(vaultRoot, "Scheduler", "Skills", "daily")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SCHEDULE.md"), []byte(scheduleMarkdown("daily", "0 8 * * *")), 0o644); err != nil {
		t.Fatal(err)
	}
	vaultProfile := profile.VaultProfile{
		ID: "test",
		Scheduler: profile.SchedulerProfile{
			Enabled:             true,
			RegistryPaths:       []string{"Scheduler/Skills/*/SCHEDULE.md"},
			ScheduledSkillRoots: []string{"Scheduler/Skills/"},
			DefaultDelivery:     []string{"outbox"},
		},
	}
	_, err := LoadRegistry(vaultRoot, vaultProfile)
	if err == nil || !strings.Contains(err.Error(), "SKILL.md") {
		t.Fatalf("error = %v, want missing skill error", err)
	}
}

func TestLoadRegistryRejectsDisabledCapability(t *testing.T) {
	vaultRoot := t.TempDir()
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/daily", scheduleMarkdownWithExtra("daily", "0 8 * * *", `
capabilities:
  - vault_read
  - vault_write
`), "# Daily")
	_, err := LoadRegistry(vaultRoot, testRegistryProfile())
	if err == nil || !strings.Contains(err.Error(), "vault_write") {
		t.Fatalf("error = %v, want disabled capability error", err)
	}
}

func TestLoadRegistryRejectsVaultContextOutsideReadRoots(t *testing.T) {
	vaultRoot := t.TempDir()
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/daily", scheduleMarkdownWithExtra("daily", "0 8 * * *", `
vault_context:
  - Private/Secrets.md
`), "# Daily")
	_, err := LoadRegistry(vaultRoot, testRegistryProfile())
	if err == nil || !strings.Contains(err.Error(), "outside read_only_vault_roots") {
		t.Fatalf("error = %v, want read root error", err)
	}
}

func TestLoadRegistryAllowsConfiguredExternalInfoSource(t *testing.T) {
	vaultRoot := t.TempDir()
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/release", scheduleMarkdownWithExtra("release", "0 8 * * *", `
external_info_sources:
  - github-releases
`), "# Release")
	vaultProfile := testRegistryProfile()
	vaultProfile.Scheduler.ExternalInfoSources = []string{"github-releases"}
	schedules, err := LoadRegistry(vaultRoot, vaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 || !containsString(schedules[0].Capabilities, CapabilityExternalInfoRead) {
		t.Fatalf("schedules = %+v, want external_info_read capability", schedules)
	}
}

func TestCronCurrentWindowAndNext(t *testing.T) {
	cronSchedule, err := ParseCron("*/15 8-9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 23, 8, 30, 42, 0, time.UTC)
	window, ok := cronSchedule.CurrentWindow(now, time.UTC)
	if !ok {
		t.Fatal("current window not matched")
	}
	if window != time.Date(2026, 5, 23, 8, 30, 0, 0, time.UTC) {
		t.Fatalf("window = %s", window)
	}
	next, err := cronSchedule.NextAfter(now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if next != time.Date(2026, 5, 23, 8, 45, 0, 0, time.UTC) {
		t.Fatalf("next = %s", next)
	}
}

func writeScheduleSkill(t *testing.T, vaultRoot, dir, schedule, skill string) {
	t.Helper()
	absDir := filepath.Join(vaultRoot, filepath.FromSlash(dir))
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(absDir, "SCHEDULE.md"), []byte(schedule), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(absDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scheduleMarkdown(id, cronExpr string) string {
	return scheduleMarkdownWithExtra(id, cronExpr, "")
}

func scheduleMarkdownWithExtra(id, cronExpr, extra string) string {
	return `---
id: ` + id + `
name: ` + id + `
enabled: true
cron_expr: "` + cronExpr + `"
timezone: UTC
delivery:
  - outbox
` + strings.TrimPrefix(extra, "\n") + `
skill_config:
  detail: normal
---

# ` + id + `
`
}

func testRegistryProfile() profile.VaultProfile {
	return profile.VaultProfile{
		ID: "test",
		Scheduler: profile.SchedulerProfile{
			Enabled:             true,
			RegistryPaths:       []string{"Scheduler/Skills/*/SCHEDULE.md"},
			ScheduledSkillRoots: []string{"Scheduler/Skills/"},
			DefaultDelivery:     []string{"outbox"},
			ReadOnlyVaultRoots:  []string{"Raw/", "Knowledge/", "Meta/"},
		},
	}
}
