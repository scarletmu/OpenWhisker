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

func TestDirectFSMoveNoteAppendsProcessingNote(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	sourcePath := filepath.Join(vaultRoot, "Raw", "Inbox", "job_raw.md")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("# Raw\n\ncontent"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID:        "job_test",
		Type:      model.JobTypeOrganizeRaw,
		Status:    model.JobStatusApplying,
		Source:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(model.MoveNotePayload{
		DestinationPath: "Raw/Processed/job_raw.md",
		ProcessingNote:  "\n## OpenWhisker Processing\n\n- Raw path after approval: Raw/Processed/job_raw.md\n- Output: Knowledge/Drafts/job_raw.md\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := model.VaultPlan{
		ID:          "plan_test",
		JobID:       "job_test",
		TargetPaths: []string{"Raw/Inbox/job_raw.md", "Raw/Processed/job_raw.md"},
		Operations: []model.VaultOperation{{
			ID:          "op_move",
			Type:        model.OperationMoveNote,
			TargetPath:  "Raw/Inbox/job_raw.md",
			PayloadJSON: string(payload),
			Reason:      "move with processing note",
		}},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}

	prepared, err := NewDirectFS(vaultRoot, store).Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Diff == nil || len(prepared.Diff.Entries) != 1 {
		t.Fatalf("diff = %+v, want one entry", prepared.Diff)
	}
	result, err := NewDirectFS(vaultRoot, store).Apply(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AppliedOperations) != 1 {
		t.Fatalf("applied operations = %d, want 1", len(result.AppliedOperations))
	}
	if result.AppliedOperations[0].AfterHash != prepared.Diff.Entries[0].AfterHash {
		t.Fatalf("after hash = %q, want prepared hash %q", result.AppliedOperations[0].AfterHash, prepared.Diff.Entries[0].AfterHash)
	}
	content, err := os.ReadFile(filepath.Join(vaultRoot, "Raw", "Processed", "job_raw.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"# Raw", "## OpenWhisker Processing", "Knowledge/Drafts/job_raw.md"} {
		if !strings.Contains(string(content), needle) {
			t.Fatalf("processed content missing %q:\n%s", needle, string(content))
		}
	}
}
