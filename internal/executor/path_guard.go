package executor

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// vaultFileMode is the file mode used for vault notes. 0o600 prevents other
// local users from reading personal-vault content; 0o700 for parent dirs.
const (
	vaultFileMode os.FileMode = 0o600
	vaultDirMode  os.FileMode = 0o700
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

// safeReadAll opens fullPath via O_RDONLY|O_NOFOLLOW (rejecting any symlink at
// the leaf), reads its full content, and returns the content plus a stat
// snapshot taken from the open FD. The snapshot is the basis for the
// pre-rename TOCTOU re-check in safeWriteAtomic.
func safeReadAll(fullPath string) ([]byte, os.FileInfo, error) {
	f, err := os.OpenFile(fullPath, os.O_RDONLY|syscallNoFollow, 0)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("refusing to read non-regular file %s", fullPath)
	}
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return content, info, nil
}

// safeCreateAtomic creates fullPath with the given content, refusing to
// follow a symlink at the leaf and refusing to clobber an existing file.
// The atomic create-only guarantee comes from link(2): unlike rename(2) it
// will not silently overwrite an existing destination, so two concurrent
// creators race deterministically (only one's link call succeeds).
func safeCreateAtomic(fullPath string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(fullPath), vaultDirMode); err != nil {
		return err
	}
	tmpPath, err := writeTempFile(fullPath, content)
	if err != nil {
		return err
	}
	defer func() {
		// Best-effort cleanup. If the link succeeded we're removing the
		// extra name, leaving the linked file at fullPath; if it failed we
		// remove the temp file.
		_ = os.Remove(tmpPath)
	}()
	if err := os.Link(tmpPath, fullPath); err != nil {
		if os.IsExist(err) {
			return conflictError("target already exists: %s", fullPath)
		}
		return err
	}
	syncParentDir(fullPath)
	return nil
}

// safeMoveAtomic moves sourcePath to destPath atomically and refuses to
// clobber destPath. Implemented as link(source -> dest) + unlink(source) so
// the destination either materializes whole or not at all, and any concurrent
// edit to sourcePath rides along with the inode (no read-then-write window
// that could drop concurrent writes). Caller must have already verified
// sourcePath's content hash through readGuardedFile.
func safeMoveAtomic(sourcePath, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), vaultDirMode); err != nil {
		return err
	}
	if err := os.Link(sourcePath, destPath); err != nil {
		if os.IsExist(err) {
			return conflictError("destination already exists: %s", destPath)
		}
		return err
	}
	if err := os.Remove(sourcePath); err != nil {
		// Roll back the link so we don't leave a duplicate. Best-effort —
		// surface the original error either way.
		_ = os.Remove(destPath)
		return fmt.Errorf("safe move: link succeeded but failed to unlink source %s: %w", sourcePath, err)
	}
	syncParentDir(destPath)
	syncParentDir(sourcePath)
	return nil
}

// writeTempFile writes content to a freshly-created O_EXCL temp file in the
// same directory as fullPath. Returns the temp file path on success — caller
// is responsible for moving it into place (link or rename) and cleaning up.
func writeTempFile(fullPath string, content []byte) (string, error) {
	dir := filepath.Dir(fullPath)
	tmp, err := os.CreateTemp(dir, ".openwhisker-write-*")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Chmod(vaultFileMode); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", err
	}
	return tmpPath, nil
}

func syncParentDir(path string) {
	dir := filepath.Dir(path)
	df, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = df.Sync()
	_ = df.Close()
}

// safeWriteReplace atomically replaces an existing fullPath, verifying that
// the path's inode + size still match guardInfo immediately before renaming.
// Mismatch produces a conflictError (the file was mutated by another process
// between the guarded read and our write). guardInfo must come from a prior
// safeReadAll on fullPath.
func safeWriteReplace(fullPath string, content []byte, guardInfo os.FileInfo) error {
	if guardInfo == nil {
		return errors.New("safeWriteReplace requires a guard FileInfo")
	}
	return writeViaTempAndRename(fullPath, content, guardInfo)
}

// writeViaTempAndRename is the in-place replace path used by
// safeWriteReplace. It writes content into a temp file in the same directory,
// re-checks that fullPath still matches guardInfo (TOCTOU between the
// guarded read and now), then renames the temp over the target. Rename is
// safe here because we *want* to replace the existing inode atomically.
func writeViaTempAndRename(fullPath string, content []byte, guardInfo os.FileInfo) error {
	if guardInfo == nil {
		// Defensive: callers must use safeCreateAtomic for the create path.
		return errors.New("writeViaTempAndRename requires a guard FileInfo")
	}
	tmpPath, err := writeTempFile(fullPath, content)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmpPath) }
	cur, err := os.Lstat(fullPath)
	if err != nil {
		cleanup()
		return fmt.Errorf("pre-rename re-stat of %s: %w", fullPath, err)
	}
	if !cur.Mode().IsRegular() {
		cleanup()
		return conflictError("target %s changed type since read (now mode=%s)", fullPath, cur.Mode())
	}
	if !sameFile(cur, guardInfo) {
		cleanup()
		return conflictError("target %s was modified between read and write", fullPath)
	}
	if err := os.Rename(tmpPath, fullPath); err != nil {
		cleanup()
		return err
	}
	syncParentDir(fullPath)
	return nil
}
