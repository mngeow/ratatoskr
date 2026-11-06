package app

// Untested: under GR-3 nothing here has been run. These tests are written, not run,
// and they exercise only pure functions: RunRuntime itself starts harnesses.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func directory(addresses ...string) []protocol.DirectoryEntry {
	entries := make([]protocol.DirectoryEntry, 0, len(addresses))
	for _, address := range addresses {
		entries = append(entries, protocol.DirectoryEntry{Address: address})
	}
	return entries
}

// TestPeersFor: self is skipped, the gateway's order is kept, duplicates collapse,
// and an empty result is a usable empty slice rather than nil.
func TestPeersFor(t *testing.T) {
	dir := directory("claude@mac", "codex@ubuntu-b", "gemini@ubuntu-c")

	tests := []struct {
		name string
		dir  []protocol.DirectoryEntry
		own  string
		want []string
	}{
		{
			name: "excludes self and keeps order",
			dir:  dir,
			own:  "claude@mac",
			want: []string{"codex@ubuntu-b", "gemini@ubuntu-c"},
		},
		{
			name: "duplicates collapse, first position wins",
			dir:  directory("a@h1", "b@h2", "a@h1"),
			own:  "c@h3",
			want: []string{"a@h1", "b@h2"},
		},
		{
			name: "empty directory gives an empty slice",
			dir:  nil,
			own:  "claude@mac",
			want: []string{},
		},
		{
			name: "only self gives an empty slice",
			dir:  directory("claude@mac"),
			own:  "claude@mac",
			want: []string{},
		},
		{
			// A local peer without a host is an address too, and is not special-cased.
			name: "bare names are kept",
			dir:  directory("codex", "claude@mac"),
			own:  "claude@mac",
			want: []string{"codex"},
		},
		{
			name: "empty addresses are skipped",
			dir:  directory("", "codex@ubuntu-b"),
			own:  "claude@mac",
			want: []string{"codex@ubuntu-b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := peersFor(tc.dir, tc.own)
			if got == nil {
				t.Fatal("peersFor returned nil, want an empty slice")
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("peersFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAgentParamsCannotHoldACredential documents the rule the contract makes: the
// pairing credential never flows from the gateway side into an agent launch. The
// struct has no field that could carry it.
func TestAgentParamsCannotHoldACredential(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	pairing := link.Pairing{
		Gateway:    "100.64.0.1:7777",
		Host:       "mac",
		Credential: secret,
	}
	if pairing.Credential != secret {
		t.Fatal("the test's pairing lost its credential")
	}

	// Build a params value the way RunRuntime does: directory, executable, session
	// directory and the environment. Then check that nothing in it can be the secret.
	params := agentParams{
		dir:        "/tmp/project",
		rtkPath:    "/usr/local/bin/rtk",
		sessionDir: "/tmp/state/session",
		env:        []string{"PATH=/usr/bin", "HOME=/tmp/home"},
	}

	value := reflect.ValueOf(params)
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		name := strings.ToLower(field.Name)
		for _, forbidden := range []string{"credential", "token", "secret", "pairing"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("agentParams has a field %q that could carry a secret", field.Name)
			}
		}
		switch fieldValue := value.Field(i).Interface().(type) {
		case string:
			if strings.Contains(fieldValue, secret) {
				t.Fatalf("agentParams field %q holds the credential", field.Name)
			}
		case []string:
			for _, item := range fieldValue {
				if strings.Contains(item, secret) {
					t.Fatalf("agentParams field %q holds the credential", field.Name)
				}
			}
		}
	}

	// And the launch spec built from those params cannot carry it either: Build is
	// given no credential at all, which is the point of keeping the client opaque.
	if _, ok := reflect.TypeOf(agent.Params{}).FieldByName("Credential"); ok {
		t.Fatal("agent.Params gained a credential field")
	}
}
