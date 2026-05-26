// Package sanitize centralizes the redaction rules applied to anything that
// leaves the OpenWhisker process boundary (outbox payloads, Matrix replies,
// agent traces, structured logs).
//
// The rules originated in internal/scheduler/rss_adapter.go for RSS skill
// configs and were extracted in Phase 6 so agent_runs.tool_trace_json and
// other downstream surfaces share a single, audited implementation.
//
// Design notes:
//
//   - All rules are intentionally heuristic. The goal is to remove the most
//     common leaks (private hostnames, tokens, Matrix IDs, machine paths)
//     before content reaches users or third-party services, not to provide
//     cryptographic guarantees.
//   - Inputs that look ambiguous are left intact rather than mangled — the
//     placeholder noise of an over-zealous regex is worse than the residual
//     risk of an obscure pattern we missed.
//   - Public infrastructure (api.openai.com, api.deepseek.com, GitHub, etc.)
//     is *not* masked. Only patterns that indicate a private / internal /
//     user-specific resource are replaced.
package sanitize

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

// IsSensitiveConfigKey reports whether the given config key name suggests its
// value carries a secret. Used to decide whether to redact the value before
// emitting it in a structured payload.
func IsSensitiveConfigKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{
		"token", "secret", "password", "api_key", "apikey",
		"authorization", "auth", "credential", "private_key",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

// FeedURLList parses a comma/newline separated list of feed URLs and returns
// the safe labels (scheme + host + path; user-info, query, fragment stripped).
// Invalid URLs are dropped silently.
func FeedURLList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		label := safeURLLabel(part)
		if label != "" {
			out = append(out, label)
		}
	}
	return out
}

func safeURLLabel(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

// SkillConfigJSON returns a sanitized copy of a skill_config JSON object.
// Keys named like feed_url(s) are URL-cleaned; keys flagged sensitive by
// IsSensitiveConfigKey have their value replaced with <redacted>. Returns
// `{}` on invalid input.
func SkillConfigJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		// Not a string→string map. Return as-is rather than dropping the
		// structure; callers can layer extra masking if needed.
		return raw
	}
	for key, value := range values {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "feed_url", "feed_urls":
			values[key] = strings.Join(FeedURLList(value), ",")
		default:
			if IsSensitiveConfigKey(key) {
				values[key] = "<redacted>"
			}
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

var (
	// Order matters: more specific patterns run first so a broad rule does
	// not consume substrings that a precise rule would label more cleanly.

	reBearerToken      = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._\-]{8,}`)
	reSyaToken         = regexp.MustCompile(`\bsyt_[A-Za-z0-9_\-]{10,}`)
	reSecretAssignment = regexp.MustCompile(`(?i)\b(api[_-]?key|access[_-]?token|secret|password|authorization)\s*[:=]\s*['"]?[A-Za-z0-9._\-/+]{8,}['"]?`)

	reMatrixRoom  = regexp.MustCompile(`![A-Za-z0-9_\-]+:[A-Za-z0-9._\-]+`)
	reMatrixUser  = regexp.MustCompile(`@[A-Za-z0-9._\-]+:[A-Za-z0-9._\-]+`)
	reMatrixEvent = regexp.MustCompile(`\$[A-Za-z0-9._\-]{16,}`)

	reEmail = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

	reUserPathMac   = regexp.MustCompile(`/Users/[^/\s'"]+`)
	reUserPathLinux = regexp.MustCompile(`/home/[^/\s'"]+`)
	reUserPathWin   = regexp.MustCompile(`(?i)[A-Z]:\\Users\\[^\\\s'"]+`)

	rePrivateIP4 = regexp.MustCompile(`\b(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3}|127\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)

	// Hostnames ending in suffixes that indicate a non-public network. Match
	// either a bare host or a host inside a URL.
	rePrivateHost = regexp.MustCompile(`\b[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?)*\.(?:local|internal|lan|home|corp|intranet)\b`)
)

// FreeText returns s with the most common identifying patterns replaced by
// generic placeholders. Use this on any free-text string that will be written
// to a durable surface (trace JSON, outbox body, log line, Matrix reply).
//
// Public service hostnames (api.openai.com, api.deepseek.com, github.com, …)
// are not modified.
func FreeText(s string) string {
	if s == "" {
		return s
	}
	// Secrets first: replace before any IP/host rule can split the URL they
	// embed.
	s = reBearerToken.ReplaceAllString(s, "Bearer <token>")
	s = reSyaToken.ReplaceAllString(s, "<matrix-access-token>")
	s = reSecretAssignment.ReplaceAllStringFunc(s, func(match string) string {
		// Keep the key name; replace the value.
		idx := strings.IndexAny(match, ":=")
		if idx < 0 {
			return "<secret>"
		}
		return strings.TrimRight(match[:idx], " ") + "=<redacted>"
	})

	s = reMatrixUser.ReplaceAllString(s, "<matrix-user>")
	s = reMatrixRoom.ReplaceAllString(s, "<matrix-room>")
	s = reMatrixEvent.ReplaceAllString(s, "<matrix-event>")
	s = reEmail.ReplaceAllString(s, "<email>")

	s = reUserPathMac.ReplaceAllString(s, "/Users/<user>")
	s = reUserPathLinux.ReplaceAllString(s, "/home/<user>")
	s = reUserPathWin.ReplaceAllString(s, `C:\Users\<user>`)

	s = rePrivateHost.ReplaceAllString(s, "<private-host>")
	s = rePrivateIP4.ReplaceAllString(s, "<private-ip>")

	return s
}

// FreeTextBytes is a convenience wrapper that returns nil for nil input.
func FreeTextBytes(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	return []byte(FreeText(string(b)))
}
