package main

// Untested: under GR-3 these tests have never been run. They are pure: no socket, and the only files
// touched live in t.TempDir().

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeToken writes a token file with mode 0600 and returns its path.
func writeToken(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gateway-token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the token file: %v", err)
	}
	return path
}

// captureLog sends the log to a buffer for the duration of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buffer := &bytes.Buffer{}
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return buffer
}

// TestResolveTokenSource covers every startup rule.
func TestResolveTokenSource(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name       string
		filePath   string
		envValue   string
		noAuth     bool
		wantName   string
		wantErrIs  error
		wantErrAny bool
	}{
		{name: "neither source", wantErrIs: errNoToken},
		{name: "whitespace environment value", envValue: "   \n", wantErrIs: errNoToken},
		{name: "no-auth alone", noAuth: true, wantName: ""},
		{name: "no-auth with a token file", noAuth: true, filePath: "/tmp/whatever", wantErrIs: errNoAuthWithAuth},
		{name: "no-auth with an environment value", noAuth: true, envValue: "secret", wantErrIs: errNoAuthWithAuth},
		{name: "file missing", filePath: "/nonexistent/gateway-token", wantErrIs: errTokenFileRead},
		{name: "file empty", filePath: writeToken(t, ""), wantErrIs: errTokenFileEmpty},
		{name: "file whitespace only", filePath: writeToken(t, "  \n\t\n"), wantErrIs: errTokenFileEmpty},
		{name: "file too large", filePath: writeToken(t, strings.Repeat("x", tokenFileLimit+1)), wantErrIs: errTokenFileLarge},
		{name: "file is a directory", filePath: dir, wantErrIs: errTokenFileKind},
		{name: "file placeholder", filePath: writeToken(t, "placeholder\n"), wantName: "file"},
		{name: "environment only", envValue: "secret", wantName: "environment"},
		{name: "both, the file wins", filePath: writeToken(t, "from-file"), envValue: "from-env", wantName: "file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			source, err := resolveTokenSource(tc.filePath, tc.envValue, tc.noAuth)
			if tc.wantErrIs != nil {
				if err == nil {
					t.Fatalf("resolveTokenSource = %+v, want an error", source)
				}
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("error = %v, want %v", err, tc.wantErrIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveTokenSource: %v", err)
			}
			if source.name != tc.wantName {
				t.Fatalf("source = %q, want %q", source.name, tc.wantName)
			}
			if source.auth() != (tc.wantName != "") {
				t.Fatalf("auth() = %v with source %q", source.auth(), source.name)
			}
		})
	}
}

// TestTokenSourceToken: trimming, the file re-read, and the environment staying put.
func TestTokenSourceToken(t *testing.T) {
	t.Run("the file is trimmed", func(t *testing.T) {
		path := writeToken(t, "  secret-value \r\n")
		source, err := resolveTokenSource(path, "", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}
		token, err := source.token()
		if err != nil {
			t.Fatalf("token: %v", err)
		}
		if token != "secret-value" {
			t.Fatalf("token = %q, want it trimmed", token)
		}
	})

	t.Run("the file is read again on every call", func(t *testing.T) {
		path := writeToken(t, "first")
		source, err := resolveTokenSource(path, "", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}

		if token, err := source.token(); err != nil || token != "first" {
			t.Fatalf("token = %q, %v", token, err)
		}

		// The owner pastes the real secret: no restart, no new source.
		if err := os.WriteFile(path, []byte("second"), 0o600); err != nil {
			t.Fatalf("rewriting the token file: %v", err)
		}
		if token, err := source.token(); err != nil || token != "second" {
			t.Fatalf("token = %q, %v; want the new value", token, err)
		}
	})

	t.Run("the environment is read once", func(t *testing.T) {
		source, err := resolveTokenSource("", "from-env", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}
		if token, err := source.token(); err != nil || token != "from-env" {
			t.Fatalf("token = %q, %v", token, err)
		}
	})

	t.Run("the file disappearing is an error", func(t *testing.T) {
		path := writeToken(t, "secret")
		source, err := resolveTokenSource(path, "", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("removing the token file: %v", err)
		}
		if _, err := source.token(); err == nil {
			t.Fatal("a missing token file was not an error")
		}
	})
}

// TestCheckBearer: exactly one header shape is accepted.
func TestCheckBearer(t *testing.T) {
	const token = "s3cr3t"

	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{name: "exact", header: "Bearer s3cr3t", want: true},
		{name: "lowercase scheme", header: "bearer s3cr3t"},
		{name: "uppercase scheme", header: "BEARER s3cr3t"},
		{name: "no token", header: "Bearer"},
		{name: "no space", header: "Bearers3cr3t"},
		{name: "trailing space", header: "Bearer s3cr3t "},
		{name: "leading space", header: " Bearer s3cr3t"},
		{name: "empty", header: ""},
		{name: "same length, different token", header: "Bearer s3cr3X"},
		{name: "a prefix", header: "Bearer s3cr3"},
		{name: "longer", header: "Bearer s3cr3ts"},
		{name: "another scheme", header: "Basic s3cr3t"},
		{name: "token repeated", header: "Bearer s3cr3t s3cr3t"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkBearer(tc.header, token); got != tc.want {
				t.Fatalf("checkBearer(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}

	t.Run("no expected token accepts nothing", func(t *testing.T) {
		if checkBearer("Bearer ", "") {
			t.Fatal("an empty expected token matched")
		}
		if checkBearer("Bearer", "") {
			t.Fatal("an empty expected token matched")
		}
	})
}

// handlerFunc adapts a function to http.Handler and reports whether it was reached.
type handlerFunc struct {
	called *bool
}

// ServeHTTP marks the handler reached.
func (h handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	*h.called = true
	w.WriteHeader(http.StatusOK)
}

// TestRequireBearer drives the gate directly.
func TestRequireBearer(t *testing.T) {
	t.Run("a valid request reaches the handler", func(t *testing.T) {
		reached := false
		gate := requireBearer(handlerFunc{called: &reached}, func() (string, error) { return "s3cr3t", nil })

		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("Authorization", "Bearer s3cr3t")
		recorder := httptest.NewRecorder()
		gate.ServeHTTP(recorder, request)

		if !reached {
			t.Fatal("the handler was not reached")
		}
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", recorder.Code)
		}
	})

	t.Run("a refused request is 401 and stops there", func(t *testing.T) {
		captureLog(t)
		reached := false
		gate := requireBearer(handlerFunc{called: &reached}, func() (string, error) { return "s3cr3t", nil })

		for _, header := range []string{"", "Bearer wrong", "bearer s3cr3t"} {
			reached = false
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			if header != "" {
				request.Header.Set("Authorization", header)
			}
			recorder := httptest.NewRecorder()
			gate.ServeHTTP(recorder, request)

			if reached {
				t.Fatalf("the handler was reached with header %q", header)
			}
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", recorder.Code)
			}
			if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q, want Bearer", got)
			}
			if got := recorder.Body.String(); got != "unauthorized\n" {
				t.Fatalf("body = %q, want unauthorized", got)
			}
		}
	})

	t.Run("a method other than POST is passed through", func(t *testing.T) {
		reached := false
		gate := requireBearer(handlerFunc{called: &reached}, func() (string, error) { return "s3cr3t", nil })

		request := httptest.NewRequest(http.MethodGet, "/", nil)
		recorder := httptest.NewRecorder()
		gate.ServeHTTP(recorder, request)

		if !reached {
			t.Fatal("a GET was refused by the gate instead of reaching the A2A handler")
		}
	})

	t.Run("a new token is picked up without a new gate", func(t *testing.T) {
		path := writeToken(t, "first")
		source, err := resolveTokenSource(path, "", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}

		reached := false
		gate := requireBearer(handlerFunc{called: &reached}, source.token)

		post := func(header string) int {
			reached = false
			request := httptest.NewRequest(http.MethodPost, "/", nil)
			request.Header.Set("Authorization", header)
			recorder := httptest.NewRecorder()
			gate.ServeHTTP(recorder, request)
			if reached != (recorder.Code == http.StatusOK) {
				t.Fatalf("reached = %v with status %d", reached, recorder.Code)
			}
			return recorder.Code
		}

		if code := post("Bearer first"); code != http.StatusOK {
			t.Fatalf("status = %d with the first token, want 200", code)
		}

		// The owner pastes the secret register printed. The next request already uses it.
		if err := os.WriteFile(path, []byte("second\n"), 0o600); err != nil {
			t.Fatalf("rewriting the token file: %v", err)
		}
		if code := post("Bearer second"); code != http.StatusOK {
			t.Fatalf("status = %d with the new token, want 200", code)
		}
		if code := post("Bearer first"); code != http.StatusUnauthorized {
			t.Fatalf("status = %d with the stale token, want 401", code)
		}
	})

	t.Run("an unreadable token file fails closed", func(t *testing.T) {
		captureLog(t)
		path := writeToken(t, "secret")
		source, err := resolveTokenSource(path, "", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatalf("removing the token file: %v", err)
		}

		reached := false
		gate := requireBearer(handlerFunc{called: &reached}, source.token)
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("Authorization", "Bearer secret")
		recorder := httptest.NewRecorder()
		gate.ServeHTTP(recorder, request)

		if reached {
			t.Fatal("a request was served while the token file was unreadable")
		}
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401 - never a 500", recorder.Code)
		}
	})

	t.Run("a token file turning unreadable mid-flight fails closed", func(t *testing.T) {
		captureLog(t)
		path := writeToken(t, "secret")
		source, err := resolveTokenSource(path, "", false)
		if err != nil {
			t.Fatalf("resolveTokenSource: %v", err)
		}

		reached := false
		gate := requireBearer(handlerFunc{called: &reached}, source.token)
		if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
			t.Fatalf("emptying the token file: %v", err)
		}

		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("Authorization", "Bearer secret")
		recorder := httptest.NewRecorder()
		gate.ServeHTTP(recorder, request)

		if reached || recorder.Code != http.StatusUnauthorized {
			t.Fatalf("reached = %v, status = %d; want a refusal", reached, recorder.Code)
		}
	})
}

// TestRefusalLog: the fixed text, at most once per second, and never the secret.
func TestRefusalLog(t *testing.T) {
	buffer := captureLog(t)

	const token = "s3cr3t-very-secret-value"
	reached := false
	gate := requireBearer(handlerFunc{called: &reached}, func() (string, error) { return token, nil }).(*bearerGate)

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	gate.now = func() time.Time { return now }

	refuse := func() {
		request := httptest.NewRequest(http.MethodPost, "/", nil)
		request.Header.Set("Authorization", "Bearer "+token+"-wrong")
		gate.ServeHTTP(httptest.NewRecorder(), request)
	}

	refuse()
	if got := buffer.String(); !strings.Contains(got, refusalLogText) {
		t.Fatalf("log = %q, want the fixed refusal text", got)
	}
	// The secret, the header and the path never reach the log.
	if strings.Contains(buffer.String(), token) {
		t.Fatalf("the log contains the token: %q", buffer.String())
	}

	// Within the same second nothing is added.
	buffer.Reset()
	now = now.Add(500 * time.Millisecond)
	refuse()
	refuse()
	if buffer.Len() != 0 {
		t.Fatalf("log = %q, want no line within one second", buffer.String())
	}

	// After the second, one line again - and still no secret.
	now = now.Add(time.Second)
	refuse()
	got := buffer.String()
	if !strings.Contains(got, refusalLogText) {
		t.Fatalf("log = %q, want a line after the interval", got)
	}
	if strings.Count(got, refusalLogText) != 1 {
		t.Fatalf("log = %q, want exactly one line", got)
	}
	if strings.Contains(got, token) {
		t.Fatalf("the log contains the token: %q", got)
	}
}

// TestAgentCardSecurity: the card says what is true of the running mode.
func TestAgentCardSecurity(t *testing.T) {
	t.Run("with auth", func(t *testing.T) {
		card := agentCard("http://127.0.0.1:7790/", "127.0.0.1:7790", true)
		encoded, err := json.Marshal(card)
		if err != nil {
			t.Fatalf("marshalling the card: %v", err)
		}
		body := string(encoded)
		if !strings.Contains(body, `"httpAuthSecurityScheme":{"scheme":"Bearer"}`) {
			t.Fatalf("card = %s, want the bearer scheme", body)
		}
		if !strings.Contains(body, `"securityRequirements":[{"schemes":{"bearer":{}}}]`) {
			t.Fatalf("card = %s, want the bearer requirement", body)
		}
	})

	t.Run("with no-auth", func(t *testing.T) {
		card := agentCard("http://127.0.0.1:7790/", "127.0.0.1:7790", false)
		encoded, err := json.Marshal(card)
		if err != nil {
			t.Fatalf("marshalling the card: %v", err)
		}
		body := string(encoded)
		for _, key := range []string{"securitySchemes", "securityRequirements", "httpAuthSecurityScheme"} {
			if strings.Contains(body, key) {
				t.Fatalf("card = %s, want no %s", body, key)
			}
		}
	})
}
