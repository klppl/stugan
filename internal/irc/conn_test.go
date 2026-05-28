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
