package storage

import (
	"database/sql"
	"errors"
	"fmt"
)

func (s *Store) migrate() error {
	// Phase 6: rename scheduler_runs → agent_runs before CREATE IF NOT EXISTS
	// runs, so that pre-Phase 6 databases preserve historical rows under the
	// new table name. Order matters: CREATE TABLE IF NOT EXISTS agent_runs
	// would silently win on fresh databases and leave a stale scheduler_runs
	// behind on upgraded databases.
	if err := s.renameSchedulerRunsIfPresent(); err != nil {
		return err
	}
	_, err := s.db.Exec(`
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS wiki_jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  status TEXT NOT NULL,
  source TEXT NOT NULL,
  source_key TEXT NOT NULL DEFAULT '',
  input_json TEXT NOT NULL,
  result_json TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS vault_plans (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  purpose TEXT NOT NULL,
  risk_level TEXT NOT NULL,
  requires_approval INTEGER NOT NULL,
  summary TEXT NOT NULL,
  source_refs_json TEXT NOT NULL,
  target_paths_json TEXT NOT NULL,
  operations_json TEXT NOT NULL,
  diff_json TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  prepared_at TEXT,
  approved_at TEXT,
  rejected_at TEXT,
  applied_at TEXT,
  rejected_reason TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (job_id) REFERENCES wiki_jobs(id)
);

CREATE TABLE IF NOT EXISTS vault_operation_logs (
  id TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL,
  job_id TEXT NOT NULL,
  op_type TEXT NOT NULL,
  target_path TEXT NOT NULL,
  before_hash TEXT,
  after_hash TEXT,
  payload_json TEXT NOT NULL,
  result_json TEXT NOT NULL,
  reason TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  applied_at TEXT,
  FOREIGN KEY (plan_id) REFERENCES vault_plans(id),
  FOREIGN KEY (job_id) REFERENCES wiki_jobs(id)
);

CREATE TABLE IF NOT EXISTS outbox_messages (
  id TEXT PRIMARY KEY,
  job_id TEXT NOT NULL,
  actor TEXT NOT NULL DEFAULT 'knowledge',
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  FOREIGN KEY (job_id) REFERENCES wiki_jobs(id)
);

CREATE TABLE IF NOT EXISTS adapter_events (
  adapter TEXT NOT NULL,
  event_id TEXT NOT NULL,
  request_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (adapter, event_id)
);

CREATE TABLE IF NOT EXISTS vault_locks (
  target_path TEXT PRIMARY KEY,
  plan_id TEXT NOT NULL,
  acquired_at TEXT NOT NULL,
  expires_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS capture_buckets (
  bucket_id TEXT PRIMARY KEY,
  source_key TEXT NOT NULL,
  raw_job_id TEXT NOT NULL,
  raw_plan_id TEXT NOT NULL,
  raw_path TEXT NOT NULL,
  status TEXT NOT NULL,
  topic_hint TEXT NOT NULL DEFAULT '',
  excerpt TEXT NOT NULL DEFAULT '',
  append_count INTEGER NOT NULL DEFAULT 0,
  raw_hash_after_last_append TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  closed_at TEXT,
  close_reason TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_capture_buckets_source_status
ON capture_buckets(source_key, status, updated_at);

CREATE TABLE IF NOT EXISTS pending_clarifications (
  clarification_id TEXT PRIMARY KEY,
  source_key TEXT NOT NULL,
  question_type TEXT NOT NULL,
  original_message TEXT NOT NULL,
  original_received_at TEXT NOT NULL,
  candidate_actions_json TEXT NOT NULL,
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  resolved_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_pending_clarifications_source_status
ON pending_clarifications(source_key, status, created_at);

CREATE TABLE IF NOT EXISTS scheduler_runtime (
  id TEXT PRIMARY KEY,
  schedule_id TEXT NOT NULL UNIQUE,
  registry_path TEXT NOT NULL,
  registry_hash TEXT NOT NULL,
  skill_dir TEXT NOT NULL,
  skill_path TEXT NOT NULL,
  last_run_at TEXT,
  next_run_at TEXT,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_scheduler_runtime_next_run
ON scheduler_runtime(next_run_at);

CREATE TABLE IF NOT EXISTS agent_runs (
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
  outbox_message_id TEXT NOT NULL DEFAULT '',
  trigger_kind TEXT NOT NULL DEFAULT 'scheduler',
  tool_trace_json TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_agent_runs_schedule_status
ON agent_runs(schedule_id, status, started_at);

CREATE INDEX IF NOT EXISTS idx_agent_runs_trigger_kind
ON agent_runs(trigger_kind, started_at);

CREATE TABLE IF NOT EXISTS scheduler_overrides (
  schedule_id TEXT PRIMARY KEY,
  enabled_override INTEGER NOT NULL,
  updated_at TEXT NOT NULL
);
`)
	if err != nil {
		return err
	}
	for _, column := range []struct {
		table string
		name  string
		def   string
	}{
		{"vault_plans", "diff_json", "TEXT NOT NULL DEFAULT ''"},
		{"vault_plans", "prepared_at", "TEXT"},
		{"vault_plans", "approved_at", "TEXT"},
		{"vault_plans", "rejected_at", "TEXT"},
		{"vault_plans", "rejected_reason", "TEXT NOT NULL DEFAULT ''"},
		{"vault_plans", "error", "TEXT NOT NULL DEFAULT ''"},
		{"wiki_jobs", "source_key", "TEXT NOT NULL DEFAULT ''"},
		{"vault_operation_logs", "outcome", "TEXT NOT NULL DEFAULT 'applied'"},
		{"outbox_messages", "actor", "TEXT NOT NULL DEFAULT 'knowledge'"},
		{"vault_locks", "expires_at", "TEXT NOT NULL DEFAULT ''"},
		// Phase 6: agent_runs added these columns. On a brand-new database
		// the CREATE TABLE above already includes them; on a renamed-from-
		// scheduler_runs database we need to ALTER them in.
		{"agent_runs", "trigger_kind", "TEXT NOT NULL DEFAULT 'scheduler'"},
		{"agent_runs", "tool_trace_json", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing(column.table, column.name, column.def); err != nil {
			return err
		}
	}
	return nil
}

// renameSchedulerRunsIfPresent performs the Phase 6 table rename in a single
// idempotent step. No down migration: a Phase 5 binary started against a
// Phase 6 database will not find scheduler_runs and will error fast, which is
// preferable to silently losing trace columns.
func (s *Store) renameSchedulerRunsIfPresent() error {
	hasOld, err := s.tableExists("scheduler_runs")
	if err != nil {
		return err
	}
	if !hasOld {
		return nil
	}
	hasNew, err := s.tableExists("agent_runs")
	if err != nil {
		return err
	}
	if hasNew {
		// Both present means a prior migration partially completed. Refuse to
		// guess: surface the situation so the operator can intervene.
		return fmt.Errorf("storage migration: both scheduler_runs and agent_runs exist; manual reconciliation required")
	}
	if _, err := s.db.Exec("ALTER TABLE scheduler_runs RENAME TO agent_runs"); err != nil {
		return fmt.Errorf("rename scheduler_runs to agent_runs: %w", err)
	}
	// Drop the old index name; recreate under the new name in the main DDL.
	if _, err := s.db.Exec("DROP INDEX IF EXISTS idx_scheduler_runs_schedule_status"); err != nil {
		return fmt.Errorf("drop legacy scheduler_runs index: %w", err)
	}
	return nil
}

func (s *Store) tableExists(name string) (bool, error) {
	var found string
	err := s.db.QueryRow(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?",
		name,
	).Scan(&found)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return found == name, nil
}

func (s *Store) addColumnIfMissing(table, name, def string) error {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var columnName, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, name, def))
	return err
}
