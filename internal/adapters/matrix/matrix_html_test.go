package matrix

import (
	"strings"
	"testing"
)

func TestRenderInline(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Inbox 有 2 条新笔记", "Inbox 有 2 条新笔记"},
		{"bold", "**Raw/Inbox** 新增 2 条", "<strong>Raw/Inbox</strong> 新增 2 条"},
		{"bold whole line", "**定时检查结果（2026-05-29）**", "<strong>定时检查结果（2026-05-29）</strong>"},
		{"code span", "用 `read_vault_note` 读取", "用 <code>read_vault_note</code> 读取"},
		{"wikilink plain", "见 [[Raw/Inbox/Gemini-Rust]]", "见 Raw/Inbox/Gemini-Rust"},
		{"wikilink alias", "见 [[Raw/Inbox/Note|Gemini 与 Rust]]", "见 Gemini 与 Rust"},
		{"wikilink heading", "见 [[Note#章节]]", "见 Note"},
		{"code wrapping wikilink", "`[[Raw/Inbox/Gemini-Rust]]`", "<code>[[Raw/Inbox/Gemini-Rust]]</code>"},
		{"escapes html", "a < b & c", "a &lt; b &amp; c"},
		{"unterminated bold is literal", "**oops", "**oops"},
		{"unterminated code is literal", "`oops", "`oops"},
		{"unterminated wikilink is literal", "[[oops", "[[oops"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderInline(tc.in); got != tc.want {
				t.Fatalf("renderInline(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMatrixHTMLBriefing(t *testing.T) {
	body := strings.Join([]string{
		"📬 Vault 快报 — Inbox 有 2 条新笔记",
		"**定时检查结果（2026-05-29）**",
		"",
		"- **Raw/Inbox** 新增 2 条笔记：",
		"- `[[Raw/Inbox/Gemini-Rust]]` — 关于 Gemini 与 Rust",
	}, "\n")
	got := matrixHTML(body)
	for _, want := range []string{
		"<strong>定时检查结果（2026-05-29）</strong>",
		"<li><strong>Raw/Inbox</strong> 新增 2 条笔记：</li>",
		"<code>[[Raw/Inbox/Gemini-Rust]]</code>",
		"<ul>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("matrixHTML output missing %q\ngot: %s", want, got)
		}
	}
	if strings.Contains(got, "**") {
		t.Fatalf("matrixHTML left literal ** in output: %s", got)
	}
}

func TestNewTextMessageSetsFormattedBody(t *testing.T) {
	content := newTextMessage("**bold** line")
	if content.Format != "org.matrix.custom.html" {
		t.Fatalf("expected html format, got %q", content.Format)
	}
	if content.FormattedBody != "<strong>bold</strong> line" {
		t.Fatalf("unexpected formatted body: %q", content.FormattedBody)
	}
	// Plain text with no markup must not set a redundant formatted body.
	plain := newTextMessage("just text")
	if plain.Format != "" || plain.FormattedBody != "" {
		t.Fatalf("plain text should not set formatted body, got format=%q body=%q", plain.Format, plain.FormattedBody)
	}
}
