package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/scarletmu/openwhisker/internal/model"
)

type SyncClient interface {
	Status(ctx context.Context, vaultRoot string) (model.SyncResult, error)
	Sync(ctx context.Context, vaultRoot string, phase string) (model.SyncResult, error)
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

type NoopSyncClient struct{}

func (NoopSyncClient) Status(_ context.Context, _ string) (model.SyncResult, error) {
	return model.SyncResult{
		Mode:    model.SyncModeOff,
		Phase:   model.SyncPhaseStatus,
		OK:      true,
		Warning: "sync disabled",
	}, nil
}

func (NoopSyncClient) Sync(_ context.Context, _ string, phase string) (model.SyncResult, error) {
	return model.SyncResult{
		Mode:    model.SyncModeOff,
		Phase:   phase,
		OK:      true,
		Warning: "sync disabled",
	}, nil
}

type HeadlessSyncClient struct {
	OBBin  string
	Runner CommandRunner
}

func (c HeadlessSyncClient) Status(ctx context.Context, vaultRoot string) (model.SyncResult, error) {
	return c.run(ctx, vaultRoot, model.SyncPhaseStatus, "sync-status")
}

func (c HeadlessSyncClient) Sync(ctx context.Context, vaultRoot string, phase string) (model.SyncResult, error) {
	switch phase {
	case model.SyncPhaseManual, model.SyncPhaseBefore, model.SyncPhaseAfter:
	default:
		return model.SyncResult{}, fmt.Errorf("unsupported sync phase %q", phase)
	}
	return c.run(ctx, vaultRoot, phase, "sync")
}

func (c HeadlessSyncClient) run(ctx context.Context, vaultRoot, phase, subcommand string) (model.SyncResult, error) {
	if strings.TrimSpace(vaultRoot) == "" {
		return model.SyncResult{}, fmt.Errorf("vault root is required")
	}
	bin := c.OBBin
	if bin == "" {
		bin = "ob"
	}
	runner := c.Runner
	if runner == nil {
		runner = ExecCommandRunner{}
	}
	args := []string{subcommand, "--path", vaultRoot}
	result := model.SyncResult{
		Mode:    model.SyncModeOn,
		Backend: model.SyncBackendHeadless,
		Phase:   phase,
		Command: displayCommand(bin, args),
	}
	output, err := runner.Run(ctx, bin, args...)
	result.Output = strings.TrimSpace(output)
	if err != nil {
		result.OK = false
		result.Error = err.Error()
		return result, fmt.Errorf("headless sync %s failed: %w", phase, err)
	}
	result.OK = true
	return result, nil
}

func displayCommand(name string, args []string) string {
	parts := append([]string{name}, args...)
	return strings.Join(parts, " ")
}
