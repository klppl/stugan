package irc

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

// selfSignedPEM returns a cert+key concatenated PEM bundle, the form stugan
// stores in NetworkParams.CertPEM for CertFP / SASL EXTERNAL.
func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "stugan-test"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	_ = pem.Encode(&b, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	_ = pem.Encode(&b, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return b.String()
}

func TestNewClientCert(t *testing.T) {
	// A valid cert+key bundle builds a connection (TLS client cert + EXTERNAL).
	c, err := New(Options{
		Network: "n", Addr: "irc.example.org:6697", TLS: true, Nick: "me",
		SASLExternal: true, CertPEM: selfSignedPEM(t),
	}, nil)
	if err != nil {
		t.Fatalf("New with valid cert: %v", err)
	}
	if c == nil {
		t.Fatal("New returned nil conn")
	}

	// A malformed bundle is rejected up front rather than failing at dial.
	if _, err := New(Options{
		Network: "n", Addr: "irc.example.org:6697", TLS: true, Nick: "me",
		CertPEM: "-----BEGIN CERTIFICATE-----\nnot base64\n-----END CERTIFICATE-----\n",
	}, nil); err == nil {
		t.Fatal("expected error for malformed client certificate")
	}
}

func TestFallbackAddrRotation(t *testing.T) {
	// New builds the address list primary-first, dropping blank fallbacks.
	c, err := New(Options{
		Network: "n", Addr: "a:6667", Nick: "me",
		Fallbacks: []string{"b:6667", "  ", "c:6667"},
	}, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := []string{"a:6667", "b:6667", "c:6667"}
	if len(c.addrs) != len(want) {
		t.Fatalf("addrs = %v, want %v", c.addrs, want)
	}
	for i, a := range want {
		if c.addrs[i] != a {
			t.Fatalf("addrs[%d] = %q, want %q", i, c.addrs[i], a)
		}
	}

	// advanceAddr walks the list and wraps back to the primary.
	for _, wantIdx := range []int{1, 2, 0, 1} {
		c.advanceAddr()
		if c.addrIdx != wantIdx {
			t.Fatalf("after advance, addrIdx = %d, want %d", c.addrIdx, wantIdx)
		}
	}

	// A single-server network never rotates.
	solo, err := New(Options{Network: "n", Addr: "only:6667", Nick: "me"}, nil)
	if err != nil {
		t.Fatalf("New solo: %v", err)
	}
	solo.advanceAddr()
	if solo.addrIdx != 0 {
		t.Fatalf("solo addrIdx = %d, want 0", solo.addrIdx)
	}
}

func TestPlanAutojoin(t *testing.T) {
	channels := []string{"#open", "#secret", "#also-open", "#vip"}
	keys := map[string]string{"#secret": "hunter2", "#vip": "swordfish"}

	keyed, keyless := planAutojoin(channels, keys)

	wantKeyed := map[string]string{"#secret": "hunter2", "#vip": "swordfish"}
	if len(keyed) != len(wantKeyed) {
		t.Fatalf("keyed = %v, want %d entries", keyed, len(wantKeyed))
	}
	for _, k := range keyed {
		if wantKeyed[k.channel] != k.key {
			t.Errorf("keyed %q = %q, want %q", k.channel, k.key, wantKeyed[k.channel])
		}
	}
	want := []string{"#open", "#also-open"}
	if strings.Join(keyless, ",") != strings.Join(want, ",") {
		t.Errorf("keyless = %v, want %v", keyless, want)
	}

	// No keys at all → everything batches as keyless, nothing keyed.
	keyed, keyless = planAutojoin(channels, nil)
	if len(keyed) != 0 || len(keyless) != len(channels) {
		t.Errorf("nil keys: keyed=%v keyless=%v", keyed, keyless)
	}
}
