package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/scarletmu/openwhisker/internal/core"
)

type OpenAIIntentClassifier struct {
	Client OpenAICompatibleClient
}

func (c OpenAIIntentClassifier) ClassifyIntent(ctx context.Context, req core.IntentClassifierRequest) (core.IntentClassifierResult, error) {
	if c.Client == nil {
		return core.IntentClassifierResult{}, errors.New("openai intent classifier client is required")
	}
	input, err := json.Marshal(req)
	if err != nil {
		return core.IntentClassifierResult{}, err
	}
	outputText, err := c.Client.CreateResponse(ctx, openAIResponseRequest{
		Instructions:    intentClassifierInstructions(),
		Input:           string(input),
		Text:            openAITextSpec{Format: intentClassifierResponseFormat()},
		MaxOutputTokens: 700,
		Store:           false,
	})
	if err != nil {
		return core.IntentClassifierResult{}, err
	}
	var output core.IntentClassifierResult
	if err := json.Unmarshal([]byte(outputText), &output); err != nil {
		return core.IntentClassifierResult{}, fmt.Errorf("decode intent classifier structured output: %w", err)
	}
	if err := validateIntentClassifierOutput(output); err != nil {
		return core.IntentClassifierResult{}, err
	}
	return output, nil
}

func intentClassifierInstructions() string {
	return strings.TrimSpace(`You classify one inbound message for OpenWhisker.

Return only JSON that matches the schema.

You only classify intent. You do not write notes, create plans, approve plans, or execute actions.
Use the provided minimal state only. Do not infer hidden vault content.

Choose high confidence only when the user intent is direct and actionable.
Use medium when a focused clarification is needed.
Use low/unclear when the message is ambiguous.`)
}

func intentClassifierResponseFormat() openAITextFormat {
	return openAITextFormat{
		Type:        "json_schema",
		Name:        "openwhisker_intent_router",
		Description: "Structured intent classification for OpenWhisker IM input.",
		Strict:      true,
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"intent": map[string]any{
					"type": "string",
					"enum": []string{"raw_capture", "organize_request", "diff_request", "approve_request", "reject_request", "unclear"},
				},
				"target": map[string]any{
					"type": "string",
					"enum": []string{"last", "today", "active", "active_bucket", "new_bucket", "none"},
				},
				"capture_action": map[string]any{
					"type": "string",
					"enum": []string{"create", "append", "close", "none"},
				},
				"bucket_relation": map[string]any{
					"type": "string",
					"enum": []string{"same_topic", "new_topic", "unrelated", "unclear"},
				},
				"payload_text":            map[string]any{"type": "string"},
				"additional_payload_text": map[string]any{"type": "string"},
				"confidence_label": map[string]any{
					"type": "string",
					"enum": []string{"high", "medium", "low"},
				},
				"confidence": map[string]any{
					"type":    "number",
					"minimum": 0,
					"maximum": 1,
				},
				"reason": map[string]any{"type": "string"},
			},
			"required": []string{
				"intent",
				"target",
				"capture_action",
				"bucket_relation",
				"payload_text",
				"additional_payload_text",
				"confidence_label",
				"confidence",
				"reason",
			},
			"additionalProperties": false,
		},
	}
}

func validateIntentClassifierOutput(output core.IntentClassifierResult) error {
	if !oneOf(output.Intent, "raw_capture", "organize_request", "diff_request", "approve_request", "reject_request", "unclear") {
		return fmt.Errorf("invalid intent %q", output.Intent)
	}
	if !oneOf(output.Target, "last", "today", "active", "active_bucket", "new_bucket", "none") {
		return fmt.Errorf("invalid target %q", output.Target)
	}
	if !oneOf(output.CaptureAction, "create", "append", "close", "none") {
		return fmt.Errorf("invalid capture_action %q", output.CaptureAction)
	}
	if !oneOf(output.BucketRelation, "same_topic", "new_topic", "unrelated", "unclear") {
		return fmt.Errorf("invalid bucket_relation %q", output.BucketRelation)
	}
	if !oneOf(output.ConfidenceLabel, "high", "medium", "low") {
		return fmt.Errorf("invalid confidence_label %q", output.ConfidenceLabel)
	}
	if output.Confidence < 0 || output.Confidence > 1 {
		return fmt.Errorf("invalid confidence %v", output.Confidence)
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
