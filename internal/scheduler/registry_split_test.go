package scheduler

import (
	"strings"
	"testing"
)

// TestSplitFrontmatterStrictWrapper covers the scheduler's strict wrapper over
// the shared internal/markdown boundary rule: a valid block splits cleanly,
// while the two missing-block cases keep their distinct authoring diagnostics.
func TestSplitFrontmatterStrictWrapper(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		block, body, err := splitFrontmatter("---\nid: daily\ncron_expr: \"0 9 * * *\"\n---\nrun body\n")
		if err != nil {
			t.Fatalf("splitFrontmatter() error = %v, want nil", err)
		}
		if block != "id: daily\ncron_expr: \"0 9 * * *\"" {
			t.Fatalf("block = %q", block)
		}
		if body != "run body\n" {
			t.Fatalf("body = %q", body)
		}
	})

	t.Run("missing opening fence", func(t *testing.T) {
		_, _, err := splitFrontmatter("id: daily\nno frontmatter at all")
		if err == nil || !strings.Contains(err.Error(), "frontmatter is required") {
			t.Fatalf("error = %v, want \"frontmatter is required\"", err)
		}
	})

	t.Run("missing closing fence", func(t *testing.T) {
		_, _, err := splitFrontmatter("---\nid: daily\nnever closes")
		if err == nil || !strings.Contains(err.Error(), "closing marker is required") {
			t.Fatalf("error = %v, want \"frontmatter closing marker is required\"", err)
		}
	})

	t.Run("four-dash line is not a close", func(t *testing.T) {
		// Tightened boundary: a "----" line must not be mistaken for the close.
		_, _, err := splitFrontmatter("---\nid: daily\n----\nbody")
		if err == nil || !strings.Contains(err.Error(), "closing marker is required") {
			t.Fatalf("error = %v, want closing-marker error", err)
		}
	})
}
