package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// ClipCapturer creates a web-clip skeleton note for a captured link and queues
// the background fetch, returning the note path and job id. Implemented by
// internal/clip.Service; injected so core does not depend on the clip package.
type ClipCapturer interface {
	Clip(ctx context.Context, rawURL, source, sourceKey string) (notePath string, jobID string, err error)
}

type AdapterService struct {
	store            *storage.Store
	vaultRoot        string
	planOpts         PlanServiceOptions
	intentRouterMode string
	intentClassifier IntentClassifier
	now              func() time.Time

	// Phase 6: optional ad-hoc agent dispatch via @<skill-id> mention. Both
	// must be set for the @-mention path to engage; either nil → fall through
	// to the existing intent classifier.
	agentDispatcher AgentDispatcher
	skillLookup     SkillLookup

	// Phase 7: optional enrich enqueuer. When set, IngestRaw calls from
	// this adapter (and downstream intent_router bucket paths that are not
	// bucket-scoped) schedule an enrich job after Apply.
	enrichHook EnrichEnqueuer

	// §3.2: optional link-clip capturer. When set, a bare-URL message in
	// quick-capture mode is clipped into Raw/Sources instead of appended to
	// the text inbox. nil → URLs are captured as plain text.
	clipCapturer ClipCapturer
}

type AdapterServiceOptions struct {
	PlanOptions      PlanServiceOptions
	IntentRouterMode string
	IntentClassifier IntentClassifier

	// Phase 6 optional ad-hoc dispatch (Matrix @-mention).
	AgentDispatcher AgentDispatcher
	SkillLookup     SkillLookup

	// Phase 7 optional enrich enqueuer. Passed through to every IngestService
	// the adapter constructs.
	EnrichHook EnrichEnqueuer

	// §3.2 optional link-clip capturer (internal/clip.Service).
	ClipCapturer ClipCapturer
}

type AdapterRequest struct {
	Adapter   string `json:"adapter"`
	EventID   string `json:"event_id"`
	Sender    string `json:"sender"`
	SourceKey string `json:"source_key,omitempty"`
	Text      string `json:"text"`
}

type AdapterResponse struct {
	Status     string `json:"status"`
	JobID      string `json:"job_id,omitempty"`
	PlanID     string `json:"plan_id,omitempty"`
	Body       string `json:"body"`
	Duplicate  bool   `json:"duplicate,omitempty"`
	OutboxKind string `json:"outbox_kind,omitempty"`
}

func NewAdapterServiceWithOptions(store *storage.Store, vaultRoot string, opts AdapterServiceOptions) AdapterService {
	return AdapterService{
		store:            store,
		vaultRoot:        vaultRoot,
		planOpts:         opts.PlanOptions,
		intentRouterMode: normalizeIntentRouterMode(opts.IntentRouterMode),
		intentClassifier: opts.IntentClassifier,
		now:              func() time.Time { return time.Now().UTC() },
		agentDispatcher:  opts.AgentDispatcher,
		skillLookup:      opts.SkillLookup,
		enrichHook:       opts.EnrichHook,
		clipCapturer:     opts.ClipCapturer,
	}
}

func (s AdapterService) HandleText(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		return AdapterResponse{}, fmt.Errorf("adapter text is required")
	}
	if req.Adapter == "" {
		req.Adapter = "unknown"
	}
	if req.SourceKey == "" {
		req.SourceKey = adapterSourceKey(req)
	}
	if req.EventID != "" {
		requestJSON, err := json.Marshal(req)
		if err != nil {
			return AdapterResponse{}, err
		}
		recorded, err := s.store.TryRecordAdapterEvent(req.Adapter, req.EventID, string(requestJSON), s.now())
		if err != nil {
			return AdapterResponse{}, err
		}
		if !recorded {
			return AdapterResponse{
				Status:    "duplicate",
				Body:      "Duplicate event ignored.",
				Duplicate: true,
			}, nil
		}
	}
	// Phase 6 @<skill-id> routing. Highest priority: when both dispatcher
	// and lookup are configured AND the message opens with @<skill-id> +
	// whitespace, dispatch through AgentRunner. Otherwise fall through to
	// the existing intent classifier so Phase 5 behavior is unchanged.
	if s.agentDispatcher != nil && s.skillLookup != nil {
		if resp, handled, err := s.tryHandleAtMention(ctx, req); handled {
			return resp, err
		}
	}
	// Quick-capture mode: every non-slash message is a frictionless capture
	// into the per-day inbox. Slash commands still reach the adapter command
	// parser (e.g. /status, /jobs) for operational use. Bucket / approval /
	// clarification intents are intentionally bypassed.
	if s.intentRouterMode == intentRouterModeQuickCapture {
		if resp, handled, err := s.tryInboxCommand(ctx, req); handled {
			return resp, err
		}
		if strings.HasPrefix(req.Text, "/") {
			if cmd, _ := splitAdapterCommand(req.Text); isRetiredIMCommand(cmd) {
				return AdapterResponse{
					Status: "command_retired",
					Body:   "速记收件箱不在 IM 里做整理或审批。把素材消化成知识，请在 Claude Code / Codex 里直接对 vault 操作。",
				}, nil
			}
			return s.handleAdapterCommand(ctx, req)
		}
		if rawURL, ok := bareURL(req.Text); ok && s.clipCapturer != nil {
			return s.handleClipCapture(ctx, req, rawURL)
		}
		return s.handleQuickCapture(ctx, req)
	}
	if s.intentRouterMode != "off" && !strings.HasPrefix(req.Text, "/") {
		return s.handleIntentText(ctx, req)
	}
	return s.handleAdapterCommand(ctx, req)
}

// handleQuickCapture appends one text snippet to the per-day inbox and returns
// an immediate "已记录" ack. No outbox message is emitted (the ack is the
// response body) and no enrich hook fires — background tagging is a later step.
func (s AdapterService) handleQuickCapture(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
	result, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).
		WithEnrichHook(s.enrichHook).
		CaptureInbox(ctx, CaptureInboxRequest{
			Text:   req.Text,
			Source: adapterSource(req),
		})
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: model.JobStatusDone,
		JobID:  result.JobID,
		Body:   "已记录",
	}, nil
}

// isRetiredIMCommand reports whether a slash command belongs to the old
// organize / approve / diff flow that §6 removes from the IM (quick-capture)
// path. Operational commands (/status, /jobs, /scheduler) and the inbox
// commands remain available; these four are redirected to the desktop tools.
func isRetiredIMCommand(command string) bool {
	switch command {
	case "/organize", "/diff", "/approve", "/reject":
		return true
	default:
		return false
	}
}

// bareURL reports whether the whole message is a single http/https URL — the
// signal that the user wants the link clipped rather than stored as plain text.
// A URL mixed with other words is treated as a text capture (§3.2: clips are the
// article itself, with no surrounding annotation).
func bareURL(text string) (string, bool) {
	t := strings.TrimSpace(text)
	if t == "" || len(strings.Fields(t)) != 1 {
		return "", false
	}
	if !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
		return "", false
	}
	parsed, err := url.Parse(t)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	return t, true
}

// handleClipCapture creates the web-clip skeleton + queues the fetch, acking
// "已记录，剪藏中". If the capturer rejects the URL (e.g. an SSRF-blocked or
// non-public target), the link is captured as plain text instead so nothing is
// lost.
func (s AdapterService) handleClipCapture(ctx context.Context, req AdapterRequest, rawURL string) (AdapterResponse, error) {
	_, jobID, err := s.clipCapturer.Clip(ctx, rawURL, adapterSource(req), req.SourceKey)
	if err != nil {
		return s.handleQuickCapture(ctx, req)
	}
	return AdapterResponse{
		Status: model.JobStatusDone,
		JobID:  jobID,
		Body:   "已记录，剪藏中",
	}, nil
}

// inboxCommandKind classifies a quick-capture message as one of the three
// reserved inbox commands, or "" when it is just another capture. List and undo
// match only an exact whole-message phrase (so "今天去爬山" is captured, not
// treated as a command); search matches a recognized prefix that is the whole
// command word (a bare "搜索", or the prefix followed by a separator + keyword).
func inboxCommandKind(text string) (kind, arg string) {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	switch {
	case exactAny(trimmed, "看今天", "今天", "今天记了啥", "今天记了什么") || lower == "/today":
		return "today", ""
	case exactAny(trimmed, "撤回", "撤回上一条", "撤回刚才", "撤销", "撤销上一条") || lower == "/undo":
		return "undo", ""
	}
	for _, prefix := range []string{"找一下", "搜一下", "搜索", "/find", "/search"} {
		rest, ok := strings.CutPrefix(trimmed, prefix)
		if !ok {
			continue
		}
		// Require the prefix to stand as its own word: either nothing
		// follows (a bare "搜索" → prompt for a keyword) or the next rune is
		// a separator. Otherwise the prefix is glued to ordinary text
		// ("搜索引擎的原理…") and the message is a capture, not a command.
		if rest != "" && !startsWithCommandSeparator(rest) {
			continue
		}
		arg = strings.TrimSpace(strings.TrimLeft(rest, "：: 　\t"))
		return "find", arg
	}
	return "", ""
}

// startsWithCommandSeparator reports whether s begins with a character that
// separates an inbox command word from its argument: ASCII / full-width space,
// tab, or an ASCII / full-width colon.
func startsWithCommandSeparator(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	switch r {
	case ' ', '\t', '　', ':', '：':
		return true
	default:
		return false
	}
}

// tryInboxCommand handles the three quick-capture inbox commands (§5). It
// returns handled=false when the message is an ordinary capture so the caller
// continues to handleQuickCapture.
func (s AdapterService) tryInboxCommand(ctx context.Context, req AdapterRequest) (AdapterResponse, bool, error) {
	kind, arg := inboxCommandKind(req.Text)
	if kind == "" {
		return AdapterResponse{}, false, nil
	}
	ingest := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).
		WithEnrichHook(s.enrichHook)
	local := s.now().In(time.Local)
	switch kind {
	case "today":
		entries, _, err := ingest.ListInboxDay(local)
		if err != nil {
			return AdapterResponse{}, true, err
		}
		return AdapterResponse{Status: "ok", Body: renderInboxToday(entries)}, true, nil
	case "undo":
		removed, ok, err := ingest.UndoLastInbox(ctx, local)
		if err != nil {
			return AdapterResponse{}, true, err
		}
		if !ok {
			return AdapterResponse{Status: "ok", Body: "今天还没有可撤回的速记。"}, true, nil
		}
		return AdapterResponse{Status: model.JobStatusDone, Body: "已撤回：" + inboxExcerpt(removed.Text, 40)}, true, nil
	case "find":
		if arg == "" {
			return AdapterResponse{Status: "ok", Body: "想找什么？用「找一下 关键词」。"}, true, nil
		}
		hits, err := ingest.SearchInbox(arg, 10)
		if err != nil {
			return AdapterResponse{}, true, err
		}
		return AdapterResponse{Status: "ok", Body: renderInboxSearch(arg, hits)}, true, nil
	}
	return AdapterResponse{}, false, nil
}

// tryHandleAtMention returns handled=false when the message is not an
// @<skill-id> ad-hoc query; the caller continues to the existing intent
// branches. Client-completed Matrix mentions (which look like @user:server)
// are *not* @-mentions in our protocol — they fall through.
func (s AdapterService) tryHandleAtMention(ctx context.Context, req AdapterRequest) (AdapterResponse, bool, error) {
	skillID, query, ok := parseAtSkillPrefix(req.Text)
	if !ok {
		return AdapterResponse{}, false, nil
	}
	skill, err := s.skillLookup.Find(skillID)
	if err != nil {
		return AdapterResponse{
			Status: "vault_query_skill_not_found",
			Body:   fmt.Sprintf("未找到 Skill: %s", skillID),
		}, true, nil
	}
	if !skill.HasToolCalling() {
		return AdapterResponse{
			Status: "vault_query_skill_not_tool_calling",
			Body:   fmt.Sprintf("Skill %s 未启用 tool-calling 引擎，无法用 @ 自然语言触发。", skillID),
		}, true, nil
	}
	dispatched, err := s.agentDispatcher.DispatchAdHoc(ctx, AgentAdHocRequest{
		Skill:       skill,
		Query:       query,
		TriggerKind: model.AgentTriggerKindAdhocMatrix,
		Now:         s.now(),
	})
	if err != nil {
		return AdapterResponse{
			Status: "vault_query_failed",
			Body:   fmt.Sprintf("Agent run 失败: %s\n[trace: %s]", err.Error(), dispatched.RunID),
		}, true, nil
	}
	if dispatched.Status == model.SchedulerRunStatusFailed {
		return AdapterResponse{
			Status: "vault_query_failed",
			Body:   fmt.Sprintf("Agent run 失败: %s\n[trace: %s]", dispatched.Error, dispatched.RunID),
		}, true, nil
	}
	body := strings.TrimSpace(dispatched.Result.Summary)
	if dispatched.Result.Title != "" {
		body = dispatched.Result.Title + "\n\n" + body
	}
	if dispatched.Status == model.SchedulerRunStatusPartial {
		body += "\n\n(状态: partial — budget 已耗尽)"
	}
	body += fmt.Sprintf("\n\n[trace: %s]", dispatched.RunID)
	return AdapterResponse{
		Status: "vault_query_handled",
		Body:   body,
	}, true, nil
}

// parseAtSkillPrefix matches messages of the form "@<skill-id> <query>" or
// "@<skill-id>\n<query>". Returns ok=false when the prefix is not present or
// the skill-id looks like a Matrix user mention (contains a colon).
// skill-id chars: lowercase letters, digits, hyphen, underscore.
func parseAtSkillPrefix(text string) (skillID, query string, ok bool) {
	if !strings.HasPrefix(text, "@") {
		return "", "", false
	}
	rest := text[1:]
	// Split on first whitespace.
	idx := strings.IndexAny(rest, " \t\n")
	if idx < 0 {
		// "@<skill-id>" alone with no following text is still a valid
		// @-mention (treated as empty query).
		skillID = rest
	} else {
		skillID = rest[:idx]
		query = strings.TrimSpace(rest[idx+1:])
	}
	if skillID == "" {
		return "", "", false
	}
	// Reject anything that looks like a Matrix user/room mention — those
	// contain colons (e.g. @user:server.tld).
	if strings.Contains(skillID, ":") {
		return "", "", false
	}
	for _, r := range skillID {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' || r == '_' {
			continue
		}
		return "", "", false
	}
	return skillID, query, true
}

func (s AdapterService) handleAdapterCommand(ctx context.Context, req AdapterRequest) (AdapterResponse, error) {
	command, args := splitAdapterCommand(req.Text)
	switch command {
	case "":
		return s.ingestRaw(ctx, req, req.Text, false)
	case "/raw":
		if strings.TrimSpace(args) == "" {
			return AdapterResponse{}, fmt.Errorf("/raw requires text")
		}
		return s.ingestRaw(ctx, req, args, false)
	case "/no-enrich":
		if strings.TrimSpace(args) == "" {
			return AdapterResponse{}, fmt.Errorf("/no-enrich requires raw text")
		}
		return s.ingestRaw(ctx, req, args, true)
	case "/organize":
		return s.handleOrganize(ctx, strings.Fields(args))
	case "/diff":
		return s.handleDiff(args)
	case "/approve":
		return s.handleApprove(ctx, args)
	case "/reject":
		return s.handleReject(args)
	case "/status":
		return s.handleStatus(args)
	case "/jobs":
		return s.handleJobs(10)
	case "/scheduler":
		return s.handleSchedulerCommand(ctx, req, args)
	default:
		if strings.HasPrefix(command, "/") {
			return AdapterResponse{}, fmt.Errorf("unsupported adapter command %q", command)
		}
		return s.ingestRaw(ctx, req, req.Text, false)
	}
}

func (s AdapterService) handleSchedulerCommand(ctx context.Context, req AdapterRequest, args string) (AdapterResponse, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 || fields[0] == "status" {
		status, err := NewSchedulerStatusService(s.store, s.vaultRoot, profile.NewConfiguredVaultProfile(s.planOpts.Conventions)).Status(5)
		if err != nil {
			return AdapterResponse{}, err
		}
		return AdapterResponse{
			Status: "ok",
			Body:   RenderSchedulerStatus(status),
		}, nil
	}
	if fields[0] == "runs" {
		limit := 5
		if len(fields) >= 2 {
			if _, err := fmt.Sscanf(fields[1], "%d", &limit); err != nil || limit <= 0 {
				return AdapterResponse{}, fmt.Errorf("limit must be a positive integer")
			}
		}
		runs, err := NewSchedulerStatusService(s.store, s.vaultRoot, profile.NewConfiguredVaultProfile(s.planOpts.Conventions)).Runs(limit)
		if err != nil {
			return AdapterResponse{}, err
		}
		return AdapterResponse{
			Status: "ok",
			Body:   RenderSchedulerRuns(runs),
		}, nil
	}
	if fields[0] == "schedules" {
		schedules, err := NewSchedulerStatusService(s.store, s.vaultRoot, profile.NewConfiguredVaultProfile(s.planOpts.Conventions)).Schedules()
		if err != nil {
			return AdapterResponse{}, err
		}
		return AdapterResponse{
			Status: "ok",
			Body:   RenderSchedulerSchedules(schedules),
		}, nil
	}
	if len(fields) < 2 || fields[0] != "accept" {
		return AdapterResponse{}, fmt.Errorf("usage: /scheduler status | /scheduler runs [limit] | /scheduler schedules | /scheduler accept <run_id> [item_number]")
	}
	item := 1
	if len(fields) >= 3 {
		if _, err := fmt.Sscanf(fields[2], "%d", &item); err != nil || item <= 0 {
			return AdapterResponse{}, fmt.Errorf("item_number must be a positive integer")
		}
	}
	result, err := NewSchedulerSuggestionService(s.store, s.vaultRoot, s.planOpts).AcceptRawCapture(ctx, AcceptSchedulerSuggestedRawCaptureRequest{
		RunID:          fields[1],
		Item:           item,
		Source:         adapterSource(req),
		SourceKey:      req.SourceKey,
		SuppressOutbox: true,
	})
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     model.JobStatusDone,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Scheduler suggestion accepted. Raw capture saved to %s", result.TargetPath),
		OutboxKind: model.OutboxKindResult,
	}, nil
}

func (s AdapterService) PullOutbox(limit int) ([]model.OutboxMessage, error) {
	return s.store.ListPendingOutbox(limit)
}

func (s AdapterService) MarkOutboxDelivered(id string) error {
	return s.store.MarkOutboxDelivered(id)
}

func (s AdapterService) ingestRaw(ctx context.Context, req AdapterRequest, text string, noEnrich bool) (AdapterResponse, error) {
	result, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.planOpts.Conventions).
		WithEnrichHook(s.enrichHook).
		IngestRaw(ctx, IngestRawRequest{
			Text:     text,
			Source:   adapterSource(req),
			NoEnrich: noEnrich,
		})
	if err != nil {
		return AdapterResponse{}, err
	}
	body := fmt.Sprintf("Raw capture saved to %s", result.TargetPath)
	if noEnrich {
		body += " (enrichment skipped)"
	}
	return AdapterResponse{
		Status:     model.JobStatusDone,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       body,
		OutboxKind: model.OutboxKindResult,
	}, nil
}

func (s AdapterService) handleOrganize(ctx context.Context, fields []string) (AdapterResponse, error) {
	if len(fields) != 1 || fields[0] != "last" {
		return AdapterResponse{}, fmt.Errorf("usage: /organize last")
	}
	result, err := s.planService().OrganizeLast(ctx)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     result.Status,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Plan %s awaits approval: %s", result.PlanID, resultStatusSummary(result.Status, result.TargetPaths)),
		OutboxKind: model.OutboxKindApproval,
	}, nil
}

func (s AdapterService) handleDiff(identifier string) (AdapterResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return AdapterResponse{}, fmt.Errorf("usage: /diff <plan_id|job_id>")
	}
	diff, err := s.planService().Diff(identifier)
	if err != nil {
		var noDiff NoPreparedDiffError
		if errors.As(err, &noDiff) {
			return AdapterResponse{
				Status: "no_diff",
				PlanID: noDiff.PlanID,
				Body:   renderNoPreparedDiff(noDiff),
			}, nil
		}
		return AdapterResponse{}, err
	}
	plan, err := s.store.GetPlan(diff.PlanID)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     "ok",
		PlanID:     diff.PlanID,
		Body:       renderAdapterDiff(plan, diff),
		OutboxKind: model.OutboxKindDiff,
	}, nil
}

func (s AdapterService) handleApprove(ctx context.Context, identifier string) (AdapterResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return AdapterResponse{}, fmt.Errorf("usage: /approve <plan_id|job_id>")
	}
	result, err := s.planService().Approve(ctx, identifier)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     result.Status,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Plan %s applied.", result.PlanID),
		OutboxKind: model.OutboxKindResult,
	}, nil
}

func (s AdapterService) handleReject(args string) (AdapterResponse, error) {
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return AdapterResponse{}, fmt.Errorf("usage: /reject <plan_id|job_id> [reason]")
	}
	identifier := fields[0]
	reason := strings.TrimSpace(strings.TrimPrefix(args, identifier))
	result, err := s.planService().Reject(identifier, reason)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status:     result.Status,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		Body:       fmt.Sprintf("Plan %s rejected.", result.PlanID),
		OutboxKind: model.OutboxKindRejected,
	}, nil
}

func (s AdapterService) handleStatus(identifier string) (AdapterResponse, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return s.handleJobs(5)
	}
	if job, err := s.store.GetJob(identifier); err == nil {
		return AdapterResponse{
			Status: job.Status,
			JobID:  job.ID,
			Body:   fmt.Sprintf("Job %s is %s.", job.ID, job.Status),
		}, nil
	}
	plan, err := s.store.GetPlan(identifier)
	if err != nil {
		return AdapterResponse{}, err
	}
	return AdapterResponse{
		Status: plan.Status,
		JobID:  plan.JobID,
		PlanID: plan.ID,
		Body:   fmt.Sprintf("Plan %s is %s.", plan.ID, plan.Status),
	}, nil
}

func (s AdapterService) handleJobs(limit int) (AdapterResponse, error) {
	jobs, err := s.store.ListRecentJobs(limit)
	if err != nil {
		return AdapterResponse{}, err
	}
	if len(jobs) == 0 {
		return AdapterResponse{Status: "ok", Body: "No jobs yet."}, nil
	}
	var lines []string
	for _, job := range jobs {
		lines = append(lines, fmt.Sprintf("- %s %s %s", job.ID, job.Type, job.Status))
	}
	return AdapterResponse{
		Status: "ok",
		Body:   "Recent jobs:\n" + strings.Join(lines, "\n"),
	}, nil
}

func (s AdapterService) planService() PlanService {
	return NewPlanServiceWithOptions(s.store, s.vaultRoot, s.planOpts)
}

func (s AdapterService) intentPlanService() PlanService {
	opts := s.planOpts
	opts.SuppressOutbox = true
	return NewPlanServiceWithOptions(s.store, s.vaultRoot, opts)
}

func splitAdapterCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", text
	}
	command, args, _ := strings.Cut(text, " ")
	return strings.ToLower(strings.TrimSpace(command)), strings.TrimSpace(args)
}

func adapterSource(req AdapterRequest) string {
	if req.Sender == "" {
		return req.Adapter
	}
	return req.Adapter + ":" + req.Sender
}

func adapterSourceKey(req AdapterRequest) string {
	if strings.TrimSpace(req.SourceKey) != "" {
		return strings.TrimSpace(req.SourceKey)
	}
	if req.Sender == "" {
		return req.Adapter
	}
	return req.Adapter + ":" + req.Sender
}

func adapterSourceKind(adapter string) string {
	switch adapter {
	case model.AdapterMatrix:
		return "matrix"
	case "":
		return "unknown"
	default:
		return adapter
	}
}
