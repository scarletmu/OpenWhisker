package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/scarletmu/openwhisker/internal/model"
)

type Checker struct{}

func NewChecker() Checker {
	return Checker{}
}

func (Checker) Check(plan model.VaultPlan) error {
	if plan.RiskLevel != model.RiskLow {
		return fmt.Errorf("risk %q is not auto-allowed", plan.RiskLevel)
	}
	if plan.RequiresApproval {
		return errors.New("plan requires approval")
	}
	if len(plan.Operations) == 0 {
		return errors.New("plan has no operations")
	}
	for _, op := range plan.Operations {
		if op.RiskLevel != model.RiskLow {
			return fmt.Errorf("operation %s risk %q is not auto-allowed", op.ID, op.RiskLevel)
		}
		if err := validateRelativeVaultPath(op.TargetPath); err != nil {
			return fmt.Errorf("operation %s target path: %w", op.ID, err)
		}
		switch op.Type {
		case model.OperationCreateNote:
			if !strings.HasPrefix(op.TargetPath, "Raw/Inbox/") {
				return fmt.Errorf("create_note target %q is outside Raw/Inbox", op.TargetPath)
			}
		case model.OperationWriteAgentReport:
			if !strings.HasPrefix(op.TargetPath, "Meta/Reports/") {
				return fmt.Errorf("write_agent_report target %q is outside Meta/Reports", op.TargetPath)
			}
		default:
			return fmt.Errorf("operation type %q is not allowed in phase 1", op.Type)
		}
	}
	return nil
}

func validateRelativeVaultPath(path string) error {
	if path == "" {
		return errors.New("empty path")
	}
	if filepath.IsAbs(path) {
		return errors.New("absolute paths are disabled")
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean != path {
		return errors.New("path must already be clean and relative")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return errors.New("path traversal is disabled")
		}
		if strings.HasPrefix(part, ".") {
			return errors.New("hidden path segments are disabled")
		}
	}
	return nil
}
