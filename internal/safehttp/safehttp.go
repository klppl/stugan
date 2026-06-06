// Package safehttp provides an HTTP client hardened against SSRF: it resolves
// the target host and refuses to connect to private, loopback, link-local, or
// otherwise non-globally-routable addresses, bounds the time, and caps
// redirects. It is shared by the link-preview / image-proxy fetchers and the
// Lua plugin http binding so the guard lives in exactly one place.
package safehttp

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// New returns an HTTP client safe for fetching user- or plugin-supplied URLs.
// Each caller gets its own client; they are cheap.
func New() *http.Client {
	return &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
		Transport: &http.Transport{
			DialContext: guardedDial,
			// Keep connections short-lived; this isn't a hot path.
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
		},
	}
}

// guardedDial resolves the host and rejects any non-public IP before
// connecting, so a crafted URL cannot reach internal services.
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	d.Timeout = 5 * time.Second
	for _, ip := range ips {
		if isPublicIP(ip.IP) {
			return d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		}
	}
	return nil, fmt.Errorf("refusing to connect to non-public address %q", host)
}

// reservedRanges are non-globally-routable CIDRs that the standard
// net.IP predicates (IsPrivate/IsLoopback/IsLinkLocal*/…) do NOT cover, so
// they would otherwise slip past the SSRF guard:
//   - 100.64.0.0/10  RFC 6598 carrier-grade NAT (common internal range)
//   - 192.0.0.0/24   RFC 6890 IETF protocol assignments
//   - 198.18.0.0/15  RFC 2544 benchmarking
//   - 240.0.0.0/4    RFC 1112 reserved/future, incl. 255.255.255.255 broadcast
var reservedRanges = func() []*net.IPNet {
	var out []*net.IPNet
	for _, c := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4"} {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// isPublicIP reports whether ip is a globally routable address.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, n := range reservedRanges {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}
