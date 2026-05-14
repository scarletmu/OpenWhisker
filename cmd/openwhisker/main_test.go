package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestRunPlanRejectAcceptsTrailingReasonFlag(t *testing.T) {
	dbPath := t.TempDir() + "/openwhisker.db"
	jobID := "job_cli_reject"
	planID := "plan_cli_reject"
	now := time.Now().UTC()

	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJob(model.WikiJob{
		ID:        jobID,
		Type:      model.JobTypeOrganizeRaw,
		Status:    model.JobStatusAwaitingApproval,
		Source:    "test",
		InputJSON: "{}",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePlan(model.VaultPlan{
		ID:               planID,
		JobID:            jobID,
		Purpose:          "test reject",
		RiskLevel:        model.RiskMedium,
		RequiresApproval: true,
		Summary:          "test reject",
		SourceRefs:       []string{},
		TargetPaths:      []string{},
		Operations:       []model.VaultOperation{},
		Status:           model.PlanStatusAwaitingApproval,
		CreatedAt:        now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = run([]string{"plan", "reject", "--db", dbPath, planID, "--reason", "not ready"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, stderr = %s", err, stderr.String())
	}

	store, err = storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan, err := store.GetPlan(planID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusRejected {
		t.Fatalf("plan status = %q, want rejected", plan.Status)
	}
	if plan.RejectedReason != "not ready" {
		t.Fatalf("rejected reason = %q, want not ready", plan.RejectedReason)
	}
	if !strings.Contains(stdout.String(), `"status": "rejected"`) {
		t.Fatalf("stdout = %s, want rejected status", stdout.String())
	}
}

func TestRunVaultSyncStatusOffReturnsNoopResult(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"vault", "sync-status", "--sync=off", "--vault", t.TempDir()}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"mode": "off"`) || !strings.Contains(stdout.String(), `"phase": "status"`) {
		t.Fatalf("stdout = %s, want disabled status result", stdout.String())
	}
}

func TestRunVaultSyncOffReturnsNoopResult(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"vault", "sync", "--sync=off", "--vault", t.TempDir()}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("run() error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"mode": "off"`) || !strings.Contains(stdout.String(), `"phase": "manual"`) {
		t.Fatalf("stdout = %s, want disabled manual sync result", stdout.String())
	}
}

func TestRunPlanApproveSyncOffAppliesWithoutExternalSync(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")

	var stdout, stderr bytes.Buffer
	err := run([]string{"ingest", "raw", "--db", dbPath, "--vault", vaultRoot, "--text", "cli sync off"}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("ingest run() error = %v, stderr = %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err = run([]string{"organize", "last", "--db", dbPath, "--vault", vaultRoot}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("organize run() error = %v, stderr = %s", err, stderr.String())
	}
	var organized struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &organized); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	err = run([]string{"plan", "approve", "--sync=off", "--db", dbPath, "--vault", vaultRoot, organized.PlanID}, strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatalf("approve run() error = %v, stderr = %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"status": "applied"`) {
		t.Fatalf("stdout = %s, want applied status", stdout.String())
	}
}

func TestEffectivePlanSyncModeDefaultsOffForTestVault(t *testing.T) {
	mode, err := effectivePlanSyncMode(model.SyncModeAuto, "./testdata/vault")
	if err != nil {
		t.Fatal(err)
	}
	if mode != model.SyncModeOff {
		t.Fatalf("mode = %q, want off", mode)
	}

	mode, err = effectivePlanSyncMode(model.SyncModeAuto, "/Users/wang/Documents/KnowLedge")
	if err != nil {
		t.Fatal(err)
	}
	if mode != model.SyncModeOn {
		t.Fatalf("mode = %q, want on", mode)
	}
}

func TestRawOrganizerForNameRequiresOpenAIKey(t *testing.T) {
	t.Setenv("OPENWHISKER_LLM_API_KEY", "")
	t.Setenv("OPENWHISKER_OPENAI_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	_, err := rawOrganizerForName("openai-compatible", "")
	if err == nil || !strings.Contains(err.Error(), "OPENWHISKER_LLM_API_KEY") {
		t.Fatalf("rawOrganizerForName error = %v, want api key error", err)
	}
}

func TestRawOrganizerForNameBuildsOpenAIOrganizer(t *testing.T) {
	t.Setenv("OPENWHISKER_LLM_API_KEY", "test-key")
	t.Setenv("OPENWHISKER_LLM_BASE_URL", "https://compatible.example.test/v1")
	organizer, err := rawOrganizerForName("openai-compatible", "model-test")
	if err != nil {
		t.Fatal(err)
	}
	if organizer == nil {
		t.Fatal("organizer is nil")
	}
}

func TestRunOrganizePreviewContext(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	var ingestOut bytes.Buffer
	if err := run([]string{
		"ingest", "raw",
		"--db", dbPath,
		"--vault", vaultRoot,
		"--text", "preview context raw",
	}, strings.NewReader(""), &ingestOut, io.Discard); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{
		"organize", "preview-context",
		"--db", dbPath,
		"--vault", vaultRoot,
	}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{`"raw_job_id"`, `"raw_path"`, "preview context raw"} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview context output = %s, want %q", body, want)
		}
	}
}

func TestRunVaultProfilePreviewBuildsSkillBundle(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{
		"vault", "profile", "preview",
		"--vault-profile", "knowledge-vault",
	}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		`"id": "knowledge-vault"`,
		`"name": "vault-raw-organizer"`,
		"VaultRawOrganizerSkill.md",
		"status/needs-review",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("profile preview output = %s, want %q", body, want)
		}
	}
}

func TestRunMatrixDaemonLoopPersistsSinceToken(t *testing.T) {
	dir := t.TempDir()
	sinceFile := filepath.Join(dir, "matrix-since.token")
	if err := writeMatrixSince(sinceFile, "initial"); err != nil {
		t.Fatal(err)
	}
	poller := &fakeMatrixPoller{next: []string{"next-1", "next-2"}}
	var logs bytes.Buffer

	finalSince, err := runMatrixDaemonLoop(context.Background(), poller, matrixDaemonOptions{
		SinceFile: sinceFile,
		Timeout:   10 * time.Millisecond,
		IdleDelay: time.Nanosecond,
		MaxPolls:  2,
		Logger:    &logs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalSince != "next-2" {
		t.Fatalf("final since = %q, want next-2", finalSince)
	}
	if got := strings.Join(poller.since, ","); got != "initial,next-1" {
		t.Fatalf("poller since = %q, want initial,next-1", got)
	}
	persisted, err := readMatrixSince(sinceFile)
	if err != nil {
		t.Fatal(err)
	}
	if persisted != "next-2" {
		t.Fatalf("persisted since = %q, want next-2", persisted)
	}
	if !strings.Contains(logs.String(), "matrix daemon poll completed") {
		t.Fatalf("logs = %q, want completion log", logs.String())
	}
}

func TestMatrixClientForAuthLogsInAndCachesSession(t *testing.T) {
	dir := t.TempDir()
	sessionFile := filepath.Join(dir, "matrix-session.json")
	var loginCount int
	httpClient := &http.Client{Transport: mainRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		loginCount++
		if r.Method != http.MethodPost || r.URL.Path != "/_matrix/client/v3/login" {
			t.Fatalf("request = %s %s, want login", r.Method, r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["password"] != "secret" {
			t.Fatalf("password = %v, want secret", payload["password"])
		}
		return mainJSONResponse(`{"user_id":"@bot:example.test","access_token":"token-1","device_id":"DEVICE1"}`), nil
	})}

	client, userID, err := matrixClientForAuth(context.Background(), matrixAuthOptions{
		Homeserver:  "https://matrix.example.test",
		UserID:      "@bot:example.test",
		Password:    "secret",
		SessionFile: sessionFile,
		HTTPClient:  httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if userID != "@bot:example.test" || client.AccessToken != "token-1" {
		t.Fatalf("auth = user %q token %q, want login credentials", userID, client.AccessToken)
	}
	session, err := readMatrixSession(sessionFile)
	if err != nil {
		t.Fatal(err)
	}
	if session.AccessToken != "token-1" || session.DeviceID != "DEVICE1" {
		t.Fatalf("session = %+v, want cached login", session)
	}
	if loginCount != 1 {
		t.Fatalf("loginCount = %d, want 1", loginCount)
	}

	cachedClient, cachedUserID, err := matrixClientForAuth(context.Background(), matrixAuthOptions{
		Homeserver:  "https://matrix.example.test",
		UserID:      "@bot:example.test",
		Password:    "secret",
		SessionFile: sessionFile,
		HTTPClient:  httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cachedUserID != "@bot:example.test" || cachedClient.AccessToken != "token-1" {
		t.Fatalf("cached auth = user %q token %q, want cached credentials", cachedUserID, cachedClient.AccessToken)
	}
	if loginCount != 1 {
		t.Fatalf("loginCount = %d, want cached session without second login", loginCount)
	}
}

func TestMatrixClientForAuthRequiresCredential(t *testing.T) {
	_, _, err := matrixClientForAuth(context.Background(), matrixAuthOptions{
		Homeserver:  "https://matrix.example.test",
		UserID:      "@bot:example.test",
		SessionFile: filepath.Join(t.TempDir(), "missing.json"),
	})
	if err == nil || !strings.Contains(err.Error(), "OPENWHISKER_MATRIX_ACCESS_TOKEN or OPENWHISKER_MATRIX_PASSWORD") {
		t.Fatalf("error = %v, want credential error", err)
	}
}

func TestLoadLocalEnvFindsParentEnvAndDoesNotOverrideExisting(t *testing.T) {
	dir := t.TempDir()
	child := filepath.Join(dir, "child")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.local"), []byte(strings.Join([]string{
		"# local env",
		"OPENWHISKER_TEST_ENV_FILE_VALUE=from-file",
		"OPENWHISKER_TEST_ENV_FILE_EXISTING=from-file",
		"export OPENWHISKER_TEST_ENV_FILE_QUOTED=\"quoted value\"",
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(child)
	t.Setenv("OPENWHISKER_TEST_ENV_FILE_VALUE", "")
	t.Setenv("OPENWHISKER_TEST_ENV_FILE_EXISTING", "from-shell")
	t.Setenv("OPENWHISKER_TEST_ENV_FILE_QUOTED", "")

	if err := loadLocalEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPENWHISKER_TEST_ENV_FILE_VALUE"); got != "" {
		t.Fatalf("env value = %q, want existing blank value to win", got)
	}
	if got := os.Getenv("OPENWHISKER_TEST_ENV_FILE_EXISTING"); got != "from-shell" {
		t.Fatalf("existing env = %q, want from-shell", got)
	}
	if got := os.Getenv("OPENWHISKER_TEST_ENV_FILE_QUOTED"); got != "" {
		t.Fatalf("quoted env = %q, want existing blank value to win", got)
	}
	os.Unsetenv("OPENWHISKER_TEST_ENV_FILE_VALUE")
	os.Unsetenv("OPENWHISKER_TEST_ENV_FILE_QUOTED")
	if err := loadLocalEnv(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("OPENWHISKER_TEST_ENV_FILE_VALUE"); got != "from-file" {
		t.Fatalf("env value after unset = %q, want from-file", got)
	}
	if got := os.Getenv("OPENWHISKER_TEST_ENV_FILE_QUOTED"); got != "quoted value" {
		t.Fatalf("quoted env after unset = %q, want quoted value", got)
	}
}

type mainRoundTripFunc func(*http.Request) (*http.Response, error)

func (f mainRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func mainJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type fakeMatrixPoller struct {
	next  []string
	since []string
}

func (p *fakeMatrixPoller) PollOnce(_ context.Context, since string, _ time.Duration) (string, error) {
	p.since = append(p.since, since)
	if len(p.next) == 0 {
		return since, nil
	}
	next := p.next[0]
	p.next = p.next[1:]
	return next, nil
}
