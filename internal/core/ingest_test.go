package core

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestIngestRawWritesNoteAndRecordsState(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	result, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "remember to review VaultPlan boundaries",
		Source: "test",
	})
	if err != nil {
		t.Fatalf("IngestRaw() error = %v", err)
	}
	if result.JobID == "" || result.PlanID == "" || result.AfterHash == "" {
		t.Fatalf("IngestRaw() returned incomplete result: %+v", result)
	}
	if !strings.HasPrefix(result.TargetPath, "Raw/Inbox/job_") {
		t.Fatalf("TargetPath = %q, want Raw/Inbox/job_*.md", result.TargetPath)
	}
	content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(result.TargetPath)))
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	for _, want := range []string{
		"openwhisker_job_id: " + result.JobID,
		"Source: test",
		"remember to review VaultPlan boundaries",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("note content missing %q:\n%s", want, got)
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, "wiki_jobs", 1)
	assertCount(t, db, "vault_plans", 1)
	assertCount(t, db, "vault_operation_logs", 1)
	assertCount(t, db, "outbox_messages", 1)
}

func assertCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}
