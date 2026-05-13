package executor

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/scarletmu/openwhisker/internal/model"
)

func TestHeadlessSyncClientUsesAllowedStatusCommand(t *testing.T) {
	runner := &fakeRunner{output: "up to date\n"}
	client := HeadlessSyncClient{OBBin: "/bin/ob", Runner: runner}

	result, err := client.Status(context.Background(), "/vault/root")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Phase != model.SyncPhaseStatus || result.Backend != model.SyncBackendHeadless {
		t.Fatalf("result = %+v, want ok headless status", result)
	}
	if runner.name != "/bin/ob" {
		t.Fatalf("command name = %q, want /bin/ob", runner.name)
	}
	wantArgs := []string{"sync-status", "--path", "/vault/root"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestHeadlessSyncClientUsesAllowedOneShotSyncCommand(t *testing.T) {
	runner := &fakeRunner{output: "synced\n"}
	client := HeadlessSyncClient{Runner: runner}

	result, err := client.Sync(context.Background(), "/vault/root", model.SyncPhaseBefore)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Phase != model.SyncPhaseBefore {
		t.Fatalf("result = %+v, want ok before_apply", result)
	}
	if runner.name != "ob" {
		t.Fatalf("command name = %q, want ob", runner.name)
	}
	wantArgs := []string{"sync", "--path", "/vault/root"}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", runner.args, wantArgs)
	}
}

func TestHeadlessSyncClientRejectsUnknownPhase(t *testing.T) {
	runner := &fakeRunner{}
	client := HeadlessSyncClient{Runner: runner}

	_, err := client.Sync(context.Background(), "/vault/root", "continuous")
	if err == nil || !strings.Contains(err.Error(), "unsupported sync phase") {
		t.Fatalf("Sync error = %v, want unsupported sync phase", err)
	}
	if runner.name != "" {
		t.Fatalf("runner was called with %q", runner.name)
	}
}

func TestHeadlessSyncClientReturnsFailedResult(t *testing.T) {
	runner := &fakeRunner{output: "offline", err: errors.New("exit status 1")}
	client := HeadlessSyncClient{Runner: runner}

	result, err := client.Sync(context.Background(), "/vault/root", model.SyncPhaseAfter)
	if err == nil {
		t.Fatal("Sync error = nil, want failure")
	}
	if result.OK || result.Error == "" || result.Output != "offline" {
		t.Fatalf("result = %+v, want failed result with output and error", result)
	}
}

type fakeRunner struct {
	name   string
	args   []string
	output string
	err    error
}

func (r *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	r.name = name
	r.args = append([]string{}, args...)
	return r.output, r.err
}
