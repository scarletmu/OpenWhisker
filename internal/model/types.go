package model

import "time"

const (
	JobTypeIngestRaw       = "ingest_raw"
	JobTypeAppendRaw       = "append_raw"
	JobTypeOrganizeRaw     = "organize_raw"
	JobTypeExpandKnowledge = "expand_knowledge"

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
	PlanStatusProposalWritten  = "proposal_written"
	PlanStatusFailed           = "failed"
	PlanStatusConflict         = "conflict"

	RiskLow    = "low"
	RiskMedium = "medium"
	RiskHigh   = "high"

	OperationCreateNote       = "create_note"
	OperationAppendNote       = "append_note"
	OperationRewriteNote      = "rewrite_note"
	OperationMoveNote         = "move_note"
	OperationWriteAgentReport = "write_agent_report"
	OperationRenameNote       = "rename_note"
	OperationBulkRetag        = "bulk_retag"
	OperationBulkLinkRewrite  = "bulk_link_rewrite"
	OperationWriteProposal    = "write_proposal"

	ProposalKindSplit            = "split"
	ProposalKindMerge            = "merge"
	ProposalKindRename           = "rename"
	ProposalKindBulkRetag        = "bulk-retag"
	ProposalKindBulkLinkRewrite  = "bulk-link-rewrite"
	ProposalBulkAffectsThreshold = 5

	OperationOutcomeApplied  = "applied"
	OperationOutcomeProposed = "proposed"

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

	PendingClarificationStatusPending   = "pending"
	PendingClarificationStatusResolved  = "resolved"
	PendingClarificationStatusExpired   = "expired"
	PendingClarificationStatusCancelled = "cancelled"

	ClarificationQuestionBucketRelation = "bucket_relation"

	ClarificationActionRawAppend    = "raw_append"
	ClarificationActionRawCreate    = "raw_create"
	ClarificationActionCancel       = "cancel"
	PendingClarificationOriginalMax = 8 * 1024
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

type CandidateAction struct {
	Action string `json:"action"`
	Label  string `json:"label"`
}

type PendingClarification struct {
	ID                 string
	SourceKey          string
	QuestionType       string
	OriginalMessage    string
	OriginalReceivedAt time.Time
	CandidateActions   []CandidateAction
	Status             string
	CreatedAt          time.Time
	ExpiresAt          time.Time
	ResolvedAt         *time.Time
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

type RenameNotePayload struct {
	SourcePath      string `json:"source_path"`
	DestinationPath string `json:"destination_path"`
	Reason          string `json:"reason,omitempty"`
}

type BulkRetagPayload struct {
	AffectedPaths []string `json:"affected_paths"`
	AddTags       []string `json:"add_tags,omitempty"`
	RemoveTags    []string `json:"remove_tags,omitempty"`
	Reason        string   `json:"reason,omitempty"`
}

type BulkLinkRewritePayload struct {
	FromPath      string   `json:"from_path"`
	ToPath        string   `json:"to_path"`
	AffectedPaths []string `json:"affected_paths"`
	Reason        string   `json:"reason,omitempty"`
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
	Outcome     string
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
