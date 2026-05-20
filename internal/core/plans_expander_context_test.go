package core

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scarletmu/openwhisker/internal/policy"
)

func TestParseExpanderSourceTracePaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "scalar raw_path and processed_path",
			body: "---\ntitle: foo\nopenwhisker:\n  raw_path: \"Raw/Inbox/a.md\"\n  processed_path: 'Raw/Processed/a.md'\n---\nbody\n",
			want: []string{"Raw/Inbox/a.md", "Raw/Processed/a.md"},
		},
		{
			name: "today-batch list fields, dedup self-equal",
			body: "---\ntitle: foo\nopenwhisker:\n  raw_paths:\n    - Raw/Inbox/a.md\n    - Raw/Inbox/b.md\n  processed_paths:\n    - Raw/Processed/a.md\n    - Raw/Processed/a.md\n---\n",
			want: []string{"Raw/Inbox/a.md", "Raw/Inbox/b.md", "Raw/Processed/a.md"},
		},
		{
			name: "stops at next top-level key",
			body: "---\nopenwhisker:\n  processed_path: Raw/Processed/a.md\ntags:\n  - type/knowledge-draft\n  - status/needs-review\n---\n",
			want: []string{"Raw/Processed/a.md"},
		},
		{
			name: "ignores related wikilinks (out of scope for source-trace)",
			body: "---\nrelated:\n  - \"[[Raw/Processed/a]]\"\ntitle: foo\n---\n",
			want: nil,
		},
		{
			name: "no frontmatter",
			body: "# heading only\n",
			want: nil,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := parseExpanderSourceTracePaths(tc.body)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseExpanderSourceTracePaths(%q)\n  got  = %#v\n  want = %#v", tc.name, got, tc.want)
			}
		})
	}
}

func TestBuildKnowledgeExpanderContextLoadsSourceTraceRelatedNotes(t *testing.T) {
	t.Parallel()
	vaultRoot := t.TempDir()
	knowledgePath := "Knowledge/topic/agent-memory.md"
	processedPath := "Raw/Processed/agent-memory.md"
	missingPath := "Raw/Processed/nonexistent.md"

	writeFile := func(rel, content string) {
		full := filepath.Join(vaultRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeFile(processedPath, "# Raw/Processed body\nfoo bar baz\n")
	writeFile(knowledgePath, ""+
		"---\n"+
		"title: agent memory\n"+
		"openwhisker:\n"+
		"  processed_path: "+processedPath+"\n"+
		"  processed_paths:\n"+
		"    - "+processedPath+"\n"+
		"    - "+missingPath+"\n"+
		"---\n"+
		"# Agent Memory\n",
	)

	ctxVal, err := buildKnowledgeExpanderContext(context.Background(), vaultRoot, knowledgePath, ContextModeMinimal, policy.DefaultConventions())
	if err != nil {
		t.Fatalf("buildKnowledgeExpanderContext error = %v", err)
	}
	if ctxVal.TargetPath != knowledgePath {
		t.Fatalf("TargetPath = %q, want %q", ctxVal.TargetPath, knowledgePath)
	}
	if len(ctxVal.RelatedNotes) != 1 {
		t.Fatalf("RelatedNotes len = %d, want 1 (missing path silently skipped, scalar+list deduped); got %#v", len(ctxVal.RelatedNotes), ctxVal.RelatedNotes)
	}
	if ctxVal.RelatedNotes[0].Path != processedPath {
		t.Fatalf("RelatedNotes[0].Path = %q, want %q", ctxVal.RelatedNotes[0].Path, processedPath)
	}
	if !strings.Contains(ctxVal.RelatedNotes[0].Content, "foo bar baz") {
		t.Fatalf("RelatedNotes[0].Content missing expected body; got %q", ctxVal.RelatedNotes[0].Content)
	}
}
