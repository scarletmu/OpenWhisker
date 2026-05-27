// Package-private enrich plan validator. Lives alongside checker.go so the
// existing frontmatter parsing helpers are in reach; exposed as a top-level
// function rather than a Checker method because the known-tag vocab is
// per-call dynamic state, not per-Checker convention state.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/scarletmu/openwhisker/internal/model"
)

// TagVocab is the minimal interface enrich policy needs from the tag
// vocabulary service: a single membership check used to gate `topic/*` /
// `skill/*` additions against the vault-derived controlled set.
type TagVocab interface {
	HasTag(tag string) bool
}

// EnrichWritableFields enumerates the seven frontmatter fields a Phase 7
// enrich plan is allowed to introduce or modify. Anything else in the
// frontmatter must have a byte-identical value before and after.
var EnrichWritableFields = []string{
	model.EnrichFrontmatterTags,
	model.EnrichFrontmatterRelated,
	model.EnrichFrontmatterRouteSuggestion,
	model.EnrichFrontmatterNewTagCandidates,
	model.EnrichFrontmatterEnrichedAt,
	model.EnrichFrontmatterEnrichRunID,
	model.EnrichFrontmatterEnrichAttempts,
}

// CheckEnrichPlan runs all three Phase 7 guards (path / field / body) on a
// candidate enrich plan. Returns the first violation as an error.
//
// expectedTargetPath is the Raw note path the enrich job was started against;
// the plan is rejected if its operations touch anything else. beforeContent
// is the on-disk file as the executor would have read it (typically just
// before the orchestrator built the plan, so the executor's own BeforeHash
// check is still the load-bearing TOCTOU defense).
func CheckEnrichPlan(plan model.VaultPlan, expectedTargetPath, beforeContent string, vocab TagVocab) error {
	if plan.RiskLevel != model.RiskLow {
		return fmt.Errorf("enrich plan risk %q is not low", plan.RiskLevel)
	}
	if plan.RequiresApproval {
		return errors.New("enrich plan must not require approval")
	}
	if len(plan.Operations) != 1 {
		return fmt.Errorf("enrich plan must have exactly 1 operation, got %d", len(plan.Operations))
	}
	op := plan.Operations[0]
	if op.Type != model.OperationRewriteNote {
		return fmt.Errorf("enrich op type %q is not %s", op.Type, model.OperationRewriteNote)
	}
	if op.RiskLevel != model.RiskLow {
		return fmt.Errorf("enrich op risk %q is not low", op.RiskLevel)
	}
	if op.TargetPath != expectedTargetPath {
		return fmt.Errorf("enrich path_guard: op target %q != expected %q", op.TargetPath, expectedTargetPath)
	}
	if err := validateRelativeVaultPath(op.TargetPath); err != nil {
		return fmt.Errorf("enrich path_guard: %w", err)
	}
	if err := validateMutatingBeforeHash(op); err != nil {
		return fmt.Errorf("enrich path_guard: %w", err)
	}

	var payload model.CreateNotePayload
	if err := json.Unmarshal([]byte(op.PayloadJSON), &payload); err != nil {
		return fmt.Errorf("enrich payload decode: %w", err)
	}
	if strings.TrimSpace(payload.Content) == "" {
		return errors.New("enrich payload content is empty")
	}

	beforeFM, beforeBody, ok := markdownFrontmatter(beforeContent)
	if !ok {
		return errors.New("enrich body_guard: before content has no frontmatter")
	}
	afterFM, afterBody, ok := markdownFrontmatter(payload.Content)
	if !ok {
		return errors.New("enrich body_guard: after content has no frontmatter")
	}

	if sha256Hex(beforeBody) != sha256Hex(afterBody) {
		return errors.New("enrich body_guard: Raw Text body changed")
	}

	return enrichCheckFieldGuard(beforeFM, afterFM, vocab)
}

// enrichCheckFieldGuard is the diff core. The strategy is:
//   - Split each frontmatter into top-level field blobs (everything between
//     one `key:` at column 0 and the next).
//   - For every key that exists in either side, decide:
//       - if in the 7-field writable allowlist: apply per-field rules
//         (tags / related ⇒ append-only with prefix/vocab checks; the other
//         five OW-private fields ⇒ any value is allowed since the
//         orchestrator owns rendering).
//       - else: blob must be byte-identical before and after.
func enrichCheckFieldGuard(beforeFM, afterFM string, vocab TagVocab) error {
	beforeFields := splitFrontmatterFields(beforeFM)
	afterFields := splitFrontmatterFields(afterFM)
	allowed := map[string]bool{}
	for _, k := range EnrichWritableFields {
		allowed[k] = true
	}

	seen := map[string]bool{}
	keys := make([]string, 0, len(beforeFields)+len(afterFields))
	for _, k := range orderedKeys(beforeFields) {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	for _, k := range orderedKeys(afterFields) {
		if !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}

	for _, k := range keys {
		beforeVal, hasBefore := beforeFields[k]
		afterVal, hasAfter := afterFields[k]
		if !allowed[k] {
			// Non-writable field: must be unchanged. Also rejects "field
			// disappeared" and "non-writable new field appeared".
			if !hasBefore || !hasAfter {
				return fmt.Errorf("enrich field_guard: non-writable field %q added or removed", k)
			}
			if normalizeBlob(beforeVal) != normalizeBlob(afterVal) {
				return fmt.Errorf("enrich field_guard: non-writable field %q changed", k)
			}
			continue
		}
		// Writable field. For tags and related the rule is append-only with
		// per-item validation; for everything else any after value is fine
		// (the orchestrator built the payload so the policy trusts shape).
		switch k {
		case model.EnrichFrontmatterTags:
			beforeList := parseYAMLList(beforeVal)
			afterList := parseYAMLList(afterVal)
			if err := enrichValidateTagsAppend(beforeList, afterList, vocab); err != nil {
				return err
			}
		case model.EnrichFrontmatterRelated:
			beforeList := parseYAMLList(beforeVal)
			afterList := parseYAMLList(afterVal)
			if err := enrichValidateRelatedAppend(beforeList, afterList); err != nil {
				return err
			}
		default:
			// OW-private writable; no schema check at policy layer.
		}
	}
	return nil
}

// enrichValidateTagsAppend enforces:
//   - after ⊇ before (no deletion);
//   - every added item carries an allowed prefix: topic/, skill/, status/
//     (limited to status/needs-review), or raw/;
//   - topic/* and skill/* additions are members of vocab.
func enrichValidateTagsAppend(before, after []string, vocab TagVocab) error {
	if !setSubset(before, after) {
		return errors.New("enrich field_guard: tags must be append-only (existing tag removed)")
	}
	added := setDiff(after, before)
	for _, tag := range added {
		clean := strings.ToLower(strings.TrimSpace(tag))
		if clean == "" {
			return errors.New("enrich field_guard: empty tag added")
		}
		switch {
		case strings.HasPrefix(clean, "topic/") && len(clean) > len("topic/"):
			if vocab == nil || !vocab.HasTag(clean) {
				return fmt.Errorf("enrich field_guard: topic tag %q not in known vocabulary", tag)
			}
		case strings.HasPrefix(clean, "skill/") && len(clean) > len("skill/"):
			if vocab == nil || !vocab.HasTag(clean) {
				return fmt.Errorf("enrich field_guard: skill tag %q not in known vocabulary", tag)
			}
		case clean == "status/needs-review":
			// only status/* value enrich is allowed to introduce.
		case strings.HasPrefix(clean, "status/"):
			return fmt.Errorf("enrich field_guard: status tag %q not allowed (only status/needs-review)", tag)
		case strings.HasPrefix(clean, "raw/") && len(clean) > len("raw/"):
			// raw/* is an existing vault convention; allowed.
		case strings.HasPrefix(clean, "type/") || strings.HasPrefix(clean, "interview/"):
			return fmt.Errorf("enrich field_guard: tag prefix not allowed for enrich: %q", tag)
		default:
			return fmt.Errorf("enrich field_guard: tag %q does not satisfy any allowed prefix", tag)
		}
	}
	return nil
}

// enrichValidateRelatedAppend mirrors tags with two differences:
//   - additions must look like wikilinks (`[[...]]` or `"[[...]]"`);
//   - at most EnrichRelatedAppendMax new items per enrich run.
func enrichValidateRelatedAppend(before, after []string) error {
	if !setSubset(before, after) {
		return errors.New("enrich field_guard: related must be append-only (existing entry removed)")
	}
	added := setDiff(after, before)
	if len(added) > model.EnrichRelatedAppendMax {
		return fmt.Errorf("enrich field_guard: related additions %d exceed max %d", len(added), model.EnrichRelatedAppendMax)
	}
	for _, item := range added {
		trimmed := strings.TrimSpace(item)
		trimmed = strings.Trim(trimmed, `"'`)
		if !strings.HasPrefix(trimmed, "[[") || !strings.HasSuffix(trimmed, "]]") {
			return fmt.Errorf("enrich field_guard: related entry %q is not a wikilink", item)
		}
	}
	return nil
}

// ---- frontmatter splitter ----

// splitFrontmatterFields chops a frontmatter block into per-top-level-key
// blobs. A "top-level key" is a line starting at column 0 of the form
// `key: ...`. The blob is the rest of that line plus all subsequent
// indented or empty lines, joined with newlines. The blob preserves the
// original text so normalizeBlob can compare with whitespace tolerance.
func splitFrontmatterFields(fm string) map[string]string {
	out := map[string]string{}
	if fm == "" {
		return out
	}
	lines := strings.Split(fm, "\n")
	currentKey := ""
	var buf strings.Builder
	flush := func() {
		if currentKey != "" {
			out[currentKey] = strings.TrimRight(buf.String(), "\n")
		}
		buf.Reset()
	}
	for _, line := range lines {
		if isTopLevelKeyLine(line) {
			flush()
			colon := strings.Index(line, ":")
			currentKey = strings.TrimSpace(line[:colon])
			rest := ""
			if colon+1 < len(line) {
				rest = strings.TrimPrefix(line[colon+1:], " ")
			}
			buf.WriteString(rest)
			buf.WriteByte('\n')
			continue
		}
		if currentKey != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
	return out
}

func isTopLevelKeyLine(line string) bool {
	if line == "" {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' || line[0] == '-' || line[0] == '#' {
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

// orderedKeys returns the keys of m in a deterministic order — sorted
// alphabetically so two splitFrontmatterFields outputs compare consistently.
func orderedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// tiny sort to avoid the package-level sort dep:
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// normalizeBlob collapses pure-whitespace differences so that
// "foo\n" and "foo" compare equal, while still rejecting any meaningful
// edit. Used only for non-writable scalar fields.
func normalizeBlob(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, strings.TrimRight(l, " \t\r"))
	}
	return strings.Join(out, "\n")
}

// parseYAMLList accepts the blob captured by splitFrontmatterFields for
// either tags or related and returns the entries as a slice. Supports
// flow ([a, "b", 'c']) and block (one `- item` per line) styles.
func parseYAMLList(blob string) []string {
	blob = strings.TrimSpace(blob)
	if blob == "" {
		return nil
	}
	// Flow style on the first line.
	if strings.HasPrefix(blob, "[") {
		if end := strings.Index(blob, "]"); end > 0 {
			inner := blob[1:end]
			out := make([]string, 0)
			for _, item := range splitFlowList(inner) {
				if v := stripQuotes(strings.TrimSpace(item)); v != "" {
					out = append(out, v)
				}
			}
			return out
		}
	}
	// Block style.
	var out []string
	for _, line := range strings.Split(blob, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if !strings.HasPrefix(trim, "-") {
			continue
		}
		item := strings.TrimSpace(strings.TrimPrefix(trim, "-"))
		if v := stripQuotes(item); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// splitFlowList handles `a, "b, c", 'd'` by tracking quote state — a naive
// strings.Split on comma would over-cut quoted entries containing commas.
func splitFlowList(inner string) []string {
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
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func stripQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func setSubset(small, big []string) bool {
	bigSet := map[string]bool{}
	for _, v := range big {
		bigSet[v] = true
	}
	for _, v := range small {
		if !bigSet[v] {
			return false
		}
	}
	return true
}

func setDiff(after, before []string) []string {
	beforeSet := map[string]bool{}
	for _, v := range before {
		beforeSet[v] = true
	}
	var added []string
	seen := map[string]bool{}
	for _, v := range after {
		if beforeSet[v] || seen[v] {
			continue
		}
		seen[v] = true
		added = append(added, v)
	}
	return added
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
