package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/scheduler"
)

func TestOpenAISchedulerEngineBuildsSkillRunResult(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"title": "每日简报",
		"summary": "今天有 1 条待处理 raw 输入。",
		"payload": {
			"observations": ["Raw/Inbox 有待处理条目。"],
			"review_items": []
		}
	}`}
	req := scheduler.SkillExecutionRequest{
		Schedule: scheduler.ScheduledSkill{
			ID:              "daily",
			Name:            "Daily",
			RegistryPath:    "Scheduler/Skills/daily/SCHEDULE.md",
			SkillPath:       "Scheduler/Skills/daily/SKILL.md",
			Capabilities:    []string{scheduler.CapabilityVaultRead, scheduler.CapabilityOutboxNotify},
			Delivery:        []string{"outbox"},
			SkillConfigJSON: []byte(`{"detail":"normal"}`),
		},
		Now:          time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC),
		SkillContent: "# Daily\n\nSummarize pending raw notes.",
		SkillHeading: "Daily",
		VaultContext: []scheduler.VaultContextItem{{
			Path:    "Raw/",
			Kind:    "directory",
			Entries: []string{"Inbox.md"},
		}},
	}

	result, err := (OpenAISchedulerEngine{Client: client}).RunSkill(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.ScheduleID != "daily" || result.Title != "每日简报" || !strings.Contains(result.Summary, "待处理") {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(string(client.request.Input), "Scheduler/Skills/daily/SKILL.md") ||
		!strings.Contains(string(client.request.Input), "Raw/") {
		t.Fatalf("input = %s, want skill path and vault context", client.request.Input)
	}
}

func TestOpenAISchedulerEngineRejectsExecutablePayload(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"title": "bad",
		"summary": "bad",
		"payload": {
			"shell_command": "rm -rf vault"
		}
	}`}
	_, err := (OpenAISchedulerEngine{Client: client}).RunSkill(context.Background(), scheduler.SkillExecutionRequest{
		Schedule: scheduler.ScheduledSkill{ID: "bad"},
	})
	if err == nil || !strings.Contains(err.Error(), "shell_command") {
		t.Fatalf("error = %v, want forbidden payload field", err)
	}
}

func TestOpenAISchedulerEngineRetriesOnceOnEmptyContent(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{"", `{"title":"ok","summary":"ok","payload":{}}`}}
	result, err := (OpenAISchedulerEngine{Client: client}).RunSkill(context.Background(), scheduler.SkillExecutionRequest{
		Schedule: scheduler.ScheduledSkill{ID: "daily"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "ok" || client.calls != 2 {
		t.Fatalf("result = %+v calls=%d, want retry success", result, client.calls)
	}
}
