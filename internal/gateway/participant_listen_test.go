package gateway

// Tests for the participant listener (P2a). Written, never run (GR-3): UNTESTED. Most cases are pure; the
// last two bind loopback sockets and run RunWith, and are the ones to run first when GR-3 is lifted.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const testSPKIPin = "sha256//AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// TestPlanParticipantListenTable: the participant flag table is its own, and its refusals name the
// participant flags.
func TestPlanParticipantListenTable(t *testing.T) {
	tests := []struct {
		name      string
		options   ParticipantOptions
		wantOff   bool
		wantClass AddrClass
		wantTrans Transport
		wantErr   string
	}{
		{name: "off", wantOff: true},
		{name: "allow-lan without a listener", options: ParticipantOptions{AllowLAN: true}, wantErr: FlagParticipantListen},
		{name: "insecure without a listener", options: ParticipantOptions{InsecureHTTP: true}, wantErr: FlagParticipantListen},

		{name: "loopback", options: ParticipantOptions{Listen: "127.0.0.1:7790"}, wantClass: ClassLoopback, wantTrans: TransportPlain},
		{name: "loopback v6", options: ParticipantOptions{Listen: "[::1]:7790"}, wantClass: ClassLoopback, wantTrans: TransportPlain},
		{name: "loopback with allow-lan", options: ParticipantOptions{Listen: "127.0.0.1:7790", AllowLAN: true}, wantErr: "loopback"},
		{name: "loopback with insecure", options: ParticipantOptions{Listen: "127.0.0.1:7790", InsecureHTTP: true}, wantErr: "loopback"},

		{name: "tailnet defaults to HTTPS", options: ParticipantOptions{Listen: "100.64.0.1:7790"}, wantClass: ClassTailscale, wantTrans: TransportHTTPS},
		{name: "tailnet v6 defaults to HTTPS", options: ParticipantOptions{Listen: "[fd7a:115c:a1e0::1]:7790"}, wantClass: ClassTailscale, wantTrans: TransportHTTPS},
		{name: "tailnet plain on request", options: ParticipantOptions{Listen: "100.64.0.1:7790", InsecureHTTP: true}, wantClass: ClassTailscale, wantTrans: TransportPlain},
		{name: "tailnet with allow-lan", options: ParticipantOptions{Listen: "100.64.0.1:7790", AllowLAN: true}, wantErr: FlagParticipantAllowLAN},

		{name: "LAN needs allow-lan", options: ParticipantOptions{Listen: "192.168.1.20:7790"}, wantErr: FlagParticipantAllowLAN},
		{name: "LAN insecure alone still needs allow-lan", options: ParticipantOptions{Listen: "192.168.1.20:7790", InsecureHTTP: true}, wantErr: FlagParticipantAllowLAN},
		{name: "LAN HTTPS", options: ParticipantOptions{Listen: "192.168.1.20:7790", AllowLAN: true}, wantClass: ClassLAN, wantTrans: TransportHTTPS},
		{name: "LAN plain", options: ParticipantOptions{Listen: "192.168.1.20:7790", AllowLAN: true, InsecureHTTP: true}, wantClass: ClassLAN, wantTrans: TransportLANHTTP},

		{name: "public address", options: ParticipantOptions{Listen: "8.8.8.8:7790"}, wantErr: participantSubject},
		{name: "hostname", options: ParticipantOptions{Listen: "example.com:7790"}, wantErr: participantSubject},
		{name: "port zero", options: ParticipantOptions{Listen: "127.0.0.1:0"}, wantErr: "port 0"},
		{name: "unspecified", options: ParticipantOptions{Listen: "0.0.0.0:7790"}, wantErr: "unspecified"},
		{name: "mapped", options: ParticipantOptions{Listen: "[::ffff:127.0.0.1]:7790"}, wantErr: "mapped"},
		{name: "zone", options: ParticipantOptions{Listen: "[fe80::1%en0]:7790"}, wantErr: participantSubject},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanParticipantListen(tc.options)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				// A refusal about participant flags never names the host's flags.
				if strings.Contains(err.Error(), " --allow-lan") || strings.Contains(err.Error(), " --insecure-http") {
					t.Fatalf("the message names a host flag: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantOff {
				if plan != nil {
					t.Fatalf("plan = %+v, want nil", plan)
				}
				return
			}
			if plan == nil || plan.Class != tc.wantClass || plan.Transport != tc.wantTrans {
				t.Fatalf("plan = %+v, want class %v transport %v", plan, tc.wantClass, tc.wantTrans)
			}
		})
	}
}

// TestPlanBothRefusesTheSameEndpointAndValidatesBoth.
func TestPlanBothRefusesTheSameEndpointAndValidatesBoth(t *testing.T) {
	if _, _, err := planBoth("100.64.0.1:7777", false, false, ParticipantOptions{Listen: "100.64.0.1:7777"}); err == nil ||
		!strings.Contains(err.Error(), "same address") {
		t.Fatalf("identical endpoints: %v", err)
	}
	// Same address, different port: fine.
	host, part, err := planBoth("100.64.0.1:7777", false, false, ParticipantOptions{Listen: "100.64.0.1:7790"})
	if err != nil || part == nil || host.Addr.Port() != 7777 || part.Addr.Port() != 7790 {
		t.Fatalf("planBoth = %+v %+v %v", host, part, err)
	}
	// A bad host plan wins even when the participant plan is fine, and the other way round.
	if _, _, err := planBoth("8.8.8.8:7777", false, false, ParticipantOptions{Listen: "127.0.0.1:7790"}); err == nil {
		t.Fatal("a public host address was accepted")
	}
	if _, _, err := planBoth("127.0.0.1:7777", false, false, ParticipantOptions{Listen: "8.8.8.8:7790"}); err == nil {
		t.Fatal("a public participant address was accepted")
	}
	// Off: the host plan alone, and a nil participant plan.
	if _, part, err := planBoth("127.0.0.1:7777", false, false, ParticipantOptions{}); err != nil || part != nil {
		t.Fatalf("participant off: %+v %v", part, err)
	}
}

// TestRunWithValidatesBeforeTouchingAnyState: a refused plan creates no file.
func TestRunWithValidatesBeforeTouchingAnyState(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	var status bytes.Buffer
	for _, options := range []ParticipantOptions{
		{Listen: "192.168.1.20:7790"},               // LAN without --participant-allow-lan
		{AllowLAN: true},                            // flag without a listener
		{Listen: "127.0.0.1:7777"},                  // same endpoint as the host
		{Listen: "100.64.0.1:7790", AllowLAN: true}, // flag that does not apply
	} {
		if err := RunWith(context.Background(), stateDir, "127.0.0.1:7777", false, false, options, 0, &status); err == nil {
			t.Fatalf("RunWith accepted %+v", options)
		}
	}
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("a refused plan created %s (%v)", stateDir, err)
	}
	if status.Len() != 0 {
		t.Fatalf("a refused plan printed %q", status.String())
	}
}

// TestSPKIPin: the pin is sha256// plus the base64 of the SHA-256 of the SubjectPublicKeyInfo, computed from
// the existing identity.
func TestSPKIPin(t *testing.T) {
	cert, _, err := LoadOrCreateIdentity(filepath.Join(t.TempDir(), "tls"))
	if err != nil {
		t.Fatal(err)
	}
	pin, err := SPKIPin(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(parsed.RawSubjectPublicKeyInfo)
	if want := "sha256//" + base64.StdEncoding.EncodeToString(sum[:]); pin != want {
		t.Fatalf("pin = %q, want %q", pin, want)
	}
	if _, err := SPKIPin([]byte("not a certificate")); err == nil {
		t.Fatal("garbage was accepted as a certificate")
	}
}

// TestParticipantStatusLines: one transport statement per listener; the plain-HTTP warning says the
// credential is readable; nothing secret.
func TestParticipantStatusLines(t *testing.T) {
	tests := []struct {
		name    string
		options ParticipantOptions
		want    []string
		absent  []string
	}{
		{name: "loopback", options: ParticipantOptions{Listen: "127.0.0.1:7790"},
			want: []string{"participant API listening on 127.0.0.1:7790", "plain HTTP; loopback only"}, absent: []string{"WARNING", "fingerprint"}},
		{name: "tailnet HTTPS", options: ParticipantOptions{Listen: "100.64.0.1:7790"},
			want: []string{"HTTPS, self-signed certificate, fingerprint " + testFingerprint, "key pin", testSPKIPin}, absent: []string{"WARNING"}},
		{name: "tailnet plain", options: ParticipantOptions{Listen: "100.64.0.1:7790", InsecureHTTP: true},
			want: []string{ParticipantPlainHTTPWarning}, absent: []string{"self-signed", "key pin"}},
		{name: "LAN plain", options: ParticipantOptions{Listen: "192.168.1.20:7790", AllowLAN: true, InsecureHTTP: true},
			want: []string{ParticipantPlainHTTPWarning, "incoming TCP on port 7790"}},
		{name: "LAN HTTPS", options: ParticipantOptions{Listen: "192.168.1.20:7790", AllowLAN: true},
			want: []string{"fingerprint " + testFingerprint, "incoming TCP on port 7790"}, absent: []string{"WARNING"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanParticipantListen(tc.options)
			if err != nil || plan == nil {
				t.Fatalf("plan: %v %v", plan, err)
			}
			fingerprint, pin := "", ""
			if plan.Transport == TransportHTTPS {
				fingerprint, pin = testFingerprint, testSPKIPin
			}
			joined := strings.Join(participantStatusLines(*plan, plan.Addr.String(), fingerprint, pin), "\n")
			for _, part := range tc.want {
				if !strings.Contains(joined, part) {
					t.Fatalf("lines %q do not contain %q", joined, part)
				}
			}
			for _, part := range tc.absent {
				if strings.Contains(joined, part) {
					t.Fatalf("lines %q contain %q", joined, part)
				}
			}
			if !strings.Contains(ParticipantPlainHTTPWarning, "readable by anything on the network path") {
				t.Fatal("the plain-HTTP warning does not say the credential is readable")
			}
		})
	}
}

// TestParticipantEndpointFile: its own file, private, validated both ways, and the host endpoint file is
// never touched by it.
func TestParticipantEndpointFile(t *testing.T) {
	dir := t.TempDir()
	https, err := PlanParticipantListen(ParticipantOptions{Listen: "100.64.0.1:7790"})
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteParticipantEndpoint(dir, *https, testFingerprint, testSPKIPin); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, participantEndpointName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("participant endpoint file: %v %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dir, endpointName)); !os.IsNotExist(err) {
		t.Fatalf("the host endpoint file was created: %v", err)
	}
	got, ok := LoadParticipantEndpoint(dir)
	if !ok || got.Scheme != schemeHTTPS || got.Class != "tailscale" || got.Fingerprint != testFingerprint || got.SPKIPin != testSPKIPin {
		t.Fatalf("loaded = %+v %t", got, ok)
	}

	plain, _ := PlanParticipantListen(ParticipantOptions{Listen: "127.0.0.1:7790"})
	if err := WriteParticipantEndpoint(dir, *plain, "", ""); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadParticipantEndpoint(dir); !ok || got.Scheme != schemeHTTP || got.Fingerprint != "" || got.SPKIPin != "" {
		t.Fatalf("plain loaded = %+v %t", got, ok)
	}

	// The writer refuses what its reader would refuse.
	if err := WriteParticipantEndpoint(dir, *https, "", ""); err == nil {
		t.Fatal("an HTTPS endpoint without pins was written")
	}
	if err := WriteParticipantEndpoint(dir, *https, testFingerprint, "sha256//short"); err == nil {
		t.Fatal("a malformed key pin was written")
	}
	if err := WriteParticipantEndpoint(dir, *plain, testFingerprint, testSPKIPin); err == nil {
		t.Fatal("a plain endpoint with pins was written")
	}

	if warning := RemoveStaleParticipantEndpoint(dir); warning != "" {
		t.Fatalf("removing a regular file warned: %q", warning)
	}
	if warning := RemoveStaleParticipantEndpoint(dir); warning != "" {
		t.Fatalf("removing a missing file warned: %q", warning)
	}
	if _, ok := LoadParticipantEndpoint(dir); ok {
		t.Fatal("a removed endpoint still loads")
	}

	// A file that is too open is not believed.
	if err := os.WriteFile(filepath.Join(dir, participantEndpointName), []byte(`{"listen":"127.0.0.1:7790","scheme":"http","class":"loopback"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadParticipantEndpoint(dir); ok {
		t.Fatal("a world-readable endpoint file was accepted")
	}
}

// TestParticipantHandlerNamesTheContractAndAnswersUnknownRoutes: an unknown path is a 404 wire error with the
// contract header and without needing the host protocol header; the host handler never serves the prefix.
func TestParticipantHandlerNamesTheContractAndAnswersUnknownRoutes(t *testing.T) {
	store, _ := openStore(t, false)
	server := NewServer(store, Options{})

	for _, path := range []string{protocol.ParticipantPathPrefix + "/nothing-here", "/anything"} {
		recorder := httptest.NewRecorder()
		server.ParticipantHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d", path, recorder.Code)
		}
		if got := recorder.Header().Get(protocol.ParticipantContractHeader); got != protocol.ParticipantContract {
			t.Fatalf("%s: %s = %q", path, protocol.ParticipantContractHeader, got)
		}
		if !strings.Contains(recorder.Body.String(), protocol.WireErrNotFound) {
			t.Fatalf("%s: body %q", path, recorder.Body.String())
		}
	}

	// The host handler never serves the participant prefix: with or without its protocol header it answers
	// a wire error (400 version_mismatch or 404 not_found), and no participant route is reached.
	for _, withHeader := range []bool{false, true} {
		request := httptest.NewRequest(http.MethodGet, protocol.ParticipantPathPrefix, nil)
		if withHeader {
			request.Header.Set(protocol.WireVersionHeader, "2")
		}
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotFound {
			t.Fatalf("header=%t: host handler answered %d, want 400 or 404", withHeader, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), `"code"`) {
			t.Fatalf("header=%t: body %q is not a wire error", withHeader, recorder.Body.String())
		}
		if recorder.Header().Get(protocol.ParticipantContractHeader) != "" {
			t.Fatalf("header=%t: the host handler set the participant contract header", withHeader)
		}
	}
}

// freeLoopbackAddr returns a loopback address with a free port. The port is released before it returns, so
// a rare race with another process is possible; these tests accept that.
func freeLoopbackAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

// TestRunWithServesBothListenersAndShutsDownTogether: both lines are printed, the participant endpoint is its
// own file, the host endpoint file keeps its shape, and one cancel stops both within the one grace.
func TestRunWithServesBothListenersAndShutsDownTogether(t *testing.T) {
	stateDir := t.TempDir()
	hostAddr, participantAddr := freeLoopbackAddr(t), freeLoopbackAddr(t)
	status := &lockedBuffer{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunWith(ctx, stateDir, hostAddr, false, false, ParticipantOptions{Listen: participantAddr}, 0, status)
	}()

	waitFor(t, "the participant status line", func() bool { return strings.Contains(status.String(), "participant API listening on") })
	for _, addr := range []string{hostAddr, participantAddr} {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("dial %s: %v", addr, err)
		}
		_ = conn.Close()
	}
	dir := filepath.Join(stateDir, "gateway")
	if _, ok := LoadParticipantEndpoint(dir); !ok {
		t.Fatal("no participant endpoint file")
	}
	if endpoint, ok := LoadEndpoint(dir); !ok || endpoint.Listen != hostAddr || endpoint.Scheme != schemeHTTP {
		t.Fatalf("host endpoint = %+v %t", endpoint, ok)
	}
	lines := status.String()
	if strings.Index(lines, "gateway listening on") > strings.Index(lines, "participant API listening on") {
		t.Fatalf("the host lines must come first: %q", lines)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunWith: %v", err)
		}
	case <-time.After(shutdownGrace + 2*time.Second):
		t.Fatal("shutdown did not finish within the one grace")
	}
	for _, addr := range []string{hostAddr, participantAddr} {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("%s still accepts after shutdown", addr)
		}
	}
}

// TestRunWithRollsBackWhenTheSecondBindFails: the participant address is taken, so the host listener that was
// already bound is closed again and nothing keeps running; with the participant API off the gateway starts
// as it always did and leaves no participant file.
func TestRunWithRollsBackWhenTheSecondBindFails(t *testing.T) {
	stateDir := t.TempDir()
	hostAddr := freeLoopbackAddr(t)
	taken, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = taken.Close() }()

	err = RunWith(context.Background(), stateDir, hostAddr, false, false,
		ParticipantOptions{Listen: taken.Addr().String()}, 0, &lockedBuffer{})
	if err == nil {
		t.Fatal("RunWith started with the participant address taken")
	}
	// The host address is free again.
	again, err := net.Listen("tcp4", hostAddr)
	if err != nil {
		t.Fatalf("the host listener was left bound: %v", err)
	}
	_ = again.Close()
	if _, ok := LoadParticipantEndpoint(filepath.Join(stateDir, "gateway")); ok {
		t.Fatal("a failed start left a participant endpoint")
	}
}

// lockedBuffer is a status writer the test can read while RunWith still writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStaleParticipantEndpointIsRemovedOnlyIfItIsARegularFile: a symlink and a directory at that name are left
// alone and reported, a symlink's target is untouched, and nothing fails.
func TestStaleParticipantEndpointIsRemovedOnlyIfItIsARegularFile(t *testing.T) {
	t.Run("a symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(t.TempDir(), "precious")
		if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, participantEndpointName)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		warning := RemoveStaleParticipantEndpoint(dir)
		if !strings.Contains(warning, "symlink") || !strings.Contains(warning, "left alone") {
			t.Fatalf("warning = %q", warning)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the symlink was removed or replaced: %v %v", info, err)
		}
		if data, err := os.ReadFile(target); err != nil || string(data) != "keep me" {
			t.Fatalf("the symlink's target changed: %q %v", data, err)
		}
	})

	t.Run("a directory, even an empty one", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, participantEndpointName)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		warning := RemoveStaleParticipantEndpoint(dir)
		if !strings.Contains(warning, "directory") || !strings.Contains(warning, "left alone") {
			t.Fatalf("warning = %q", warning)
		}
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			t.Fatalf("the directory was removed: %v %v", info, err)
		}
	})

	t.Run("a regular file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, participantEndpointName)
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if warning := RemoveStaleParticipantEndpoint(dir); warning != "" {
			t.Fatalf("warning = %q", warning)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("the regular file is still there: %v", err)
		}
	})

	t.Run("the host endpoint file is never involved", func(t *testing.T) {
		dir := t.TempDir()
		host := filepath.Join(dir, endpointName)
		if err := os.WriteFile(host, []byte("host"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = RemoveStaleParticipantEndpoint(dir)
		if data, err := os.ReadFile(host); err != nil || string(data) != "host" {
			t.Fatalf("the host endpoint file changed: %q %v", data, err)
		}
	})
}

// TestRunWithStopsBothServersWhenTheParticipantEndpointCannotBeWritten: the endpoint write fails after both
// sockets are bound (a symlink sits at the file's name), so RunWith returns the error with both servers shut
// down and nothing left accepting.
func TestRunWithStopsBothServersWhenTheParticipantEndpointCannotBeWritten(t *testing.T) {
	stateDir := t.TempDir()
	gatewayDir := filepath.Join(stateDir, "gateway")
	if err := os.MkdirAll(gatewayDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(stateDir, "elsewhere"), filepath.Join(gatewayDir, participantEndpointName)); err != nil {
		t.Fatal(err)
	}
	hostAddr, participantAddr := freeLoopbackAddr(t), freeLoopbackAddr(t)

	done := make(chan error, 1)
	go func() {
		done <- RunWith(context.Background(), stateDir, hostAddr, false, false, ParticipantOptions{Listen: participantAddr}, 0, &lockedBuffer{})
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("err = %v, want the symlink refusal", err)
		}
	case <-time.After(shutdownGrace + 3*time.Second):
		t.Fatal("RunWith did not return")
	}
	for _, addr := range []string{hostAddr, participantAddr} {
		if conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = conn.Close()
			t.Fatalf("%s still accepts after the failed start", addr)
		}
	}
}
