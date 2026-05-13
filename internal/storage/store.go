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
	diffJSON := ""
	if plan.Diff != nil {
		encoded, err := json.Marshal(plan.Diff)
		if err != nil {
			return err
		}
		diffJSON = string(encoded)
	}
	_, err = s.db.Exec(`
INSERT INTO vault_plans (
  id, job_id, purpose, risk_level, requires_approval, summary, source_refs_json,
  target_paths_json, operations_json, diff_json, status, created_at, prepared_at,
  approved_at, rejected_at, applied_at, rejected_reason, error
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.JobID, plan.Purpose, plan.RiskLevel, boolInt(plan.RequiresApproval),
		plan.Summary, string(sourceRefs), string(targetPaths), string(operations), diffJSON,
		plan.Status, formatTime(plan.CreatedAt), nullableTime(plan.PreparedAt),
		nullableTime(plan.ApprovedAt), nullableTime(plan.RejectedAt), nullableTime(plan.AppliedAt),
		plan.RejectedReason, plan.Error)
	return err
}

func (s *Store) UpdatePlanStatus(id, status string, appliedAt *time.Time) error {
	_, err := s.db.Exec(`
UPDATE vault_plans
SET status = ?, applied_at = ?
WHERE id = ?`, status, nullableTime(appliedAt), id)
	return err
}

func (s *Store) UpdatePlanPrepared(plan model.VaultPlan) error {
	operations, err := json.Marshal(plan.Operations)
	if err != nil {
		return err
	}
	diffJSON := ""
	if plan.Diff != nil {
		encoded, err := json.Marshal(plan.Diff)
		if err != nil {
			return err
		}
		diffJSON = string(encoded)
	}
	_, err = s.db.Exec(`
UPDATE vault_plans
SET operations_json = ?, diff_json = ?, status = ?, prepared_at = ?, error = ''
WHERE id = ?`,
		string(operations), diffJSON, plan.Status, nullableTime(plan.PreparedAt), plan.ID)
	return err
}

func (s *Store) ApprovePlan(id string, approvedAt time.Time) error {
	_, err := s.db.Exec(`
UPDATE vault_plans
SET status = ?, approved_at = ?, error = ''
WHERE id = ?`, model.PlanStatusApproved, formatTime(approvedAt), id)
	return err
}

func (s *Store) RejectPlan(id, reason string, rejectedAt time.Time) error {
	_, err := s.db.Exec(`
UPDATE vault_plans
SET status = ?, rejected_at = ?, rejected_reason = ?
WHERE id = ?`, model.PlanStatusRejected, formatTime(rejectedAt), reason, id)
	return err
}

func (s *Store) MarkPlanFailed(id, status, errText string) error {
	_, err := s.db.Exec(`
UPDATE vault_plans
SET status = ?, error = ?
WHERE id = ?`, status, errText, id)
	return err
}

func (s *Store) GetJob(id string) (model.WikiJob, error) {
	var job model.WikiJob
	var createdAt, updatedAt string
	err := s.db.QueryRow(`
SELECT id, type, status, source, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
WHERE id = ?`, id).Scan(&job.ID, &job.Type, &job.Status, &job.Source, &job.InputJSON,
		&job.ResultJSON, &job.Error, &createdAt, &updatedAt, &job.Attempts)
	if err != nil {
		return model.WikiJob{}, err
	}
	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	return job, nil
}

func (s *Store) LatestDoneIngestRawJob() (model.WikiJob, error) {
	var job model.WikiJob
	var createdAt, updatedAt string
	err := s.db.QueryRow(`
SELECT id, type, status, source, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
WHERE type = ? AND status = ?
ORDER BY created_at DESC
LIMIT 1`, model.JobTypeIngestRaw, model.JobStatusDone).Scan(&job.ID, &job.Type, &job.Status,
		&job.Source, &job.InputJSON, &job.ResultJSON, &job.Error, &createdAt, &updatedAt, &job.Attempts)
	if err != nil {
		return model.WikiJob{}, err
	}
	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	return job, nil
}

func (s *Store) ListDoneIngestRawJobsCreatedBetween(start, end time.Time, limit int) ([]model.WikiJob, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
SELECT id, type, status, source, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
WHERE type = ? AND status = ? AND created_at >= ? AND created_at < ?
ORDER BY created_at ASC
LIMIT ?`, model.JobTypeIngestRaw, model.JobStatusDone, formatTime(start), formatTime(end), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []model.WikiJob
	for rows.Next() {
		var job model.WikiJob
		var createdAt, updatedAt string
		if err := rows.Scan(&job.ID, &job.Type, &job.Status, &job.Source, &job.InputJSON,
			&job.ResultJSON, &job.Error, &createdAt, &updatedAt, &job.Attempts); err != nil {
			return nil, err
		}
		job.CreatedAt = parseTime(createdAt)
		job.UpdatedAt = parseTime(updatedAt)
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) GetPlan(id string) (model.VaultPlan, error) {
	return s.scanPlan(s.db.QueryRow(`
SELECT id, job_id, purpose, risk_level, requires_approval, summary, source_refs_json,
  target_paths_json, operations_json, diff_json, status, created_at, prepared_at,
  approved_at, rejected_at, applied_at, rejected_reason, error
FROM vault_plans
WHERE id = ?`, id))
}

func (s *Store) GetPlanByJobID(jobID string) (model.VaultPlan, error) {
	return s.scanPlan(s.db.QueryRow(`
SELECT id, job_id, purpose, risk_level, requires_approval, summary, source_refs_json,
  target_paths_json, operations_json, diff_json, status, created_at, prepared_at,
  approved_at, rejected_at, applied_at, rejected_reason, error
FROM vault_plans
WHERE job_id = ?
ORDER BY created_at DESC
LIMIT 1`, jobID))
}

type planScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanPlan(row planScanner) (model.VaultPlan, error) {
	var plan model.VaultPlan
	var requiresApproval int
	var sourceRefsJSON, targetPathsJSON, operationsJSON, diffJSON string
	var createdAt, preparedAt, approvedAt, rejectedAt, appliedAt sql.NullString
	err := row.Scan(&plan.ID, &plan.JobID, &plan.Purpose, &plan.RiskLevel, &requiresApproval,
		&plan.Summary, &sourceRefsJSON, &targetPathsJSON, &operationsJSON, &diffJSON,
		&plan.Status, &createdAt, &preparedAt, &approvedAt, &rejectedAt, &appliedAt,
		&plan.RejectedReason, &plan.Error)
	if err != nil {
		return model.VaultPlan{}, err
	}
	plan.RequiresApproval = requiresApproval != 0
	if err := json.Unmarshal([]byte(sourceRefsJSON), &plan.SourceRefs); err != nil {
		return model.VaultPlan{}, err
	}
	if err := json.Unmarshal([]byte(targetPathsJSON), &plan.TargetPaths); err != nil {
		return model.VaultPlan{}, err
	}
	if err := json.Unmarshal([]byte(operationsJSON), &plan.Operations); err != nil {
		return model.VaultPlan{}, err
	}
	if diffJSON != "" {
		var diff model.VaultDiff
		if err := json.Unmarshal([]byte(diffJSON), &diff); err != nil {
			return model.VaultPlan{}, err
		}
		plan.Diff = &diff
	}
	if createdAt.Valid {
		plan.CreatedAt = parseTime(createdAt.String)
	}
	plan.PreparedAt = parseNullableTime(preparedAt)
	plan.ApprovedAt = parseNullableTime(approvedAt)
	plan.RejectedAt = parseNullableTime(rejectedAt)
	plan.AppliedAt = parseNullableTime(appliedAt)
	return plan, nil
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

func (s *Store) ListPendingOutbox(limit int) ([]model.OutboxMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, job_id, kind, body, status, created_at
FROM outbox_messages
WHERE status = ?
ORDER BY created_at ASC
LIMIT ?`, model.OutboxStatusPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []model.OutboxMessage
	for rows.Next() {
		var msg model.OutboxMessage
		var createdAt string
		if err := rows.Scan(&msg.ID, &msg.JobID, &msg.Kind, &msg.Body, &msg.Status, &createdAt); err != nil {
			return nil, err
		}
		msg.CreatedAt = parseTime(createdAt)
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return messages, nil
}

func (s *Store) MarkOutboxDelivered(id string) error {
	_, err := s.db.Exec(`
UPDATE outbox_messages
SET status = ?
WHERE id = ?`, model.OutboxStatusDelivered, id)
	return err
}

func (s *Store) TryRecordAdapterEvent(adapter, eventID, requestJSON string, createdAt time.Time) (bool, error) {
	result, err := s.db.Exec(`
INSERT OR IGNORE INTO adapter_events (adapter, event_id, request_json, created_at)
VALUES (?, ?, ?, ?)`, adapter, eventID, requestJSON, formatTime(createdAt))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (s *Store) ListRecentJobs(limit int) ([]model.WikiJob, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`
SELECT id, type, status, source, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
ORDER BY created_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []model.WikiJob
	for rows.Next() {
		var job model.WikiJob
		var createdAt, updatedAt string
		if err := rows.Scan(&job.ID, &job.Type, &job.Status, &job.Source, &job.InputJSON,
			&job.ResultJSON, &job.Error, &createdAt, &updatedAt, &job.Attempts); err != nil {
			return nil, err
		}
		job.CreatedAt = parseTime(createdAt)
		job.UpdatedAt = parseTime(updatedAt)
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return jobs, nil
}

func (s *Store) AcquireLocks(paths []string, planID string, acquiredAt time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, path := range paths {
		if _, err := tx.Exec(`
INSERT INTO vault_locks (target_path, plan_id, acquired_at)
VALUES (?, ?, ?)`, path, planID, formatTime(acquiredAt)); err != nil {
			return fmt.Errorf("acquire lock for %s: %w", path, err)
		}
	}
	return tx.Commit()
}

func (s *Store) ReleaseLocks(planID string) error {
	_, err := s.db.Exec(`DELETE FROM vault_locks WHERE plan_id = ?`, planID)
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

func parseTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func parseNullableTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t := parseTime(value.String)
	return &t
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
