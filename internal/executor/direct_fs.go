package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func (e DirectFS) Apply(ctx context.Context, plan model.VaultPlan) (model.VaultApplyResult, error) {
	var result model.VaultApplyResult
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
	default:
		return model.AppliedOperation{}, fmt.Errorf("unsupported operation type %q", op.Type)
	}
}

func (e DirectFS) createNote(plan model.VaultPlan, op model.VaultOperation) (model.AppliedOperation, error) {
	fullPath, err := ResolveVaultPath(e.vaultRoot, op.TargetPath)
	if err != nil {
		return model.AppliedOperation{}, err
	}
	if _, err := os.Lstat(fullPath); err == nil {
		return model.AppliedOperation{}, fmt.Errorf("target already exists: %s", op.TargetPath)
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

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
