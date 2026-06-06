package core

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/markdown"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
)

func resultStatusSummary(status string, paths []string) string {
	if len(paths) == 0 {
		return status
	}
	return status + " for " + strings.Join(paths, ", ")
}

func renderNoPreparedDiff(err NoPreparedDiffError) string {
	if err.RiskLevel == model.RiskLow && err.Status == model.PlanStatusApplied {
		return fmt.Sprintf("Plan %s is a low-risk raw capture that was already auto-applied, so it has no approval diff.\nRun /organize last first, then use /diff <plan_id> on the approval plan.", err.PlanID)
	}
	return fmt.Sprintf("Plan %s has no prepared approval diff. Current status: %s.", err.PlanID, err.Status)
}

func renderAdapterDiff(plan model.VaultPlan, diff *model.VaultDiff) string {
	if plan.RiskLevel == model.RiskHigh {
		return renderHighRiskAdapterDiff(plan, diff)
	}
	lines := []string{
		"## 审批预览",
		"",
		fmt.Sprintf("- Plan: %s", diff.PlanID),
		fmt.Sprintf("- 摘要: %s", diff.Summary),
	}
	if len(plan.SourceRefs) > 0 {
		lines = append(lines, fmt.Sprintf("- 来源: %s", strings.Join(plan.SourceRefs, ", ")))
	}
	if created := renderCreatedNotePreview(plan.Operations); created != "" {
		lines = append(lines, "", created)
	}
	if moved := renderRawMovePreview(plan.Operations); moved != "" {
		lines = append(lines, "", moved)
	}
	if created := renderCreatedOperationSummary(plan.Operations); created != "" {
		lines = append(lines, "", "## 将执行", created)
	}
	lines = append(lines,
		"",
		"## 审批动作",
		fmt.Sprintf("- 批准：//approve %s", diff.PlanID),
		fmt.Sprintf("- 拒绝：//reject %s", diff.PlanID),
	)
	return strings.Join(lines, "\n")
}

func renderHighRiskAdapterDiff(plan model.VaultPlan, diff *model.VaultDiff) string {
	// The high-risk diff puts the proposal-write entry first; subsequent
	// entries are per-affected-path previews carrying the "→ proposal only"
	// suffix (see synthesizeHighRiskDiff).
	var proposalPath, kindLabel string
	previewEntries := diff.Entries
	if len(diff.Entries) > 0 && diff.Entries[0].Type == model.OperationWriteProposal {
		proposalPath = diff.Entries[0].TargetPath
		kindLabel = diff.Entries[0].Summary
		previewEntries = diff.Entries[1:]
	}
	lines := []string{
		"⚠ 高风险计划：批准后只生成 proposal note，不会写入正式 Knowledge note。",
		"",
		"## 审批预览",
		"",
		fmt.Sprintf("- Plan: %s", diff.PlanID),
		fmt.Sprintf("- 原摘要: %s", plan.Summary),
	}
	if kindLabel != "" {
		lines = append(lines, fmt.Sprintf("- 类型: %s", kindLabel))
	}
	if proposalPath != "" {
		lines = append(lines, fmt.Sprintf("- proposal 路径: %s", proposalPath))
	}
	if len(plan.SourceRefs) > 0 {
		lines = append(lines, fmt.Sprintf("- 来源: %s", strings.Join(plan.SourceRefs, ", ")))
	}
	lines = append(lines, "", "## 影响路径")
	for _, entry := range previewEntries {
		lines = append(lines, "- "+entry.Summary)
		if entry.Preview != "" {
			lines = append(lines, "  - 理由："+entry.Preview)
		}
	}
	lines = append(lines,
		"",
		"## 审批动作",
		fmt.Sprintf("- 批准 (写 proposal)：//approve %s", diff.PlanID),
		fmt.Sprintf("- 拒绝：//reject %s", diff.PlanID),
	)
	return strings.Join(lines, "\n")
}

func renderCreatedNotePreview(operations []model.VaultOperation) string {
	var sections []string
	for _, op := range operations {
		if op.Type != model.OperationCreateNote && op.Type != model.OperationAppendNote && op.Type != model.OperationWriteAgentReport {
			continue
		}
		content, ok := operationMarkdownContent(op)
		if !ok {
			continue
		}
		doc := summarizeMarkdownDocument(content, op.TargetPath)
		heading := "## 将写入的知识草稿"
		switch op.Type {
		case model.OperationAppendNote:
			heading = "## 将追加到笔记"
		case model.OperationWriteAgentReport:
			heading = "## 将写入的 Agent 报告"
		}
		lines := []string{
			heading,
			"",
			fmt.Sprintf("- 路径: %s", op.TargetPath),
			fmt.Sprintf("- 标题: %s", doc.Title),
		}
		if len(doc.Tags) > 0 {
			lines = append(lines, fmt.Sprintf("- 标签: %s", strings.Join(doc.Tags, ", ")))
		}
		if doc.Source != "" {
			lines = append(lines, fmt.Sprintf("- 来源: %s", doc.Source))
		}
		if doc.BodyPreview != "" {
			lines = append(lines, "", "### 正文预览", "", doc.BodyPreview)
		}
		if len(doc.ReviewItems) > 0 {
			lines = append(lines, "", "### 待核查")
			for _, item := range doc.ReviewItems {
				lines = append(lines, "- "+item)
			}
		}
		sections = append(sections, strings.Join(lines, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func renderRawMovePreview(operations []model.VaultOperation) string {
	var lines []string
	for _, op := range operations {
		if op.Type != model.OperationMoveNote {
			continue
		}
		var payload model.MoveNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
			continue
		}
		lines = append(lines,
			"## 将移动的 Raw",
			"",
			op.TargetPath,
			fmt.Sprintf("-> %s", payload.DestinationPath),
		)
		if note := summarizeProcessingNote(payload.ProcessingNote); note != "" {
			lines = append(lines, "", "处理记录会追加：", note)
		}
	}
	return strings.Join(lines, "\n")
}

func renderCreatedOperationSummary(operations []model.VaultOperation) string {
	var lines []string
	for _, op := range operations {
		switch op.Type {
		case model.OperationCreateNote:
			lines = append(lines, "- 创建笔记: "+op.TargetPath)
		case model.OperationWriteAgentReport:
			lines = append(lines, "- 创建 agent report: "+op.TargetPath)
		case model.OperationAppendNote:
			lines = append(lines, "- 追加内容: "+op.TargetPath)
		case model.OperationMoveNote:
			var payload model.MoveNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err == nil && payload.DestinationPath != "" {
				lines = append(lines, fmt.Sprintf("- 移动笔记: %s -> %s", op.TargetPath, payload.DestinationPath))
			} else {
				lines = append(lines, "- 移动笔记: "+op.TargetPath)
			}
		default:
			lines = append(lines, "- "+op.Type+": "+op.TargetPath)
		}
	}
	return strings.Join(lines, "\n")
}

func operationMarkdownContent(op model.VaultOperation) (string, bool) {
	switch op.Type {
	case model.OperationCreateNote, model.OperationWriteAgentReport:
		var payload model.CreateNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil || strings.TrimSpace(payload.Content) == "" {
			return "", false
		}
		return payload.Content, true
	case model.OperationAppendNote:
		var payload model.AppendNotePayload
		if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil || strings.TrimSpace(payload.Content) == "" {
			return "", false
		}
		return payload.Content, true
	default:
		return "", false
	}
}

type markdownDocumentSummary struct {
	Title       string
	Tags        []string
	Source      string
	BodyPreview string
	ReviewItems []string
}

func summarizeMarkdownDocument(content, fallbackPath string) markdownDocumentSummary {
	block, body, _ := markdown.SplitFrontmatter(content)
	bodyLines := visibleMarkdownLines(body)
	return markdownDocumentSummary{
		Title:       firstMarkdownTitle(bodyLines, fallbackTitle(fallbackPath)),
		Tags:        markdown.Parse(block).List("tags"),
		Source:      firstSourceLine(bodyLines),
		BodyPreview: markdownPreview(bodyLines, 10),
		ReviewItems: reviewItems(bodyLines),
	}
}

func visibleMarkdownLines(markdown string) []string {
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "openwhisker_") || strings.Contains(lower, "before_hash") || strings.Contains(lower, "after_hash") {
			continue
		}
		out = append(out, strings.TrimRight(line, " \t"))
	}
	return out
}

func firstMarkdownTitle(lines []string, fallback string) string {
	var out []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "# "); ok {
			return strings.TrimSpace(rest)
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
			continue
		}
		out = append(out, trimmed)
		if len(out) > 0 {
			return truncateRunes(strings.Join(out, " "), 80)
		}
	}
	return fallback
}

func fallbackTitle(path string) string {
	name := path
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	return strings.TrimSuffix(name, ".md")
}

func firstSourceLine(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "[[Raw/") || strings.Contains(trimmed, "来源") {
			return strings.TrimPrefix(trimmed, "- ")
		}
		if strings.Contains(lower, "raw path before approval") {
			return strings.TrimPrefix(trimmed, "- ")
		}
	}
	return ""
}

func markdownPreview(lines []string, maxLines int) string {
	var out []string
	inReviewSection := false
	inTraceSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if rest, ok := strings.CutPrefix(trimmed, "## "); ok {
			heading := strings.TrimSpace(rest)
			headingLower := strings.ToLower(heading)
			inTraceSection = headingLower == "source" || strings.Contains(heading, "来源") || strings.Contains(headingLower, "trace")
			if inTraceSection {
				continue
			}
		}
		if inTraceSection && strings.HasPrefix(trimmed, "#") {
			inTraceSection = false
		}
		if inTraceSection {
			continue
		}
		if strings.Contains(trimmed, "待核查") || strings.Contains(lower, "review") {
			inReviewSection = true
			continue
		}
		if inReviewSection && strings.HasPrefix(trimmed, "#") {
			inReviewSection = false
		}
		if inReviewSection {
			continue
		}
		if trimmed == "" && len(out) == 0 {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") && len(out) == 0 {
			continue
		}
		out = append(out, truncateRunes(line, 180))
		if len(out) >= maxLines {
			break
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func reviewItems(lines []string) []string {
	var items []string
	inReviewSection := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.Contains(trimmed, "待核查") || strings.Contains(lower, "needs review") || strings.Contains(lower, "needs-review") {
			inReviewSection = true
			if rest, ok := strings.CutPrefix(trimmed, "- "); ok {
				items = append(items, strings.TrimSpace(rest))
			}
			continue
		}
		if inReviewSection && strings.HasPrefix(trimmed, "#") {
			break
		}
		if inReviewSection && strings.HasPrefix(trimmed, "- ") {
			items = append(items, strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")))
		}
	}
	if len(items) > 5 {
		return items[:5]
	}
	return items
}

func summarizeProcessingNote(note string) string {
	var out []string
	for line := range strings.SplitSeq(strings.ReplaceAll(note, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "## ") {
			continue
		}
		lower := strings.ToLower(trimmed)
		if strings.Contains(lower, "openwhisker_") || strings.Contains(lower, "before_hash") || strings.Contains(lower, "after_hash") {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") {
			out = append(out, trimmed)
		}
		if len(out) >= 5 {
			break
		}
	}
	return strings.Join(out, "\n")
}

func truncateRunes(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "..."
}
func renderKnowledgeDraft(job, rawJob model.WikiJob, rawPath, processedPath string, conventions policy.Conventions, createdAt time.Time) string {
	return fmt.Sprintf(`---
title: "Knowledge Draft from %s"
tags:
%s
related:
  - "[[%s]]"
openwhisker:
  job_id: %s
  job_type: %s
  raw_job_id: %s
  raw_path: %s
  processed_path: %s
  created_at: %s
---

# Knowledge Draft from %s

> [!todo] OpenWhisker Raw Organizer 草稿
> 由 OpenWhisker 从 raw 输入整理。请人工审阅 → 补全 → 转写为正式 Knowledge note 后归档此 draft。

## 摘要

Deterministic Phase 2 draft；待 LLM 替换正文。

## 笔记

This deterministic Phase 2 draft preserves traceability and proves the approval-before-write path. Replace this section with LLM-backed organization in a later phase.

## 来源

- [[%s]]

## 待核查

> [!todo] 待核查
> - 核对从 raw 输入推断出的内容是否准确。
`,
		rawJob.ID,
		renderYAMLList(mergeKnowledgeDraftTags(conventions.RequiredDraftTags)),
		trimVaultExt(processedPath),
		job.ID, job.Type, rawJob.ID, rawPath, processedPath, createdAt.Format(time.RFC3339),
		rawJob.ID,
		trimVaultExt(processedPath),
	)
}

func renderProcessedRawNote(job, rawJob model.WikiJob, rawPath, processedPath string, outputPaths []string, processedAt time.Time, note string) string {
	var outputLines []string
	for _, path := range outputPaths {
		path = strings.TrimSpace(path)
		if path != "" {
			outputLines = append(outputLines, fmt.Sprintf("- [[%s]]", trimVaultExt(path)))
		}
	}
	if len(outputLines) == 0 {
		outputLines = []string{"- 待补充"}
	}
	return fmt.Sprintf(`

---

> [!note] OpenWhisker Processing
> 由 OpenWhisker 处理为 Knowledge draft；以下为处理元信息与输出。

### Outputs

%s

### Processing Note

%s

### Trace

- plan_job: `+"`%s`"+`
- raw_job: `+"`%s`"+`
- raw_path: `+"`%s`"+`
- processed_path: `+"`%s`"+`
- processed_at: `+"`%s`"+`
`,
		strings.Join(outputLines, "\n"),
		strings.TrimSpace(note),
		job.ID, rawJob.ID, rawPath, processedPath, processedAt.Format(time.RFC3339),
	)
}

func joinVaultPath(dir, name string) string {
	dir = strings.Trim(strings.TrimSpace(filepath.ToSlash(dir)), "/")
	name = strings.Trim(strings.TrimSpace(filepath.ToSlash(name)), "/")
	if dir == "" {
		return name
	}
	if name == "" {
		return dir
	}
	return dir + "/" + name
}

func renderYAMLList(values []string) string {
	if len(values) == 0 {
		return "  []"
	}
	lines := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		lines = append(lines, "  - "+value)
	}
	if len(lines) == 0 {
		return "  []"
	}
	return strings.Join(lines, "\n")
}

func trimVaultExt(path string) string {
	return strings.TrimSuffix(strings.TrimSpace(path), ".md")
}

func mergeKnowledgeDraftTags(required []string) []string {
	defaults := []string{"type/knowledge-draft", "status/needs-review"}
	seen := make(map[string]struct{}, len(defaults)+len(required))
	out := make([]string, 0, len(defaults)+len(required))
	for _, tag := range defaults {
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	for _, tag := range required {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
