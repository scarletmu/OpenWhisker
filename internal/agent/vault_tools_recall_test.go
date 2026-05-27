package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scarletmu/openwhisker/internal/memory"
	"github.com/scarletmu/openwhisker/internal/storage"
)

// The recall_memory tool must:
//   - reject calls when memory.Service is unconfigured (defense in depth)
//   - reject calls when both query and tags are empty (bad_args)
//   - route through memory.Recall and return a markdown-formatted payload
//     containing the hit path and its source label
func TestRecallMemoryTool(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	writeVaultFile(t, vault, "Knowledge/a.md", "---\ntags:\n  - topic/golang\n---\nbody\n")

	store, err := storage.Open(filepath.Join(dir, "ow.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc, err := memory.NewService(memory.Config{Store: store, VaultRoot: vault})
	if err != nil {
		t.Fatalf("memory.NewService: %v", err)
	}
	if err := svc.RescanAll(context.Background()); err != nil {
		t.Fatalf("rescan: %v", err)
	}

	skill := testSkill([]string{"Knowledge/", "Interview/", "Life/"}, []string{"recall_memory"})

	t.Run("missing memory service rejected", func(t *testing.T) {
		exec := VaultToolExecutor{VaultRoot: vault, Skill: skill}
		_, err := exec.Execute(context.Background(), ToolInvocation{
			Name: "recall_memory",
			Args: json.RawMessage(`{"tags":["topic/golang"]}`),
		})
		if err == nil {
			t.Fatal("expected memory_unavailable error when Memory is nil")
		}
		var toolErr *ToolError
		if !errorsAs(err, &toolErr) || toolErr.Code != "memory_unavailable" {
			t.Errorf("expected ToolError code memory_unavailable, got %v", err)
		}
	})

	t.Run("empty query and tags rejected", func(t *testing.T) {
		exec := VaultToolExecutor{VaultRoot: vault, Skill: skill, Memory: svc}
		_, err := exec.Execute(context.Background(), ToolInvocation{
			Name: "recall_memory",
			Args: json.RawMessage(`{}`),
		})
		if err == nil {
			t.Fatal("expected bad_args when both query and tags are empty")
		}
		var toolErr *ToolError
		if !errorsAs(err, &toolErr) || toolErr.Code != "bad_args" {
			t.Errorf("expected ToolError code bad_args, got %v", err)
		}
	})

	t.Run("happy path returns markdown payload", func(t *testing.T) {
		exec := VaultToolExecutor{VaultRoot: vault, Skill: skill, Memory: svc}
		result, err := exec.Execute(context.Background(), ToolInvocation{
			Name: "recall_memory",
			Args: json.RawMessage(`{"tags":["topic/golang"]}`),
		})
		if err != nil {
			t.Fatalf("recall_memory: %v", err)
		}
		var payload struct {
			Query    string  `json:"query"`
			Hits     int     `json:"hits"`
			Markdown string  `json:"markdown"`
			Degraded bool    `json:"degraded"`
			TagMode  string  `json:"tag_mode"`
			Tags     []string `json:"tags"`
		}
		if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
			t.Fatalf("payload unmarshal: %v\n%s", err, result.Content)
		}
		if payload.Hits != 1 {
			t.Errorf("hits = %d, want 1 (Knowledge/a.md)", payload.Hits)
		}
		if !strings.Contains(payload.Markdown, "Knowledge/a.md") {
			t.Errorf("markdown missing hit path:\n%s", payload.Markdown)
		}
		if !strings.Contains(payload.Markdown, "tag_match") {
			t.Errorf("markdown missing source label tag_match:\n%s", payload.Markdown)
		}
	})

	t.Run("scope_dirs outside skill scope rejected", func(t *testing.T) {
		exec := VaultToolExecutor{VaultRoot: vault, Skill: skill, Memory: svc}
		_, err := exec.Execute(context.Background(), ToolInvocation{
			Name: "recall_memory",
			Args: json.RawMessage(`{"tags":["topic/golang"],"scope_dirs":["Archive/"]}`),
		})
		if err == nil {
			t.Fatal("expected scope_violation when scope_dirs falls outside Skill.VaultScope")
		}
		var toolErr *ToolError
		if !errorsAs(err, &toolErr) || toolErr.Code != "scope_violation" {
			t.Errorf("expected ToolError code scope_violation, got %v", err)
		}
	})
}

// errorsAs is a thin wrapper around errors.As that avoids importing errors
// just for this test file's single-target type assertion.
func errorsAs(err error, target **ToolError) bool {
	te, ok := err.(*ToolError)
	if !ok {
		return false
	}
	*target = te
	return true
}

