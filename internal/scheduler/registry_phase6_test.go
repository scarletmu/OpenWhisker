package scheduler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarletmu/openwhisker/internal/profile"
)

func writeAgentSkill(t *testing.T, vaultRoot, dir, skill string) {
	t.Helper()
	absDir := filepath.Join(vaultRoot, filepath.FromSlash(dir))
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(absDir, "SKILL.md"), []byte(skill), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeAgentSchedule(t *testing.T, vaultRoot, dir, schedule string) {
	t.Helper()
	absDir := filepath.Join(vaultRoot, filepath.FromSlash(dir))
	if err := os.MkdirAll(absDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(absDir, "SCHEDULE.md"), []byte(schedule), 0o644); err != nil {
		t.Fatal(err)
	}
}

func phase6Profile() profile.VaultProfile {
	return profile.VaultProfile{
		ID: "test",
		Scheduler: profile.SchedulerProfile{
			Enabled:            true,
			AgentSkillRoots:    []string{"Agent/Skills/"},
			AgentRegistryPaths: []string{"Agent/Skills/*/SKILL.md"},
			DefaultDelivery:    []string{"outbox"},
			ReadOnlyVaultRoots: []string{"Raw/", "Knowledge/", "Meta/"},
			DefaultEngine:      profile.AgentSkillEngineStatic,
			ToolBudgetDefaults: profile.DefaultToolBudgetDefaults(),
		},
	}
}

const phase6SkillTemplate = `---
id: %s
name: Vault QA
engine: tool-calling
vault_tools:
  - read_vault_note
  - vault_text_search
vault_scope:
  - Raw/
  - Knowledge/
budget:
  max_tool_calls: 4
  max_total_bytes: 102400
  max_wall_clock_seconds: 30
---

# Vault QA

Answer the user query by reading notes.
`

func TestLoadRegistry_AgentSkillOnly_AdHocOnly(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/vault-qa", strings.Replace(phase6SkillTemplate, "%s", "vault-qa", 1))
	schedules, err := LoadRegistry(vaultRoot, phase6Profile())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedule count = %d, want 1: %+v", len(schedules), schedules)
	}
	s := schedules[0]
	if s.ID != "vault-qa" {
		t.Errorf("id = %q, want vault-qa", s.ID)
	}
	if s.HasSchedule {
		t.Errorf("HasSchedule = true, want false (no SCHEDULE.md)")
	}
	if s.Engine != profile.AgentSkillEngineToolCalling {
		t.Errorf("engine = %q, want tool-calling", s.Engine)
	}
	if !s.HasToolCalling() {
		t.Errorf("HasToolCalling() = false; expected true")
	}
	if got := s.Budget.MaxToolCalls; got != 4 {
		t.Errorf("budget.max_tool_calls = %d, want 4", got)
	}
	wantTools := []string{"read_vault_note", "vault_text_search"}
	if strings.Join(s.VaultTools, ",") != strings.Join(wantTools, ",") {
		t.Errorf("vault_tools = %v, want %v (sorted)", s.VaultTools, wantTools)
	}
}

func TestLoadRegistry_AgentSkillWithSchedule(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/daily-qa", strings.Replace(phase6SkillTemplate, "%s", "daily-qa", 1))
	writeAgentSchedule(t, vaultRoot, "Agent/Skills/daily-qa", `---
id: daily-qa
enabled: true
cron_expr: "0 8 * * *"
timezone: UTC
---
`)
	schedules, err := LoadRegistry(vaultRoot, phase6Profile())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedule count = %d", len(schedules))
	}
	s := schedules[0]
	if !s.HasSchedule {
		t.Errorf("HasSchedule = false, want true")
	}
	if s.CronExpr != "0 8 * * *" {
		t.Errorf("cron = %q", s.CronExpr)
	}
	if !s.Enabled {
		t.Errorf("enabled = false, want true")
	}
}

func TestLoadRegistry_ScheduleMdRejectsPhase6Fields(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", strings.Replace(phase6SkillTemplate, "%s", "qa", 1))
	writeAgentSchedule(t, vaultRoot, "Agent/Skills/qa", `---
id: qa
cron_expr: "0 8 * * *"
vault_tools:
  - read_vault_note
---
`)
	_, err := LoadRegistry(vaultRoot, phase6Profile())
	if err == nil || !strings.Contains(err.Error(), "vault_tools") {
		t.Fatalf("error = %v, want error about vault_tools belonging in SKILL.md", err)
	}
}

func TestLoadRegistry_ScheduleMdWarnsOnPhase5Fields(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", strings.Replace(phase6SkillTemplate, "%s", "qa", 1))
	writeAgentSchedule(t, vaultRoot, "Agent/Skills/qa", `---
id: qa
cron_expr: "0 8 * * *"
capabilities:
  - vault_read
---
`)
	schedules, err := LoadRegistry(vaultRoot, phase6Profile())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatal("expected 1 skill loaded with warning")
	}
	found := false
	for _, w := range schedules[0].DeprecationWarnings {
		if strings.Contains(w, "capabilities") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected deprecation warning about capabilities, got %v", schedules[0].DeprecationWarnings)
	}
}

func TestLoadRegistry_RejectsForbiddenPrefixInReadRoots(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", strings.Replace(phase6SkillTemplate, "%s", "qa", 1))
	p := phase6Profile()
	p.Scheduler.ReadOnlyVaultRoots = append(p.Scheduler.ReadOnlyVaultRoots, ".obsidian/")
	_, err := LoadRegistry(vaultRoot, p)
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("error = %v, want forbidden-prefix rejection", err)
	}
}

func TestLoadRegistry_RejectsVaultScopeForbiddenPrefix(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", `---
id: qa
engine: tool-calling
vault_tools:
  - read_vault_note
vault_scope:
  - .obsidian/plugins/
---
# QA
body
`)
	_, err := LoadRegistry(vaultRoot, phase6Profile())
	if err == nil || !strings.Contains(err.Error(), "forbidden prefix") {
		t.Fatalf("error = %v, want forbidden prefix rejection", err)
	}
}

func TestLoadRegistry_EngineConflict(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", `---
id: qa
engine: static
vault_tools:
  - read_vault_note
vault_scope:
  - Raw/
---
# QA
body
`)
	_, err := LoadRegistry(vaultRoot, phase6Profile())
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v, want engine/tools conflict", err)
	}
}

func TestLoadRegistry_BudgetExceedsCapRejected(t *testing.T) {
	vaultRoot := t.TempDir()
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", `---
id: qa
engine: tool-calling
vault_tools:
  - read_vault_note
vault_scope:
  - Raw/
budget:
  max_tool_calls: 100
---
# QA
body
`)
	p := phase6Profile()
	p.Scheduler.ToolBudgetDefaults.MaxToolCalls = 8
	_, err := LoadRegistry(vaultRoot, p)
	if err == nil || !strings.Contains(err.Error(), "exceeds profile cap") {
		t.Fatalf("error = %v, want cap exceeded", err)
	}
}

func TestLoadRegistry_GlobalIDUniqueAcrossTrees(t *testing.T) {
	vaultRoot := t.TempDir()
	// Legacy path
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/qa", scheduleMarkdown("qa", "0 8 * * *"), "# QA legacy")
	// Agent path with same id
	writeAgentSkill(t, vaultRoot, "Agent/Skills/qa", strings.Replace(phase6SkillTemplate, "%s", "qa", 1))

	p := phase6Profile()
	p.Scheduler.RegistryPaths = []string{"Scheduler/Skills/*/SCHEDULE.md"}
	p.Scheduler.ScheduledSkillRoots = []string{"Scheduler/Skills/"}
	_, err := LoadRegistry(vaultRoot, p)
	if err == nil || !strings.Contains(err.Error(), "duplicate skill id") {
		t.Fatalf("error = %v, want duplicate id rejection", err)
	}
}

func TestLoadRegistry_LegacyKindStillLoads(t *testing.T) {
	vaultRoot := t.TempDir()
	writeScheduleSkill(t, vaultRoot, "Scheduler/Skills/daily", scheduleMarkdown("daily", "0 8 * * *"), "# Daily")
	p := phase6Profile()
	p.Scheduler.RegistryPaths = []string{"Scheduler/Skills/*/SCHEDULE.md"}
	p.Scheduler.ScheduledSkillRoots = []string{"Scheduler/Skills/"}
	schedules, err := LoadRegistry(vaultRoot, p)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedule count = %d, want 1", len(schedules))
	}
	s := schedules[0]
	if s.Engine != profile.AgentSkillEngineStatic {
		t.Errorf("engine = %q, want static (legacy default)", s.Engine)
	}
	if !s.HasSchedule {
		t.Errorf("legacy skill should have HasSchedule=true")
	}
}

func TestIsForbiddenScopePath(t *testing.T) {
	for _, p := range []string{".obsidian", ".obsidian/", ".obsidian/plugins/foo", "Notes/.git/", ".trash/old.md", "Knowledge/.DS_Store"} {
		if !IsForbiddenScopePath(p) {
			t.Errorf("IsForbiddenScopePath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{"Raw/", "Knowledge/CS/locks.md", "Meta/AGENTS.md"} {
		if IsForbiddenScopePath(p) {
			t.Errorf("IsForbiddenScopePath(%q) = true, want false", p)
		}
	}
}
