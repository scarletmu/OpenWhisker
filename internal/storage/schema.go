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
