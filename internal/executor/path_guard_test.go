package executor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveVaultPathAllowsRelativePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveVaultPath(root, "Raw/Inbox/job_test.md")
	if err != nil {
		t.Fatalf("ResolveVaultPath() error = %v", err)
	}
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(rootEval, "Raw", "Inbox", "job_test.md")
	if got != want {
		t.Fatalf("ResolveVaultPath() = %q, want %q", got, want)
	}
}

func TestResolveVaultPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Raw", "Inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "Raw", "Inbox", "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ResolveVaultPath(root, "Raw/Inbox/escape/job_test.md"); err == nil {
		t.Fatal("ResolveVaultPath() expected symlink escape error")
	}
}

func TestResolveVaultPathRejectsHiddenSegments(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveVaultPath(root, "Raw/Inbox/.obsidian/config.md"); err == nil {
		t.Fatal("ResolveVaultPath() expected hidden segment error")
	}
}
