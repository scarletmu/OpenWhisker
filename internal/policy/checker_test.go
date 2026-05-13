package policy

import (
	"encoding/json"
	"strings"
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

func TestCheckerAllowsMediumRiskRawOrganizerPlan(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	if err := NewChecker().CheckForApproval(plan); err != nil {
		t.Fatalf("CheckForApproval() error = %v", err)
	}
}

func TestCheckerRejectsMediumRiskPlanWithoutSourceRefs(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.SourceRefs = nil
	err := NewChecker().CheckForApproval(plan)
	if err == nil || !strings.Contains(err.Error(), "source_refs") {
		t.Fatalf("CheckForApproval() error = %v, want source_refs error", err)
	}
}

func TestCheckerRejectsKnowledgeDraftWithoutRequiredFrontmatter(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.Operations[0].PayloadJSON = mustJSON(t, model.CreateNotePayload{
		Content: "# Missing frontmatter\n",
	})
	err := NewChecker().CheckForApproval(plan)
	if err == nil || !strings.Contains(err.Error(), "frontmatter") {
		t.Fatalf("CheckForApproval() error = %v, want frontmatter error", err)
	}
}

func TestCheckerRejectsKnowledgeDraftWithoutProcessedSourceLink(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.Operations[0].PayloadJSON = mustJSON(t, model.CreateNotePayload{
		Content: strings.Replace(validKnowledgeDraft(), "- Raw path after approval: Raw/Processed/job_test.md", "- Raw path after approval: omitted", 1),
	})
	err := NewChecker().CheckForApproval(plan)
	if err == nil || !strings.Contains(err.Error(), "source_processed_path in the body") {
		t.Fatalf("CheckForApproval() error = %v, want processed source link error", err)
	}
}

func TestCheckerRejectsMoveDestinationMissingFromTargetPaths(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.TargetPaths = []string{"Knowledge/Drafts/job_test.md", "Raw/Inbox/job_test.md"}
	err := NewChecker().CheckForApproval(plan)
	if err == nil || !strings.Contains(err.Error(), "missing from plan target_paths") {
		t.Fatalf("CheckForApproval() error = %v, want target_paths error", err)
	}
}

func TestCheckerRejectsMoveWithoutProcessingNote(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.Operations[1].PayloadJSON = mustJSON(t, model.MoveNotePayload{DestinationPath: "Raw/Processed/job_test.md"})
	err := NewChecker().CheckForApproval(plan)
	if err == nil || !strings.Contains(err.Error(), "processing_note") {
		t.Fatalf("CheckForApproval() error = %v, want processing_note error", err)
	}
}

func mediumRiskRawOrganizerPlan(t *testing.T) model.VaultPlan {
	t.Helper()
	return model.VaultPlan{
		ID:               "plan_test",
		JobID:            "job_plan",
		Purpose:          "organize raw into Knowledge draft",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          "Create one Knowledge draft and move raw to processed.",
		SourceRefs:       []string{"job_raw", "Raw/Inbox/job_test.md"},
		TargetPaths:      []string{"Knowledge/Drafts/job_test.md", "Raw/Inbox/job_test.md", "Raw/Processed/job_test.md"},
		Operations: []model.VaultOperation{
			{
				ID:          "op_create",
				Type:        model.OperationCreateNote,
				TargetPath:  "Knowledge/Drafts/job_test.md",
				PayloadJSON: mustJSON(t, model.CreateNotePayload{Content: validKnowledgeDraft()}),
				Reason:      "Create a reviewed Knowledge draft.",
				RiskLevel:   model.RiskMedium,
			},
			{
				ID:         "op_move",
				Type:       model.OperationMoveNote,
				TargetPath: "Raw/Inbox/job_test.md",
				PayloadJSON: mustJSON(t, model.MoveNotePayload{
					DestinationPath: "Raw/Processed/job_test.md",
					ProcessingNote:  "Processed raw: Raw/Processed/job_test.md\nOutput: Knowledge/Drafts/job_test.md",
				}),
				Reason:    "Move raw to processed after approval.",
				RiskLevel: model.RiskMedium,
			},
		},
	}
}

func validKnowledgeDraft() string {
	return `---
openwhisker_job_id: job_plan
openwhisker_job_type: organize_raw
source_raw_job_id: job_raw
source_raw_path: Raw/Inbox/job_test.md
source_processed_path: Raw/Processed/job_test.md
status: draft
needs_review: true
tags:
  - knowledge/draft
  - review/needed
---

# Test Draft

## Source

- Raw path after approval: Raw/Processed/job_test.md
`
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
