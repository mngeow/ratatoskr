package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"errors"
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

func TestStartSessionsValidation(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	tests := []struct {
		name  string
		host  string
		names []string
		want  error
	}{
		{name: "bad host", host: "Laptop", names: []string{"claude"}, want: ErrBadHostName},
		{name: "no names", host: "laptop", names: nil, want: ErrBadAgentList},
		{name: "too many", host: "laptop", names: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, want: ErrBadAgentList},
		{name: "duplicate", host: "laptop", names: []string{"claude", "claude"}, want: ErrBadAgentList},
		{name: "uppercase", host: "laptop", names: []string{"Claude"}, want: ErrBadAgentName},
		{name: "empty name", host: "laptop", names: []string{""}, want: ErrBadAgentName},
		{name: "too long", host: "laptop", names: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, want: ErrBadAgentName},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.StartSessions(ctx, tc.host, tc.names, sessionNow, idGenerator("s")); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			// Nothing was written: a rejected registration must not end sessions.
			var count int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&count); err != nil {
				t.Fatalf("count sessions: %v", err)
			}
			if count != 0 {
				t.Fatalf("a rejected registration wrote %d sessions", count)
			}
		})
	}

	if _, err := store.StartSessions(ctx, "laptop", []string{"claude"}, sessionNow, nil); err == nil {
		t.Fatal("StartSessions accepted a nil id generator")
	}
}

func TestStartSessionsEndsEarlierAndCurrentSession(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	first, err := store.StartSessions(ctx, "laptop", []string{"claude", "codex"}, sessionNow, idGenerator("a"))
	if err != nil {
		t.Fatalf("first StartSessions: %v", err)
	}
	if len(first) != 2 || first[0].Name != "claude" || first[1].Name != "codex" {
		t.Fatalf("sessions = %+v, want both names in order", first)
	}
	if first[0].ID == first[1].ID {
		t.Fatal("two sessions share an id")
	}

	second, err := store.StartSessions(ctx, "laptop", []string{"claude"}, sessionNow.Add(time.Minute), idGenerator("b"))
	if err != nil {
		t.Fatalf("second StartSessions: %v", err)
	}

	current, ok, err := store.CurrentSession(ctx, "laptop", "claude")
	if err != nil {
		t.Fatalf("CurrentSession: %v", err)
	}
	if !ok || current.ID != second[0].ID {
		t.Fatalf("current session = %+v, want the second registration", current)
	}

	// The earlier claude and codex sessions are gone; EverSeen still knows them.
	if _, ok, err := store.CurrentSession(ctx, "laptop", "codex"); err != nil {
		t.Fatalf("CurrentSession codex: %v", err)
	} else if ok {
		t.Fatal("the earlier codex session is still current")
	}
	if seen, err := store.EverSeen(ctx, "laptop", "codex"); err != nil {
		t.Fatalf("EverSeen: %v", err)
	} else if !seen {
		t.Fatal("EverSeen forgot a session that was registered")
	}
	if seen, err := store.EverSeen(ctx, "laptop", "gemini"); err != nil {
		t.Fatalf("EverSeen: %v", err)
	} else if seen {
		t.Fatal("EverSeen invented a session")
	}
}

func TestEndSessions(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	if err := store.EndSessions(ctx, "laptop", sessionNow); err != nil {
		t.Fatalf("EndSessions with nothing open: %v", err)
	}

	if _, err := store.StartSessions(ctx, "laptop", []string{"claude"}, sessionNow, idGenerator("s")); err != nil {
		t.Fatalf("StartSessions: %v", err)
	}
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

	if _, err := store.StartSessions(ctx, "beta", []string{"zeta"}, sessionNow, idGenerator("b")); err != nil {
		t.Fatalf("StartSessions beta: %v", err)
	}
	if _, err := store.StartSessions(ctx, "alpha", []string{"codex", "claude"}, sessionNow, idGenerator("a")); err != nil {
		t.Fatalf("StartSessions alpha: %v", err)
	}
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
