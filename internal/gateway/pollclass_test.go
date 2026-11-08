package gateway

// Untested: under GR-3 nothing here has been run. Every case is a table row; no HTTP exchange, socket or
// clock is involved.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// fakeNetError is a net.Error with a settable Timeout.
type fakeNetError struct {
	timeout bool
	text    string
}

// Error renders the error.
func (e fakeNetError) Error() string { return e.text }

// Timeout reports whether this error is a deadline.
func (e fakeNetError) Timeout() bool { return e.timeout }

// Temporary is part of net.Error's historical shape.
func (e fakeNetError) Temporary() bool { return false }

// TestClassifyPollTable walks every rule in order.
func TestClassifyPollTable(t *testing.T) {
	addressText := "dial tcp 100.64.0.9:7778: refused"

	tests := []struct {
		name        string
		in          PollInput
		wantOutcome string
		wantReason  string
		wantParse   bool
	}{
		{
			name:        "pin mismatch",
			in:          PollInput{Err: ErrPinMismatch},
			wantOutcome: protocol.PollCertificate,
			wantReason:  reasonCertificate,
		},
		{
			name:        "pin mismatch wrapped",
			in:          PollInput{Err: fmt.Errorf("poll: %w", fmt.Errorf("tls: %w", ErrPinMismatch))},
			wantOutcome: protocol.PollCertificate,
			wantReason:  reasonCertificate,
		},
		{
			name:        "certificate verification error wrapped in a url.Error",
			in:          PollInput{Err: &url.Error{Op: "Get", URL: "https://100.64.0.9:7778/.well-known/agent-card.json", Err: &tls.CertificateVerificationError{}}},
			wantOutcome: protocol.PollCertificate,
			wantReason:  reasonCertificate,
		},
		{
			name:        "unknown authority",
			in:          PollInput{Err: x509.UnknownAuthorityError{}},
			wantOutcome: protocol.PollCertificate,
			wantReason:  reasonCertificate,
		},
		{
			name:        "hostname error",
			in:          PollInput{Err: x509.HostnameError{Host: "example"}},
			wantOutcome: protocol.PollCertificate,
			wantReason:  reasonCertificate,
		},
		{
			name:        "certificate invalid",
			in:          PollInput{Err: x509.CertificateInvalidError{}},
			wantOutcome: protocol.PollCertificate,
			wantReason:  reasonCertificate,
		},
		{
			name:        "address refused",
			in:          PollInput{Err: ErrAddressRefused},
			wantOutcome: protocol.PollRefused,
			wantReason:  reasonAddress,
		},
		{
			name:        "context deadline",
			in:          PollInput{Err: context.DeadlineExceeded},
			wantOutcome: protocol.PollTimeout,
			wantReason:  reasonTimeout,
		},
		{
			name:        "wrapped deadline",
			in:          PollInput{Err: fmt.Errorf("poll: %w", context.DeadlineExceeded)},
			wantOutcome: protocol.PollTimeout,
			wantReason:  reasonTimeout,
		},
		{
			name:        "net error timeout",
			in:          PollInput{Err: fakeNetError{timeout: true, text: addressText}},
			wantOutcome: protocol.PollTimeout,
			wantReason:  reasonTimeout,
		},
		{
			name:        "url error wrapping a timeout",
			in:          PollInput{Err: &url.Error{Op: "Get", URL: "http://100.64.0.9:7778", Err: fakeNetError{timeout: true, text: addressText}}},
			wantOutcome: protocol.PollTimeout,
			wantReason:  reasonTimeout,
		},
		{
			name:        "connection refused",
			in:          PollInput{Err: errors.New(addressText)},
			wantOutcome: protocol.PollRefused,
			wantReason:  reasonNotMade,
		},
		{
			name:        "cancelled is not a timeout",
			in:          PollInput{Err: context.Canceled},
			wantOutcome: protocol.PollRefused,
			wantReason:  reasonNotMade,
		},
		{
			name:        "no status and no error",
			in:          PollInput{},
			wantOutcome: protocol.PollRefused,
			wantReason:  reasonNotMade,
		},
		{
			name:        "a negative status is nonsense",
			in:          PollInput{Status: -1},
			wantOutcome: protocol.PollRefused,
			wantReason:  reasonNotMade,
		},
		{
			name:        "301",
			in:          PollInput{Status: 301},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "302",
			in:          PollInput{Status: 302},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "307",
			in:          PollInput{Status: 307},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "308",
			in:          PollInput{Status: 308},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "300",
			in:          PollInput{Status: 300},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "303",
			in:          PollInput{Status: 303},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "305",
			in:          PollInput{Status: 305},
			wantOutcome: protocol.PollRedirect,
			wantReason:  reasonRedirect,
		},
		{
			name:        "304 is a status with the compatibility reason, never success or a redirect",
			in:          PollInput{Status: 304},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reason304,
		},
		{
			name:        "200 json",
			in:          PollInput{Status: 200, ContentType: "application/json"},
			wantOutcome: protocol.PollOK,
			wantParse:   true,
		},
		{
			name:        "200 json with utf-8",
			in:          PollInput{Status: 200, ContentType: "application/json; charset=utf-8"},
			wantOutcome: protocol.PollOK,
			wantParse:   true,
		},
		{
			name:        "200 json with a quoted utf-8",
			in:          PollInput{Status: 200, ContentType: "application/json; charset=\"UTF-8\""},
			wantOutcome: protocol.PollOK,
			wantParse:   true,
		},
		{
			name:        "200 json with another charset",
			in:          PollInput{Status: 200, ContentType: "application/json; charset=ISO-8859-1"},
			wantOutcome: protocol.PollInvalid,
			wantReason:  reasonContentType,
		},
		{
			name:        "200 problem+json",
			in:          PollInput{Status: 200, ContentType: "application/problem+json"},
			wantOutcome: protocol.PollOK,
			wantParse:   true,
		},
		{
			name:        "200 text/html",
			in:          PollInput{Status: 200, ContentType: "text/html"},
			wantOutcome: protocol.PollInvalid,
			wantReason:  reasonContentType,
		},
		{
			name:        "200 text/json is not application/*+json",
			in:          PollInput{Status: 200, ContentType: "text/json"},
			wantOutcome: protocol.PollInvalid,
			wantReason:  reasonContentType,
		},
		{
			name:        "200 with no content type",
			in:          PollInput{Status: 200},
			wantOutcome: protocol.PollInvalid,
			wantReason:  reasonContentType,
		},
		{
			name:        "200 with a garbage content type",
			in:          PollInput{Status: 200, ContentType: ";;;"},
			wantOutcome: protocol.PollInvalid,
			wantReason:  reasonContentType,
		},
		{
			name:        "200 overflow",
			in:          PollInput{Status: 200, ContentType: "application/json", BodyOverflow: true},
			wantOutcome: protocol.PollOversize,
			wantReason:  reasonOversize,
		},
		{
			name:        "204",
			in:          PollInput{Status: 204},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonHTTPStatus + "204",
		},
		{
			name:        "206",
			in:          PollInput{Status: 206},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonHTTPStatus + "206",
		},
		{
			name:        "401",
			in:          PollInput{Status: 401},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonHTTPStatus + "401",
		},
		{
			name:        "503 with a JSON body is no card, and is not parsed",
			in:          PollInput{Status: 503, ContentType: "application/json"},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonNoCard,
		},
		{
			name:        "404",
			in:          PollInput{Status: 404},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonHTTPStatus + "404",
		},
		{
			name:        "500",
			in:          PollInput{Status: 500},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonHTTPStatus + "500",
		},
		{
			name:        "101",
			in:          PollInput{Status: 101},
			wantOutcome: protocol.PollHTTPStatus,
			wantReason:  reasonHTTPStatus + "101",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyPoll(tc.in)
			if got.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.ParseBody != tc.wantParse {
				t.Fatalf("parseBody = %t, want %t", got.ParseBody, tc.wantParse)
			}
			if got.ParseBody && got.Outcome != protocol.PollOK {
				t.Fatalf("ParseBody is set for %q", got.Outcome)
			}
		})
	}
}

// TestClassifyPollReasonsLeakNothing walks a grid of inputs and asserts that no reason can quote an
// address, a port or the injected error's text.
func TestClassifyPollReasonsLeakNothing(t *testing.T) {
	injected := "dial tcp 100.64.0.9:7778: refused"

	inputs := []PollInput{
		{Err: errors.New(injected)},
		{Err: fmt.Errorf("get %s: %w", injected, context.DeadlineExceeded)},
		{Err: &url.Error{Op: "Get", URL: "http://100.64.0.9:7778", Err: errors.New(injected)}},
		{Err: ErrPinMismatch},
		{Err: ErrAddressRefused},
		{Status: 301},
		{Status: 304},
		{Status: 200, ContentType: "text/html"},
		{Status: 200, BodyOverflow: true},
		{Status: 418},
		{Status: 0},
	}

	// A dotted quad is an address; a colon directly followed by digits is a port. "HTTP 404" has neither.
	addressPattern := regexp.MustCompile(`\b\d{1,3}(?:\.\d{1,3}){3}\b`)
	portPattern := regexp.MustCompile(`:\d`)

	for index, input := range inputs {
		reason := ClassifyPoll(input).Reason
		if addressPattern.MatchString(reason) {
			t.Fatalf("input %d: reason %q contains an address", index, reason)
		}
		if portPattern.MatchString(reason) {
			t.Fatalf("input %d: reason %q contains a port", index, reason)
		}
		for _, fragment := range []string{"100.64", injected, "tcp"} {
			if strings.Contains(reason, fragment) {
				t.Fatalf("input %d: reason %q contains %q", index, reason, fragment)
			}
		}
	}
}

// TestSucceededAndRejection: the truth tables, and an unknown outcome is neither.
func TestSucceededAndRejection(t *testing.T) {
	outcomes := []string{
		protocol.PollOK,
		protocol.PollTimeout,
		protocol.PollRefused,
		protocol.PollRedirect,
		protocol.PollHTTPStatus,
		protocol.PollInvalid,
		protocol.PollOversize,
		protocol.PollCertificate,
		"",
		"something-else",
	}

	wantSucceeded := map[string]bool{protocol.PollOK: true}
	wantRejection := map[string]bool{
		protocol.PollInvalid: true, protocol.PollOversize: true, protocol.PollCertificate: true,
	}

	for _, outcome := range outcomes {
		if got, want := Succeeded(outcome), wantSucceeded[outcome]; got != want {
			t.Fatalf("Succeeded(%q) = %t, want %t", outcome, got, want)
		}
		if got, want := Rejection(outcome), wantRejection[outcome]; got != want {
			t.Fatalf("Rejection(%q) = %t, want %t", outcome, got, want)
		}
	}
}

// TestOfflineAfter: the longer of fifteen minutes and four intervals, and the no-overlap relation.
func TestOfflineAfter(t *testing.T) {
	tests := []struct {
		interval time.Duration
		want     time.Duration
	}{
		{interval: 2 * time.Second, want: 15 * time.Minute},
		{interval: 5 * time.Second, want: 15 * time.Minute},
		{interval: 10 * time.Second, want: 15 * time.Minute},
		{interval: time.Minute, want: 15 * time.Minute},
		{interval: 7*time.Minute + 30*time.Second, want: 30 * time.Minute},
		{interval: 20 * time.Minute, want: 80 * time.Minute},
		{interval: time.Hour, want: 4 * time.Hour},
	}

	for _, tc := range tests {
		if got := OfflineAfter(tc.interval); got != tc.want {
			t.Fatalf("OfflineAfter(%v) = %v, want %v", tc.interval, got, tc.want)
		}
		// Fresh and offline can never overlap: the offline threshold is always beyond twice the interval.
		if OfflineAfter(tc.interval) <= 2*tc.interval {
			t.Fatalf("interval %v: offline %v is not beyond fresh %v", tc.interval, OfflineAfter(tc.interval), 2*tc.interval)
		}
	}
}

// TestFreshness: every state, and every boundary from both sides.
func TestFreshness(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		lastOK     time.Time
		haveCard   bool
		lastResult string
		interval   time.Duration
		want       string
	}{
		{
			name: "never polled", haveCard: false, interval: time.Minute,
			want: protocol.CardNone,
		},
		{
			name: "a stored card with no good poll", haveCard: true, interval: time.Minute,
			want: protocol.CardNone,
		},
		{
			name: "rejected outranks a fresh poll", lastOK: now.Add(-time.Second), haveCard: true,
			lastResult: protocol.PollInvalid, interval: time.Minute,
			want: protocol.CardRejected,
		},
		{
			name: "certificate is a rejection too", lastOK: now.Add(-time.Second), haveCard: true,
			lastResult: protocol.PollCertificate, interval: time.Minute,
			want: protocol.CardRejected,
		},
		{
			name: "just inside fresh at the interval", lastOK: now.Add(-2 * time.Minute), haveCard: true,
			interval: time.Minute, want: protocol.CardFresh,
		},
		{
			name: "just outside fresh", lastOK: now.Add(-2*time.Minute - time.Second), haveCard: true,
			interval: time.Minute, want: protocol.CardStale,
		},
		{
			name: "just inside offline", lastOK: now.Add(-15*time.Minute + time.Second), haveCard: true,
			interval: time.Minute, want: protocol.CardStale,
		},
		{
			name: "at the offline boundary", lastOK: now.Add(-15 * time.Minute), haveCard: true,
			interval: time.Minute, want: protocol.CardOffline,
		},
		{
			name: "long interval, just outside fresh", lastOK: now.Add(-41 * time.Minute), haveCard: true,
			interval: 20 * time.Minute, want: protocol.CardStale,
		},
		{
			name: "long interval, at offline", lastOK: now.Add(-80 * time.Minute), haveCard: true,
			interval: 20 * time.Minute, want: protocol.CardOffline,
		},
		{
			name: "a future last-good time is not extra fresh", lastOK: now.Add(time.Hour), haveCard: true,
			interval: time.Minute, want: protocol.CardFresh,
		},
		{
			name: "zero interval falls back to the default", lastOK: now.Add(-90 * time.Second), haveCard: true,
			interval: 0, want: protocol.CardStale,
		},
		{
			name: "a timeout is not a rejection", lastOK: now.Add(-10 * time.Minute), haveCard: true,
			lastResult: protocol.PollTimeout, interval: time.Minute, want: protocol.CardStale,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Freshness(now, tc.lastOK, tc.haveCard, tc.lastResult, tc.interval)
			if got != tc.want {
				t.Fatalf("Freshness = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClassifyEveryRedirectCodeButNotModified: for every code from 300 to 399 only 304 is not a redirect,
// and a 304 is an http-status with the compatibility reason: never success, never a redirect, never parsed.
func TestClassifyEveryRedirectCodeButNotModified(t *testing.T) {
	for status := 300; status <= 399; status++ {
		got := ClassifyPoll(PollInput{Status: status})
		want, reason := protocol.PollRedirect, reasonRedirect
		if status == 304 {
			want, reason = protocol.PollHTTPStatus, reason304
		}
		if got.Outcome != want || got.Reason != reason || got.ParseBody || Succeeded(got.Outcome) {
			t.Fatalf("status %d = %+v, want outcome %q reason %q", status, got, want, reason)
		}
	}
}

// TestFreshWindow: two intervals, never under fifteen seconds, so one lost poll at the 5 s default does
// not flip a host to stale.
func TestFreshWindow(t *testing.T) {
	for _, tc := range []struct{ interval, want time.Duration }{
		{0, 15 * time.Second},
		{2 * time.Second, 15 * time.Second},
		{5 * time.Second, 15 * time.Second},
		{7500 * time.Millisecond, 15 * time.Second},
		{10 * time.Second, 20 * time.Second},
		{time.Minute, 2 * time.Minute},
	} {
		if got := FreshWindow(tc.interval); got != tc.want {
			t.Fatalf("FreshWindow(%v) = %v, want %v", tc.interval, got, tc.want)
		}
	}
}

// TestFreshnessAtTheDefaultInterval: fresh at 15 s, stale just after, offline at fifteen minutes as before.
func TestFreshnessAtTheDefaultInterval(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{0, protocol.CardFresh},
		{15 * time.Second, protocol.CardFresh},
		{15*time.Second + time.Millisecond, protocol.CardStale},
		{15*time.Minute - time.Second, protocol.CardStale},
		{15 * time.Minute, protocol.CardOffline},
	} {
		if got := Freshness(now, now.Add(-tc.age), true, protocol.PollOK, DefaultCardPollInterval); got != tc.want {
			t.Fatalf("age %v = %q, want %q", tc.age, got, tc.want)
		}
	}
}
