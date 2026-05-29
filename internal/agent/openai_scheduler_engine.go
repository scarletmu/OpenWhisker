package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/scarletmu/openwhisker/internal/scheduler"
)

type OpenAISchedulerEngine struct {
	Client OpenAICompatibleClient
}

type schedulerEngineLLMOutput struct {
	Title   string          `json:"title"`
	Summary string          `json:"summary"`
	Payload json.RawMessage `json:"payload"`
}

func (e OpenAISchedulerEngine) RunSkill(ctx context.Context, req scheduler.SkillExecutionRequest) (scheduler.SkillRunResult, error) {
	if e.Client == nil {
		return scheduler.SkillRunResult{}, errors.New("openai scheduler engine client is required")
	}
	outputText, err := e.createWithRetry(ctx, openAIResponseRequest{
		Instructions:    schedulerEngineInstructions(),
		Input:           renderSchedulerEngineInput(req),
		Text:            openAITextSpec{Format: rawOrganizerResponseFormat()},
		MaxOutputTokens: 4096,
		Store:           false,
	})
	if err != nil {
		return scheduler.SkillRunResult{}, err
	}
	var output schedulerEngineLLMOutput
	if err := json.Unmarshal([]byte(outputText), &output); err != nil {
		return scheduler.SkillRunResult{}, fmt.Errorf("decode scheduler engine structured output: %w", err)
	}
	if err := validateSchedulerEngineOutput(output); err != nil {
		return scheduler.SkillRunResult{}, err
	}
	payload := output.Payload
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	return scheduler.SkillRunResult{
		ScheduleID: req.Schedule.ID,
		SkillID:    req.Schedule.ID,
		Title:      strings.TrimSpace(output.Title),
		Summary:    strings.TrimSpace(output.Summary),
		Payload:    payload,
	}, nil
}

func (e OpenAISchedulerEngine) createWithRetry(ctx context.Context, req openAIResponseRequest) (string, error) {
	return createResponseWithRetry(ctx, e.Client, req, "scheduler engine")
}

func schedulerEngineInstructions() string {
	return strings.TrimSpace(`You are OpenWhisker Scheduler Engine.

Return ONLY a single JSON object. No prose, no markdown fences, no comments, no trailing text.

The JSON object MUST contain exactly these top-level fields:

- title: short human-facing title.
- summary: human-facing summary. Use Chinese by default; keep fixed technical terms, paths, APIs, and protocol names in English.
- payload: JSON object owned by this scheduled skill. It may contain observations, reminders, suggested_raw_captures, candidate_questions, review_items, or source summaries.

You are running inside a read-only Scheduler Host. You may only use the provided SKILL.md, schedule metadata, vault_context, external_info, and skill_config. Do not claim to have read files or external systems that are not present in the input.

Hard prohibitions:
- Do not write, move, rename, delete, or create vault notes.
- Do not output a VaultPlan, VaultOperation, approval command, shell command, HTTP request, Obsidian CLI command, or executable instruction.
- Do not ask OpenWhisker to bypass approval, call VaultExecutor, auto-approve, or perform external API side effects.
- If external information should be preserved, place it as a suggested_raw_captures item inside payload, phrased for later human confirmation. Do not say it has been saved.

Keep summaries compact and useful. Mark uncertainty in payload.review_items when source coverage is incomplete.`)
}

func renderSchedulerEngineInput(req scheduler.SkillExecutionRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Schedule id: %s\n", req.Schedule.ID)
	fmt.Fprintf(&b, "Schedule name: %s\n", req.Schedule.Name)
	fmt.Fprintf(&b, "Registry path: %s\n", req.Schedule.RegistryPath)
	fmt.Fprintf(&b, "Skill path: %s\n", req.Schedule.SkillPath)
	fmt.Fprintf(&b, "Run time UTC: %s\n", req.Now.UTC().Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "Capabilities: %s\n", strings.Join(req.Schedule.Capabilities, ", "))
	fmt.Fprintf(&b, "Delivery: %s\n", strings.Join(req.Schedule.Delivery, ", "))
	fmt.Fprintf(&b, "Skill config JSON: %s\n\n", limitString(string(scheduler.SanitizedSkillConfigJSON(req.Schedule)), 6000))
	fmt.Fprintf(&b, "## SKILL.md\n%s\n\n", limitString(req.SkillContent, 12000))
	for _, item := range req.VaultContext {
		fmt.Fprintf(&b, "## Vault context: %s (%s", item.Path, item.Kind)
		if item.Truncated {
			fmt.Fprintf(&b, ", truncated")
		}
		fmt.Fprintf(&b, ")\n")
		if item.Kind == "directory" {
			fmt.Fprintf(&b, "%s\n\n", strings.Join(item.Entries, "\n"))
			continue
		}
		fmt.Fprintf(&b, "%s\n\n", limitString(item.Content, 12000))
	}
	for _, item := range req.ExternalInfo {
		fmt.Fprintf(&b, "## External info: %s\n", item.Source)
		if strings.TrimSpace(item.Summary) != "" {
			fmt.Fprintf(&b, "Summary: %s\n", item.Summary)
		}
		if len(item.Payload) > 0 {
			fmt.Fprintf(&b, "Payload JSON: %s\n", limitString(string(item.Payload), 12000))
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}

func validateSchedulerEngineOutput(output schedulerEngineLLMOutput) error {
	if strings.TrimSpace(output.Title) == "" {
		return errors.New("scheduler engine output title is required")
	}
	if strings.TrimSpace(output.Summary) == "" {
		return errors.New("scheduler engine output summary is required")
	}
	if len(output.Payload) == 0 {
		output.Payload = json.RawMessage(`{}`)
	}
	if !json.Valid(output.Payload) {
		return errors.New("scheduler engine output payload must be valid JSON")
	}
	var payload map[string]any
	if err := json.Unmarshal(output.Payload, &payload); err != nil {
		return errors.New("scheduler engine output payload must be a JSON object")
	}
	for _, forbidden := range []string{"vault_plan", "vault_operations", "shell_command", "http_request", "auto_approve"} {
		if _, ok := payload[forbidden]; ok {
			return fmt.Errorf("scheduler engine payload field %q is not allowed", forbidden)
		}
	}
	return nil
}
