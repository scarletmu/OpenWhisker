package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// runAgentRuns dispatches `openwhisker agent runs <subcommand>`. The Phase 5
// `openwhisker scheduler runs` command is preserved as a compatibility alias
// that pre-fills --trigger-kind=scheduler.
func runAgentRuns(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: openwhisker agent runs <list|<run_id>> [flags]")
	}
	switch args[0] {
	case "list":
		return runAgentRunsList(args[1:], stdout, stderr)
	default:
		return runAgentRunsShow(args, stdout, stderr)
	}
}

func runAgentRunsList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent runs list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	limit := fs.Int("limit", 20, "maximum runs to return")
	triggerKind := fs.String("trigger-kind", "", "filter by trigger kind (scheduler, adhoc_matrix, adhoc_cli); empty = all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	runs, err := store.ListRecentAgentRuns(*triggerKind, *limit)
	if err != nil {
		return err
	}
	rendered := make([]map[string]any, 0, len(runs))
	for _, r := range runs {
		row := map[string]any{
			"run_id":       r.ID,
			"schedule_id":  r.ScheduleID,
			"status":       renderAgentRunStatus(r.Status),
			"trigger_kind": r.TriggerKind,
			"started_at":   r.StartedAt,
			"skill_dir":    r.SkillDir,
		}
		if r.FinishedAt != nil {
			row["finished_at"] = *r.FinishedAt
		}
		if r.Error != "" {
			row["error"] = r.Error
		}
		rendered = append(rendered, row)
	}
	return printJSON(stdout, rendered)
}

func runAgentRunsShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("agent runs <id>", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	withTrace := fs.Bool("trace", false, "also print the tool_trace_json column")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker agent runs <run_id> [--trace]")
	}
	runID := fs.Arg(0)
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	run, err := store.GetSchedulerRun(runID)
	if err != nil {
		return err
	}
	row := map[string]any{
		"run_id":       run.ID,
		"schedule_id":  run.ScheduleID,
		"status":       renderAgentRunStatus(run.Status),
		"trigger_kind": run.TriggerKind,
		"started_at":   run.StartedAt,
		"skill_dir":    run.SkillDir,
		"skill_path":   run.SkillPath,
	}
	if run.FinishedAt != nil {
		row["finished_at"] = *run.FinishedAt
	}
	if run.Error != "" {
		row["error"] = run.Error
	}
	if run.OutboxMessageID != "" {
		row["outbox_message_id"] = run.OutboxMessageID
	}
	if run.ResultJSON != "" {
		var resultObj any
		if err := json.Unmarshal([]byte(run.ResultJSON), &resultObj); err == nil {
			row["result"] = resultObj
		}
	}
	if *withTrace {
		if run.ToolTraceJSON == "" {
			row["trace"] = "(no trace; legacy or non-tool-calling run)"
		} else {
			var traceObj any
			if err := json.Unmarshal([]byte(run.ToolTraceJSON), &traceObj); err == nil {
				row["trace"] = traceObj
			} else {
				row["trace_raw"] = run.ToolTraceJSON
			}
		}
	}
	return printJSON(stdout, row)
}

// renderAgentRunStatus maps the SQLite-level status to its user-facing form
// (Phase 6: `done` displays as `ok`; other values pass through).
func renderAgentRunStatus(status string) string {
	if status == model.SchedulerRunStatusDone {
		return "ok"
	}
	return status
}
