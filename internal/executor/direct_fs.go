package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/storage"
)

type DirectFS struct {
	vaultRoot string
	store     *storage.Store
}

func NewDirectFS(vaultRoot string, store *storage.Store) DirectFS {
	return DirectFS{vaultRoot: vaultRoot, store: store}
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
	if err := e.store.AcquireLocks(uniquePaths(plan.TargetPaths), plan.ID, time.Now().UTC()); err != nil {
		return result, err
	}
	defer e.store.ReleaseLocks(plan.ID)
	if err := e.preflightApply(plan); err != nil {
		return result, err
	}
	for _, op := range plan.Operations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		applied, err := e.applyOperation(plan, op)
		if err != nil {
			return result, err
		}
		result.AppliedOperations = append(result.AppliedOperations, applied)
	}
	return result, nil
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
		AfterHash:   sha256Hex([]byte(payload.Content)),
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
	op.BeforeHash = sha256Hex(current)
	next := append(append([]byte{}, current...), []byte(payload.Content)...)
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   sha256Hex(next),
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
	op.BeforeHash = sha256Hex(current)
	next := []byte(payload.Content)
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   sha256Hex(next),
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
	op.BeforeHash = sha256Hex(current)
	next := movedContent(current, payload.ProcessingNote)
	return model.DiffEntry{
		OperationID: op.ID,
		Type:        op.Type,
		TargetPath:  op.TargetPath,
		BeforeHash:  op.BeforeHash,
		AfterHash:   sha256Hex(next),
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
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return model.AppliedOperation{}, err
	}
	if err := os.WriteFile(fullPath, []byte(payload.Content), 0o644); err != nil {
		return model.AppliedOperation{}, err
	}
	afterHash := sha256Hex([]byte(payload.Content))
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
	current, err := e.readGuarded(op.TargetPath, op.BeforeHash)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	var payload model.AppendNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.AppliedOperation{}, fmt.Errorf("decode append_note payload: %w", err)
	}
	next := append(append([]byte{}, current...), []byte(payload.Content)...)
	if err := os.WriteFile(fullPath, next, 0o644); err != nil {
		return model.AppliedOperation{}, err
	}
	return e.recordApplied(plan, op, sha256Hex(next))
}

func (e DirectFS) rewriteNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	if _, err := e.readGuarded(op.TargetPath, op.BeforeHash); err != nil {
		return model.AppliedOperation{}, err
	}
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return model.AppliedOperation{}, fmt.Errorf("decode rewrite_note payload: %w", err)
	}
	next := []byte(payload.Content)
	if err := os.WriteFile(fullPath, next, 0o644); err != nil {
		return model.AppliedOperation{}, err
	}
	return e.recordApplied(plan, op, sha256Hex(next))
}

func (e DirectFS) moveNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	current, err := e.readGuarded(op.TargetPath, op.BeforeHash)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	sourcePath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
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
	if _, err := os.Lstat(destPath); err == nil {
		return model.AppliedOperation{}, conflictError("destination already exists: %s", payload.DestinationPath)
	} else if !os.IsNotExist(err) {
		return model.AppliedOperation{}, err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return model.AppliedOperation{}, err
	}
	if err := os.Rename(sourcePath, destPath); err != nil {
		return model.AppliedOperation{}, err
	}
	next := movedContent(current, payload.ProcessingNote)
	if payload.ProcessingNote != "" {
		if err := os.WriteFile(destPath, next, 0o644); err != nil {
			return model.AppliedOperation{}, err
		}
	}
	return e.recordApplied(plan, op, sha256Hex(next))
}

func (e DirectFS) readGuarded(targetPath, beforeHash string) ([]byte, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, targetPath)
	if err != nil {
		return nil, err
	}
	current, err := os.ReadFile(fullPath)
	if err != nil {
		return nil, err
	}
	if got := sha256Hex(current); got != beforeHash {
		return nil, conflictError("hash conflict for %s: plan %s current %s", targetPath, beforeHash, got)
	}
	return current, nil
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

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
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

func uniquePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var out []string
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
