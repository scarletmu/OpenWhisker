package storage

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigrateUpgradePath_TriggerKindIndex reproduces the production upgrade
// failure: a database whose agent_runs table predates the trigger_kind /
// tool_trace_json columns (e.g. renamed from scheduler_runs by an earlier
// release). migrate() must add the columns and create the dependent index
// without failing — the index on trigger_kind has to run after the column is
// ensured, not inline with the table DDL.
func TestMigrateUpgradePath_TriggerKindIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Seed the legacy shape: agent_runs without the Phase 6 trace columns.
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`CREATE TABLE agent_runs (
  id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL,
  runtime_id TEXT NOT NULL,
  skill_dir TEXT NOT NULL,
  skill_path TEXT NOT NULL,
  status TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  result_json TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  outbox_message_id TEXT NOT NULL DEFAULT ''
);`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open/migrate failed on upgrade-path DB: %v", err)
	}
	defer store.db.Close()

	for _, want := range []string{"trigger_kind", "tool_trace_json"} {
		if !columnExists(t, store, "agent_runs", want) {
			t.Errorf("agent_runs missing column %q after migrate", want)
		}
	}

	var idx string
	if err := store.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='index' AND name='idx_agent_runs_trigger_kind'",
	).Scan(&idx); err != nil {
		t.Fatalf("idx_agent_runs_trigger_kind not created: %v", err)
	}
}

func columnExists(t *testing.T, s *Store, table, column string) bool {
	t.Helper()
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		if name == column {
			return true
		}
	}
	return false
}
