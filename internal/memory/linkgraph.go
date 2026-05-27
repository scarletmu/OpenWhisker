package memory

import "github.com/scarletmu/openwhisker/internal/vault/linkindex"

// LinkGraph is the read-only adjacency interface Recall Pass 3 (one-hop
// related diffusion) needs. Implementations must return *frontmatter
// `related:`* edges only — body wikilinks are intentionally not part of the
// "strong" relationship signal Phase 8 callers expect.
//
// Returned slices contain vault-relative forward-slash paths and are not
// mutated by callers. Callers tolerate nil returns: a degraded link graph
// simply means Pass 3 contributes no neighbours.
type LinkGraph interface {
	Ready() bool
	OutlinksRelated(notePath string) []string
	BacklinksRelated(notePath string) []string
}

// LinkIndexAdapter wraps the Phase 6 `internal/vault/linkindex.Index` and
// exposes it as a LinkGraph by filtering on
// linkindex.SourceKindFrontmatterRelated. The daemon constructs one of
// these once and passes it into memory.NewService.
type LinkIndexAdapter struct {
	Index *linkindex.Index
}

func (a LinkIndexAdapter) Ready() bool { return a.Index != nil && a.Index.Ready() }

func (a LinkIndexAdapter) OutlinksRelated(notePath string) []string {
	if !a.Ready() {
		return nil
	}
	links, err := a.Index.Outlinks(notePath)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		if l.SourceKind == linkindex.SourceKindFrontmatterRelated && l.Resolved && l.TargetPath != "" {
			out = append(out, l.TargetPath)
		}
	}
	return out
}

// BacklinksRelated re-derives the frontmatter-related subset of backlinks
// by walking each backlink source's outlinks once and keeping the ones
// whose SourceKind is FrontmatterRelated. Cheap enough for the typical
// diffusion seed count (single-digit) per Recall call; if this ever shows
// up in profiles, the right fix is a dedicated frontmatter-backlink map in
// linkindex, not a cache here.
func (a LinkIndexAdapter) BacklinksRelated(notePath string) []string {
	if !a.Ready() {
		return nil
	}
	sources, err := a.Index.Backlinks(notePath)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(sources))
	for _, src := range sources {
		srcLinks, err := a.Index.Outlinks(src)
		if err != nil {
			continue
		}
		for _, l := range srcLinks {
			if l.SourceKind == linkindex.SourceKindFrontmatterRelated && l.Resolved && l.TargetPath == notePath {
				out = append(out, src)
				break
			}
		}
	}
	return out
}
