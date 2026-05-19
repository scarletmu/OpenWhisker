package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/scarletmu/openwhisker/internal/core"
	"github.com/scarletmu/openwhisker/internal/model"
	"github.com/scarletmu/openwhisker/internal/policy"
)

func TestOpenAIKnowledgeExpanderBuildsAppendPlan(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"kind": "append",
		"title": "补充 Phase4 笔记",
		"summary": "在已有 Knowledge note 末尾追加 raw 中的新结论。",
		"append_section": "## 新增结论\n\n- agent 记忆来自 vault。",
		"child_note": null,
		"restructure": null,
		"review_items": ["核对新增结论来源。"]
	}`}
	req := core.KnowledgeExpanderRequest{
		Job:         model.WikiJob{ID: "job_exp", Type: model.JobTypeExpandKnowledge},
		TargetPath:  "Knowledge/topic/agent-memory.md",
		Conventions: policy.DefaultConventions(),
		VaultContext: core.KnowledgeExpanderContext{
			TargetPath:    "Knowledge/topic/agent-memory.md",
			TargetContent: "# Agent Memory\n\nthin note",
		},
		Now: time.Date(2026, 5, 19, 1, 2, 3, 0, time.UTC),
	}

	plan, err := (OpenAIKnowledgeExpander{Client: client}).ExpandKnowledge(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RiskLevel != model.RiskMedium || !plan.RequiresApproval {
		t.Fatalf("risk/approval = %q/%v", plan.RiskLevel, plan.RequiresApproval)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Type != model.OperationAppendNote {
		t.Fatalf("operations = %+v, want one append_note", plan.Operations)
	}
	if plan.Operations[0].TargetPath != req.TargetPath {
		t.Fatalf("append target = %q", plan.Operations[0].TargetPath)
	}
	if !strings.Contains(plan.Operations[0].PayloadJSON, "新增结论") {
		t.Fatalf("payload = %q, want append section", plan.Operations[0].PayloadJSON)
	}
	if client.request.Text.Format.Type != "json_object" {
		t.Fatalf("response_format = %+v, want json_object", client.request.Text.Format)
	}
}

func TestOpenAIKnowledgeExpanderBuildsCreateChildPlan(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"kind": "create_child_note",
		"title": "子主题：raw bucket",
		"summary": "为目标 note 创建一篇子主题草稿。",
		"append_section": "",
		"child_note": {
			"relative_path": "agent-memory-raw-bucket.md",
			"title": "raw bucket 设计",
			"draft_body": "## 概念\n\nraw bucket 用于短期记忆。"
		},
		"restructure": null,
		"review_items": []
	}`}
	req := core.KnowledgeExpanderRequest{
		Job:         model.WikiJob{ID: "job_exp", Type: model.JobTypeExpandKnowledge},
		TargetPath:  "Knowledge/topic/agent-memory.md",
		Conventions: policy.DefaultConventions(),
		VaultContext: core.KnowledgeExpanderContext{
			TargetPath:    "Knowledge/topic/agent-memory.md",
			TargetContent: "# Agent Memory",
		},
		Now: time.Date(2026, 5, 19, 1, 2, 3, 0, time.UTC),
	}

	plan, err := (OpenAIKnowledgeExpander{Client: client}).ExpandKnowledge(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RiskLevel != model.RiskMedium {
		t.Fatalf("risk = %q", plan.RiskLevel)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Type != model.OperationCreateNote {
		t.Fatalf("operations = %+v, want one create_note", plan.Operations)
	}
	wantPath := "Knowledge/Drafts/agent-memory-raw-bucket.md"
	if plan.Operations[0].TargetPath != wantPath {
		t.Fatalf("child target = %q, want %q", plan.Operations[0].TargetPath, wantPath)
	}
	if plan.TargetPaths[0] != wantPath {
		t.Fatalf("plan.TargetPaths[0] = %q, want %q", plan.TargetPaths[0], wantPath)
	}
	if !strings.Contains(plan.Operations[0].PayloadJSON, "raw bucket 用于短期记忆") {
		t.Fatalf("payload missing child body: %q", plan.Operations[0].PayloadJSON)
	}
	if !strings.Contains(plan.Operations[0].PayloadJSON, "source_knowledge_path: Knowledge/topic/agent-memory.md") {
		t.Fatalf("payload missing source_knowledge_path traceability: %q", plan.Operations[0].PayloadJSON)
	}
	if !strings.Contains(plan.Operations[0].PayloadJSON, "needs_review: true") {
		t.Fatalf("payload missing needs_review marker: %q", plan.Operations[0].PayloadJSON)
	}
}

func TestOpenAIKnowledgeExpanderBuildsHighRiskRestructurePlan(t *testing.T) {
	client := &fakeCompatibleClient{output: `{
		"kind": "propose_restructure",
		"title": "拆分 agent-memory",
		"summary": "目标 note 已经混杂两个独立主题，建议拆分。",
		"append_section": "",
		"child_note": null,
		"restructure": {
			"proposal_kind": "split",
			"rationale": "短期与长期记忆是两个独立主题。",
			"affected_paths": [
				"Knowledge/topic/agent-memory.md",
				"Knowledge/topic/agent-memory/short-term.md",
				"Knowledge/topic/agent-memory/long-term.md"
			]
		},
		"review_items": ["确认拆分后的子主题命名。"]
	}`}
	req := core.KnowledgeExpanderRequest{
		Job:         model.WikiJob{ID: "job_exp", Type: model.JobTypeExpandKnowledge},
		TargetPath:  "Knowledge/topic/agent-memory.md",
		Conventions: policy.DefaultConventions(),
		VaultContext: core.KnowledgeExpanderContext{
			TargetPath:    "Knowledge/topic/agent-memory.md",
			TargetContent: "# Agent Memory\n\nshort-term + long-term mixed",
		},
		Now: time.Date(2026, 5, 19, 1, 2, 3, 0, time.UTC),
	}

	plan, err := (OpenAIKnowledgeExpander{Client: client}).ExpandKnowledge(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.RiskLevel != model.RiskHigh {
		t.Fatalf("risk = %q, want high", plan.RiskLevel)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Type != model.OperationRenameNote {
		t.Fatalf("operations = %+v, want one rename_note for split", plan.Operations)
	}
	if plan.Operations[0].RiskLevel != model.RiskHigh {
		t.Fatalf("op risk = %q, want high", plan.Operations[0].RiskLevel)
	}
	if !strings.Contains(plan.Operations[0].PayloadJSON, "agent-memory/short-term.md") {
		t.Fatalf("payload missing first affected path destination: %q", plan.Operations[0].PayloadJSON)
	}
}

func TestOpenAIKnowledgeExpanderRejectsKindOnlyAndPayloadOnlyOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{
			name: "unknown kind",
			output: `{
				"kind": "weird",
				"title": "x",
				"summary": "x",
				"append_section": "",
				"child_note": null,
				"restructure": null,
				"review_items": []
			}`,
			want: `kind "weird" is not allowed`,
		},
		{
			name: "append without append_section",
			output: `{
				"kind": "append",
				"title": "x",
				"summary": "x",
				"append_section": "",
				"child_note": null,
				"restructure": null,
				"review_items": []
			}`,
			want: "append_section is required",
		},
		{
			name: "create_child_note without child_note",
			output: `{
				"kind": "create_child_note",
				"title": "x",
				"summary": "x",
				"append_section": "",
				"child_note": null,
				"restructure": null,
				"review_items": []
			}`,
			want: "child_note is required",
		},
		{
			name: "create_child_note relative_path escapes draft dir",
			output: `{
				"kind": "create_child_note",
				"title": "x",
				"summary": "x",
				"append_section": "",
				"child_note": {"relative_path": "../escape.md", "title": "x", "draft_body": "x"},
				"restructure": null,
				"review_items": []
			}`,
			want: "must stay inside the draft directory",
		},
		{
			name: "propose_restructure missing proposal_kind",
			output: `{
				"kind": "propose_restructure",
				"title": "x",
				"summary": "x",
				"append_section": "",
				"child_note": null,
				"restructure": {"proposal_kind": "", "rationale": "x", "affected_paths": ["Knowledge/x.md"]},
				"review_items": []
			}`,
			want: "proposal_kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeCompatibleClient{output: tc.output}
			_, err := (OpenAIKnowledgeExpander{Client: client}).ExpandKnowledge(context.Background(), core.KnowledgeExpanderRequest{Conventions: policy.DefaultConventions()})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestOpenAIKnowledgeExpanderRetriesOnceOnEmptyContent(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{
		"",
		`{
			"kind": "append",
			"title": "ok",
			"summary": "ok",
			"append_section": "## ok\n\nok",
			"child_note": null,
			"restructure": null,
			"review_items": []
		}`,
	}}
	req := core.KnowledgeExpanderRequest{
		Job:         model.WikiJob{ID: "job_exp", Type: model.JobTypeExpandKnowledge},
		TargetPath:  "Knowledge/topic/agent-memory.md",
		Conventions: policy.DefaultConventions(),
		Now:         time.Date(2026, 5, 19, 1, 2, 3, 0, time.UTC),
	}
	if _, err := (OpenAIKnowledgeExpander{Client: client}).ExpandKnowledge(context.Background(), req); err != nil {
		t.Fatalf("ExpandKnowledge err = %v, want success after retry", err)
	}
	if client.calls != 2 {
		t.Fatalf("client.calls = %d, want exactly 2", client.calls)
	}
}

func TestOpenAIKnowledgeExpanderFailsAfterTwoEmptyContents(t *testing.T) {
	client := &sequenceCompatibleClient{outputs: []string{"", "   "}}
	_, err := (OpenAIKnowledgeExpander{Client: client}).ExpandKnowledge(context.Background(), core.KnowledgeExpanderRequest{Conventions: policy.DefaultConventions()})
	if err == nil || !strings.Contains(err.Error(), "empty content after one retry") {
		t.Fatalf("err = %v, want empty-content-after-retry error", err)
	}
}
