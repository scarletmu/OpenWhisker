package agent

import (
	"testing"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/scheduler"
)

func TestTightenSchedulerBudget(t *testing.T) {
	mk := func(trigger, query string, maxCalls int) AgentRunRequest {
		return AgentRunRequest{
			TriggerKind: trigger,
			Query:       query,
			Skill:       scheduler.ScheduledSkill{Budget: scheduler.ToolBudget{MaxToolCalls: maxCalls}},
		}
	}
	cases := []struct {
		name     string
		req      AgentRunRequest
		wantMax  int
		wantNote bool
	}{
		{"scheduler no query halves then caps at 4", mk(model.AgentTriggerKindScheduler, "", 8), 4, true},
		{"scheduler no query halves below cap", mk(model.AgentTriggerKindScheduler, "", 6), 3, true},
		{"scheduler no query minimal already", mk(model.AgentTriggerKindScheduler, "", 2), 1, true},
		{"scheduler with query untouched", mk(model.AgentTriggerKindScheduler, "find X", 8), 8, false},
		{"scheduler with blank query untouched at 1", mk(model.AgentTriggerKindScheduler, "  ", 1), 1, false},
		{"adhoc cli untouched", mk(model.AgentTriggerKindAdhocCLI, "", 8), 8, false},
		{"adhoc matrix untouched", mk(model.AgentTriggerKindAdhocMatrix, "", 8), 8, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			note := tightenSchedulerBudget(&req)
			if got := req.Skill.Budget.MaxToolCalls; got != tc.wantMax {
				t.Errorf("MaxToolCalls = %d, want %d", got, tc.wantMax)
			}
			if gotNote := note != ""; gotNote != tc.wantNote {
				t.Errorf("note=%q, wantNote=%v", note, tc.wantNote)
			}
		})
	}
}
