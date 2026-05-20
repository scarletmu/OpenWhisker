package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
)

const (
	DefaultOpenAICompatibleBaseURL = "https://api.openai.com/v1"
	DefaultOpenAICompatibleModel   = "gpt-4o-mini"
)

type OpenAIClient struct {
	APIKey        string
	BaseURL       string
	Model         string
	MaxOutput     int
	HTTPClient    *http.Client
	Organization  string
	Project       string
	PromptVersion string
}

type OpenAIRawOrganizer struct {
	Client OpenAICompatibleClient
}

type OpenAICompatibleClient interface {
	CreateResponse(context.Context, openAIResponseRequest) (string, error)
}

type openAIResponseRequest struct {
	Model           string         `json:"model"`
	Instructions    string         `json:"instructions"`
	Input           string         `json:"input"`
	Text            openAITextSpec `json:"text"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Store           bool           `json:"store"`
}

type openAITextSpec struct {
	Format openAITextFormat `json:"format"`
}

type openAITextFormat struct {
	Type                 string         `json:"type"`
	Name                 string         `json:"name"`
	Description          string         `json:"description,omitempty"`
	Strict               bool           `json:"strict"`
	Schema               map[string]any `json:"schema"`
	AdditionalProperties bool           `json:"additionalProperties,omitempty"`
}

type rawOrganizerLLMOutput struct {
	Title       string   `json:"title"`
	RawKind     string   `json:"raw_kind"`
	Summary     string   `json:"summary"`
	DraftBody   string   `json:"draft_body"`
	ReviewItems []string `json:"review_items"`
}

type openAIChatCompletionRequest struct {
	Model          string                     `json:"model"`
	Messages       []openAIChatMessage        `json:"messages"`
	ResponseFormat openAIChatCompletionFormat `json:"response_format"`
	MaxTokens      int                        `json:"max_tokens,omitempty"`
	Temperature    *float64                   `json:"temperature,omitempty"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatCompletionFormat struct {
	Type       string            `json:"type"`
	JSONSchema *openAIJSONSchema `json:"json_schema,omitempty"`
}

type openAIJSONSchema struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Strict      bool           `json:"strict"`
	Schema      map[string]any `json:"schema"`
}

type openAIChatCompletionResponse struct {
	ID      string `json:"id"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o OpenAIRawOrganizer) OrganizeRaw(ctx context.Context, req core.RawOrganizerRequest) (model.VaultPlan, error) {
	if o.Client == nil {
		return model.VaultPlan{}, errors.New("openai raw organizer client is required")
	}
	outputText, err := o.createWithRetry(ctx, openAIResponseRequest{
		Instructions:    rawOrganizerInstructions(),
		Input:           renderRawOrganizerInput(req),
		Text:            openAITextSpec{Format: rawOrganizerResponseFormat()},
		MaxOutputTokens: 4096,
		Store:           false,
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	var output rawOrganizerLLMOutput
	if err := json.Unmarshal([]byte(outputText), &output); err != nil {
		return model.VaultPlan{}, fmt.Errorf("decode raw organizer structured output: %w", err)
	}
	if err := validateRawOrganizerOutput(output); err != nil {
		return model.VaultPlan{}, err
	}
	return buildLLMOrganizePlan(req, output)
}

func (o OpenAIRawOrganizer) OrganizeRawToday(ctx context.Context, req core.RawTodayOrganizerRequest) (model.VaultPlan, error) {
	if o.Client == nil {
		return model.VaultPlan{}, errors.New("openai raw organizer client is required")
	}
	outputText, err := o.createWithRetry(ctx, openAIResponseRequest{
		Instructions:    rawOrganizerInstructions(),
		Input:           renderRawTodayOrganizerInput(req),
		Text:            openAITextSpec{Format: rawOrganizerResponseFormat()},
		MaxOutputTokens: 4096,
		Store:           false,
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	var output rawOrganizerLLMOutput
	if err := json.Unmarshal([]byte(outputText), &output); err != nil {
		return model.VaultPlan{}, fmt.Errorf("decode raw today organizer structured output: %w", err)
	}
	if err := validateRawOrganizerOutput(output); err != nil {
		return model.VaultPlan{}, err
	}
	return buildLLMTodayPlan(req, output)
}

func (o OpenAIRawOrganizer) createWithRetry(ctx context.Context, req openAIResponseRequest) (string, error) {
	out, err := o.Client.CreateResponse(ctx, req)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) != "" {
		return out, nil
	}
	out, err = o.Client.CreateResponse(ctx, req)
	if err != nil {
		return "", fmt.Errorf("raw organizer retry after empty content failed: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return "", errors.New("raw organizer returned empty content after one retry")
	}
	return out, nil
}

func (c OpenAIClient) CreateResponse(ctx context.Context, req openAIResponseRequest) (string, error) {
	if strings.TrimSpace(c.APIKey) == "" {
		return "", errors.New("openai-compatible api key is required")
	}
	if strings.TrimSpace(req.Model) == "" {
		req.Model = c.model()
	}
	if req.MaxOutputTokens == 0 {
		req.MaxOutputTokens = c.maxOutput()
	}
	body, err := json.Marshal(openAIChatCompletionRequest{
		Model: req.Model,
		Messages: []openAIChatMessage{
			{Role: "system", Content: req.Instructions},
			{Role: "user", Content: req.Input},
		},
		ResponseFormat: chatCompletionFormat(req.Text.Format),
		MaxTokens:      req.MaxOutputTokens,
	})
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.baseURL(), "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Organization != "" {
		httpReq.Header.Set("OpenAI-Organization", c.Organization)
	}
	if c.Project != "" {
		httpReq.Header.Set("OpenAI-Project", c.Project)
	}
	resp, err := c.httpClient().Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openai-compatible chat completions api returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var decoded openAIChatCompletionResponse
	if err := json.Unmarshal(respBody, &decoded); err != nil {
		return "", fmt.Errorf("decode openai-compatible chat completion response: %w", err)
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return "", errors.New(decoded.Error.Message)
	}
	text := extractOpenAIChatCompletionText(decoded)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("openai-compatible chat completion %s produced no output text", decoded.ID)
	}
	return text, nil
}

func (c OpenAIClient) baseURL() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return c.BaseURL
	}
	return DefaultOpenAICompatibleBaseURL
}

func (c OpenAIClient) model() string {
	if strings.TrimSpace(c.Model) != "" {
		return c.Model
	}
	return DefaultOpenAICompatibleModel
}

func (c OpenAIClient) maxOutput() int {
	if c.MaxOutput > 0 {
		return c.MaxOutput
	}
	return 4096
}

func (c OpenAIClient) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func chatCompletionFormat(format openAITextFormat) openAIChatCompletionFormat {
	if format.Type != "json_schema" {
		return openAIChatCompletionFormat{Type: format.Type}
	}
	return openAIChatCompletionFormat{
		Type: format.Type,
		JSONSchema: &openAIJSONSchema{
			Name:        format.Name,
			Description: format.Description,
			Strict:      format.Strict,
			Schema:      format.Schema,
		},
	}
}

func extractOpenAIChatCompletionText(resp openAIChatCompletionResponse) string {
	var parts []string
	for _, choice := range resp.Choices {
		if strings.TrimSpace(choice.Message.Content) != "" {
			parts = append(parts, choice.Message.Content)
		}
	}
	return strings.Join(parts, "\n")
}

func rawOrganizerInstructions() string {
	return strings.TrimSpace(`You are OpenWhisker Raw Organizer.

Return ONLY a single JSON object. No prose, no markdown fences, no comments, no trailing text.

The JSON object MUST contain exactly these fields:

- title: string. Short Chinese title for the Knowledge draft.
- raw_kind: one of ["concept-seed", "web-clip", "todo-list", "llm-chat", "mixed"].
- summary: string. One or two Chinese sentences summarizing the intended draft.
- draft_body: string. Chinese markdown body without YAML frontmatter and without a top-level H1.
- review_items: array of strings. Items the user should still verify.

Example JSON output:
{"title":"Phase4 记忆模型","raw_kind":"concept-seed","summary":"整理 Phase4 agent 记忆模型。","draft_body":"## 核心观点\n\nagent 的长期记忆来自 vault。","review_items":["确认是否需要补充 Matrix 入口说明。"]}

Follow the task-specific Vault Skill documents in the provided context.
Generate Chinese knowledge draft content by default.
Preserve source traceability and mark uncertain claims for review.
Do not ask to write files, run shell, call Obsidian CLI, or bypass approval.`)
}

func renderRawOrganizerInput(req core.RawOrganizerRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Organize this raw Obsidian note into one reviewable Knowledge draft.\n")
	fmt.Fprintf(&b, "Raw job id: %s\n", req.RawJob.ID)
	fmt.Fprintf(&b, "Raw path: %s\n", req.RawPath)
	fmt.Fprintf(&b, "Plan job id: %s\n\n", req.Job.ID)
	for _, doc := range req.VaultContext.Documents {
		fmt.Fprintf(&b, "## Context: %s\n%s\n\n", doc.Path, limitString(doc.Content, 6000))
	}
	fmt.Fprintf(&b, "## Raw note\n%s\n", limitString(req.VaultContext.RawNote, 24000))
	return b.String()
}

func renderRawTodayOrganizerInput(req core.RawTodayOrganizerRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Organize today's raw Obsidian notes into one grouped reviewable Knowledge draft.\n")
	fmt.Fprintf(&b, "Date: %s\n", req.Day.Format("2006-01-02"))
	fmt.Fprintf(&b, "Plan job id: %s\n\n", req.Job.ID)
	if len(req.VaultContext) > 0 {
		for _, doc := range req.VaultContext[0].Documents {
			fmt.Fprintf(&b, "## Context: %s\n%s\n\n", doc.Path, limitString(doc.Content, 6000))
		}
	}
	for i, rawJob := range req.RawJobs {
		fmt.Fprintf(&b, "## Raw note %d\n", i+1)
		fmt.Fprintf(&b, "Raw job id: %s\n", rawJob.ID)
		fmt.Fprintf(&b, "Raw path: %s\n", req.RawPaths[i])
		fmt.Fprintf(&b, "%s\n\n", limitString(req.VaultContext[i].RawNote, 12000))
	}
	return b.String()
}

func rawOrganizerResponseFormat() openAITextFormat {
	return openAITextFormat{Type: "json_object"}
}

func validateRawOrganizerOutput(output rawOrganizerLLMOutput) error {
	if strings.TrimSpace(output.Title) == "" {
		return errors.New("raw organizer output title is required")
	}
	if !allowedRawKind(output.RawKind) {
		return fmt.Errorf("raw organizer output raw_kind %q is not allowed", output.RawKind)
	}
	if strings.TrimSpace(output.Summary) == "" {
		return errors.New("raw organizer output summary is required")
	}
	if strings.TrimSpace(output.DraftBody) == "" {
		return errors.New("raw organizer output draft_body is required")
	}
	return nil
}

func allowedRawKind(kind string) bool {
	switch kind {
	case "concept-seed", "web-clip", "todo-list", "llm-chat", "mixed":
		return true
	default:
		return false
	}
}

func buildLLMOrganizePlan(req core.RawOrganizerRequest, output rawOrganizerLLMOutput) (model.VaultPlan, error) {
	conventions := req.Conventions.Normalize()
	base := strings.TrimSuffix(filepath.Base(req.RawPath), filepath.Ext(req.RawPath))
	knowledgePath := joinVaultPath(conventions.KnowledgeDraftDir, base+".md")
	processedPath := joinVaultPath(conventions.RawProcessedDir, base+".md")
	createPayload, err := json.Marshal(model.CreateNotePayload{
		Content: renderLLMKnowledgeDraft(req, output, processedPath, conventions.RequiredDraftTags),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	movePayload, err := json.Marshal(model.MoveNotePayload{
		DestinationPath: processedPath,
		ProcessingNote:  renderLLMProcessedRawNote(req, output, processedPath, []string{knowledgePath}),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	operations := []model.VaultOperation{
		{
			ID:          model.NewID("op"),
			Type:        model.OperationCreateNote,
			TargetPath:  knowledgePath,
			PayloadJSON: string(createPayload),
			Reason:      "Create an LLM-organized Knowledge draft with source traceability and review markers.",
			RiskLevel:   model.RiskMedium,
		},
		{
			ID:          model.NewID("op"),
			Type:        model.OperationMoveNote,
			TargetPath:  req.RawPath,
			PayloadJSON: string(movePayload),
			Reason:      "Mark the raw capture as processed only after the Knowledge draft is approved.",
			RiskLevel:   model.RiskMedium,
		},
	}
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            req.Job.ID,
		Purpose:          "organize raw into Knowledge draft with LLM",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          output.Summary,
		SourceRefs:       []string{req.RawJob.ID, req.RawPath},
		TargetPaths:      []string{knowledgePath, req.RawPath, processedPath},
		Operations:       operations,
		Status:           model.PlanStatusProposed,
		CreatedAt:        req.Now,
	}, nil
}

func buildLLMTodayPlan(req core.RawTodayOrganizerRequest, output rawOrganizerLLMOutput) (model.VaultPlan, error) {
	if len(req.RawJobs) == 0 || len(req.RawJobs) != len(req.RawPaths) {
		return model.VaultPlan{}, errors.New("raw today plan requires matching raw jobs and paths")
	}
	conventions := req.Conventions.Normalize()
	date := req.Day.Format("2006-01-02")
	knowledgePath := joinVaultPath(conventions.KnowledgeDraftDir, fmt.Sprintf("%s-raw-review-%s.md", date, req.Job.ID))
	var processedPaths []string
	for _, rawPath := range req.RawPaths {
		base := strings.TrimSuffix(filepath.Base(rawPath), filepath.Ext(rawPath))
		processedPaths = append(processedPaths, joinVaultPath(conventions.RawProcessedDir, base+".md"))
	}
	createPayload, err := json.Marshal(model.CreateNotePayload{
		Content: renderLLMTodayKnowledgeDraft(req, output, processedPaths, conventions.RequiredDraftTags),
	})
	if err != nil {
		return model.VaultPlan{}, err
	}
	operations := []model.VaultOperation{{
		ID:          model.NewID("op"),
		Type:        model.OperationCreateNote,
		TargetPath:  knowledgePath,
		PayloadJSON: string(createPayload),
		Reason:      "Create an LLM-organized grouped Knowledge draft for today's raw captures.",
		RiskLevel:   model.RiskMedium,
	}}
	for i, rawPath := range req.RawPaths {
		movePayload, err := json.Marshal(model.MoveNotePayload{
			DestinationPath: processedPaths[i],
			ProcessingNote:  renderLLMTodayProcessedRawNote(req, output, i, processedPaths[i], []string{knowledgePath}),
		})
		if err != nil {
			return model.VaultPlan{}, err
		}
		operations = append(operations, model.VaultOperation{
			ID:          model.NewID("op"),
			Type:        model.OperationMoveNote,
			TargetPath:  rawPath,
			PayloadJSON: string(movePayload),
			Reason:      "Mark the raw capture as processed only after the grouped Knowledge draft is approved.",
			RiskLevel:   model.RiskMedium,
		})
	}
	targetPaths := append([]string{knowledgePath}, req.RawPaths...)
	targetPaths = append(targetPaths, processedPaths...)
	sourceRefs := append([]string{}, rawTodayJobIDs(req.RawJobs)...)
	sourceRefs = append(sourceRefs, req.RawPaths...)
	return model.VaultPlan{
		ID:               model.NewID("plan"),
		JobID:            req.Job.ID,
		Purpose:          "organize today's raw captures into a grouped Knowledge draft with LLM",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          output.Summary,
		SourceRefs:       sourceRefs,
		TargetPaths:      targetPaths,
		Operations:       operations,
		Status:           model.PlanStatusProposed,
		CreatedAt:        req.Now,
	}, nil
}

func renderLLMKnowledgeDraft(req core.RawOrganizerRequest, output rawOrganizerLLMOutput, processedPath string, requiredTags []string) string {
	reviewItems := output.ReviewItems
	if len(reviewItems) == 0 {
		reviewItems = []string{"核对从 raw 输入推断出的内容是否准确。"}
	}
	var reviewLines []string
	for _, item := range reviewItems {
		item = strings.TrimSpace(item)
		if item != "" {
			reviewLines = append(reviewLines, "> - "+item)
		}
	}
	if len(reviewLines) == 0 {
		reviewLines = []string{"> - 核对从 raw 输入推断出的内容是否准确。"}
	}
	return fmt.Sprintf(`---
title: "%s"
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
  raw_kind: %s
  created_at: %s
---

# %s

> [!todo] OpenWhisker Raw Organizer 草稿
> 由 OpenWhisker 从 raw 输入整理。请人工审阅 → 补全 → 转写为正式 Knowledge note 后归档此 draft。

## 摘要

%s

## 笔记

%s

## 来源

- [[%s]]

## 待核查

> [!todo] 待核查
%s
`,
		escapeYAMLString(strings.TrimSpace(output.Title)),
		renderYAMLList(mergeKnowledgeDraftTags(requiredTags)),
		trimVaultExt(processedPath),
		req.Job.ID, req.Job.Type, req.RawJob.ID, req.RawPath, processedPath, output.RawKind,
		req.Now.Format(time.RFC3339),
		strings.TrimSpace(output.Title),
		strings.TrimSpace(output.Summary),
		strings.TrimSpace(output.DraftBody),
		trimVaultExt(processedPath),
		strings.Join(reviewLines, "\n"),
	)
}

func renderLLMTodayKnowledgeDraft(req core.RawTodayOrganizerRequest, output rawOrganizerLLMOutput, processedPaths []string, requiredTags []string) string {
	var rawJobLines, rawPathLines, processedPathLines, relatedLines, sourceLines []string
	for i, rawJob := range req.RawJobs {
		rawPath := req.RawPaths[i]
		processedPath := processedPaths[i]
		rawJobLines = append(rawJobLines, "    - "+rawJob.ID)
		rawPathLines = append(rawPathLines, "    - "+rawPath)
		processedPathLines = append(processedPathLines, "    - "+processedPath)
		relatedLines = append(relatedLines, fmt.Sprintf("  - \"[[%s]]\"", trimVaultExt(processedPath)))
		sourceLines = append(sourceLines, fmt.Sprintf("- [[%s]]", trimVaultExt(processedPath)))
	}
	reviewItems := output.ReviewItems
	if len(reviewItems) == 0 {
		reviewItems = []string{"核对从 raw 输入推断出的内容是否准确。"}
	}
	var reviewLines []string
	for _, item := range reviewItems {
		item = strings.TrimSpace(item)
		if item != "" {
			reviewLines = append(reviewLines, "> - "+item)
		}
	}
	if len(reviewLines) == 0 {
		reviewLines = []string{"> - 核对从 raw 输入推断出的内容是否准确。"}
	}
	return fmt.Sprintf(`---
title: "%s"
tags:
%s
related:
%s
openwhisker:
  job_id: %s
  job_type: %s
  raw_job_id: batch
  raw_path: %s
  processed_path: %s
  raw_job_ids:
%s
  raw_paths:
%s
  processed_paths:
%s
  raw_kind: %s
  created_at: %s
---

# %s

> [!todo] OpenWhisker Raw Organizer 草稿
> 由 OpenWhisker 从 raw 输入整理。请人工审阅 → 补全 → 转写为正式 Knowledge note 后归档此 draft。

## 摘要

%s

## 笔记

%s

## 来源

%s

## 待核查

> [!todo] 待核查
%s
`,
		escapeYAMLString(strings.TrimSpace(output.Title)),
		renderYAMLList(mergeKnowledgeDraftTags(requiredTags)),
		strings.Join(relatedLines, "\n"),
		req.Job.ID, req.Job.Type, req.Conventions.RawInboxDir, processedPaths[0],
		strings.Join(rawJobLines, "\n"), strings.Join(rawPathLines, "\n"), strings.Join(processedPathLines, "\n"),
		output.RawKind, req.Now.Format(time.RFC3339),
		strings.TrimSpace(output.Title), strings.TrimSpace(output.Summary), strings.TrimSpace(output.DraftBody),
		strings.Join(sourceLines, "\n"), strings.Join(reviewLines, "\n"),
	)
}

func renderLLMProcessedRawNote(req core.RawOrganizerRequest, output rawOrganizerLLMOutput, processedPath string, outputPaths []string) string {
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
	var reviewLines []string
	for _, item := range output.ReviewItems {
		item = strings.TrimSpace(item)
		if item != "" {
			reviewLines = append(reviewLines, "> - "+item)
		}
	}
	if len(reviewLines) == 0 {
		reviewLines = []string{"> - 核对从 raw 输入推断出的内容是否准确。"}
	}
	return fmt.Sprintf(`

---

> [!note] OpenWhisker Processing
> 由 OpenWhisker 处理为 Knowledge draft；以下为处理元信息与输出。

### Outputs

%s

### Processing Note

%s

### Remaining Review

> [!todo] Remaining Review
%s

### Trace

- plan_job: `+"`%s`"+`
- raw_job: `+"`%s`"+`
- raw_path: `+"`%s`"+`
- processed_path: `+"`%s`"+`
- processed_at: `+"`%s`"+`
- raw_kind: `+"`%s`"+`
`,
		strings.Join(outputLines, "\n"),
		strings.TrimSpace(output.Summary),
		strings.Join(reviewLines, "\n"),
		req.Job.ID, req.RawJob.ID, req.RawPath, processedPath, req.Now.Format(time.RFC3339), output.RawKind,
	)
}

func renderLLMTodayProcessedRawNote(req core.RawTodayOrganizerRequest, output rawOrganizerLLMOutput, index int, processedPath string, outputPaths []string) string {
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
	var reviewLines []string
	for _, item := range output.ReviewItems {
		item = strings.TrimSpace(item)
		if item != "" {
			reviewLines = append(reviewLines, "> - "+item)
		}
	}
	if len(reviewLines) == 0 {
		reviewLines = []string{"> - 核对从 raw 输入推断出的内容是否准确。"}
	}
	return fmt.Sprintf(`

---

> [!note] OpenWhisker Processing
> 由 OpenWhisker 处理为 Knowledge draft；以下为处理元信息与输出。

### Outputs

%s

### Processing Note

Grouped by OpenWhisker Raw Organizer for %s. %s

### Remaining Review

> [!todo] Remaining Review
%s

### Trace

- plan_job: `+"`%s`"+`
- raw_job: `+"`%s`"+`
- raw_path: `+"`%s`"+`
- processed_path: `+"`%s`"+`
- processed_at: `+"`%s`"+`
- raw_kind: `+"`%s`"+`
`,
		strings.Join(outputLines, "\n"),
		req.Day.Format("2006-01-02"), strings.TrimSpace(output.Summary),
		strings.Join(reviewLines, "\n"),
		req.Job.ID, req.RawJobs[index].ID, req.RawPaths[index], processedPath, req.Now.Format(time.RFC3339), output.RawKind,
	)
}

func rawTodayJobIDs(jobs []model.WikiJob) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}

func limitString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[truncated]"
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

func escapeYAMLString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return s
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
