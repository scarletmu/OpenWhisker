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

func TestCheckerUsesProfileRequiredDraftTags(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.Operations[0].PayloadJSON = mustJSON(t, model.CreateNotePayload{
		Content: strings.Replace(validKnowledgeDraft(), "  - status/needs-review\n", "", 1),
	})
	if err := NewChecker().CheckForApproval(plan); err != nil {
		t.Fatalf("generic checker should not require vault-specific tags: %v", err)
	}
	err := NewCheckerWithConventions(KnowledgeVaultConventions()).CheckForApproval(plan)
	if err == nil || !strings.Contains(err.Error(), "status/needs-review") {
		t.Fatalf("CheckForApproval() error = %v, want required tag error", err)
	}
}

func TestConventionsCarryProfileIdentity(t *testing.T) {
	generic := DefaultConventions().Normalize()
	if generic.ProfileID != "generic" {
		t.Fatalf("generic profile id = %q", generic.ProfileID)
	}
	knowledge := KnowledgeVaultConventions().Normalize()
	if knowledge.ProfileID != "knowledge-vault" {
		t.Fatalf("knowledge profile id = %q", knowledge.ProfileID)
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
  - type/knowledge
  - status/draft
  - status/needs-review
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

func TestCheckerAllowsHighRiskRenamePlan(t *testing.T) {
	plan := highRiskRenamePlan(t)
	if err := NewChecker().CheckForApprovalHighRisk(plan); err != nil {
		t.Fatalf("CheckForApprovalHighRisk() error = %v", err)
	}
	kind, err := ClassifyProposalKind(plan)
	if err != nil {
		t.Fatalf("ClassifyProposalKind() error = %v", err)
	}
	if kind != model.ProposalKindRename {
		t.Fatalf("ClassifyProposalKind() = %q, want %q", kind, model.ProposalKindRename)
	}
}

func TestCheckerAllowsHighRiskBulkRetagPlan(t *testing.T) {
	plan := highRiskBulkRetagPlan(t)
	if err := NewChecker().CheckForApprovalHighRisk(plan); err != nil {
		t.Fatalf("CheckForApprovalHighRisk() error = %v", err)
	}
	kind, err := ClassifyProposalKind(plan)
	if err != nil {
		t.Fatalf("ClassifyProposalKind() error = %v", err)
	}
	if kind != model.ProposalKindBulkRetag {
		t.Fatalf("ClassifyProposalKind() = %q, want %q", kind, model.ProposalKindBulkRetag)
	}
}

func TestCheckerRejectsHighRiskPlanWithMediumRisk(t *testing.T) {
	plan := highRiskRenamePlan(t)
	plan.RiskLevel = model.RiskMedium
	err := NewChecker().CheckForApprovalHighRisk(plan)
	if err == nil || !strings.Contains(err.Error(), "high-risk approval flow") {
		t.Fatalf("CheckForApprovalHighRisk() error = %v, want risk mismatch", err)
	}
}

func TestCheckerRejectsHighRiskRenameOutsideKnowledge(t *testing.T) {
	plan := highRiskRenamePlan(t)
	plan.Operations[0].TargetPath = "Raw/Inbox/note.md"
	plan.Operations[0].PayloadJSON = mustJSON(t, model.RenameNotePayload{
		SourcePath:      "Raw/Inbox/note.md",
		DestinationPath: "Raw/Inbox/renamed.md",
	})
	plan.TargetPaths = []string{"Raw/Inbox/note.md", "Raw/Inbox/renamed.md"}
	err := NewChecker().CheckForApprovalHighRisk(plan)
	if err == nil || !strings.Contains(err.Error(), "outside Knowledge") {
		t.Fatalf("CheckForApprovalHighRisk() error = %v, want Knowledge-scope error", err)
	}
}

func TestCheckerRejectsHighRiskRenameInsideDrafts(t *testing.T) {
	plan := highRiskRenamePlan(t)
	plan.Operations[0].TargetPath = "Knowledge/Drafts/draft.md"
	plan.Operations[0].PayloadJSON = mustJSON(t, model.RenameNotePayload{
		SourcePath:      "Knowledge/Drafts/draft.md",
		DestinationPath: "Knowledge/Drafts/renamed.md",
	})
	plan.TargetPaths = []string{"Knowledge/Drafts/draft.md", "Knowledge/Drafts/renamed.md"}
	err := NewChecker().CheckForApprovalHighRisk(plan)
	if err == nil || !strings.Contains(err.Error(), "draft renames") {
		t.Fatalf("CheckForApprovalHighRisk() error = %v, want draft-renames error", err)
	}
}

func TestCheckerRejectsHighRiskBulkRetagBelowThreshold(t *testing.T) {
	plan := highRiskBulkRetagPlan(t)
	short := []string{"Knowledge/a.md", "Knowledge/b.md"}
	plan.Operations[0].PayloadJSON = mustJSON(t, model.BulkRetagPayload{
		AffectedPaths: short,
		AddTags:       []string{"topic/database"},
	})
	err := NewChecker().CheckForApprovalHighRisk(plan)
	if err == nil || !strings.Contains(err.Error(), ">=") {
		t.Fatalf("CheckForApprovalHighRisk() error = %v, want threshold error", err)
	}
}

func TestCheckerRejectsHighRiskOpInMediumPlan(t *testing.T) {
	plan := mediumRiskRawOrganizerPlan(t)
	plan.Operations = append(plan.Operations, model.VaultOperation{
		ID:         "op_rename",
		Type:       model.OperationRenameNote,
		TargetPath: "Knowledge/topic/note.md",
		PayloadJSON: mustJSON(t, model.RenameNotePayload{
			SourcePath:      "Knowledge/topic/note.md",
			DestinationPath: "Knowledge/topic/renamed.md",
		}),
		Reason:    "rename note",
		RiskLevel: model.RiskHigh,
	})
	err := NewChecker().CheckForApproval(plan)
	if err == nil {
		t.Fatal("CheckForApproval() expected high-risk op inside medium plan to be rejected")
	}
}

func TestClassifyProposalKindMergeAndSplit(t *testing.T) {
	mergePlan := highRiskRenamePlan(t)
	mergePlan.Operations = []model.VaultOperation{
		renameOp(t, "op_r1", "Knowledge/topic/a.md", "Knowledge/topic/merged.md"),
		renameOp(t, "op_r2", "Knowledge/topic/b.md", "Knowledge/topic/merged.md"),
	}
	mergePlan.TargetPaths = []string{
		"Knowledge/topic/a.md",
		"Knowledge/topic/b.md",
		"Knowledge/topic/merged.md",
	}
	kind, err := ClassifyProposalKind(mergePlan)
	if err != nil {
		t.Fatalf("ClassifyProposalKind() error = %v", err)
	}
	if kind != model.ProposalKindMerge {
		t.Fatalf("merge kind = %q, want %q", kind, model.ProposalKindMerge)
	}

	splitPlan := highRiskRenamePlan(t)
	splitPlan.Operations = []model.VaultOperation{
		renameOp(t, "op_r1", "Knowledge/topic/a.md", "Knowledge/topic/a-part1.md"),
		renameOp(t, "op_r2", "Knowledge/topic/a.md", "Knowledge/topic/a-part2.md"),
	}
	splitPlan.TargetPaths = []string{
		"Knowledge/topic/a.md",
		"Knowledge/topic/a-part1.md",
		"Knowledge/topic/a-part2.md",
	}
	kind, err = ClassifyProposalKind(splitPlan)
	if err != nil {
		t.Fatalf("ClassifyProposalKind() error = %v", err)
	}
	if kind != model.ProposalKindSplit {
		t.Fatalf("split kind = %q, want %q", kind, model.ProposalKindSplit)
	}
}

func renameOp(t *testing.T, id, src, dst string) model.VaultOperation {
	t.Helper()
	return model.VaultOperation{
		ID:         id,
		Type:       model.OperationRenameNote,
		TargetPath: src,
		PayloadJSON: mustJSON(t, model.RenameNotePayload{
			SourcePath:      src,
			DestinationPath: dst,
		}),
		Reason:    "rename for high-risk test",
		RiskLevel: model.RiskHigh,
	}
}

func highRiskRenamePlan(t *testing.T) model.VaultPlan {
	t.Helper()
	src := "Knowledge/topic/old-name.md"
	dst := "Knowledge/topic/new-name.md"
	return model.VaultPlan{
		ID:               "plan_high",
		JobID:            "job_high",
		Purpose:          "rename existing Knowledge note",
		RiskLevel:        model.RiskHigh,
		RequiresApproval: true,
		Summary:          "Rename existing Knowledge note to clearer title.",
		SourceRefs:       []string{"job_review", src},
		TargetPaths:      []string{src, dst},
		Operations: []model.VaultOperation{
			renameOp(t, "op_rename", src, dst),
		},
	}
}

func highRiskBulkRetagPlan(t *testing.T) model.VaultPlan {
	t.Helper()
	affected := []string{
		"Knowledge/topic/a.md",
		"Knowledge/topic/b.md",
		"Knowledge/topic/c.md",
		"Knowledge/topic/d.md",
		"Knowledge/topic/e.md",
	}
	return model.VaultPlan{
		ID:               "plan_retag",
		JobID:            "job_retag",
		Purpose:          "consolidate tag taxonomy across topic",
		RiskLevel:        model.RiskHigh,
		RequiresApproval: true,
		Summary:          "Bulk retag five Knowledge notes under topic/database.",
		SourceRefs:       []string{"job_review"},
		TargetPaths:      append([]string{}, affected...),
		Operations: []model.VaultOperation{
			{
				ID:         "op_retag",
				Type:       model.OperationBulkRetag,
				TargetPath: affected[0],
				PayloadJSON: mustJSON(t, model.BulkRetagPayload{
					AffectedPaths: affected,
					AddTags:       []string{"topic/database"},
					RemoveTags:    []string{"topic/db"},
				}),
				Reason:    "Migrate topic/db to topic/database.",
				RiskLevel: model.RiskHigh,
			},
		},
	}
}
