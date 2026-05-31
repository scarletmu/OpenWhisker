package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

type OpenAIKnowledgeExpander struct {
	Client OpenAICompatibleClient
}

type knowledgeExpanderLLMOutput struct {
	Kind          string                     `json:"kind"`
	Title         string                     `json:"title"`
	Summary       string                     `json:"summary"`
	AppendSection string                     `json:"append_section"`
	Restructure   *knowledgeExpanderRestruct `json:"restructure"`
	ReviewItems   []string                   `json:"review_items"`
}

type knowledgeExpanderRestruct struct {
	ProposalKind  string   `json:"proposal_kind"`
	Rationale     string   `json:"rationale"`
	AffectedPaths []string `json:"affected_paths"`
}

const (
	knowledgeExpanderKindAppend      = "append"
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
	return createResponseWithRetry(ctx, o.Client, req, "knowledge expander")
}

func knowledgeExpanderInstructions() string {
	return strings.TrimSpace(`You are OpenWhisker Knowledge Expander.

Return ONLY a single JSON object. No prose, no markdown fences, no comments, no trailing text.

The JSON object MUST contain these fields:

- kind: one of ["append", "propose_restructure"].
- title: short Chinese title for the human approval prompt.
- summary: one or two Chinese sentences describing the change.
- append_section: string. Required when kind=append; Markdown section to append at the end of the target note. Must NOT contain frontmatter and MUST start with an H2 heading. Empty string when kind!=append.
- restructure: object. Required when kind=propose_restructure; must be null otherwise. Fields: { proposal_kind: one of ["split","merge","rename"], rationale: string, affected_paths: string array }. For split / merge the array must list the source note followed by at least one destination path.
- review_items: array of strings. Items the user should still verify. May be empty.

Example JSON output (append):
{"kind":"append","title":"补充 Phase4 笔记","summary":"在已有 Knowledge note 末尾追加 raw 中的新结论。","append_section":"## 新增结论\n\n- ...","restructure":null,"review_items":["确认新增结论来源是否充分。"]}

OpenWhisker Knowledge Expander only directly produces append operations. If the target note needs a new child note, a split, a merge, a rename, or bulk tag / link changes, return kind=propose_restructure with a clear rationale and the full affected_paths list. OpenWhisker will turn that into a proposal note for the human to review and execute manually (often via a stronger external tool); do NOT attempt to create child notes or execute the restructure here.

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
		if out.Restructure != nil {
			return errors.New("knowledge expander restructure must be null when kind=append")
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
		// split / merge both encode the affected children in
		// out.Restructure.AffectedPaths and need at least one destination beyond
		// the source. The downstream rename op uses AffectedPaths[1] as the
		// destination; with len<2 it would silently degrade to source==dest and
		// the proposal note would record a no-op move.
		switch out.Restructure.ProposalKind {
		case model.ProposalKindSplit, model.ProposalKindMerge:
			if len(out.Restructure.AffectedPaths) < 2 {
				return fmt.Errorf("knowledge expander %s requires >= 2 affected_paths (source + at least one destination), got %d",
					out.Restructure.ProposalKind, len(out.Restructure.AffectedPaths))
			}
		}
		if strings.TrimSpace(out.AppendSection) != "" {
			return errors.New("knowledge expander append_section must be empty when kind=propose_restructure")
		}
	default:
		return fmt.Errorf("knowledge expander kind %q is not allowed", out.Kind)
	}
	return nil
}

// allowedProposalKind is the contract surface the Knowledge Expander emits.
// bulk-retag / bulk-link-rewrite are intentionally excluded for v1: the
// `knowledgeExpanderRestruct` JSON shape does not carry tag deltas or link
// rewrites, so any plan built from those kinds is guaranteed to fail
// downstream `CheckForApprovalHighRisk` validation. Re-introduce them once
// the model contract gains the necessary delta fields.
func allowedProposalKind(kind string) bool {
	switch kind {
	case model.ProposalKindSplit, model.ProposalKindMerge, model.ProposalKindRename:
		return true
	default:
		return false
	}
}

func buildKnowledgeExpanderPlan(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultPlan, error) {
	switch out.Kind {
	case knowledgeExpanderKindAppend:
		return buildKnowledgeAppendPlan(req, out)
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
	sourceRefs := []string{req.Job.ID, req.TargetPath}
	for _, doc := range req.VaultContext.RelatedNotes {
		sourceRefs = append(sourceRefs, doc.Path)
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            req.Job.ID,
		Purpose:          "expand existing Knowledge note via append",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          strings.TrimSpace(out.Summary),
		SourceRefs:       sourceRefs,
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

func buildKnowledgeRestructurePlan(req core.KnowledgeExpanderRequest, out knowledgeExpanderLLMOutput) (model.VaultPlan, error) {
	var (
		operations []model.VaultOperation
		op         model.VaultOperation
		err        error
	)
	switch out.Restructure.ProposalKind {
	case model.ProposalKindRename, model.ProposalKindSplit, model.ProposalKindMerge:
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
	for _, doc := range req.VaultContext.RelatedNotes {
		sourceRefs = append(sourceRefs, doc.Path)
	}
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

func ensureLeadingBlankLine(content string) string {
	if content == "" {
		return content
	}
	return "\n\n" + content + "\n"
}
