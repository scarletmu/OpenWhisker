# Safehttp Package Index

This package is the single SSRF guard for every outbound HTTP caller in
OpenWhisker. Duplicated SSRF logic drifts, and a drift here is a vulnerability —
so the IP/hostname checks and the guarded client live here only.

Surface:

- `AssertPublicIP(ip)`: rejects loopback, link-local, multicast, unspecified,
  RFC1918 / RFC4193 private, RFC6598 CGNAT, and cloud-metadata addresses.
- `IsReservedHostname(host)`: rejects `localhost` and internal TLD suffixes.
- `ValidatePublicURL(url)`: absolute http/https, no userinfo, non-reserved host.
- `AssertRequestHostPublic(req)`: resolves the host at dial time and re-applies
  the guard, narrowing the DNS-rebinding window (the transport re-resolves
  independently when it dials, so the gap is narrowed, not fully closed).
- `Transport` / `Client(timeout)`: an `http.Client` that guards every dialled
  request and re-validates every redirect target (depth-capped).

Consumers:

- `internal/scheduler` RSS adapter (`validateFeedURL` + `newSafeRSSClient`).
- `internal/clip` link fetcher.

Keep this package dependency-light (stdlib only) and defensive-by-default.
