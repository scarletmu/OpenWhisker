package core

import (
	"context"
	"time"
)

type IntentClassifier interface {
	ClassifyIntent(context.Context, IntentClassifierRequest) (IntentClassifierResult, error)
}

type IntentClassifierRequest struct {
	Message          string                     `json:"message"`
	SourceKind       string                     `json:"source_kind"`
	ActiveBucket     *IntentActiveBucketSummary `json:"active_bucket,omitempty"`
	PendingPlanCount int                        `json:"pending_plan_count"`
	RecentEvents     []IntentRecentEventSummary `json:"recent_events,omitempty"`
}

type IntentActiveBucketSummary struct {
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	TopicHint   string    `json:"topic_hint"`
	Excerpt     string    `json:"excerpt"`
	AppendCount int       `json:"append_count"`
}

type IntentRecentEventSummary struct {
	Type string `json:"type"`
}

type IntentClassifierResult struct {
	Intent                string  `json:"intent"`
	Target                string  `json:"target"`
	CaptureAction         string  `json:"capture_action"`
	BucketRelation        string  `json:"bucket_relation"`
	PayloadText           string  `json:"payload_text"`
	AdditionalPayloadText string  `json:"additional_payload_text"`
	ConfidenceLabel       string  `json:"confidence_label"`
	Confidence            float64 `json:"confidence"`
	Reason                string  `json:"reason"`
}
