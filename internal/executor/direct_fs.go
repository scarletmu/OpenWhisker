package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// ApplyObserver receives best-effort notifications after a successful
// per-operation Apply. Implementations must not block; errors are
// discarded by callers because a side-effect-cache observer must never
// fail a successfully-applied vault write.
//
// Phase 8 wires the memory service in here so memory_tag_index +
// memory_known_tags stay fresh without re-scanning the vault after every
// enrich Apply. See docs/phases/phase-8-memory-recall.md.
type ApplyObserver interface {
	OnRewriteNote(ctx context.Context, notePath, newContent string)
}

type DirectFS struct {
	vaultRoot     string
	store         *storage.Store
	applyObserver ApplyObserver
}

func NewDirectFS(vaultRoot string, store *storage.Store) DirectFS {
	return DirectFS{vaultRoot: vaultRoot, store: store}
}

// WithApplyObserver returns a copy of the executor with the supplied
// observer attached. DirectFS is a value type, so callers store the
// returned value rather than mutating in place.
func (e DirectFS) WithApplyObserver(obs ApplyObserver) DirectFS {
	e.applyObserver = obs
	return e
}

func (e DirectFS) Prepare(ctx context.Context, plan model.VaultPlan) (model.VaultPlan, error) {
	var entries []model.DiffEntry
	for i, op := range plan.Operations {
		if err := ctx.Err(); err != nil {
			return model.VaultPlan{}, err
		}
		entry, preparedOp, err := e.prepareOperation(op)
		if err != nil {
			return model.VaultPlan{}, err
		}
		plan.Operations[i] = preparedOp
		entries = append(entries, entry)
	}
	plan.Diff = &model.VaultDiff{
		PlanID:  plan.ID,
		Summary: plan.Summary,
		Entries: entries,
	}
	return plan, nil
}

func (e DirectFS) Apply(ctx context.Context, plan model.VaultPlan) (model.VaultApplyResult, error) {
	var result model.VaultApplyResult
	if err := e.store.AcquireLocks(uniqueStrings(plan.TargetPaths), plan.ID, time.Now().UTC()); err != nil {
		return result, err
	}
	defer e.store.ReleaseLocks(plan.ID)
	if err := e.preflightApply(plan); err != nil {
		return result, err
	}
	for i, op := range plan.Operations {
		if err := ctx.Err(); err != nil {
			e.logRemainingFailed(plan, plan.Operations[i:], err)
			return result, err
		}
		applied, err := e.applyOperation(plan, op)
		if err != nil {
			// Audit trail: record this op as failed and every remaining op as
			// failed too so operators can see exactly where the partial apply
			// stopped. Earlier ops already wrote applied-status logs from
			// inside applyOperation.
			e.logRemainingFailed(plan, plan.Operations[i:], err)
			return result, err
		}
		result.AppliedOperations = append(result.AppliedOperations, applied)
		e.notifyApplied(ctx, op)
	}
	return result, nil
}

// notifyApplied fans out the applied operation to the observer (if any).
// Phase 8 memory wiring only cares about rewrite_note for tag-index
// freshness; other op types are passed through silently so future
// observers can opt in without changing this dispatcher.
func (e DirectFS) notifyApplied(ctx context.Context, op model.VaultOperation) {
	if e.applyObserver == nil {
		return
	}
	if op.Type != model.OperationRewriteNote {
		return
	}
	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return
	}
	e.applyObserver.OnRewriteNote(ctx, op.TargetPath, payload.Content)
}

// logRemainingFailed records OperationStatusFailed log rows for the failing
// op (head of the slice) and any ops that never got attempted. Best-effort:
// errors writing the audit row itself are swallowed because the caller is
// already returning the underlying apply error.
func (e DirectFS) logRemainingFailed(plan model.VaultPlan, ops []model.VaultOperation, applyErr error) {
	now := time.Now().UTC()
	errText := ""
	if applyErr != nil {
		errText = applyErr.Error()
	}
	for _, op := range ops {
		log := model.VaultOperationLog{
			ID:          model.NewID("vop"),
			PlanID:      plan.ID,
			JobID:       plan.JobID,
			OpType:      op.Type,
			TargetPath:  op.TargetPath,
			BeforeHash:  op.BeforeHash,
			AfterHash:   "",
			PayloadJSON: op.PayloadJSON,
			ResultJSON:  fmt.Sprintf(`{"error":%q}`, errText),
			Reason:      op.Reason,
			Status:      model.OperationStatusFailed,
			CreatedAt:   now,
		}
		_ = e.store.AppendOperationLog(log)
	}
}

func (e DirectFS) applyOperation(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	switch op.Type {
	case model.OperationCreateNote, model.OperationWriteAgentReport:
		return e.createNote(plan, op)
	case model.OperationAppendNote:
		return e.appendNote(plan, op)
	case model.OperationRewriteNote:
		return e.rewriteNote(plan, op)
	case model.OperationMoveNote:
		return e.moveNote(plan, op)
	default:
		return model.AppliedOperation{}, fmt.Errorf("unsupported operation type %q", op.Type)
	}
}

func (e DirectFS) preflightApply(plan model.VaultPlan) error {
	for _, op := range plan.Operations {
		switch op.Type {
		case model.OperationCreateNote, model.OperationWriteAgentReport:
			fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(fullPath); err == nil {
				return conflictError("target already exists: %s", op.TargetPath)
			} else if !os.IsNotExist(err) {
				return err
			}
		case model.OperationAppendNote, model.OperationRewriteNote:
			if _, err := e.readGuarded(op.TargetPath, op.BeforeHash); err != nil {
				return err
			}
		case model.OperationMoveNote:
			if _, err := e.readGuarded(op.TargetPath, op.BeforeHash); err != nil {
				return err
			}
			var payload model.MoveNotePayload
			if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
				return fmt.Errorf("decode move_note payload: %w", err)
			}
			destPath, err := ResolveVaultPath(e.vaultRoot, payload.DestinationPath)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(destPath); err == nil {
				return conflictError("destination already exists: %s", payload.DestinationPath)
			} else if !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (e DirectFS) prepareOperation(op model.VaultOperation) (model.DiffEntry, model.VaultOperation, error) {
	switch op.Type {
	case model.OperationCreateNote, model.OperationWriteAgentReport:
		return e.prepareCreate(op)
	case model.OperationAppendNote:
		return e.prepareAppend(op)
	case model.OperationRewriteNote:
		return e.prepareRewrite(op)
	case model.OperationMoveNote:
		return e.prepareMove(op)
	default:
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("unsupported operation type %q", op.Type)
	}
}

func (e DirectFS) prepareCreate(op model.VaultOperation) (model.DiffEntry, model.VaultOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	if _, err := os.Lstat(fullPath); err == nil {
		return model.DiffEntry{}, model.VaultOperation{}, conflictError("target already exists: %s", op.TargetPath)
	} else if !os.IsNotExist(err) {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("decode create_note payload: %w", err)
	}
	if payload.Content == "" {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("create_note payload content is required")
	}
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  "",
		AfterHash:   model.ContentHash([]byte(payload.Content)),
		Summary:     "create note",
		Preview:     preview(payload.Content),
	}, op, nil
}

func (e DirectFS) prepareAppend(op model.VaultOperation) (model.DiffEntry, model.VaultOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	current, err := os.ReadFile(fullPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	var payload model.AppendNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("decode append_note payload: %w", err)
	}
	if payload.Content == "" {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("append_note payload content is required")
	}
	op.BeforeHash = model.ContentHash(current)
	next := append(append([]byte{}, current...), []byte(payload.Content)...)
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   model.ContentHash(next),
		Summary:     "append note",
		Preview:     preview(payload.Content),
	}, op, nil
}

func (e DirectFS) prepareRewrite(op model.VaultOperation) (model.DiffEntry, model.VaultOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	current, err := os.ReadFile(fullPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("decode rewrite_note payload: %w", err)
	}
	if payload.Content == "" {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("rewrite_note payload content is required")
	}
	op.BeforeHash = model.ContentHash(current)
	next := []byte(payload.Content)
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   model.ContentHash(next),
		Summary:     "rewrite note",
		Preview:     preview(payload.Content),
	}, op, nil
}

func (e DirectFS) prepareMove(op model.VaultOperation) (model.DiffEntry, model.VaultOperation, error) {
	sourcePath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	current, err := os.ReadFile(sourcePath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	var payload model.MoveNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, fmt.Errorf("decode move_note payload: %w", err)
	}
	destPath, err := ResolveVaultPath(e.vaultRoot, payload.DestinationPath)
	if err != nil {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	if _, err := os.Lstat(destPath); err == nil {
		return model.DiffEntry{}, model.VaultOperation{}, conflictError("destination already exists: %s", payload.DestinationPath)
	} else if !os.IsNotExist(err) {
		return model.DiffEntry{}, model.VaultOperation{}, err
	}
	op.BeforeHash = model.ContentHash(current)
	next := movedContent(current, payload.ProcessingNote)
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   model.ContentHash(next),
		Summary:     "move note to " + payload.DestinationPath,
		Preview:     preview(op.TargetPath + " -> " + payload.DestinationPath + "\n" + payload.ProcessingNote),
	}, op, nil
}

func (e DirectFS) createNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	if _, err := os.Lstat(fullPath); err == nil {
		return model.AppliedOperation{}, conflictError("target already exists: %s", op.TargetPath)
	} else if !os.IsNotExist(err) {
		return model.AppliedOperation{}, err
	}
	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.AppliedOperation{}, fmt.Errorf("decode create_note payload: %w", err)
	}
	if payload.Content == "" {
		return model.AppliedOperation{}, fmt.Errorf("create_note payload content is required")
	}
	if err := safeCreateAtomic(fullPath, []byte(payload.Content)); err != nil {
		return model.AppliedOperation{}, err
	}
	afterHash := model.ContentHash([]byte(payload.Content))
	applied := model.AppliedOperation{
		OperationID: op.ID,
		TargetPath:  op.TargetPath,
		AfterHash:   afterHash,
	}
	resultJSON, _ := json.Marshal(applied)
	now := time.Now().UTC()
	log := model.VaultOperationLog{
		ID:          model.NewID("vop"),
		PlanID:      plan.ID,
		JobID:       plan.JobID,
		OpType:      op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   afterHash,
		PayloadJSON: op.PayloadJSON,
		ResultJSON:  string(resultJSON),
		Reason:      op.Reason,
		Status:      model.OperationStatusApplied,
		CreatedAt:   now,
		AppliedAt:   &now,
	}
	if err := e.store.AppendOperationLog(log); err != nil {
		return model.AppliedOperation{}, err
	}
	return applied, nil
}

func (e DirectFS) appendNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	current, guardInfo, err := readGuardedFile(fullPath, op.TargetPath, op.BeforeHash)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	var payload model.AppendNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.AppliedOperation{}, fmt.Errorf("decode append_note payload: %w", err)
	}
	next := append(append([]byte{}, current...), []byte(payload.Content)...)
	if err := safeWriteReplace(fullPath, next, guardInfo); err != nil {
		return model.AppliedOperation{}, err
	}
	return e.recordApplied(plan, op, model.ContentHash(next))
}

func (e DirectFS) rewriteNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	_, guardInfo, err := readGuardedFile(fullPath, op.TargetPath, op.BeforeHash)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.AppliedOperation{}, fmt.Errorf("decode rewrite_note payload: %w", err)
	}
	next := []byte(payload.Content)
	if err := safeWriteReplace(fullPath, next, guardInfo); err != nil {
		return model.AppliedOperation{}, err
	}
	return e.recordApplied(plan, op, model.ContentHash(next))
}

// moveNote moves the source note to the destination atomically and then, if
// the operation carries a processing note, appends it to the destination via
// the standard safeWriteReplace path. Atomic-link semantics preserve any
// concurrent edit to the source (the inode moves with the file) instead of
// the previous read-then-write window that silently dropped concurrent writes.
func (e DirectFS) moveNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	sourcePath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	// Hash-guard the read so a stale plan against a modified source still
	// fails fast; we don't keep the FileInfo because the move below operates
	// at the directory-entry level, not on the open FD.
	if _, _, err := readGuardedFile(sourcePath, op.TargetPath, op.BeforeHash); err != nil {
		return model.AppliedOperation{}, err
	}
	var payload model.MoveNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.AppliedOperation{}, fmt.Errorf("decode move_note payload: %w", err)
	}
	destPath, err := ResolveVaultPath(e.vaultRoot, payload.DestinationPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	if err := safeMoveAtomic(sourcePath, destPath); err != nil {
		return model.AppliedOperation{}, err
	}
	// Read the destination back to compute the final after-hash. If a
	// processing note was provided, append it under the same TOCTOU guard
	// safeWriteReplace uses elsewhere (the guardInfo from this single
	// safeReadAll is the basis for the pre-rename re-check).
	finalContent, guardInfo, err := safeReadAll(destPath)
	if err != nil {
		return model.AppliedOperation{}, fmt.Errorf("move_note: re-read destination %s: %w", payload.DestinationPath, err)
	}
	if payload.ProcessingNote != "" {
		next := movedContent(finalContent, payload.ProcessingNote)
		if err := safeWriteReplace(destPath, next, guardInfo); err != nil {
			return model.AppliedOperation{}, err
		}
		finalContent = next
	}
	return e.recordApplied(plan, op, model.ContentHash(finalContent))
}

// readGuardedFile opens fullPath with O_NOFOLLOW (rejecting symlink swaps at
// the leaf), reads its full content, verifies the sha256 matches beforeHash,
// and returns content + an os.FileInfo snapshot used by safeWriteReplace to
// detect mutation between read and write.
func readGuardedFile(fullPath, targetPath, beforeHash string) ([]byte, os.FileInfo, error) {
	current, info, err := safeReadAll(fullPath)
	if err != nil {
		return nil, nil, err
	}
	if got := model.ContentHash(current); got != beforeHash {
		return nil, nil, conflictError("hash conflict for %s: plan %s current %s", targetPath, beforeHash, got)
	}
	return current, info, nil
}

// readGuarded is kept for callers (preflightApply) that only need to verify
// the hash without taking a guard handle.
func (e DirectFS) readGuarded(targetPath, beforeHash string) ([]byte, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, targetPath)
	if err != nil {
		return nil, err
	}
	content, _, err := readGuardedFile(fullPath, targetPath, beforeHash)
	return content, err
}

func (e DirectFS) recordApplied(plan model.VaultPlan, op model.VaultOperation, afterHash string) (model.AppliedOperation, error) {
	applied := model.AppliedOperation{
		OperationID: op.ID,
		TargetPath:  op.TargetPath,
		AfterHash:   afterHash,
	}
	resultJSON, _ := json.Marshal(applied)
	now := time.Now().UTC()
	log := model.VaultOperationLog{
		ID:          model.NewID("vop"),
		PlanID:      plan.ID,
		JobID:       plan.JobID,
		OpType:      op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   afterHash,
		PayloadJSON: op.PayloadJSON,
		ResultJSON:  string(resultJSON),
		Reason:      op.Reason,
		Status:      model.OperationStatusApplied,
		CreatedAt:   now,
		AppliedAt:   &now,
	}
	if err := e.store.AppendOperationLog(log); err != nil {
		return model.AppliedOperation{}, err
	}
	return applied, nil
}

func preview(content string) string {
	content = strings.TrimSpace(content)
	if len(content) <= 500 {
		return content
	}
	return content[:500] + "\n..."
}

func movedContent(current []byte, processingNote string) []byte {
	if processingNote == "" {
		return current
	}
	next := append([]byte{}, current...)
	if len(next) > 0 && !strings.HasSuffix(string(next), "\n") {
		next = append(next, '\n')
	}
	next = append(next, []byte(processingNote)...)
	return next
}
