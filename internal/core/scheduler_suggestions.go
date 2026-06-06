package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

const maxSuggestedRawCaptureTextRunes = 20 * 1024

type SchedulerSuggestionService struct {
	store       *storage.Store
	vaultRoot   string
	conventions PlanServiceOptions
}

type AcceptSchedulerSuggestedRawCaptureRequest struct {
	RunID          string `json:"run_id"`
	Item           int    `json:"item"`
	Source         string `json:"source"`
	SourceKey      string `json:"source_key,omitempty"`
	SuppressOutbox bool   `json:"suppress_outbox,omitempty"`
}

type AcceptSchedulerSuggestedRawCaptureResult struct {
	RunID      string `json:"run_id"`
	ScheduleID string `json:"schedule_id"`
	Item       int    `json:"item"`
	JobID      string `json:"job_id"`
	PlanID     string `json:"plan_id"`
	TargetPath string `json:"target_path"`
	AfterHash  string `json:"after_hash"`
}

type suggestedRawCaptureItem struct {
	Text  string
	Title string
	Raw   json.RawMessage
}

func NewSchedulerSuggestionService(store *storage.Store, vaultRoot string, opts PlanServiceOptions) SchedulerSuggestionService {
	return SchedulerSuggestionService{store: store, vaultRoot: vaultRoot, conventions: opts}
}

func (s SchedulerSuggestionService) AcceptRawCapture(ctx context.Context, req AcceptSchedulerSuggestedRawCaptureRequest) (AcceptSchedulerSuggestedRawCaptureResult, error) {
	req.RunID = strings.TrimSpace(req.RunID)
	if req.RunID == "" {
		return AcceptSchedulerSuggestedRawCaptureResult{}, errors.New("scheduler run id is required")
	}
	if req.Item <= 0 {
		req.Item = 1
	}
	run, err := s.store.GetSchedulerRun(req.RunID)
	if err != nil {
		return AcceptSchedulerSuggestedRawCaptureResult{}, err
	}
	if run.Status != model.SchedulerRunStatusDone {
		return AcceptSchedulerSuggestedRawCaptureResult{}, fmt.Errorf("scheduler run %s is %s, not done", run.ID, run.Status)
	}
	item, err := suggestedRawCaptureFromRun(run, req.Item)
	if err != nil {
		return AcceptSchedulerSuggestedRawCaptureResult{}, err
	}
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "scheduler"
	}
	sourceKey := strings.TrimSpace(req.SourceKey)
	if sourceKey == "" {
		sourceKey = "scheduler:" + run.ID
	}
	rawText := renderAcceptedSchedulerRawCapture(run, req.Item, item)
	result, err := NewIngestServiceWithConventions(s.store, s.vaultRoot, s.conventions.Conventions).IngestRaw(ctx, IngestRawRequest{
		Text:           rawText,
		Source:         source,
		SourceKey:      sourceKey,
		SuppressOutbox: req.SuppressOutbox,
	})
	if err != nil {
		return AcceptSchedulerSuggestedRawCaptureResult{}, err
	}
	return AcceptSchedulerSuggestedRawCaptureResult{
		RunID:      run.ID,
		ScheduleID: run.ScheduleID,
		Item:       req.Item,
		JobID:      result.JobID,
		PlanID:     result.PlanID,
		TargetPath: result.TargetPath,
		AfterHash:  result.AfterHash,
	}, nil
}

func suggestedRawCaptureFromRun(run model.SchedulerRun, itemNumber int) (suggestedRawCaptureItem, error) {
	var result scheduler.SkillRunResult
	if err := json.Unmarshal([]byte(run.ResultJSON), &result); err != nil {
		return suggestedRawCaptureItem{}, fmt.Errorf("decode scheduler run result: %w", err)
	}
	if len(result.Payload) == 0 {
		return suggestedRawCaptureItem{}, errors.New("scheduler run payload is empty")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(result.Payload, &payload); err != nil {
		return suggestedRawCaptureItem{}, fmt.Errorf("decode scheduler run payload: %w", err)
	}
	rawItems := payload["suggested_raw_captures"]
	if len(rawItems) == 0 {
		return suggestedRawCaptureItem{}, errors.New("scheduler run has no suggested_raw_captures")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(rawItems, &items); err != nil {
		return suggestedRawCaptureItem{}, errors.New("suggested_raw_captures must be an array")
	}
	index := itemNumber - 1
	if index < 0 || index >= len(items) {
		return suggestedRawCaptureItem{}, fmt.Errorf("suggested_raw_captures item %d is out of range", itemNumber)
	}
	return parseSuggestedRawCaptureItem(items[index])
}

func parseSuggestedRawCaptureItem(raw json.RawMessage) (suggestedRawCaptureItem, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return suggestedRawCaptureItem{}, errors.New("suggested raw capture text is empty")
		}
		return suggestedRawCaptureItem{Text: truncateCaptureRunes(text, maxSuggestedRawCaptureTextRunes), Raw: raw}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return suggestedRawCaptureItem{}, errors.New("suggested raw capture item must be a string or object")
	}
	title := firstStringField(obj, "title", "name")
	text = firstStringField(obj, "text", "raw_text", "content", "body", "summary")
	if strings.TrimSpace(text) == "" {
		return suggestedRawCaptureItem{}, errors.New("suggested raw capture item has no text/content/raw_text/body/summary field")
	}
	return suggestedRawCaptureItem{
		Text:  truncateCaptureRunes(strings.TrimSpace(text), maxSuggestedRawCaptureTextRunes),
		Title: strings.TrimSpace(title),
		Raw:   raw,
	}, nil
}

func firstStringField(obj map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw, ok := obj[key]; ok && json.Unmarshal(raw, &value) == nil {
			if strings.TrimSpace(value) != "" {
				return value
			}
		}
	}
	return ""
}

func renderAcceptedSchedulerRawCapture(run model.SchedulerRun, itemNumber int, item suggestedRawCaptureItem) string {
	var b strings.Builder
	if item.Title != "" {
		fmt.Fprintf(&b, "# %s\n\n", item.Title)
	}
	b.WriteString(item.Text)
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "OpenWhisker scheduler suggested raw capture\n\n")
	fmt.Fprintf(&b, "- Scheduler run: %s\n", run.ID)
	fmt.Fprintf(&b, "- Schedule: %s\n", run.ScheduleID)
	fmt.Fprintf(&b, "- Skill: %s\n", run.SkillPath)
	fmt.Fprintf(&b, "- Suggested item: %d\n", itemNumber)
	return b.String()
}
