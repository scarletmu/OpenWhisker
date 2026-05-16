package model

import "time"

const (
	JobTypeIngestRaw        = "ingest_raw"
	JobTypeAppendRaw        = "append_raw"
	JobTypeOrganizeRaw      = "organize_raw"
	JobTypeOrganizeRawToday = "organize_raw_today"

	JobStatusPending          = "pending"
	JobStatusAwaitingApproval = "awaiting_approval"
	JobStatusApplying         = "applying"
	JobStatusDone             = "done"
	JobStatusFailed           = "failed"
	JobStatusRejected         = "rejected"

	PlanStatusProposed         = "proposed"
	PlanStatusAwaitingApproval = "awaiting_approval"
	PlanStatusApproved         = "approved"
	PlanStatusRejected         = "rejected"
	PlanStatusApplying         = "applying"
	PlanStatusApplied          = "applied"
	PlanStatusFailed           = "failed"
	PlanStatusConflict         = "conflict"

	RiskLow    = "low"
	RiskMedium = "medium"

	OperationCreateNote       = "create_note"
	OperationAppendNote       = "append_note"
	OperationRewriteNote      = "rewrite_note"
	OperationMoveNote         = "move_note"
	OperationWriteAgentReport = "write_agent_report"

	OutboxKindResult   = "result"
	OutboxKindError    = "error"
	OutboxKindApproval = "approval"
	OutboxKindDiff     = "diff"
	OutboxKindConflict = "conflict"
	OutboxKindRejected = "rejected"

	OutboxStatusPending   = "pending"
	OutboxStatusDelivered = "delivered"

	AdapterMatrix = "matrix"

	OperationStatusApplied = "applied"
	OperationStatusFailed  = "failed"

	SyncModeAuto = "auto"
	SyncModeOff  = "off"
	SyncModeOn   = "on"

	SyncBackendHeadless = "headless"

	SyncPhaseStatus = "status"
	SyncPhaseManual = "manual"
	SyncPhaseBefore = "before_apply"
	SyncPhaseAfter  = "after_apply"

	CaptureBucketStatusActive       = "active"
	CaptureBucketStatusClosed       = "closed"
	CaptureBucketStatusOrganized    = "organized"
	CaptureBucketStatusHashMismatch = "hash_mismatch"
	CaptureBucketStatusExpired      = "expired"
)

type WikiJob struct {
	ID         string
	Type       string
	Status     string
	Source     string
	SourceKey  string
	InputJSON  string
	ResultJSON string
	Error      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	Attempts   int
}

type CaptureBucket struct {
	ID                     string
	SourceKey              string
	RawJobID               string
	RawPlanID              string
	RawPath                string
	Status                 string
	TopicHint              string
	Excerpt                string
	AppendCount            int
	RawHashAfterLastAppend string
	StartedAt              time.Time
	UpdatedAt              time.Time
	ExpiresAt              time.Time
	ClosedAt               *time.Time
	CloseReason            string
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
	Diff             *VaultDiff
	Status           string
	CreatedAt        time.Time
	PreparedAt       *time.Time
	ApprovedAt       *time.Time
	RejectedAt       *time.Time
	AppliedAt        *time.Time
	RejectedReason   string
	Error            string
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

type AppendNotePayload struct {
	Content string `json:"content"`
}

type MoveNotePayload struct {
	DestinationPath string `json:"destination_path"`
	ProcessingNote  string `json:"processing_note,omitempty"`
}

type VaultDiff struct {
	PlanID  string      `json:"plan_id"`
	Summary string      `json:"summary"`
	Entries []DiffEntry `json:"entries"`
}

type DiffEntry struct {
	OperationID string `json:"operation_id"`
	Type        string `json:"type"`
	TargetPath  string `json:"target_path"`
	BeforeHash  string `json:"before_hash"`
	AfterHash   string `json:"after_hash,omitempty"`
	Summary     string `json:"summary"`
	Preview     string `json:"preview"`
}

type VaultApplyResult struct {
	AppliedOperations []AppliedOperation `json:"applied_operations"`
	SyncBefore        *SyncResult        `json:"sync_before,omitempty"`
	SyncAfter         *SyncResult        `json:"sync_after,omitempty"`
}

type SyncResult struct {
	Mode    string `json:"mode"`
	Backend string `json:"backend,omitempty"`
	Phase   string `json:"phase"`
	OK      bool   `json:"ok"`
	Command string `json:"command,omitempty"`
	Output  string `json:"output,omitempty"`
	Warning string `json:"warning,omitempty"`
	Error   string `json:"error,omitempty"`
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
