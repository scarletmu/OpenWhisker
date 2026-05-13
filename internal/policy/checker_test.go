package policy

import (
	"testing"

	"github.com/scarletmu/openwhisker/internal/model"
)

func TestCheckerAllowsLowRiskRawInboxCreate(t *testing.T) {
	plan := model.VaultPlan{
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Operations: []model.VaultOperation{{
			ID:         "op_test",
			Type:       model.OperationCreateNote,
			TargetPath: "Raw/Inbox/job_test.md",
			RiskLevel:  model.RiskLow,
		}},
	}
	if err := NewChecker().Check(plan); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
}

func TestCheckerRejectsUnsafeTargets(t *testing.T) {
	tests := []string{
		"/tmp/outside.md",
		"Raw/Inbox/../outside.md",
		"Raw/Inbox/.hidden.md",
		"Knowledge/note.md",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			plan := model.VaultPlan{
				RiskLevel: model.RiskLow,
				Operations: []model.VaultOperation{{
					ID:         "op_test",
					Type:       model.OperationCreateNote,
					TargetPath: target,
					RiskLevel:  model.RiskLow,
				}},
			}
			if err := NewChecker().Check(plan); err == nil {
				t.Fatalf("Check() expected error for %q", target)
			}
		})
	}
}

func TestCheckerRejectsApprovalRequiredPlan(t *testing.T) {
	plan := model.VaultPlan{
		RiskLevel:        model.RiskLow,
		RequiresApproval: true,
		Operations: []model.VaultOperation{{
			ID:         "op_test",
			Type:       model.OperationCreateNote,
			TargetPath: "Raw/Inbox/job_test.md",
			RiskLevel:  model.RiskLow,
		}},
	}
	if err := NewChecker().Check(plan); err == nil {
		t.Fatal("Check() expected approval-required plan to be rejected")
	}
}
