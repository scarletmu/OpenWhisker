package scheduler

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/scarletmu/openwhisker/internal/sanitize"
)

const (
	rssDefaultItemLimit   = 10
	rssMaxItemLimit       = 50
	rssMaxFeeds           = 10
	rssMaxResponseBytes   = 1024 * 1024
	rssHTTPClientTimeout  = 10 * time.Second
	rssMaxTitleRunes      = 300
	rssMaxLinkRunes       = 1000
	rssMaxSummaryRunes    = 4000
	rssMaxTimestampRunes  = 128
	rssMaxFeedTitleRunes  = 300
	rssMaxFeedLinkRunes   = 1000
	rssMaxFeedSummaryRuns = 1000
)

type RSSExternalInfoAdapter struct {
	HTTPClient *http.Client
}

type rssAdapterConfig struct {
	FeedURLs  []string
	ItemLimit int
}

type rssPayload struct {
	Feeds     []rssFeedSnapshot `json:"feeds"`
	FeedCount int               `json:"feed_count"`
	ItemCount int               `json:"item_count"`
	Truncated bool              `json:"truncated,omitempty"`
}

type rssFeedSnapshot struct {
	URL       string        `json:"url"`
	Title     string        `json:"title,omitempty"`
	Link      string        `json:"link,omitempty"`
	Summary   string        `json:"summary,omitempty"`
	Items     []rssItemInfo `json:"items"`
	Truncated bool          `json:"truncated,omitempty"`
}

type rssItemInfo struct {
	Title     string `json:"title,omitempty"`
	Link      string `json:"link,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Published string `json:"published,omitempty"`
	ID        string `json:"id,omitempty"`
}

type rssDocument struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title       string       `xml:"title"`
		Link        string       `xml:"link"`
		Description string       `xml:"description"`
		Items       []rssXMLItem `xml:"item"`
	} `xml:"channel"`
}

type rssXMLItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Content     string `xml:"encoded"`
	PubDate     string `xml:"pubDate"`
	GUID        string `xml:"guid"`
}

type atomDocument struct {
	XMLName xml.Name       `xml:"feed"`
	Title   string         `xml:"title"`
	Links   []atomXMLLink  `xml:"link"`
	Updated string         `xml:"updated"`
	Entries []atomXMLEntry `xml:"entry"`
}

type atomXMLEntry struct {
	Title     string        `xml:"title"`
	Links     []atomXMLLink `xml:"link"`
	Summary   string        `xml:"summary"`
	Content   string        `xml:"content"`
	Published string        `xml:"published"`
	Updated   string        `xml:"updated"`
	ID        string        `xml:"id"`
}

type atomXMLLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Text string `xml:",chardata"`
}

func (a RSSExternalInfoAdapter) ReadInfo(ctx context.Context, req ExternalInfoRequest) (ExternalInfoItem, error) {
	cfg, err := rssConfigFromSchedule(req.Schedule)
	if err != nil {
		return ExternalInfoItem{}, err
	}
	client := a.HTTPClient
	if client == nil {
		client = newSafeRSSClient()
	}
	payload := rssPayload{}
	n := len(cfg.FeedURLs)
	if n > rssMaxFeeds {
		n = rssMaxFeeds
		payload.Truncated = true
	}
	// The feeds are independent network round-trips, so fetch them concurrently
	// (wall clock = slowest feed, not the sum) and collect into per-index slots
	// to keep the output order aligned with cfg.FeedURLs.
	feeds := make([]rssFeedSnapshot, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			feeds[i], errs[i] = fetchRSSFeed(ctx, client, cfg.FeedURLs[i], cfg.ItemLimit)
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			return ExternalInfoItem{}, fmt.Errorf("feed %d: %w", i+1, errs[i])
		}
		payload.ItemCount += len(feeds[i].Items)
		payload.Feeds = append(payload.Feeds, feeds[i])
	}
	payload.FeedCount = len(payload.Feeds)
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ExternalInfoItem{}, err
	}
	return ExternalInfoItem{
		Source:    req.Source,
		Summary:   fmt.Sprintf("Fetched %d RSS/Atom feed(s), %d item(s).", payload.FeedCount, payload.ItemCount),
		Payload:   encoded,
		FetchedAt: req.Now.UTC(),
	}, nil
}

func rssConfigFromSchedule(schedule ScheduledSkill) (rssAdapterConfig, error) {
	var raw map[string]string
	if len(schedule.SkillConfigJSON) > 0 {
		if err := json.Unmarshal(schedule.SkillConfigJSON, &raw); err != nil {
			return rssAdapterConfig{}, fmt.Errorf("skill_config must be an object: %w", err)
		}
	}
	feedURLs := splitFeedURLs(firstNonBlank(raw["feed_urls"], raw["feed_url"]))
	if len(feedURLs) == 0 {
		return rssAdapterConfig{}, errors.New("skill_config.feed_urls is required for rss external info")
	}
	if len(feedURLs) > rssMaxFeeds {
		feedURLs = feedURLs[:rssMaxFeeds]
	}
	itemLimit := rssDefaultItemLimit
	if value := strings.TrimSpace(raw["feed_limit"]); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return rssAdapterConfig{}, errors.New("skill_config.feed_limit must be a positive integer")
		}
		itemLimit = parsed
	}
	if itemLimit > rssMaxItemLimit {
		itemLimit = rssMaxItemLimit
	}
	for _, feedURL := range feedURLs {
		if err := validateFeedURL(feedURL); err != nil {
			return rssAdapterConfig{}, err
		}
	}
	return rssAdapterConfig{FeedURLs: feedURLs, ItemLimit: itemLimit}, nil
}

// SanitizedSkillConfigJSON is a thin wrapper around sanitize.SkillConfigJSON
// that takes a ScheduledSkill (the only caller pattern in this codebase).
// All redaction logic lives in internal/sanitize.
func SanitizedSkillConfigJSON(schedule ScheduledSkill) json.RawMessage {
	return sanitize.SkillConfigJSON(schedule.SkillConfigJSON)
}

func fetchRSSFeed(ctx context.Context, client *http.Client, feedURL string, itemLimit int) (rssFeedSnapshot, error) {
	parsed, err := url.Parse(feedURL)
	if err != nil {
		return rssFeedSnapshot{}, errors.New("invalid feed URL")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return rssFeedSnapshot{}, errors.New("invalid feed request")
	}
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml;q=0.9, */*;q=0.1")
	req.Header.Set("User-Agent", "OpenWhisker scheduler rss adapter")
	resp, err := client.Do(req)
	if err != nil {
		return rssFeedSnapshot{}, fmt.Errorf("request %s failed: %w", parsed.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return rssFeedSnapshot{}, fmt.Errorf("request %s returned HTTP %d", parsed.Host, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, rssMaxResponseBytes+1))
	if err != nil {
		return rssFeedSnapshot{}, fmt.Errorf("read %s response: %w", parsed.Host, err)
	}
	if len(body) > rssMaxResponseBytes {
		return rssFeedSnapshot{}, fmt.Errorf("response from %s exceeds size limit", parsed.Host)
	}
	return parseFeedSnapshot(body, feedURL, itemLimit)
}

func parseFeedSnapshot(body []byte, feedURL string, itemLimit int) (rssFeedSnapshot, error) {
	var probe struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &probe); err != nil {
		return rssFeedSnapshot{}, fmt.Errorf("parse feed XML: %w", err)
	}
	switch strings.ToLower(probe.XMLName.Local) {
	case "rss":
		var doc rssDocument
		if err := xml.Unmarshal(body, &doc); err != nil {
			return rssFeedSnapshot{}, fmt.Errorf("parse RSS feed: %w", err)
		}
		return rssSnapshotFromDocument(feedURL, doc, itemLimit), nil
	case "feed":
		var doc atomDocument
		if err := xml.Unmarshal(body, &doc); err != nil {
			return rssFeedSnapshot{}, fmt.Errorf("parse Atom feed: %w", err)
		}
		return atomSnapshotFromDocument(feedURL, doc, itemLimit), nil
	default:
		return rssFeedSnapshot{}, fmt.Errorf("unsupported feed XML root %q", probe.XMLName.Local)
	}
}

func rssSnapshotFromDocument(feedURL string, doc rssDocument, itemLimit int) rssFeedSnapshot {
	items := make([]rssItemInfo, 0, minInt(len(doc.Channel.Items), itemLimit))
	for i, item := range doc.Channel.Items {
		if i >= itemLimit {
			break
		}
		summary := firstNonBlank(item.Description, item.Content)
		items = append(items, rssItemInfo{
			Title:     cleanRSSField(item.Title, rssMaxTitleRunes),
			Link:      cleanRSSField(item.Link, rssMaxLinkRunes),
			Summary:   cleanRSSField(summary, rssMaxSummaryRunes),
			Published: cleanRSSField(item.PubDate, rssMaxTimestampRunes),
			ID:        cleanRSSField(item.GUID, rssMaxLinkRunes),
		})
	}
	return rssFeedSnapshot{
		URL:       safeFeedURLLabel(feedURL),
		Title:     cleanRSSField(doc.Channel.Title, rssMaxFeedTitleRunes),
		Link:      cleanRSSField(doc.Channel.Link, rssMaxFeedLinkRunes),
		Summary:   cleanRSSField(doc.Channel.Description, rssMaxFeedSummaryRuns),
		Items:     items,
		Truncated: len(doc.Channel.Items) > itemLimit,
	}
}

func atomSnapshotFromDocument(feedURL string, doc atomDocument, itemLimit int) rssFeedSnapshot {
	items := make([]rssItemInfo, 0, minInt(len(doc.Entries), itemLimit))
	for i, entry := range doc.Entries {
		if i >= itemLimit {
			break
		}
		summary := firstNonBlank(entry.Summary, entry.Content)
		published := firstNonBlank(entry.Published, entry.Updated)
		items = append(items, rssItemInfo{
			Title:     cleanRSSField(entry.Title, rssMaxTitleRunes),
			Link:      cleanRSSField(atomLink(entry.Links), rssMaxLinkRunes),
			Summary:   cleanRSSField(summary, rssMaxSummaryRunes),
			Published: cleanRSSField(published, rssMaxTimestampRunes),
			ID:        cleanRSSField(entry.ID, rssMaxLinkRunes),
		})
	}
	return rssFeedSnapshot{
		URL:       safeFeedURLLabel(feedURL),
		Title:     cleanRSSField(doc.Title, rssMaxFeedTitleRunes),
		Link:      cleanRSSField(atomLink(doc.Links), rssMaxFeedLinkRunes),
		Items:     items,
		Truncated: len(doc.Entries) > itemLimit,
	}
}

func validateFeedURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return errors.New("feed URL must be absolute")
	}
	if parsed.User != nil {
		return errors.New("feed URL userinfo is not allowed")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return errors.New("feed URL scheme must be http or https")
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return errors.New("feed URL host is required")
	}
	// Reject explicit IP literals that target internal/loopback/metadata
	// services without paying for a DNS lookup. DNS-name targets still get
	// re-checked at fetch time (see fetchRSSFeed) to defeat DNS rebinding
	// and to refuse names that resolve only to private space.
	if ip := net.ParseIP(hostname); ip != nil {
		if err := assertPublicIP(ip); err != nil {
			return fmt.Errorf("feed URL host %s: %w", hostname, err)
		}
	} else if isReservedHostname(hostname) {
		return fmt.Errorf("feed URL host %q targets a reserved name", hostname)
	}
	return nil
}

// assertPublicIP rejects loopback, link-local, multicast, unspecified, and
// RFC1918 / RFC4193 / cloud-metadata addresses. This is the SSRF guard for
// the RSS adapter — without it a vault SCHEDULE.md could point feed_urls at
// http://169.254.169.254/ (AWS/GCP IMDS) or http://10.x.x.x/ (internal admin
// endpoints) and the daemon would happily fetch + surface the response.
func assertPublicIP(ip net.IP) error {
	if ip.IsUnspecified() {
		return errors.New("address is unspecified (0.0.0.0/::)")
	}
	if ip.IsLoopback() {
		return errors.New("address is loopback")
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("address is link-local")
	}
	if ip.IsMulticast() {
		return errors.New("address is multicast")
	}
	if ip.IsPrivate() {
		return errors.New("address is private (RFC1918 / RFC4193)")
	}
	// Cloud-metadata + benchmark ranges. IsPrivate() does not cover
	// 169.254.169.254 (link-local handled above), but 100.64.0.0/10
	// (RFC6598 carrier-grade NAT) and IPv4-mapped IPv6 of the same need an
	// explicit check.
	if v4 := ip.To4(); v4 != nil {
		// 100.64.0.0/10 RFC6598 shared address space.
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return errors.New("address is RFC6598 shared address space")
		}
	}
	return nil
}

// isReservedHostname returns true for DNS names that map to local/internal
// services we never want to fetch even if the operator forgets to use an
// IP literal.
func isReservedHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return true
	}
	if host == "localhost" {
		return true
	}
	suffixes := []string{
		".localhost",
		".local",       // mDNS
		".internal",    // common internal TLD
		".intranet",
		".corp",
		".home",
		".lan",
	}
	for _, suffix := range suffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// safeFeedURLLabel returns a single-URL form of sanitize.FeedURLList: useful
// for tagging a fetched snapshot with its (sanitized) source URL without
// retaining user-info / query / fragment leaks.
func safeFeedURLLabel(value string) string {
	labels := sanitize.FeedURLList(value)
	if len(labels) == 0 {
		return ""
	}
	return labels[0]
}

// splitFeedURLs is still used by the RSS input-parsing path (validateFeedURL
// loop in newRSSAdapterConfig). The earlier sanitized-URL helpers moved to
// internal/sanitize; the splitter stays here because it serves a parsing
// purpose rather than a redaction one.
func splitFeedURLs(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func atomLink(links []atomXMLLink) string {
	for _, link := range links {
		if strings.TrimSpace(link.Rel) == "" || strings.EqualFold(strings.TrimSpace(link.Rel), "alternate") {
			return firstNonBlank(link.Href, link.Text)
		}
	}
	if len(links) > 0 {
		return firstNonBlank(links[0].Href, links[0].Text)
	}
	return ""
}

func cleanRSSField(value string, maxRunes int) string {
	value = html.UnescapeString(strings.TrimSpace(value))
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes])
	}
	return value
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// newSafeRSSClient returns the default outbound HTTP client used by the RSS
// adapter when the caller doesn't inject one. It wraps http.DefaultTransport
// with a RoundTripper that resolves the request hostname and re-applies the
// public-IP guard to every resolved address (defeating DNS rebinding /
// CNAME-to-private-IP attacks against feed URLs whose hostnames passed the
// initial validateFeedURL check). The wrapper preserves http.DefaultTransport
// as the underlying transport so callers that swap DefaultTransport (e.g.
// integration tests using a stub round-tripper) continue to win.
func newSafeRSSClient() *http.Client {
	return &http.Client{
		Timeout:   rssHTTPClientTimeout,
		Transport: safeRSSTransport{},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return validateFeedURL(req.URL.String())
		},
	}
}

type safeRSSTransport struct{}

func (safeRSSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := assertRequestHostPublic(req); err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(req)
}

// assertRequestHostPublic resolves the request hostname and rejects the
// request if any returned address is non-public. If resolution fails or
// returns no addresses, the underlying transport handles the dial (and any
// failure that follows), so this function never adds a false positive.
func assertRequestHostPublic(req *http.Request) error {
	hostname := req.URL.Hostname()
	if hostname == "" {
		return errors.New("rss adapter: request has no hostname")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if err := assertPublicIP(ip); err != nil {
			return fmt.Errorf("rss adapter: refusing host %s: %w", hostname, err)
		}
		return nil
	}
	resolver := net.DefaultResolver
	addrs, err := resolver.LookupHost(req.Context(), hostname)
	if err != nil || len(addrs) == 0 {
		return nil
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if err := assertPublicIP(ip); err != nil {
			return fmt.Errorf("rss adapter: %s resolved to %s: %w", hostname, addr, err)
		}
	}
	return nil
}
