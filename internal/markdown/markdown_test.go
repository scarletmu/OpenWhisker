package markdown

import (
	"reflect"
	"testing"
)

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantBlock string
		wantBody  string
		wantFound bool
	}{
		{
			name:      "standard block",
			content:   "---\ntitle: A\n---\nbody\n",
			wantBlock: "title: A",
			wantBody:  "body\n",
			wantFound: true,
		},
		{
			name:      "no frontmatter",
			content:   "# heading\nbody",
			wantBlock: "",
			wantBody:  "# heading\nbody",
			wantFound: false,
		},
		{
			name:      "missing closing fence",
			content:   "---\ntitle: A\nbody without close",
			wantBlock: "",
			wantBody:  "---\ntitle: A\nbody without close",
			wantFound: false,
		},
		{
			// The central divergence: a substring "\n---" matcher would close
			// here on "----", but the whole-line rule does not — "----" trimmed
			// is not "---", so this has no valid close.
			name:      "four-dash line is not a close",
			content:   "---\ntitle: A\n----\nbody",
			wantBlock: "",
			wantBody:  "---\ntitle: A\n----\nbody",
			wantFound: false,
		},
		{
			// Likewise "--- x" must not close the block.
			name:      "trailing-text fence is not a close",
			content:   "---\ntitle: A\n--- x\n---\nbody",
			wantBlock: "title: A\n--- x",
			wantBody:  "body",
			wantFound: true,
		},
		{
			name:      "CRLF normalized",
			content:   "---\r\ntitle: A\r\n---\r\nbody\r\n",
			wantBlock: "title: A",
			wantBody:  "body\n",
			wantFound: true,
		},
		{
			name:      "leading BOM stripped",
			content:   "\ufeff---\ntitle: A\n---\nbody",
			wantBlock: "title: A",
			wantBody:  "body",
			wantFound: true,
		},
		{
			name:      "whitespace-padded close still closes",
			content:   "---\ntitle: A\n  ---  \nbody",
			wantBlock: "title: A",
			wantBody:  "body",
			wantFound: true,
		},
		{
			name:      "empty frontmatter block",
			content:   "---\n---\nbody",
			wantBlock: "",
			wantBody:  "body",
			wantFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block, body, found := SplitFrontmatter(tt.content)
			if block != tt.wantBlock || body != tt.wantBody || found != tt.wantFound {
				t.Errorf("SplitFrontmatter(%q)\n got  block=%q body=%q found=%v\n want block=%q body=%q found=%v",
					tt.content, block, body, found, tt.wantBlock, tt.wantBody, tt.wantFound)
			}
		})
	}
}

func TestDocScalar(t *testing.T) {
	d := Parse(`title: "Quoted Title"
status: status/draft
related:
  - "[[Other Note]]"
empty:`)
	if got := d.Scalar("title"); got != "Quoted Title" {
		t.Errorf("Scalar(title) = %q, want %q", got, "Quoted Title")
	}
	if got := d.Scalar("status"); got != "status/draft" {
		t.Errorf("Scalar(status) = %q, want %q", got, "status/draft")
	}
	if got := d.Scalar("related"); got != "" {
		t.Errorf("Scalar(related) on block key = %q, want empty", got)
	}
	if got := d.Scalar("empty"); got != "" {
		t.Errorf("Scalar(empty) = %q, want empty", got)
	}
	if got := d.Scalar("absent"); got != "" {
		t.Errorf("Scalar(absent) = %q, want empty", got)
	}
}

func TestDocHas(t *testing.T) {
	d := Parse("title: A\ntags:\n  - x")
	if !d.Has("title") {
		t.Error("Has(title) = false, want true")
	}
	if !d.Has("tags") {
		t.Error("Has(tags) = false, want true")
	}
	if d.Has("missing") {
		t.Error("Has(missing) = true, want false")
	}
	// A prefix that is not a full key segment must not match.
	if d.Has("tag") {
		t.Error("Has(tag) matched the line \"tags:\", want false")
	}
}

func TestDocList(t *testing.T) {
	tests := []struct {
		name  string
		block string
		key   string
		want  []string
	}{
		{
			name:  "block style",
			block: "tags:\n  - type/note\n  - status/draft",
			key:   "tags",
			want:  []string{"type/note", "status/draft"},
		},
		{
			name:  "flow style",
			block: "tags: [type/note, status/draft]",
			key:   "tags",
			want:  []string{"type/note", "status/draft"},
		},
		{
			name:  "inline scalar as single element",
			block: "tags: type/note",
			key:   "tags",
			want:  []string{"type/note"},
		},
		{
			name:  "quoted block items unquoted",
			block: "related:\n  - \"[[A]]\"\n  - '[[B]]'",
			key:   "related",
			want:  []string{"[[A]]", "[[B]]"},
		},
		{
			name:  "block ends at next top-level key",
			block: "tags:\n  - x\nstatus: draft",
			key:   "tags",
			want:  []string{"x"},
		},
		{
			name:  "blank line inside block is skipped",
			block: "tags:\n  - x\n\n  - y\nstatus: draft",
			key:   "tags",
			want:  []string{"x", "y"},
		},
		{
			name:  "absent key returns nil",
			block: "title: A",
			key:   "tags",
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.block).List(tt.key); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("List(%q) = %#v, want %#v", tt.key, got, tt.want)
			}
		})
	}
}

func TestDocHasListValue(t *testing.T) {
	d := Parse("tags:\n  - type/note\n  - status/draft")
	if !d.HasListValue("tags", "status/draft") {
		t.Error("HasListValue(tags, status/draft) = false, want true")
	}
	if d.HasListValue("tags", "status/published") {
		t.Error("HasListValue(tags, status/published) = true, want false")
	}
	// Membership is key-scoped: a value present under another key does not match.
	if d.HasListValue("aliases", "type/note") {
		t.Error("HasListValue(aliases, type/note) = true, want false")
	}
}

func TestZeroDoc(t *testing.T) {
	var d Doc
	if d.Has("anything") || d.Scalar("anything") != "" || d.List("anything") != nil || d.HasListValue("a", "b") {
		t.Error("zero Doc should behave as empty frontmatter")
	}
}
