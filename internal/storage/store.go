package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/model"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) CreateJob(job model.WikiJob) error {
	_, err := s.db.Exec(`
INSERT INTO wiki_jobs (
  id, type, status, source, input_json, result_json, error, created_at, updated_at, attempts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Type, job.Status, job.Source, job.InputJSON, job.ResultJSON, job.Error,
		formatTime(job.CreatedAt), formatTime(job.UpdatedAt), job.Attempts)
	return err
}

func (s *Store) UpdateJobStatus(id, status, resultJSON, errText string) error {
	_, err := s.db.Exec(`
UPDATE wiki_jobs
SET status = ?, result_json = ?, error = ?, updated_at = ?
WHERE id = ?`,
		status, resultJSON, errText, formatTime(time.Now().UTC()), id)
	return err
}

func (s *Store) SavePlan(plan model.VaultPlan) error {
	sourceRefs, err := json.Marshal(plan.SourceRefs)
	if err != nil {
		return err
	}
	targetPaths, err := json.Marshal(plan.TargetPaths)
	if err != nil {
		return err
	}
	operations, err := json.Marshal(plan.Operations)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO vault_plans (
  id, job_id, purpose, risk_level, requires_approval, summary, source_refs_json,
  target_paths_json, operations_json, status, created_at, applied_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.JobID, plan.Purpose, plan.RiskLevel, boolInt(plan.RequiresApproval),
		plan.Summary, string(sourceRefs), string(targetPaths), string(operations), plan.Status,
		formatTime(plan.CreatedAt), nullableTime(plan.AppliedAt))
	return err
}

func (s *Store) UpdatePlanStatus(id, status string, appliedAt *time.Time) error {
	_, err := s.db.Exec(`
UPDATE vault_plans
SET status = ?, applied_at = ?
WHERE id = ?`, status, nullableTime(appliedAt), id)
	return err
}

func (s *Store) AppendOperationLog(log model.VaultOperationLog) error {
	_, err := s.db.Exec(`
INSERT INTO vault_operation_logs (
  id, plan_id, job_id, op_type, target_path, before_hash, after_hash, payload_json,
  result_json, reason, status, created_at, applied_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, log.PlanID, log.JobID, log.OpType, log.TargetPath, log.BeforeHash, log.AfterHash,
		log.PayloadJSON, log.ResultJSON, log.Reason, log.Status, formatTime(log.CreatedAt),
		nullableTime(log.AppliedAt))
	return err
}

func (s *Store) AddOutboxMessage(msg model.OutboxMessage) error {
	_, err := s.db.Exec(`
INSERT INTO outbox_messages (id, job_id, kind, body, status, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.JobID, msg.Kind, msg.Body, msg.Status, formatTime(msg.CreatedAt))
	return err
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
