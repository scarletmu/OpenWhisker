package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
)

const proposalShortIDLen = 12

// ProposalNotePath returns the controlled proposal-note path for a plan under
// the supplied agent-proposals directory (from the vault profile's
// conventions.AgentProposalsDir). The caller is responsible for passing a
// non-empty, vault-relative directory.
func ProposalNotePath(planID, baseDir string) string {
	short := strings.TrimPrefix(planID, "plan_")
	if len(short) > proposalShortIDLen {
		short = short[:proposalShortIDLen]
	}
	return strings.TrimRight(baseDir, "/") + "/proposal_" + short + ".md"
}

// ApplyAsProposal renders a proposal note for a high-risk plan and writes it
// under baseDir (vault-profile-supplied agent-proposals directory). The plan's
// high-risk operations are recorded in the rendered note but are not executed
// against the vault. The operation log row carries Outcome=proposed so
// downstream auditors can distinguish proposal writes from regular applies.
func (e DirectFS) ApplyAsProposal(ctx context.Context, plan model.VaultPlan, baseDir string) (model.AppliedOperation, error) {
	if err := ctx.Err(); err != nil {
		return model.AppliedOperation{}, err
	}
	if plan.RiskLevel != model.RiskHigh {
		return model.AppliedOperation{}, fmt.Errorf("apply as proposal requires risk %q, got %q", model.RiskHigh, plan.RiskLevel)
	}
	if strings.TrimSpace(baseDir) == "" {
		return model.AppliedOperation{}, fmt.Errorf("apply as proposal requires non-empty proposals directory")
	}
	kind, err := policy.ClassifyProposalKind(plan)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	targetPath := ProposalNotePath(plan.ID, baseDir)
	fullPath, err := ResolveVaultPath(e.vaultRoot, targetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	if _, err := os.Lstat(fullPath); err == nil {
		return model.AppliedOperation{}, conflictError("proposal note already exists: %s", targetPath)
	} else if !os.IsNotExist(err) {
		return model.AppliedOperation{}, err
	}
	now := time.Now().UTC()
	if err := e.store.AcquireLocks([]string{targetPath}, plan.ID, now); err != nil {
		return model.AppliedOperation{}, err
	}
	defer e.store.ReleaseLocks(plan.ID)

	content, err := RenderProposalNote(plan, kind, now)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return model.AppliedOperation{}, err
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return model.AppliedOperation{}, err
	}
	afterHash := sha256Hex([]byte(content))
	applied := model.AppliedOperation{
		OperationID: plan.ID,
		TargetPath:  targetPath,
		AfterHash:   afterHash,
	}
	resultJSON, _ := json.Marshal(applied)
	log := model.VaultOperationLog{
		ID:          model.NewID("vop"),
		PlanID:      plan.ID,
		JobID:       plan.JobID,
		OpType:      model.OperationWriteProposal,
		TargetPath:  targetPath,
		BeforeHash:  "",
		AfterHash:   afterHash,
		PayloadJSON: "",
		ResultJSON:  string(resultJSON),
		Reason:      "Write proposal note for high-risk plan.",
		Status:      model.OperationStatusApplied,
		Outcome:     model.OperationOutcomeProposed,
		CreatedAt:   now,
		AppliedAt:   &now,
	}
	if err := e.store.AppendOperationLog(log); err != nil {
		return model.AppliedOperation{}, err
	}
	return applied, nil
}

// AffectedRow describes one row in a proposal note's "## 影响路径" section.
// BuildAffectedRows derives these from a high-risk plan; the proposal renderer
// and the high-risk diff synthesizer both consume them so the preview before
// approval and the artifact after approval cover the same set of paths.
type AffectedRow struct {
	Action    string
	Source    string
	Target    string
	Rationale string
}

// BuildAffectedRows derives display rows for a high-risk plan keyed by its
// proposal kind. The Knowledge Expander currently encodes split/merge as a
// single rename op plus the affected children in plan.TargetPaths[1:], so the
// rows for those kinds expand the plan-level path list rather than the op.
func BuildAffectedRows(plan model.VaultPlan, kind string) ([]AffectedRow, error) {
	if len(plan.Operations) == 0 {
		return nil, fmt.Errorf("plan has no operations")
	}
	op := plan.Operations[0]

	switch kind {
	case model.ProposalKindRename:
		var payload model.RenameNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("decode rename payload for %s: %w", op.ID, err)
		}
		return []AffectedRow{{
			Action:    kind,
			Source:    payload.SourcePath,
			Target:    payload.DestinationPath,
			Rationale: strings.TrimSpace(payload.Reason),
		}}, nil

	case model.ProposalKindSplit, model.ProposalKindMerge:
		var payload model.RenameNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("decode rename payload for %s: %w", op.ID, err)
		}
		rationale := strings.TrimSpace(payload.Reason)
		source := payload.SourcePath
		seen := map[string]bool{source: true}
		var rows []AffectedRow
		for _, t := range plan.TargetPaths[1:] {
			if t == "" || seen[t] {
				continue
			}
			seen[t] = true
			rows = append(rows, AffectedRow{
				Action: kind, Source: source, Target: t, Rationale: rationale,
			})
		}
		if len(rows) == 0 {
			rows = append(rows, AffectedRow{
				Action: kind, Source: source, Target: payload.DestinationPath, Rationale: rationale,
			})
		}
		return rows, nil

	case model.ProposalKindBulkRetag:
		var payload model.BulkRetagPayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("decode bulk_retag payload for %s: %w", op.ID, err)
		}
		rationale := strings.TrimSpace(payload.Reason)
		tagSummary := bulkRetagTagSummary(payload.AddTags, payload.RemoveTags)
		var rows []AffectedRow
		for _, p := range payload.AffectedPaths {
			rows = append(rows, AffectedRow{
				Action:    kind,
				Source:    p,
				Target:    p,
				Rationale: joinNonEmpty("；", tagSummary, rationale),
			})
		}
		return rows, nil

	case model.ProposalKindBulkLinkRewrite:
		var payload model.BulkLinkRewritePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return nil, fmt.Errorf("decode bulk_link_rewrite payload for %s: %w", op.ID, err)
		}
		rationale := strings.TrimSpace(payload.Reason)
		linkChange := fmt.Sprintf("链接 %s → %s", payload.FromPath, payload.ToPath)
		var rows []AffectedRow
		for _, p := range payload.AffectedPaths {
			rows = append(rows, AffectedRow{
				Action:    kind,
				Source:    p,
				Target:    p,
				Rationale: joinNonEmpty("；", linkChange, rationale),
			})
		}
		return rows, nil
	}
	return nil, fmt.Errorf("unsupported proposal kind %q in affected-row rendering", kind)
}

// RenderProposalNote produces markdown conforming to docs/architecture/proposal-note-schema.md.
// kind must be one of model.ProposalKind*. As of 4C.2 only the Knowledge Expander
// emits high-risk plans, so the rendered `source.origin` is hardcoded to
// "expander"; revisit when another agent gains a high-risk path.
func RenderProposalNote(plan model.VaultPlan, kind string, now time.Time) (string, error) {
	if strings.TrimSpace(plan.ID) == "" {
		return "", fmt.Errorf("plan id is required")
	}
	if strings.TrimSpace(plan.JobID) == "" {
		return "", fmt.Errorf("plan job id is required")
	}
	if strings.TrimSpace(plan.Summary) == "" {
		return "", fmt.Errorf("plan summary is required")
	}
	if strings.TrimSpace(kind) == "" {
		return "", fmt.Errorf("proposal kind is required")
	}
	rows, err := BuildAffectedRows(plan, kind)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("proposal note must have at least one affected path")
	}

	title := proposalShortTitle(plan, kind)
	timestamp := now.Format(time.RFC3339)

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlQuoteString(title))
	b.WriteString("type: proposal\n")
	b.WriteString("status: needs-review\n")
	b.WriteString("risk: high\n")
	fmt.Fprintf(&b, "created: %q\n", timestamp)
	fmt.Fprintf(&b, "updated: %q\n", timestamp)
	b.WriteString("source:\n")
	fmt.Fprintf(&b, "  plan_id: %s\n", plan.ID)
	fmt.Fprintf(&b, "  job_id: %s\n", plan.JobID)
	b.WriteString("  origin: expander\n")
	b.WriteString("tags:\n")
	b.WriteString("  - type/proposal\n")
	b.WriteString("  - status/needs-review\n")
	fmt.Fprintf(&b, "  - proposal/%s\n", kind)
	b.WriteString("related:\n")
	for _, target := range uniqueStrings(plan.TargetPaths) {
		fmt.Fprintf(&b, "  - \"[[%s]]\"\n", trimMarkdownExt(target))
	}
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", title)

	b.WriteString("> [!warning] 高风险结构变更提案\n")
	b.WriteString("> 本文档由 OpenWhisker 自动生成。vault 未被修改。\n")
	b.WriteString("> 请人工评审 → 在外部工具（Codex / Claude Code / Obsidian）执行所需操作。\n\n")

	// 来源：列出分析对象（TargetPaths[0]）加上任何额外的 vault 路径来源（如
	// 关联 raw note）。排除 job/plan id 和受影响子路径（expander 出于 traceability
	// 把它们塞进 SourceRefs，但语义上属于 proposal 的"输出"，不属于来源）。
	sources := vaultSourceCandidates(plan)
	b.WriteString("## 来源\n\n")
	if len(sources) == 0 {
		b.WriteString("- 未提供 vault 来源\n")
	} else {
		for _, ref := range sources {
			fmt.Fprintf(&b, "- [[%s]]\n", trimMarkdownExt(ref))
		}
	}
	b.WriteString("\n")

	b.WriteString("## 目标结构\n\n")
	fmt.Fprintf(&b, "%s\n\n", plan.Summary)

	b.WriteString("## 影响路径\n\n")
	if len(rows) >= 2 {
		b.WriteString("| 操作 | 原路径 | 目标路径 | 理由 |\n")
		b.WriteString("| --- | --- | --- | --- |\n")
		for _, r := range rows {
			fmt.Fprintf(&b, "| %s | [[%s]] | [[%s]] | %s |\n",
				r.Action,
				trimMarkdownExt(r.Source),
				trimMarkdownExt(r.Target),
				escapeTableCell(r.Rationale),
			)
		}
	} else {
		r := rows[0]
		fmt.Fprintf(&b, "- **%s**: [[%s]] → [[%s]]\n",
			r.Action, trimMarkdownExt(r.Source), trimMarkdownExt(r.Target))
		if r.Rationale != "" {
			fmt.Fprintf(&b, "  - 理由：%s\n", r.Rationale)
		}
	}
	b.WriteString("\n")

	b.WriteString("## 建议操作\n\n")
	b.WriteString("- 审阅上表逐条目标路径，确认与现有 Knowledge note 不冲突。\n")
	b.WriteString("- 在外部工具（Codex / Claude Code）中按上表逐项执行所需变更。\n")
	b.WriteString("- 若不接受，将本 proposal 的 `status` 标记为 `rejected` 或归档。\n\n")

	b.WriteString("## 待人工确认问题\n\n")
	questions := proposalOpenQuestions(plan, kind)
	if len(questions) == 0 {
		return "", fmt.Errorf("proposal note must include at least one open question")
	}
	for i, q := range questions {
		fmt.Fprintf(&b, "- %s ^q%d\n", q, i+1)
	}

	return b.String(), nil
}

func proposalShortTitle(plan model.VaultPlan, kind string) string {
	primary := ""
	if len(plan.TargetPaths) > 0 {
		primary = filepath.Base(strings.TrimSuffix(plan.TargetPaths[0], ".md"))
	}
	var verb string
	switch kind {
	case model.ProposalKindSplit:
		verb = "拆分"
	case model.ProposalKindMerge:
		verb = "合并"
	case model.ProposalKindRename:
		verb = "重命名"
	case model.ProposalKindBulkRetag:
		verb = "批量重打标签"
	case model.ProposalKindBulkLinkRewrite:
		verb = "批量改写链接"
	default:
		verb = "结构变更"
	}
	if primary == "" {
		return verb + " 提案"
	}
	return verb + " " + primary + " 提案"
}

func proposalOpenQuestions(plan model.VaultPlan, kind string) []string {
	var questions []string
	switch kind {
	case model.ProposalKindRename:
		questions = append(questions,
			"目标路径是否会破坏现有反向链接？是否需要先扫描 backlinks 再执行？",
			"新名称是否与现有 Knowledge note 冲突或语义重复？",
		)
	case model.ProposalKindMerge:
		questions = append(questions,
			"合并后哪些来源 note 应该被删除、哪些应改为 redirect？",
			"合并后的目标 note 是否需要拆分章节以保留每个来源的上下文？",
		)
	case model.ProposalKindSplit:
		questions = append(questions,
			"拆分后每篇子 note 的边界是否清晰、是否存在重复内容？",
			"原 note 是否保留为 hub note，还是直接归档？",
		)
	case model.ProposalKindBulkRetag:
		questions = append(questions,
			"标签变更是否符合 Meta/Tagging.md 的受控前缀？",
			"是否需要先在小范围（1-2 篇）试运行确认效果？",
		)
	case model.ProposalKindBulkLinkRewrite:
		questions = append(questions,
			"链接改写是否会影响 dataview / base / 第三方插件查询？",
			"是否需要保留旧路径的占位 stub 以避免外部引用断裂？",
		)
	default:
		questions = append(questions, "本 proposal 是否需要拆分为更小的可独立审批 plan？")
	}
	if len(plan.Operations) > 1 {
		questions = append(questions, "上述多个 high-risk 操作是否需要按顺序分批执行，而不是一次性应用？")
	}
	return questions
}

func bulkRetagTagSummary(add, remove []string) string {
	var parts []string
	if len(add) > 0 {
		parts = append(parts, "新增 "+strings.Join(add, ", "))
	}
	if len(remove) > 0 {
		parts = append(parts, "移除 "+strings.Join(remove, ", "))
	}
	return strings.Join(parts, "；")
}

func joinNonEmpty(sep string, values ...string) string {
	var nonEmpty []string
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			nonEmpty = append(nonEmpty, v)
		}
	}
	return strings.Join(nonEmpty, sep)
}

func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func yamlQuoteString(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func trimMarkdownExt(path string) string {
	return strings.TrimSuffix(path, ".md")
}

func looksLikeVaultPath(ref string) bool {
	return strings.HasSuffix(ref, ".md") && !strings.HasPrefix(ref, "job_") && !strings.HasPrefix(ref, "plan_")
}

func vaultSourceCandidates(plan model.VaultPlan) []string {
	var origin string
	if len(plan.TargetPaths) > 0 && looksLikeVaultPath(plan.TargetPaths[0]) {
		origin = plan.TargetPaths[0]
	}
	// Affected children are the proposal's outputs, not sources. Skip the
	// origin if the LLM happened to also list it under affected_paths.
	excludeChildren := make(map[string]bool, len(plan.TargetPaths))
	for _, p := range plan.TargetPaths[1:] {
		if p == origin || p == "" {
			continue
		}
		excludeChildren[p] = true
	}
	out := make([]string, 0, len(plan.SourceRefs)+1)
	seen := make(map[string]bool, len(plan.SourceRefs)+1)
	if origin != "" {
		out = append(out, origin)
		seen[origin] = true
	}
	for _, r := range plan.SourceRefs {
		if !looksLikeVaultPath(r) || seen[r] || excludeChildren[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
