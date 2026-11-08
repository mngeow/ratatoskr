package gateway

// Untested: under GR-3 nothing here has been run. Every case drives the command functions with a
// temporary store and buffers; no participant is fetched and no card is dialled.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// adminTime is a fixed clock for these tests.
var adminTime = time.Date(2026, 11, 8, 4, 0, 0, 0, time.UTC)

// participantIn runs one participant command against a fresh store.
func participantIn(t *testing.T, stateDir string, args ...string) (string, string, error) {
	t.Helper()

	store, err := Open(context.Background(), stateDir, false)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	var out, errOut bytes.Buffer
	runErr := runParticipant(context.Background(), store, args, &out, &errOut, adminTime)
	return out.String(), errOut.String(), runErr
}

// TestParticipantArgumentErrors: every subcommand's argument rules.
func TestParticipantArgumentErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no subcommand", args: nil, want: "gateway: participant needs enrol, register, list, revoke or delete-tasks"},
		{name: "unknown subcommand", args: []string{"frobnicate"}, want: `gateway: unknown participant command "frobnicate"`},
		{name: "enrol with no name", args: []string{"enrol"}, want: "gateway: participant enrol needs NAME"},
		{name: "enrol with two names", args: []string{"enrol", "a", "b"}, want: "gateway: participant enrol needs NAME"},
		{name: "register without --url", args: []string{"register", "alpha"}, want: "gateway: participant register needs NAME and --url URL"},
		{name: "register with two names", args: []string{"register", "a", "b", "--url", "http://127.0.0.1:1/"}, want: "gateway: participant register needs NAME and --url URL"},
		{name: "list with an argument", args: []string{"list", "extra"}, want: "gateway: participant list takes no arguments"},
		{name: "revoke with no name", args: []string{"revoke"}, want: "gateway: participant revoke needs NAME"},
		{name: "delete-tasks with nothing", args: []string{"delete-tasks"}, want: "gateway: participant delete-tasks needs exactly one of --task ID, --all-final or --participant NAME"},
		{
			name: "delete-tasks with two",
			args: []string{"delete-tasks", "--task", "t1", "--all-final"},
			want: "gateway: participant delete-tasks needs exactly one of --task ID, --all-final or --participant NAME",
		},
		{
			name: "delete-tasks with all three",
			args: []string{"delete-tasks", "--task", "t1", "--all-final", "--participant", "alpha"},
			want: "gateway: participant delete-tasks needs exactly one of --task ID, --all-final or --participant NAME",
		},
		{name: "delete-tasks with a positional", args: []string{"delete-tasks", "--all-final", "extra"}, want: "gateway: participant delete-tasks takes flags only"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := participantIn(t, t.TempDir(), tc.args...)
			if err == nil {
				t.Fatalf("runParticipant(%q) succeeded", tc.args)
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error = %q, want a prefix %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "usage:") {
				t.Fatalf("error = %q, want the usage text after it", err)
			}
		})
	}
}

// TestParticipantEnrol: the credential is printed once, on stdout, and never to stderr.
func TestParticipantEnrol(t *testing.T) {
	out, errOut, err := participantIn(t, t.TempDir(), "enrol", "Alpha")
	if err != nil {
		t.Fatalf("enrol: %v", err)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want nothing", errOut)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("stdout has %d lines, want 3: %q", len(lines), out)
	}
	credential := lines[0]
	if credential == "" || strings.ContainsAny(credential, " \t") {
		t.Fatalf("the credential line is %q", credential)
	}
	if lines[1] != "participant: alpha" {
		t.Fatalf("second line = %q, want the normalised name", lines[1])
	}
	if !strings.HasPrefix(lines[2], "this credential is shown once") {
		t.Fatalf("third line = %q", lines[2])
	}
	// Exactly one line holds the credential.
	if count := strings.Count(out, credential); count != 1 {
		t.Fatalf("the credential appears %d times in stdout", count)
	}
}

// TestParticipantEnrolDuplicatesAndBadNames: the two mapped refusals.
func TestParticipantEnrolDuplicatesAndBadNames(t *testing.T) {
	stateDir := t.TempDir()

	if _, _, err := participantIn(t, stateDir, "enrol", "alpha"); err != nil {
		t.Fatalf("first enrol: %v", err)
	}
	_, errOut, err := participantIn(t, stateDir, "enrol", "alpha")
	if err == nil {
		t.Fatal("a duplicate enrol succeeded")
	}
	if err.Error() != "gateway: alpha is already enrolled" {
		t.Fatalf("error = %q", err)
	}
	if errOut != "" {
		t.Fatalf("a refusal wrote to stderr: %q", errOut)
	}

	_, _, err = participantIn(t, stateDir, "enrol", "9lives")
	if err == nil {
		t.Fatal("an invalid name was accepted")
	}
	if !strings.Contains(err.Error(), "is not a valid participant name") {
		t.Fatalf("error = %q", err)
	}
}

// TestParticipantList: none, one active, one revoked.
func TestParticipantList(t *testing.T) {
	stateDir := t.TempDir()

	out, _, err := participantIn(t, stateDir, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out != "no participants enrolled\n" {
		t.Fatalf("list output = %q", out)
	}
	if strings.Contains(out, "NAME") {
		t.Fatal("an empty list printed a table header")
	}

	if _, _, err := participantIn(t, stateDir, "enrol", "alpha"); err != nil {
		t.Fatalf("enrol: %v", err)
	}
	out, _, err = participantIn(t, stateDir, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"NAME", "STATE", "URL", "SINCE", "alpha", "active", "2026-11-08T04:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("list output %q does not contain %q", out, want)
		}
	}
	// No URL shows as "-", never as an empty column.
	if !strings.Contains(out, "-") {
		t.Fatalf("list output %q has no placeholder for a missing URL", out)
	}

	if _, _, err := participantIn(t, stateDir, "revoke", "alpha"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	out, _, err = participantIn(t, stateDir, "list")
	if err != nil {
		t.Fatalf("list after revoke: %v", err)
	}
	if !strings.Contains(out, "revoked") {
		t.Fatalf("list output = %q, want the revoked state", out)
	}
}

// TestParticipantRegisterErrors: the mappings that need no card fetch.
func TestParticipantRegisterErrors(t *testing.T) {
	t.Run("not enrolled", func(t *testing.T) {
		_, _, err := participantIn(t, t.TempDir(), "register", "ghost", "--url", "http://127.0.0.1:1/")
		if err == nil {
			t.Fatal("registering an unknown participant succeeded")
		}
		if err.Error() != "gateway: ghost is not enrolled (run: rtk gateway participant enrol ghost)" {
			t.Fatalf("error = %q", err)
		}
	})

	t.Run("a bad url names nothing", func(t *testing.T) {
		stateDir := t.TempDir()
		if _, _, err := participantIn(t, stateDir, "enrol", "alpha"); err != nil {
			t.Fatalf("enrol: %v", err)
		}
		_, _, err := participantIn(t, stateDir, "register", "alpha", "--url", "not-a-url")
		if err == nil {
			t.Fatal("a bad URL was accepted")
		}
		if !strings.HasPrefix(err.Error(), "gateway: ") {
			t.Fatalf("error = %q, want our prefix", err)
		}
	})
}

// TestParticipantRevokeErrors: revoking something that is not active.
func TestParticipantRevokeErrors(t *testing.T) {
	_, _, err := participantIn(t, t.TempDir(), "revoke", "ghost")
	if err == nil {
		t.Fatal("revoking an unknown participant succeeded")
	}
	if err.Error() != "gateway: ghost is not an active participant" {
		t.Fatalf("error = %q", err)
	}
}

// TestParticipantDeleteTasks: each single choice is accepted and nothing is deleted yet.
func TestParticipantDeleteTasks(t *testing.T) {
	for _, args := range [][]string{
		{"delete-tasks", "--task", "t1"},
		{"delete-tasks", "--all-final"},
		{"delete-tasks", "--participant", "alpha"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, errOut, err := participantIn(t, t.TempDir(), args...)
			if err != nil {
				t.Fatalf("delete-tasks: %v", err)
			}
			if errOut != "" {
				t.Fatalf("stderr = %q", errOut)
			}
			if out != "deleted 0 task(s)\n" {
				t.Fatalf("output = %q", out)
			}
		})
	}

	t.Run("the literal plural is kept", func(t *testing.T) {
		out, _, err := participantIn(t, t.TempDir(), "delete-tasks", "--all-final")
		if err != nil {
			t.Fatalf("delete-tasks: %v", err)
		}
		if strings.Contains(out, "tasks") {
			t.Fatalf("output = %q, want the literal task(s)", out)
		}
	})
}

// TestAdminUsageListsParticipants: the five lines are in the usage text.
func TestAdminUsageListsParticipants(t *testing.T) {
	for _, line := range []string{
		"rtk gateway participant enrol NAME",
		"rtk gateway participant register NAME --url URL [--fingerprint sha256:...] [--insecure-http]",
		"rtk gateway participant list",
		"rtk gateway participant revoke NAME",
		"rtk gateway participant delete-tasks (--task ID | --all-final | --participant NAME)",
	} {
		if !strings.Contains(adminUsage, line) {
			t.Fatalf("adminUsage does not mention %q:\n%s", line, adminUsage)
		}
	}
}
