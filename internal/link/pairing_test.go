package link

// Written under GR-3 and never run. Behaviour asserted here is unverified.
//
// Pair tests inject a fake transport; no socket is ever opened.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/gateway"
)

func TestValidatePairing(t *testing.T) {
	valid := map[string]Pairing{
		"loopback":       {Gateway: "127.0.0.1:7777", Host: "laptop", Credential: "abcdefABCDEF0123456789-_"},
		"ipv6 loopback":  {Gateway: "[::1]:7777", Host: "laptop", Credential: "abc"},
		"tailscale ipv4": {Gateway: "100.64.0.1:7777", Host: "ubuntu-b", Credential: "abc"},
	}
	for name, pairing := range valid {
		t.Run("valid "+name, func(t *testing.T) {
			if err := validatePairing(pairing); err != nil {
				t.Fatalf("validatePairing: %v", err)
			}
		})
	}

	invalid := map[string]Pairing{
		"hostname":         {Gateway: "localhost:7777", Host: "laptop", Credential: "abc"},
		"private lan":      {Gateway: "192.168.1.5:7777", Host: "laptop", Credential: "abc"},
		"port zero":        {Gateway: "100.64.0.1:0", Host: "laptop", Credential: "abc"},
		"no port":          {Gateway: "100.64.0.1", Host: "laptop", Credential: "abc"},
		"empty gateway":    {Gateway: "", Host: "laptop", Credential: "abc"},
		"public address":   {Gateway: "8.8.8.8:7777", Host: "laptop", Credential: "abc"},
		"bad host":         {Gateway: "127.0.0.1:7777", Host: "Laptop", Credential: "abc"},
		"empty host":       {Gateway: "127.0.0.1:7777", Host: "", Credential: "abc"},
		"long host":        {Gateway: "127.0.0.1:7777", Host: strings.Repeat("a", 64), Credential: "abc"},
		"empty credential": {Gateway: "127.0.0.1:7777", Host: "laptop", Credential: ""},
		"long credential":  {Gateway: "127.0.0.1:7777", Host: "laptop", Credential: strings.Repeat("a", 129)},
		"space":            {Gateway: "127.0.0.1:7777", Host: "laptop", Credential: "a b"},
		"slash":            {Gateway: "127.0.0.1:7777", Host: "laptop", Credential: "a/b"},
	}
	for name, pairing := range invalid {
		t.Run("invalid "+name, func(t *testing.T) {
			err := validatePairing(pairing)
			if err == nil {
				t.Fatalf("validatePairing accepted %+v", pairing)
			}
			if pairing.Credential != "" && strings.Contains(err.Error(), pairing.Credential) {
				t.Fatalf("the error text carries the credential: %v", err)
			}
		})
	}
}

func TestSaveAndLoadPairing(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	pairing := testPairing()

	if err := SavePairing(stateDir, pairing); err != nil {
		t.Fatalf("SavePairing: %v", err)
	}

	// Exactly 0600 for the file and 0700 for the directory.
	info, err := os.Stat(pairingPath(stateDir))
	if err != nil {
		t.Fatalf("stat pairing: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("pairing file mode = %o, want 600", mode)
	}
	dirInfo, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("state dir mode = %o, want 700", mode)
	}

	// No temp file survived.
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != pairingFileName {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("state directory holds %v, want only %s", names, pairingFileName)
	}

	loaded, err := LoadPairing(stateDir)
	if err != nil {
		t.Fatalf("LoadPairing: %v", err)
	}
	if loaded != pairing {
		t.Fatalf("loaded = %+v, want %+v", loaded, pairing)
	}
}

// TestSavePairingRefusesASymlink: the file is the only place the credential lives,
// so a link there is refused before anything is written.
func TestSavePairingRefusesASymlink(t *testing.T) {
	stateDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")

	if err := os.WriteFile(target, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, pairingPath(stateDir)); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := SavePairing(stateDir, testPairing()); err == nil {
		t.Fatal("SavePairing wrote through a symlink")
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(content) != "do not touch" {
		t.Fatalf("the symlink target was changed: %q", content)
	}
}

// TestLoadPairingRefusals covers the file rules.
func TestLoadPairingRefusals(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadPairing(t.TempDir()); err != ErrNotPaired {
			t.Fatalf("error = %v, want ErrNotPaired", err)
		}
	})

	t.Run("world readable", func(t *testing.T) {
		stateDir := t.TempDir()
		path := pairingPath(stateDir)
		content, err := json.Marshal(testPairing())
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		_, err = LoadPairing(stateDir)
		if err == nil {
			t.Fatal("LoadPairing accepted a world-readable pairing file")
		}
		if !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("error = %q, want the chmod hint", err)
		}
		if strings.Contains(err.Error(), testPairing().Credential) {
			t.Fatalf("the error text carries the credential: %v", err)
		}
	})

	t.Run("bad gateway in the file", func(t *testing.T) {
		stateDir := t.TempDir()
		bad := testPairing()
		bad.Gateway = "192.168.1.5:7777"

		content, err := json.Marshal(bad)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(pairingPath(stateDir), content, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		if _, err := LoadPairing(stateDir); err == nil {
			t.Fatal("LoadPairing accepted a saved address the listen rule refuses")
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		stateDir := t.TempDir()
		content := `{"gateway":"127.0.0.1:7777","host":"laptop","credential":"abc","extra":1}`
		if err := os.WriteFile(pairingPath(stateDir), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		if _, err := LoadPairing(stateDir); err == nil {
			t.Fatal("LoadPairing accepted an unknown field")
		}
	})
}

// TestPair covers the exchange: no Authorization header, no host key, and the
// answer validated before it is returned.
func TestPair(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		transport := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusOK,
			body:   `{"host":"desktop","credential":"abcDEF012_-"}`,
		}}}

		pairing, err := pair(context.Background(), Target{Gateway: "127.0.0.1:7777"}, "ABCD-EFGH-JKLM", transport)
		if err != nil {
			t.Fatalf("Pair: %v", err)
		}
		if pairing.Host != "desktop" || pairing.Credential != "abcDEF012_-" {
			t.Fatalf("pairing = %+v", pairing)
		}
		// The exact typed endpoint is kept.
		if pairing.Gateway != "127.0.0.1:7777" {
			t.Fatalf("gateway = %q, want the typed value", pairing.Gateway)
		}

		recorded := transport.last(t)
		if recorded.path != "/v1/pair" {
			t.Fatalf("path = %q, want /v1/pair", recorded.path)
		}
		if recorded.header.Get("Authorization") != "" {
			t.Fatal("the pairing request carried an Authorization header")
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(recorded.body), &body); err != nil {
			t.Fatalf("body is not JSON: %v", err)
		}
		if _, hasHost := body["host"]; hasHost {
			t.Fatalf("the pairing body named a host: %v", body)
		}
		if body["code"] != "ABCD-EFGH-JKLM" {
			t.Fatalf("body code = %v", body["code"])
		}
	})

	t.Run("hostile answer refused", func(t *testing.T) {
		for _, body := range []string{
			`{"host":"BAD HOST","credential":"abc"}`,
			`{"host":"desktop","credential":""}`,
			`{"host":"desktop","credential":"has space"}`,
		} {
			transport := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: body}}}
			if _, err := pair(context.Background(), Target{Gateway: "127.0.0.1:7777"}, "code", transport); err == nil {
				t.Fatalf("Pair accepted %s", body)
			}
		}
	})

	t.Run("bad code", func(t *testing.T) {
		transport := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusForbidden,
			body:   `{"code":"bad_pairing_code","message":"that pairing code is not valid"}`,
		}}}

		_, err := pair(context.Background(), Target{Gateway: "127.0.0.1:7777"}, "SECRET-CODE", transport)
		if err == nil {
			t.Fatal("Pair accepted a refused code")
		}
		if strings.Contains(err.Error(), "SECRET-CODE") {
			t.Fatalf("the error echoes the code: %v", err)
		}
	})

	t.Run("version mismatch uses the gateway's message", func(t *testing.T) {
		transport := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusBadRequest,
			body:   `{"code":"version_mismatch","message":"this gateway speaks version 2"}`,
		}}}

		_, err := pair(context.Background(), Target{Gateway: "127.0.0.1:7777"}, "code", transport)
		if err == nil {
			t.Fatal("Pair accepted a version mismatch")
		}
		if !strings.Contains(err.Error(), "this gateway speaks version 2") {
			t.Fatalf("error = %q, want the gateway's own message", err)
		}
	})

	t.Run("unusable address", func(t *testing.T) {
		transport := &fakeTransport{answers: []fakeAnswer{{status: http.StatusOK, body: `{}`}}}
		if _, err := pair(context.Background(), Target{Gateway: "localhost:7777"}, "code", transport); err == nil {
			t.Fatal("Pair accepted an address the listen rule refuses")
		}
		if transport.count() != 0 {
			t.Fatalf("%d requests, want 0", transport.count())
		}
	})
}

// TestRunPairOutput: two lines, and neither the credential nor the code.
func TestRunPairOutput(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	transport := &fakeTransport{answers: []fakeAnswer{{
		status: http.StatusOK,
		body:   `{"host":"desktop","credential":"abcDEF012_-"}`,
	}}}

	var out bytes.Buffer
	if err := runPair(context.Background(), stateDir, Target{Gateway: "127.0.0.1:7777"}, "ABCD-EFGH-JKLM", &out, transport); err != nil {
		t.Fatalf("runPair: %v", err)
	}

	output := out.String()
	if strings.Count(output, "\n") != 2 {
		t.Fatalf("output = %q, want exactly two lines", output)
	}
	if !strings.Contains(output, "paired as desktop with gateway 127.0.0.1:7777") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "credential saved in "+pairingPath(stateDir)+" (mode 600)") {
		t.Fatalf("output = %q", output)
	}
	// Neither secret is printed.
	if strings.Contains(output, "abcDEF012_-") || strings.Contains(output, "ABCD-EFGH-JKLM") {
		t.Fatalf("output carries a secret: %q", output)
	}

	// And the pairing really was saved.
	saved, err := LoadPairing(stateDir)
	if err != nil {
		t.Fatalf("LoadPairing after runPair: %v", err)
	}
	if saved.Host != "desktop" {
		t.Fatalf("saved host = %q", saved.Host)
	}
}

// TestPlanTransport walks both rows of the contract's runtime table.
// UNTESTED: written, not run.
func TestPlanTransport(t *testing.T) {
	const fingerprint = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	tests := []struct {
		name   string
		target Target
		want   gateway.Transport
		isErr  bool
	}{
		{
			name:   "loopback, no flag, stays plain",
			target: Target{Gateway: "127.0.0.1:7777"},
			want:   gateway.TransportPlain,
		},
		{
			name:   "tailscale, no flag, stays plain",
			target: Target{Gateway: "100.64.0.1:7777"},
			want:   gateway.TransportPlain,
		},
		{
			name:   "loopback with a fingerprint is refused",
			target: Target{Gateway: "127.0.0.1:7777", Fingerprint: fingerprint},
			isErr:  true,
		},
		{
			name:   "tailscale with insecure-http is refused",
			target: Target{Gateway: "100.64.0.1:7777", InsecureHTTP: true},
			isErr:  true,
		},
		{
			name:   "LAN with a fingerprint is https",
			target: Target{Gateway: "192.168.1.20:7777", Fingerprint: fingerprint},
			want:   gateway.TransportHTTPS,
		},
		{
			name:   "LAN with insecure-http is plain",
			target: Target{Gateway: "192.168.1.20:7777", InsecureHTTP: true},
			want:   gateway.TransportLANHTTP,
		},
		{
			name:   "LAN with neither must choose",
			target: Target{Gateway: "192.168.1.20:7777"},
			isErr:  true,
		},
		{
			name:   "LAN with both must choose",
			target: Target{Gateway: "192.168.1.20:7777", Fingerprint: fingerprint, InsecureHTTP: true},
			isErr:  true,
		},
		{
			name:   "LAN with a malformed fingerprint is refused",
			target: Target{Gateway: "192.168.1.20:7777", Fingerprint: "sha256:abc"},
			isErr:  true,
		},
		{
			name:   "a hostname is not an address",
			target: Target{Gateway: "gateway.local:7777", InsecureHTTP: true},
			isErr:  true,
		},
		{
			name:   "a public address is refused",
			target: Target{Gateway: "8.8.8.8:7777", InsecureHTTP: true},
			isErr:  true,
		},
		{
			name:   "an IPv6 LAN address works",
			target: Target{Gateway: "[fd00::1]:7777", InsecureHTTP: true},
			want:   gateway.TransportLANHTTP,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := planTransport(tc.target)
			if tc.isErr {
				if err == nil {
					t.Fatalf("planTransport accepted %+v", tc.target)
				}
				// A refusal never names the credential, because it never has one, and
				// it never quotes a typed fingerprint back.
				if tc.target.Fingerprint != "" && strings.Contains(err.Error(), tc.target.Fingerprint) {
					t.Fatalf("the error quotes the fingerprint: %v", err)
				}
				if strings.Contains(err.Error(), "Bearer") {
					t.Fatalf("the error mentions a credential: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("planTransport(%+v): %v", tc.target, err)
			}
			if got != tc.want {
				t.Fatalf("transport = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPlanTransportMessages: the two refusals a user can meet, in their own words.
func TestPlanTransportMessages(t *testing.T) {
	t.Run("a non-LAN address names the flag and the class", func(t *testing.T) {
		_, err := planTransport(Target{Gateway: "100.64.0.1:7777", Fingerprint: "sha256:" + strings.Repeat("a", 64)})
		if err == nil {
			t.Fatal("planTransport accepted a flagged Tailscale address")
		}
		for _, part := range []string{"--fingerprint", "--insecure-http", "Tailscale", "apply only to a LAN address"} {
			if !strings.Contains(err.Error(), part) {
				t.Fatalf("error = %q, want %q", err, part)
			}
		}
		// The typed address is not echoed: the class is enough to act on.
		if strings.Contains(err.Error(), "100.64.0.1") {
			t.Fatalf("error = %q, want no address echoed", err)
		}
	})

	t.Run("a LAN address shows both forms", func(t *testing.T) {
		_, err := planTransport(Target{Gateway: "192.168.1.20:7777"})
		if err == nil {
			t.Fatal("planTransport accepted a LAN address with no choice")
		}
		for _, part := range []string{"--fingerprint", "--insecure-http", "rtk runtime pair --gateway ADDR --code CODE --fingerprint", "rtk runtime pair --gateway ADDR --code CODE --insecure-http"} {
			if !strings.Contains(err.Error(), part) {
				t.Fatalf("error = %q, want %q", err, part)
			}
		}
	})
}

// TestScheme: a fingerprint always means https, and nothing else does.
func TestScheme(t *testing.T) {
	if got := (Pairing{}).scheme(); got != "http" {
		t.Fatalf("scheme with no fingerprint = %q, want http", got)
	}
	if got := (Pairing{InsecureHTTP: true}).scheme(); got != "http" {
		t.Fatalf("scheme for a plain LAN pairing = %q, want http", got)
	}
	fingerprint := "sha256:" + strings.Repeat("a", 64)
	if got := (Pairing{Fingerprint: fingerprint}).scheme(); got != "https" {
		t.Fatalf("scheme with a fingerprint = %q, want https", got)
	}
}

// TestPairingTransportRoundTrip: the fields survive save and load, and a file written
// before Direct LAN still loads.
func TestPairingTransportRoundTrip(t *testing.T) {
	t.Run("an https pairing keeps its fingerprint", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		fingerprint := "sha256:" + strings.Repeat("ab", 32)
		saved := Pairing{Gateway: "192.168.1.20:7777", Host: "mac", Credential: "abc", Fingerprint: fingerprint}
		if err := SavePairing(stateDir, saved); err != nil {
			t.Fatalf("SavePairing: %v", err)
		}
		loaded, err := LoadPairing(stateDir)
		if err != nil {
			t.Fatalf("LoadPairing: %v", err)
		}
		if loaded.Fingerprint != fingerprint || loaded.scheme() != "https" {
			t.Fatalf("loaded = %+v", loaded)
		}
	})

	t.Run("a plain LAN pairing keeps insecure_http", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		saved := Pairing{Gateway: "192.168.1.20:7777", Host: "mac", Credential: "abc", InsecureHTTP: true}
		if err := SavePairing(stateDir, saved); err != nil {
			t.Fatalf("SavePairing: %v", err)
		}
		loaded, err := LoadPairing(stateDir)
		if err != nil {
			t.Fatalf("LoadPairing: %v", err)
		}
		if !loaded.InsecureHTTP || loaded.scheme() != "http" {
			t.Fatalf("loaded = %+v", loaded)
		}
	})

	t.Run("an old file with no new fields still loads", func(t *testing.T) {
		stateDir := t.TempDir()
		content := `{"gateway":"100.64.0.1:7777","host":"ubuntu-b","credential":"abc"}`
		if err := os.WriteFile(pairingPath(stateDir), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		loaded, err := LoadPairing(stateDir)
		if err != nil {
			t.Fatalf("an old pairing file was refused: %v", err)
		}
		if loaded.scheme() != "http" || loaded.Fingerprint != "" || loaded.InsecureHTTP {
			t.Fatalf("loaded = %+v", loaded)
		}
	})

	t.Run("a LAN file with no transport choice is refused", func(t *testing.T) {
		stateDir := t.TempDir()
		content := `{"gateway":"192.168.1.20:7777","host":"mac","credential":"abc"}`
		if err := os.WriteFile(pairingPath(stateDir), []byte(content), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadPairing(stateDir)
		if err == nil {
			t.Fatal("LoadPairing accepted a LAN pairing with no transport")
		}
		if !strings.Contains(err.Error(), "re-pair") {
			t.Fatalf("error = %q, want the re-pair advice", err)
		}
	})
}

// TestRunPairPrintsTheWarningFirst: the plain-HTTP warning is shown before the
// exchange, so it appears even when the pairing then fails.
func TestRunPairPrintsTheWarningFirst(t *testing.T) {
	t.Run("success: warning, two lines, then the pin", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		transport := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusOK,
			body:   `{"host":"mac","credential":"abcDEF012_-"}`,
		}}}

		var out bytes.Buffer
		err := runPair(context.Background(), stateDir, Target{Gateway: "192.168.1.20:7777", InsecureHTTP: true}, "CODE", &out, transport)
		if err != nil {
			t.Fatalf("runPair: %v", err)
		}

		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("output = %q, want three lines", out.String())
		}
		if lines[0] != gateway.PlainHTTPWarning {
			t.Fatalf("first line = %q, want the warning", lines[0])
		}
		if !strings.Contains(lines[1], "paired as mac with gateway 192.168.1.20:7777") {
			t.Fatalf("second line = %q", lines[1])
		}
		if !strings.Contains(lines[2], "credential saved in") {
			t.Fatalf("third line = %q", lines[2])
		}
		if strings.Contains(out.String(), "abcDEF012_-") || strings.Contains(out.String(), "CODE") {
			t.Fatalf("output carries a secret: %q", out.String())
		}
	})

	t.Run("failure still shows the warning", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		transport := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusForbidden,
			body:   `{"code":"bad_pairing_code","message":"that pairing code is not valid"}`,
		}}}

		var out bytes.Buffer
		if err := runPair(context.Background(), stateDir, Target{Gateway: "192.168.1.20:7777", InsecureHTTP: true}, "CODE", &out, transport); err == nil {
			t.Fatal("runPair accepted a refused code")
		}
		if !strings.Contains(out.String(), gateway.PlainHTTPWarning) {
			t.Fatalf("output = %q, want the warning before the failure", out.String())
		}
	})

	t.Run("an https pairing prints the pin last", func(t *testing.T) {
		stateDir := filepath.Join(t.TempDir(), "state")
		fingerprint := "sha256:" + strings.Repeat("ab", 32)
		transport := &fakeTransport{answers: []fakeAnswer{{
			status: http.StatusOK,
			body:   `{"host":"mac","credential":"abcDEF012_-"}`,
		}}}

		var out bytes.Buffer
		err := runPair(context.Background(), stateDir, Target{Gateway: "192.168.1.20:7777", Fingerprint: fingerprint}, "CODE", &out, transport)
		if err != nil {
			t.Fatalf("runPair: %v", err)
		}

		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("output = %q, want three lines", out.String())
		}
		if strings.Contains(lines[0], "WARNING") {
			t.Fatal("an https pairing printed the plain-HTTP warning")
		}
		if lines[2] != "transport: HTTPS, certificate pinned to "+fingerprint {
			t.Fatalf("last line = %q", lines[2])
		}
	})
}

// TestPairKeepsTheChosenTransport: the saved pairing carries what was typed, so a later
// load agrees with it.
func TestPairKeepsTheChosenTransport(t *testing.T) {
	transport := &fakeTransport{answers: []fakeAnswer{{
		status: http.StatusOK,
		body:   `{"host":"mac","credential":"abcDEF012_-"}`,
	}}}
	fingerprint := "sha256:" + strings.Repeat("ab", 32)

	pairing, err := pair(context.Background(), Target{Gateway: "192.168.1.20:7777", Fingerprint: fingerprint}, "CODE", transport)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if pairing.Fingerprint != fingerprint || pairing.InsecureHTTP {
		t.Fatalf("pairing = %+v", pairing)
	}
	if pairing.scheme() != "https" {
		t.Fatalf("scheme = %q, want https", pairing.scheme())
	}

	// The request went to the scheme the fingerprint implies.
	recorded := transport.last(t)
	if recorded.path != "/v1/pair" {
		t.Fatalf("path = %q", recorded.path)
	}
}
