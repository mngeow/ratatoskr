package gateway

// Untested: under GR-3 nothing here has been run. These tests create real files in a
// temporary directory; no socket is bound and no network is used.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func endpointDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return dir
}

func lanPlan(t *testing.T, transport Transport) ListenPlan {
	t.Helper()
	plan, err := PlanListen("192.168.1.20:7777", transport == TransportHTTPS, transport == TransportLANHTTP)
	if err != nil {
		t.Fatalf("PlanListen: %v", err)
	}
	if plan.Transport != transport {
		t.Fatalf("transport = %v, want %v", plan.Transport, transport)
	}
	return plan
}

const testFingerprint = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestWriteThenLoadEndpoint: the file round-trips, at 0600, with the fields the
// contract lists.
func TestWriteThenLoadEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		plan        ListenPlan
		fingerprint string
		wantScheme  string
	}{
		{
			name: "https on a LAN address",
			plan: lanPlan(t, TransportHTTPS), fingerprint: testFingerprint,
			wantScheme: schemeHTTPS,
		},
		{
			name:       "plain HTTP on a LAN address",
			plan:       lanPlan(t, TransportLANHTTP),
			wantScheme: schemeHTTP,
		},
		{
			name:       "loopback",
			plan:       mustPlan(t, "127.0.0.1:7777", false, false),
			wantScheme: schemeHTTP,
		},
		{
			name:       "Tailscale",
			plan:       mustPlan(t, "100.64.0.1:7777", false, false),
			wantScheme: schemeHTTP,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := endpointDir(t)
			if err := WriteEndpoint(dir, tc.plan, tc.fingerprint); err != nil {
				t.Fatalf("WriteEndpoint: %v", err)
			}

			info, err := os.Lstat(filepath.Join(dir, endpointName))
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("mode = %04o, want 0600", perm)
			}

			endpoint, ok := LoadEndpoint(dir)
			if !ok {
				t.Fatal("LoadEndpoint refused what WriteEndpoint wrote")
			}
			if endpoint.Listen != tc.plan.Addr.String() {
				t.Fatalf("listen = %q, want %q", endpoint.Listen, tc.plan.Addr.String())
			}
			if endpoint.Scheme != tc.wantScheme {
				t.Fatalf("scheme = %q, want %q", endpoint.Scheme, tc.wantScheme)
			}
			if endpoint.Fingerprint != tc.fingerprint {
				t.Fatalf("fingerprint = %q, want %q", endpoint.Fingerprint, tc.fingerprint)
			}
			if wantLAN := tc.plan.Class == ClassLAN; endpoint.LAN != wantLAN {
				t.Fatalf("lan = %t, want %t", endpoint.LAN, wantLAN)
			}
		})
	}
}

func mustPlan(t *testing.T, listen string, allowLAN, insecureHTTP bool) ListenPlan {
	t.Helper()
	plan, err := PlanListen(listen, allowLAN, insecureHTTP)
	if err != nil {
		t.Fatalf("PlanListen(%q): %v", listen, err)
	}
	return plan
}

// TestLoadEndpointRefusesEveryDefect: each bad file reports false, never an error, and
// never quotes its contents.
func TestLoadEndpointRefusesEveryDefect(t *testing.T) {
	valid := `{"listen":"192.168.1.20:7777","scheme":"http","lan":true}`

	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
	}{
		{
			name: "missing file",
			setup: func(t *testing.T, dir string) {
				_ = dir
			},
		},
		{
			name: "not JSON",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, "not json at all", 0o600)
			},
		},
		{
			name: "unknown field",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"http","lan":true,"extra":1}`, 0o600)
			},
		},
		{
			name: "trailing data",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, valid+"{\"listen\":\"192.168.1.21:7777\"}", 0o600)
			},
		},
		{
			name: "mode wider than 0600",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, valid, 0o644)
			},
		},
		{
			name: "bad address",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"nope","scheme":"http","lan":true}`, 0o600)
			},
		},
		{
			name: "hostname is not an address",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"gateway.local:7777","scheme":"http","lan":true}`, 0o600)
			},
		},
		{
			name: "non-canonical address",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.001.020:7777","scheme":"http","lan":true}`, 0o600)
			},
		},
		{
			name: "https without a fingerprint",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"https","lan":true}`, 0o600)
			},
		},
		{
			name: "http with a fingerprint",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"http","lan":true,"fingerprint":"`+testFingerprint+`"}`, 0o600)
			},
		},
		{
			name: "https without lan",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"https","fingerprint":"`+testFingerprint+`"}`, 0o600)
			},
		},
		{
			name: "lan true on a Tailscale address",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"100.64.0.1:7777","scheme":"http","lan":true}`, 0o600)
			},
		},
		{
			name: "lan false on a LAN address",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"http"}`, 0o600)
			},
		},
		{
			name: "unknown scheme",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"ftp","lan":true}`, 0o600)
			},
		},
		{
			name: "uppercase fingerprint",
			setup: func(t *testing.T, dir string) {
				writeEndpointFile(t, dir, `{"listen":"192.168.1.20:7777","scheme":"https","lan":true,"fingerprint":"sha256:0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"}`, 0o600)
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, dir string) {
				real := filepath.Join(dir, "real.json")
				if err := os.WriteFile(real, []byte(valid), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
				if err := os.Symlink(real, filepath.Join(dir, endpointName)); err != nil {
					t.Fatalf("symlink: %v", err)
				}
			},
		},
		{
			name: "a directory",
			setup: func(t *testing.T, dir string) {
				if err := os.Mkdir(filepath.Join(dir, endpointName), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := endpointDir(t)
			tc.setup(t, dir)

			if endpoint, ok := LoadEndpoint(dir); ok {
				t.Fatalf("LoadEndpoint accepted a defective file: %+v", endpoint)
			}

			// The reason, kept for a future caller, never quotes the file.
			if _, err := loadEndpoint(dir); err == nil {
				t.Fatal("loadEndpoint gave no reason")
			} else if strings.Contains(err.Error(), valid) {
				t.Fatalf("the reason quotes the file: %q", err)
			}
		})
	}
}

func writeEndpointFile(t *testing.T, dir, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(dir, endpointName)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write: %v", err)
	}
	// os.WriteFile honours the umask; set the mode the test means.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod: %v", err)
	}
}

// TestWriteEndpointIsAtomic: an existing endpoint is replaced, not truncated in place,
// and the replacement carries no stale field.
func TestWriteEndpointIsAtomic(t *testing.T) {
	dir := endpointDir(t)

	if err := WriteEndpoint(dir, lanPlan(t, TransportHTTPS), testFingerprint); err != nil {
		t.Fatalf("first WriteEndpoint: %v", err)
	}
	if err := WriteEndpoint(dir, mustPlan(t, "127.0.0.1:7777", false, false), ""); err != nil {
		t.Fatalf("second WriteEndpoint: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, endpointName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("the file is not JSON: %v", err)
	}
	if _, present := decoded["fingerprint"]; present {
		t.Fatalf("the fingerprint survived a rewrite: %s", raw)
	}
	if _, present := decoded["lan"]; present {
		t.Fatalf("the lan field survived a rewrite: %s", raw)
	}

	// No temporary file is left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("a temporary file was left behind: %s", entry.Name())
		}
	}
}

// TestValidateEndpointCanonicalForm: the file records what was bound, not a near miss.
func TestValidateEndpointCanonicalForm(t *testing.T) {
	if err := validateEndpoint(Endpoint{Listen: "192.168.1.20:7777", Scheme: schemeHTTP, LAN: true}); err != nil {
		t.Fatalf("a canonical endpoint was refused: %v", err)
	}
	if err := validateEndpoint(Endpoint{Listen: "192.168.1.020:7777", Scheme: schemeHTTP, LAN: true}); err == nil {
		t.Fatal("a non-canonical address was accepted")
	}
}
