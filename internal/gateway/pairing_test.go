package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

var pairingNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

// pairHost mints a code and redeems it, returning the credential.
func pairHost(t *testing.T, store *Store, host string, now time.Time) string {
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

func TestCodeAlphabetIsBiased(t *testing.T) {
	// byte&31 is uniform only for exactly 32 characters, and this test is the
	// reason an edit to the alphabet cannot pass unnoticed.
	if len(codeAlphabet) != 32 {
		t.Fatalf("codeAlphabet has %d characters, want 32", len(codeAlphabet))
	}
}

func TestCreateAndRedeemOnce(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	if len(code) != 14 || strings.Count(code, "-") != 2 {
		t.Fatalf("display code = %q, want XXXX-XXXX-XXXX", code)
	}

	credential, err := store.RedeemCode(ctx, code, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("RedeemCode: %v", err)
	}
	if len(credential) != 43 {
		t.Fatalf("credential length = %d, want 43 base64url characters", len(credential))
	}

	host, err := store.Authenticate(ctx, credential)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if host != "laptop" {
		t.Fatalf("host = %q, want laptop", host)
	}

	// Single use.
	if _, err := store.RedeemCode(ctx, code, "laptop", pairingNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("second redeem error = %v, want ErrBadCode", err)
	}
}

// TestRedeemIsCaseAndDashInsensitive: the code is shown in groups, and a person
// may type it in either case.
func TestRedeemIsCaseAndDashInsensitive(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if _, err := store.RedeemCode(ctx, typed, "laptop", pairingNow); err != nil {
		t.Fatalf("RedeemCode with a retyped code: %v", err)
	}
}

func TestRedeemFailsTheSameWayForEveryMistake(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	// Wrong host, expired code, unknown code: one error, no hint which.
	if _, err := store.RedeemCode(ctx, code, "desktop", pairingNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("wrong host error = %v, want ErrBadCode", err)
	}
	if _, err := store.RedeemCode(ctx, code, "laptop", pairingNow.Add(codeTTL+time.Second)); !errors.Is(err, ErrBadCode) {
		t.Fatalf("expired code error = %v, want ErrBadCode", err)
	}
	if _, err := store.RedeemCode(ctx, "AAAA-BBBB-CCCC", "laptop", pairingNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("unknown code error = %v, want ErrBadCode", err)
	}

	// An exact-boundary expiry is still usable, so the window is not off by one.
	if _, err := store.RedeemCode(ctx, code, "laptop", pairingNow.Add(codeTTL)); err != nil {
		t.Fatalf("code at exactly its expiry: %v", err)
	}
}

func TestNewCodeInvalidatesTheOldOne(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	first, err := store.CreatePairingCode(ctx, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("first CreatePairingCode: %v", err)
	}
	second, err := store.CreatePairingCode(ctx, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("second CreatePairingCode: %v", err)
	}

	if _, err := store.RedeemCode(ctx, first, "laptop", pairingNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("the replaced code still works: %v", err)
	}
	if _, err := store.RedeemCode(ctx, second, "laptop", pairingNow); err != nil {
		t.Fatalf("the newest code does not work: %v", err)
	}
}

func TestRepairReplacesTheCredentialAndEndsSessions(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	oldCredential := pairHost(t, store, "laptop", pairingNow)

	// An open session for that host, as the routing slice would have made.
	if _, err := store.db.ExecContext(ctx,
		`INSERT INTO sessions(id, host, name, started_at, ended_at) VALUES(?,?,?,?,NULL)`,
		"s1", "laptop", "claude", pairingNow.UnixMilli()); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	newCredential := pairHost(t, store, "laptop", pairingNow.Add(time.Minute))

	if _, err := store.Authenticate(ctx, oldCredential); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("the old credential still authenticates: %v", err)
	}
	host, err := store.Authenticate(ctx, newCredential)
	if err != nil || host != "laptop" {
		t.Fatalf("the new credential = (%q, %v), want laptop", host, err)
	}

	var endedAt sql.NullInt64
	if err := store.db.QueryRowContext(ctx, `SELECT ended_at FROM sessions WHERE id='s1'`).Scan(&endedAt); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !endedAt.Valid {
		t.Fatal("re-pairing left the old session open")
	}
}

// TestUnpairRefusesAtOnce is the property the contract names: authentication
// reads the database every time, so a second process sees the removal without a
// restart.
func TestUnpairRefusesAtOnce(t *testing.T) {
	holder, stateDir := openStore(t, true)
	ctx := context.Background()

	credential := pairHost(t, holder, "laptop", pairingNow)

	admin, err := Open(ctx, stateDir, false)
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	defer func() {
		_ = admin.Close()
	}()

	if err := admin.Unpair(ctx, "laptop", pairingNow); err != nil {
		t.Fatalf("Unpair: %v", err)
	}

	// Both stores now refuse it, and the holder did not need a restart.
	if _, err := holder.Authenticate(ctx, credential); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("the holder still authenticates an unpaired host: %v", err)
	}
	if _, err := admin.Authenticate(ctx, credential); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("the admin store still authenticates an unpaired host: %v", err)
	}

	if err := admin.Unpair(ctx, "laptop", pairingNow); !errors.Is(err, ErrNoSuchHost) {
		t.Fatalf("second Unpair error = %v, want ErrNoSuchHost", err)
	}
}

func TestTouchHeartbeatAndHosts(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	pairHost(t, store, "laptop", pairingNow)
	pairHost(t, store, "desktop", pairingNow)

	beaten := pairingNow.Add(5 * time.Second)
	if err := store.TouchHeartbeat(ctx, "laptop", beaten); err != nil {
		t.Fatalf("TouchHeartbeat: %v", err)
	}
	if err := store.TouchHeartbeat(ctx, "nobody", beaten); !errors.Is(err, ErrNoSuchHost) {
		t.Fatalf("TouchHeartbeat for an unknown host = %v, want ErrNoSuchHost", err)
	}

	hosts, err := store.Hosts(ctx)
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("Hosts returned %d rows, want 2", len(hosts))
	}
	// Ordered by name.
	if hosts[0].Name != "desktop" || hosts[1].Name != "laptop" {
		t.Fatalf("order = %q, %q; want desktop, laptop", hosts[0].Name, hosts[1].Name)
	}
	if !hosts[0].LastHeartbeat.IsZero() {
		t.Fatal("a host that never sent a heartbeat has a heartbeat time")
	}
	if !hosts[1].LastHeartbeat.Equal(beaten) {
		t.Fatalf("heartbeat = %v, want %v", hosts[1].LastHeartbeat, beaten)
	}
	if !hosts[1].PairedAt.Equal(pairingNow) {
		t.Fatalf("paired at = %v, want %v", hosts[1].PairedAt, pairingNow)
	}
}

func TestPendingCodes(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	if _, err := store.CreatePairingCode(ctx, "laptop", pairingNow); err != nil {
		t.Fatalf("CreatePairingCode laptop: %v", err)
	}
	if _, err := store.CreatePairingCode(ctx, "desktop", pairingNow); err != nil {
		t.Fatalf("CreatePairingCode desktop: %v", err)
	}

	codes, err := store.PendingCodes(ctx, pairingNow)
	if err != nil {
		t.Fatalf("PendingCodes: %v", err)
	}
	if len(codes) != 2 {
		t.Fatalf("PendingCodes = %d, want 2", len(codes))
	}

	// After the TTL, none are pending.
	codes, err = store.PendingCodes(ctx, pairingNow.Add(codeTTL))
	if err != nil {
		t.Fatalf("PendingCodes later: %v", err)
	}
	if len(codes) != 0 {
		t.Fatalf("expired codes are still pending: %v", codes)
	}
}

// TestErrorsNeverCarrySecrets: a code, a credential or a hash in an error message
// would end up in a terminal, a log or a bug report.
func TestErrorsNeverCarrySecrets(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}
	credential, err := store.RedeemCode(ctx, code, "laptop", pairingNow)
	if err != nil {
		t.Fatalf("RedeemCode: %v", err)
	}

	normalised := normaliseCode(code)
	hash := hashText(normalised)

	errorsToCheck := []error{
		func() error { _, err := store.RedeemCode(ctx, code, "laptop", pairingNow); return err }(),
		func() error { _, err := store.Authenticate(ctx, "not-the-credential"); return err }(),
		func() error { return store.Unpair(ctx, "nobody", pairingNow) }(),
		func() error { return store.TouchHeartbeat(ctx, "nobody", pairingNow) }(),
		func() error { _, err := store.CreatePairingCode(ctx, "BAD HOST", pairingNow); return err }(),
	}
	_ = credential

	for _, candidate := range errorsToCheck {
		if candidate == nil {
			continue
		}
		text := candidate.Error()
		for _, secret := range []string{normalised, code, credential, hash} {
			if secret != "" && strings.Contains(text, secret) {
				t.Fatalf("error text carries a secret: %q", text)
			}
		}
	}
}

// TestRedeemCodeForAnyHostReturnsTheMintedHost: `rtk runtime pair` has no host
// flag, so the code alone must identify the host, and it is still single use.
func TestRedeemCodeForAnyHostReturnsTheMintedHost(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()

	code, err := store.CreatePairingCode(ctx, "ubuntu-b", pairingNow)
	if err != nil {
		t.Fatalf("CreatePairingCode: %v", err)
	}

	credential, host, err := store.RedeemCodeForAnyHost(ctx, code, pairingNow)
	if err != nil {
		t.Fatalf("RedeemCodeForAnyHost: %v", err)
	}
	if host != "ubuntu-b" || credential == "" {
		t.Fatalf("got host %q and a credential of length %d", host, len(credential))
	}
	if authenticated, err := store.Authenticate(ctx, credential); err != nil || authenticated != "ubuntu-b" {
		t.Fatalf("Authenticate = %q, %v", authenticated, err)
	}

	if _, _, err := store.RedeemCodeForAnyHost(ctx, code, pairingNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("a second redeem = %v, want ErrBadCode", err)
	}
	if _, _, err := store.RedeemCodeForAnyHost(ctx, "NOT-A-REAL-CODE", pairingNow); !errors.Is(err, ErrBadCode) {
		t.Fatalf("an unknown code = %v, want ErrBadCode", err)
	}
}
