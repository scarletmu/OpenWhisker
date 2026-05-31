package enrich

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/agent"
	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/memory"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// AgentRunner is the subset of agent.AgentRunner that enrich depends on.
// Defined as an interface so tests can inject a fake without standing up a
// real LLM client.
type AgentRunner interface {
	Run(ctx context.Context, req agent.AgentRunRequest) (agent.AgentRunResult, error)
}

// Config wires the runtime dependencies enrich needs to process one job.
// All fields are required for production use; tests may stub Runner / Vocab.
type Config struct {
	Store     *storage.Store
	Queue     *Queue
	VaultRoot string
	Runner    AgentRunner
	Vocab     *memory.Service
	Executor  executor.DirectFS
	// Now is the clock for trace timestamps and frontmatter stamps. Defaults
	// to time.Now().UTC() when nil.
	Now func() time.Time
}

// Phase 7 enrich budgets are intentionally hard-coded. Per decision 3 +
// decision 10, the v1 constants live in code; we revisit only after demo
// data shows the chosen values are wrong.
const (
	EnrichMaxToolCalls       = 8
	EnrichMaxTotalBytes      = 256 * 1024
	EnrichMaxWallClockSecs   = 20
	EnrichExcerptRunes       = 800
	EnrichSkillID            = "openwhisker:inbox-enrich"
)

// EnrichVaultScope is the subtree set the inbox-enrich skill is allowed to
// read via vault tools. We intentionally include Raw/Inbox so the agent can
// re-read its own input note via read_vault_note; the policy field_guard is
// the real defense against the agent trying to write anywhere else.
var EnrichVaultScope = []string{"Raw/Inbox", "Knowledge", "Interview", "Life"}

// EnrichVaultTools mirrors the Phase 6 read-only tool set; enrich does not
// add any new tools (decision: "复用 Phase 6 的 5 + 1 read-only 工具").
var EnrichVaultTools = []string{"list_vault_dir", "read_vault_note", "vault_text_search", "vault_outlinks", "vault_backlinks"}

// EnrichResult mirrors the schema enrich agents must produce via
// submit_result's payload field. Marshalling tolerates absent or nil
// optional fields — empty signals are a valid terminal state per decision 5.
type EnrichResult struct {
	Tags              []string         `json:"tags,omitempty"`
	Related           []string         `json:"related,omitempty"`
	RouteSuggestion   *RouteSuggestion `json:"route_suggestion,omitempty"`
	NewTagCandidates  []NewTagCand     `json:"new_tag_candidates,omitempty"`
	NeedsReview       bool             `json:"needs_review,omitempty"`
	Notes             string           `json:"notes,omitempty"`
}

// RouteSuggestion is the enrich agent's "this Raw probably belongs under X"
// signal. We only persist it (and ping Matrix) when Confidence ≥ 0.7.
type RouteSuggestion struct {
	TargetDir  string  `json:"target_dir"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// UnmarshalJSON tolerates models that emit route_suggestion as a bare string
// instead of the {target_dir, confidence, reason} object the schema asks for.
// The prompt marks the field optional ("only fill when confident"), and some
// providers (e.g. deepseek-chat) simplify it to a scalar. A string form carries
// no confidence, so we keep it as Reason and leave Confidence at 0 — below
// EnrichRouteSuggestionMinConfidence — so it is never written to frontmatter.
// This stops a stray string from hard-failing the entire EnrichResult decode
// and silently dropping the tags/related the run did produce.
func (r *RouteSuggestion) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		r.Reason = strings.TrimSpace(s)
		return nil
	}
	// Object form: decode via an alias type so we don't recurse into this method.
	type routeAlias RouteSuggestion
	var a routeAlias
	if err := json.Unmarshal(trimmed, &a); err != nil {
		return err
	}
	*r = RouteSuggestion(a)
	return nil
}

// NewTagCand carries a tag the agent thought should exist but did not find
// in the controlled vocabulary. Always recorded — never auto-promoted.
type NewTagCand struct {
	Tag    string `json:"tag"`
	Reason string `json:"reason"`
}

// Service is the per-process orchestrator. Worker / scan / manual retry all
// route a single rawPath through Run to get the outcome.
type Service struct {
	cfg Config
}

// NewService validates cfg and returns a ready-to-use orchestrator. Callers
// (daemon main, tests) construct one and share it across the worker goroutine
// and the manual `openwhisker enrich` subcommand.
func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("enrich: Store is required")
	}
	if cfg.Queue == nil {
		return nil, errors.New("enrich: Queue is required")
	}
	if strings.TrimSpace(cfg.VaultRoot) == "" {
		return nil, errors.New("enrich: VaultRoot is required")
	}
	if cfg.Runner == nil {
		return nil, errors.New("enrich: Runner is required")
	}
	if cfg.Vocab == nil {
		return nil, errors.New("enrich: Vocab is required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{cfg: cfg}, nil
}

// RunOutcome reports what happened to one enrich attempt. Internal to the
// package; callers branch on this to decide whether to log to outbox at
// debug or info level.
type RunOutcome struct {
	State   string // matches EnrichJobState* constants
	Err     error
	RunID   string
	Applied bool
	Result  EnrichResult
}

// RunOne processes a single enrich job end-to-end. Always calls Queue.Finish
// before returning so the state machine stays consistent even if a panic
// recovers up the stack later.
func (s *Service) RunOne(ctx context.Context, job storage.EnrichJob) RunOutcome {
	// 1. Vocab must be ready. KnownTags blocking on rescan is part of the
	// contract — Phase 7 prefers correctness over throughput here.
	if err := s.cfg.Vocab.WaitReady(ctx); err != nil {
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("vocab wait: %w", err), EnrichResult{}, "")
	}

	// 1b. Create a wiki_job row for this enrich attempt so the plan's
	// job_id FK resolves and downstream `agent runs` listings can show
	// enrich activity alongside other job types. parent_job_id is recorded
	// in input_json so we can trace back to the originating Raw capture.
	enrichJobInput, _ := json.Marshal(map[string]string{
		"raw_path":      job.RawPath,
		"parent_job_id": job.ParentJobID,
	})
	now := s.cfg.Now()
	wikiJob := model.WikiJob{
		ID:        model.NewID("job"),
		Type:      model.JobTypeEnrichRaw,
		Status:    model.JobStatusApplying,
		Source:    "enrich",
		SourceKey: job.RawPath,
		InputJSON: string(enrichJobInput),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.cfg.Store.CreateJob(wikiJob); err != nil {
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("create enrich job: %w", err), EnrichResult{}, "")
	}

	// 2. Read the Raw note + capture pre_hash. Treat missing files as a
	// terminal done — the user deleted the note between enqueue and run.
	beforeContent, err := s.readVaultFile(job.RawPath)
	if err != nil {
		if os.IsNotExist(err) {
			_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusDone, "", "")
			return s.finish(job.RawPath, StateDone, nil, EnrichResult{}, "")
		}
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("read raw: %w", err), EnrichResult{}, "")
	}
	preHash := model.ContentHash([]byte(beforeContent))

	// 3. Don't double-enrich. If the file already has openwhisker_enriched_at
	// (eg. queued by both event and scan paths), treat as done.
	if hasFrontmatterKey(beforeContent, model.EnrichFrontmatterEnrichedAt) {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusDone, "", "")
		return s.finish(job.RawPath, StateDone, nil, EnrichResult{}, "")
	}

	// 4. Build skill + input, run agent.
	knownTopic := s.cfg.Vocab.KnownTags("topic/")
	knownSkill := s.cfg.Vocab.KnownTags("skill/")
	excerpt := bodyExcerpt(beforeContent, EnrichExcerptRunes)
	query, err := buildAgentInput(job.RawPath, job.ParentJobID, beforeContent, excerpt, knownTopic, knownSkill)
	if err != nil {
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("agent input: %w", err), EnrichResult{}, "")
	}
	skill := buildEnrichSkill(EnrichVaultScope, EnrichVaultTools)

	runReq := agent.AgentRunRequest{
		Skill:       skill,
		Query:       query,
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         now,
	}
	agentResult, agentErr := s.cfg.Runner.Run(ctx, runReq)
	if agentErr != nil {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", agentErr.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("agent run: %w", agentErr), EnrichResult{}, "")
	}
	if agentResult.Status == model.SchedulerRunStatusFailed {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", agentResult.Error)
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("agent failed: %s", agentResult.Error), EnrichResult{}, agentResult.SkillID)
	}

	var er EnrichResult
	if len(agentResult.Payload) > 0 && string(agentResult.Payload) != "null" {
		if err := json.Unmarshal(agentResult.Payload, &er); err != nil {
			_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
			return s.finish(job.RawPath, StateFailed, fmt.Errorf("agent payload decode: %w", err), EnrichResult{}, agentResult.SkillID)
		}
	}

	// 5. Re-read for cur_hash. Concurrent edit → drop + scan picks up later.
	curContent, err := s.readVaultFile(job.RawPath)
	if err != nil {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("re-read raw: %w", err), er, agentResult.SkillID)
	}
	if model.ContentHash([]byte(curContent)) != preHash {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", "concurrent edit")
		return s.finish(job.RawPath, StateSkippedConcEdit, errors.New("hash mismatch: concurrent edit"), er, agentResult.SkillID)
	}

	// 6. Build the rewrite_note plan. The orchestrator owns rendering; the
	// agent never produces raw content. The plan's job_id is the wiki_job
	// row we created above so FK constraints are satisfied.
	plan, err := s.buildPlan(wikiJob.ID, job, beforeContent, preHash, er, agentResult.SkillID, now)
	if err != nil {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("build plan: %w", err), er, agentResult.SkillID)
	}
	if err := s.cfg.Store.SavePlan(plan); err != nil {
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("save plan: %w", err), er, agentResult.SkillID)
	}

	// 7. policy guard.
	if err := policy.CheckEnrichPlan(plan, job.RawPath, beforeContent, vocabAdapter{s.cfg.Vocab}); err != nil {
		_ = s.cfg.Store.UpdatePlanStatus(plan.ID, model.PlanStatusRejected, nil)
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("policy: %w", err), er, agentResult.SkillID)
	}

	// 8. Executor apply.
	if _, err := s.cfg.Executor.Apply(ctx, plan); err != nil {
		_ = s.cfg.Store.UpdatePlanStatus(plan.ID, model.PlanStatusFailed, nil)
		_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusFailed, "", err.Error())
		return s.finish(job.RawPath, StateFailed, fmt.Errorf("apply: %w", err), er, agentResult.SkillID)
	}
	appliedAt := s.cfg.Now()
	_ = s.cfg.Store.UpdatePlanStatus(plan.ID, model.PlanStatusApplied, &appliedAt)
	_ = s.cfg.Store.UpdateJobStatus(wikiJob.ID, model.JobStatusDone, "", "")

	// 9. Record vault-introduced tags so vocab keeps up without waiting for
	// the next rescan. Errors here are warnings, not failures — sqlite is
	// down isn't worth losing the successful Apply.
	_ = s.cfg.Vocab.Record(ctx, er.Tags)

	return s.finish(job.RawPath, StateDone, nil, er, agentResult.SkillID)
}

func (s *Service) finish(rawPath, state string, err error, result EnrichResult, runID string) RunOutcome {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if qErr := s.cfg.Queue.Finish(rawPath, state, msg); qErr != nil {
		// Surface the queue write error if there isn't already one. The
		// run outcome carries both pieces so the worker can log the more
		// informative message.
		if err == nil {
			err = qErr
		}
	}
	return RunOutcome{State: state, Err: err, Applied: state == StateDone && msg == "", Result: result, RunID: runID}
}

func (s *Service) buildPlan(wikiJobID string, job storage.EnrichJob, beforeContent, preHash string, er EnrichResult, runID string, now time.Time) (model.VaultPlan, error) {
	attemptsNext := job.Attempts + 1 // we're inside this attempt
	private := map[string]string{
		model.EnrichFrontmatterEnrichedAt:    now.Format(time.RFC3339),
		model.EnrichFrontmatterEnrichRunID:   runID,
		model.EnrichFrontmatterEnrichAttempts: fmt.Sprintf("%d", attemptsNext),
	}
	if er.RouteSuggestion != nil && er.RouteSuggestion.Confidence >= model.EnrichRouteSuggestionMinConfidence {
		rsJSON, _ := json.Marshal(er.RouteSuggestion)
		private[model.EnrichFrontmatterRouteSuggestion] = string(rsJSON)
	}
	if len(er.NewTagCandidates) > 0 {
		cJSON, _ := json.Marshal(er.NewTagCandidates)
		private[model.EnrichFrontmatterNewTagCandidates] = string(cJSON)
	}
	tagsToAdd := append([]string{}, sanitizeStrings(er.Tags)...)
	if er.NeedsReview {
		tagsToAdd = append(tagsToAdd, "status/needs-review")
	}
	relatedToAdd := sanitizeStrings(er.Related)
	if len(relatedToAdd) > model.EnrichRelatedAppendMax {
		relatedToAdd = relatedToAdd[:model.EnrichRelatedAppendMax]
	}
	newContent, err := applyEnrichToFrontmatter(beforeContent, tagsToAdd, relatedToAdd, private)
	if err != nil {
		return model.VaultPlan{}, err
	}
	payload, err := json.Marshal(model.CreateNotePayload{Content: newContent})
	if err != nil {
		return model.VaultPlan{}, err
	}
	op := model.VaultOperation{
		ID:          model.NewID("op"),
		Type:        model.OperationRewriteNote,
		TargetPath:  job.RawPath,
		BeforeHash:  preHash,
		PayloadJSON: string(payload),
		Reason:      "Enrich Raw note with vault-native tag attribution and OpenWhisker provenance fields.",
		RiskLevel:   model.RiskLow,
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            wikiJobID,
		Purpose:          "enrich raw note",
		RiskLevel:        model.RiskLow,
		RequiresApproval: false,
		Summary:          "Append tags and OpenWhisker enrich metadata to a Raw/Inbox note under the rewrite_note guard.",
		SourceRefs:       compactStrings(job.ParentJobID, job.RawPath),
		TargetPaths:      []string{job.RawPath},
		Operations:       []model.VaultOperation{op},
		Status:           model.PlanStatusProposed,
		CreatedAt:        now,
	}, nil
}

// readVaultFile resolves a vault-relative path under VaultRoot and reads
// the bytes. Mirrors core.IngestService.readVaultFile so the orchestrator
// never invents its own path handling.
func (s *Service) readVaultFile(relPath string) (string, error) {
	full, err := executor.ResolveVaultPath(s.cfg.VaultRoot, relPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// vocabAdapter satisfies policy.TagVocab without exporting the memory
// package's full Service surface to the policy layer. Avoids drawing
// internal/policy into a dependency on internal/memory.
type vocabAdapter struct{ inner *memory.Service }

func (v vocabAdapter) HasTag(tag string) bool { return v.inner.HasTag(tag) }

// buildEnrichSkill returns the in-code ScheduledSkill used by enrich. Per
// decision 1 the prompt lives in this binary (not in vault Skills/), so it
// cannot drift away from the policy field_guard contract.
func buildEnrichSkill(scope, tools []string) scheduler.ScheduledSkill {
	return scheduler.ScheduledSkill{
		ID:         EnrichSkillID,
		Name:       "Inbox Enrich",
		Enabled:    true,
		Engine:     profile.AgentSkillEngineToolCalling,
		Body:       enrichSkillBody,
		VaultTools: append([]string{}, tools...),
		VaultScope: append([]string{}, scope...),
		Budget: scheduler.ToolBudget{
			MaxToolCalls:        EnrichMaxToolCalls,
			MaxTotalBytes:       EnrichMaxTotalBytes,
			MaxWallClockSeconds: EnrichMaxWallClockSecs,
		},
		HasSchedule: false,
	}
}

const enrichSkillBody = `You are the OpenWhisker inbox-enrich agent.

Your only job is to look at one freshly captured Raw note and decide, using
the vault's existing controlled tag vocabulary, which 1–4 ` + "`topic/`" + ` and
` + "`skill/`" + ` tags it should carry. You do NOT modify Raw notes directly;
your output is a structured plan that the host validates and applies.

Inputs you will receive (as the user message, JSON-encoded):

- raw_path: the vault-relative path of the Raw note.
- raw_job_id, source, source_key, created_at: provenance fields.
- excerpt: the first ~800 runes of the Raw body.
- known_topic_tags: array of every ` + "`topic/*`" + ` tag currently in use
  across Knowledge/, Interview/, and Life/.
- known_skill_tags: same for ` + "`skill/*`" + `.

Strict rules you must obey when filling submit_result.payload:

1. ` + "`tags`" + `: 1–4 entries. Every ` + "`topic/*`" + ` or ` + "`skill/*`" + ` you
   pick MUST appear verbatim in the input ` + "`known_*`" + ` arrays. If
   nothing in the vocabulary fits, leave ` + "`tags`" + ` empty and set
   ` + "`needs_review: true`" + `.
2. ` + "`related`" + ` (optional): zero to three ` + "`[[wikilink]]`" + ` strings
   pointing to concrete Knowledge / Interview / Life notes that the Raw
   relates to. Use vault_text_search / vault_outlinks to confirm a target
   exists before listing it.
3. ` + "`route_suggestion`" + ` (optional): only fill when you are confident
   (≥ 0.7) the Raw belongs in a different subtree than Raw/Inbox/. Otherwise
   omit.
4. ` + "`new_tag_candidates`" + ` (optional): when the Raw clearly needs a tag
   that does not exist in the vocabulary, propose it here with a one-line
   reason. Never sneak a fresh tag into ` + "`tags`" + `.
5. ` + "`needs_review`" + `: true when you set ` + "`new_tag_candidates`" + ` or
   could not confidently pick tags.
6. ` + "`notes`" + ` (optional): a short human-readable explanation for the
   outbox (≤ 200 chars). Do not include the raw content.

You may use the vault tools to peek at related notes; use them sparingly —
your tool_call budget is small. If you cannot finish before the budget
expires, call submit_result with whatever partial signals you have.
`

// buildAgentInput renders the JSON user message handed to the agent. We keep
// it in one place so the schema stays in lockstep with the skill body's
// description.
func buildAgentInput(rawPath, parentJobID, content, excerpt string, knownTopic, knownSkill []string) (string, error) {
	fm := extractFrontmatterKV(content)
	in := map[string]any{
		"raw_path":          rawPath,
		"raw_job_id":        parentJobID,
		"source":            fm["source"],
		"source_key":        fm["source_key"],
		"created_at":        fm["created_at"],
		"excerpt":           excerpt,
		"known_topic_tags":  knownTopic,
		"known_skill_tags":  knownSkill,
	}
	b, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func extractFrontmatterKV(content string) map[string]string {
	out := map[string]string{}
	const opener = "---\n"
	if !strings.HasPrefix(content, opener) {
		return out
	}
	rest := content[len(opener):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return out
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:colon])
		val := strings.TrimSpace(line[colon+1:])
		val = strings.Trim(val, `"'`)
		if key != "" {
			out[key] = val
		}
	}
	return out
}

// bodyExcerpt returns the first maxRunes runes of the body (after frontmatter).
// Truncation happens at the rune boundary so multi-byte characters survive.
func bodyExcerpt(content string, maxRunes int) string {
	const opener = "---\n"
	body := content
	if strings.HasPrefix(content, opener) {
		rest := content[len(opener):]
		if end := strings.Index(rest, "\n---"); end >= 0 {
			body = rest[end+len("\n---"):]
		}
	}
	body = strings.TrimLeft(body, "\n")
	runes := []rune(body)
	if len(runes) <= maxRunes {
		return body
	}
	return string(runes[:maxRunes])
}

func hasFrontmatterKey(content, key string) bool {
	const opener = "---\n"
	if !strings.HasPrefix(content, opener) {
		return false
	}
	rest := content[len(opener):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if strings.HasPrefix(line, key+":") {
			return true
		}
	}
	return false
}

func sanitizeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func compactStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// isBucketPath returns true when raw is a CaptureBucket file (filename ends
// _bucket.md) or its frontmatter declares the bucket via openwhisker_bucket_id.
// Scan must skip these; IngestRaw already skips them at enqueue time.
func isBucketPath(raw string, frontmatterContent string) bool {
	if strings.HasSuffix(raw, "_bucket.md") {
		return true
	}
	if hasFrontmatterKey(frontmatterContent, "openwhisker_bucket_id") {
		return true
	}
	if hasFrontmatterListTag(frontmatterContent, "raw/bucket") {
		return true
	}
	return false
}

// hasFrontmatterListTag is a lightweight check (no parsing) for the
// presence of `- <tag>` under any list-style frontmatter key. Used by
// isBucketPath to detect `raw/bucket` under `tags`.
func hasFrontmatterListTag(content, tag string) bool {
	const opener = "---\n"
	if !strings.HasPrefix(content, opener) {
		return false
	}
	rest := content[len(opener):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return false
	}
	needle := "- " + tag
	for _, line := range strings.Split(rest[:end], "\n") {
		if strings.TrimSpace(line) == needle {
			return true
		}
	}
	return false
}

