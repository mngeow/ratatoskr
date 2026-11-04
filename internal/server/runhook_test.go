package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// panicReader panics on the first read. RunHook must survive it: a panic would
// exit the hook process non-zero and could block or decide for the harness.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom") }

// captureRunHook runs fn with stdout and stderr redirected, and returns what was
// written to each.
func captureRunHook(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()

	outRead, outWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	errRead, errWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}

	originalOut, originalErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outWrite, errWrite

	fn()

	os.Stdout, os.Stderr = originalOut, originalErr
	_ = outWrite.Close()
	_ = errWrite.Close()

	outBytes, _ := io.ReadAll(outRead)
	errBytes, _ := io.ReadAll(errRead)
	_ = outRead.Close()
	_ = errRead.Close()

	return string(outBytes), string(errBytes)
}

// hookServer starts a local server that records the one request it receives.
func hookServer(t *testing.T, status int, delay time.Duration) (*httptest.Server, *capturedRequest) {
	t.Helper()

	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("Authorization")
		captured.body = body
		captured.received = true

		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

type capturedRequest struct {
	method   string
	path     string
	auth     string
	body     []byte
	received bool
}

// TestRunHookAlwaysReturnsNilAndPrintsNothing is the invariant-2 test: every
// failure path is silent on stdout, returns nil, and says at most one short line
// on stderr that never carries the token.
func TestRunHookAlwaysReturnsNilAndPrintsNothing(t *testing.T) {
	const token = "secret-token-value"

	tests := []struct {
		name    string
		args    []string
		stdin   io.Reader
		arrange func(t *testing.T, dir string)
	}{
		{
			name:  "missing hook file",
			args:  []string{"--agent", "alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: strings.NewReader(`{}`),
		},
		{
			name:  "hook file is a directory",
			args:  []string{"--agent", "alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: strings.NewReader(`{}`),
			arrange: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "hook-alpha.json"), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
			},
		},
		{
			name:  "bad flags",
			args:  []string{"--nope"},
			stdin: strings.NewReader(`{}`),
		},
		{
			name:  "missing session dir",
			args:  []string{"--agent", "alpha", "--event", "Stop"},
			stdin: strings.NewReader(`{}`),
		},
		{
			name:  "invalid agent name",
			args:  []string{"--agent", "../alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: strings.NewReader(`{}`),
		},
		{
			name:  "non-loopback url in the hook file",
			args:  []string{"--agent", "alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: strings.NewReader(`{}`),
			arrange: func(t *testing.T, dir string) {
				t.Helper()
				writeRawHookFile(t, dir, "alpha", "http://example.com:4100", token)
			},
		},
		{
			name:  "https url in the hook file",
			args:  []string{"--agent", "alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: strings.NewReader(`{}`),
			arrange: func(t *testing.T, dir string) {
				t.Helper()
				writeRawHookFile(t, dir, "alpha", "https://127.0.0.1:4100", token)
			},
		},
		{
			name:  "unparseable hook file",
			args:  []string{"--agent", "alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: strings.NewReader(`{}`),
			arrange: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "hook-alpha.json"), []byte("nope"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			},
		},
		{
			name:  "stdin reader panics",
			args:  []string{"--agent", "alpha", "--event", "Stop", "--session-dir", "{dir}"},
			stdin: panicReader{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.arrange != nil {
				tc.arrange(t, dir)
			}

			args := make([]string, len(tc.args))
			for i, arg := range tc.args {
				args[i] = strings.ReplaceAll(arg, "{dir}", dir)
			}

			var err error
			stdout, stderr := captureRunHook(t, func() {
				err = RunHook(args, tc.stdin)
			})

			if err != nil {
				t.Fatalf("RunHook returned %v, want nil on every path", err)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing: a hook that printed could answer the harness", stdout)
			}
			if strings.Contains(stderr, token) {
				t.Fatalf("stderr leaked the token: %q", stderr)
			}
			if lines := strings.Count(stderr, "\n"); lines > 1 {
				t.Fatalf("stderr has %d lines, want at most one: %q", lines, stderr)
			}
			if stderr != "" && !strings.Contains(stderr, "not reported") {
				t.Fatalf("stderr = %q, want it to say the event was not reported", stderr)
			}
		})
	}
}

// TestRunHookSurvivesAServerErrorOrSlowServer covers the two network failures a
// hook can meet.
func TestRunHookSurvivesAServerErrorOrSlowServer(t *testing.T) {
	tests := []struct {
		name   string
		status int
		delay  time.Duration
	}{
		{name: "server error", status: http.StatusInternalServerError},
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "slow server", status: http.StatusNoContent, delay: 3 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, _ := hookServer(t, tc.status, tc.delay)
			dir := t.TempDir()
			writeRawHookFile(t, dir, "alpha", server.URL, "token")

			var err error
			stdout, stderr := captureRunHook(t, func() {
				err = RunHook([]string{
					"--agent", "alpha",
					"--event", "UserPromptSubmit",
					"--session-dir", dir,
				}, strings.NewReader(`{"session_id":"s1","prompt":"hi"}`))
			})

			if err != nil {
				t.Fatalf("RunHook returned %v, want nil", err)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing", stdout)
			}
			if !strings.Contains(stderr, "UserPromptSubmit not reported") {
				t.Fatalf("stderr = %q, want the one line naming the event", stderr)
			}
		})
	}
}

// TestRunHookPostsTheEventAndPayload is the happy path: the harness JSON becomes
// the payload, untouched.
func TestRunHookPostsTheEventAndPayload(t *testing.T) {
	server, captured := hookServer(t, http.StatusNoContent, 0)
	dir := t.TempDir()
	writeRawHookFile(t, dir, "alpha", server.URL, "token-123")

	payload := `{"session_id":"s1","prompt":"hello \"world\"","hook_event_name":"UserPromptSubmit"}`

	var err error
	stdout, stderr := captureRunHook(t, func() {
		err = RunHook([]string{
			"--agent", "alpha",
			"--event", "UserPromptSubmit",
			"--session-dir", dir,
		}, strings.NewReader(payload))
	})

	if err != nil {
		t.Fatalf("RunHook: %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("success path wrote stdout=%q stderr=%q, want silence", stdout, stderr)
	}
	if !captured.received {
		t.Fatal("no request reached the server")
	}
	if captured.method != http.MethodPost || captured.path != "/hook" {
		t.Fatalf("request = %s %s, want POST /hook", captured.method, captured.path)
	}
	if captured.auth != "Bearer token-123" {
		t.Fatalf("Authorization = %q", captured.auth)
	}

	var body struct {
		Event   string          `json:"event"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(captured.body, &body); err != nil {
		t.Fatalf("body is not JSON: %v (%q)", err, captured.body)
	}
	if body.Event != "UserPromptSubmit" {
		t.Fatalf("event = %q", body.Event)
	}
	if string(body.Payload) != payload {
		t.Fatalf("payload = %s, want the harness JSON verbatim", body.Payload)
	}
}

// TestRunHookDropsAnUnusablePayloadButStillReports: a body that is not JSON, or
// is over the cap, still reports the event. The router then treats the missing
// session id as a mismatch, which fails to hold - the safe side.
func TestRunHookDropsAnUnusablePayloadButStillReports(t *testing.T) {
	tests := []struct {
		name  string
		stdin io.Reader
	}{
		{name: "not json", stdin: strings.NewReader("this is not json")},
		{name: "empty", stdin: strings.NewReader("")},
		{name: "over the cap", stdin: strings.NewReader(`{"prompt":"` + strings.Repeat("x", maxHookStdinBytes+16) + `"}`)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server, captured := hookServer(t, http.StatusNoContent, 0)
			dir := t.TempDir()
			writeRawHookFile(t, dir, "alpha", server.URL, "token")

			var err error
			stdout, _ := captureRunHook(t, func() {
				err = RunHook([]string{
					"--agent", "alpha",
					"--event", "Stop",
					"--session-dir", dir,
				}, tc.stdin)
			})

			if err != nil {
				t.Fatalf("RunHook: %v", err)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing", stdout)
			}
			if !captured.received {
				t.Fatal("the event was not reported at all")
			}

			var body map[string]any
			if err := json.Unmarshal(captured.body, &body); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			if body["event"] != "Stop" {
				t.Fatalf("event = %v, want Stop", body["event"])
			}
			if _, hasPayload := body["payload"]; hasPayload {
				t.Fatalf("body carries a payload it should have dropped: %s", captured.body)
			}
		})
	}
}

func TestHookTargetValidation(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "ipv4", raw: "http://127.0.0.1:4100", want: "http://127.0.0.1:4100/hook", ok: true},
		{name: "ipv4 with root path", raw: "http://127.0.0.1:4100/", want: "http://127.0.0.1:4100/hook", ok: true},
		{name: "ipv6", raw: "http://[::1]:4100", want: "http://[::1]:4100/hook", ok: true},
		{name: "https", raw: "https://127.0.0.1:4100", ok: false},
		{name: "other scheme", raw: "ftp://127.0.0.1:4100", ok: false},
		{name: "non-loopback", raw: "http://example.com:4100", ok: false},
		{name: "loopback name", raw: "http://localhost:4100", ok: false},
		{name: "path", raw: "http://127.0.0.1:4100/api", ok: false},
		{name: "userinfo", raw: "http://user:pw@127.0.0.1:4100", ok: false},
		{name: "no port", raw: "http://127.0.0.1", ok: false},
		{name: "query", raw: "http://127.0.0.1:4100?x=1", ok: false},
		{name: "garbage", raw: "://", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hookTarget(tc.raw)
			if ok != tc.ok {
				t.Fatalf("hookTarget(%q) ok = %v, want %v", tc.raw, ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("hookTarget(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestReadHookPayloadCap(t *testing.T) {
	exactly := strings.Repeat("x", maxHookStdinBytes)
	if _, ok := readHookPayload(strings.NewReader(exactly)); ok {
		t.Fatal("a non-JSON payload at the cap was accepted")
	}

	payload := `{"prompt":"` + strings.Repeat("x", maxHookStdinBytes/2) + `"}`
	if _, ok := readHookPayload(strings.NewReader(payload)); !ok {
		t.Fatal("a valid payload under the cap was rejected")
	}
}

// writeRawHookFile writes a hook file directly, so a test can name a server URL
// that WriteHookFile would never produce.
func writeRawHookFile(t *testing.T, dir, agent, rawURL, token string) {
	t.Helper()

	content, err := json.Marshal(hookFile{URL: rawURL, Token: token})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(dir, "hook-"+agent+".json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write hook file: %v", err)
	}
}
