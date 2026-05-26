package sanitize

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsSensitiveConfigKey(t *testing.T) {
	for _, key := range []string{"token", "API_KEY", "secret_value", "AuthHeader", "password"} {
		if !IsSensitiveConfigKey(key) {
			t.Errorf("IsSensitiveConfigKey(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"feed_url", "topic", "delivery"} {
		if IsSensitiveConfigKey(key) {
			t.Errorf("IsSensitiveConfigKey(%q) = true, want false", key)
		}
	}
}

func TestFeedURLList(t *testing.T) {
	got := FeedURLList("https://user:pw@example.com/feed?token=abc, https://news.example.org/rss")
	if len(got) != 2 {
		t.Fatalf("FeedURLList returned %d items, want 2: %v", len(got), got)
	}
	if strings.Contains(got[0], "user") || strings.Contains(got[0], "token") {
		t.Errorf("FeedURLList kept secret material: %q", got[0])
	}
}

func TestSkillConfigJSON_RedactsSensitive(t *testing.T) {
	raw := json.RawMessage(`{"feed_url":"https://x:y@a.com/feed?token=z","api_key":"abc123","topic":"news"}`)
	got := SkillConfigJSON(raw)
	var parsed map[string]string
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("unmarshal sanitized JSON: %v", err)
	}
	if parsed["api_key"] != "<redacted>" {
		t.Errorf("api_key = %q, want <redacted>", parsed["api_key"])
	}
	if strings.Contains(parsed["feed_url"], "token") || strings.Contains(parsed["feed_url"], "x:y") {
		t.Errorf("feed_url leaked secrets: %q", parsed["feed_url"])
	}
	if parsed["topic"] != "news" {
		t.Errorf("topic should be preserved, got %q", parsed["topic"])
	}
}

func TestSkillConfigJSON_EmptyAndInvalid(t *testing.T) {
	if got := string(SkillConfigJSON(nil)); got != "{}" {
		t.Errorf("nil input → %q, want %q", got, "{}")
	}
	if got := string(SkillConfigJSON(json.RawMessage("not json"))); got != "{}" {
		t.Errorf("invalid input → %q, want %q", got, "{}")
	}
}

func TestFreeText_MasksEverything(t *testing.T) {
	cases := []struct {
		in     string
		mustNotContain []string
		mustContain    []string
	}{
		{
			in:             "User @alice:matrix.internal sent to !room123:server.local",
			mustNotContain: []string{"@alice:matrix.internal", "!room123:server.local"},
			mustContain:    []string{"<matrix-user>", "<matrix-room>"},
		},
		{
			in:             "contact ops@company.internal for help",
			mustNotContain: []string{"ops@company.internal"},
			mustContain:    []string{"<email>"},
		},
		{
			in:             "Authorization: Bearer sk_test_abcdefgh1234567890",
			mustNotContain: []string{"sk_test_abcdefgh1234567890"},
			mustContain:    []string{"Bearer <token>"},
		},
		{
			in:             "syt_dXNlcg_LongOpaqueToken123 was leaked",
			mustNotContain: []string{"syt_dXNlcg_LongOpaqueToken123"},
			mustContain:    []string{"<matrix-access-token>"},
		},
		{
			in:             "path /Users/alice/Documents/secret.md changed",
			mustNotContain: []string{"/Users/alice"},
			mustContain:    []string{"/Users/<user>"},
		},
		{
			in:             "host gateway.internal at 10.0.1.5",
			mustNotContain: []string{"gateway.internal", "10.0.1.5"},
			mustContain:    []string{"<private-host>", "<private-ip>"},
		},
	}
	for _, c := range cases {
		got := FreeText(c.in)
		for _, fragment := range c.mustNotContain {
			if strings.Contains(got, fragment) {
				t.Errorf("FreeText(%q) still contains %q: got %q", c.in, fragment, got)
			}
		}
		for _, fragment := range c.mustContain {
			if !strings.Contains(got, fragment) {
				t.Errorf("FreeText(%q) missing %q: got %q", c.in, fragment, got)
			}
		}
	}
}

func TestFreeText_PreservesPublicServices(t *testing.T) {
	in := "calling https://api.openai.com/v1/chat/completions and github.com/foo/bar"
	got := FreeText(in)
	if !strings.Contains(got, "api.openai.com") || !strings.Contains(got, "github.com") {
		t.Errorf("FreeText masked public services: %q → %q", in, got)
	}
}
