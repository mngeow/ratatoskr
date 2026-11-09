package gateway

// Tests for `rtk gateway participant pair|list|revoke`. Written, never run (GR-3): UNTESTED. Each case
// drives the command functions with a temporary store and buffers.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

var participantAdminNow = time.Unix(1700000000, 0)

// participantCmd runs one participant command against an existing store.
func participantCmd(t *testing.T, store *Store, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runParticipant(context.Background(), store, args, &out, &errOut, participantAdminNow)
	return out.String(), errOut.String(), err
}

// recordEndpoint writes the participant endpoint file a running gateway would have written.
func recordEndpoint(t *testing.T, store *Store, options ParticipantOptions) {
	t.Helper()
	plan, err := PlanParticipantListen(options)
	if err != nil || plan == nil {
		t.Fatalf("plan: %v %v", plan, err)
	}
	fingerprint, pin := "", ""
	if plan.Transport == TransportHTTPS {
		fingerprint, pin = testFingerprint, testSPKIPin
	}
	if err := WriteParticipantEndpoint(store.dir, *plan, fingerprint, pin); err != nil {
		t.Fatal(err)
	}
}

// TestParticipantCommandArgumentErrors.
func TestParticipantCommandArgumentErrors(t *testing.T) {
	store, _ := openStore(t, false)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"nothing", nil, "gateway: participant needs pair, list or revoke"},
		{"unknown", []string{"frobnicate"}, `gateway: unknown participant command "frobnicate"`},
		{"pair without an id", []string{"pair"}, "gateway: participant pair needs exactly one ID"},
		{"pair with two", []string{"pair", "a", "b"}, "gateway: participant pair needs exactly one ID"},
		{"list with an argument", []string{"list", "x"}, "gateway: participant list takes no arguments"},
		{"revoke without an id", []string{"revoke"}, "gateway: participant revoke needs exactly one ID"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := participantCmd(t, store, tc.args...)
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) || !strings.Contains(err.Error(), "usage:") {
				t.Fatalf("err = %v, want prefix %q and the usage", err, tc.want)
			}
		})
	}
}

// TestParticipantPairNeedsARunningListenerAndMintsNothingWithout.
func TestParticipantPairNeedsARunningListenerAndMintsNothingWithout(t *testing.T) {
	store, _ := openStore(t, false)
	out, errOut, err := participantCmd(t, store, "pair", "pi-lab")
	if err == nil || !strings.Contains(err.Error(), "not running") || !strings.Contains(err.Error(), FlagParticipantListen) ||
		!strings.Contains(err.Error(), "No code was made") {
		t.Fatalf("err = %v", err)
	}
	if out != "" || errOut != "" {
		t.Fatalf("output without a listener: %q %q", out, errOut)
	}
	var codes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM participant_pairing_codes`).Scan(&codes); err != nil || codes != 0 {
		t.Fatalf("a code was minted: %d %v", codes, err)
	}
}

// TestParticipantPairOverHTTPSPrintsBothLabelledPins: the code once and first, the URL, both pins labelled as
// different rules, and nothing else secret.
func TestParticipantPairOverHTTPSPrintsBothLabelledPins(t *testing.T) {
	store, _ := openStore(t, false)
	recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790"})

	out, errOut, err := participantCmd(t, store, "pair", "pi-lab")
	if err != nil {
		t.Fatal(err)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q", errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	code := lines[0]
	if code == "" || strings.ContainsAny(code, " \t") || strings.Count(out, code) != 1 {
		t.Fatalf("the first line is not a code printed once: %q", out)
	}
	for _, want := range []string{
		"participant: pi-lab",
		"expires in 10 minutes; single use",
		"participant API: https://100.64.0.1:7790",
		"certificate SHA-256 (the pin to use when your TLS library can pin a certificate): " + testFingerprint,
		"key pin (a different rule, for clients that can pin only a public key): " + testSPKIPin,
		"stale",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
	if strings.Contains(out, "WARNING") || strings.Contains(out, "rotation") {
		t.Fatalf("output has a warning or rotation note it should not: %q", out)
	}

	// The code redeems for that id.
	if _, id, err := store.RedeemParticipantCode(context.Background(), code, "", participantAdminNow); err != nil || id != "pi-lab" {
		t.Fatalf("the printed code does not redeem: %q %v", id, err)
	}
	// Pairing an existing active id says it is a rotation.
	out, _, err = participantCmd(t, store, "pair", "pi-lab")
	if err != nil || !strings.Contains(out, "rotation") {
		t.Fatalf("a second pair: %q %v", out, err)
	}
}

// TestParticipantPairOverPlainHTTP: loopback has no warning; a tailnet or LAN plain listener does, and neither
// prints a pin.
func TestParticipantPairOverPlainHTTP(t *testing.T) {
	t.Run("loopback", func(t *testing.T) {
		store, _ := openStore(t, false)
		recordEndpoint(t, store, ParticipantOptions{Listen: "127.0.0.1:7790"})
		out, _, err := participantCmd(t, store, "pair", "pi-lab")
		if err != nil || !strings.Contains(out, "participant API: http://127.0.0.1:7790") ||
			strings.Contains(out, "WARNING") || strings.Contains(out, "pin") {
			t.Fatalf("%q %v", out, err)
		}
	})
	t.Run("tailnet plain", func(t *testing.T) {
		store, _ := openStore(t, false)
		recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790", InsecureHTTP: true})
		out, _, err := participantCmd(t, store, "pair", "pi-lab")
		if err != nil || !strings.Contains(out, ParticipantPlainHTTPWarning) || strings.Contains(out, "SHA-256") || strings.Contains(out, "key pin") {
			t.Fatalf("%q %v", out, err)
		}
	})
}

// TestParticipantPairRefusals: a host name, host history, a bad id. Each is an error and prints no code.
func TestParticipantPairRefusals(t *testing.T) {
	store, _ := openStore(t, false)
	recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790"})
	pairHostFrom(t, store, "vmb", participantAdminNow)
	if _, err := store.db.Exec(`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES('s1','old-host','a',1,2)`); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"vmb", "old-host", "Bad Id", ""} {
		out, _, err := participantCmd(t, store, "pair", id)
		if err == nil || out != "" {
			t.Fatalf("pair %q: out=%q err=%v", id, out, err)
		}
	}
	// And the host command refuses a participant's name.
	pairParticipant(t, store, "pi-lab", participantAdminNow)
	if _, _, err := runPairIn(t, store, "pi-lab"); err == nil || !strings.Contains(err.Error(), "participant") {
		t.Fatalf("host pair of a participant name: %v", err)
	}
}

func runPairIn(t *testing.T, store *Store, host string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runPair(context.Background(), store, []string{host}, &out, &errOut, participantAdminNow)
	return out.String(), errOut.String(), err
}

// TestParticipantList: empty, then enrolled, live, revoked.
func TestParticipantList(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	out, _, err := participantCmd(t, store, "list")
	if err != nil || out != "no participants\n" {
		t.Fatalf("empty: %q %v", out, err)
	}

	pairParticipant(t, store, "pi-lab", participantAdminNow)
	pairParticipant(t, store, "lab-2", participantAdminNow)
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantAdminNow, randomID); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchPoll(ctx, "pi-lab", participantAdminNow.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeParticipant(ctx, "lab-2", participantAdminNow); err != nil {
		t.Fatal(err)
	}

	out, _, err = participantCmd(t, store, "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "STATE", "PAIRED", "LAST POLL", "LIVE", "SESSIONS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("header %q missing in %q", want, out)
		}
	}
	var piLab, lab2 string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "pi-lab"):
			piLab = line
		case strings.HasPrefix(line, "lab-2"):
			lab2 = line
		}
	}
	if !strings.Contains(piLab, "enrolled") || !strings.Contains(piLab, "yes") || !strings.HasSuffix(strings.TrimSpace(piLab), "1") {
		t.Fatalf("pi-lab row = %q", piLab)
	}
	if !strings.Contains(lab2, "revoked") || !strings.Contains(lab2, "never") || !strings.Contains(lab2, "no") {
		t.Fatalf("lab-2 row = %q", lab2)
	}
	// A credential, a hash or a code never appears.
	if strings.Contains(out, "sha256") {
		t.Fatalf("list printed a hash: %q", out)
	}
}

// TestParticipantRevoke: says exactly what it ended, and that the running gateway notices on its own
// schedule; unknown and already revoked ids are errors.
func TestParticipantRevoke(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	credential := pairParticipant(t, store, "pi-lab", participantAdminNow)
	for _, name := range []string{"coder", "tester"} {
		if _, err := store.AddParticipantSession(ctx, "pi-lab", name, participantAdminNow, randomID); err != nil {
			t.Fatal(err)
		}
	}
	recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790"})
	if _, _, err := participantCmd(t, store, "pair", "pi-lab"); err != nil { // leaves one unused code
		t.Fatal(err)
	}

	out, _, err := participantCmd(t, store, "revoke", "pi-lab")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"revoked pi-lab: ended 2 open session(s), removed 1 unused pairing code(s)",
		"refused from the next request",
		"notices within about " + participantRevokeCheck.String(),
		"answered 401",
		"cannot trigger one",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q does not contain %q", out, want)
		}
	}
	if _, err := store.AuthenticateParticipant(ctx, credential); err == nil {
		t.Fatal("the credential still works after revoke")
	}
	for _, id := range []string{"pi-lab", "ghost"} {
		if _, _, err := participantCmd(t, store, "revoke", id); err == nil || !strings.Contains(err.Error(), "is not an active participant") {
			t.Fatalf("revoke %s again: %v", id, err)
		}
	}
}

// TestAdminDispatchesParticipantAndListsItInUsage.
func TestAdminDispatchesParticipantAndListsItInUsage(t *testing.T) {
	for _, line := range []string{
		"rtk gateway participant pair ID [--insecure-http]",
		"rtk gateway participant list",
		"rtk gateway participant revoke ID",
	} {
		if !strings.Contains(adminUsage, line) {
			t.Fatalf("adminUsage does not mention %q", line)
		}
	}
	var out, errOut bytes.Buffer
	if err := RunAdmin(context.Background(), t.TempDir(), []string{"participant", "list"}, &out, &errOut, nil); err != nil {
		t.Fatalf("RunAdmin participant list: %v", err)
	}
	if out.String() != "no participants\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func codeCount(t *testing.T, store *Store) int {
	t.Helper()
	var codes int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM participant_pairing_codes`).Scan(&codes); err != nil {
		t.Fatal(err)
	}
	return codes
}

// TestParticipantPairInsecureHTTP: the flag asks for the plain block of a plain tailnet or LAN listener, may come
// before or after the ID, and every refusal mints no code.
func TestParticipantPairInsecureHTTP(t *testing.T) {
	t.Run("plain tailnet listener prints the plain block", func(t *testing.T) {
		for _, args := range [][]string{{"pair", "--insecure-http", "pi-lab"}, {"pair", "pi-lab", "--insecure-http"}} {
			store, _ := openStore(t, false)
			recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790", InsecureHTTP: true})
			out, _, err := participantCmd(t, store, args...)
			if err != nil || !strings.Contains(out, "participant API: http://100.64.0.1:7790") || !strings.Contains(out, ParticipantPlainHTTPWarning) ||
				strings.Contains(out, "SHA-256") || strings.Contains(out, "key pin") || codeCount(t, store) != 1 {
				t.Fatalf("%v: %q %v", args, out, err)
			}
		}
	})
	t.Run("HTTPS listener refuses before minting a code and names the remedy", func(t *testing.T) {
		store, _ := openStore(t, false)
		recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790"})
		out, errOut, err := participantCmd(t, store, "pair", "pi-lab", "--insecure-http")
		if err == nil || !strings.Contains(err.Error(), "is HTTPS") || !strings.Contains(err.Error(), FlagParticipantInsecureHTTP) ||
			!strings.Contains(err.Error(), "does not apply to the participant API") || out != "" || errOut != "" || codeCount(t, store) != 0 {
			t.Fatalf("%q %q %v", out, errOut, err)
		}
	})
	t.Run("loopback refuses the flag", func(t *testing.T) {
		store, _ := openStore(t, false)
		recordEndpoint(t, store, ParticipantOptions{Listen: "127.0.0.1:7790"})
		out, _, err := participantCmd(t, store, "pair", "--insecure-http", "pi-lab")
		if err == nil || !strings.Contains(err.Error(), "loopback") || out != "" || codeCount(t, store) != 0 {
			t.Fatalf("%q %v", out, err)
		}
	})
	t.Run("without the flag nothing changes", func(t *testing.T) {
		store, _ := openStore(t, false)
		recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790"})
		out, _, err := participantCmd(t, store, "pair", "pi-lab")
		if err != nil || !strings.Contains(out, "participant API: https://") || !strings.Contains(out, "SHA-256") {
			t.Fatalf("%q %v", out, err)
		}
	})
	t.Run("two IDs are still refused", func(t *testing.T) {
		store, _ := openStore(t, false)
		recordEndpoint(t, store, ParticipantOptions{Listen: "100.64.0.1:7790", InsecureHTTP: true})
		if _, _, err := participantCmd(t, store, "pair", "a", "b", "--insecure-http"); err == nil || codeCount(t, store) != 0 {
			t.Fatalf("err = %v", err)
		}
	})
}
