package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/scarletmu/openwhisker/internal/adapters/matrix"
	"github.com/scarletmu/openwhisker/internal/agent"
	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
	"github.com/scarletmu/openwhisker/internal/profile"
	schedulerpkg "github.com/scarletmu/openwhisker/internal/scheduler"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func main() {
	if err := loadLocalEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return flag.ErrHelp
	}
	if args[0] == "daemon" {
		if len(args) >= 2 && args[1] == "status" {
			return runDaemonStatus(args[2:], stdout, stderr)
		}
		return runDaemon(args[1:], stdout, stderr)
	}
	if len(args) < 2 {
		printUsage(stderr)
		return flag.ErrHelp
	}
	switch {
	case args[0] == "ingest" && args[1] == "raw":
		return runIngestRaw(args[2:], stdin, stdout, stderr)
	case args[0] == "organize" && args[1] == "last":
		return runOrganizeLast(args[2:], stdout, stderr)
	case args[0] == "organize" && args[1] == "preview-context":
		return runOrganizePreviewContext(args[2:], stdout, stderr)
	case args[0] == "expand":
		return runExpandKnowledge(args[1:], stdout, stderr)
	case args[0] == "plan" && args[1] == "diff":
		return runPlanDiff(args[2:], stdout, stderr)
	case args[0] == "plan" && args[1] == "approve":
		return runPlanApprove(args[2:], stdout, stderr)
	case args[0] == "plan" && args[1] == "reject":
		return runPlanReject(args[2:], stdout, stderr)
	case args[0] == "vault" && args[1] == "sync-status":
		return runVaultSyncStatus(args[2:], stdout, stderr)
	case args[0] == "vault" && args[1] == "sync":
		return runVaultSync(args[2:], stdout, stderr)
	case len(args) >= 3 && args[0] == "vault" && args[1] == "profile" && args[2] == "preview":
		return runVaultProfilePreview(args[3:], stdout, stderr)
	case args[0] == "jobs" && args[1] == "show":
		return runJobShow(args[2:], stdout, stderr)
	case args[0] == "scheduler" && args[1] == "tick":
		return runSchedulerTick(args[2:], stdout, stderr)
	case args[0] == "scheduler" && args[1] == "status":
		return runSchedulerStatus(args[2:], stdout, stderr)
	case args[0] == "scheduler" && args[1] == "runs":
		return runSchedulerRuns(args[2:], stdout, stderr)
	case len(args) >= 3 && args[0] == "scheduler" && args[1] == "schedules" && args[2] == "list":
		return runSchedulerSchedulesList(args[3:], stdout, stderr)
	case len(args) >= 3 && args[0] == "scheduler" && args[1] == "schedules" && args[2] == "enable":
		return runSchedulerSchedulesSetEnabled(args[3:], stdout, stderr, true)
	case len(args) >= 3 && args[0] == "scheduler" && args[1] == "schedules" && args[2] == "disable":
		return runSchedulerSchedulesSetEnabled(args[3:], stdout, stderr, false)
	case args[0] == "scheduler" && args[1] == "accept":
		return runSchedulerAccept(args[2:], stdout, stderr)
	case args[0] == "matrix" && args[1] == "poll-once":
		return runMatrixPollOnce(args[2:], stdout, stderr)
	case args[0] == "matrix" && args[1] == "daemon":
		return runMatrixDaemon(args[2:], stdout, stderr)
	default:
		printUsage(stderr)
		return flag.ErrHelp
	}
}

func runIngestRaw(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ingest raw", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	text := fs.String("text", "", "raw text to ingest; stdin is used when empty")
	source := fs.String("source", "cli", "input source label")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rawText := *text
	if strings.TrimSpace(rawText) == "" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		rawText = string(data)
	}
	if err := os.MkdirAll(*vaultRoot, 0o755); err != nil {
		return err
	}

	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	service := core.NewIngestServiceWithConventions(store, *vaultRoot, conventions)
	result, err := service.IngestRaw(context.Background(), core.IngestRawRequest{
		Text:   rawText,
		Source: *source,
	})
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}

func runOrganizeLast(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("organize last", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	contextMode := fs.String("context-mode", contextModeDefault(), "raw organizer context mode: minimal or vault-rules")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	openAIModel := fs.String("openai-model", "", "deprecated alias for --llm-model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	organizer, err := rawOrganizerForName(*organizerName, coalesce(*openAIModel, *llmModel))
	if err != nil {
		return err
	}
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	result, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		Organizer:   organizer,
		ContextMode: *contextMode,
		Conventions: conventions,
	}).OrganizeLast(context.Background())
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runOrganizePreviewContext(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("organize preview-context", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	contextMode := fs.String("context-mode", contextModeDefault(), "raw organizer context mode: minimal or vault-rules")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker organize preview-context [--db data/openwhisker.db] [--vault testdata/vault]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	preview, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		ContextMode: *contextMode,
		Conventions: conventions,
	}).PreviewLastRawContext(context.Background())
	if err != nil {
		return err
	}
	return printJSON(stdout, preview)
}

func runExpandKnowledge(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("expand", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	organizerName := fs.String("organizer", organizerDefault(), "knowledge expander: deterministic or openai-compatible")
	contextMode := fs.String("context-mode", contextModeDefault(), "knowledge expander context mode: minimal or vault-rules")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker expand [--db data/openwhisker.db] [--vault testdata/vault] [--organizer deterministic|openai-compatible] <knowledge_path>")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	expander, err := knowledgeExpanderForName(*organizerName, *llmModel)
	if err != nil {
		return err
	}
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	result, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		KnowledgeExpander: expander,
		ContextMode:       *contextMode,
		Conventions:       conventions,
	}).ExpandKnowledge(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runVaultProfilePreview(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vault profile preview", flag.ContinueOnError)
	fs.SetOutput(stderr)
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker vault profile preview [--vault-profile generic|knowledge-vault]")
	}
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	return printJSON(stdout, profile.BuildBundle(conventions))
}

func runPlanDiff(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plan diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker plan diff [--db data/openwhisker.db] [--vault testdata/vault] <plan_id|job_id>")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	diff, err := core.NewPlanService(store, *vaultRoot).Diff(fs.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(stdout, diff)
}

func runPlanApprove(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("plan approve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	syncMode := fs.String("sync", model.SyncModeAuto, "sync mode: auto, off, or on")
	obBin := fs.String("ob-bin", defaultOBBin(), "Headless ob binary")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker plan approve [--sync=auto|off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault] <plan_id|job_id>")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	effectiveSyncMode, err := effectivePlanSyncMode(*syncMode, *vaultRoot)
	if err != nil {
		return err
	}
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	result, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		SyncMode:    effectiveSyncMode,
		SyncClient:  syncClientForMode(effectiveSyncMode, *obBin),
		Conventions: conventions,
	}).Approve(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runVaultSyncStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vault sync-status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_ = fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	syncMode := fs.String("sync", model.SyncModeOn, "sync mode: off or on")
	obBin := fs.String("ob-bin", defaultOBBin(), "Headless ob binary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker vault sync-status [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]")
	}
	mode, err := parseManualSyncMode(*syncMode)
	if err != nil {
		return err
	}
	result, err := syncClientForMode(mode, *obBin).Status(context.Background(), *vaultRoot)
	if err != nil {
		_ = printJSON(stdout, result)
		return err
	}
	return printJSON(stdout, result)
}

func runVaultSync(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("vault sync", flag.ContinueOnError)
	fs.SetOutput(stderr)
	_ = fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	syncMode := fs.String("sync", model.SyncModeOn, "sync mode: off or on")
	obBin := fs.String("ob-bin", defaultOBBin(), "Headless ob binary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker vault sync [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]")
	}
	mode, err := parseManualSyncMode(*syncMode)
	if err != nil {
		return err
	}
	result, err := syncClientForMode(mode, *obBin).Sync(context.Background(), *vaultRoot, model.SyncPhaseManual)
	if err != nil {
		_ = printJSON(stdout, result)
		return err
	}
	return printJSON(stdout, result)
}

func runPlanReject(args []string, stdout, stderr io.Writer) error {
	dbPath, identifier, reason, err := parsePlanRejectArgs(args)
	if err != nil {
		return err
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	result, err := core.NewPlanService(store, "").Reject(identifier, reason)
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func parsePlanRejectArgs(args []string) (dbPath, identifier, reason string, err error) {
	dbPath = "data/openwhisker.db"
	reason = "rejected by user"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest := args[i+1:]
			if len(rest) != 1 || identifier != "" {
				err = planRejectUsageError()
				return
			}
			identifier = rest[0]
			return
		case arg == "--db" || arg == "-db":
			i++
			if i >= len(args) {
				err = fmt.Errorf("flag needs an argument: %s", arg)
				return
			}
			dbPath = args[i]
		case strings.HasPrefix(arg, "--db="):
			dbPath = strings.TrimPrefix(arg, "--db=")
		case strings.HasPrefix(arg, "-db="):
			dbPath = strings.TrimPrefix(arg, "-db=")
		case arg == "--reason" || arg == "-reason":
			i++
			if i >= len(args) {
				err = fmt.Errorf("flag needs an argument: %s", arg)
				return
			}
			reason = args[i]
		case strings.HasPrefix(arg, "--reason="):
			reason = strings.TrimPrefix(arg, "--reason=")
		case strings.HasPrefix(arg, "-reason="):
			reason = strings.TrimPrefix(arg, "-reason=")
		case strings.HasPrefix(arg, "-"):
			err = fmt.Errorf("flag provided but not defined: %s", arg)
			return
		default:
			if identifier != "" {
				err = planRejectUsageError()
				return
			}
			identifier = arg
		}
	}
	if identifier == "" {
		err = planRejectUsageError()
	}
	return
}

func planRejectUsageError() error {
	return fmt.Errorf("usage: openwhisker plan reject [--db data/openwhisker.db] [--reason TEXT] <plan_id|job_id>")
}

func runJobShow(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("jobs show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker jobs show [--db data/openwhisker.db] <job_id>")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetJob(fs.Arg(0))
	if err != nil {
		return err
	}
	return printJSON(stdout, job)
}

func runSchedulerTick(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("scheduler tick", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	engineName := fs.String("engine", schedulerEngineDefault(), "scheduler engine: static or openai-compatible")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --engine=openai-compatible")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker scheduler tick [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--engine static|openai-compatible]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	vaultProfile := profile.NewConfiguredVaultProfile(conventions)
	engine, err := schedulerEngineForName(*engineName, *llmModel)
	if err != nil {
		return err
	}
	result, err := core.NewSchedulerServiceWithOptions(store, *vaultRoot, core.SchedulerServiceOptions{
		VaultProfile: vaultProfile,
		Runner: schedulerpkg.StaticSkillRunner{
			VaultRoot:            *vaultRoot,
			ExternalInfoAdapters: schedulerExternalInfoAdapters(),
			Engine:               engine,
		},
	}).Tick(context.Background())
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runSchedulerStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("scheduler status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	limit := fs.Int("limit", 10, "maximum runtimes and recent runs to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker scheduler status [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--limit 10]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	vaultProfile := profile.NewConfiguredVaultProfile(conventions)
	status, err := core.NewSchedulerStatusService(store, *vaultRoot, vaultProfile).Status(*limit)
	if err != nil {
		return err
	}
	return printJSON(stdout, status)
}

func runSchedulerRuns(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("scheduler runs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	limit := fs.Int("limit", 20, "maximum recent runs to show")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker scheduler runs [--db data/openwhisker.db] [--limit 20]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	runs, err := core.NewSchedulerStatusService(store, "", profile.VaultProfile{}).Runs(*limit)
	if err != nil {
		return err
	}
	return printJSON(stdout, runs)
}

func runSchedulerSchedulesList(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("scheduler schedules list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker scheduler schedules list [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	vaultProfile := profile.NewConfiguredVaultProfile(conventions)
	schedules, err := core.NewSchedulerStatusService(store, *vaultRoot, vaultProfile).Schedules()
	if err != nil {
		return err
	}
	return printJSON(stdout, schedules)
}

func runSchedulerSchedulesSetEnabled(args []string, stdout, stderr io.Writer, enabled bool) error {
	name := "scheduler schedules disable"
	if enabled {
		name = "scheduler schedules enable"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker scheduler schedules enable|disable [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] <schedule_id>")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	vaultProfile := profile.NewConfiguredVaultProfile(conventions)
	result, err := core.NewSchedulerStatusService(store, *vaultRoot, vaultProfile).SetScheduleEnabled(fs.Arg(0), enabled)
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runSchedulerAccept(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("scheduler accept", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	item := fs.Int("item", 1, "1-based suggested_raw_captures item number")
	source := fs.String("source", "scheduler", "input source label for accepted raw capture")
	sourceKey := fs.String("source-key", "", "optional source key for accepted raw capture")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: openwhisker scheduler accept [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--item 1] <scheduler_run_id>")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	result, err := core.NewSchedulerSuggestionService(store, *vaultRoot, core.PlanServiceOptions{
		Conventions: conventions,
	}).AcceptRawCapture(context.Background(), core.AcceptSchedulerSuggestedRawCaptureRequest{
		RunID:     fs.Arg(0),
		Item:      *item,
		Source:    *source,
		SourceKey: *sourceKey,
	})
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runMatrixPollOnce(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("matrix poll-once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	homeserver := fs.String("homeserver", os.Getenv("OPENWHISKER_MATRIX_HOMESERVER"), "Matrix homeserver URL")
	accessToken := fs.String("access-token", os.Getenv("OPENWHISKER_MATRIX_ACCESS_TOKEN"), "Matrix access token")
	password := fs.String("password", os.Getenv("OPENWHISKER_MATRIX_PASSWORD"), "Matrix bot password used to login when no access token is configured")
	userID := fs.String("user-id", os.Getenv("OPENWHISKER_MATRIX_USER_ID"), "Matrix bot user id")
	roomID := fs.String("room-id", os.Getenv("OPENWHISKER_MATRIX_ROOM_ID"), "Matrix room id")
	sessionFile := fs.String("session-file", matrixSessionFileDefault(), "Matrix login session cache file")
	schedulerAccessToken := fs.String("scheduler-access-token", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_ACCESS_TOKEN"), "Matrix Scheduler Bot access token")
	schedulerPassword := fs.String("scheduler-password", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_PASSWORD"), "Matrix Scheduler Bot password used to login when no scheduler access token is configured")
	schedulerUserID := fs.String("scheduler-user-id", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_USER_ID"), "Matrix Scheduler Bot user id")
	schedulerSessionFile := fs.String("scheduler-session-file", matrixSchedulerSessionFileDefault(), "Matrix Scheduler Bot login session cache file")
	since := fs.String("since", "", "Matrix sync token")
	timeout := fs.Duration("timeout", 5*time.Second, "Matrix sync timeout")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	contextMode := fs.String("context-mode", contextModeDefault(), "raw organizer context mode: minimal or vault-rules")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	openAIModel := fs.String("openai-model", "", "deprecated alias for --llm-model")
	intentRouter := fs.String("intent-router", intentRouterDefault(), "intent router mode: hybrid, rules, or off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker matrix poll-once [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--password PASSWORD] [--user-id USER] [--room-id ROOM] [--session-file data/matrix-session.json] [--scheduler-access-token TOKEN] [--scheduler-password PASSWORD] [--scheduler-user-id USER] [--scheduler-session-file data/matrix-scheduler-session.json] [--since TOKEN]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	organizer, err := rawOrganizerForName(*organizerName, coalesce(*openAIModel, *llmModel))
	if err != nil {
		return err
	}
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	intentClassifier, err := intentClassifierForMode(*intentRouter)
	if err != nil {
		return err
	}
	service := core.NewAdapterServiceWithOptions(store, *vaultRoot, core.AdapterServiceOptions{
		IntentRouterMode: *intentRouter,
		IntentClassifier: intentClassifier,
		PlanOptions: core.PlanServiceOptions{
			Organizer:   organizer,
			ContextMode: *contextMode,
			Conventions: conventions,
		},
	})
	matrixClient, resolvedUserID, err := matrixClientForAuth(context.Background(), matrixAuthOptions{
		Homeserver:  *homeserver,
		AccessToken: *accessToken,
		UserID:      *userID,
		Password:    *password,
		SessionFile: *sessionFile,
	})
	if err != nil {
		return err
	}
	deliveryClients, ignoredUserIDs, err := matrixDeliveryConfig(context.Background(), matrixClient, resolvedUserID, matrixAuthOptions{
		Homeserver:  *homeserver,
		AccessToken: *schedulerAccessToken,
		UserID:      *schedulerUserID,
		Password:    *schedulerPassword,
		SessionFile: *schedulerSessionFile,
	})
	if err != nil {
		return err
	}
	adapter := matrix.Adapter{
		Core:            service,
		Client:          matrixClient,
		UserID:          resolvedUserID,
		RoomID:          *roomID,
		DeliveryClients: deliveryClients,
		IgnoredUserIDs:  ignoredUserIDs,
	}
	nextBatch, err := adapter.PollOnce(context.Background(), *since, *timeout)
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]string{"next_batch": nextBatch})
}

func runMatrixDaemon(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("matrix daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	homeserver := fs.String("homeserver", os.Getenv("OPENWHISKER_MATRIX_HOMESERVER"), "Matrix homeserver URL")
	accessToken := fs.String("access-token", os.Getenv("OPENWHISKER_MATRIX_ACCESS_TOKEN"), "Matrix access token")
	password := fs.String("password", os.Getenv("OPENWHISKER_MATRIX_PASSWORD"), "Matrix bot password used to login when no access token is configured")
	userID := fs.String("user-id", os.Getenv("OPENWHISKER_MATRIX_USER_ID"), "Matrix bot user id")
	roomID := fs.String("room-id", os.Getenv("OPENWHISKER_MATRIX_ROOM_ID"), "Matrix room id")
	sinceFile := fs.String("since-file", "data/matrix-since.token", "Matrix sync token state file")
	sessionFile := fs.String("session-file", matrixSessionFileDefault(), "Matrix login session cache file")
	schedulerAccessToken := fs.String("scheduler-access-token", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_ACCESS_TOKEN"), "Matrix Scheduler Bot access token")
	schedulerPassword := fs.String("scheduler-password", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_PASSWORD"), "Matrix Scheduler Bot password used to login when no scheduler access token is configured")
	schedulerUserID := fs.String("scheduler-user-id", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_USER_ID"), "Matrix Scheduler Bot user id")
	schedulerSessionFile := fs.String("scheduler-session-file", matrixSchedulerSessionFileDefault(), "Matrix Scheduler Bot login session cache file")
	timeout := fs.Duration("timeout", 30*time.Second, "Matrix sync timeout")
	idleDelay := fs.Duration("idle-delay", time.Second, "delay between successful sync loops")
	errorDelay := fs.Duration("error-delay", 5*time.Second, "delay after Matrix sync errors")
	maxPolls := fs.Int("max-polls", 0, "maximum poll attempts before exiting; 0 means run until interrupted")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	contextMode := fs.String("context-mode", contextModeDefault(), "raw organizer context mode: minimal or vault-rules")
	vaultProfile := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	openAIModel := fs.String("openai-model", "", "deprecated alias for --llm-model")
	intentRouter := fs.String("intent-router", intentRouterDefault(), "intent router mode: hybrid, rules, or off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker matrix daemon [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--password PASSWORD] [--user-id USER] [--room-id ROOM] [--since-file data/matrix-since.token] [--session-file data/matrix-session.json] [--scheduler-access-token TOKEN] [--scheduler-password PASSWORD] [--scheduler-user-id USER] [--scheduler-session-file data/matrix-scheduler-session.json]")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	organizer, err := rawOrganizerForName(*organizerName, coalesce(*openAIModel, *llmModel))
	if err != nil {
		return err
	}
	conventions, err := vaultConventionsForProfile(*vaultProfile)
	if err != nil {
		return err
	}
	intentClassifier, err := intentClassifierForMode(*intentRouter)
	if err != nil {
		return err
	}
	service := core.NewAdapterServiceWithOptions(store, *vaultRoot, core.AdapterServiceOptions{
		IntentRouterMode: *intentRouter,
		IntentClassifier: intentClassifier,
		PlanOptions: core.PlanServiceOptions{
			Organizer:   organizer,
			ContextMode: *contextMode,
			Conventions: conventions,
		},
	})
	matrixClient, resolvedUserID, err := matrixClientForAuth(context.Background(), matrixAuthOptions{
		Homeserver:  *homeserver,
		AccessToken: *accessToken,
		UserID:      *userID,
		Password:    *password,
		SessionFile: *sessionFile,
	})
	if err != nil {
		return err
	}
	deliveryClients, ignoredUserIDs, err := matrixDeliveryConfig(context.Background(), matrixClient, resolvedUserID, matrixAuthOptions{
		Homeserver:  *homeserver,
		AccessToken: *schedulerAccessToken,
		UserID:      *schedulerUserID,
		Password:    *schedulerPassword,
		SessionFile: *schedulerSessionFile,
	})
	if err != nil {
		return err
	}
	adapter := matrix.Adapter{
		Core:            service,
		Client:          matrixClient,
		UserID:          resolvedUserID,
		RoomID:          *roomID,
		DeliveryClients: deliveryClients,
		IgnoredUserIDs:  ignoredUserIDs,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	finalSince, err := runMatrixDaemonLoop(ctx, adapter, matrixDaemonOptions{
		SinceFile:  *sinceFile,
		Timeout:    *timeout,
		IdleDelay:  *idleDelay,
		ErrorDelay: *errorDelay,
		MaxPolls:   *maxPolls,
		Logger:     stderr,
	})
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]string{"next_batch": finalSince})
}

func runDaemon(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	schedulerTickInterval := fs.Duration("scheduler-tick-interval", time.Minute, "scheduler tick interval; must be positive")
	statusFile := fs.String("status-file", "data/daemon-status.json", "daemon health status JSON file")
	schedulerEngineName := fs.String("scheduler-engine", schedulerEngineDefault(), "scheduler engine: static or openai-compatible")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for daemon LLM-backed components")
	matrixMode := fs.String("matrix", "auto", "Matrix adapter mode: auto, on, or off")
	homeserver := fs.String("homeserver", os.Getenv("OPENWHISKER_MATRIX_HOMESERVER"), "Matrix homeserver URL")
	accessToken := fs.String("access-token", os.Getenv("OPENWHISKER_MATRIX_ACCESS_TOKEN"), "Matrix access token")
	password := fs.String("password", os.Getenv("OPENWHISKER_MATRIX_PASSWORD"), "Matrix bot password used to login when no access token is configured")
	userID := fs.String("user-id", os.Getenv("OPENWHISKER_MATRIX_USER_ID"), "Matrix bot user id")
	roomID := fs.String("room-id", os.Getenv("OPENWHISKER_MATRIX_ROOM_ID"), "Matrix room id")
	sinceFile := fs.String("since-file", "data/matrix-since.token", "Matrix sync token state file")
	sessionFile := fs.String("session-file", matrixSessionFileDefault(), "Matrix login session cache file")
	schedulerAccessToken := fs.String("scheduler-access-token", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_ACCESS_TOKEN"), "Matrix Scheduler Bot access token")
	schedulerPassword := fs.String("scheduler-password", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_PASSWORD"), "Matrix Scheduler Bot password used to login when no scheduler access token is configured")
	schedulerUserID := fs.String("scheduler-user-id", os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_USER_ID"), "Matrix Scheduler Bot user id")
	schedulerSessionFile := fs.String("scheduler-session-file", matrixSchedulerSessionFileDefault(), "Matrix Scheduler Bot login session cache file")
	timeout := fs.Duration("timeout", 30*time.Second, "Matrix sync timeout")
	idleDelay := fs.Duration("idle-delay", time.Second, "delay between successful Matrix sync loops")
	errorDelay := fs.Duration("error-delay", 5*time.Second, "delay after Matrix sync errors")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	contextMode := fs.String("context-mode", contextModeDefault(), "raw organizer context mode: minimal or vault-rules")
	intentRouter := fs.String("intent-router", intentRouterDefault(), "intent router mode: hybrid, rules, or off")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker daemon [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--scheduler-tick-interval 1m] [--scheduler-engine static|openai-compatible] [--matrix auto|on|off]")
	}
	if *schedulerTickInterval <= 0 {
		return fmt.Errorf("--scheduler-tick-interval must be positive")
	}
	mode := strings.ToLower(strings.TrimSpace(*matrixMode))
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "on" && mode != "off" {
		return fmt.Errorf("--matrix must be auto, on, or off")
	}
	store, err := storage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()
	conventions, err := vaultConventionsForProfile(*vaultProfileName)
	if err != nil {
		return err
	}
	vaultProfile := profile.NewConfiguredVaultProfile(conventions)
	schedulerEngine, err := schedulerEngineForName(*schedulerEngineName, *llmModel)
	if err != nil {
		return err
	}
	schedulerService := core.NewSchedulerServiceWithOptions(store, *vaultRoot, core.SchedulerServiceOptions{
		VaultProfile: vaultProfile,
		Runner: schedulerpkg.StaticSkillRunner{
			VaultRoot:            *vaultRoot,
			ExternalInfoAdapters: schedulerExternalInfoAdapters(),
			Engine:               schedulerEngine,
		},
	})
	matrixEnabled := daemonMatrixEnabled(mode, *homeserver, *roomID, *accessToken, *password, *userID)
	if mode == "on" && !matrixEnabled {
		return fmt.Errorf("Matrix configuration is incomplete")
	}
	var matrixAdapter *matrix.Adapter
	if matrixEnabled {
		organizer, err := rawOrganizerForName(*organizerName, *llmModel)
		if err != nil {
			return err
		}
		intentClassifier, err := intentClassifierForMode(*intentRouter)
		if err != nil {
			return err
		}
		adapterService := core.NewAdapterServiceWithOptions(store, *vaultRoot, core.AdapterServiceOptions{
			IntentRouterMode: *intentRouter,
			IntentClassifier: intentClassifier,
			PlanOptions: core.PlanServiceOptions{
				Organizer:   organizer,
				ContextMode: *contextMode,
				Conventions: conventions,
			},
		})
		matrixClient, resolvedUserID, err := matrixClientForAuth(context.Background(), matrixAuthOptions{
			Homeserver:  *homeserver,
			AccessToken: *accessToken,
			UserID:      *userID,
			Password:    *password,
			SessionFile: *sessionFile,
		})
		if err != nil {
			return err
		}
		deliveryClients, ignoredUserIDs, err := matrixDeliveryConfig(context.Background(), matrixClient, resolvedUserID, matrixAuthOptions{
			Homeserver:  *homeserver,
			AccessToken: *schedulerAccessToken,
			UserID:      *schedulerUserID,
			Password:    *schedulerPassword,
			SessionFile: *schedulerSessionFile,
		})
		if err != nil {
			return err
		}
		matrixAdapter = &matrix.Adapter{
			Core:            adapterService,
			Client:          matrixClient,
			UserID:          resolvedUserID,
			RoomID:          *roomID,
			DeliveryClients: deliveryClients,
			IgnoredUserIDs:  ignoredUserIDs,
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startedAt := time.Now().UTC()
	writeDaemonStatus(*statusFile, daemonStatus{
		PID:                   os.Getpid(),
		StartedAt:             startedAt,
		UpdatedAt:             startedAt,
		SchedulerTickInterval: schedulerTickInterval.String(),
		MatrixEnabled:         matrixAdapter != nil,
		State:                 "starting",
	})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runSchedulerDaemonLoop(ctx, schedulerService, matrixAdapter, *roomID, *schedulerTickInterval, *statusFile, startedAt, stderr)
	}()
	if matrixAdapter != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := runMatrixDaemonLoop(ctx, matrixAdapter, matrixDaemonOptions{
				SinceFile:  *sinceFile,
				Timeout:    *timeout,
				IdleDelay:  *idleDelay,
				ErrorDelay: *errorDelay,
				Logger:     stderr,
			}); err != nil {
				errCh <- err
			}
		}()
	}
	logMatrixDaemon(stderr, "openwhisker daemon started scheduler_interval=%s matrix=%t", schedulerTickInterval.String(), matrixAdapter != nil)
	select {
	case err := <-errCh:
		stop()
		wg.Wait()
		return err
	case <-ctx.Done():
		wg.Wait()
		return nil
	}
}

type daemonStatus struct {
	PID                   int       `json:"pid"`
	StartedAt             time.Time `json:"started_at"`
	UpdatedAt             time.Time `json:"updated_at"`
	SchedulerTickInterval string    `json:"scheduler_tick_interval"`
	MatrixEnabled         bool      `json:"matrix_enabled"`
	State                 string    `json:"state"`
	LastTickAt            time.Time `json:"last_tick_at,omitempty"`
	LastTickError         string    `json:"last_tick_error,omitempty"`
	LastTickCompleted     int       `json:"last_tick_completed,omitempty"`
	LastTickFailed        int       `json:"last_tick_failed,omitempty"`
	LastTickSkipped       int       `json:"last_tick_skipped,omitempty"`
}

func runDaemonStatus(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	statusFile := fs.String("status-file", "data/daemon-status.json", "daemon health status JSON file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker daemon status [--status-file data/daemon-status.json]")
	}
	data, err := os.ReadFile(*statusFile)
	if err != nil {
		return err
	}
	var status daemonStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return err
	}
	type daemonStatusResult struct {
		daemonStatus
		PIDRunning bool `json:"pid_running"`
	}
	return printJSON(stdout, daemonStatusResult{
		daemonStatus: status,
		PIDRunning:   daemonPIDRunning(status.PID),
	})
}

func daemonPIDRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func writeDaemonStatus(path string, status daemonStatus) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, append(data, '\n'), 0o644)
}

func daemonMatrixEnabled(mode, homeserver, roomID, accessToken, password, userID string) bool {
	if mode == "off" {
		return false
	}
	if strings.TrimSpace(homeserver) == "" || strings.TrimSpace(roomID) == "" {
		return false
	}
	if strings.TrimSpace(accessToken) != "" {
		return true
	}
	return strings.TrimSpace(password) != "" && strings.TrimSpace(userID) != ""
}

func runSchedulerDaemonLoop(ctx context.Context, service core.SchedulerService, adapter *matrix.Adapter, roomID string, interval time.Duration, statusFile string, startedAt time.Time, logger io.Writer) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		result, err := runSchedulerDaemonTick(ctx, service, adapter, roomID, logger)
		status := daemonStatus{
			PID:                   os.Getpid(),
			StartedAt:             startedAt,
			UpdatedAt:             time.Now().UTC(),
			SchedulerTickInterval: interval.String(),
			MatrixEnabled:         adapter != nil,
			State:                 "running",
			LastTickAt:            time.Now().UTC(),
			LastTickCompleted:     result.Completed,
			LastTickFailed:        result.Failed,
			LastTickSkipped:       result.Skipped,
		}
		if err != nil {
			status.LastTickError = err.Error()
		}
		writeDaemonStatus(statusFile, status)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runSchedulerDaemonTick(ctx context.Context, service core.SchedulerService, adapter *matrix.Adapter, roomID string, logger io.Writer) (core.SchedulerTickResult, error) {
	result, err := service.Tick(ctx)
	if err != nil {
		logMatrixDaemon(logger, "scheduler tick error: %v", err)
		return result, err
	}
	logMatrixDaemon(logger, "scheduler tick completed enabled=%t discovered=%d due=%d completed=%d failed=%d skipped=%d",
		result.Enabled, result.Discovered, result.Due, result.Completed, result.Failed, result.Skipped)
	if adapter != nil {
		if err := adapter.DeliverOutbox(ctx, roomID); err != nil {
			logMatrixDaemon(logger, "scheduler outbox delivery error: %v", err)
			return result, err
		}
	}
	return result, nil
}

type matrixPoller interface {
	PollOnce(context.Context, string, time.Duration) (string, error)
}

type matrixDaemonOptions struct {
	SinceFile  string
	Timeout    time.Duration
	IdleDelay  time.Duration
	ErrorDelay time.Duration
	MaxPolls   int
	Logger     io.Writer
}

type matrixAuthOptions struct {
	Homeserver  string
	AccessToken string
	UserID      string
	Password    string
	SessionFile string
	HTTPClient  *http.Client
}

type matrixSession struct {
	Homeserver  string `json:"homeserver"`
	UserID      string `json:"user_id"`
	AccessToken string `json:"access_token"`
	DeviceID    string `json:"device_id,omitempty"`
	UpdatedAt   string `json:"updated_at"`
}

func runMatrixDaemonLoop(ctx context.Context, poller matrixPoller, opts matrixDaemonOptions) (string, error) {
	if poller == nil {
		return "", fmt.Errorf("matrix daemon poller is required")
	}
	since, err := readMatrixSince(opts.SinceFile)
	if err != nil {
		return "", err
	}
	polls := 0
	for {
		if err := ctx.Err(); err != nil {
			return since, nil
		}
		nextBatch, err := poller.PollOnce(ctx, since, opts.Timeout)
		polls++
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return since, nil
			}
			logMatrixDaemon(opts.Logger, "matrix daemon poll error: %v", err)
			if opts.MaxPolls > 0 && polls >= opts.MaxPolls {
				return since, err
			}
			if !sleepContext(ctx, durationOrDefault(opts.ErrorDelay, 5*time.Second)) {
				return since, nil
			}
			continue
		}
		if strings.TrimSpace(nextBatch) != "" && nextBatch != since {
			since = nextBatch
			if err := writeMatrixSince(opts.SinceFile, since); err != nil {
				return since, err
			}
		}
		logMatrixDaemon(opts.Logger, "matrix daemon poll completed next_batch=%s", since)
		if opts.MaxPolls > 0 && polls >= opts.MaxPolls {
			return since, nil
		}
		if !sleepContext(ctx, durationOrDefault(opts.IdleDelay, time.Second)) {
			return since, nil
		}
	}
}

func matrixClientForAuth(ctx context.Context, opts matrixAuthOptions) (matrix.Client, string, error) {
	homeserver := strings.TrimSpace(opts.Homeserver)
	userID := strings.TrimSpace(opts.UserID)
	client := matrix.Client{
		Homeserver: homeserver,
		HTTPClient: opts.HTTPClient,
	}
	if token := strings.TrimSpace(opts.AccessToken); token != "" {
		client.AccessToken = token
		return client, userID, nil
	}

	session, err := readMatrixSession(opts.SessionFile)
	if err != nil {
		return matrix.Client{}, "", err
	}
	if matrixSessionMatches(session, homeserver, userID) {
		client.AccessToken = session.AccessToken
		return client, coalesce(userID, session.UserID), nil
	}

	if opts.Password == "" {
		return matrix.Client{}, "", fmt.Errorf("OPENWHISKER_MATRIX_ACCESS_TOKEN or OPENWHISKER_MATRIX_PASSWORD is required")
	}
	if userID == "" {
		return matrix.Client{}, "", fmt.Errorf("OPENWHISKER_MATRIX_USER_ID is required when logging in with OPENWHISKER_MATRIX_PASSWORD")
	}
	login, err := client.LoginPassword(ctx, matrix.LoginRequest{
		UserID:                   userID,
		Password:                 opts.Password,
		DeviceID:                 reusableMatrixDeviceID(session, homeserver, userID),
		InitialDeviceDisplayName: "OpenWhisker Matrix Adapter",
	})
	if err != nil {
		return matrix.Client{}, "", err
	}
	resolvedUserID := coalesce(strings.TrimSpace(login.UserID), userID)
	client.AccessToken = login.AccessToken
	if err := writeMatrixSession(opts.SessionFile, matrixSession{
		Homeserver:  homeserver,
		UserID:      resolvedUserID,
		AccessToken: login.AccessToken,
		DeviceID:    strings.TrimSpace(login.DeviceID),
		UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		return matrix.Client{}, "", err
	}
	return client, resolvedUserID, nil
}

func matrixDeliveryConfig(ctx context.Context, knowledgeClient matrix.Client, knowledgeUserID string, schedulerAuth matrixAuthOptions) (map[string]matrix.Client, []string, error) {
	deliveryClients := map[string]matrix.Client{
		model.OutboxActorKnowledge: knowledgeClient,
	}
	ignoredUserIDs := compactStrings(knowledgeUserID)
	if !matrixAuthConfigured(schedulerAuth) {
		return deliveryClients, ignoredUserIDs, nil
	}
	if strings.TrimSpace(schedulerAuth.UserID) == "" {
		return nil, nil, fmt.Errorf("OPENWHISKER_MATRIX_SCHEDULER_USER_ID is required when scheduler Matrix bot credentials are configured")
	}
	schedulerClient, schedulerUserID, err := matrixClientForAuth(ctx, schedulerAuth)
	if err != nil {
		return nil, nil, fmt.Errorf("scheduler matrix auth: %w", err)
	}
	deliveryClients[model.OutboxActorScheduler] = schedulerClient
	ignoredUserIDs = append(ignoredUserIDs, compactStrings(schedulerUserID)...)
	return deliveryClients, ignoredUserIDs, nil
}

func matrixAuthConfigured(opts matrixAuthOptions) bool {
	return strings.TrimSpace(opts.AccessToken) != "" ||
		strings.TrimSpace(opts.Password) != "" ||
		strings.TrimSpace(opts.UserID) != ""
}

func compactStrings(values ...string) []string {
	var compacted []string
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		compacted = append(compacted, value)
	}
	return compacted
}

func matrixSessionMatches(session matrixSession, homeserver, userID string) bool {
	if strings.TrimSpace(session.AccessToken) == "" {
		return false
	}
	if homeserver != "" && strings.TrimSpace(session.Homeserver) != homeserver {
		return false
	}
	if userID != "" && strings.TrimSpace(session.UserID) != userID {
		return false
	}
	return true
}

func reusableMatrixDeviceID(session matrixSession, homeserver, userID string) string {
	if strings.TrimSpace(session.DeviceID) == "" {
		return ""
	}
	if homeserver != "" && strings.TrimSpace(session.Homeserver) != homeserver {
		return ""
	}
	if userID != "" && strings.TrimSpace(session.UserID) != userID {
		return ""
	}
	return strings.TrimSpace(session.DeviceID)
}

func readMatrixSession(path string) (matrixSession, error) {
	if strings.TrimSpace(path) == "" {
		return matrixSession{}, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return matrixSession{}, nil
	}
	if err != nil {
		return matrixSession{}, err
	}
	var session matrixSession
	if err := json.Unmarshal(data, &session); err != nil {
		return matrixSession{}, err
	}
	return session, nil
}

func writeMatrixSession(path string, session matrixSession) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func matrixSessionFileDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_MATRIX_SESSION_FILE")); value != "" {
		return value
	}
	return "data/matrix-session.json"
}

func matrixSchedulerSessionFileDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_MATRIX_SCHEDULER_SESSION_FILE")); value != "" {
		return value
	}
	return "data/matrix-scheduler-session.json"
}

func readMatrixSince(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeMatrixSince(path, since string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(strings.TrimSpace(since)+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func durationOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func logMatrixDaemon(logger io.Writer, format string, args ...any) {
	if logger == nil {
		return
	}
	fmt.Fprintf(logger, format+"\n", args...)
}

func printJSON(stdout io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(encoded))
	return nil
}

func printUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, `usage:
  openwhisker daemon [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--scheduler-tick-interval 1m] [--scheduler-engine static|openai-compatible] [--matrix auto|on|off]
  openwhisker daemon status [--status-file data/daemon-status.json]
  openwhisker ingest raw [--text TEXT] [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault]
  openwhisker organize last [--db data/openwhisker.db] [--vault testdata/vault] [--organizer deterministic|openai-compatible] [--context-mode minimal|vault-rules] [--vault-profile generic|knowledge-vault] [--llm-model MODEL]
  openwhisker organize preview-context [--db data/openwhisker.db] [--vault testdata/vault] [--context-mode minimal|vault-rules] [--vault-profile generic|knowledge-vault]
  openwhisker expand [--db data/openwhisker.db] [--vault testdata/vault] [--organizer deterministic|openai-compatible] [--context-mode minimal|vault-rules] [--vault-profile generic|knowledge-vault] [--llm-model MODEL] <knowledge_path>
  openwhisker plan diff [--db data/openwhisker.db] [--vault testdata/vault] <plan_id|job_id>
  openwhisker plan approve [--sync=auto|off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] <plan_id|job_id>
  openwhisker plan reject [--db data/openwhisker.db] [--reason TEXT] <plan_id|job_id>
  openwhisker vault profile preview [--vault-profile generic|knowledge-vault]
  openwhisker vault sync-status [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]
  openwhisker vault sync [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]
  openwhisker jobs show [--db data/openwhisker.db] <job_id>
  openwhisker scheduler tick [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--engine static|openai-compatible] [--llm-model MODEL]
  openwhisker scheduler status [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--limit 10]
  openwhisker scheduler runs [--db data/openwhisker.db] [--limit 20]
  openwhisker scheduler schedules list [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault]
  openwhisker scheduler schedules enable|disable [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] <schedule_id>
  openwhisker scheduler accept [--db data/openwhisker.db] [--vault testdata/vault] [--vault-profile generic|knowledge-vault] [--item 1] <scheduler_run_id>
  openwhisker matrix poll-once [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--password PASSWORD] [--user-id USER] [--room-id ROOM] [--session-file data/matrix-session.json] [--scheduler-access-token TOKEN] [--scheduler-password PASSWORD] [--scheduler-user-id USER] [--scheduler-session-file data/matrix-scheduler-session.json] [--since TOKEN] [--intent-router hybrid|rules|off] [--organizer deterministic|openai-compatible] [--context-mode minimal|vault-rules] [--vault-profile generic|knowledge-vault] [--llm-model MODEL]
  openwhisker matrix daemon [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--password PASSWORD] [--user-id USER] [--room-id ROOM] [--since-file data/matrix-since.token] [--session-file data/matrix-session.json] [--scheduler-access-token TOKEN] [--scheduler-password PASSWORD] [--scheduler-user-id USER] [--scheduler-session-file data/matrix-scheduler-session.json] [--intent-router hybrid|rules|off] [--organizer deterministic|openai-compatible] [--context-mode minimal|vault-rules] [--vault-profile generic|knowledge-vault] [--llm-model MODEL]`)
}

func defaultOBBin() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_OB_BIN")); value != "" {
		return value
	}
	return "ob"
}

func intentRouterDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_INTENT_ROUTER")); value != "" {
		return value
	}
	return "hybrid"
}

func effectivePlanSyncMode(mode, vaultRoot string) (string, error) {
	switch mode {
	case "", model.SyncModeAuto:
		if isDefaultTestVault(vaultRoot) {
			return model.SyncModeOff, nil
		}
		return model.SyncModeOn, nil
	case model.SyncModeOff, model.SyncModeOn:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported sync mode %q; want auto, off, or on", mode)
	}
}

func parseManualSyncMode(mode string) (string, error) {
	switch mode {
	case "", model.SyncModeOn:
		return model.SyncModeOn, nil
	case model.SyncModeOff:
		return model.SyncModeOff, nil
	default:
		return "", fmt.Errorf("unsupported sync mode %q; want off or on", mode)
	}
}

func syncClientForMode(mode, obBin string) executor.SyncClient {
	if mode == model.SyncModeOn {
		return executor.HeadlessSyncClient{OBBin: obBin}
	}
	return executor.NoopSyncClient{}
}

func isDefaultTestVault(vaultRoot string) bool {
	return filepath.Clean(vaultRoot) == filepath.Clean("testdata/vault")
}

func organizerDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_RAW_ORGANIZER")); value != "" {
		return value
	}
	return "deterministic"
}

func schedulerEngineDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_SCHEDULER_ENGINE")); value != "" {
		return value
	}
	return "static"
}

func contextModeDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_CONTEXT_MODE")); value != "" {
		return value
	}
	return core.ContextModeMinimal
}

func vaultProfileDefault() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_VAULT_PROFILE")); value != "" {
		return value
	}
	return "generic"
}

func vaultConventionsForProfile(profile string) (policy.Conventions, error) {
	conventions, err := policy.ConventionsForProfile(profile)
	if err != nil {
		return policy.Conventions{}, err
	}
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_RAW_INBOX_DIR")); value != "" {
		conventions.RawInboxDir = value
	}
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_RAW_PROCESSED_DIR")); value != "" {
		conventions.RawProcessedDir = value
	}
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_KNOWLEDGE_DIR")); value != "" {
		conventions.KnowledgeDir = value
	}
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_KNOWLEDGE_DRAFT_DIR")); value != "" {
		conventions.KnowledgeDraftDir = value
	}
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_REQUIRED_DRAFT_TAGS")); value != "" {
		conventions.RequiredDraftTags = splitCommaList(value)
	}
	return conventions.Normalize(), nil
}

func splitCommaList(value string) []string {
	if strings.EqualFold(strings.TrimSpace(value), "none") {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func rawOrganizerForName(name, openAIModel string) (core.RawOrganizer, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "deterministic":
		return nil, nil
	case "openai-compatible", "compatible", "openai":
		apiKey := llmAPIKey()
		if apiKey == "" {
			return nil, fmt.Errorf("OPENWHISKER_LLM_API_KEY is required for --organizer=openai-compatible")
		}
		return agent.OpenAIRawOrganizer{
			Client: agent.OpenAIClient{
				APIKey:       apiKey,
				BaseURL:      llmBaseURL(),
				Model:        openAIModel,
				Organization: coalesce(os.Getenv("OPENWHISKER_LLM_ORG_ID"), os.Getenv("OPENAI_ORG_ID")),
				Project:      coalesce(os.Getenv("OPENWHISKER_LLM_PROJECT_ID"), os.Getenv("OPENAI_PROJECT_ID")),
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported organizer %q; want deterministic or openai-compatible", name)
	}
}

func knowledgeExpanderForName(name, llmModel string) (core.KnowledgeExpander, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "deterministic":
		return nil, nil
	case "openai-compatible", "compatible", "openai":
		apiKey := llmAPIKey()
		if apiKey == "" {
			return nil, fmt.Errorf("OPENWHISKER_LLM_API_KEY is required for --organizer=openai-compatible")
		}
		return agent.OpenAIKnowledgeExpander{
			Client: agent.OpenAIClient{
				APIKey:       apiKey,
				BaseURL:      llmBaseURL(),
				Model:        llmModel,
				Organization: coalesce(os.Getenv("OPENWHISKER_LLM_ORG_ID"), os.Getenv("OPENAI_ORG_ID")),
				Project:      coalesce(os.Getenv("OPENWHISKER_LLM_PROJECT_ID"), os.Getenv("OPENAI_PROJECT_ID")),
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported expander %q; want deterministic or openai-compatible", name)
	}
}

func schedulerEngineForName(name, llmModel string) (schedulerpkg.SkillEngine, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "static":
		return nil, nil
	case "openai-compatible", "compatible", "openai":
		apiKey := llmAPIKey()
		if apiKey == "" {
			return nil, fmt.Errorf("OPENWHISKER_LLM_API_KEY is required for --engine=openai-compatible")
		}
		return agent.OpenAISchedulerEngine{
			Client: agent.OpenAIClient{
				APIKey:       apiKey,
				BaseURL:      llmBaseURL(),
				Model:        llmModel,
				Organization: coalesce(os.Getenv("OPENWHISKER_LLM_ORG_ID"), os.Getenv("OPENAI_ORG_ID")),
				Project:      coalesce(os.Getenv("OPENWHISKER_LLM_PROJECT_ID"), os.Getenv("OPENAI_PROJECT_ID")),
			},
		}, nil
	default:
		return nil, fmt.Errorf("unsupported scheduler engine %q; want static or openai-compatible", name)
	}
}

func schedulerExternalInfoAdapters() map[string]schedulerpkg.ExternalInfoAdapter {
	return map[string]schedulerpkg.ExternalInfoAdapter{
		"rss": schedulerpkg.RSSExternalInfoAdapter{},
	}
}

func intentClassifierForMode(mode string) (core.IntentClassifier, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "hybrid":
		apiKey := intentAPIKey()
		if apiKey == "" {
			return nil, nil
		}
		return agent.OpenAIIntentClassifier{
			Client: agent.OpenAIClient{
				APIKey:       apiKey,
				BaseURL:      intentBaseURL(),
				Model:        intentModelDefault(),
				MaxOutput:    700,
				Organization: coalesce(os.Getenv("OPENWHISKER_INTENT_ORG_ID"), os.Getenv("OPENAI_ORG_ID")),
				Project:      coalesce(os.Getenv("OPENWHISKER_INTENT_PROJECT_ID"), os.Getenv("OPENAI_PROJECT_ID")),
			},
		}, nil
	case "rules", "off":
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported intent router mode %q; want hybrid, rules, or off", mode)
	}
}

func llmAPIKey() string {
	return coalesce(os.Getenv("OPENWHISKER_LLM_API_KEY"), os.Getenv("OPENWHISKER_OPENAI_API_KEY"), os.Getenv("OPENAI_API_KEY"))
}

func llmBaseURL() string {
	return coalesce(os.Getenv("OPENWHISKER_LLM_BASE_URL"), os.Getenv("OPENWHISKER_OPENAI_BASE_URL"))
}

func llmModelDefault() string {
	return coalesce(os.Getenv("OPENWHISKER_LLM_MODEL"), os.Getenv("OPENWHISKER_OPENAI_MODEL"))
}

func intentAPIKey() string {
	return strings.TrimSpace(os.Getenv("OPENWHISKER_INTENT_API_KEY"))
}

func intentBaseURL() string {
	return strings.TrimSpace(os.Getenv("OPENWHISKER_INTENT_BASE_URL"))
}

func intentModelDefault() string {
	return strings.TrimSpace(os.Getenv("OPENWHISKER_INTENT_MODEL"))
}

func coalesce(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func loadLocalEnv() error {
	path, err := findUp(".env.local")
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for lineNumber, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNumber+1)
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			return fmt.Errorf("%s:%d: invalid env key %q", path, lineNumber+1, key)
		}
		value = strings.TrimSpace(value)
		if unquoted, err := unquoteEnvValue(value); err != nil {
			return fmt.Errorf("%s:%d: %w", path, lineNumber+1, err)
		} else {
			value = unquoted
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return nil
}

func findUp(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func unquoteEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, `'`) {
		if len(value) < 2 || value[len(value)-1] != value[0] {
			return "", fmt.Errorf("unterminated quoted env value")
		}
		value = value[1 : len(value)-1]
	}
	return value, nil
}
