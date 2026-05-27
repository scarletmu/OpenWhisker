package memory

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/scarletmu/openwhisker/internal/vault/linkindex"
)

// The LinkIndexAdapter must restrict diffusion edges to
// SourceKindFrontmatterRelated. A body wikilink to the same neighbour must
// NOT surface through OutlinksRelated / BacklinksRelated, otherwise Recall
// Pass 3 would treat "casually mentioned in body" the same as "explicitly
// curated relationship" (spec acceptance row #3).
func TestLinkIndexAdapterFiltersBodyWikilinks(t *testing.T) {
	vault := t.TempDir()

	// seed.md has both a frontmatter related neighbour AND a body wikilink
	// to a different note. Only the related target should appear in
	// OutlinksRelated; the body target must be excluded.
	writeAdapterFile(t, vault, "Knowledge/seed.md",
		"---\nrelated:\n  - \"[[Knowledge/related-target]]\"\n---\n"+
			"See also [[Knowledge/body-target]] in passing.\n")
	writeAdapterFile(t, vault, "Knowledge/related-target.md", "# related target\n")
	writeAdapterFile(t, vault, "Knowledge/body-target.md", "# body target\n")

	// backlink-source.md is the inverse case: its frontmatter related points
	// at seed.md, so BacklinksRelated("Knowledge/seed.md") should surface it.
	// body-link-source.md only mentions seed.md in body — it must NOT.
	writeAdapterFile(t, vault, "Knowledge/backlink-source.md",
		"---\nrelated:\n  - \"[[Knowledge/seed]]\"\n---\nbody\n")
	writeAdapterFile(t, vault, "Knowledge/body-link-source.md",
		"# body-link-source\n\nMentions [[Knowledge/seed]] in body only.\n")

	ix := linkindex.New(vault, []string{"Knowledge"})
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("linkindex build: %v", err)
	}
	if !ix.Ready() {
		t.Fatal("linkindex not ready after sync Build")
	}

	adapter := LinkIndexAdapter{Index: ix}

	outs := adapter.OutlinksRelated("Knowledge/seed.md")
	sort.Strings(outs)
	if len(outs) != 1 || outs[0] != "Knowledge/related-target.md" {
		t.Errorf("OutlinksRelated should expose only the frontmatter related neighbour, got %v", outs)
	}

	backs := adapter.BacklinksRelated("Knowledge/seed.md")
	sort.Strings(backs)
	if len(backs) != 1 || backs[0] != "Knowledge/backlink-source.md" {
		t.Errorf("BacklinksRelated should expose only frontmatter-related backlink sources, got %v", backs)
	}
}

func writeAdapterFile(t *testing.T, root, rel, body string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
