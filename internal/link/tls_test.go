package link

// Untested: under GR-3 nothing here has been run. No socket is opened and no handshake
// is made: VerifyConnection is called directly with a hand-built connection state.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/gateway"
)

// testCertificate builds a throwaway self-signed certificate.
func testCertificate(t *testing.T) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "ratatoskr-gateway"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return parsed
}

// TestPinnedTLSConfigMatches: the pinned certificate passes, a different one fails with
// errPinMismatch, and no peer certificate fails too.
func TestPinnedTLSConfigMatches(t *testing.T) {
	leaf := testCertificate(t)
	pinned := sha256.Sum256(leaf.Raw)
	fingerprint := gateway.Fingerprint(leaf.Raw)

	cfg, err := pinnedTLSConfig(fingerprint)
	if err != nil {
		t.Fatalf("pinnedTLSConfig: %v", err)
	}
	if cfg.VerifyConnection == nil {
		t.Fatal("the config has no pin check")
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("the system-roots check is still on, so the pin is not the trust")
	}

	matching := tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	if err := cfg.VerifyConnection(matching); err != nil {
		t.Fatalf("the pinned certificate was refused: %v", err)
	}

	other := testCertificate(t)
	mismatch := tls.ConnectionState{PeerCertificates: []*x509.Certificate{other}}
	if err := cfg.VerifyConnection(mismatch); !errors.Is(err, errPinMismatch) {
		t.Fatalf("a different certificate gave %v, want errPinMismatch", err)
	}

	if err := cfg.VerifyConnection(tls.ConnectionState{}); !errors.Is(err, errPinMismatch) {
		t.Fatalf("a connection with no certificate gave %v, want errPinMismatch", err)
	}

	// The same leaf's fingerprint is what the gateway prints: one rule, two packages.
	if got := gateway.Fingerprint(leaf.Raw); got != fingerprint {
		t.Fatalf("fingerprint = %q, want %q", got, fingerprint)
	}
	if sum := sha256.Sum256(leaf.Raw); sum != pinned {
		t.Fatal("the pinned bytes are not the leaf's digest")
	}
}

// TestPinnedTLSConfigRefusesABadFingerprint: an empty, malformed, uppercase or zero
// fingerprint yields no config at all - fail closed, never a config to fall back on.
func TestPinnedTLSConfigRefusesABadFingerprint(t *testing.T) {
	zeroes := "sha256:" + strings.Repeat("0", 64)

	bad := map[string]string{
		"empty":         "",
		"no prefix":     strings.Repeat("a", 64),
		"short":         "sha256:abc",
		"uppercase":     "sha256:" + strings.Repeat("A", 64),
		"not hex":       "sha256:" + strings.Repeat("z", 64),
		"all zeroes":    zeroes,
		"trailing text": "sha256:" + strings.Repeat("a", 64) + " ",
	}
	for name, fingerprint := range bad {
		t.Run(name, func(t *testing.T) {
			cfg, err := pinnedTLSConfig(fingerprint)
			if err == nil {
				t.Fatalf("pinnedTLSConfig accepted %q", fingerprint)
			}
			if cfg != nil {
				t.Fatalf("pinnedTLSConfig returned a config for %q", fingerprint)
			}
		})
	}
}

// TestPinnedTLSConfigShape: TLS 1.3 minimum, http/1.1 only, no client auth.
func TestPinnedTLSConfigShape(t *testing.T) {
	leaf := testCertificate(t)
	cfg, err := pinnedTLSConfig(gateway.Fingerprint(leaf.Raw))
	if err != nil {
		t.Fatalf("pinnedTLSConfig: %v", err)
	}

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Fatalf("NextProtos = %v, want only http/1.1", cfg.NextProtos)
	}
	if len(cfg.Certificates) != 0 {
		t.Fatalf("the client offers %d certificates, want none", len(cfg.Certificates))
	}
	if cfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("ClientAuth = %v", cfg.ClientAuth)
	}
	if cfg.RootCAs != nil {
		t.Fatal("the client consults system roots, which the pin replaces")
	}
	if cfg.ServerName != "" {
		t.Fatalf("ServerName = %q, want no hostname check", cfg.ServerName)
	}
}

// TestPinMismatchTextIsSafe: the sentinel names no address, fingerprint or certificate.
func TestPinMismatchTextIsSafe(t *testing.T) {
	text := errPinMismatch.Error()
	for _, forbidden := range []string{"sha256:", "CERTIFICATE", "https://", "100.", "192.168"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("errPinMismatch text %q contains %q", text, forbidden)
		}
	}
}
