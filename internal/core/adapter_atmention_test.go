package core

import (
	"testing"
)

func TestParseAtSkillPrefix_HappyPath(t *testing.T) {
	id, q, ok := parseAtSkillPrefix("@vault-qa what's in my notes about TLS?")
	if !ok {
		t.Fatalf("expected ok=true")
	}
	if id != "vault-qa" {
		t.Errorf("id = %q, want vault-qa", id)
	}
	if q != "what's in my notes about TLS?" {
		t.Errorf("query = %q", q)
	}
}

func TestParseAtSkillPrefix_BareMention(t *testing.T) {
	id, q, ok := parseAtSkillPrefix("@vault-qa")
	if !ok {
		t.Fatalf("expected bare @-mention to parse")
	}
	if id != "vault-qa" || q != "" {
		t.Errorf("id=%q q=%q", id, q)
	}
}

func TestParseAtSkillPrefix_NewlineDelimiter(t *testing.T) {
	id, q, ok := parseAtSkillPrefix("@daily-radar\nfollow up question line")
	if !ok || id != "daily-radar" || q != "follow up question line" {
		t.Errorf("got id=%q q=%q ok=%v", id, q, ok)
	}
}

func TestParseAtSkillPrefix_RejectsMatrixUserMention(t *testing.T) {
	cases := []string{
		"@alice:matrix.example.com hi",
		"@user:server hello",
		"@:colon-only",
	}
	for _, c := range cases {
		if _, _, ok := parseAtSkillPrefix(c); ok {
			t.Errorf("expected %q to NOT match (looks like Matrix mention)", c)
		}
	}
}

func TestParseAtSkillPrefix_RejectsInvalidChars(t *testing.T) {
	for _, c := range []string{"@Vault-QA hi", "@vault.qa hi", "@vault qa hi"} {
		if _, _, ok := parseAtSkillPrefix(c); ok && c != "@vault qa hi" {
			t.Errorf("expected %q to be rejected", c)
		}
	}
}

func TestParseAtSkillPrefix_NotPrefixed(t *testing.T) {
	if _, _, ok := parseAtSkillPrefix("normal message"); ok {
		t.Errorf("expected no @-mention match")
	}
	if _, _, ok := parseAtSkillPrefix(""); ok {
		t.Errorf("expected empty to not match")
	}
}
