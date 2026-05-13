package model

import "time"

const (
	JobTypeIngestRaw = "ingest_raw"

	JobStatusPending  = "pending"
	JobStatusApplying = "applying"
	JobStatusDone     = "done"
	JobStatusFailed   = "failed"

	PlanStatusProposed = "proposed"
	PlanStatusApplied  = "applied"
	PlanStatusFailed   = "failed"

	RiskLow = "low"

	OperationCreateNote       = "create_note"
	OperationWriteAgentReport = "write_agent_report"

	OutboxKindResult = "result"
	OutboxKindError  = "error"

	OperationStatusApplied = "applied"
	OperationStatusFailed  = "failed"
)

type WikiJob struct {
	ID         string
	Type       string
	Status     string
	Source     string
	InputJSON  string
	ResultJSON string
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Attempts   int
}

type VaultPlan struct {
	ID               string
	JobID            string
	Purpose          string
	RiskLevel        string
	RequiresApproval bool
	Summary          string
	SourceRefs       []string
	TargetPaths      []string
	Operations       []VaultOperation
	Status           string
	CreatedAt        time.Time
	AppliedAt        *time.Time
}

type VaultOperation struct {
	ID          string
	Type        string
	TargetPath  string
	BeforeHash  string
	PayloadJSON string
	Reason      string
	RiskLevel   string
}

type CreateNotePayload struct {
	Content string `json:"content"`
}

type VaultApplyResult struct {
	AppliedOperations []AppliedOperation `json:"applied_operations"`
}

type AppliedOperation struct {
	OperationID string `json:"operation_id"`
	TargetPath  string `json:"target_path"`
	AfterHash   string `json:"after_hash"`
}

type VaultOperationLog struct {
	ID          string
	PlanID      string
	JobID       string
	OpType      string
	TargetPath  string
	BeforeHash  string
	AfterHash   string
	PayloadJSON string
	ResultJSON  string
	Reason      string
	Status      string
	CreatedAt   time.Time
	AppliedAt   *time.Time
}

type OutboxMessage struct {
	ID        string
	JobID     string
	Kind      string
	Body      string
	Status    string
	CreatedAt time.Time
}
