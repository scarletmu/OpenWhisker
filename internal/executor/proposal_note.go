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

// RenderProposalNote produces markdown conforming to docs/architecture/proposal-note-schema.md.
// kind must be one of model.ProposalKind*.
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
	title := summaryToTitle(plan.Summary)
	timestamp := now.Format(time.RFC3339)

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", yamlQuoteString(title))
	b.WriteString("type: proposal\n")
	b.WriteString("status: needs-review\n")
	fmt.Fprintf(&b, "created: %q\n", timestamp)
	fmt.Fprintf(&b, "updated: %q\n", timestamp)
	b.WriteString("source:\n")
	fmt.Fprintf(&b, "  plan_id: %s\n", plan.ID)
	fmt.Fprintf(&b, "  job_id: %s\n", plan.JobID)
	origin := plan.Purpose
	if strings.TrimSpace(origin) == "" {
		origin = "unknown"
	}
	fmt.Fprintf(&b, "  origin: %s\n", yamlQuoteString(origin))
	b.WriteString("tags:\n")
	b.WriteString("  - type/proposal\n")
	b.WriteString("  - status/needs-review\n")
	fmt.Fprintf(&b, "  - proposal/%s\n", kind)
	b.WriteString("aliases:\n")
	b.WriteString("related:\n")
	for _, target := range uniqueStrings(plan.TargetPaths) {
		fmt.Fprintf(&b, "  - \"[[%s]]\"\n", trimMarkdownExt(target))
	}
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", title)

	b.WriteString("## 来源\n\n")
	if len(plan.SourceRefs) == 0 {
		b.WriteString("- 未提供 source_refs\n")
	} else {
		for _, ref := range plan.SourceRefs {
			if looksLikeVaultPath(ref) {
				fmt.Fprintf(&b, "- [[%s]]\n", trimMarkdownExt(ref))
			} else {
				fmt.Fprintf(&b, "- %s\n", ref)
			}
		}
	}
	b.WriteString("\n")

	b.WriteString("## 目标结构\n\n")
	fmt.Fprintf(&b, "%s\n\n", plan.Summary)
	if len(plan.TargetPaths) > 0 {
		b.WriteString("涉及路径:\n\n")
		for _, target := range uniqueStrings(plan.TargetPaths) {
			fmt.Fprintf(&b, "- `%s`\n", target)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 影响路径\n\n")
	if len(plan.Operations) == 0 {
		b.WriteString("- 未提供任何操作\n\n")
	}
	for _, op := range plan.Operations {
		impact, err := renderOperationImpact(op)
		if err != nil {
			return "", err
		}
		b.WriteString(impact)
	}

	b.WriteString("## 建议操作\n\n")
	b.WriteString("- 审阅上述影响路径,确认所列每条变更是否符合预期。\n")
	b.WriteString("- 若整体可接受,可手工执行或拆分为更小的 plan 重新走 OpenWhisker 审批流程。\n")
	b.WriteString("- 若不接受,直接将本 proposal 的 `status` 标记为 `rejected` 或归档。\n\n")

	b.WriteString("## 待人工确认问题\n\n")
	questions := proposalOpenQuestions(plan, kind)
	if len(questions) == 0 {
		return "", fmt.Errorf("proposal note must include at least one open question")
	}
	for _, q := range questions {
		fmt.Fprintf(&b, "- %s\n", q)
	}

	return b.String(), nil
}

func renderOperationImpact(op model.VaultOperation) (string, error) {
	var b strings.Builder
	switch op.Type {
	case model.OperationRenameNote:
		var payload model.RenameNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return "", fmt.Errorf("decode rename_note payload for %s: %w", op.ID, err)
		}
		fmt.Fprintf(&b, "- **rename**: `%s` → `%s`\n", payload.SourcePath, payload.DestinationPath)
		if reason := strings.TrimSpace(op.Reason); reason != "" {
			fmt.Fprintf(&b, "  - 理由: %s\n", reason)
		}
	case model.OperationBulkRetag:
		var payload model.BulkRetagPayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return "", fmt.Errorf("decode bulk_retag payload for %s: %w", op.ID, err)
		}
		fmt.Fprintf(&b, "- **bulk-retag**: 影响 %d 篇笔记\n", len(payload.AffectedPaths))
		if len(payload.AddTags) > 0 {
			fmt.Fprintf(&b, "  - 新增标签: %s\n", strings.Join(payload.AddTags, ", "))
		}
		if len(payload.RemoveTags) > 0 {
			fmt.Fprintf(&b, "  - 移除标签: %s\n", strings.Join(payload.RemoveTags, ", "))
		}
		for _, p := range payload.AffectedPaths {
			fmt.Fprintf(&b, "  - `%s`\n", p)
		}
		if reason := strings.TrimSpace(op.Reason); reason != "" {
			fmt.Fprintf(&b, "  - 理由: %s\n", reason)
		}
	case model.OperationBulkLinkRewrite:
		var payload model.BulkLinkRewritePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			return "", fmt.Errorf("decode bulk_link_rewrite payload for %s: %w", op.ID, err)
		}
		fmt.Fprintf(&b, "- **bulk-link-rewrite**: `%s` → `%s` (影响 %d 篇)\n", payload.FromPath, payload.ToPath, len(payload.AffectedPaths))
		for _, p := range payload.AffectedPaths {
			fmt.Fprintf(&b, "  - `%s`\n", p)
		}
		if reason := strings.TrimSpace(op.Reason); reason != "" {
			fmt.Fprintf(&b, "  - 理由: %s\n", reason)
		}
	default:
		return "", fmt.Errorf("unsupported operation type %q in proposal rendering", op.Type)
	}
	return b.String(), nil
}

func proposalOpenQuestions(plan model.VaultPlan, kind string) []string {
	var questions []string
	switch kind {
	case model.ProposalKindRename:
		questions = append(questions,
			"目标路径是否会破坏现有反向链接? 是否需要先扫描 backlinks 再执行?",
			"新名称是否与现有 Knowledge note 冲突或语义重复?",
		)
	case model.ProposalKindMerge:
		questions = append(questions,
			"合并后哪些来源 note 应该被删除、哪些应改为 redirect?",
			"合并后的目标 note 是否需要拆分章节以保留每个来源的上下文?",
		)
	case model.ProposalKindSplit:
		questions = append(questions,
			"拆分后每篇子 note 的边界是否清晰、是否存在重复内容?",
			"原 note 是否保留为 hub note, 还是直接归档?",
		)
	case model.ProposalKindBulkRetag:
		questions = append(questions,
			"标签变更是否符合 Meta/Tagging.md 的受控前缀?",
			"是否需要先在小范围 (1-2 篇) 试运行确认效果?",
		)
	case model.ProposalKindBulkLinkRewrite:
		questions = append(questions,
			"链接改写是否会影响 dataview / base / 第三方插件查询?",
			"是否需要保留旧路径的占位 stub 以避免外部引用断裂?",
		)
	default:
		questions = append(questions, "本 proposal 是否需要拆分为更小的可独立审批 plan?")
	}
	if len(plan.Operations) > 1 {
		questions = append(questions, "上述多个 high-risk 操作是否需要按顺序分批执行,而不是一次性应用?")
	}
	return questions
}

func summaryToTitle(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "Proposal"
	}
	for _, sep := range []string{"\n", "。", ". "} {
		if idx := strings.Index(summary, sep); idx > 0 {
			summary = summary[:idx]
			break
		}
	}
	const max = 80
	if runes := []rune(summary); len(runes) > max {
		summary = string(runes[:max])
	}
	return summary
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
