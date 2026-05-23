package storage

import "fmt"

func (s *Store) migrate() error {
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
  acquired_at TEXT NOT NULL
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

CREATE TABLE IF NOT EXISTS scheduler_runs (
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
);

CREATE INDEX IF NOT EXISTS idx_scheduler_runs_schedule_status
ON scheduler_runs(schedule_id, status, started_at);

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
	} {
		if err := s.addColumnIfMissing(column.table, column.name, column.def); err != nil {
			return err
		}
	}
	return nil
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
