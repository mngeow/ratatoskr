package gateway

// Untested: under GR-3 nothing here has been run. These tests create real files in a
// temporary directory and read them back; they bind no socket and use no network.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tlsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir
}

// TestLoadOrCreateIdentityCreatesThenReloads: the second call reuses the files, so the
// fingerprint a runtime pinned keeps working.
func TestLoadOrCreateIdentityCreatesThenReloads(t *testing.T) {
	dir := tlsDir(t)

	cert, fingerprint, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("first LoadOrCreateIdentity: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if cert.Leaf == nil {
		t.Fatal("the created certificate has no parsed leaf")
	}
	if got := Fingerprint(cert.Leaf.Raw); got != fingerprint {
		t.Fatalf("fingerprint = %q, want the leaf's %q", fingerprint, got)
	}

	// The files are where the contract says, with the modes it says.
	for _, name := range []string{certName, keyName} {
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%s has mode %04o, want 0600", name, perm)
		}
	}
	if info, err := os.Lstat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the tls directory is not 0700: %v %v", info, err)
	}

	_, again, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("second LoadOrCreateIdentity: %v", err)
	}
	if again != fingerprint {
		t.Fatalf("reload gave %q, want the original %q", again, fingerprint)
	}
}

// TestCreatedCertificateShape: P-256, self-signed, server-auth only, and the validity
// the contract asks for.
func TestCreatedCertificateShape(t *testing.T) {
	dir := tlsDir(t)
	cert, _, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}

	leaf := cert.Leaf
	if leaf.Subject.CommonName != "ratatoskr-gateway" {
		t.Fatalf("CN = %q", leaf.Subject.CommonName)
	}
	if leaf.SerialNumber == nil || leaf.SerialNumber.Sign() == 0 {
		t.Fatal("the certificate has no serial number")
	}
	if leaf.KeyUsage != x509.KeyUsageDigitalSignature {
		t.Fatalf("key usage = %v", leaf.KeyUsage)
	}
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("extended key usage = %v", leaf.ExtKeyUsage)
	}
	if err := leaf.CheckSignatureFrom(leaf); err != nil {
		t.Fatalf("the certificate is not self-signed: %v", err)
	}

	key, ok := cert.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("private key is %T, want ECDSA", cert.PrivateKey)
	}
	if key.Curve != elliptic.P256() {
		t.Fatalf("curve = %v, want P-256", key.Curve)
	}

	// Ten years minus an hour, within a generous window because the clock moves.
	validity := leaf.NotAfter.Sub(leaf.NotBefore)
	if validity < certValidity {
		t.Fatalf("validity = %v, want at least %v", validity, certValidity)
	}
	if skew := leaf.NotBefore.Sub(leaf.NotAfter.Add(certValidity)); skew > 2*60*1e9 {
		t.Fatalf("the certificate's start is not an hour before now: %v", leaf.NotBefore)
	}
}

// TestServerTLSConfig: TLS 1.3 minimum, http/1.1 only, no client auth.
func TestServerTLSConfig(t *testing.T) {
	cert, _, err := LoadOrCreateIdentity(tlsDir(t))
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}

	cfg := serverTLSConfig(cert)
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}
	if len(cfg.NextProtos) != 1 || cfg.NextProtos[0] != "http/1.1" {
		t.Fatalf("NextProtos = %v, want only http/1.1", cfg.NextProtos)
	}
	if cfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("ClientAuth = %v, want no client certificate", cfg.ClientAuth)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want exactly one", len(cfg.Certificates))
	}
}

// TestLoadOrCreateIdentityRefusesOneFile: half an identity is a refusal that changes
// nothing, in both directions.
func TestLoadOrCreateIdentityRefusesOneFile(t *testing.T) {
	t.Run("certificate without key", func(t *testing.T) {
		dir := tlsDir(t)
		if _, _, err := LoadOrCreateIdentity(dir); err != nil {
			t.Fatalf("creating the identity: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, keyName)); err != nil {
			t.Fatalf("remove the key: %v", err)
		}
		_, _, err := LoadOrCreateIdentity(dir)
		if err == nil {
			t.Fatal("LoadOrCreateIdentity accepted a certificate without its key")
		}
		if !strings.Contains(err.Error(), keyName) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("error = %q, want it to name the missing key and say nothing changed", err)
		}
	})

	t.Run("key without certificate", func(t *testing.T) {
		dir := tlsDir(t)
		if _, _, err := LoadOrCreateIdentity(dir); err != nil {
			t.Fatalf("creating the identity: %v", err)
		}
		if err := os.Remove(filepath.Join(dir, certName)); err != nil {
			t.Fatalf("remove the certificate: %v", err)
		}
		_, _, err := LoadOrCreateIdentity(dir)
		if err == nil {
			t.Fatal("LoadOrCreateIdentity accepted a key without its certificate")
		}
		if !strings.Contains(err.Error(), certName) || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("error = %q, want it to name the missing certificate and say nothing changed", err)
		}
	})
}

// TestLoadOrCreateIdentityRefusesWideKeyMode: a key another user can read is refused,
// and the message says how to fix it.
func TestLoadOrCreateIdentityRefusesWideKeyMode(t *testing.T) {
	dir := tlsDir(t)
	if _, _, err := LoadOrCreateIdentity(dir); err != nil {
		t.Fatalf("creating the identity: %v", err)
	}
	keyPath := filepath.Join(dir, keyName)
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, _, err := LoadOrCreateIdentity(dir)
	if err == nil {
		t.Fatal("LoadOrCreateIdentity accepted a group/other-readable key")
	}
	if !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), keyPath) {
		t.Fatalf("error = %q, want it to name the path and the chmod", err)
	}

	// The certificate is public: a wider mode on it is not a refusal.
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	certPath := filepath.Join(dir, certName)
	if err := os.Chmod(certPath, 0o644); err != nil {
		t.Fatalf("chmod the certificate: %v", err)
	}
	if _, _, err := LoadOrCreateIdentity(dir); err != nil {
		t.Fatalf("a world-readable certificate was refused: %v", err)
	}
}

// TestLoadOrCreateIdentityRefusesSymlink: a symlinked key or certificate is refused and
// never followed.
func TestLoadOrCreateIdentityRefusesSymlink(t *testing.T) {
	for _, name := range []string{keyName, certName} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "tls")
			if _, _, err := LoadOrCreateIdentity(dir); err != nil {
				t.Fatalf("creating the identity: %v", err)
			}

			// Move the real file aside and point a symlink at it.
			real := filepath.Join(root, "real-"+name)
			path := filepath.Join(dir, name)
			if err := os.Rename(path, real); err != nil {
				t.Fatalf("rename: %v", err)
			}
			if err := os.Symlink(real, path); err != nil {
				t.Fatalf("symlink: %v", err)
			}

			if _, _, err := LoadOrCreateIdentity(dir); err == nil {
				t.Fatalf("LoadOrCreateIdentity followed a symlinked %s", name)
			}
		})
	}
}

// TestIdentityErrorsNeverQuoteTheKey: no error text contains PEM markers or key bytes.
func TestIdentityErrorsNeverQuoteTheKey(t *testing.T) {
	dir := tlsDir(t)
	if _, _, err := LoadOrCreateIdentity(dir); err != nil {
		t.Fatalf("creating the identity: %v", err)
	}

	keyPEM, err := os.ReadFile(filepath.Join(dir, keyName))
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	keyBody := string(keyPEM)
	if index := strings.Index(keyBody, "-----\n"); index >= 0 {
		keyBody = keyBody[index+6:]
	}

	var collected []string

	// A corrupted certificate, a corrupted key, a wide mode, and a missing key.
	breakages := []func() error{
		func() error { return os.WriteFile(filepath.Join(dir, certName), []byte("not a certificate\n"), 0o600) },
		func() error { return os.WriteFile(filepath.Join(dir, keyName), []byte("not a key\n"), 0o600) },
		func() error { return os.Chmod(filepath.Join(dir, keyName), 0o640) },
		func() error { return os.Remove(filepath.Join(dir, keyName)) },
	}
	for i, breakage := range breakages {
		if i > 0 {
			// Rebuild a good identity for the next case.
			_ = os.Remove(filepath.Join(dir, certName))
			_ = os.Remove(filepath.Join(dir, keyName))
			if _, _, err := LoadOrCreateIdentity(dir); err != nil {
				t.Fatalf("rebuilding the identity: %v", err)
			}
		}
		if err := breakage(); err != nil {
			t.Fatalf("breakage %d: %v", i, err)
		}
		_, _, err := LoadOrCreateIdentity(dir)
		if err == nil {
			continue
		}
		collected = append(collected, err.Error())
	}
	if len(collected) == 0 {
		t.Fatal("no error was produced")
	}

	for _, text := range collected {
		for _, marker := range []string{"-----BEGIN", "-----END", "PRIVATE KEY-----"} {
			if strings.Contains(text, marker) {
				t.Fatalf("error text %q quotes a PEM marker", text)
			}
		}
		trimmed := strings.TrimSpace(keyBody)
		if trimmed != "" && strings.Contains(text, trimmed) {
			t.Fatal("an error text quotes the key")
		}
	}
}

// TestLoadOrCreateIdentityRefusesSymlinkedDir: the tls directory itself must be real.
func TestLoadOrCreateIdentityRefusesSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(root, "tls")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, _, err := LoadOrCreateIdentity(link); err == nil {
		t.Fatal("LoadOrCreateIdentity accepted a symlinked directory")
	}
}
