package server

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAuthRateLimitBoundsIPState(t *testing.T) {
	r := newAuthRateLimit(time.Minute, 3, 4)
	for i := range 100 {
		r.fail(fmt.Sprintf("192.0.2.%d", i))
	}
	if got := len(r.hits); got != 4 {
		t.Fatalf("tracked IPs = %d, want hard cap 4", got)
	}

	// Stale buckets are pruned before an active bucket must be evicted.
	stale := time.Now().Add(-2 * r.window)
	for ip := range r.hits {
		r.hits[ip] = []time.Time{stale}
	}
	r.fail("198.51.100.1")
	if got := len(r.hits); got != 1 {
		t.Fatalf("tracked IPs after stale sweep = %d, want 1", got)
	}
}

// TestClientIPTrustedProxy verifies that X-Forwarded-For is honoured only when
// the direct peer is a configured trusted proxy, and that the real client is
// recovered as the right-most non-proxy entry (so chained proxies are skipped).
func TestClientIPTrustedProxy(t *testing.T) {
	srv := New(SingleUser(&Tenant{}), Options{
		TrustedProxies: []string{"127.0.0.1/32", "10.0.0.0/8"},
	})

	tests := []struct {
		name       string
		remoteAddr string
		xff        string
		want       string
	}{
		{"untrusted peer ignores xff", "203.0.113.9:5000", "1.2.3.4", "203.0.113.9"},
		{"trusted peer, no xff", "127.0.0.1:5000", "", "127.0.0.1"},
		{"trusted peer uses xff", "127.0.0.1:5000", "198.51.100.7", "198.51.100.7"},
		{"chained proxies skipped", "127.0.0.1:5000", "198.51.100.7, 10.1.2.3", "198.51.100.7"},
		{"all-trusted falls back to peer", "127.0.0.1:5000", "10.1.2.3, 10.4.5.6", "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/login", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := srv.clientIP(r); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClientIPCloudflare verifies CF-Connecting-IP (the Cloudflare Tunnel
// case: peer is localhost cloudflared) is preferred over X-Forwarded-For and
// is ignored from an untrusted peer.
func TestClientIPCloudflare(t *testing.T) {
	srv := New(SingleUser(&Tenant{}), Options{TrustedProxies: []string{"127.0.0.1/32"}})
	tests := []struct {
		name       string
		remoteAddr string
		cf         string
		xff        string
		want       string
	}{
		{"cf preferred over xff", "127.0.0.1:5000", "198.51.100.7", "1.2.3.4", "198.51.100.7"},
		{"cf alone", "127.0.0.1:5000", "198.51.100.7", "", "198.51.100.7"},
		{"untrusted peer ignores cf", "203.0.113.9:5000", "198.51.100.7", "", "203.0.113.9"},
		{"bogus cf falls back to xff", "127.0.0.1:5000", "not-an-ip", "198.51.100.7", "198.51.100.7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/login", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.cf != "" {
				r.Header.Set("CF-Connecting-IP", tt.cf)
			}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := srv.clientIP(r); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClientIPNoTrustedProxies confirms that with no configured proxies the
// forwarded header is never consulted — the direct peer is always the key.
func TestClientIPNoTrustedProxies(t *testing.T) {
	srv := New(SingleUser(&Tenant{}), Options{})
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := srv.clientIP(r); got != "127.0.0.1" {
		t.Errorf("clientIP = %q, want 127.0.0.1 (xff must be ignored)", got)
	}
}
