package server

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// authRateLimit is a tiny in-process sliding-window limiter for the
// password-accepting endpoints (/api/login and /api/magicword). It
// exists to slow down credential-stuffing bots — not to be a CDN.
//
// Tracking is per source IP: failed attempts are stamped on `fail`,
// older entries are pruned on every read, and `allow` reports whether
// the count for an IP is still under `max` within `window`. Successful
// attempts deliberately don't consume budget, so a user typo'ing once
// won't be locked out.
//
// X-Forwarded-For is honoured only when the direct peer is a configured
// trusted proxy (server.trusted_proxies); from any other peer the header is
// ignored, so a bot facing the daemon directly cannot pick a unique key per
// request. See Server.clientIP.
type authRateLimit struct {
	mu     sync.Mutex
	hits   map[string][]time.Time
	window time.Duration
	max    int
}

func newAuthRateLimit(window time.Duration, max int) *authRateLimit {
	return &authRateLimit{hits: map[string][]time.Time{}, window: window, max: max}
}

// allow reports whether another attempt from ip is permitted right now.
func (r *authRateLimit) allow(ip string) bool {
	if r == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(ip, time.Now().Add(-r.window))
	return len(r.hits[ip]) < r.max
}

// fail records one failed attempt against ip.
func (r *authRateLimit) fail(ip string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.pruneLocked(ip, now.Add(-r.window))
	r.hits[ip] = append(r.hits[ip], now)
}

func (r *authRateLimit) pruneLocked(ip string, cutoff time.Time) {
	a := r.hits[ip]
	i := 0
	for i < len(a) && a[i].Before(cutoff) {
		i++
	}
	if i == len(a) {
		delete(r.hits, ip)
		return
	}
	if i > 0 {
		r.hits[ip] = a[i:]
	}
}

// clientIP returns the request's source IP for rate-limit keying. By default
// it is the direct peer (r.RemoteAddr), stripped of any port. When that peer
// is a configured trusted proxy, the real client is recovered from
// X-Forwarded-For: the right-most entry that is not itself a trusted proxy
// (so chained proxies are walked past). This keeps per-client keying behind a
// reverse proxy — without it every request collapses onto the proxy's address
// and a handful of failures would lock the auth endpoints out for everyone —
// while never trusting a header an arbitrary, untrusted client could forge.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.trustedProxy(host) {
		return host
	}
	// The peer is a trusted proxy, so its forwarded client-IP headers are
	// authoritative. Cloudflare's CF-Connecting-IP is a single, unambiguous
	// visitor address (and the only header cloudflared is guaranteed to set);
	// prefer it, then fall back to the right-most non-proxy X-Forwarded-For
	// entry, skipping any chained proxies.
	if cf := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); cf != "" && net.ParseIP(cf) != nil {
		return cf
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		if ip := strings.TrimSpace(parts[i]); ip != "" && !s.trustedProxy(ip) {
			return ip
		}
	}
	return host
}

// trustedProxy reports whether ip (a bare host, no port) falls within one of
// the configured trusted-proxy ranges.
func (s *Server) trustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, n := range s.trustedProxies {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// parseTrustedProxies parses CIDR strings (or bare IPs, treated as a single
// host) into networks, silently skipping malformed entries.
func parseTrustedProxies(cidrs []string) []*net.IPNet {
	var out []*net.IPNet
	for _, c := range cidrs {
		if !strings.Contains(c, "/") {
			if ip := net.ParseIP(c); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				c = fmt.Sprintf("%s/%d", c, bits)
			}
		}
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}
