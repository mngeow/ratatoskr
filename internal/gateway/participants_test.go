package gateway

// Tests for participant pairing, credentials, revocation, sessions and the wire directory. Written, never
// run (GR-3): UNTESTED. Each case uses a temporary store.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

var participantNow = time.Unix(1700000000, 0)

// pairParticipant mints and redeems a code and returns the credential.
func pairParticipant(t *testing.T, store *Store, id string, now time.Time) string {
	t.Helper()
	code, err := store.CreateParticipantCode(context.Background(), id, now)
	if err != nil {
		t.Fatalf("CreateParticipantCode(%s): %v", id, err)
	}
	credential, got, err := store.RedeemParticipantCode(context.Background(), code, "", now)
	if err != nil || got != id {
		t.Fatalf("RedeemParticipantCode: id=%q err=%v", got, err)
	}
	return credential
}

// TestMigration0003CreatesTheParticipantTables: a fresh database gets both tables and nothing else changes.
func TestMigration0003CreatesTheParticipantTables(t *testing.T) {
	path := filepath.Join(t.TempDir(), dbName)
	db := openRawDatabase(t, path)
	if _, err := migrate(context.Background(), db, path, true, participantNow); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, table := range []string{"participants", "participant_pairing_codes"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = ?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s: count=%d err=%v", table, count, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO participants(id, credential_hash, created_at) VALUES('a','h',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO participants(id, credential_hash, created_at) VALUES('a','h',1)`); err == nil {
		t.Fatal("a duplicate participant id was accepted")
	}
}

// TestParticipantPairingIsSingleUseAndRotates.
func TestParticipantPairingIsSingleUseAndRotates(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	code, err := store.CreateParticipantCode(ctx, "pi-lab", participantNow)
	if err != nil {
		t.Fatal(err)
	}
	// Wrong id, then the right one.
	if _, _, err := store.RedeemParticipantCode(ctx, code, "other", participantNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("a code for another id: %v", err)
	}
	first, id, err := store.RedeemParticipantCode(ctx, code, "pi-lab", participantNow)
	if err != nil || id != "pi-lab" || first == "" {
		t.Fatalf("redeem: %q %q %v", first, id, err)
	}
	if _, _, err := store.RedeemParticipantCode(ctx, code, "", participantNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("a used code: %v", err)
	}
	if got, err := store.AuthenticateParticipant(ctx, first); err != nil || got != "pi-lab" {
		t.Fatalf("authenticate: %q %v", got, err)
	}

	// Expiry.
	late, err := store.CreateParticipantCode(ctx, "pi-lab", participantNow)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.RedeemParticipantCode(ctx, late, "", participantNow.Add(codeTTL+time.Second)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("an expired code: %v", err)
	}

	// Rotation: a new code replaces the credential and ends the sessions; the old credential stops working.
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow, randomID); err != nil {
		t.Fatal(err)
	}
	second := pairParticipant(t, store, "pi-lab", participantNow.Add(time.Minute))
	if _, err := store.AuthenticateParticipant(ctx, first); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("the old credential still works: %v", err)
	}
	if got, err := store.AuthenticateParticipant(ctx, second); err != nil || got != "pi-lab" {
		t.Fatalf("the new credential: %q %v", got, err)
	}
	if _, found, _ := store.CurrentSession(ctx, "pi-lab", "coder"); found {
		t.Fatal("a rotation left the old session open")
	}

	// A newer code replaces an older unused one.
	one, _ := store.CreateParticipantCode(ctx, "pi-lab", participantNow)
	two, _ := store.CreateParticipantCode(ctx, "pi-lab", participantNow)
	if _, _, err := store.RedeemParticipantCode(ctx, one, "", participantNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("the older code still works: %v", err)
	}
	if _, _, err := store.RedeemParticipantCode(ctx, two, "", participantNow); err != nil {
		t.Fatalf("the newer code: %v", err)
	}

	for _, bad := range []string{"", "x!", "UPPER", "has space"} {
		if _, err := store.CreateParticipantCode(ctx, bad, participantNow); !errors.Is(err, ErrBadHostName) {
			t.Fatalf("CreateParticipantCode(%q): %v", bad, err)
		}
	}
	if _, err := store.AuthenticateParticipant(ctx, ""); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an empty credential: %v", err)
	}
}

// TestParticipantAndHostNamesNeverCollide: both pairing paths refuse the other kind's name, at mint time and
// again inside the redeem transaction, and a refused redemption does not consume the code.
func TestParticipantAndHostNamesNeverCollide(t *testing.T) {
	ctx := context.Background()

	t.Run("participant pair refuses a host", func(t *testing.T) {
		store, _ := openStore(t, false)
		pairHostFrom(t, store, "vmb", participantNow)
		if _, err := store.CreateParticipantCode(ctx, "vmb", participantNow); !errors.Is(err, ErrNameIsHost) {
			t.Fatalf("err = %v, want ErrNameIsHost", err)
		}
	})

	t.Run("host pair refuses a participant, revoked or not", func(t *testing.T) {
		store, _ := openStore(t, false)
		pairParticipant(t, store, "pi-lab", participantNow)
		if _, err := store.CreatePairingCode(ctx, "pi-lab", participantNow); !errors.Is(err, ErrNameIsParticipant) {
			t.Fatalf("err = %v, want ErrNameIsParticipant", err)
		}
		if _, err := store.RevokeParticipant(ctx, "pi-lab", participantNow); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreatePairingCode(ctx, "pi-lab", participantNow); !errors.Is(err, ErrNameIsParticipant) {
			t.Fatalf("a revoked name was released: %v", err)
		}
	})

	t.Run("a race at redemption: the participant code, then a host appears", func(t *testing.T) {
		store, _ := openStore(t, false)
		code, err := store.CreateParticipantCode(ctx, "twin", participantNow)
		if err != nil {
			t.Fatal(err)
		}
		pairHostFrom(t, store, "twin", participantNow) // the host got the name first
		if _, _, err := store.RedeemParticipantCode(ctx, code, "", participantNow); !errors.Is(err, ErrBadCode) {
			t.Fatalf("err = %v, want ErrBadCode", err)
		}
		var rows int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM participants WHERE id='twin'`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("a participant row exists beside the host: %d %v", rows, err)
		}
		var used int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM participant_pairing_codes WHERE used_at IS NOT NULL`).Scan(&used); err != nil || used != 0 {
			t.Fatalf("a refused redemption consumed the code: %d %v", used, err)
		}
	})

	t.Run("a race at redemption: the host code, then a participant appears", func(t *testing.T) {
		store, _ := openStore(t, false)
		code, err := store.CreatePairingCode(ctx, "twin", participantNow)
		if err != nil {
			t.Fatal(err)
		}
		pairParticipant(t, store, "twin", participantNow)
		if _, err := store.RedeemCode(ctx, code, "twin", participantNow); !errors.Is(err, ErrBadCode) {
			t.Fatalf("err = %v, want ErrBadCode", err)
		}
		if _, _, err := store.RedeemCodeForAnyHost(ctx, code, participantNow); !errors.Is(err, ErrBadCode) {
			t.Fatalf("any-host redeem: %v", err)
		}
		var hosts int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM hosts WHERE name='twin'`).Scan(&hosts); err != nil || hosts != 0 {
			t.Fatalf("a host row exists beside the participant: %d %v", hosts, err)
		}
	})

	t.Run("a host and a participant with different names are both fine", func(t *testing.T) {
		store, _ := openStore(t, false)
		pairHostFrom(t, store, "vmb", participantNow)
		pairParticipant(t, store, "pi-lab", participantNow)
	})
}

// TestParticipantCredentialsAreNotHostCredentials: neither works on the other's authenticator.
func TestParticipantCredentialsAreNotHostCredentials(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	host := pairHostFrom(t, store, "vmb", participantNow)
	participant := pairParticipant(t, store, "pi-lab", participantNow)

	if _, err := store.AuthenticateParticipant(ctx, host); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a host credential works as a participant: %v", err)
	}
	if _, err := store.Authenticate(ctx, participant); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a participant credential works as a host: %v", err)
	}
}

// TestRevokeParticipant: sessions end, the credential stops at once, codes are removed, and the row stays.
func TestRevokeParticipant(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	credential := pairParticipant(t, store, "pi-lab", participantNow)
	for _, name := range []string{"coder", "tester"} {
		if _, err := store.AddParticipantSession(ctx, "pi-lab", name, participantNow, randomID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateParticipantCode(ctx, "pi-lab", participantNow); err != nil {
		t.Fatal(err)
	}

	revocation, err := store.RevokeParticipant(ctx, "pi-lab", participantNow.Add(time.Minute))
	if err != nil || revocation.SessionsEnded != 2 || revocation.CodesRemoved != 1 {
		t.Fatalf("revocation = %+v, %v", revocation, err)
	}
	if _, err := store.AuthenticateParticipant(ctx, credential); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a revoked credential works: %v", err)
	}
	row, found, err := store.GetParticipant(ctx, "pi-lab")
	if err != nil || !found || !row.Revoked() || row.OpenSessions != 0 {
		t.Fatalf("row = %+v found=%t err=%v", row, found, err)
	}
	if _, err := store.RevokeParticipant(ctx, "pi-lab", participantNow); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("a second revoke: %v", err)
	}
	if _, err := store.RevokeParticipant(ctx, "ghost", participantNow); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("revoking an unknown id: %v", err)
	}
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "late", participantNow, randomID); !errors.Is(err, ErrParticipantRevoked) {
		t.Fatalf("a session for a revoked participant: %v", err)
	}
	if err := store.TouchPoll(ctx, "pi-lab", participantNow); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("a poll touch on a revoked participant: %v", err)
	}

	// Pairing the revoked id again clears the revocation.
	again := pairParticipant(t, store, "pi-lab", participantNow.Add(time.Hour))
	if got, err := store.AuthenticateParticipant(ctx, again); err != nil || got != "pi-lab" {
		t.Fatalf("after re-pairing: %q %v", got, err)
	}
	if row, _, _ := store.GetParticipant(ctx, "pi-lab"); row.Revoked() {
		t.Fatal("re-pairing left the participant revoked")
	}
}

// TestParticipantSessionsUseTheSessionsTable: the existing session methods work with the participant id as
// the host, replacement and the limit behave as for a host, and an unknown participant gets nothing.
func TestParticipantSessionsUseTheSessionsTable(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	pairParticipant(t, store, "pi-lab", participantNow)

	if _, err := store.AddParticipantSession(ctx, "ghost", "coder", participantNow, randomID); !errors.Is(err, ErrNoSuchParticipant) {
		t.Fatalf("a session for an unknown participant: %v", err)
	}

	first, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow, randomID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow.Add(time.Second), randomID)
	if err != nil || second.ID == first.ID {
		t.Fatalf("replacement: %+v %v", second, err)
	}
	current, found, err := store.CurrentSession(ctx, "pi-lab", "coder")
	if err != nil || !found || current.ID != second.ID {
		t.Fatalf("CurrentSession = %+v found=%t err=%v", current, found, err)
	}
	if seen, err := store.EverSeen(ctx, "pi-lab", "coder"); err != nil || !seen {
		t.Fatalf("EverSeen: %t %v", seen, err)
	}

	// The limit counts open sessions after replacement: eight names fit, the ninth does not, and replacing at
	// the limit still works.
	for i := 2; i <= maxAgentsPerHost; i++ {
		if _, err := store.AddParticipantSession(ctx, "pi-lab", fmt.Sprintf("worker-%d", i), participantNow, randomID); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "worker-9", participantNow, randomID); !errors.Is(err, ErrTooManySessions) {
		t.Fatalf("the ninth session: %v", err)
	}
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow, randomID); err != nil {
		t.Fatalf("replacing at the limit: %v", err)
	}

	if err := store.EndSession(ctx, "pi-lab", "coder", participantNow); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := store.CurrentSession(ctx, "pi-lab", "coder"); found {
		t.Fatal("EndSession left the session open")
	}
	if err := store.EndSessions(ctx, "pi-lab", participantNow); err != nil {
		t.Fatal(err)
	}
	if row, _, _ := store.GetParticipant(ctx, "pi-lab"); row.OpenSessions != 0 {
		t.Fatalf("%d sessions open after EndSessions", row.OpenSessions)
	}
}

// TestWireDirectoryIncludesParticipantsAndDirectoryDoesNot: the wire directory holds host sessions and the
// sessions of active participants; the host-only Directory (Network and Graph) is unchanged.
func TestWireDirectoryIncludesParticipantsAndDirectoryDoesNot(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	pairHostFrom(t, store, "vmb", participantNow)
	pairParticipant(t, store, "pi-lab", participantNow)
	pairParticipant(t, store, "gone", participantNow)

	if _, err := store.AddSession(ctx, "vmb", "claude", participantNow, randomID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow, randomID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddParticipantSession(ctx, "gone", "ghost", participantNow, randomID); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchPoll(ctx, "pi-lab", participantNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeParticipant(ctx, "gone", participantNow); err != nil {
		t.Fatal(err)
	}

	wire, err := store.WireDirectory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) != 2 {
		t.Fatalf("wire directory = %+v, want the host session and the active participant's", wire)
	}
	// Ordered by host then name: pi-lab sorts before vmb.
	if wire[0].Host != "pi-lab" || wire[0].Name != "coder" || !wire[0].Participant || !wire[0].LastSeen.Equal(time.UnixMilli(participantNow.UnixMilli())) {
		t.Fatalf("first row = %+v", wire[0])
	}
	if wire[1].Host != "vmb" || wire[1].Participant {
		t.Fatalf("second row = %+v", wire[1])
	}

	hostsOnly, err := store.Directory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(hostsOnly) != 1 || hostsOnly[0].Host != "vmb" {
		t.Fatalf("Directory = %+v, want the host session only", hostsOnly)
	}
}

// TestParticipantsListAndPoll: the list shows state, last poll and open sessions; the poll window is the
// host's 15 s rule.
func TestParticipantsListAndPoll(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	pairParticipant(t, store, "pi-lab", participantNow)
	pairParticipant(t, store, "lab-2", participantNow)
	if _, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow, randomID); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchPoll(ctx, "pi-lab", participantNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RevokeParticipant(ctx, "lab-2", participantNow); err != nil {
		t.Fatal(err)
	}

	rows, err := store.Participants(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("Participants = %+v %v", rows, err)
	}
	if rows[0].ID != "lab-2" || !rows[0].Revoked() || rows[0].OpenSessions != 0 || !rows[0].LastPoll.IsZero() {
		t.Fatalf("lab-2 = %+v", rows[0])
	}
	if rows[1].ID != "pi-lab" || rows[1].Revoked() || rows[1].OpenSessions != 1 {
		t.Fatalf("pi-lab = %+v", rows[1])
	}
	if !HeartbeatFresh(rows[1].LastPoll, participantNow.Add(10*time.Second)) || HeartbeatFresh(rows[1].LastPoll, participantNow.Add(20*time.Second)) {
		t.Fatalf("the 15 s window does not hold for last poll %v", rows[1].LastPoll)
	}
	if _, found, err := store.GetParticipant(ctx, "ghost"); err != nil || found {
		t.Fatalf("GetParticipant(ghost): found=%t err=%v", found, err)
	}
}

// TestParticipantIdRefusesAnyNameWithHostHistory: an unpaired former host stays reserved. The check looks at
// sessions.host and at the host part of both ledger addresses, runs when the code is minted and again at
// redemption, and never applies to an id that is already a participant.
func TestParticipantIdRefusesAnyNameWithHostHistory(t *testing.T) {
	ctx := context.Background()

	t.Run("a former host", func(t *testing.T) {
		store, _ := openStore(t, false)
		pairHostFrom(t, store, "old-host", participantNow)
		if _, err := store.AddSession(ctx, "old-host", "claude", participantNow, randomID); err != nil {
			t.Fatal(err)
		}
		if err := store.Unpair(ctx, "old-host", participantNow); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateParticipantCode(ctx, "old-host", participantNow); !errors.Is(err, ErrNameHasHostHistory) {
			t.Fatalf("err = %v, want ErrNameHasHostHistory", err)
		}
	})

	t.Run("only a session row", func(t *testing.T) {
		store, _ := openStore(t, false)
		if _, err := store.db.Exec(`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES('s1','gone-host','a',1,2)`); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateParticipantCode(ctx, "gone-host", participantNow); !errors.Is(err, ErrNameHasHostHistory) {
			t.Fatalf("err = %v, want ErrNameHasHostHistory", err)
		}
	})

	t.Run("only the ledger, as sender or as recipient", func(t *testing.T) {
		for _, column := range []string{"from_address", "to_address"} {
			store, _ := openStore(t, false)
			from, to := "a@ghost-host", "x@somewhere"
			if column == "to_address" {
				from, to = to, from
			}
			if _, err := store.db.Exec(
				`INSERT INTO messages(id, from_address, to_address, body, result, created_at, updated_at) VALUES('m1',?,?,'b','host_stored',1,1)`,
				from, to); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateParticipantCode(ctx, "ghost-host", participantNow); !errors.Is(err, ErrNameHasHostHistory) {
				t.Fatalf("%s: err = %v, want ErrNameHasHostHistory", column, err)
			}
			// A name that only matches the start of an address is not history.
			if _, err := store.CreateParticipantCode(ctx, "ghost", participantNow); err != nil {
				t.Fatalf("%s: a name that is only a prefix of a host was refused: %v", column, err)
			}
		}
	})

	t.Run("at redemption", func(t *testing.T) {
		store, _ := openStore(t, false)
		code, err := store.CreateParticipantCode(ctx, "late-host", participantNow)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO sessions(id, host, name, started_at) VALUES('s2','late-host','a',1)`); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.RedeemParticipantCode(ctx, code, "", participantNow); !errors.Is(err, ErrBadCode) {
			t.Fatalf("err = %v, want ErrBadCode", err)
		}
		var rows int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM participants`).Scan(&rows); err != nil || rows != 0 {
			t.Fatalf("a participant row was written: %d %v", rows, err)
		}
	})

	t.Run("an id that is already a participant is exempt", func(t *testing.T) {
		store, _ := openStore(t, false)
		pairParticipant(t, store, "pi-lab", participantNow)
		if _, err := store.AddParticipantSession(ctx, "pi-lab", "coder", participantNow, randomID); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(
			`INSERT INTO messages(id, from_address, to_address, body, result, created_at, updated_at) VALUES('m2','coder@pi-lab','x@y','b','host_stored',1,1)`); err != nil {
			t.Fatal(err)
		}
		// Rotation, and pairing again after a revoke, both leave sessions and ledger rows with the id.
		pairParticipant(t, store, "pi-lab", participantNow.Add(time.Minute))
		if _, err := store.RevokeParticipant(ctx, "pi-lab", participantNow.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		pairParticipant(t, store, "pi-lab", participantNow.Add(3*time.Minute))
	})
}
