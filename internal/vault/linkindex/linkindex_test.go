package linkindex

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestBuild_WikilinkAndRelated(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Knowledge/a.md", `# A

See [[b]] and [[Knowledge/c|alias text]].
`)
	writeFile(t, root, "Knowledge/b.md", `---
related:
  - "[[a]]"
  - c
---
# B
`)
	writeFile(t, root, "Knowledge/c.md", `# C`)

	ix := New(root, nil)
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !ix.Ready() {
		t.Fatal("index not ready after Build")
	}

	out, err := ix.Outlinks("Knowledge/a.md")
	if err != nil {
		t.Fatalf("Outlinks a: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("outlinks(a) = %d items, want 2: %+v", len(out), out)
	}
	gotResolved := map[string]bool{}
	for _, l := range out {
		if !l.Resolved {
			t.Errorf("outlink %+v unresolved", l)
		}
		gotResolved[l.TargetPath] = true
	}
	if !gotResolved["Knowledge/b.md"] || !gotResolved["Knowledge/c.md"] {
		t.Errorf("outlinks did not resolve b and c: %v", gotResolved)
	}

	back, err := ix.Backlinks("Knowledge/a.md")
	if err != nil {
		t.Fatalf("Backlinks a: %v", err)
	}
	if len(back) != 1 || back[0] != "Knowledge/b.md" {
		t.Errorf("backlinks(a) = %v, want [Knowledge/b.md]", back)
	}

	// b's frontmatter related: a, c. Body has no wikilinks. So outlinks(b)
	// should have 2 entries, both frontmatter_related.
	bout, _ := ix.Outlinks("Knowledge/b.md")
	if len(bout) != 2 {
		t.Errorf("outlinks(b) = %v, want 2 frontmatter related", bout)
	}
	for _, l := range bout {
		if l.SourceKind != SourceKindFrontmatterRelated {
			t.Errorf("outlink(b) source kind = %s, want frontmatter_related", l.SourceKind)
		}
	}
}

func TestBuild_SkipsForbiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Knowledge/keep.md", `# Keep
[[ignored]]
`)
	writeFile(t, root, ".obsidian/plugin/data.md", `# Plugin internal`)
	writeFile(t, root, ".git/notes/internal.md", `# Git internal`)
	writeFile(t, root, ".trash/old.md", `# Old`)

	ix := New(root, nil)
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	stats := ix.Stats()
	if stats.Notes != 1 {
		t.Errorf("notes = %d, want 1 (forbidden dirs should be skipped): stats=%+v", stats.Notes, stats)
	}

	// .obsidian/plugin/data.md should not be in the outlinks map.
	if _, err := ix.Outlinks(".obsidian/plugin/data.md"); err != nil {
		t.Errorf("Outlinks on forbidden file returned err %v; want nil empty", err)
	}
}

func TestBuild_ReadRootsFilter(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Knowledge/a.md", "# A")
	writeFile(t, root, "Private/secret.md", "# Secret")

	ix := New(root, []string{"Knowledge/"})
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	stats := ix.Stats()
	if stats.Notes != 1 {
		t.Errorf("notes = %d, want 1 (read roots should exclude Private/): %+v", stats.Notes, stats)
	}
}

func TestNotReadyBeforeBuild(t *testing.T) {
	root := t.TempDir()
	ix := New(root, nil)
	if _, err := ix.Outlinks("any.md"); err != ErrNotReady {
		t.Errorf("Outlinks before build = %v, want ErrNotReady", err)
	}
	if _, err := ix.Backlinks("any.md"); err != ErrNotReady {
		t.Errorf("Backlinks before build = %v, want ErrNotReady", err)
	}
}

func TestResolveTarget_AmbiguousLeftUnresolved(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Knowledge/foo.md", "")
	writeFile(t, root, "Interview/foo.md", "")
	writeFile(t, root, "Raw/uses.md", "See [[foo]].")

	ix := New(root, nil)
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, _ := ix.Outlinks("Raw/uses.md")
	if len(out) != 1 {
		t.Fatalf("outlinks(uses) = %v", out)
	}
	if out[0].Resolved {
		t.Errorf("ambiguous wikilink [[foo]] should be unresolved, got %+v", out[0])
	}
}

func TestGenerationIncrements(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "[[b]]")
	ix := New(root, nil)
	if g := ix.Generation(); g != 0 {
		t.Errorf("initial generation = %d, want 0", g)
	}
	_ = ix.Build(context.Background())
	if g := ix.Generation(); g == 0 {
		t.Errorf("post-build generation should bump above 0, got %d", g)
	}
}

func TestSortStableOutput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "[[c]]\n[[b]]")
	writeFile(t, root, "b.md", "")
	writeFile(t, root, "c.md", "")
	ix := New(root, nil)
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	out, _ := ix.Outlinks("a.md")
	gotPaths := make([]string, 0, len(out))
	for _, l := range out {
		gotPaths = append(gotPaths, l.TargetPath)
	}
	sort.Strings(gotPaths)
	want := []string{"b.md", "c.md"}
	if len(gotPaths) != 2 || gotPaths[0] != want[0] || gotPaths[1] != want[1] {
		t.Errorf("outlinks(a) sorted = %v, want %v", gotPaths, want)
	}
}
