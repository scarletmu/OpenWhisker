package memory

import "github.com/scarletmu/openwhisker/internal/markdown"

// ExtractFrontmatterTags parses the YAML frontmatter and returns every value
// under the top-level `tags:` key. Supports both flow style `tags: [a, b]` and
// block style `tags:\n  - a\n  - b`. Unknown formats produce nothing rather
// than erroring — the walker tolerates frontmatter noise across the vault.
//
// Migrated from `internal/tagvocab/tagvocab.go` per Phase 8 decision #1; the
// frontmatter boundary and list parsing now come from internal/markdown, the
// single source of truth (see docs/architecture/frontmatter-parsing.md).
func ExtractFrontmatterTags(content string) []string {
	block, _, found := markdown.SplitFrontmatter(content)
	if !found {
		return nil
	}
	return markdown.Parse(block).List("tags")
}
