package scheduler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRSSExternalInfoAdapterReadsRSSHubStyleRSSFeed(t *testing.T) {
	client := &http.Client{Transport: schedulerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/rsshub/example" {
			t.Fatalf("path = %q, want RSSHub-style route", r.URL.Path)
		}
		return rssTestResponse(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>RSSHub Example</title>
    <link>https://example.test/</link>
    <description>Example feed</description>
    <item>
      <title>First item</title>
      <link>https://example.test/first</link>
      <description>First summary</description>
      <pubDate>Sat, 23 May 2026 08:00:00 GMT</pubDate>
      <guid>first</guid>
    </item>
    <item>
      <title>Second item</title>
      <link>https://example.test/second</link>
      <description>Second summary</description>
    </item>
  </channel>
</rss>`), nil
	})}

	result, err := RSSExternalInfoAdapter{HTTPClient: client}.ReadInfo(context.Background(), ExternalInfoRequest{
		Source: "rss",
		Schedule: ScheduledSkill{
			ID:              "release-watch",
			ExternalSources: []string{"rss"},
			SkillConfigJSON: json.RawMessage(`{"feed_urls":"https://rsshub.example.test/rsshub/example?placeholder=redacted","feed_limit":"1"}`),
		},
		Now: time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload rssPayload
	if err := json.Unmarshal(result.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FeedCount != 1 || payload.ItemCount != 1 {
		t.Fatalf("payload = %+v, want one feed and one item", payload)
	}
	feed := payload.Feeds[0]
	if feed.Title != "RSSHub Example" || len(feed.Items) != 1 || feed.Items[0].Title != "First item" {
		t.Fatalf("feed = %+v, want parsed RSSHub-style RSS item", feed)
	}
	if strings.Contains(feed.URL, "placeholder=redacted") || strings.Contains(string(result.Payload), "placeholder=redacted") {
		t.Fatalf("payload leaked query token: %s", string(result.Payload))
	}
	if !feed.Truncated {
		t.Fatalf("feed = %+v, want item truncation marker", feed)
	}
}

func TestRSSExternalInfoAdapterReadsAtomFeed(t *testing.T) {
	client := &http.Client{Transport: schedulerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return rssTestResponse(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Atom Example</title>
  <link href="https://example.test/"/>
  <entry>
    <title>Atom item</title>
    <link href="https://example.test/atom-item"/>
    <summary>Atom summary</summary>
    <updated>2026-05-23T08:00:00Z</updated>
    <id>atom-1</id>
  </entry>
</feed>`), nil
	})}

	result, err := RSSExternalInfoAdapter{HTTPClient: client}.ReadInfo(context.Background(), ExternalInfoRequest{
		Source: "rss",
		Schedule: ScheduledSkill{
			ID:              "atom-watch",
			ExternalSources: []string{"rss"},
			SkillConfigJSON: json.RawMessage(`{"feed_urls":"https://rsshub.example.test/feed?format=atom"}`),
		},
		Now: time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload rssPayload
	if err := json.Unmarshal(result.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.FeedCount != 1 || payload.Feeds[0].Title != "Atom Example" || payload.Feeds[0].Items[0].Title != "Atom item" {
		t.Fatalf("payload = %+v, want parsed Atom feed", payload)
	}
}

func TestRSSExternalInfoAdapterRejectsUnsafeFeedURL(t *testing.T) {
	_, err := RSSExternalInfoAdapter{}.ReadInfo(context.Background(), ExternalInfoRequest{
		Source: "rss",
		Schedule: ScheduledSkill{
			ID:              "unsafe",
			ExternalSources: []string{"rss"},
			SkillConfigJSON: json.RawMessage(`{"feed_urls":"ftp://example.test/feed.xml"}`),
		},
		Now: time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC),
	})
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("error = %v, want unsafe scheme rejection", err)
	}
}

func TestRSSExternalInfoAdapterRejectsInternalHosts(t *testing.T) {
	cases := []struct {
		name   string
		url    string
		needle string
	}{
		{"loopback ipv4", "http://127.0.0.1/feed", "loopback"},
		{"loopback ipv6", "http://[::1]/feed", "loopback"},
		{"private rfc1918", "http://10.0.0.1/feed", "private"},
		{"metadata link-local", "http://169.254.169.254/latest/meta-data/", "link-local"},
		{"localhost name", "http://localhost/feed", "reserved name"},
		{"internal suffix", "http://api.internal/feed", "reserved name"},
		{"mDNS local", "http://router.local/feed", "reserved name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RSSExternalInfoAdapter{}.ReadInfo(context.Background(), ExternalInfoRequest{
				Source: "rss",
				Schedule: ScheduledSkill{
					ID:              "internal",
					ExternalSources: []string{"rss"},
					SkillConfigJSON: json.RawMessage(`{"feed_urls":"` + tc.url + `"}`),
				},
				Now: time.Date(2026, 5, 23, 8, 0, 0, 0, time.UTC),
			})
			if err == nil || !strings.Contains(err.Error(), tc.needle) {
				t.Fatalf("error = %v, want %q rejection", err, tc.needle)
			}
		})
	}
}

type schedulerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f schedulerRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func rssTestResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
