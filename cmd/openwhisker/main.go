package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/scarletmu/openwhisker/internal/adapters/matrix"
	"github.com/scarletmu/openwhisker/internal/agent"
	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/executor"
	"github.com/scarletmu/openwhisker/internal/model"
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
	if len(args) < 2 {
		printUsage(stderr)
		return flag.ErrHelp
	}
	switch {
	case args[0] == "ingest" && args[1] == "raw":
		return runIngestRaw(args[2:], stdin, stdout, stderr)
	case args[0] == "organize" && args[1] == "last":
		return runOrganizeLast(args[2:], stdout, stderr)
	case args[0] == "organize" && args[1] == "today":
		return runOrganizeToday(args[2:], stdout, stderr)
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
	case args[0] == "jobs" && args[1] == "show":
		return runJobShow(args[2:], stdout, stderr)
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

	service := core.NewIngestService(store, *vaultRoot)
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
	result, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		Organizer: organizer,
	}).OrganizeLast(context.Background())
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
}

func runOrganizeToday(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("organize today", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target test vault root")
	dateValue := fs.String("date", "", "local date to organize in YYYY-MM-DD; default is today")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	openAIModel := fs.String("openai-model", "", "deprecated alias for --llm-model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker organize today [--db data/openwhisker.db] [--vault testdata/vault] [--date YYYY-MM-DD] [--organizer deterministic|openai-compatible]")
	}
	day, err := parseLocalDate(*dateValue)
	if err != nil {
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
	result, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		Organizer: organizer,
	}).OrganizeToday(context.Background(), day)
	if err != nil {
		return err
	}
	return printJSON(stdout, result)
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
	result, err := core.NewPlanServiceWithOptions(store, *vaultRoot, core.PlanServiceOptions{
		SyncMode:   effectiveSyncMode,
		SyncClient: syncClientForMode(effectiveSyncMode, *obBin),
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

func runMatrixPollOnce(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("matrix poll-once", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "target vault root")
	homeserver := fs.String("homeserver", os.Getenv("OPENWHISKER_MATRIX_HOMESERVER"), "Matrix homeserver URL")
	accessToken := fs.String("access-token", os.Getenv("OPENWHISKER_MATRIX_ACCESS_TOKEN"), "Matrix access token")
	userID := fs.String("user-id", os.Getenv("OPENWHISKER_MATRIX_USER_ID"), "Matrix bot user id")
	roomID := fs.String("room-id", os.Getenv("OPENWHISKER_MATRIX_ROOM_ID"), "Matrix room id")
	since := fs.String("since", "", "Matrix sync token")
	timeout := fs.Duration("timeout", 5*time.Second, "Matrix sync timeout")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	openAIModel := fs.String("openai-model", "", "deprecated alias for --llm-model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker matrix poll-once [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--user-id USER] [--room-id ROOM] [--since TOKEN]")
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
	service := core.NewAdapterServiceWithOptions(store, *vaultRoot, core.AdapterServiceOptions{
		PlanOptions: core.PlanServiceOptions{Organizer: organizer},
	})
	adapter := matrix.Adapter{
		Core:   service,
		Client: matrix.Client{Homeserver: *homeserver, AccessToken: *accessToken},
		UserID: *userID,
		RoomID: *roomID,
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
	userID := fs.String("user-id", os.Getenv("OPENWHISKER_MATRIX_USER_ID"), "Matrix bot user id")
	roomID := fs.String("room-id", os.Getenv("OPENWHISKER_MATRIX_ROOM_ID"), "Matrix room id")
	sinceFile := fs.String("since-file", "data/matrix-since.token", "Matrix sync token state file")
	timeout := fs.Duration("timeout", 30*time.Second, "Matrix sync timeout")
	idleDelay := fs.Duration("idle-delay", time.Second, "delay between successful sync loops")
	errorDelay := fs.Duration("error-delay", 5*time.Second, "delay after Matrix sync errors")
	maxPolls := fs.Int("max-polls", 0, "maximum poll attempts before exiting; 0 means run until interrupted")
	organizerName := fs.String("organizer", organizerDefault(), "raw organizer: deterministic or openai-compatible")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model for --organizer=openai-compatible")
	openAIModel := fs.String("openai-model", "", "deprecated alias for --llm-model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: openwhisker matrix daemon [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--user-id USER] [--room-id ROOM] [--since-file data/matrix-since.token]")
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
	service := core.NewAdapterServiceWithOptions(store, *vaultRoot, core.AdapterServiceOptions{
		PlanOptions: core.PlanServiceOptions{Organizer: organizer},
	})
	adapter := matrix.Adapter{
		Core:   service,
		Client: matrix.Client{Homeserver: *homeserver, AccessToken: *accessToken},
		UserID: *userID,
		RoomID: *roomID,
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
  openwhisker ingest raw [--text TEXT] [--db data/openwhisker.db] [--vault testdata/vault]
  openwhisker organize last [--db data/openwhisker.db] [--vault testdata/vault] [--organizer deterministic|openai-compatible] [--llm-model MODEL]
  openwhisker organize today [--db data/openwhisker.db] [--vault testdata/vault] [--date YYYY-MM-DD] [--organizer deterministic|openai-compatible] [--llm-model MODEL]
  openwhisker plan diff [--db data/openwhisker.db] [--vault testdata/vault] <plan_id|job_id>
  openwhisker plan approve [--sync=auto|off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault] <plan_id|job_id>
  openwhisker plan reject [--db data/openwhisker.db] [--reason TEXT] <plan_id|job_id>
  openwhisker vault sync-status [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]
  openwhisker vault sync [--sync=off|on] [--ob-bin ob] [--db data/openwhisker.db] [--vault testdata/vault]
  openwhisker jobs show [--db data/openwhisker.db] <job_id>
  openwhisker matrix poll-once [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--user-id USER] [--room-id ROOM] [--since TOKEN] [--organizer deterministic|openai-compatible] [--llm-model MODEL]
  openwhisker matrix daemon [--db data/openwhisker.db] [--vault testdata/vault] [--homeserver URL] [--access-token TOKEN] [--user-id USER] [--room-id ROOM] [--since-file data/matrix-since.token] [--organizer deterministic|openai-compatible] [--llm-model MODEL]`)
}

func defaultOBBin() string {
	if value := strings.TrimSpace(os.Getenv("OPENWHISKER_OB_BIN")); value != "" {
		return value
	}
	return "ob"
}

func parseLocalDate(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		now := time.Now()
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local), nil
	}
	day, err := time.ParseInLocation("2006-01-02", value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse --date: %w", err)
	}
	return day, nil
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

func llmAPIKey() string {
	return coalesce(os.Getenv("OPENWHISKER_LLM_API_KEY"), os.Getenv("OPENWHISKER_OPENAI_API_KEY"), os.Getenv("OPENAI_API_KEY"))
}

func llmBaseURL() string {
	return coalesce(os.Getenv("OPENWHISKER_LLM_BASE_URL"), os.Getenv("OPENWHISKER_OPENAI_BASE_URL"))
}

func llmModelDefault() string {
	return coalesce(os.Getenv("OPENWHISKER_LLM_MODEL"), os.Getenv("OPENWHISKER_OPENAI_MODEL"))
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
