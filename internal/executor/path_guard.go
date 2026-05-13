package executor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func ResolveVaultPath(vaultRoot, targetPath string) (string, error) {
	if vaultRoot == "" {
		return "", errors.New("vault root is required")
	}
	if filepath.IsAbs(targetPath) {
		return "", errors.New("absolute target path is disabled")
	}
	cleanTarget := filepath.ToSlash(filepath.Clean(targetPath))
	if cleanTarget == "." || cleanTarget != targetPath {
		return "", errors.New("target path must already be clean and relative")
	}
	for _, part := range strings.Split(cleanTarget, "/") {
		if part == ".." {
			return "", errors.New("path traversal is disabled")
		}
		if strings.HasPrefix(part, ".") {
			return "", errors.New("hidden path segments are disabled")
		}
	}

	root, err := filepath.Abs(vaultRoot)
	if err != nil {
		return "", err
	}
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve vault root: %w", err)
	}

	candidate := filepath.Join(rootEval, filepath.FromSlash(cleanTarget))
	parent := filepath.Dir(candidate)
	parentEval, err := filepath.EvalSymlinks(parent)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve target parent: %w", err)
		}
		parentEval = firstExistingParent(parent, rootEval)
	}
	if !isWithin(rootEval, parentEval) {
		return "", errors.New("target parent escapes vault root")
	}
	if existing, err := filepath.EvalSymlinks(candidate); err == nil && !isWithin(rootEval, existing) {
		return "", errors.New("target path escapes vault root")
	}
	return candidate, nil
}

func firstExistingParent(path, fallback string) string {
	for {
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			if evaluated, err := filepath.EvalSymlinks(path); err == nil {
				return evaluated
			}
			return path
		}
		next := filepath.Dir(path)
		if next == path {
			return fallback
		}
		path = next
	}
}

func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
