// Package markdown is the single source of truth for splitting and reading a
// note's YAML frontmatter. Before this package, every consumer (core, memory,
// vault/linkindex, policy, scheduler) re-implemented its own splitter, and no
// two agreed on the boundary rules — substring vs whole-line close, whether to
// normalize CRLF, whether to strip a BOM. See docs/architecture/frontmatter-parsing.md.
//
// The boundary rule here is the strict union of those parsers: callers migrate
// by tightening, never by loosening. Frontmatter is recognized only when it is
// a well-formed Obsidian Properties block — opening "---\n" at the very top and
// closing on the first line that is exactly "---".
package markdown

import "strings"

// SplitFrontmatter separates a note's YAML frontmatter from its body using the
// canonical boundary rule:
//
//  1. CRLF is normalized to LF.
//  2. A leading UTF-8 BOM is stripped.
//  3. The content must begin with "---\n" (after BOM/CRLF normalization).
//  4. The block closes on the first line that, trimmed, equals exactly "---".
//
// block is the frontmatter content without the enclosing fences; body is
// everything after the closing fence. found is false when there is no opening
// fence or no closing fence, in which case block is empty and body is the
// normalized content. The returned strings always use LF line endings.
func SplitFrontmatter(content string) (block, body string, found bool) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") {
		return "", content, false
	}
	lines := strings.Split(content, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return "", content, false
}

// Doc is a parsed frontmatter block. Construct it with Parse and read fields
// with the typed accessors below. A zero Doc behaves like empty frontmatter:
// every accessor returns the empty result.
type Doc struct {
	lines []string
}

// Parse splits a frontmatter block (the first return value of SplitFrontmatter,
// without fences) into lines for the accessors. It does no validation; callers
// that need strict validation layer it on top of SplitFrontmatter themselves.
func Parse(block string) Doc {
	if block == "" {
		return Doc{}
	}
	return Doc{lines: strings.Split(block, "\n")}
}

// Has reports whether the block contains a top-level-or-indented key. A key
// matches when some line, trimmed, begins with "key:".
func (d Doc) Has(key string) bool {
	prefix := key + ":"
	for _, line := range d.lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

// Scalar returns the unquoted value of the first line whose trimmed form begins
// with "key:". Returns "" when the key is absent or has no inline value (e.g. a
// block-list key). Surrounding single or double quotes are stripped.
func (d Doc) Scalar(key string) string {
	prefix := key + ":"
	for _, line := range d.lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return cleanScalar(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

// List returns every value under key, supporting both flow style
// (key: [a, b]) and block style (key:\n  - a\n  - b). An inline scalar value
// (key: a) is returned as a single-element list. Values are trimmed and
// unquoted; empty values are dropped. Returns nil when the key is absent.
func (d Doc) List(key string) []string {
	prefix := key + ":"
	var out []string
	for i := 0; i < len(d.lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(d.lines[i]), prefix) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(d.lines[i]), prefix))
		if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
			inner := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
			for _, item := range strings.Split(inner, ",") {
				if v := cleanScalar(item); v != "" {
					out = append(out, v)
				}
			}
			return out
		}
		if value != "" {
			if v := cleanScalar(value); v != "" {
				out = append(out, v)
			}
			return out
		}
		// Block style — walk subsequent indented "- item" lines until a
		// non-indented non-empty line ends the block.
		for j := i + 1; j < len(d.lines); j++ {
			next := d.lines[j]
			indent := len(next) - len(strings.TrimLeft(next, " \t"))
			trimmed := strings.TrimSpace(next)
			if trimmed == "" {
				continue
			}
			if indent == 0 {
				break
			}
			if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
				break
			}
			if v := cleanScalar(strings.TrimPrefix(trimmed, "-")); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return nil
}

// HasListValue reports whether value appears in the list under key. It is the
// key-scoped form of the membership checks scattered across consumers.
func (d Doc) HasListValue(key, value string) bool {
	for _, v := range d.List(key) {
		if v == value {
			return true
		}
	}
	return false
}

// cleanScalar trims whitespace and a single surrounding quote pair.
func cleanScalar(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	return strings.TrimSpace(s)
}
