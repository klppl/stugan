package server

import (
	"net"
	"net/http"
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
// X-Forwarded-For is intentionally ignored — stugan is normally
// reverse-proxied locally and trusting that header would let a bot
// pick a unique key per request. Operators behind a real proxy should
// also enforce rate limits at that layer.
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

// clientIP returns the request's source IP, stripped of any port. It
// reads only the direct peer (r.RemoteAddr); see authRateLimit's doc
// comment for why X-Forwarded-For is not honoured here.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
