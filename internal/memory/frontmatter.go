package memory

import "strings"

// ExtractFrontmatterTags parses the YAML frontmatter and returns every
// value under the top-level `tags:` key. Supports both flow style
// `tags: [a, b]` and block style `tags:\n  - a\n  - b`. Unknown formats
// produce nothing rather than erroring — the walker tolerates frontmatter
// noise across the vault.
//
// Migrated from `internal/tagvocab/tagvocab.go` per Phase 8 decision #1.
func ExtractFrontmatterTags(content string) []string {
	content = strings.TrimLeft(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") {
		return nil
	}
	rest := content[len("---\n"):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	fm := rest[:end]
	lines := strings.Split(fm, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, "tags:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trim, "tags:"))
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			inner := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			for _, item := range strings.Split(inner, ",") {
				if v := cleanScalar(item); v != "" {
					out = append(out, v)
				}
			}
			continue
		}
		if value != "" {
			if v := cleanScalar(value); v != "" {
				out = append(out, v)
			}
			continue
		}
		// Block style — walk subsequent lines that start with "  - ".
		for j := i + 1; j < len(lines); j++ {
			next := lines[j]
			indent := len(next) - len(strings.TrimLeft(next, " \t"))
			trimmed := strings.TrimSpace(next)
			if indent == 0 && trimmed != "" {
				break
			}
			if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
				if trimmed == "" {
					continue
				}
				break
			}
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			if v := cleanScalar(item); v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

func cleanScalar(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return strings.TrimSpace(s)
}
