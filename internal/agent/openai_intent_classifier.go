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
	request := openAIResponseRequest{
		Instructions:    intentClassifierInstructions(),
		Input:           string(input),
		Text:            openAITextSpec{Format: intentClassifierJSONObjectFormat()},
		MaxOutputTokens: 700,
		Store:           false,
	}
	outputText, err := c.Client.CreateResponse(ctx, request)
	if err != nil {
		return core.IntentClassifierResult{}, err
	}
	if strings.TrimSpace(outputText) == "" {
		outputText, err = c.Client.CreateResponse(ctx, request)
		if err != nil {
			return core.IntentClassifierResult{}, fmt.Errorf("intent classifier retry after empty content failed: %w", err)
		}
		if strings.TrimSpace(outputText) == "" {
			return core.IntentClassifierResult{}, errors.New("intent classifier returned empty content after one retry")
		}
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
	return strings.TrimSpace(`You classify one inbound message for OpenWhisker IM input.

Return ONLY a single JSON object. No prose, no markdown fences, no comments, no trailing text.

The JSON object MUST contain exactly these fields, with values restricted to the listed enums where applicable:

- intent: one of ["raw_capture", "organize_request", "diff_request", "approve_request", "reject_request", "unclear"]
- target: one of ["last", "today", "active", "active_bucket", "new_bucket", "none"]
- capture_action: one of ["create", "append", "close", "none"]
- bucket_relation: one of ["same_topic", "new_topic", "unrelated", "unclear"]
- payload_text: string
- additional_payload_text: string
- confidence_label: one of ["high", "medium", "low"]
- confidence: number between 0 and 1
- reason: string

Example JSON output:
{"intent":"raw_capture","target":"new_bucket","capture_action":"create","bucket_relation":"new_topic","payload_text":"用户想新建一段记录","additional_payload_text":"","confidence_label":"high","confidence":0.9,"reason":"明确的新主题记录请求"}

You only classify intent. You do not write notes, create plans, approve plans, or execute actions.
Use the provided minimal state only. Do not infer hidden vault content.

Choose high confidence only when the user intent is direct and actionable.
Use medium when a focused clarification is needed.
Use low/unclear when the message is ambiguous.`)
}

func intentClassifierJSONObjectFormat() openAITextFormat {
	return openAITextFormat{Type: "json_object"}
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
