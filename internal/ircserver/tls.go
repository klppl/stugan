package ircserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TLSResult holds the configured TLS config, certificate fingerprint, and origin source.
type TLSResult struct {
	Config      *tls.Config
	Fingerprint string
	Source      string // "configured" or "self-signed"
}

// CertFingerprint returns the uppercase colon-separated SHA-256 fingerprint of a DER certificate.
func CertFingerprint(der []byte) string {
	h := sha256.Sum256(der)
	var hexParts []string
	for _, b := range h {
		hexParts = append(hexParts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(hexParts, ":")
}

// SetupTLS configures TLS for the IRC server.
// If certFile and keyFile are provided, it loads them from disk.
// Otherwise, it generates and persists a self-signed ECDSA certificate in dataDir.
func SetupTLS(certFile, keyFile, dataDir string) (*TLSResult, error) {
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load tls cert pair: %w", err)
		}
		var fp string
		if len(cert.Certificate) > 0 {
			fp = CertFingerprint(cert.Certificate[0])
		}
		return &TLSResult{
			Config:      &tls.Config{Certificates: []tls.Certificate{cert}},
			Fingerprint: fp,
			Source:      "configured",
		}, nil
	}

	// Auto-generate or load self-signed certificate in dataDir
	autoCertPath := ""
	autoKeyPath := ""
	if dataDir != "" {
		autoCertPath = filepath.Join(dataDir, "bouncer_cert.pem")
		autoKeyPath = filepath.Join(dataDir, "bouncer_key.pem")

		if _, err1 := os.Stat(autoCertPath); err1 == nil {
			if _, err2 := os.Stat(autoKeyPath); err2 == nil {
				cert, err := tls.LoadX509KeyPair(autoCertPath, autoKeyPath)
				if err == nil && len(cert.Certificate) > 0 {
					return &TLSResult{
						Config:      &tls.Config{Certificates: []tls.Certificate{cert}},
						Fingerprint: CertFingerprint(cert.Certificate[0]),
						Source:      "self-signed",
					}, nil
				}
			}
		}
	}

	// Generate fresh ECDSA P-256 self-signed certificate
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ecdsa key: %w", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		return nil, fmt.Errorf("generate serial number: %w", err)
	}

	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"stugan"},
			CommonName:   "stugan bouncer",
		},
		NotBefore:             now.Add(-1 * time.Hour),
		NotAfter:              now.AddDate(10, 0, 0), // 10 years
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal ec private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	if autoCertPath != "" && autoKeyPath != "" {
		_ = os.WriteFile(autoCertPath, certPEM, 0o644)
		_ = os.WriteFile(autoKeyPath, keyPEM, 0o600)
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse generated keypair: %w", err)
	}

	return &TLSResult{
		Config:      &tls.Config{Certificates: []tls.Certificate{cert}},
		Fingerprint: CertFingerprint(derBytes),
		Source:      "self-signed",
	}, nil
}
