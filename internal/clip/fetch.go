package clip

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/scarletmu/openwhisker/internal/safehttp"
)

const (
	clipHTTPTimeout = 15 * time.Second
	clipMaxBytes    = 4 * 1024 * 1024 // 4 MiB ceiling on a fetched page
)

// safeClient returns the default SSRF-guarded HTTP client for clipping.
func safeClient() *http.Client { return safehttp.Client(clipHTTPTimeout) }

// validateClipURL rejects non-public / non-http(s) clip targets up front.
func validateClipURL(rawURL string) error {
	return safehttp.ValidatePublicURL(strings.TrimSpace(rawURL))
}

// fetchHTML retrieves url and returns its HTML body. The URL is re-validated
// against the SSRF guard before the request, and the client re-checks every
// dialled address + redirect target. Non-HTML and oversized responses are
// rejected.
func fetchHTML(ctx context.Context, client *http.Client, rawURL string) (string, error) {
	if err := safehttp.ValidatePublicURL(rawURL); err != nil {
		return "", err
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid clip URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("invalid clip request")
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.1")
	req.Header.Set("User-Agent", "OpenWhisker link clipper")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request %s failed: %w", parsed.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("request %s returned HTTP %d", parsed.Host, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		mediatype := strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
		if mediatype != "" && !strings.Contains(mediatype, "html") && !strings.Contains(mediatype, "xml") && mediatype != "text/plain" {
			return "", fmt.Errorf("unsupported content type %q", mediatype)
		}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, clipMaxBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %s response: %w", parsed.Host, err)
	}
	if int64(len(body)) > clipMaxBytes {
		return "", fmt.Errorf("response from %s exceeds size limit", parsed.Host)
	}
	return string(body), nil
}
