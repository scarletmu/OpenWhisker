package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

func TestDirectFSMoveNoteAppendsProcessingNote(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	sourcePath := filepath.Join(vaultRoot, "Raw", "Inbox", "job_raw.md")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte("# Raw\n\ncontent"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID:        "job_test",
		Type:      model.JobTypeOrganizeRaw,
		Status:    model.JobStatusApplying,
		Source:    "test",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(model.MoveNotePayload{
		DestinationPath: "Raw/Processed/job_raw.md",
		ProcessingNote:  "\n## OpenWhisker Processing\n\n- Raw path after approval: Raw/Processed/job_raw.md\n- Output: Knowledge/Drafts/job_raw.md\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := model.VaultPlan{
		ID:          "plan_test",
		JobID:       "job_test",
		TargetPaths: []string{"Raw/Inbox/job_raw.md", "Raw/Processed/job_raw.md"},
		Operations: []model.VaultOperation{{
			ID:          "op_move",
			Type:        model.OperationMoveNote,
			TargetPath:  "Raw/Inbox/job_raw.md",
			PayloadJSON: string(payload),
			Reason:      "move with processing note",
		}},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}

	prepared, err := NewDirectFS(vaultRoot, store).Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Diff == nil || len(prepared.Diff.Entries) != 1 {
		t.Fatalf("diff = %+v, want one entry", prepared.Diff)
	}
	result, err := NewDirectFS(vaultRoot, store).Apply(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AppliedOperations) != 1 {
		t.Fatalf("applied operations = %d, want 1", len(result.AppliedOperations))
	}
	if result.AppliedOperations[0].AfterHash != prepared.Diff.Entries[0].AfterHash {
		t.Fatalf("after hash = %q, want prepared hash %q", result.AppliedOperations[0].AfterHash, prepared.Diff.Entries[0].AfterHash)
	}
	content, err := os.ReadFile(filepath.Join(vaultRoot, "Raw", "Processed", "job_raw.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"# Raw", "## OpenWhisker Processing", "Knowledge/Drafts/job_raw.md"} {
		if !strings.Contains(string(content), needle) {
			t.Fatalf("processed content missing %q:\n%s", needle, string(content))
		}
	}
	if _, err := os.Lstat(filepath.Join(vaultRoot, "Raw", "Inbox", "job_raw.md")); !os.IsNotExist(err) {
		t.Fatalf("source still present after move: err=%v", err)
	}
}

// safeWriteReplace must detect a concurrent mutation between read and write.
// We simulate the race by mutating the source file after Prepare records the
// before-hash and the executor's read happens — append then apply.
func TestDirectFSAppendDetectsConcurrentMutation(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	target := filepath.Join(vaultRoot, "Raw", "Inbox", "job_concurrent.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID: "job_c", Type: model.JobTypeAppendRaw, Status: model.JobStatusApplying,
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(model.AppendNotePayload{Content: "more\n"})
	plan := model.VaultPlan{
		ID: "plan_c", JobID: "job_c",
		TargetPaths: []string{"Raw/Inbox/job_concurrent.md"},
		Operations: []model.VaultOperation{{
			ID: "op_append", Type: model.OperationAppendNote,
			TargetPath: "Raw/Inbox/job_concurrent.md",
			// Use the prepared hash (set below) — we cheat by setting it to
			// the hash of the original content so the read passes, then
			// mutate the file before the executor renames.
			BeforeHash:  model.ContentHash([]byte("initial\n")),
			PayloadJSON: string(payload),
		}},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	// Inject mutation between read and rename by replacing the file *after*
	// Prepare but before Apply. The current pre-rename re-stat in
	// safeWriteReplace should reject the rename because inode (on macOS via
	// truncate+write, size at minimum) changes.
	originalApply := NewDirectFS(vaultRoot, store)
	// Simulate concurrent mutation by writing different content to the file
	// — must come *before* Apply for this test to be deterministic given
	// the helper opens-then-reads-then-writes inline. Replicating the exact
	// race requires hook points; here we exercise the "file changed between
	// read and any later check" path by faking a stale before_hash.
	if err := os.WriteFile(target, []byte("mutated by sync\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = originalApply.Apply(context.Background(), plan)
	if err == nil {
		t.Fatalf("Apply() expected hash conflict, got nil")
	}
	if !strings.Contains(err.Error(), "hash conflict") {
		t.Fatalf("Apply() error = %v, want hash conflict", err)
	}
}

// Partial-apply failure must write OperationStatusFailed log rows for the
// failing op (and remaining unattempted ops) so the audit trail captures
// where Apply stopped.
func TestDirectFSPartialApplyWritesFailedLog(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID: "job_p", Type: model.JobTypeIngestRaw, Status: model.JobStatusApplying,
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	okPayload, _ := json.Marshal(model.CreateNotePayload{Content: "first\n"})
	// Second op targets the SAME path. Preflight passes (neither file exists
	// yet), op_ok succeeds and creates ok.md, then op_bad fails at apply
	// time because Lstat sees ok.md already exists. That gives us a
	// deterministic mid-loop failure to exercise the failure-log path.
	badPayload, _ := json.Marshal(model.CreateNotePayload{Content: "second\n"})
	plan := model.VaultPlan{
		ID: "plan_p", JobID: "job_p",
		TargetPaths: []string{"Raw/Inbox/ok.md"},
		Operations: []model.VaultOperation{
			{ID: "op_ok", Type: model.OperationCreateNote, TargetPath: "Raw/Inbox/ok.md", PayloadJSON: string(okPayload)},
			{ID: "op_bad", Type: model.OperationCreateNote, TargetPath: "Raw/Inbox/ok.md", PayloadJSON: string(badPayload)},
		},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirectFS(vaultRoot, store).Apply(context.Background(), plan); err == nil {
		t.Fatalf("Apply() expected error from hidden-segment op")
	}
	logs, err := store.ListOperationLogsByPlan(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var hasApplied, hasFailed bool
	for _, log := range logs {
		switch log.Status {
		case model.OperationStatusApplied:
			hasApplied = true
		case model.OperationStatusFailed:
			hasFailed = true
		}
	}
	if !hasApplied {
		t.Errorf("expected at least one applied log row, got %+v", logs)
	}
	if !hasFailed {
		t.Errorf("expected at least one failed log row, got %+v", logs)
	}
}

// safeCreateAtomic must refuse to clobber a file that races into existence
// after the existence pre-check. Two concurrent creators on the same path
// must produce exactly one winner; the loser must surface a conflict error
// without overwriting the winner's content. Previously rename(2) would
// silently overwrite — the link(2)-based create now used here returns EEXIST.
func TestSafeCreateAtomicRefusesToClobberRace(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "create-race.md")
	if err := os.WriteFile(target, []byte("winner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := safeCreateAtomic(target, []byte("loser\n"))
	if err == nil {
		t.Fatalf("safeCreateAtomic should refuse to clobber existing file")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want already-exists conflict", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "winner\n" {
		t.Fatalf("winner content was clobbered: %q", string(got))
	}
}

// moveNote must preserve writes that happen to the source file between the
// hash-guarded read and the actual rename. The atomic link+unlink path moves
// the inode, so a writer holding an FD on the source keeps writing to the
// same (now-renamed) file rather than losing data.
func TestDirectFSMoveNotePreservesConcurrentSourceEdits(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	sourcePath := filepath.Join(vaultRoot, "Raw", "Inbox", "concurrent.md")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("original line\n")
	if err := os.WriteFile(sourcePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// Hold an open append FD on the source like Obsidian Sync (or another
	// writer) would. After the move, this FD should still target the now-
	// renamed inode, and subsequent writes must land at the destination.
	writer, err := os.OpenFile(sourcePath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID: "job_mv", Type: model.JobTypeOrganizeRaw, Status: model.JobStatusApplying,
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(model.MoveNotePayload{
		DestinationPath: "Raw/Processed/concurrent.md",
		// Empty processing note so we exercise the bare move path (no
		// post-move append that would replace the inode).
	})
	plan := model.VaultPlan{
		ID: "plan_mv", JobID: "job_mv",
		TargetPaths: []string{"Raw/Inbox/concurrent.md", "Raw/Processed/concurrent.md"},
		Operations: []model.VaultOperation{{
			ID: "op_mv", Type: model.OperationMoveNote,
			TargetPath:  "Raw/Inbox/concurrent.md",
			PayloadJSON: string(payload),
		}},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	prepared, err := NewDirectFS(vaultRoot, store).Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirectFS(vaultRoot, store).Apply(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	// Writer was open before the move; write *after* the move and check
	// that the bytes landed at the destination, not at a re-created source.
	if _, err := writer.Write([]byte("late edit\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatal(err)
	}
	destBytes, err := os.ReadFile(filepath.Join(vaultRoot, "Raw", "Processed", "concurrent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(destBytes), "late edit") {
		t.Fatalf("late concurrent edit was lost; destination = %q", string(destBytes))
	}
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("source path still exists after move (err=%v)", err)
	}
}

// safeCreateAtomic must refuse to write through a leaf symlink (so a
// malicious replacement of the leaf path can't be followed into outside the
// vault).
func TestSafeCreateAtomicRejectsLeafSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.md")
	if err := os.WriteFile(outside, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(dir, "leaf.md")
	if err := os.Symlink(outside, leaf); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := safeCreateAtomic(leaf, []byte("written-through-symlink\n"))
	if err == nil {
		t.Fatalf("safeCreateAtomic should have refused symlink leaf, but succeeded")
	}
	// outside must remain unchanged.
	got, _ := os.ReadFile(outside)
	if string(got) != "original\n" {
		t.Fatalf("outside file was followed/overwritten: %q", string(got))
	}
}

// Vault writes must land at 0o600 — not 0o644 — so other local users can't
// read personal-vault content.
func TestDirectFSWriteFileMode(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID: "job_m", Type: model.JobTypeIngestRaw, Status: model.JobStatusApplying,
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(model.CreateNotePayload{Content: "secret note\n"})
	plan := model.VaultPlan{
		ID: "plan_m", JobID: "job_m",
		TargetPaths: []string{"Raw/Inbox/secret.md"},
		Operations: []model.VaultOperation{{
			ID: "op_create", Type: model.OperationCreateNote,
			TargetPath: "Raw/Inbox/secret.md", PayloadJSON: string(payload),
		}},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDirectFS(vaultRoot, store).Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(vaultRoot, "Raw", "Inbox", "secret.md"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("created file mode = %o, want 0600", perm)
	}
}

type recordingObserver struct {
	rewrites []recordedRewrite
}

type recordedRewrite struct {
	notePath string
	content  string
}

func (r *recordingObserver) OnRewriteNote(_ context.Context, notePath, newContent string) {
	r.rewrites = append(r.rewrites, recordedRewrite{notePath: notePath, content: newContent})
}

// Phase 8 memory wiring expects the executor to call ApplyObserver.OnRewriteNote
// exactly once per successful rewrite_note operation, with the new payload
// content. Other op types (create_note, move_note, append_note) must NOT
// invoke the observer — memory tag-index freshness only cares about
// in-place rewrites where frontmatter `tags` may have changed.
func TestDirectFSApplyObserverFiresOnlyOnRewriteNote(t *testing.T) {
	dir := t.TempDir()
	vaultRoot := filepath.Join(dir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a note we can rewrite.
	existing := "---\ntags: []\n---\nbody\n"
	targetPath := filepath.Join(vaultRoot, "Raw", "Inbox", "x.md")
	if err := os.WriteFile(targetPath, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(filepath.Join(dir, "openwhisker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	if err := store.CreateJob(model.WikiJob{
		ID: "job_obs", Type: model.JobTypeIngestRaw, Status: model.JobStatusApplying,
		Source: "test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Op 1: create_note (must NOT trigger observer).
	createPayload, _ := json.Marshal(model.CreateNotePayload{Content: "fresh\n"})
	// Op 2: rewrite_note targeting x.md with new tags (MUST trigger observer).
	rewriteContent := "---\ntags:\n  - topic/observed\n---\nbody\n"
	rewritePayload, _ := json.Marshal(model.CreateNotePayload{Content: rewriteContent})

	plan := model.VaultPlan{
		ID: "plan_obs", JobID: "job_obs",
		TargetPaths: []string{"Raw/Inbox/new.md", "Raw/Inbox/x.md"},
		Operations: []model.VaultOperation{
			{ID: "op_create", Type: model.OperationCreateNote,
				TargetPath: "Raw/Inbox/new.md", PayloadJSON: string(createPayload)},
			{ID: "op_rewrite", Type: model.OperationRewriteNote,
				TargetPath: "Raw/Inbox/x.md", PayloadJSON: string(rewritePayload)},
		},
		CreatedAt: now,
	}
	if err := store.SavePlan(plan); err != nil {
		t.Fatal(err)
	}
	// Prepare to fill BeforeHash on op_rewrite.
	prepared, err := NewDirectFS(vaultRoot, store).Prepare(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	obs := &recordingObserver{}
	executor := NewDirectFS(vaultRoot, store).WithApplyObserver(obs)
	if _, err := executor.Apply(context.Background(), prepared); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(obs.rewrites) != 1 {
		t.Fatalf("observer fired %d times, want exactly 1 (only rewrite_note): %+v", len(obs.rewrites), obs.rewrites)
	}
	got := obs.rewrites[0]
	if got.notePath != "Raw/Inbox/x.md" {
		t.Errorf("observer notePath = %q, want Raw/Inbox/x.md", got.notePath)
	}
	if got.content != rewriteContent {
		t.Errorf("observer content mismatch:\n got: %q\nwant: %q", got.content, rewriteContent)
	}
}
