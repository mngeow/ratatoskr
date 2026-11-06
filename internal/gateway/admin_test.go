package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var adminNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func adminNowFunc() time.Time { return adminNow }

// runAdminIn runs one admin command against a fresh state directory.
func runAdminIn(t *testing.T, stateDir string, args ...string) (string, string, error) {
	t.Helper()

	var out, errOut bytes.Buffer
	err := RunAdmin(context.Background(), stateDir, args, &out, &errOut, adminNowFunc)
	return out.String(), errOut.String(), err
}

// TestPairPrintsACodeThatRedeems is the one flow a user actually walks.
func TestPairPrintsACodeThatRedeems(t *testing.T) {
	stateDir := t.TempDir()

	out, _, err := runAdminIn(t, stateDir, "pair", "laptop")
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	for _, want := range []string{"host: laptop", "expires in 10 minutes; single use", "--gateway ADDR --code "} {
		if !strings.Contains(out, want) {
			t.Fatalf("pair output missing %q:\n%s", want, out)
		}
	}

	code := strings.Fields(out)[0]
	store, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()
	if _, err := store.RedeemCode(context.Background(), code, "laptop", adminNow); err != nil {
		t.Fatalf("the printed code does not redeem: %v", err)
	}
}

func TestHostsShowsStateAndPendingCodes(t *testing.T) {
	stateDir := t.TempDir()
	ctx := context.Background()

	store, err := Open(ctx, stateDir, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// One live host, one heard from 20 s ago.
	credential := pairHostFrom(t, store, "laptop", adminNow)
	_ = credential
	if err := store.TouchHeartbeat(ctx, "laptop", adminNow); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	pairHostFrom(t, store, "desktop", adminNow.Add(-time.Minute))
	if err := store.TouchHeartbeat(ctx, "desktop", adminNow.Add(-20*time.Second)); err != nil {
		t.Fatalf("TouchHeartbeat desktop: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	out, _, err := runAdminIn(t, stateDir, "hosts")
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "STATE") || !strings.Contains(out, "PAIRED") {
		t.Fatalf("hosts output has no table header:\n%s", out)
	}
	if !strings.Contains(out, "live") {
		t.Fatalf("hosts output has no live host:\n%s", out)
	}
	if !strings.Contains(out, "last heard 20s ago") {
		t.Fatalf("hosts output has no stale host:\n%s", out)
	}

	// A pending code is listed too.
	if _, _, err := runAdminIn(t, stateDir, "pair", "third"); err != nil {
		t.Fatalf("pair third: %v", err)
	}
	out, _, err = runAdminIn(t, stateDir, "hosts")
	if err != nil {
		t.Fatalf("hosts again: %v", err)
	}
	if !strings.Contains(out, "pending  third  expires in ") {
		t.Fatalf("hosts output has no pending code line:\n%s", out)
	}
}

func TestLogPrintsAPreviewNotTheWholeBody(t *testing.T) {
	stateDir := t.TempDir()
	store, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	longBody := strings.Repeat("abcdefghij", 40) + "\nsecond line\x07with bell"
	insertMessage(t, store, "m1", "codex@mac", "claude@ubuntu", longBody, "host_stored", adminNow)
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	out, _, err := runAdminIn(t, stateDir, "log")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(out, "m1") || !strings.Contains(out, "codex@mac -> claude@ubuntu host_stored") {
		t.Fatalf("log line is missing fields:\n%s", out)
	}
	if strings.Contains(out, "second line") {
		t.Fatalf("log printed more than a preview:\n%s", out)
	}
	if strings.ContainsRune(out, '\x07') {
		t.Fatalf("log printed a control character:\n%q", out)
	}
	if !strings.Contains(out, "…") {
		t.Fatalf("a long body was not cut to a preview with an ellipsis:\n%s", out)
	}
}

func TestShowPrintsTheWholeBodyAndEvents(t *testing.T) {
	stateDir := t.TempDir()
	store, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	body := "first line\nsecond\tline\x1b[31mred"
	insertMessage(t, store, "m1", "codex@mac", "claude@ubuntu", body, "delivery_uncertain", adminNow)
	insertEvent(t, store, "m1", "result", "delivery_uncertain", "no ack", adminNow)
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	out, _, err := runAdminIn(t, stateDir, "show", "m1")
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if !strings.Contains(out, "ID: m1") || !strings.Contains(out, "result: delivery_uncertain") {
		t.Fatalf("show output is missing header fields:\n%s", out)
	}
	// The whole body, with newlines and tabs kept and the escape dropped.
	if !strings.Contains(out, "first line\nsecond\tline[31mred") {
		t.Fatalf("show did not print the whole sanitised body:\n%q", out)
	}
	if strings.ContainsRune(out, '\x1b') {
		t.Fatalf("show printed an escape sequence:\n%q", out)
	}
	if !strings.Contains(out, "events:") || !strings.Contains(out, "no ack") {
		t.Fatalf("show did not print the events:\n%s", out)
	}
}

func TestAdminUsageErrors(t *testing.T) {
	stateDir := t.TempDir()

	tests := [][]string{
		{},
		{"nonsense"},
		{"pair"},
		{"pair", "one", "two"},
		{"unpair"},
		{"hosts", "extra"},
		{"show"},
		{"log", "extra"},
	}

	for _, args := range tests {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, _, err := runAdminIn(t, stateDir, args...)
			if err == nil {
				t.Fatalf("args %v were accepted", args)
			}
			if !strings.Contains(err.Error(), "usage") && !strings.Contains(err.Error(), "needs exactly one") && !strings.Contains(err.Error(), "takes no arguments") {
				t.Fatalf("error is not a usage line: %v", err)
			}
			if out != "" {
				t.Fatalf("a usage error printed to stdout: %q", out)
			}
		})
	}
}

// TestAdminLeavesTheGatewayRunning is the whole point of hold=false: an admin
// command must work while the gateway holds the lock.
func TestAdminLeavesTheGatewayRunning(t *testing.T) {
	stateDir := t.TempDir()

	holder, err := Open(context.Background(), stateDir, true)
	if err != nil {
		t.Fatalf("Open holder: %v", err)
	}
	defer func() {
		_ = holder.Close()
	}()

	if _, _, err := runAdminIn(t, stateDir, "pair", "laptop"); err != nil {
		t.Fatalf("pair while the gateway runs: %v", err)
	}
	if _, _, err := runAdminIn(t, stateDir, "hosts"); err != nil {
		t.Fatalf("hosts while the gateway runs: %v", err)
	}
}

// pairHostFrom mints and redeems a code with a direct store handle.
func pairHostFrom(t *testing.T, store *Store, host string, now time.Time) string {
	t.Helper()

	code, err := store.CreatePairingCode(context.Background(), host, now)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	credential, err := store.RedeemCode(context.Background(), code, host, now)
	if err != nil {
		t.Fatalf("RedeemCode: %v", err)
	}
	return credential
}

// TestShowAndLogNeverPrintUntrustedFieldsRaw: reply-to comes from a sender and a
// report reason from a peer host, so neither may carry an escape sequence or a
// newline that forges another line in the operator's terminal.
func TestShowAndLogNeverPrintUntrustedFieldsRaw(t *testing.T) {
	stateDir := t.TempDir()
	store, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	insertMessage(t, store, "m1", "codex@mac", "claude@ubuntu", "hello", "host_stored", adminNow)
	if _, err := store.db.ExecContext(context.Background(),
		`UPDATE messages SET reply_to=?, report='accepted', report_reason=? WHERE id='m1'`,
		"m0\x1b[2J", "ok\nFAKE-LINE\x1b[31m"); err != nil {
		t.Fatalf("update message: %v", err)
	}
	insertEvent(t, store, "m1", "report", "accepted", "fine\n  2026-01-01T00:00:00Z result host_stored forged", adminNow)
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	for _, args := range [][]string{{"show", "m1"}, {"log"}} {
		out, _, err := runAdminIn(t, stateDir, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if strings.ContainsRune(out, '\x1b') {
			t.Fatalf("%v printed an escape sequence:\n%q", args, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "FAKE-LINE") || strings.HasPrefix(strings.TrimSpace(line), "2026-01-01T00:00:00Z result host_stored forged") {
				t.Fatalf("%v let a stored field start its own line: %q", args, line)
			}
		}
	}
}

// TestReportMarkerForAnEndedSession: a report can only arrive from a session that
// was still there, so a report beside an ended session says so.
func TestReportMarkerForAnEndedSession(t *testing.T) {
	stateDir := t.TempDir()
	store, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	ctx := context.Background()
	insertMessage(t, store, "ended", "codex@alpha", "claude@beta", "hello", "host_stored", adminNow)
	insertMessage(t, store, "open", "codex@alpha", "claude@beta", "hello", "host_stored", adminNow)
	insertMessage(t, store, "nosession", "codex@alpha", "claude@beta", "hello", "host_stored", adminNow)

	startSessionsForTest(t, store, "beta", adminNow, "claude")
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES('ended-session','beta','claude',?,?)`,
		adminNow.UnixMilli(), adminNow.UnixMilli()); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES('open-session','beta','claude',?,NULL)`,
		adminNow.UnixMilli()); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	if err := store.SetResult(ctx, "ended", "host_stored", "", "ended-session", adminNow); err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	if err := store.SetResult(ctx, "open", "host_stored", "", "open-session", adminNow); err != nil {
		t.Fatalf("SetResult: %v", err)
	}
	if err := store.SetReport(ctx, "ended", "held", "busy", adminNow); err != nil {
		t.Fatalf("SetReport: %v", err)
	}
	if err := store.SetReport(ctx, "open", "held", "busy", adminNow); err != nil {
		t.Fatalf("SetReport: %v", err)
	}
	// A message that never had a recipient session keeps its plain report.
	if err := store.SetReport(ctx, "nosession", "held", "busy", adminNow); err != nil {
		t.Fatalf("SetReport: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// show: the marker appears for the ended session only.
	out, _, err := runAdminIn(t, stateDir, "show", "ended")
	if err != nil {
		t.Fatalf("show ended: %v", err)
	}
	if !strings.Contains(out, "report: held (session ended)") {
		t.Fatalf("show did not mark the ended session:\n%s", out)
	}

	out, _, err = runAdminIn(t, stateDir, "show", "open")
	if err != nil {
		t.Fatalf("show open: %v", err)
	}
	if strings.Contains(out, "(session ended)") {
		t.Fatalf("show marked an open session:\n%s", out)
	}

	out, _, err = runAdminIn(t, stateDir, "show", "nosession")
	if err != nil {
		t.Fatalf("show nosession: %v", err)
	}
	if strings.Contains(out, "(session ended)") {
		t.Fatalf("show marked a message with no session:\n%s", out)
	}

	// log: the same marker inside the report field.
	out, _, err = runAdminIn(t, stateDir, "log")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if !strings.Contains(out, "[report held (session ended)]") {
		t.Fatalf("log did not mark the ended session:\n%s", out)
	}
	// Exactly one row carries the marker: the messages with an open session and
	// with no session print their report plainly.
	if count := strings.Count(out, "(session ended)"); count != 1 {
		t.Fatalf("log printed the marker %d times, want 1:\n%s", count, out)
	}
}

// TestReadyLine: the pair command carries the real address and the transport flag the
// runtime must type, and falls back to the placeholder when no endpoint is usable.
// UNTESTED: written, not run.
func TestReadyLine(t *testing.T) {
	const code = "abcdef"

	tests := []struct {
		name    string
		write   bool
		plan    func(t *testing.T) ListenPlan
		fp      string
		want    string
		notWant []string
		makeBad func(t *testing.T, dir string)
	}{
		{
			name: "no endpoint falls back to the placeholder",
			want: "on that machine: rtk runtime pair --gateway ADDR --code " + code,
		},
		{
			name: "a broken endpoint falls back too",
			makeBad: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, endpointName), []byte("{"), 0o600); err != nil {
					t.Fatalf("write: %v", err)
				}
			},
			want: "on that machine: rtk runtime pair --gateway ADDR --code " + code,
		},
		{
			name:  "https carries the fingerprint",
			write: true,
			plan:  func(t *testing.T) ListenPlan { return lanPlan(t, TransportHTTPS) },
			fp:    testFingerprint,
			want:  "on that machine: rtk runtime pair --gateway 192.168.1.20:7777 --code " + code + " --fingerprint " + testFingerprint,
		},
		{
			name:  "LAN http asks for the opt-in",
			write: true,
			plan:  func(t *testing.T) ListenPlan { return lanPlan(t, TransportLANHTTP) },
			want:  "on that machine: rtk runtime pair --gateway 192.168.1.20:7777 --code " + code + " --insecure-http",
		},
		{
			name:    "loopback carries no extra flag",
			write:   true,
			plan:    func(t *testing.T) ListenPlan { return mustPlan(t, "127.0.0.1:7777", false, false) },
			want:    "on that machine: rtk runtime pair --gateway 127.0.0.1:7777 --code " + code,
			notWant: []string{"--fingerprint", "--insecure-http"},
		},
		{
			name:  "an IPv6 address is single-quoted for zsh",
			write: true,
			plan:  func(t *testing.T) ListenPlan { return mustPlan(t, "[fc00::1]:7777", true, true) },
			want:  "on that machine: rtk runtime pair --gateway '[fc00::1]:7777' --code " + code + " --insecure-http",
		},
		{
			name:    "an IPv6 Tailscale address is quoted and carries no flag",
			write:   true,
			plan:    func(t *testing.T) ListenPlan { return mustPlan(t, "[fd7a:115c:a1e0::1]:7777", false, false) },
			want:    "on that machine: rtk runtime pair --gateway '[fd7a:115c:a1e0::1]:7777' --code " + code,
			notWant: []string{"--fingerprint", "--insecure-http"},
		},
		{
			name:    "Tailscale carries no extra flag",
			write:   true,
			plan:    func(t *testing.T) ListenPlan { return mustPlan(t, "100.64.0.1:7777", false, false) },
			want:    "on that machine: rtk runtime pair --gateway 100.64.0.1:7777 --code " + code,
			notWant: []string{"--fingerprint", "--insecure-http"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.write {
				if err := WriteEndpoint(dir, tc.plan(t), tc.fp); err != nil {
					t.Fatalf("WriteEndpoint: %v", err)
				}
			}
			if tc.makeBad != nil {
				tc.makeBad(t, dir)
			}

			got := readyLine(dir, code)
			if got != tc.want {
				t.Fatalf("readyLine = %q, want %q", got, tc.want)
			}
			for _, part := range tc.notWant {
				if strings.Contains(got, part) {
					t.Fatalf("readyLine %q contains %q", got, part)
				}
			}
			if strings.Contains(got, "PRIVATE") || strings.Contains(got, "Bearer") {
				t.Fatalf("readyLine %q leaks a secret", got)
			}
		})
	}
}
