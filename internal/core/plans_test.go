package core

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestOrganizeLastPreparesDiffAndApproveAppliesPlan(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ingest, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "phase 2 should review plans before writing knowledge",
		Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	planResult, err := NewPlanService(store, vaultRoot).OrganizeLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if planResult.Status != model.PlanStatusAwaitingApproval {
		t.Fatalf("OrganizeLast status = %q, want awaiting_approval", planResult.Status)
	}
	if planResult.Diff == nil || len(planResult.Diff.Entries) != 2 {
		t.Fatalf("OrganizeLast diff = %+v, want two entries", planResult.Diff)
	}
	knowledgePath := "Knowledge/Drafts/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md"
	processedPath := "Raw/Processed/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md"
	if exists(filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))) {
		t.Fatal("OrganizeLast wrote Knowledge note before approval")
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))) {
		t.Fatal("OrganizeLast moved raw note before approval")
	}

	approveResult, err := NewPlanService(store, vaultRoot).Approve(context.Background(), planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if approveResult.Status != model.PlanStatusApplied {
		t.Fatalf("Approve status = %q, want applied", approveResult.Status)
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))) {
		t.Fatalf("approved plan did not create %s", knowledgePath)
	}
	if exists(filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))) {
		t.Fatalf("approved plan did not move %s", ingest.TargetPath)
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(processedPath))) {
		t.Fatalf("approved plan did not create %s", processedPath)
	}
	processedContent, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(processedPath)))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{
		"## OpenWhisker Processing",
		"Raw path after approval: " + processedPath,
		"Knowledge/Drafts/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md",
	} {
		if !strings.Contains(string(processedContent), needle) {
			t.Fatalf("processed raw missing %q:\n%s", needle, string(processedContent))
		}
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertCount(t, db, "vault_operation_logs", 3)
}

func TestOrganizeLastUsesInjectedRawOrganizer(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "agent host contract",
		Source: "test",
	}); err != nil {
		t.Fatal(err)
	}

	service := NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		Organizer: fakeRawOrganizer{summary: "fake agent host plan"},
	})
	result, err := service.OrganizeLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Diff == nil || result.Diff.Summary != "fake agent host plan" {
		t.Fatalf("diff = %+v, want injected organizer summary", result.Diff)
	}
}

func TestOrganizeLastPassesMinimalRawOrganizerContextByDefault(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "AGENTS.md"), []byte("root vault rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "Meta", "Tagging.md"), []byte("tag rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "context should include this raw text",
		Source: "test",
	}); err != nil {
		t.Fatal(err)
	}

	service := NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		Organizer: checkingRawOrganizer{},
	})
	if _, err := service.OrganizeLast(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizeLastCanIncludeVaultRulesContext(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "AGENTS.md"), []byte("root vault rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vaultRoot, "Meta", "Tagging.md"), []byte("tag rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "context should include this raw text",
		Source: "test",
	}); err != nil {
		t.Fatal(err)
	}

	service := NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		Organizer:   checkingVaultRulesRawOrganizer{},
		ContextMode: ContextModeVaultRules,
	})
	if _, err := service.OrganizeLast(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOrganizeTodayPreparesGroupedPlanAndApproveAppliesAllRaw(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	raw1, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "first raw for today",
		Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "second raw for today",
		Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := NewPlanService(store, vaultRoot).OrganizeToday(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.PlanStatusAwaitingApproval {
		t.Fatalf("OrganizeToday status = %q, want awaiting_approval", result.Status)
	}
	if len(result.RawJobIDs) != 2 {
		t.Fatalf("raw job ids = %+v, want two", result.RawJobIDs)
	}
	if result.Diff == nil || len(result.Diff.Entries) != 3 {
		t.Fatalf("diff = %+v, want create plus two moves", result.Diff)
	}

	approve, err := NewPlanService(store, vaultRoot).Approve(context.Background(), result.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if approve.Status != model.PlanStatusApplied {
		t.Fatalf("approve status = %q, want applied", approve.Status)
	}
	for _, raw := range []IngestRawResult{raw1, raw2} {
		if exists(filepath.Join(vaultRoot, filepath.FromSlash(raw.TargetPath))) {
			t.Fatalf("raw path still exists after approve: %s", raw.TargetPath)
		}
		processedPath := "Raw/Processed/" + strings.TrimSuffix(filepath.Base(raw.TargetPath), ".md") + ".md"
		content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(processedPath)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), "## OpenWhisker Processing") || !strings.Contains(string(content), result.TargetPaths[0]) {
			t.Fatalf("processed raw %s missing grouped processing note:\n%s", processedPath, string(content))
		}
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(result.TargetPaths[0]))) {
		t.Fatalf("grouped knowledge draft not created: %s", result.TargetPaths[0])
	}
}

func TestApproveDetectsHashConflictBeforePartialWrite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ingest, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "original raw",
		Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := NewPlanService(store, vaultRoot).OrganizeLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rawFullPath := filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))
	if err := os.WriteFile(rawFullPath, []byte("changed after prepare"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = NewPlanService(store, vaultRoot).Approve(context.Background(), planResult.PlanID)
	if err == nil || !strings.Contains(err.Error(), "hash conflict") {
		t.Fatalf("Approve error = %v, want hash conflict", err)
	}
	plan, err := store.GetPlan(planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusConflict {
		t.Fatalf("plan status = %q, want conflict", plan.Status)
	}
	knowledgePath := "Knowledge/Drafts/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md"
	if exists(filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))) {
		t.Fatal("conflicted plan created Knowledge note before detecting raw hash conflict")
	}
}

func TestApproveTreatsPreparedCreateTargetAsConflict(t *testing.T) {
	dbPath, vaultRoot, store, ingest, planResult := prepareAwaitingOrganizePlan(t, "create target conflict")
	defer store.Close()

	knowledgePath := "Knowledge/Drafts/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md"
	knowledgeFullPath := filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))
	if err := os.MkdirAll(filepath.Dir(knowledgeFullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(knowledgeFullPath, []byte("created after prepare"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewPlanService(store, vaultRoot).Approve(context.Background(), planResult.PlanID)
	if err == nil || !strings.Contains(err.Error(), "target already exists") {
		t.Fatalf("Approve error = %v, want target already exists", err)
	}
	plan, err := store.GetPlan(planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusConflict {
		t.Fatalf("plan status = %q, want conflict", plan.Status)
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))) {
		t.Fatal("conflicted plan moved raw note")
	}
	assertOutboxKindCount(t, dbPath, model.OutboxKindConflict, 1)
}

func TestApproveTreatsPreparedMoveDestinationAsConflict(t *testing.T) {
	dbPath, vaultRoot, store, ingest, planResult := prepareAwaitingOrganizePlan(t, "move destination conflict")
	defer store.Close()

	base := strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md")
	knowledgePath := "Knowledge/Drafts/" + base + ".md"
	processedPath := "Raw/Processed/" + base + ".md"
	processedFullPath := filepath.Join(vaultRoot, filepath.FromSlash(processedPath))
	if err := os.MkdirAll(filepath.Dir(processedFullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(processedFullPath, []byte("processed after prepare"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewPlanService(store, vaultRoot).Approve(context.Background(), planResult.PlanID)
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("Approve error = %v, want destination already exists", err)
	}
	plan, err := store.GetPlan(planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusConflict {
		t.Fatalf("plan status = %q, want conflict", plan.Status)
	}
	if exists(filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))) {
		t.Fatal("conflicted plan created Knowledge note before detecting move destination conflict")
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))) {
		t.Fatal("conflicted plan moved raw note")
	}
	assertOutboxKindCount(t, dbPath, model.OutboxKindConflict, 1)
}

func TestApprovePreSyncFailureDoesNotWriteVault(t *testing.T) {
	dbPath, vaultRoot, store, ingest, planResult := prepareAwaitingOrganizePlan(t, "pre sync failure")
	defer store.Close()

	syncClient := &fakeSyncClient{syncErr: errors.New("remote unavailable")}
	_, err := NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		SyncMode:   model.SyncModeOn,
		SyncClient: syncClient,
	}).Approve(context.Background(), planResult.PlanID)
	if err == nil || !strings.Contains(err.Error(), "remote unavailable") {
		t.Fatalf("Approve error = %v, want pre-sync failure", err)
	}
	knowledgePath := "Knowledge/Drafts/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md"
	if exists(filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))) {
		t.Fatal("pre-sync failure created Knowledge note")
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))) {
		t.Fatal("pre-sync failure moved raw note")
	}
	plan, err := store.GetPlan(planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusFailed {
		t.Fatalf("plan status = %q, want failed", plan.Status)
	}
	job, err := store.GetJob(plan.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(job.ResultJSON, `"sync_before"`) {
		t.Fatalf("job result json = %s, want sync_before", job.ResultJSON)
	}
	assertOutboxKindCount(t, dbPath, model.OutboxKindError, 1)
}

func TestApproveDetectsHashConflictAfterPreSync(t *testing.T) {
	_, vaultRoot, store, ingest, planResult := prepareAwaitingOrganizePlan(t, "pre sync conflict")
	defer store.Close()

	rawFullPath := filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))
	syncClient := &fakeSyncClient{
		onSync: func(phase string) {
			if phase == model.SyncPhaseBefore {
				if err := os.WriteFile(rawFullPath, []byte("changed during pre-sync"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		},
	}
	_, err := NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		SyncMode:   model.SyncModeOn,
		SyncClient: syncClient,
	}).Approve(context.Background(), planResult.PlanID)
	if err == nil || !strings.Contains(err.Error(), "hash conflict") {
		t.Fatalf("Approve error = %v, want hash conflict", err)
	}
	plan, err := store.GetPlan(planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusConflict {
		t.Fatalf("plan status = %q, want conflict", plan.Status)
	}
}

func TestApprovePostSyncFailureKeepsAppliedWithWarning(t *testing.T) {
	dbPath, vaultRoot, store, _, planResult := prepareAwaitingOrganizePlan(t, "post sync warning")
	defer store.Close()

	syncClient := &fakeSyncClient{failPhase: model.SyncPhaseAfter, syncErr: errors.New("push failed")}
	result, err := NewPlanServiceWithOptions(store, vaultRoot, PlanServiceOptions{
		SyncMode:   model.SyncModeOn,
		SyncClient: syncClient,
	}).Approve(context.Background(), planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != model.PlanStatusApplied {
		t.Fatalf("Approve status = %q, want applied", result.Status)
	}
	if result.SyncAfter == nil || !strings.Contains(result.SyncAfter.Warning, "push failed") {
		t.Fatalf("SyncAfter = %+v, want warning", result.SyncAfter)
	}
	plan, err := store.GetPlan(planResult.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != model.PlanStatusApplied {
		t.Fatalf("plan status = %q, want applied", plan.Status)
	}
	job, err := store.GetJob(plan.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != model.JobStatusDone {
		t.Fatalf("job status = %q, want done", job.Status)
	}
	if !strings.Contains(job.ResultJSON, `"sync_after"`) || !strings.Contains(job.ResultJSON, `"warning"`) {
		t.Fatalf("job result json = %s, want sync_after warning", job.ResultJSON)
	}
	assertOutboxBodyContains(t, dbPath, "post-sync warning")
}

func TestRejectPlanDoesNotWriteVault(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ingest, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   "reject this plan",
		Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := NewPlanService(store, vaultRoot).OrganizeLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rejectResult, err := NewPlanService(store, vaultRoot).Reject(planResult.PlanID, "not ready")
	if err != nil {
		t.Fatal(err)
	}
	if rejectResult.Status != model.PlanStatusRejected {
		t.Fatalf("Reject status = %q, want rejected", rejectResult.Status)
	}
	knowledgePath := "Knowledge/Drafts/" + strings.TrimSuffix(filepath.Base(ingest.TargetPath), ".md") + ".md"
	if exists(filepath.Join(vaultRoot, filepath.FromSlash(knowledgePath))) {
		t.Fatal("rejected plan wrote Knowledge note")
	}
	if !exists(filepath.Join(vaultRoot, filepath.FromSlash(ingest.TargetPath))) {
		t.Fatal("rejected plan moved raw note")
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func prepareAwaitingOrganizePlan(t *testing.T, text string) (string, string, *storage.Store, IngestRawResult, OrganizeRawResult) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "openwhisker.db")
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(vaultRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ingest, err := NewIngestService(store, vaultRoot).IngestRaw(context.Background(), IngestRawRequest{
		Text:   text,
		Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	planResult, err := NewPlanService(store, vaultRoot).OrganizeLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return dbPath, vaultRoot, store, ingest, planResult
}

func assertOutboxKindCount(t *testing.T, dbPath, kind string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox_messages WHERE kind = ?", kind).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("outbox kind %q count = %d, want %d", kind, got, want)
	}
}

func assertOutboxBodyContains(t *testing.T, dbPath, needle string) {
	t.Helper()
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox_messages WHERE body LIKE ?", "%"+needle+"%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatalf("outbox body does not contain %q", needle)
	}
}

type fakeSyncClient struct {
	failPhase string
	syncErr   error
	onSync    func(phase string)
}

type fakeRawOrganizer struct {
	summary string
}

type checkingRawOrganizer struct{}

type checkingVaultRulesRawOrganizer struct{}

func (f fakeRawOrganizer) OrganizeRaw(ctx context.Context, req RawOrganizerRequest) (model.VaultPlan, error) {
	if err := ctx.Err(); err != nil {
		return model.VaultPlan{}, err
	}
	plan, err := buildOrganizePlan(req.Job, req.RawJob, req.RawPath, req.Conventions, req.Now)
	if err != nil {
		return model.VaultPlan{}, err
	}
	plan.Summary = f.summary
	return plan, nil
}

func (checkingRawOrganizer) OrganizeRaw(ctx context.Context, req RawOrganizerRequest) (model.VaultPlan, error) {
	if err := ctx.Err(); err != nil {
		return model.VaultPlan{}, err
	}
	if !strings.Contains(req.VaultContext.RawNote, "context should include this raw text") {
		return model.VaultPlan{}, errors.New("raw note missing from organizer context")
	}
	var sawSkill, sawProfile bool
	for _, doc := range req.VaultContext.Documents {
		if doc.Path == "OpenWhisker/VaultRawOrganizerSkill.md" && strings.Contains(doc.Content, "compiled from the current VaultProfile") {
			sawSkill = true
			continue
		}
		if doc.Path == "OpenWhisker/VaultProfile.md" && strings.Contains(doc.Content, "local convention summary") {
			sawProfile = true
			continue
		}
		if doc.Path == "AGENTS.md" || doc.Path == "Meta/Tagging.md" {
			return model.VaultPlan{}, errors.New("minimal organizer context included vault rules")
		}
	}
	if !sawSkill || !sawProfile {
		return model.VaultPlan{}, errors.New("minimal organizer context missing OpenWhisker docs")
	}
	return buildOrganizePlan(req.Job, req.RawJob, req.RawPath, req.Conventions, req.Now)
}

func (checkingVaultRulesRawOrganizer) OrganizeRaw(ctx context.Context, req RawOrganizerRequest) (model.VaultPlan, error) {
	if err := ctx.Err(); err != nil {
		return model.VaultPlan{}, err
	}
	if !strings.Contains(req.VaultContext.RawNote, "context should include this raw text") {
		return model.VaultPlan{}, errors.New("raw note missing from organizer context")
	}
	var sawSkill, sawProfile, sawRootRule, sawTagRule bool
	for _, doc := range req.VaultContext.Documents {
		if doc.Path == "OpenWhisker/VaultRawOrganizerSkill.md" && strings.Contains(doc.Content, "compiled from the current VaultProfile") {
			sawSkill = true
		}
		if doc.Path == "OpenWhisker/VaultProfile.md" && strings.Contains(doc.Content, "local convention summary") {
			sawProfile = true
		}
		if doc.Path == "AGENTS.md" && strings.Contains(doc.Content, "root vault rule") {
			sawRootRule = true
		}
		if doc.Path == "Meta/Tagging.md" && strings.Contains(doc.Content, "tag rule") {
			sawTagRule = true
		}
	}
	if !sawSkill || !sawProfile || !sawRootRule || !sawTagRule {
		return model.VaultPlan{}, errors.New("vault context rules missing")
	}
	return buildOrganizePlan(req.Job, req.RawJob, req.RawPath, req.Conventions, req.Now)
}

func (c *fakeSyncClient) Status(_ context.Context, _ string) (model.SyncResult, error) {
	return model.SyncResult{Mode: model.SyncModeOn, Backend: model.SyncBackendHeadless, Phase: model.SyncPhaseStatus, OK: true}, nil
}

func (c *fakeSyncClient) Sync(_ context.Context, _ string, phase string) (model.SyncResult, error) {
	if c.onSync != nil {
		c.onSync(phase)
	}
	result := model.SyncResult{Mode: model.SyncModeOn, Backend: model.SyncBackendHeadless, Phase: phase, OK: true}
	if c.syncErr != nil && (c.failPhase == "" || c.failPhase == phase) {
		result.OK = false
		result.Error = c.syncErr.Error()
		return result, c.syncErr
	}
	return result, nil
}
