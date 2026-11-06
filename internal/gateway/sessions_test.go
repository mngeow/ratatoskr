package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var sessionNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// idGenerator hands out predictable session ids.
func idGenerator(prefix string) func() (string, error) {
	sequence := 0
	return func() (string, error) {
		sequence++
		return prefix + string(rune('0'+sequence)), nil
	}
}

// startSessionsForTest ends the host's open sessions and adds each name, returning the
// new rows. It replaces the old multi-name StartSessions for tests that only need
// sessions to exist.
func startSessionsForTest(t *testing.T, store *Store, host string, now time.Time, names ...string) []SessionRow {
	t.Helper()
	ctx := context.Background()

	if err := store.EndSessions(ctx, host, now); err != nil {
		t.Fatalf("EndSessions %s: %v", host, err)
	}
	rows := make([]SessionRow, 0, len(names))
	for _, name := range names {
		row, err := store.AddSession(ctx, host, name, now, randomID)
		if err != nil {
			t.Fatalf("AddSession %s/%s: %v", host, name, err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestAddSessionValidation(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	tests := []struct {
		name string
		host string
		add  string
		want error
	}{
		{name: "bad host", host: "Laptop", add: "claude", want: ErrBadHostName},
		{name: "uppercase", host: "laptop", add: "Claude", want: ErrBadAgentName},
		{name: "empty name", host: "laptop", add: "", want: ErrBadAgentName},
		{name: "too long", host: "laptop", add: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", want: ErrBadAgentName},
		{name: "a slash", host: "laptop", add: "cli/ent", want: ErrBadAgentName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.AddSession(ctx, tc.host, tc.add, sessionNow, idGenerator("s")); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			// Nothing was written: a rejected add must not end a session either.
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
				t.Fatalf("count sessions: %v", err)
			}
			if count != 0 {
				t.Fatalf("a rejected add wrote %d sessions", count)
			}
		})
	}

	if _, err := store.AddSession(ctx, "laptop", "claude", sessionNow, nil); err == nil {
		t.Fatal("AddSession accepted a nil id generator")
	}
}

// TestAddSessionMintsANewIDAndReplacesOnlyTheSameName: a new ID every time, and the
// host's other sessions untouched.
func TestAddSessionMintsANewIDAndReplacesOnlyTheSameName(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	firstClaude, err := store.AddSession(ctx, "laptop", "claude", sessionNow, idGenerator("a"))
	if err != nil {
		t.Fatalf("first AddSession: %v", err)
	}
	firstCodex, err := store.AddSession(ctx, "laptop", "codex", sessionNow, idGenerator("b"))
	if err != nil {
		t.Fatalf("second AddSession: %v", err)
	}
	if firstClaude.ID == firstCodex.ID {
		t.Fatal("two sessions share an id")
	}

	secondClaude, err := store.AddSession(ctx, "laptop", "claude", sessionNow.Add(time.Minute), idGenerator("c"))
	if err != nil {
		t.Fatalf("replacing AddSession: %v", err)
	}
	if secondClaude.ID == firstClaude.ID {
		t.Fatalf("the replacement reused the id %q", secondClaude.ID)
	}

	current, ok, err := store.CurrentSession(ctx, "laptop", "claude")
	if err != nil {
		t.Fatalf("CurrentSession: %v", err)
	}
	if !ok || current.ID != secondClaude.ID {
		t.Fatalf("current claude = %+v, want the replacement", current)
	}

	// codex was not touched by a claude add.
	codexNow, ok, err := store.CurrentSession(ctx, "laptop", "codex")
	if err != nil {
		t.Fatalf("CurrentSession codex: %v", err)
	}
	if !ok || codexNow.ID != firstCodex.ID {
		t.Fatalf("codex = %+v, want the untouched session %q", codexNow, firstCodex.ID)
	}
	// Even the replaced session is remembered as ever seen.
	if seen, err := store.EverSeen(ctx, "laptop", "claude"); err != nil || !seen {
		t.Fatalf("EverSeen claude = %t, %v", seen, err)
	}
}

// TestAddSessionLimit: at most maxAgentsPerHost open sessions, and a replace at the limit
// still works because the same-name session ends first.
func TestAddSessionLimit(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	fill := make([]string, 0, maxAgentsPerHost)
	for i := 0; i < maxAgentsPerHost; i++ {
		name := "agent" + string(rune('a'+i))
		fill = append(fill, name)
		if _, err := store.AddSession(ctx, "laptop", name, sessionNow, idGenerator("f")); err != nil {
			t.Fatalf("AddSession %s: %v", name, err)
		}
	}

	if _, err := store.AddSession(ctx, "laptop", "onemore", sessionNow, idGenerator("x")); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("error = %v, want ErrTooManySessions", err)
	}
	if !strings.Contains(ErrTooManySessions.Error(), "open sessions") {
		t.Fatalf("the sentinel text lost its meaning: %q", ErrTooManySessions.Error())
	}

	// Replacing one of them still works: the same-name session ends before the count.
	if _, err := store.AddSession(ctx, "laptop", fill[0], sessionNow, idGenerator("y")); err != nil {
		t.Fatalf("replacing at the limit: %v", err)
	}

	// Another host is unaffected.
	if _, err := store.AddSession(ctx, "other", "claude", sessionNow, idGenerator("z")); err != nil {
		t.Fatalf("another host was refused: %v", err)
	}
}

// TestEndSession: only that name ends, and ending nothing is not an error.
func TestEndSession(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	if err := store.EndSession(ctx, "laptop", "claude", sessionNow); err != nil {
		t.Fatalf("EndSession with nothing open: %v", err)
	}

	if _, err := store.AddSession(ctx, "laptop", "claude", sessionNow, idGenerator("a")); err != nil {
		t.Fatalf("AddSession claude: %v", err)
	}
	if _, err := store.AddSession(ctx, "laptop", "codex", sessionNow, idGenerator("b")); err != nil {
		t.Fatalf("AddSession codex: %v", err)
	}

	if err := store.EndSession(ctx, "laptop", "claude", sessionNow.Add(time.Minute)); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if _, ok, err := store.CurrentSession(ctx, "laptop", "claude"); err != nil {
		t.Fatalf("CurrentSession claude: %v", err)
	} else if ok {
		t.Fatal("claude is still open after EndSession")
	}
	if _, ok, err := store.CurrentSession(ctx, "laptop", "codex"); err != nil {
		t.Fatalf("CurrentSession codex: %v", err)
	} else if !ok {
		t.Fatal("EndSession ended codex too")
	}

	// Ending it again is still not an error.
	if err := store.EndSession(ctx, "laptop", "claude", sessionNow.Add(2*time.Minute)); err != nil {
		t.Fatalf("EndSession again: %v", err)
	}

	// A malformed name is still refused.
	if err := store.EndSession(ctx, "laptop", "Claude", sessionNow); !errors.Is(err, ErrBadAgentName) {
		t.Fatalf("EndSession with a bad name = %v, want ErrBadAgentName", err)
	}
}

func TestEndSessions(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	if err := store.EndSessions(ctx, "laptop", sessionNow); err != nil {
		t.Fatalf("EndSessions with nothing open: %v", err)
	}

	startSessionsForTest(t, store, "laptop", sessionNow, "claude")
	if err := store.EndSessions(ctx, "laptop", sessionNow.Add(time.Minute)); err != nil {
		t.Fatalf("EndSessions: %v", err)
	}
	if _, ok, err := store.CurrentSession(ctx, "laptop", "claude"); err != nil {
		t.Fatalf("CurrentSession: %v", err)
	} else if ok {
		t.Fatal("a session is still open after EndSessions")
	}
}

// TestDirectoryOrdersAndExcludesUnpaired covers both halves of the query: ordering
// by host then name, and the INNER JOIN that hides an unpaired host's sessions.
func TestDirectoryOrdersAndExcludesUnpaired(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	pairHostFrom(t, store, "alpha", sessionNow)
	pairHostFrom(t, store, "beta", sessionNow)

	startSessionsForTest(t, store, "beta", sessionNow, "zeta")
	startSessionsForTest(t, store, "alpha", sessionNow, "codex", "claude")
	if err := store.TouchHeartbeat(ctx, "beta", sessionNow); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}

	directory, err := store.Directory(ctx)
	if err != nil {
		t.Fatalf("Directory: %v", err)
	}
	if len(directory) != 3 {
		t.Fatalf("directory = %+v, want three entries", directory)
	}
	want := []struct{ host, name string }{
		{"alpha", "claude"}, {"alpha", "codex"}, {"beta", "zeta"},
	}
	for i, expected := range want {
		if directory[i].Host != expected.host || directory[i].Name != expected.name {
			t.Fatalf("entry %d = %s@%s, want %s@%s", i, directory[i].Name, directory[i].Host, expected.name, expected.host)
		}
	}
	// alpha never sent a heartbeat, beta did.
	if !directory[0].LastHeartbeat.IsZero() {
		t.Fatal("a host that never sent a heartbeat has one")
	}
	if !directory[2].LastHeartbeat.Equal(sessionNow) {
		t.Fatalf("heartbeat = %v, want %v", directory[2].LastHeartbeat, sessionNow)
	}

	// Unpairing alpha hides its sessions entirely.
	if err := store.Unpair(ctx, "alpha", sessionNow); err != nil {
		t.Fatalf("Unpair: %v", err)
	}
	directory, err = store.Directory(ctx)
	if err != nil {
		t.Fatalf("Directory after unpair: %v", err)
	}
	if len(directory) != 1 || directory[0].Host != "beta" {
		t.Fatalf("directory after unpair = %+v, want only beta", directory)
	}
}
