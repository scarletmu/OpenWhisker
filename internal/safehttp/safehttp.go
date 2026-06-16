// Package safehttp centralises the SSRF guard used by every outbound HTTP
// caller in OpenWhisker (the scheduler's RSS adapter and the link-clip
// fetcher). It refuses requests whose host resolves to loopback, link-local,
// multicast, RFC1918 / RFC4193 private space, or cloud-metadata ranges — the
// defence against a vault file (feed URL, captured link) pointing the daemon
// at an internal admin endpoint or the cloud IMDS.
//
// The guard lives in one place on purpose: duplicated SSRF logic drifts, and a
// drift here is a vulnerability, not a cosmetic bug.
package safehttp

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AssertPublicIP rejects loopback, link-local, multicast, unspecified, and
// RFC1918 / RFC4193 / cloud-metadata addresses.
func AssertPublicIP(ip net.IP) error {
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
	// IsPrivate() does not cover 169.254.169.254 (link-local, handled above) or
	// 100.64.0.0/10 (RFC6598 carrier-grade NAT), so check the latter explicitly.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return errors.New("address is RFC6598 shared address space")
		}
	}
	return nil
}

// IsReservedHostname returns true for DNS names that map to local/internal
// services we never want to fetch even if the caller forgets to use an IP
// literal.
func IsReservedHostname(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return true
	}
	if host == "localhost" {
		return true
	}
	suffixes := []string{
		".localhost",
		".local",    // mDNS
		".internal", // common internal TLD
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

// AssertRequestHostPublic resolves the request hostname and rejects the request
// if any returned address is non-public. If resolution fails or returns no
// addresses, the underlying transport handles the dial (and any failure that
// follows), so this never adds a false positive.
//
// This narrows but does not fully close DNS rebinding: the transport re-resolves
// the host independently when it dials, so an attacker who returns a public IP
// to this lookup and a private one to the dial's lookup can still slip through
// the gap. Fully closing it would require pinning the address verified here into
// the dialer (a custom DialContext). The current callers fetch low-value
// vault-supplied URLs, so the residual window is accepted rather than closed.
func AssertRequestHostPublic(req *http.Request) error {
	hostname := req.URL.Hostname()
	if hostname == "" {
		return errors.New("safehttp: request has no hostname")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if err := AssertPublicIP(ip); err != nil {
			return fmt.Errorf("safehttp: refusing host %s: %w", hostname, err)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupHost(req.Context(), hostname)
	if err != nil || len(addrs) == 0 {
		return nil
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if err := AssertPublicIP(ip); err != nil {
			return fmt.Errorf("safehttp: %s resolved to %s: %w", hostname, addr, err)
		}
	}
	return nil
}

// ValidatePublicURL enforces that a raw URL is an absolute http/https URL with
// no userinfo and a host that is not an obviously-internal IP literal or
// reserved name. DNS-name hosts are re-resolved and re-checked at dial time by
// the guarded transport, which narrows (but does not fully close — see
// AssertRequestHostPublic) the DNS-rebinding window.
func ValidatePublicURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return errors.New("URL must be absolute")
	}
	if parsed.User != nil {
		return errors.New("URL userinfo is not allowed")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return errors.New("URL scheme must be http or https")
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return errors.New("URL host is required")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if err := AssertPublicIP(ip); err != nil {
			return fmt.Errorf("URL host %s: %w", hostname, err)
		}
	} else if IsReservedHostname(hostname) {
		return fmt.Errorf("URL host %q targets a reserved name", hostname)
	}
	return nil
}

// Transport wraps http.DefaultTransport with the public-host guard applied to
// every dialled request (including those reached via redirect).
type Transport struct{}

func (Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := AssertRequestHostPublic(req); err != nil {
		return nil, err
	}
	return http.DefaultTransport.RoundTrip(req)
}

// Client returns an http.Client that applies the SSRF guard on every request
// and re-validates every redirect target, capping redirect depth at 5.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: Transport{},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return ValidatePublicURL(req.URL.String())
		},
	}
}
