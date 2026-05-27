package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// safeClient is an HTTP client for fetching user-supplied URLs (link
// previews, the image proxy). It refuses to connect to private, loopback,
// or link-local addresses to blunt SSRF, bounds the time, and caps
// redirects.
var safeClient = &http.Client{
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

// isPublicIP reports whether ip is a globally routable address.
func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	return true
}
