package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

type OpenAIKnowledgeExpander struct {
	Client OpenAICompatibleClient
}

type knowledgeExpanderLLMOutput struct {
	Kind          string                      `json:"kind"`
	Title         string                      `json:"title"`
	Summary       string                      `json:"summary"`
	AppendSection string                      `json:"append_section"`
	ChildNote     *knowledgeExpanderChildNote `json:"child_note"`
	Restructure   *knowledgeExpanderRestruct  `json:"restructure"`
	ReviewItems   []string                    `json:"review_items"`
}

type knowledgeExpanderChildNote struct {
	RelativePath string `json:"relative_path"`
	Title        string `json:"title"`
	DraftBody    string `json:"draft_body"`
}

type knowledgeExpanderRestruct struct {
	ProposalKind  string   `json:"proposal_kind"`
	Rationale     string   `json:"rationale"`
	AffectedPaths []string `json:"affected_paths"`
}

const (
	knowledgeExpanderKindAppend      = "append"
	knowledgeExpanderKindCreateChild = "create_child_note"
	knowledgeExpanderKindRestructure = "propose_restructure"
)

func (o OpenAIKnowledgeExpander) ExpandKnowledge(ctx context.Context, req core.KnowledgeExpanderRequest) (model.VaultPlan, error) {
	if o.Client == nil {
		return model.VaultPlan{}, errors.New("openai knowledge expander client is required")
	}
	outputText, err := o.createWithRetry(ctx, openAIResponseRequest{
		Instructions:    knowledgeExpanderInstructions(),
		Input:           renderKnowledgeExpanderInput(req),
		Text:            openAITextSpec{Format: rawOrganizerResponseFormat()},
		MaxOutputTokens: 4096,
		Store:           false,
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	var output knowledgeExpanderLLMOutput
	if err := json.Unmarshal([]byte(outputText), &output); err != nil {
		return model.VaultPlan{}, fmt.Errorf("decode knowledge expander structured output: %w", err)
	}
	if err := validateKnowledgeExpanderOutput(output); err != nil {
		return model.VaultPlan{}, err
	}
	return buildKnowledgeExpanderPlan(req, output)
}

func (o OpenAIKnowledgeExpander) createWithRetry(ctx context.Context, req openAIResponseRequest) (string, error) {
	out, err := o.Client.CreateResponse(ctx, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) != "" {
		return out, nil
	}
	out, err = o.Client.CreateResponse(ctx, req)
	if err != nil {
		return "", fmt.Errorf("knowledge expander retry after empty content failed: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", errors.New("knowledge expander returned empty content after one retry")
	}
	return out, nil
}

func knowledgeExpanderInstructions() string {
	return strings.TrimSpace(`You are OpenWhisker Knowledge Expander.

Return ONLY a single JSON object. No prose, no markdown fences, no comments, no trailing text.

The JSON object MUST contain these fields:

- kind: one of ["append", "create_child_note", "propose_restructure"].
- title: short Chinese title for the human approval prompt.
- summary: one or two Chinese sentences describing the change.
- append_section: string. Required when kind=append; Markdown section to append at the end of the target note. Must NOT contain frontmatter and MUST start with an H2 heading. Empty string when kind!=append.
- child_note: object. Required when kind=create_child_note; must be null otherwise. Fields: { relative_path: string, title: string, draft_body: string }. relative_path is a slug joined under the configured Knowledge draft directory; must be a clean relative path. draft_body is Chinese Markdown without frontmatter and without a top-level H1.
- restructure: object. Required when kind=propose_restructure; must be null otherwise. Fields: { proposal_kind: one of ["split","merge","rename","bulk-retag","bulk-link-rewrite"], rationale: string, affected_paths: string array }.
- review_items: array of strings. Items the user should still verify. May be empty.

Example JSON output (append):
{"kind":"append","title":"补充 Phase4 笔记","summary":"在已有 Knowledge note 末尾追加 raw 中的新结论。","append_section":"## 新增结论\n\n- ...","child_note":null,"restructure":null,"review_items":["确认新增结论来源是否充分。"]}

Use kind=propose_restructure only when the target note clearly needs split / merge / rename. The user will turn this into a proposal note and approve it manually; do NOT attempt to execute the restructure here.

Generate Chinese content by default; keep fixed technical terms, paths, property names, tag values, commands, APIs, and protocol names in English.
Preserve source traceability and mark uncertain claims for review.
Do not ask to write files, run shell, call Obsidian CLI, or bypass approval.`)
}

func renderKnowledgeExpanderInput(req core.KnowledgeExpanderRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Expand the target Knowledge note into a reviewable plan or a restructure proposal.\n")
	fmt.Fprintf(&b, "Plan job id: %s\n", req.Job.ID)
	fmt.Fprintf(&b, "Target Knowledge path: %s\n\n", req.TargetPath)
	for _, doc := range req.VaultContext.Documents {
		fmt.Fprintf(&b, "## Context: %s\n%s\n\n", doc.Path, limitString(doc.Content, 6000))
	}
	for _, doc := range req.VaultContext.RelatedNotes {
		fmt.Fprintf(&b, "## Related: %s\n%s\n\n", doc.Path, limitString(doc.Content, 6000))
	}
	fmt.Fprintf(&b, "## Target note\n%s\n", limitString(req.VaultContext.TargetContent, 24000))
	return b.String()
}

func validateKnowledgeExpanderOutput(out knowledgeExpanderLLMOutput) error {
	if strings.TrimSpace(out.Title) == "" {
		return errors.New("knowledge expander output title is required")
	}
	if strings.TrimSpace(out.Summary) == "" {
		return errors.New("knowledge expander output summary is required")
	}
	switch out.Kind {
	case knowledgeExpanderKindAppend:
		if strings.TrimSpace(out.AppendSection) == "" {
			return errors.New("knowledge expander append_section is required when kind=append")
		}
		if out.ChildNote != nil {
			return errors.New("knowledge expander child_note must be null when kind=append")
		}
		if out.Restructure != nil {
			return errors.New("knowledge expander restructure must be null when kind=append")
		}
	case knowledgeExpanderKindCreateChild:
		if out.ChildNote == nil {
			return errors.New("knowledge expander child_note is required when kind=create_child_note")
		}
		if strings.TrimSpace(out.ChildNote.RelativePath) == "" {
			return errors.New("knowledge expander child_note.relative_path is required")
		}
		if strings.TrimSpace(out.ChildNote.Title) == "" {
			return errors.New("knowledge expander child_note.title is required")
		}
		if strings.TrimSpace(out.ChildNote.DraftBody) == "" {
			return errors.New("knowledge expander child_note.draft_body is required")
		}
		if strings.TrimSpace(out.AppendSection) != "" {
			return errors.New("knowledge expander append_section must be empty when kind=create_child_note")
		}
		if out.Restructure != nil {
			return errors.New("knowledge expander restructure must be null when kind=create_child_note")
		}
		if err := validateChildRelativePath(out.ChildNote.RelativePath); err != nil {
			return err
		}
	case knowledgeExpanderKindRestructure:
		if out.Restructure == nil {
			return errors.New("knowledge expander restructure is required when kind=propose_restructure")
		}
		if !allowedProposalKind(out.Restructure.ProposalKind) {
			return fmt.Errorf("knowledge expander restructure.proposal_kind %q is not allowed", out.Restructure.ProposalKind)
		}
		if len(out.Restructure.AffectedPaths) == 0 {
			return errors.New("knowledge expander restructure.affected_paths must not be empty")
		}
		if out.ChildNote != nil {
			return errors.New("knowledge expander child_note must be null when kind=propose_restructure")
		}
		if strings.TrimSpace(out.AppendSection) != "" {
			return errors.New("knowledge expander append_section must be empty when kind=propose_restructure")
		}
	default:
		return fmt.Errorf("knowledge expander kind %q is not allowed", out.Kind)
	}
	return nil
}

func validateChildRelativePath(rel string) error {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return errors.New("child note relative_path is required")
	}
	if strings.HasPrefix(rel, "/") {
		return errors.New("child note relative_path must not be absolute")
	}
	cleaned := path.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return fmt.Errorf("child note relative_path %q must stay inside the draft directory", rel)
	}
	if !strings.HasSuffix(strings.ToLower(cleaned), ".md") {
		return fmt.Errorf("child note relative_path %q must end with .md", rel)
	}
	return nil
}

func allowedProposalKind(kind string) bool {
	switch kind {
	case model.ProposalKindSplit, model.ProposalKindMerge, model.ProposalKindRename,
		model.ProposalKindBulkRetag, model.ProposalKindBulkLinkRewrite:
		return true
	default:
		return false
	}
}

func buildKnowledgeExpanderPlan(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultPlan, error) {
	switch out.Kind {
	case knowledgeExpanderKindAppend:
		return buildKnowledgeAppendPlan(req, out)
	case knowledgeExpanderKindCreateChild:
		return buildKnowledgeCreateChildPlan(req, out)
	case knowledgeExpanderKindRestructure:
		return buildKnowledgeRestructurePlan(req, out)
	default:
		return model.VaultPlan{}, fmt.Errorf("knowledge expander kind %q is not allowed", out.Kind)
	}
}

func buildKnowledgeAppendPlan(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultPlan, error) {
	appendPayload, err := json.Marshal(model.AppendNotePayload{
		Content: ensureLeadingBlankLine(strings.TrimSpace(out.AppendSection)),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            req.Job.ID,
		Purpose:          "expand existing Knowledge note via append",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          strings.TrimSpace(out.Summary),
		SourceRefs:       []string{req.Job.ID, req.TargetPath},
		TargetPaths:      []string{req.TargetPath},
		Operations: []model.VaultOperation{{
			ID:          model.NewID("op"),
			Type:        model.OperationAppendNote,
			TargetPath:  req.TargetPath,
			PayloadJSON: string(appendPayload),
			Reason:      "Append an LLM-organized section to an existing Knowledge note.",
			RiskLevel:   model.RiskMedium,
		}},
		Status:    model.PlanStatusProposed,
		CreatedAt: req.Now,
	}, nil
}

func buildKnowledgeCreateChildPlan(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultPlan, error) {
	conv := req.Conventions.Normalize()
	childPath := joinVaultPath(conv.KnowledgeDraftDir, strings.TrimSpace(out.ChildNote.RelativePath))
	content := renderKnowledgeExpanderChildDraft(req, out, childPath, conv.RequiredDraftTags)
	createPayload, err := json.Marshal(model.CreateNotePayload{Content: content})
	if err != nil {
		return model.VaultPlan{}, err
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            req.Job.ID,
		Purpose:          "expand existing Knowledge note via child draft",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          strings.TrimSpace(out.Summary),
		SourceRefs:       []string{req.Job.ID, req.TargetPath},
		TargetPaths:      []string{childPath},
		Operations: []model.VaultOperation{{
			ID:          model.NewID("op"),
			Type:        model.OperationCreateNote,
			TargetPath:  childPath,
			PayloadJSON: string(createPayload),
			Reason:      "Create a draft child note expanding the target Knowledge note.",
			RiskLevel:   model.RiskMedium,
		}},
		Status:    model.PlanStatusProposed,
		CreatedAt: req.Now,
	}, nil
}

func buildKnowledgeRestructurePlan(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultPlan, error) {
	var (
		operations []model.VaultOperation
		op         model.VaultOperation
		err        error
	)
	switch out.Restructure.ProposalKind {
	case model.ProposalKindRename:
		op, err = buildKnowledgeRestructureRenameOp(req, out)
	case model.ProposalKindBulkRetag:
		op, err = buildKnowledgeRestructureBulkRetagOp(req, out)
	case model.ProposalKindBulkLinkRewrite:
		op, err = buildKnowledgeRestructureBulkLinkRewriteOp(req, out)
	case model.ProposalKindSplit, model.ProposalKindMerge:
		// split / merge are represented as a rename of the target note plus
		// affected_paths; first version models them as a single rename op
		// so policy.ClassifyProposalKind can route them to proposal output.
		op, err = buildKnowledgeRestructureRenameOp(req, out)
	default:
		return model.VaultPlan{}, fmt.Errorf("knowledge expander restructure.proposal_kind %q is not supported", out.Restructure.ProposalKind)
	}
	if err != nil {
		return model.VaultPlan{}, err
	}
	operations = append(operations, op)
	targetPaths := append([]string{req.TargetPath}, out.Restructure.AffectedPaths...)
	sourceRefs := append([]string{req.Job.ID, req.TargetPath}, out.Restructure.AffectedPaths...)
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            req.Job.ID,
		Purpose:          "propose Knowledge restructure (" + out.Restructure.ProposalKind + ")",
		RiskLevel:        model.RiskHigh,
		RequiresApproval: true,
		Summary:          strings.TrimSpace(out.Summary),
		SourceRefs:       sourceRefs,
		TargetPaths:      targetPaths,
		Operations:       operations,
		Status:           model.PlanStatusProposed,
		CreatedAt:        req.Now,
	}, nil
}

func buildKnowledgeRestructureRenameOp(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultOperation, error) {
	destination := req.TargetPath
	if len(out.Restructure.AffectedPaths) >= 2 {
		destination = out.Restructure.AffectedPaths[1]
	}
	payload, err := json.Marshal(model.RenameNotePayload{
		SourcePath:      req.TargetPath,
		DestinationPath: destination,
		Reason:          strings.TrimSpace(out.Restructure.Rationale),
	})
	if err != nil {
		return model.VaultOperation{}, err
	}
	return model.VaultOperation{
		ID:          model.NewID("op"),
		Type:        model.OperationRenameNote,
		TargetPath:  req.TargetPath,
		PayloadJSON: string(payload),
		Reason:      "Knowledge expander surfaced a high-risk restructure proposal.",
		RiskLevel:   model.RiskHigh,
	}, nil
}

func buildKnowledgeRestructureBulkRetagOp(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultOperation, error) {
	payload, err := json.Marshal(model.BulkRetagPayload{
		AffectedPaths: out.Restructure.AffectedPaths,
		Reason:        strings.TrimSpace(out.Restructure.Rationale),
	})
	if err != nil {
		return model.VaultOperation{}, err
	}
	return model.VaultOperation{
		ID:          model.NewID("op"),
		Type:        model.OperationBulkRetag,
		TargetPath:  req.TargetPath,
		PayloadJSON: string(payload),
		Reason:      "Knowledge expander surfaced a bulk-retag proposal.",
		RiskLevel:   model.RiskHigh,
	}, nil
}

func buildKnowledgeRestructureBulkLinkRewriteOp(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultOperation, error) {
	destination := req.TargetPath
	if len(out.Restructure.AffectedPaths) >= 2 {
		destination = out.Restructure.AffectedPaths[1]
	}
	payload, err := json.Marshal(model.BulkLinkRewritePayload{
		FromPath:      req.TargetPath,
		ToPath:        destination,
		AffectedPaths: out.Restructure.AffectedPaths,
		Reason:        strings.TrimSpace(out.Restructure.Rationale),
	})
	if err != nil {
		return model.VaultOperation{}, err
	}
	return model.VaultOperation{
		ID:          model.NewID("op"),
		Type:        model.OperationBulkLinkRewrite,
		TargetPath:  req.TargetPath,
		PayloadJSON: string(payload),
		Reason:      "Knowledge expander surfaced a bulk-link-rewrite proposal.",
		RiskLevel:   model.RiskHigh,
	}, nil
}

func renderKnowledgeExpanderChildDraft(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput, childPath string, requiredTags []string) string {
	reviewItems := out.ReviewItems
	if len(reviewItems) == 0 {
		reviewItems = []string{"核对扩展内容是否与目标 Knowledge note 一致。"}
	}
	var reviewLines []string
	for _, item := range reviewItems {
		item = strings.TrimSpace(item)
		if item != "" {
			reviewLines = append(reviewLines, "- "+item)
		}
	}
	if len(reviewLines) == 0 {
		reviewLines = []string{"- 核对扩展内容是否与目标 Knowledge note 一致。"}
	}
	return fmt.Sprintf(`---
openwhisker_job_id: %s
openwhisker_job_type: %s
source_knowledge_path: %s
status: draft
needs_review: true
created_at: %s
tags:
%s
---

# %s

## 摘要

%s

## 笔记

%s

## 待核查

%s

## Source

- Source Knowledge note: %s
- Plan job: %s
- Child draft path: %s
`, req.Job.ID, req.Job.Type, req.TargetPath, req.Now.Format(time.RFC3339), renderYAMLList(requiredTags),
		strings.TrimSpace(out.ChildNote.Title), strings.TrimSpace(out.Summary), strings.TrimSpace(out.ChildNote.DraftBody),
		strings.Join(reviewLines, "\n"), req.TargetPath, req.Job.ID, childPath)
}

func ensureLeadingBlankLine(content string) string {
	if content == "" {
		return content
	}
	return "\n\n" + content + "\n"
}
