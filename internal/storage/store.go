package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
  id, type, status, source, source_key, input_json, result_json, error, created_at, updated_at, attempts
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID, job.Type, job.Status, job.Source, job.SourceKey, job.InputJSON, job.ResultJSON, job.Error,
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
SELECT id, type, status, source, source_key, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
WHERE id = ?`, id).Scan(&job.ID, &job.Type, &job.Status, &job.Source, &job.SourceKey, &job.InputJSON,
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
SELECT id, type, status, source, source_key, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
WHERE type = ? AND status = ?
ORDER BY created_at DESC
LIMIT 1`, model.JobTypeIngestRaw, model.JobStatusDone).Scan(&job.ID, &job.Type, &job.Status,
		&job.Source, &job.SourceKey, &job.InputJSON, &job.ResultJSON, &job.Error, &createdAt, &updatedAt, &job.Attempts)
	if err != nil {
		return model.WikiJob{}, err
	}
	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	return job, nil
}

func (s *Store) LatestDoneIngestRawJobBySourceKey(sourceKey string) (model.WikiJob, error) {
	var job model.WikiJob
	var createdAt, updatedAt string
	err := s.db.QueryRow(`
SELECT id, type, status, source, source_key, input_json, result_json, error, created_at, updated_at, attempts
FROM wiki_jobs
WHERE type = ? AND status = ? AND source_key = ?
ORDER BY created_at DESC
LIMIT 1`, model.JobTypeIngestRaw, model.JobStatusDone, sourceKey).Scan(&job.ID, &job.Type, &job.Status,
		&job.Source, &job.SourceKey, &job.InputJSON, &job.ResultJSON, &job.Error, &createdAt, &updatedAt, &job.Attempts)
	if err != nil {
		return model.WikiJob{}, err
	}
	job.CreatedAt = parseTime(createdAt)
	job.UpdatedAt = parseTime(updatedAt)
	return job, nil
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

func (s *Store) ListOperationLogsByPlan(planID string) ([]model.VaultOperationLog, error) {
	rows, err := s.db.Query(`
SELECT id, plan_id, job_id, op_type, target_path, before_hash, after_hash, payload_json,
       result_json, reason, status, outcome, created_at, applied_at
FROM vault_operation_logs
WHERE plan_id = ?
ORDER BY created_at ASC`, planID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []model.VaultOperationLog
	for rows.Next() {
		var log model.VaultOperationLog
		var createdAt string
		var appliedAt sql.NullString
		if err := rows.Scan(
			&log.ID, &log.PlanID, &log.JobID, &log.OpType, &log.TargetPath,
			&log.BeforeHash, &log.AfterHash, &log.PayloadJSON, &log.ResultJSON,
			&log.Reason, &log.Status, &log.Outcome, &createdAt, &appliedAt,
		); err != nil {
			return nil, err
		}
		log.CreatedAt = parseTime(createdAt)
		log.AppliedAt = parseNullableTime(appliedAt)
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

func (s *Store) AppendOperationLog(log model.VaultOperationLog) error {
	outcome := log.Outcome
	if outcome == "" {
		outcome = model.OperationOutcomeApplied
	}
	_, err := s.db.Exec(`
INSERT INTO vault_operation_logs (
  id, plan_id, job_id, op_type, target_path, before_hash, after_hash, payload_json,
  result_json, reason, status, outcome, created_at, applied_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.ID, log.PlanID, log.JobID, log.OpType, log.TargetPath, log.BeforeHash, log.AfterHash,
		log.PayloadJSON, log.ResultJSON, log.Reason, log.Status, outcome, formatTime(log.CreatedAt),
		nullableTime(log.AppliedAt))
	return err
}

func (s *Store) AddOutboxMessage(msg model.OutboxMessage) error {
	actor := strings.TrimSpace(msg.Actor)
	if actor == "" {
		actor = model.OutboxActorKnowledge
	}
	_, err := s.db.Exec(`
INSERT INTO outbox_messages (id, job_id, actor, kind, body, status, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		msg.ID, msg.JobID, actor, msg.Kind, msg.Body, msg.Status, formatTime(msg.CreatedAt))
	return err
}

func (s *Store) ListPendingOutbox(limit int) ([]model.OutboxMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, job_id, actor, kind, body, status, created_at
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
		if err := rows.Scan(&msg.ID, &msg.JobID, &msg.Actor, &msg.Kind, &msg.Body, &msg.Status, &createdAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(msg.Actor) == "" {
			msg.Actor = model.OutboxActorKnowledge
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
SELECT id, type, status, source, source_key, input_json, result_json, error, created_at, updated_at, attempts
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
		if err := rows.Scan(&job.ID, &job.Type, &job.Status, &job.Source, &job.SourceKey, &job.InputJSON,
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

func (s *Store) SaveCaptureBucket(bucket model.CaptureBucket) error {
	_, err := s.db.Exec(`
INSERT INTO capture_buckets (
  bucket_id, source_key, raw_job_id, raw_plan_id, raw_path, status, topic_hint,
  excerpt, append_count, raw_hash_after_last_append, started_at, updated_at,
  expires_at, closed_at, close_reason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(bucket_id) DO UPDATE SET
  source_key = excluded.source_key,
  raw_job_id = excluded.raw_job_id,
  raw_plan_id = excluded.raw_plan_id,
  raw_path = excluded.raw_path,
  status = excluded.status,
  topic_hint = excluded.topic_hint,
  excerpt = excluded.excerpt,
  append_count = excluded.append_count,
  raw_hash_after_last_append = excluded.raw_hash_after_last_append,
  updated_at = excluded.updated_at,
  expires_at = excluded.expires_at,
  closed_at = excluded.closed_at,
  close_reason = excluded.close_reason`,
		bucket.ID, bucket.SourceKey, bucket.RawJobID, bucket.RawPlanID, bucket.RawPath,
		bucket.Status, bucket.TopicHint, bucket.Excerpt, bucket.AppendCount,
		bucket.RawHashAfterLastAppend, formatTime(bucket.StartedAt), formatTime(bucket.UpdatedAt),
		formatTime(bucket.ExpiresAt), nullableTime(bucket.ClosedAt), bucket.CloseReason)
	return err
}

func (s *Store) GetCaptureBucket(sourceKey, bucketID string) (model.CaptureBucket, error) {
	return s.scanCaptureBucket(s.db.QueryRow(`
SELECT bucket_id, source_key, raw_job_id, raw_plan_id, raw_path, status, topic_hint,
  excerpt, append_count, raw_hash_after_last_append, started_at, updated_at,
  expires_at, closed_at, close_reason
FROM capture_buckets
WHERE bucket_id = ? AND source_key = ?`, bucketID, sourceKey))
}

func (s *Store) ActiveCaptureBucket(sourceKey string) (model.CaptureBucket, error) {
	return s.scanCaptureBucket(s.db.QueryRow(`
SELECT bucket_id, source_key, raw_job_id, raw_plan_id, raw_path, status, topic_hint,
  excerpt, append_count, raw_hash_after_last_append, started_at, updated_at,
  expires_at, closed_at, close_reason
FROM capture_buckets
WHERE source_key = ? AND status = ?
ORDER BY updated_at DESC
LIMIT 1`, sourceKey, model.CaptureBucketStatusActive))
}

func (s *Store) ListAwaitingApprovalPlansBySourceKey(sourceKey string) ([]model.VaultPlan, error) {
	rows, err := s.db.Query(`
SELECT p.id, p.job_id, p.purpose, p.risk_level, p.requires_approval, p.summary,
  p.source_refs_json, p.target_paths_json, p.operations_json, p.diff_json,
  p.status, p.created_at, p.prepared_at, p.approved_at, p.rejected_at,
  p.applied_at, p.rejected_reason, p.error
FROM vault_plans p
JOIN wiki_jobs j ON j.id = p.job_id
WHERE p.status = ? AND j.source_key = ?
ORDER BY p.created_at DESC
LIMIT 20`, model.PlanStatusAwaitingApproval, sourceKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []model.VaultPlan
	for rows.Next() {
		plan, err := s.scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return plans, nil
}

func (s *Store) SavePendingClarification(c model.PendingClarification) error {
	candidates, err := json.Marshal(c.CandidateActions)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
INSERT INTO pending_clarifications (
  clarification_id, source_key, question_type, original_message, original_received_at,
  candidate_actions_json, status, created_at, expires_at, resolved_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.SourceKey, c.QuestionType, c.OriginalMessage, formatTime(c.OriginalReceivedAt),
		string(candidates), c.Status, formatTime(c.CreatedAt), formatTime(c.ExpiresAt),
		nullableTime(c.ResolvedAt))
	return err
}

func (s *Store) ActivePendingClarification(sourceKey string, now time.Time) (model.PendingClarification, error) {
	return s.scanPendingClarification(s.db.QueryRow(`
SELECT clarification_id, source_key, question_type, original_message, original_received_at,
  candidate_actions_json, status, created_at, expires_at, resolved_at
FROM pending_clarifications
WHERE source_key = ? AND status = ? AND expires_at > ?
ORDER BY created_at DESC
LIMIT 1`, sourceKey, model.PendingClarificationStatusPending, formatTime(now)))
}

func (s *Store) UpdatePendingClarificationStatus(id, status string, resolvedAt time.Time) error {
	_, err := s.db.Exec(`
UPDATE pending_clarifications
SET status = ?, resolved_at = ?
WHERE clarification_id = ?`, status, formatTime(resolvedAt), id)
	return err
}

func (s *Store) ExpirePendingClarifications(sourceKey string, now time.Time) (int64, error) {
	result, err := s.db.Exec(`
UPDATE pending_clarifications
SET status = ?, resolved_at = ?
WHERE source_key = ? AND status = ? AND expires_at <= ?`,
		model.PendingClarificationStatusExpired, formatTime(now), sourceKey,
		model.PendingClarificationStatusPending, formatTime(now))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) scanPendingClarification(row planScanner) (model.PendingClarification, error) {
	var c model.PendingClarification
	var receivedAt, createdAt, expiresAt string
	var resolvedAt sql.NullString
	var candidatesJSON string
	err := row.Scan(&c.ID, &c.SourceKey, &c.QuestionType, &c.OriginalMessage, &receivedAt,
		&candidatesJSON, &c.Status, &createdAt, &expiresAt, &resolvedAt)
	if err != nil {
		return model.PendingClarification{}, err
	}
	if candidatesJSON != "" {
		if err := json.Unmarshal([]byte(candidatesJSON), &c.CandidateActions); err != nil {
			return model.PendingClarification{}, err
		}
	}
	c.OriginalReceivedAt = parseTime(receivedAt)
	c.CreatedAt = parseTime(createdAt)
	c.ExpiresAt = parseTime(expiresAt)
	c.ResolvedAt = parseNullableTime(resolvedAt)
	return c, nil
}

func (s *Store) scanCaptureBucket(row planScanner) (model.CaptureBucket, error) {
	var bucket model.CaptureBucket
	var startedAt, updatedAt, expiresAt string
	var closedAt sql.NullString
	err := row.Scan(&bucket.ID, &bucket.SourceKey, &bucket.RawJobID, &bucket.RawPlanID,
		&bucket.RawPath, &bucket.Status, &bucket.TopicHint, &bucket.Excerpt,
		&bucket.AppendCount, &bucket.RawHashAfterLastAppend, &startedAt, &updatedAt,
		&expiresAt, &closedAt, &bucket.CloseReason)
	if err != nil {
		return model.CaptureBucket{}, err
	}
	bucket.StartedAt = parseTime(startedAt)
	bucket.UpdatedAt = parseTime(updatedAt)
	bucket.ExpiresAt = parseTime(expiresAt)
	bucket.ClosedAt = parseNullableTime(closedAt)
	return bucket, nil
}

func (s *Store) SaveSchedulerRuntime(runtime model.SchedulerRuntime) error {
	_, err := s.db.Exec(`
INSERT INTO scheduler_runtime (
  id, schedule_id, registry_path, registry_hash, skill_dir, skill_path,
  last_run_at, next_run_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(schedule_id) DO UPDATE SET
  registry_path = excluded.registry_path,
  registry_hash = excluded.registry_hash,
  skill_dir = excluded.skill_dir,
  skill_path = excluded.skill_path,
  last_run_at = excluded.last_run_at,
  next_run_at = excluded.next_run_at,
  updated_at = excluded.updated_at`,
		runtime.ID, runtime.ScheduleID, runtime.RegistryPath, runtime.RegistryHash,
		runtime.SkillDir, runtime.SkillPath, nullableTime(runtime.LastRunAt),
		nullableTime(runtime.NextRunAt), formatTime(runtime.UpdatedAt))
	return err
}

func (s *Store) GetSchedulerRuntime(scheduleID string) (model.SchedulerRuntime, error) {
	return s.scanSchedulerRuntime(s.db.QueryRow(`
SELECT id, schedule_id, registry_path, registry_hash, skill_dir, skill_path,
  last_run_at, next_run_at, updated_at
FROM scheduler_runtime
WHERE schedule_id = ?`, scheduleID))
}

func (s *Store) HasRunningSchedulerRun(scheduleID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`
SELECT COUNT(*)
FROM scheduler_runs
WHERE schedule_id = ? AND status = ?`, scheduleID, model.SchedulerRunStatusRunning).Scan(&count)
	return count > 0, err
}

func (s *Store) CreateSchedulerRun(run model.SchedulerRun) error {
	_, err := s.db.Exec(`
INSERT INTO scheduler_runs (
  id, schedule_id, runtime_id, skill_dir, skill_path, status, started_at,
  finished_at, result_json, error, outbox_message_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.ScheduleID, run.RuntimeID, run.SkillDir, run.SkillPath, run.Status,
		formatTime(run.StartedAt), nullableTime(run.FinishedAt), run.ResultJSON,
		run.Error, run.OutboxMessageID)
	return err
}

func (s *Store) FinishSchedulerRun(id, status, resultJSON, errText, outboxMessageID string, finishedAt time.Time) error {
	_, err := s.db.Exec(`
UPDATE scheduler_runs
SET status = ?, finished_at = ?, result_json = ?, error = ?, outbox_message_id = ?
WHERE id = ?`, status, formatTime(finishedAt), resultJSON, errText, outboxMessageID, id)
	return err
}

func (s *Store) ListSchedulerRuns(scheduleID string, limit int) ([]model.SchedulerRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, schedule_id, runtime_id, skill_dir, skill_path, status, started_at,
  finished_at, result_json, error, outbox_message_id
FROM scheduler_runs
WHERE schedule_id = ?
ORDER BY started_at DESC
LIMIT ?`, scheduleID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []model.SchedulerRun
	for rows.Next() {
		run, err := scanSchedulerRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) ListRecentSchedulerRuns(limit int) ([]model.SchedulerRun, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, schedule_id, runtime_id, skill_dir, skill_path, status, started_at,
  finished_at, result_json, error, outbox_message_id
FROM scheduler_runs
ORDER BY started_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []model.SchedulerRun
	for rows.Next() {
		run, err := scanSchedulerRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runs, nil
}

func (s *Store) ListSchedulerRuntimes(limit int) ([]model.SchedulerRuntime, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.Query(`
SELECT id, schedule_id, registry_path, registry_hash, skill_dir, skill_path,
  last_run_at, next_run_at, updated_at
FROM scheduler_runtime
ORDER BY next_run_at ASC, updated_at DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runtimes []model.SchedulerRuntime
	for rows.Next() {
		runtime, err := s.scanSchedulerRuntime(rows)
		if err != nil {
			return nil, err
		}
		runtimes = append(runtimes, runtime)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return runtimes, nil
}

func (s *Store) GetSchedulerRun(id string) (model.SchedulerRun, error) {
	return scanSchedulerRun(s.db.QueryRow(`
SELECT id, schedule_id, runtime_id, skill_dir, skill_path, status, started_at,
  finished_at, result_json, error, outbox_message_id
FROM scheduler_runs
WHERE id = ?`, id))
}

func (s *Store) SaveSchedulerEnabledOverride(scheduleID string, enabled bool, updatedAt time.Time) error {
	_, err := s.db.Exec(`
INSERT INTO scheduler_overrides (schedule_id, enabled_override, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(schedule_id) DO UPDATE SET
  enabled_override = excluded.enabled_override,
  updated_at = excluded.updated_at`, scheduleID, boolInt(enabled), formatTime(updatedAt))
	return err
}

func (s *Store) GetSchedulerEnabledOverrides() (map[string]bool, error) {
	rows, err := s.db.Query(`
SELECT schedule_id, enabled_override
FROM scheduler_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overrides := map[string]bool{}
	for rows.Next() {
		var scheduleID string
		var enabled int
		if err := rows.Scan(&scheduleID, &enabled); err != nil {
			return nil, err
		}
		overrides[scheduleID] = enabled != 0
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return overrides, nil
}

func (s *Store) scanSchedulerRuntime(row planScanner) (model.SchedulerRuntime, error) {
	var runtime model.SchedulerRuntime
	var lastRunAt, nextRunAt sql.NullString
	var updatedAt string
	err := row.Scan(&runtime.ID, &runtime.ScheduleID, &runtime.RegistryPath,
		&runtime.RegistryHash, &runtime.SkillDir, &runtime.SkillPath,
		&lastRunAt, &nextRunAt, &updatedAt)
	if err != nil {
		return model.SchedulerRuntime{}, err
	}
	runtime.LastRunAt = parseNullableTime(lastRunAt)
	runtime.NextRunAt = parseNullableTime(nextRunAt)
	runtime.UpdatedAt = parseTime(updatedAt)
	return runtime, nil
}

func scanSchedulerRun(row planScanner) (model.SchedulerRun, error) {
	var run model.SchedulerRun
	var startedAt string
	var finishedAt sql.NullString
	err := row.Scan(&run.ID, &run.ScheduleID, &run.RuntimeID, &run.SkillDir,
		&run.SkillPath, &run.Status, &startedAt, &finishedAt, &run.ResultJSON,
		&run.Error, &run.OutboxMessageID)
	if err != nil {
		return model.SchedulerRun{}, err
	}
	run.StartedAt = parseTime(startedAt)
	run.FinishedAt = parseNullableTime(finishedAt)
	return run, nil
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
