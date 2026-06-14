package clip

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Clip note status values written to frontmatter.
const (
	statusClipping = "clipping"
	statusClipped  = "clipped"
	statusFailed   = "clip-failed"
)

// renderSkeleton builds the placeholder note written the instant a link is
// captured, before the page is fetched. It already carries raw_kind: web-clip
// and the source URL so the note is traceable and recoverable even if the
// daemon restarts mid-clip (the clip scanner re-picks status: clipping notes).
func renderSkeleton(rawURL, source string, createdAt time.Time) string {
	stamp := createdAt.Format(time.RFC3339)
	return fmt.Sprintf(`---
title: "剪藏中…"
status: %s
source: %s
created: %s
updated: %s
url: %q
raw_kind: web-clip
openwhisker_capture: web-clip
---

# 剪藏中…

> 来源：%s
`, statusClipping, source, stamp, stamp, rawURL, rawURL)
}

// renderClipped builds the finished clip note: the extracted article body with
// a single source-trace line. Per §3.2 the clip is the article itself with no
// user annotation.
func renderClipped(rawURL, source, title, body string, createdAt, updatedAt time.Time) string {
	if strings.TrimSpace(title) == "" {
		title = rawURL
	}
	return fmt.Sprintf(`---
title: %q
status: %s
source: %s
created: %s
updated: %s
url: %q
raw_kind: web-clip
openwhisker_capture: web-clip
---

# %s

> 来源：%s

%s
`, title, statusClipped, source, createdAt.Format(time.RFC3339), updatedAt.Format(time.RFC3339),
		rawURL, title, rawURL, strings.TrimSpace(body))
}

// renderFailed rewrites the skeleton when the fetch/extract fails, recording the
// error so the note is not silently stuck in "clipping".
func renderFailed(rawURL, source, reason string, createdAt, updatedAt time.Time) string {
	return fmt.Sprintf(`---
title: "剪藏失败"
status: %s
source: %s
created: %s
updated: %s
url: %q
raw_kind: web-clip
openwhisker_capture: web-clip
---

# 剪藏失败

> 来源：%s

剪藏未成功：%s
`, statusFailed, source, createdAt.Format(time.RFC3339), updatedAt.Format(time.RFC3339),
		rawURL, rawURL, reason)
}

// clipSlug derives a filesystem-safe slug from the URL host and path tail, used
// (with a timestamp prefix) as the clip note filename.
func clipSlug(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return "clip"
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	tail := strings.Trim(parsed.Path, "/")
	if idx := strings.LastIndex(tail, "/"); idx >= 0 {
		tail = tail[idx+1:]
	}
	tail = strings.TrimSuffix(tail, ".html")
	slug := sanitizeSlug(host)
	if tail != "" {
		slug += "-" + sanitizeSlug(tail)
	}
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "clip"
	}
	if len([]rune(slug)) > 60 {
		slug = string([]rune(slug)[:60])
		slug = strings.Trim(slug, "-")
	}
	return slug
}

// sanitizeSlug keeps ASCII letters/digits and CJK runes, mapping everything else
// to a single hyphen.
func sanitizeSlug(s string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
			prevHyphen = false
		case r >= 0x4e00 && r <= 0x9fff: // CJK unified ideographs
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	return b.String()
}
