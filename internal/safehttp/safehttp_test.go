package safehttp

import (
	"net"
	"testing"
)

func TestAssertPublicIPRejectsInternalRanges(t *testing.T) {
	rejected := []string{
		"127.0.0.1",
		"::1",
		"0.0.0.0",
		"10.1.2.3",
		"192.168.1.1",
		"172.16.5.4",
		"169.254.169.254", // cloud IMDS (link-local)
		"100.64.0.1",      // RFC6598 CGNAT
		"fd00::1",         // RFC4193 ULA
		"224.0.0.1",       // multicast
	}
	for _, s := range rejected {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if err := AssertPublicIP(ip); err == nil {
			t.Errorf("AssertPublicIP(%s) = nil, want rejection", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"} {
		if err := AssertPublicIP(net.ParseIP(s)); err != nil {
			t.Errorf("AssertPublicIP(%s) = %v, want nil", s, err)
		}
	}
}

func TestIsReservedHostname(t *testing.T) {
	for _, h := range []string{"localhost", "foo.local", "db.internal", "x.corp", "host.lan", ""} {
		if !IsReservedHostname(h) {
			t.Errorf("IsReservedHostname(%q) = false, want true", h)
		}
	}
	for _, h := range []string{"example.com", "raft.example.org", "api.deepseek.com"} {
		if IsReservedHostname(h) {
			t.Errorf("IsReservedHostname(%q) = true, want false", h)
		}
	}
}

func TestValidatePublicURL(t *testing.T) {
	bad := []string{
		"",
		"not a url",
		"ftp://example.com/x",
		"http://localhost/x",
		"http://127.0.0.1/x",
		"http://169.254.169.254/meta",
		"http://user:pass@example.com/x", // userinfo
		"http://10.0.0.1/admin",
	}
	for _, u := range bad {
		if err := ValidatePublicURL(u); err == nil {
			t.Errorf("ValidatePublicURL(%q) = nil, want rejection", u)
		}
	}
	for _, u := range []string{"https://example.com/a", "http://example.org:8080/b?q=1"} {
		if err := ValidatePublicURL(u); err != nil {
			t.Errorf("ValidatePublicURL(%q) = %v, want nil", u, err)
		}
	}
}
