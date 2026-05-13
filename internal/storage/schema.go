package storage

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
  status TEXT NOT NULL,
  created_at TEXT NOT NULL,
  applied_at TEXT,
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
`)
	return err
}
