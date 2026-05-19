package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestAdapterServiceHandlesMatrixRawDedupeAndApproval(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{
		IntentRouterMode: "off",
	})

	raw, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-1",
		Sender:  "@user:example.test",
		Text:    "Phase4 matrix raw capture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw.Status != model.JobStatusDone || raw.JobID == "" {
		t.Fatalf("raw response = %+v, want done job", raw)
	}
	rawDiff, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-raw-diff",
		Sender:  "@user:example.test",
		Text:    "/diff " + raw.JobID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rawDiff.Status != "no_diff" || !strings.Contains(rawDiff.Body, "low-risk raw capture") {
		t.Fatalf("raw diff response = %+v, want friendly no_diff response", rawDiff)
	}
	duplicate, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-1",
		Sender:  "@user:example.test",
		Text:    "duplicate should not create another job",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatalf("duplicate response = %+v, want duplicate", duplicate)
	}

	organized, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-2",
		Sender:  "@user:example.test",
		Text:    "/organize last",
	})
	if err != nil {
		t.Fatal(err)
	}
	if organized.Status != model.PlanStatusAwaitingApproval || organized.PlanID == "" {
		t.Fatalf("organize response = %+v, want awaiting approval", organized)
	}
	diff, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-3",
		Sender:  "@user:example.test",
		Text:    "/diff " + organized.PlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"审批预览", "将写入的知识草稿", "将移动的 Raw", "审批动作", "创建笔记", "移动笔记"} {
		if !strings.Contains(diff.Body, want) {
			t.Fatalf("diff body = %q, want %q", diff.Body, want)
		}
	}
	if strings.Contains(diff.Body, "openwhisker_job_id") || strings.Contains(diff.Body, "before_hash") {
		t.Fatalf("diff body = %q, should hide low-level metadata", diff.Body)
	}
	approved, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$event-4",
		Sender:  "@user:example.test",
		Text:    "/approve " + organized.PlanID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != model.PlanStatusApplied {
		t.Fatalf("approve response = %+v, want applied", approved)
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, "wiki_jobs", 2)
	assertCount(t, db, "adapter_events", 5)
	pending, err := service.PullOutbox(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 {
		t.Fatalf("pending outbox count = %d, want 3", len(pending))
	}
	if err := service.MarkOutboxDelivered(pending[0].ID); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterServiceHandlesOrganizeToday(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := NewAdapterServiceWithOptions(store, vaultRoot, AdapterServiceOptions{
		IntentRouterMode: "off",
	})
	for _, text := range []string{"today raw one", "today raw two"} {
		if _, err := service.HandleText(context.Background(), AdapterRequest{
			Adapter: model.AdapterMatrix,
			EventID: "$raw-" + strings.ReplaceAll(text, " ", "-"),
			Sender:  "@user:example.test",
			Text:    text,
		}); err != nil {
			t.Fatal(err)
		}
	}
	organized, err := service.HandleText(context.Background(), AdapterRequest{
		Adapter: model.AdapterMatrix,
		EventID: "$organize-today",
		Sender:  "@user:example.test",
		Text:    "/organize today",
	})
	if err != nil {
		t.Fatal(err)
	}
	if organized.Status != model.PlanStatusAwaitingApproval || organized.PlanID == "" {
		t.Fatalf("organize today response = %+v, want awaiting approval", organized)
	}
	if !strings.Contains(organized.Body, "2 raw captures") {
		t.Fatalf("organize today body = %q, want raw count", organized.Body)
	}
}

func TestRenderAdapterDiffPrependsHighRiskWarning(t *testing.T) {
	src := "Knowledge/topic/old-name.md"
	dst := "Knowledge/topic/new-name.md"
	plan := model.VaultPlan{
		ID:         "plan_high",
		RiskLevel:  model.RiskHigh,
		Summary:    "Rename Knowledge note to clearer title.",
		SourceRefs: []string{"job_review", src},
		Operations: []model.VaultOperation{{
			ID:          "op_rename",
			Type:        model.OperationRenameNote,
			TargetPath:  src,
			PayloadJSON: `{"source_path":"` + src + `","destination_path":"` + dst + `"}`,
			RiskLevel:   model.RiskHigh,
		}},
	}
	diff := &model.VaultDiff{
		PlanID:  plan.ID,
		Summary: "⚠ 高风险计划：批准后只生成 proposal note，不写入 Knowledge。原摘要：" + plan.Summary,
		Entries: []model.DiffEntry{{
			OperationID: plan.ID,
			Type:        model.OperationWriteProposal,
			TargetPath:  "Meta/Agent-Proposals/proposal_high.md",
			Summary:     "write proposal note (proposal/rename)",
		}},
	}
	body := renderAdapterDiff(plan, diff)
	for _, needle := range []string{
		"⚠ 高风险计划",
		"## 影响路径",
		"rename: `" + src + "` → `" + dst + "`",
		"proposal 路径: Meta/Agent-Proposals/proposal_high.md",
		"批准 (写 proposal)",
	} {
		if !strings.Contains(body, needle) {
			t.Fatalf("renderAdapterDiff missing %q:\n%s", needle, body)
		}
	}
}
