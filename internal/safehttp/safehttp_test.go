package safehttp

import (
	"net"
	"testing"
)

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true}, // public IPv6
		{"127.0.0.1", false},           // loopback
		{"::1", false},                 // loopback v6
		{"10.0.0.1", false},            // private
		{"192.168.1.1", false},         // private
		{"172.16.0.1", false},          // private
		{"169.254.0.1", false},         // link-local
		{"0.0.0.0", false},             // unspecified
		{"100.64.0.1", false},          // RFC 6598 CGNAT
		{"192.0.0.1", false},           // RFC 6890
		{"198.18.0.1", false},          // RFC 2544 benchmarking
		{"240.0.0.1", false},           // reserved
		{"255.255.255.255", false},     // broadcast
		{"224.0.0.1", false},           // multicast
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		if got := isPublicIP(ip); got != c.want {
			t.Errorf("isPublicIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestNewClientConfigured(t *testing.T) {
	c := New()
	if c.Timeout == 0 {
		t.Error("client has no timeout")
	}
	if c.CheckRedirect == nil {
		t.Error("client has no redirect cap")
	}
}
