package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/agentdispatch"
	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/profile"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// runAsk implements `openwhisker ask --skill <id> [--json] [--debug] <query>`.
// Synchronous CLI invocation: builds an ad-hoc link index (with soft
// threshold protection), dispatches one agent run, writes the result.
func runAsk(args []string, _ io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("ask", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", "data/openwhisker.db", "SQLite database path")
	vaultRoot := fs.String("vault", "testdata/vault", "vault root")
	vaultProfileName := fs.String("vault-profile", vaultProfileDefault(), "vault profile: generic or knowledge-vault")
	llmModel := fs.String("llm-model", llmModelDefault(), "OpenAI-compatible model name for ToolCallingEngine")
	skillID := fs.String("skill", "", "Skill id to invoke (required)")
	asJSON := fs.Bool("json", false, "print the full AgentAdHocResult as JSON instead of just summary")
	debug := fs.Bool("debug", false, "also write data/agent-debug/<run_id>.ndjson with the full LLM message stream")
	forceBuild := fs.Bool("force-build-index", false, "skip the link-index soft threshold and build regardless of vault size")
	fileWarn := fs.Int("cli-index-file-warn", 5000, "refuse to build the link index when the vault has more .md files than this (override with --force-build-index)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*skillID) == "" {
		return fmt.Errorf("usage: openwhisker ask --skill <id> [--json] [--debug] [--force-build-index] \"<query>\"")
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		return fmt.Errorf("openwhisker ask requires a query as positional argument")
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

	runner := buildAgentRunner(*vaultRoot, *llmModel)
	if runner == nil {
		return fmt.Errorf("openwhisker ask requires OPENWHISKER_LLM_API_KEY (no LLM chat client could be constructed)")
	}

	// Build link index synchronously with soft threshold.
	idx, err := buildCLILinkIndex(context.Background(), *vaultRoot, vaultProfile.Scheduler.ReadOnlyVaultRoots, *fileWarn, *forceBuild)
	if err != nil {
		return err
	}

	dispatcher := agentdispatch.Dispatcher{Runner: runner, LinkIndex: idx, Store: store}
	lookup := newRegistrySkillLookup(*vaultRoot, vaultProfile)
	skill, err := lookup.Find(*skillID)
	if err != nil {
		return err
	}
	if !skill.HasToolCalling() {
		return fmt.Errorf("skill %q is not configured for tool-calling (engine = %q, vault_tools = %v)", skill.ID, skill.Engine, skill.VaultTools)
	}

	// Capture debug ndjson in-memory so we can name the file after the run id
	// the dispatcher mints.
	var debugBuf bytes.Buffer
	var debugWriter interface{ Write(p []byte) (int, error) }
	if *debug {
		debugWriter = &debugBuf
	}

	dispatched, err := dispatcher.DispatchAdHoc(context.Background(), core.AgentAdHocRequest{
		Skill:       skill,
		Query:       query,
		TriggerKind: model.AgentTriggerKindAdhocCLI,
		Now:         time.Now().UTC(),
		DebugWriter: debugWriter,
	})
	if err != nil {
		fmt.Fprintf(stderr, "agent run failed: %v\n", err)
		return err
	}

	if *debug && debugBuf.Len() > 0 {
		dir := "data/agent-debug"
		_ = os.MkdirAll(dir, 0o755)
		path := filepath.Join(dir, dispatched.RunID+".ndjson")
		if err := os.WriteFile(path, debugBuf.Bytes(), 0o644); err == nil {
			fmt.Fprintf(stderr, "wrote debug ndjson to %s\n", path)
		}
	}

	if *asJSON {
		return printJSON(stdout, dispatched)
	}
	summary := strings.TrimSpace(dispatched.Result.Summary)
	if dispatched.Result.Title != "" {
		fmt.Fprintln(stdout, strings.TrimSpace(dispatched.Result.Title))
		fmt.Fprintln(stdout)
	}
	fmt.Fprintln(stdout, summary)
	if dispatched.Status == model.SchedulerRunStatusPartial {
		fmt.Fprintln(stdout, "\n(status: partial — budget forced finalize)")
	}
	fmt.Fprintf(stdout, "\n[trace: %s]\n", dispatched.RunID)
	return nil
}

