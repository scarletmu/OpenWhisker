package enrich

import (
	"fmt"
	"strings"
)

// applyEnrichToFrontmatter is the pure rendering primitive. Given the
// original on-disk content of a Raw note, an EnrichResult, and the run
// metadata to stamp, it returns the new content with the seven writable
// fields added or merged.
//
// Contract:
//   - Body is returned byte-identical (the policy body_guard depends on it).
//   - Existing top-level frontmatter keys that aren't `tags` or `related`
//     are returned byte-identical (the policy field_guard depends on it).
//   - `tags` / `related` are appended-to in place when they already exist,
//     and added as fresh block-style fields at the bottom when absent.
//   - The five `openwhisker_enrich_*` keys are added at the bottom; if any
//     pre-existed they are replaced in place to keep the frontmatter from
//     accumulating duplicates across retries.
func applyEnrichToFrontmatter(beforeContent string, addTags, addRelated []string, privateFields map[string]string) (string, error) {
	fmStart, fmEnd, ok := findFrontmatterBounds(beforeContent)
	if !ok {
		return "", fmt.Errorf("enrich render: input has no frontmatter")
	}
	frontmatter := beforeContent[fmStart:fmEnd]
	prefix := beforeContent[:fmStart]
	suffix := beforeContent[fmEnd:]

	updated, err := mergeFrontmatter(frontmatter, addTags, addRelated, privateFields)
	if err != nil {
		return "", err
	}
	return prefix + updated + suffix, nil
}

// findFrontmatterBounds returns the byte offsets [start, end) of the
// frontmatter block content (excluding the `---` fences). Requires the
// file to start with `---\n` and contain a matching closer ending with a
// newline so the suffix (body) keeps its leading separator intact.
func findFrontmatterBounds(content string) (int, int, bool) {
	const opener = "---\n"
	if !strings.HasPrefix(content, opener) {
		return 0, 0, false
	}
	rest := content[len(opener):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return 0, 0, false
	}
	return len(opener), len(opener) + end + 1, true // include trailing newline of last key
}

// mergeFrontmatter rewrites the (newline-terminated) frontmatter body. The
// strategy is intentionally conservative:
//   - Walk the frontmatter line by line, copying each top-level key+block
//     verbatim into `out`. When we encounter `tags` or `related`, switch
//     to a small merger that produces a single canonical block-style list
//     containing the original entries plus our additions, then skip the
//     original block's lines.
//   - Private fields are overwritten in place if they already exist; the
//     remaining ones are appended after the last original key.
func mergeFrontmatter(frontmatter string, addTags, addRelated []string, privateFields map[string]string) (string, error) {
	lines := strings.Split(frontmatter, "\n")
	// Stable order for private fields so two renders of the same input are
	// byte-identical (helps testing).
	privateKeyOrder := []string{
		"openwhisker_route_suggestion",
		"openwhisker_new_tag_candidates",
		"openwhisker_enriched_at",
		"openwhisker_enrich_run_id",
		"openwhisker_enrich_attempts",
	}

	var out strings.Builder
	written := map[string]bool{}
	i := 0
	for i < len(lines) {
		line := lines[i]
		if !isTopKey(line) {
			out.WriteString(line)
			if i+1 < len(lines) {
				out.WriteByte('\n')
			}
			i++
			continue
		}
		key, _ := splitKey(line)
		blockEnd := i + 1
		for blockEnd < len(lines) && !isTopKey(lines[blockEnd]) && lines[blockEnd] != "" {
			blockEnd++
		}
		// Include trailing blank lines inside the block (rare in OpenWhisker
		// rendered Raw notes; future-proof against vault-edited frontmatter).
		switch key {
		case "tags":
			existing := readListBlock(lines, i)
			merged := mergeUnique(existing, addTags)
			emitListBlock(&out, "tags", merged)
			written["tags"] = true
		case "related":
			existing := readListBlock(lines, i)
			merged := mergeUnique(existing, addRelated)
			emitListBlock(&out, "related", merged)
			written["related"] = true
		default:
			if val, isPrivate := privateFields[key]; isPrivate {
				emitScalar(&out, key, val)
				written[key] = true
			} else {
				// Verbatim copy of the original block + its trailing newline.
				for j := i; j < blockEnd; j++ {
					out.WriteString(lines[j])
					out.WriteByte('\n')
				}
			}
		}
		i = blockEnd
	}
	// Append any writable fields we didn't already replace in place.
	if !written["tags"] && len(addTags) > 0 {
		emitListBlock(&out, "tags", addTags)
	}
	if !written["related"] && len(addRelated) > 0 {
		emitListBlock(&out, "related", addRelated)
	}
	for _, k := range privateKeyOrder {
		if written[k] {
			continue
		}
		if val, ok := privateFields[k]; ok && strings.TrimSpace(val) != "" {
			emitScalar(&out, k, val)
		}
	}
	return out.String(), nil
}

// isTopKey returns true if the line starts at column 0 with `key:` (or
// `key: value`). Mirrors policy.isTopLevelKeyLine but kept local to avoid
// reaching across packages from a renderer.
func isTopKey(line string) bool {
	if line == "" {
		return false
	}
	c := line[0]
	if c == ' ' || c == '\t' || c == '-' || c == '#' {
		return false
	}
	colon := strings.Index(line, ":")
	if colon <= 0 {
		return false
	}
	key := line[:colon]
	for _, r := range key {
		if r == ' ' || r == '\t' {
			return false
		}
	}
	return true
}

func splitKey(line string) (string, string) {
	colon := strings.Index(line, ":")
	if colon < 0 {
		return line, ""
	}
	val := ""
	if colon+1 < len(line) {
		val = strings.TrimPrefix(line[colon+1:], " ")
	}
	return strings.TrimSpace(line[:colon]), val
}

// readListBlock parses the values under a tags/related key starting at the
// declaration line. Handles both `key: [a, b]` flow style and block-style
// children. Returns the entries in their existing order.
func readListBlock(lines []string, start int) []string {
	_, rest := splitKey(lines[start])
	if strings.HasPrefix(rest, "[") {
		if end := strings.Index(rest, "]"); end > 0 {
			inner := rest[1:end]
			return parseFlowList(inner)
		}
	}
	if rest != "" {
		// Inline single value (uncommon for tags; tolerate it).
		return []string{stripQuotes(rest)}
	}
	var out []string
	for j := start + 1; j < len(lines); j++ {
		line := lines[j]
		if isTopKey(line) {
			break
		}
		trim := strings.TrimSpace(line)
		if trim == "" {
			break
		}
		if !strings.HasPrefix(trim, "-") {
			break
		}
		item := strings.TrimSpace(strings.TrimPrefix(trim, "-"))
		if v := stripQuotes(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func parseFlowList(inner string) []string {
	var out []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
			cur.WriteByte(c)
		case ',':
			if v := stripQuotes(strings.TrimSpace(cur.String())); v != "" {
				out = append(out, v)
			}
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		if v := stripQuotes(strings.TrimSpace(cur.String())); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// stripQuotes mirrors policy.stripQuotes; duplicated locally so the renderer
// has no package-internal dependency on policy.
func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func mergeUnique(existing, additions []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(existing)+len(additions))
	for _, v := range existing {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	for _, v := range additions {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func emitListBlock(out *strings.Builder, key string, items []string) {
	if len(items) == 0 {
		fmt.Fprintf(out, "%s: []\n", key)
		return
	}
	fmt.Fprintf(out, "%s:\n", key)
	for _, item := range items {
		fmt.Fprintf(out, "  - %s\n", quoteIfNeeded(item))
	}
}

func emitScalar(out *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		fmt.Fprintf(out, "%s: \"\"\n", key)
		return
	}
	if needsQuote(value) {
		fmt.Fprintf(out, "%s: %s\n", key, jsonStringQuote(value))
		return
	}
	fmt.Fprintf(out, "%s: %s\n", key, value)
}

// needsQuote returns true when YAML would otherwise misparse the value
// (starts with `[`, `{`, `&`, `*`, `!`, `|`, `>`, `'`, `"`, `%`, `@`,
// `` ` ``, has a leading `-` or `?`, contains `:` followed by space, or
// contains a `#`).
func needsQuote(v string) bool {
	if v == "" {
		return true
	}
	switch v[0] {
	case '[', '{', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`', '?':
		return true
	}
	if v[0] == '-' && (len(v) == 1 || v[1] == ' ') {
		return true
	}
	if strings.Contains(v, ": ") || strings.Contains(v, " #") || strings.Contains(v, "\n") {
		return true
	}
	return false
}

func quoteIfNeeded(v string) string {
	if needsQuote(v) {
		return jsonStringQuote(v)
	}
	return v
}

// jsonStringQuote returns a YAML-safe double-quoted string. YAML accepts
// JSON-style quoted scalars for the small set of escapes we need (\", \\,
// \n, etc.), so leveraging encoding/json's escaper is the lowest-risk path.
func jsonStringQuote(v string) string {
	// Manually produce a double-quoted YAML scalar with the same escape
	// rules as JSON; avoids importing encoding/json from the renderer.
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
