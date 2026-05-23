package scheduler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStaticSkillRunnerReadsDeclaredVaultContext(t *testing.T) {
	vaultRoot := t.TempDir()
	skillDir := filepath.Join(vaultRoot, "Scheduler", "Skills", "daily")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Daily\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rawDir := filepath.Join(vaultRoot, "Raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rawDir, "Inbox.md"), []byte("pending item"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := StaticSkillRunner{VaultRoot: vaultRoot}.Run(context.Background(), SkillRunRequest{
		Schedule: ScheduledSkill{
			ID:           "daily",
			Name:         "Daily",
			SkillPath:    "Scheduler/Skills/daily/SKILL.md",
			RegistryPath: "Scheduler/Skills/daily/SCHEDULE.md",
			Capabilities: []string{CapabilityVaultRead, CapabilitySchedulerRunLogWrite, CapabilityOutboxNotify},
			VaultContext: []string{"Raw/Inbox.md"},
		},
		Now: time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		VaultContext []VaultContextItem `json:"vault_context"`
	}
	if err := json.Unmarshal(result.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.VaultContext) != 1 || !strings.Contains(payload.VaultContext[0].Content, "pending item") {
		t.Fatalf("payload = %s, want declared vault context content", string(result.Payload))
	}
}

func TestStaticSkillRunnerFailsWhenExternalAdapterIsMissing(t *testing.T) {
	vaultRoot := t.TempDir()
	skillDir := filepath.Join(vaultRoot, "Scheduler", "Skills", "release")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := StaticSkillRunner{VaultRoot: vaultRoot}.Run(context.Background(), SkillRunRequest{
		Schedule: ScheduledSkill{
			ID:              "release",
			Name:            "Release",
			SkillPath:       "Scheduler/Skills/release/SKILL.md",
			ExternalSources: []string{"github-releases"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "external info adapter") {
		t.Fatalf("error = %v, want missing external adapter error", err)
	}
}

func TestStaticSkillRunnerReadsConfiguredExternalInfoAdapter(t *testing.T) {
	vaultRoot := t.TempDir()
	skillDir := filepath.Join(vaultRoot, "Scheduler", "Skills", "release")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 5, 23, 9, 0, 0, 0, time.UTC)
	result, err := StaticSkillRunner{
		VaultRoot: vaultRoot,
		ExternalInfoAdapters: map[string]ExternalInfoAdapter{
			"github-releases": fakeExternalInfoAdapter{},
		},
	}.Run(context.Background(), SkillRunRequest{
		Schedule: ScheduledSkill{
			ID:              "release",
			Name:            "Release",
			SkillPath:       "Scheduler/Skills/release/SKILL.md",
			ExternalSources: []string{"github-releases"},
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		ExternalInfo []ExternalInfoItem `json:"external_info"`
	}
	if err := json.Unmarshal(result.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.ExternalInfo) != 1 || payload.ExternalInfo[0].Source != "github-releases" {
		t.Fatalf("payload = %s, want external info item", string(result.Payload))
	}
}

type fakeExternalInfoAdapter struct{}

func (fakeExternalInfoAdapter) ReadInfo(_ context.Context, req ExternalInfoRequest) (ExternalInfoItem, error) {
	payload, _ := json.Marshal(map[string]string{"schedule_id": req.Schedule.ID})
	return ExternalInfoItem{
		Source:    req.Source,
		Summary:   "release summary",
		Payload:   payload,
		FetchedAt: req.Now,
	}, nil
}
