package gateway

// The gateway's own TLS identity: one self-signed certificate per gateway, pinned by
// its runtimes. No CA, no ACME, no hostname checks, no rotation command.
//
// The private key never leaves <gateway dir>/tls: it is not in the database, not in a
// log, and not in an error text. Every error below names a path and never key bytes.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	certName = "cert.pem"
	keyName  = "key.pem"

	// certValidity and certSkew are the contract's "10 years from now minus one
	// hour". A pinned certificate is never checked for expiry, but a sane validity
	// keeps the file usable by ordinary tools.
	certValidity = 10 * 365 * 24 * time.Hour
	certSkew     = time.Hour

	// serialBits is the contract's 128-bit random serial.
	serialBits = 128

	// identityFileLimit bounds what we will read from the two files so a runaway
	// file cannot exhaust memory. A real certificate is a few KB.
	identityFileLimit = 1 << 20
)

// LoadOrCreateIdentity returns the gateway's certificate and its fingerprint.
//
// dir is <gateway dir>/tls. It is created 0700 with the same symlink and mode rules
// as the rest of the gateway directory. Both files present: they are read and must be
// usable. Exactly one present: refused, naming what is missing, and nothing is
// changed. Neither present: a new identity is generated and written key-first.
//
// The identity is never regenerated automatically: a replaced certificate would make
// every paired runtime refuse the gateway until it re-pairs.
//
// UNTESTED.
func LoadOrCreateIdentity(dir string) (tls.Certificate, string, error) {
	if err := prepareOwnedDir(dir); err != nil {
		return tls.Certificate{}, "", err
	}

	certPath := filepath.Join(dir, certName)
	keyPath := filepath.Join(dir, keyName)

	certPresent, err := regularFilePresent(certPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyPresent, err := regularFilePresent(keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	switch {
	case certPresent && keyPresent:
		return loadIdentity(certPath, keyPath)
	case certPresent:
		return tls.Certificate{}, "", fmt.Errorf(
			"gateway: %s exists but %s is missing: nothing was changed; restore the key from a backup, or remove %s to generate a new identity (every paired runtime must then pair again)",
			certPath, keyPath, certPath,
		)
	case keyPresent:
		return tls.Certificate{}, "", fmt.Errorf(
			"gateway: %s exists but %s is missing: nothing was changed; remove %s to generate a new identity (every paired runtime must then pair again)",
			keyPath, certPath, keyPath,
		)
	}
	return generateIdentity(certPath, keyPath)
}

// regularFilePresent reports whether path exists, refusing a symlink or a
// non-regular file. A missing file is not an error.
func regularFilePresent(path string) (bool, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("gateway: stat %s: %w", path, err)
	case info.Mode()&os.ModeSymlink != 0:
		return false, fmt.Errorf("gateway: %s must be a regular file, not a symlink", path)
	case !info.Mode().IsRegular():
		return false, fmt.Errorf("gateway: %s must be a regular file", path)
	}
	return true, nil
}

// loadIdentity reads an existing identity.
//
// The key must not be readable by group or other; the certificate is public, so it is
// only required to be a real regular file. A mode is never repaired silently: a mode
// the user did not ask for is reported.
//
// UNTESTED.
func loadIdentity(certPath, keyPath string) (tls.Certificate, string, error) {
	keyInfo, err := os.Lstat(keyPath)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: stat %s: %w", keyPath, err)
	}
	if perm := keyInfo.Mode().Perm(); perm&0o077 != 0 {
		return tls.Certificate{}, "", fmt.Errorf(
			"gateway: %s is readable by group or other (mode %04o): run chmod 600 %s",
			keyPath, perm, keyPath,
		)
	}

	certPEM, err := readSmallFile(certPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyPEM, err := readSmallFile(keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		// The parser's own error names no bytes, and neither does ours.
		return tls.Certificate{}, "", fmt.Errorf("gateway: %s and %s are not a usable certificate and key: %w", certPath, keyPath, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: parse %s: %w", certPath, err)
	}
	pair.Leaf = leaf
	return pair, Fingerprint(leaf.Raw), nil
}

// readSmallFile reads a small file, refusing an oversized one.
func readSmallFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("gateway: stat %s: %w", path, err)
	}
	if info.Size() > identityFileLimit {
		return nil, fmt.Errorf("gateway: %s is too large to be a certificate or key", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("gateway: read %s: %w", path, err)
	}
	return data, nil
}

// generateIdentity creates the gateway's identity and writes it key-first.
//
// UNTESTED.
func generateIdentity(certPath, keyPath string) (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: generate key: %w", err)
	}

	serial := make([]byte, serialBits/8)
	if _, err := rand.Read(serial); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: new(big.Int).SetBytes(serial),
		Subject:      pkix.Name{CommonName: "ratatoskr-gateway"},
		NotBefore:    now.Add(-certSkew),
		NotAfter:     now.Add(certValidity),
		// Digital signature only, and server auth only: this certificate signs
		// nothing and identifies nothing else.
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: encode key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	// Key first, then certificate. An interruption between the two leaves a key with no
	// certificate, which the next start refuses by name (see LoadOrCreateIdentity)
	// instead of ever serving a half identity.
	if err := writePrivateFile(keyPath, keyPEM); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := writePrivateFile(certPath, certPEM); err != nil {
		return tls.Certificate{}, "", err
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("gateway: parse %s: %w", certPath, err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, Fingerprint(der), nil
}

// writePrivateFile writes data to path through an exclusively created 0600 temp file
// in the same directory, synced, closed, then renamed.
//
// UNTESTED.
func writePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)

	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("gateway: %s must be a regular file, not a symlink", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("gateway: stat %s: %w", path, err)
	}

	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("gateway: create a temporary file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}

	// CreateTemp creates 0600 already; the mode is set again so a modified umask
	// cannot widen it.
	if err := temp.Chmod(fileMode); err != nil {
		cleanup()
		return fmt.Errorf("gateway: chmod %s: %w", tempName, err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("gateway: write %s: %w", tempName, err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("gateway: sync %s: %w", tempName, err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("gateway: close %s: %w", tempName, err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("gateway: rename %s: %w", tempName, err)
	}
	return nil
}

// serverTLSConfig is the gateway's TLS configuration: TLS 1.3 and HTTP/1.1 only, with
// no client authentication.
//
// HTTP/2 is not offered in ALPN, and the http.Server's TLSNextProto is empty as well,
// so the stream behaves exactly as it does on the tailnet.
//
// UNTESTED.
func serverTLSConfig(cert tls.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"http/1.1"},
		Certificates: []tls.Certificate{cert},
	}
}
