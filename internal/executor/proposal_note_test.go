package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestRenderProposalNoteIncludesAllRequiredSections(t *testing.T) {
	plan := highRiskRenamePlanFixture(t)
	out, err := RenderProposalNote(plan, model.ProposalKindRename, time.Date(2026, 5, 19, 6, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RenderProposalNote() error = %v", err)
	}
	mustContain(t, out, []string{
		"type: proposal",
		"status: needs-review",
		"  plan_id: plan_high",
		"  job_id: job_high",
		"  - type/proposal",
		"  - status/needs-review",
		"  - proposal/rename",
		"## 来源",
		"## 目标结构",
		"## 影响路径",
		"## 建议操作",
		"## 待人工确认问题",
		"`Knowledge/topic/old-name.md`",
		"`Knowledge/topic/new-name.md`",
		"反向链接",
	})
}

func TestRenderProposalNoteRejectsEmptyPlanID(t *testing.T) {
	plan := highRiskRenamePlanFixture(t)
	plan.ID = ""
	if _, err := RenderProposalNote(plan, model.ProposalKindRename, time.Now().UTC()); err == nil {
		t.Fatal("RenderProposalNote() expected error for empty plan id")
	}
}

func TestApplyAsProposalWritesNoteAndProposedLog(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID:        "job_high",
		Type:      model.JobTypeOrganizeRaw,
		Status:    model.JobStatusApplying,
		Source:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	plan := highRiskRenamePlanFixture(t)
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}

	applied, err := NewDirectFS(vaultRoot, store).ApplyAsProposal(context.Background(), plan, "Raw/Agent-Proposals")
	if err != nil {
		t.Fatalf("ApplyAsProposal() error = %v", err)
	}

	expectedPath := ProposalNotePath(plan.ID, "Raw/Agent-Proposals")
	if applied.TargetPath != expectedPath {
		t.Fatalf("applied target = %q, want %q", applied.TargetPath, expectedPath)
	}

	content, err := os.ReadFile(filepath.Join(vaultRoot, expectedPath))
	if err != nil {
		t.Fatalf("read proposal note: %v", err)
	}
	mustContain(t, string(content), []string{
		"type: proposal",
		"  - proposal/rename",
		"## 影响路径",
	})

	// Knowledge/ must remain untouched.
	if _, err := os.Stat(filepath.Join(vaultRoot, "Knowledge")); !os.IsNotExist(err) {
		t.Fatalf("Knowledge/ unexpectedly created: err=%v", err)
	}

	logs, err := store.ListOperationLogsByPlan(plan.ID)
	if err != nil {
		t.Fatalf("ListOperationLogsByPlan: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}
	logEntry := logs[0]
	if logEntry.OpType != model.OperationWriteProposal {
		t.Fatalf("op_type = %q, want %q", logEntry.OpType, model.OperationWriteProposal)
	}
	if logEntry.Outcome != model.OperationOutcomeProposed {
		t.Fatalf("outcome = %q, want %q", logEntry.Outcome, model.OperationOutcomeProposed)
	}
	if logEntry.TargetPath != expectedPath {
		t.Fatalf("log target_path = %q, want %q", logEntry.TargetPath, expectedPath)
	}
}

func TestApplyAsProposalRejectsMediumRisk(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan := highRiskRenamePlanFixture(t)
	plan.RiskLevel = model.RiskMedium
	_, err = NewDirectFS(vaultRoot, store).ApplyAsProposal(context.Background(), plan, "Raw/Agent-Proposals")
	if err == nil || !strings.Contains(err.Error(), "high") {
		t.Fatalf("ApplyAsProposal() error = %v, want risk mismatch", err)
	}
}

func TestProposalNotePathStripsPlanPrefix(t *testing.T) {
	got := ProposalNotePath("plan_c896197d0cbd1234", "Raw/Agent-Proposals")
	want := "Raw/Agent-Proposals/proposal_c896197d0cbd.md"
	if got != want {
		t.Fatalf("ProposalNotePath = %q, want %q", got, want)
	}
}

func TestProposalNotePathHonorsBaseDirFromProfile(t *testing.T) {
	got := ProposalNotePath("plan_abc123def456ghi", "Meta/CustomProposals/")
	want := "Meta/CustomProposals/proposal_abc123def456.md"
	if got != want {
		t.Fatalf("ProposalNotePath with custom base = %q, want %q", got, want)
	}
}

func mustContain(t *testing.T, haystack string, needles []string) {
	t.Helper()
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			t.Fatalf("expected to contain %q, full content:\n%s", n, haystack)
		}
	}
}

func highRiskRenamePlanFixture(t *testing.T) model.VaultPlan {
	t.Helper()
	src := "Knowledge/topic/old-name.md"
	dst := "Knowledge/topic/new-name.md"
	payload, err := json.Marshal(model.RenameNotePayload{
		SourcePath:      src,
		DestinationPath: dst,
		Reason:          "clearer title",
	})
	if err != nil {
		t.Fatal(err)
	}
	return model.VaultPlan{
		ID:               "plan_high",
		JobID:            "job_high",
		Purpose:          "expander",
		RiskLevel:        model.RiskHigh,
		RequiresApproval: true,
		Summary:          "Rename Knowledge note to clearer title。后续段落仅作截断测试。",
		SourceRefs:       []string{"job_review", src},
		TargetPaths:      []string{src, dst},
		Operations: []model.VaultOperation{{
			ID:          "op_rename",
			Type:        model.OperationRenameNote,
			TargetPath:  src,
			PayloadJSON: string(payload),
			Reason:      "Rename to fix ambiguity.",
			RiskLevel:   model.RiskHigh,
		}},
		Status:    model.PlanStatusApproved,
		CreatedAt: time.Now().UTC(),
	}
}
