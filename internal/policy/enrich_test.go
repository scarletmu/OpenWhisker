package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/scarletmu/openwhisker/internal/model"
)

type fakeVocab struct {
	tags map[string]bool
}

func (f fakeVocab) HasTag(t string) bool { return f.tags[strings.ToLower(strings.TrimSpace(t))] }

func mkVocab(tags ...string) fakeVocab {
	m := map[string]bool{}
	for _, t := range tags {
		m[strings.ToLower(t)] = true
	}
	return fakeVocab{tags: m}
}

const sampleBefore = `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
---

# Raw Capture

text body
`

func mkPlan(t *testing.T, target, afterContent string) model.VaultPlan {
	t.Helper()
	payload, err := json.Marshal(model.CreateNotePayload{Content: afterContent})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	hash := sha256.Sum256([]byte(sampleBefore))
	return model.VaultPlan{
		ID:        "plan-1",
		JobID:     "job-1",
		RiskLevel: model.RiskLow,
		Operations: []model.VaultOperation{{
			ID:          "op-1",
			Type:        model.OperationRewriteNote,
			TargetPath:  target,
			BeforeHash:  hex.EncodeToString(hash[:]),
			PayloadJSON: string(payload),
			RiskLevel:   model.RiskLow,
		}},
	}
}

func TestEnrichHappyPath(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
tags:
  - topic/database
  - skill/golang
related:
  - "[[Knowledge/Go/concurrency]]"
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	vocab := mkVocab("topic/database", "skill/golang")
	if err := CheckEnrichPlan(plan, target, sampleBefore, vocab); err != nil {
		t.Fatalf("happy path rejected: %v", err)
	}
}

func TestEnrichEmptySignalsAllowed(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	if err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab()); err != nil {
		t.Fatalf("empty-signal apply rejected: %v", err)
	}
}

func TestEnrichRejectsPathMismatch(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	other := "Raw/Inbox/job-2.md"
	plan := mkPlan(t, other, sampleBefore)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab())
	if err == nil || !strings.Contains(err.Error(), "path_guard") {
		t.Fatalf("expected path_guard reject, got %v", err)
	}
}

func TestEnrichRejectsBodyChange(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := strings.Replace(sampleBefore, "text body", "TAMPERED body", 1)
	plan := mkPlan(t, target, after)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab())
	if err == nil || !strings.Contains(err.Error(), "body_guard") {
		t.Fatalf("expected body_guard reject, got %v", err)
	}
}

func TestEnrichRejectsNonWritableFieldChange(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := strings.Replace(sampleBefore, "source: cli", "source: matrix", 1)
	plan := mkPlan(t, target, after)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab())
	if err == nil || !strings.Contains(err.Error(), "field_guard") {
		t.Fatalf("expected field_guard reject, got %v", err)
	}
}

func TestEnrichRejectsTagDeletion(t *testing.T) {
	before := `---
source: cli
tags:
  - topic/old
---
body
`
	after := `---
source: cli
tags:
  - topic/new
---
body
`
	target := "Raw/Inbox/job-1.md"
	plan := mkPlan(t, target, after)
	// Update plan BeforeHash to match the new before content.
	h := sha256.Sum256([]byte(before))
	plan.Operations[0].BeforeHash = hex.EncodeToString(h[:])
	err := CheckEnrichPlan(plan, target, before, mkVocab("topic/old", "topic/new"))
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("expected append-only tag reject, got %v", err)
	}
}

func TestEnrichRejectsUnknownTopicTag(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
tags:
  - topic/not-in-vocab
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab("topic/something-else"))
	if err == nil || !strings.Contains(err.Error(), "not in known vocabulary") {
		t.Fatalf("expected vocab reject, got %v", err)
	}
}

func TestEnrichRejectsDisallowedPrefixTag(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
tags:
  - type/knowledge
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab())
	if err == nil || !strings.Contains(err.Error(), "not allowed for enrich") {
		t.Fatalf("expected type/ prefix reject, got %v", err)
	}
}

func TestEnrichRejectsTooManyRelated(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
related:
  - "[[Knowledge/a]]"
  - "[[Knowledge/b]]"
  - "[[Knowledge/c]]"
  - "[[Knowledge/d]]"
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab())
	if err == nil || !strings.Contains(err.Error(), "related additions") {
		t.Fatalf("expected related max reject, got %v", err)
	}
}

func TestEnrichRejectsNonWikilinkRelated(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
related:
  - just text not a wikilink
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab())
	if err == nil || !strings.Contains(err.Error(), "wikilink") {
		t.Fatalf("expected wikilink reject, got %v", err)
	}
}

func TestEnrichAllowsStatusNeedsReview(t *testing.T) {
	target := "Raw/Inbox/job-1.md"
	after := `---
openwhisker_job_id: job-1
openwhisker_job_type: ingest_raw
source: cli
source_key: foo
created_at: 2026-05-27T00:00:00Z
status: raw
tags:
  - status/needs-review
openwhisker_enriched_at: 2026-05-27T01:00:00Z
openwhisker_enrich_run_id: agentrun-1
openwhisker_enrich_attempts: 1
---

# Raw Capture

text body
`
	plan := mkPlan(t, target, after)
	if err := CheckEnrichPlan(plan, target, sampleBefore, mkVocab()); err != nil {
		t.Fatalf("status/needs-review rejected: %v", err)
	}
}
