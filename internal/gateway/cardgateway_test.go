package gateway

// Untested: under GR-3 nothing here has been run. Databases are temporary; the only sockets are the
// loopback httptest servers in the exchange tests.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// validTargetCard builds a card that passes polled parsing, with the given own URL.
func validTargetCard(t *testing.T, ownURL string) (body []byte, parsed protocol.HostCard) {
	t.Helper()
	parsed = protocol.HostCard{
		Name:        "vmb",
		Description: "Build host",
		SupportedInterfaces: []protocol.CardInterface{{
			URL: ownURL, ProtocolBinding: protocol.CardProtocolBinding, ProtocolVersion: protocol.CardProtocolVersion,
		}},
		Version: "4",
		Capabilities: protocol.CardCapabilities{
			Extensions: []protocol.CardExtension{{URI: protocol.CardExtensionURI, Params: protocol.CardExtensionParams{Skills: map[string]protocol.SkillEvidence{}}}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []protocol.CardSkill{},
	}
	canonical, err := card.Canonical(parsed)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	return canonical, parsed
}

// TestParseCardTarget: the scheme, flag, address and path rules.
func TestParseCardTarget(t *testing.T) {
	pin := "sha256:" + strings.Repeat("ab", 32)
	tests := []struct {
		name     string
		url      string
		pin      string
		insecure bool
		wantErr  string // substring; "" means accepted
		wantURL  string
	}{
		{"tailscale http", "http://100.64.0.5:7778", "", false, "", "http://100.64.0.5:7778" + protocol.CardPath},
		{"loopback http with the well-known path", "http://127.0.0.1:7778" + protocol.CardPath, "", false, "", "http://127.0.0.1:7778" + protocol.CardPath},
		{"trailing slash", "http://100.64.0.5:7778/", "", false, "", "http://100.64.0.5:7778" + protocol.CardPath},
		{"ipv6 tailscale", "http://[fd7a:115c:a1e0::5]:7778", "", false, "", "http://[fd7a:115c:a1e0::5]:7778" + protocol.CardPath},
		{"lan https with pin", "https://192.168.1.4:8443", pin, false, "", "https://192.168.1.4:8443" + protocol.CardPath},
		{"lan http with insecure", "http://192.168.1.4:8443", "", true, "", "http://192.168.1.4:8443" + protocol.CardPath},

		{"lan http without insecure", "http://192.168.1.4:8443", "", false, "--insecure-http", ""},
		{"lan https without pin", "https://192.168.1.4:8443", "", false, "--fingerprint", ""},
		{"lan https with bad pin", "https://192.168.1.4:8443", "sha256:zz", false, "fingerprint", ""},
		{"https on tailscale", "https://100.64.0.5:7778", pin, false, "LAN", ""},
		{"https on loopback", "https://127.0.0.1:7778", pin, false, "LAN", ""},
		{"pin with http", "http://100.64.0.5:7778", pin, false, "--fingerprint applies only", ""},
		{"insecure on tailscale", "http://100.64.0.5:7778", "", true, "--insecure-http applies only", ""},
		{"insecure with https", "https://192.168.1.4:8443", pin, true, "--insecure-http applies only", ""},
		{"hostname", "http://example.com:7778", "", false, "IP", ""},
		{"no port", "http://100.64.0.5", "", false, "port", ""},
		{"public address", "http://8.8.8.8:80", "", false, "not a loopback", ""},
		{"credentials", "http://user:pw@100.64.0.5:7778", "", false, "credentials", ""},
		{"query", "http://100.64.0.5:7778/?x=1", "", false, "credentials, query", ""},
		{"other path", "http://100.64.0.5:7778/other", "", false, "path is fixed", ""},
		{"alias path", "http://100.64.0.5:7778/.well-known/agent.json", "", false, "path is fixed", ""},
		{"ftp", "ftp://100.64.0.5:7778", "", false, "http://", ""},
		{"metadata link-local", "http://169.254.169.254:80", "", false, "not a loopback", ""},
		{"alibaba metadata", "http://100.100.100.200:80", "", false, "refused", ""},
		{"tailscale resolver", "http://100.100.100.100:80", "", false, "refused", ""},
		{"tailscale resolver v6", "http://[fd7a:115c:a1e0::53]:80", "", false, "refused", ""},
		{"aws v6 metadata", "http://[fd00:ec2::254]:80", "", false, "refused", ""},
		{"empty", "", "", false, "must look like", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, err := ParseCardTarget(tc.url, tc.pin, tc.insecure)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if target.URL() != tc.wantURL {
				t.Fatalf("URL = %q, want %q", target.URL(), tc.wantURL)
			}
		})
	}
}

// TestDenyReason: each listed address, the predicates, and the unmapping of 4-in-6.
func TestDenyReason(t *testing.T) {
	denied := []string{"169.254.169.254", "169.254.0.1", "100.100.100.200", "100.100.100.100", "fd7a:115c:a1e0::53",
		"fd00:ec2::254", "0.0.0.0", "::", "224.0.0.1", "ff02::1", "fe80::1", "::ffff:169.254.169.254"}
	for _, text := range denied {
		if DenyReason(netip.MustParseAddr(text)) == "" {
			t.Errorf("%s was not denied", text)
		}
	}
	allowed := []string{"127.0.0.1", "100.64.0.5", "100.100.100.201", "10.0.0.4", "192.168.1.4", "fd7a:115c:a1e0::5", "fd00:ec2::255", "::1"}
	for _, text := range allowed {
		if reason := DenyReason(netip.MustParseAddr(text)); reason != "" {
			t.Errorf("%s was denied: %s", text, reason)
		}
	}
}

// TestDialControlChecksTheAddressActuallyDialled: equality with the registered address, class and the
// deny list, re-applied at connect time.
func TestDialControlChecksTheAddressActuallyDialled(t *testing.T) {
	target := CardTarget{Host: "vmb", Scheme: "http", Addr: netip.MustParseAddrPort("100.64.0.5:7778"), Class: ClassTailscale}
	control := cardDialControl(target)

	tests := []struct {
		address string
		ok      bool
	}{
		{"100.64.0.5:7778", true},
		{"[::ffff:100.64.0.5]:7778", true},
		{"100.64.0.6:7778", false},
		{"100.64.0.5:7779", false},
		{"100.100.100.200:7778", false},
		{"169.254.169.254:80", false},
		{"not an address", false},
		{"127.0.0.1:7778", false},
	}
	for _, tc := range tests {
		err := control("tcp", tc.address, nil)
		if (err == nil) != tc.ok {
			t.Errorf("control(%q) = %v, want ok=%t", tc.address, err, tc.ok)
		}
	}
}

// pairedWithTarget pairs a host and registers a loopback target for it.
func pairedWithTarget(t *testing.T, store *Store, addr string, now time.Time) CardTarget {
	t.Helper()
	pairHostFrom(t, store, "vmb", now)
	target := CardTarget{Host: "vmb", Scheme: "http", Addr: netip.MustParseAddrPort(addr), Class: ClassLoopback}
	if _, err := store.SetCardTarget(context.Background(), target, now); err != nil {
		t.Fatalf("SetCardTarget: %v", err)
	}
	return target
}

// TestSetCardTargetRules: a paired host only; a changed target clears the stored card; the same target
// again keeps it; clear removes everything.
func TestSetCardTargetRules(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	ghost := CardTarget{Host: "ghost", Scheme: "http", Addr: netip.MustParseAddrPort("127.0.0.1:1"), Class: ClassLoopback}
	if _, err := store.SetCardTarget(ctx, ghost, now); err == nil {
		t.Fatal("a target for an unpaired host was accepted")
	}

	target := pairedWithTarget(t, store, "127.0.0.1:7778", now)
	body, parsed := validTargetCard(t, "http://127.0.0.1:7778"+protocol.CardPath)
	applied, err := store.RecordPoll(ctx, PollRecord{
		Host: "vmb", TargetUpdatedAt: now, At: now, Outcome: protocol.PollOK, Status: 200,
		CardJSON: string(body), Revision: parsed.Version, ETag: `"e1"`, Hash: card.Hash(body),
	})
	if err != nil || !applied {
		t.Fatalf("RecordPoll applied=%t err=%v", applied, err)
	}

	// The same target again: only updated_at moves; the card stays.
	later := now.Add(time.Minute)
	changed, err := store.SetCardTarget(ctx, target, later)
	if err != nil || changed {
		t.Fatalf("identical target changed=%t err=%v", changed, err)
	}
	rows, _ := store.CardRows(ctx)
	if rows["vmb"].CardJSON == "" {
		t.Fatal("an identical target cleared the card")
	}

	// A different address: the card was fetched from somewhere else and goes.
	other := target
	other.Addr = netip.MustParseAddrPort("127.0.0.1:9999")
	changed, err = store.SetCardTarget(ctx, other, later.Add(time.Minute))
	if err != nil || !changed {
		t.Fatalf("different target changed=%t err=%v", changed, err)
	}
	rows, _ = store.CardRows(ctx)
	if _, ok := rows["vmb"]; ok {
		t.Fatal("a changed target kept the stored card")
	}

	if err := store.ClearCardTarget(ctx, "vmb"); err != nil {
		t.Fatalf("ClearCardTarget: %v", err)
	}
	if err := store.ClearCardTarget(ctx, "vmb"); err != ErrNoSuchCardTarget {
		t.Fatalf("second clear = %v, want ErrNoSuchCardTarget", err)
	}
}

// TestRecordPollSemantics: a good card replaces and keeps the older one; 304 moves last_ok only; a
// failure keeps the card; a result for a changed target is dropped.
func TestRecordPollSemantics(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pairedWithTarget(t, store, "127.0.0.1:7778", now)

	first, _ := validTargetCard(t, "http://127.0.0.1:7778"+protocol.CardPath)
	record := func(at time.Time, outcome string, body []byte, version string) bool {
		t.Helper()
		pr := PollRecord{Host: "vmb", TargetUpdatedAt: now, At: at, Outcome: outcome, Status: 200, Duration: 5 * time.Millisecond}
		if body != nil {
			pr.CardJSON, pr.Revision, pr.ETag, pr.Hash = string(body), version, `"e-`+version+`"`, card.Hash(body)
		}
		applied, err := store.RecordPoll(ctx, pr)
		if err != nil {
			t.Fatalf("RecordPoll: %v", err)
		}
		return applied
	}

	if !record(now, protocol.PollOK, first, "1") {
		t.Fatal("first poll not applied")
	}
	// A failed poll keeps the card and records the attempt.
	record(now.Add(time.Minute), protocol.PollTimeout, nil, "")
	rows, _ := store.CardRows(ctx)
	if rows["vmb"].CardJSON == "" || rows["vmb"].LastResult != protocol.PollTimeout {
		t.Fatalf("after a timeout: %+v", rows["vmb"])
	}
	if !rows["vmb"].LastOKAt.Equal(time.UnixMilli(now.UnixMilli())) {
		t.Fatalf("a timeout moved last_ok_at: %v", rows["vmb"].LastOKAt)
	}
	// A 304 moves last_ok only.
	record(now.Add(2*time.Minute), protocol.PollNotModified, nil, "")
	rows, _ = store.CardRows(ctx)
	if !rows["vmb"].LastOKAt.Equal(time.UnixMilli(now.Add(2 * time.Minute).UnixMilli())) {
		t.Fatalf("304 did not move last_ok_at: %v", rows["vmb"].LastOKAt)
	}
	if rows["vmb"].Hash != card.Hash(first) {
		t.Fatal("304 changed the stored card")
	}

	// A different good card: the old one becomes a revision.
	second := []byte(strings.Replace(string(first), `"version":"4"`, `"version":"5"`, 1))
	record(now.Add(3*time.Minute), protocol.PollOK, second, "5")
	var revisions int
	if err := store.db.QueryRow(`SELECT count(*) FROM card_revisions WHERE host='vmb'`).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatalf("revisions = %d, %v", revisions, err)
	}

	// A result for a target that changed meanwhile is dropped.
	stale := PollRecord{Host: "vmb", TargetUpdatedAt: now.Add(-time.Hour), At: now, Outcome: protocol.PollOK, Status: 200}
	if applied, err := store.RecordPoll(ctx, stale); err != nil || applied {
		t.Fatalf("stale result applied=%t err=%v", applied, err)
	}
}

// TestRecordPollPrunesAttemptsAndRevisions: the per-host bounds hold.
func TestRecordPollPrunesAttemptsAndRevisions(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pairedWithTarget(t, store, "127.0.0.1:7778", now)

	for i := 0; i < maxPollAttemptsKept+25; i++ {
		_, err := store.RecordPoll(ctx, PollRecord{
			Host: "vmb", TargetUpdatedAt: now, At: now.Add(time.Duration(i) * time.Second), Outcome: protocol.PollTimeout,
		})
		if err != nil {
			t.Fatalf("RecordPoll: %v", err)
		}
	}
	var attempts int
	if err := store.db.QueryRow(`SELECT count(*) FROM poll_attempts WHERE host='vmb'`).Scan(&attempts); err != nil || attempts != maxPollAttemptsKept {
		t.Fatalf("attempts = %d, %v, want %d", attempts, err, maxPollAttemptsKept)
	}

	base, _ := validTargetCard(t, "http://127.0.0.1:7778"+protocol.CardPath)
	for i := 0; i < maxCardRevisionsKept+5; i++ {
		body := []byte(strings.Replace(string(base), `"version":"4"`, `"version":"`+string(rune('a'+i%26))+`"`, 1))
		store.RecordPoll(ctx, PollRecord{
			Host: "vmb", TargetUpdatedAt: now, At: now.Add(time.Duration(i) * time.Minute), Outcome: protocol.PollOK, Status: 200,
			CardJSON: string(body), Revision: "r", ETag: `"e"`, Hash: card.Hash(body) + string(rune('a'+i)),
		})
	}
	var revisions int
	if err := store.db.QueryRow(`SELECT count(*) FROM card_revisions WHERE host='vmb'`).Scan(&revisions); err != nil || revisions != maxCardRevisionsKept {
		t.Fatalf("revisions = %d, %v, want %d", revisions, err, maxCardRevisionsKept)
	}
}

// TestMessageEdges: decided sends between paired hosts only, host_stored is the subset, no same-host
// pairs, unparseable addresses skipped, rows older than the cutoff end the walk.
func TestMessageEdges(t *testing.T) {
	store, _ := openStore(t, false)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	insert := func(id, from, to, result string, at time.Time) {
		t.Helper()
		if _, err := store.db.Exec(`INSERT INTO messages(id, from_address, to_address, body, result, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?)`, id, from, to, "BODY-NEVER-READ", result, at.UnixMilli(), at.UnixMilli()); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	insert("old", "a@mac", "b@vmb", "host_stored", now.Add(-48*time.Hour))
	insert("m1", "a@mac", "b@vmb", "host_stored", now.Add(-3*time.Hour))
	insert("m2", "a@mac", "b@vmb", "destination_unreachable", now.Add(-2*time.Hour))
	insert("m3", "a@mac", "b@vmb", "stored", now.Add(-time.Hour))
	insert("m4", "b@vmb", "a@mac", "host_stored", now.Add(-time.Minute))
	insert("m5", "a@mac", "c@mac", "host_stored", now)
	insert("m6", "a@mac", "z@gone", "host_stored", now)
	insert("m7", "bad address", "b@vmb", "host_stored", now)

	paired := map[string]bool{"mac": true, "vmb": true}
	edges, partial, err := store.MessageEdges(ctx, paired, now.Add(-24*time.Hour))
	if err != nil || partial {
		t.Fatalf("MessageEdges err=%v partial=%t", err, partial)
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %+v", edges)
	}
	if edges[0].From != "mac" || edges[0].To != "vmb" || edges[0].Count != 2 || edges[0].HostStored != 1 {
		t.Fatalf("mac->vmb = %+v, want count 2 (not the stored one, not the old one), host_stored 1", edges[0])
	}
	if edges[1].From != "vmb" || edges[1].To != "mac" || edges[1].Count != 1 || edges[1].HostStored != 1 {
		t.Fatalf("vmb->mac = %+v", edges[1])
	}
	for _, edge := range edges {
		if edge.Kind != protocol.GraphEdgeMessagesSeen || edge.WindowHours != 24 || edge.Source != protocol.GraphSourceLedger {
			t.Fatalf("edge labels = %+v", edge)
		}
	}
}

// TestFitGraphOmitsLargestFirstAndStably: bodies go largest first, ties by name, and the flags say so.
func TestFitGraphOmitsLargestFirstAndStably(t *testing.T) {
	_, small := validTargetCard(t, "http://127.0.0.1:1"+protocol.CardPath)
	big := small
	big.Description = strings.Repeat("x", protocol.MaxCardDescription)
	for i := 0; i < protocol.MaxCardSkills; i++ {
		big.Skills = append(big.Skills, protocol.CardSkill{ID: "s" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Name: strings.Repeat("n", 60), Description: strings.Repeat("d", 270), Tags: []string{"t"}})
	}

	build := func(count int) protocol.GraphSnapshot {
		snapshot := protocol.GraphSnapshot{Schema: 1}
		for i := 0; i < count; i++ {
			c := big
			c.Name = "host"
			snapshot.Hosts = append(snapshot.Hosts, protocol.GraphHost{
				Name: "h" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
				Card: protocol.GraphCard{State: protocol.CardFresh, Card: &c},
			})
		}
		return snapshot
	}

	small1 := protocol.GraphSnapshot{Schema: 1, Hosts: []protocol.GraphHost{{Name: "a", Card: protocol.GraphCard{Card: &small}}}}
	if err := fitGraph(&small1); err != nil || small1.Truncated {
		t.Fatalf("a small snapshot was truncated: %v", err)
	}

	snapshot := build(40)
	if err := fitGraph(&snapshot); err != nil {
		t.Fatalf("fitGraph: %v", err)
	}
	if !snapshot.Truncated {
		t.Fatal("an oversize snapshot was not truncated")
	}
	encoded, _ := json.Marshal(snapshot)
	if len(encoded) > protocol.MaxGraphBytes {
		t.Fatalf("still %d bytes after fitting", len(encoded))
	}
	omitted := 0
	for _, host := range snapshot.Hosts {
		if host.Card.CardOmitted {
			omitted++
			if host.Card.Card != nil {
				t.Fatal("an omitted card kept its body")
			}
		}
		if host.Name == "" || host.Card.State != protocol.CardFresh {
			t.Fatalf("a host lost its summary: %+v", host)
		}
	}
	if omitted == 0 {
		t.Fatal("nothing was omitted")
	}

	// Same inputs, same omission.
	again := build(40)
	_ = fitGraph(&again)
	for index := range snapshot.Hosts {
		if snapshot.Hosts[index].Card.CardOmitted != again.Hosts[index].Card.CardOmitted {
			t.Fatal("omission is not stable")
		}
	}
}

// TestGraphSignatureIgnoresAgesButNotState: an age or an attempt time moves nothing; state, restored
// and omission do.
func TestGraphSignatureIgnoresAgesButNotState(t *testing.T) {
	age := int64(5)
	base := protocol.GraphSnapshot{
		Schema: 1, Revision: 3, GeneratedAt: "2026-11-07T10:00:00Z",
		Hosts: []protocol.GraphHost{{Name: "vmb",
			Connection: protocol.GraphConnection{Live: true, HeartbeatAgeMS: &age},
			Card:       protocol.GraphCard{State: protocol.CardFresh, Restored: true, AgeMS: &age, LastOKAt: "a", LastAttemptAt: "b"},
		}},
	}
	signatureOf := func(s protocol.GraphSnapshot) string {
		t.Helper()
		out, err := graphSignature(s)
		if err != nil {
			t.Fatalf("graphSignature: %v", err)
		}
		return out
	}
	reference := signatureOf(base)

	aged := base
	aged.Hosts = []protocol.GraphHost{base.Hosts[0]}
	otherAge := int64(99)
	aged.Hosts[0].Connection.HeartbeatAgeMS = &otherAge
	aged.Hosts[0].Card.AgeMS = &otherAge
	aged.Hosts[0].Card.LastOKAt, aged.Hosts[0].Card.LastAttemptAt = "c", "d"
	aged.GeneratedAt, aged.Revision = "later", 9
	if signatureOf(aged) != reference {
		t.Fatal("ages, times or the revision changed the signature")
	}

	for name, edit := range map[string]func(*protocol.GraphHost){
		"state":    func(h *protocol.GraphHost) { h.Card.State = protocol.CardStale },
		"restored": func(h *protocol.GraphHost) { h.Card.Restored = false },
		"omitted":  func(h *protocol.GraphHost) { h.Card.CardOmitted = true },
		"live":     func(h *protocol.GraphHost) { h.Connection.Live = false },
		"result":   func(h *protocol.GraphHost) { h.Card.LastResult = protocol.PollTimeout },
	} {
		changed := base
		changed.Hosts = []protocol.GraphHost{base.Hosts[0]}
		edit(&changed.Hosts[0])
		if signatureOf(changed) == reference {
			t.Errorf("a %s change did not move the signature", name)
		}
	}
}

// TestGraphRouteEndToEnd: the route needs a credential, returns the separate connection and card
// signals, a stored card, and the 204 on an equal revision.
func TestGraphRouteEndToEnd(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "vmb", now)
	server := NewServer(store, Options{Now: func() time.Time { return now }})
	server.streamOpen = func(string) bool { return true }

	if recorder := call(server, http.MethodGet, protocol.GraphPath, version(), "", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("no credential = %d", recorder.Code)
	}

	target := CardTarget{Host: "vmb", Scheme: "http", Addr: netip.MustParseAddrPort("127.0.0.1:7778"), Class: ClassLoopback}
	if _, err := store.SetCardTarget(context.Background(), target, now); err != nil {
		t.Fatalf("SetCardTarget: %v", err)
	}
	body, parsed := validTargetCard(t, "http://127.0.0.1:7778"+protocol.CardPath)
	if _, err := store.RecordPoll(context.Background(), PollRecord{
		Host: "vmb", TargetUpdatedAt: now, At: now.Add(-10 * time.Second), Outcome: protocol.PollOK, Status: 200,
		CardJSON: string(body), Revision: parsed.Version, ETag: `"e"`, Hash: card.Hash(body),
	}); err != nil {
		t.Fatalf("RecordPoll: %v", err)
	}

	recorder := call(server, http.MethodGet, protocol.GraphPath, version(), credential, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("graph = %d: %s", recorder.Code, recorder.Body.String())
	}
	var snapshot protocol.GraphSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snapshot.Schema != protocol.GraphSchema || snapshot.Revision != 1 || len(snapshot.Hosts) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	host := snapshot.Hosts[0]
	if !host.Connection.Live || host.Connection.Source != protocol.GraphSourceStream {
		t.Fatalf("connection = %+v", host.Connection)
	}
	if host.Card.State != protocol.CardFresh || host.Card.Source != protocol.GraphSourcePoll || host.Card.Card == nil || host.Card.Card.Name != "vmb" {
		t.Fatalf("card = %+v", host.Card)
	}
	if !host.Card.Restored {
		t.Fatal("a stored card read with no poller running must be restored")
	}

	// An equal revision with no wait is a 204.
	recorder = call(server, http.MethodGet, protocol.GraphPath+"?since=1", version(), credential, "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("equal revision = %d", recorder.Code)
	}
	// A bad since is a 400.
	recorder = call(server, http.MethodGet, protocol.GraphPath+"?since=x", version(), credential, "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad since = %d", recorder.Code)
	}
}

// TestPollExchange: the poller's request against a loopback test server, for each outcome the
// contract names. The request carries no credential and only If-None-Match as a conditional.
func TestPollExchange(t *testing.T) {
	var seen *http.Request
	var status int
	var contentType string
	var respBody []byte
	var etag string

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		if status == 302 {
			w.Header().Set("Location", "http://127.0.0.1:1/elsewhere")
		}
		w.WriteHeader(status)
		_, _ = w.Write(respBody)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()

	addr := netip.MustParseAddrPort(listener.Addr().String())
	target := CardTarget{Host: "vmb", Scheme: "http", Addr: addr, Class: ClassLoopback}
	good, _ := validTargetCard(t, "http://"+addr.String()+protocol.CardPath)

	poller := newCardPoller(nil, time.Minute, time.Now, nil, nil)

	tests := []struct {
		name        string
		status      int
		contentType string
		body        []byte
		etag        string
		stored      string
		want        string
		wantCard    bool
	}{
		{"valid card", 200, "application/json", good, `"e1"`, "", protocol.PollOK, true},
		{"not modified with a stored card", 304, "", nil, "", `"e1"`, protocol.PollNotModified, false},
		{"not modified with nothing stored", 304, "", nil, "", "", protocol.PollHTTPStatus, false},
		{"redirect", 302, "", nil, "", "", protocol.PollRedirect, false},
		{"server error", 500, "", nil, "", "", protocol.PollHTTPStatus, false},
		{"not json", 200, "text/html", []byte("<html>"), "", "", protocol.PollInvalid, false},
		{"bad card", 200, "application/json", []byte(`{"name":5}`), "", "", protocol.PollInvalid, false},
		{"oversize", 200, "application/json", []byte(strings.Repeat(" ", protocol.MaxCardPollBytes+10)), "", "", protocol.PollOversize, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, contentType, respBody, etag = tc.status, tc.contentType, tc.body, tc.etag
			if tc.name == "valid card" {
				etag = `"e1"`
			}
			seen = nil

			record := poller.exchange(context.Background(), target, tc.stored)
			if record.Outcome != tc.want {
				t.Fatalf("outcome = %q (%s), want %q", record.Outcome, record.Reason, tc.want)
			}
			if (record.CardJSON != "") != tc.wantCard {
				t.Fatalf("card stored = %t", record.CardJSON != "")
			}
			if seen == nil {
				t.Fatal("the test server saw no request")
			}
			for _, header := range []string{"Authorization", "Cookie", "X-Ratatoskr-Version"} {
				if seen.Header.Get(header) != "" {
					t.Fatalf("the poll sent %s", header)
				}
			}
			if got := seen.Header.Get("If-None-Match"); got != tc.stored {
				t.Fatalf("If-None-Match = %q, want %q", got, tc.stored)
			}
			if seen.Header.Get("Accept") != "application/json" {
				t.Fatalf("Accept = %q", seen.Header.Get("Accept"))
			}
			for _, field := range []string{record.Reason} {
				if strings.Contains(field, addr.String()) {
					t.Fatalf("a reason carries the address: %q", field)
				}
			}
		})
	}
}

// TestPollExchangeRefusedConnection: nothing listening is a refusal with no address in the reason.
func TestPollExchangeRefusedConnection(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback listener: %v", err)
	}
	addr := netip.MustParseAddrPort(listener.Addr().String())
	listener.Close()

	target := CardTarget{Host: "vmb", Scheme: "http", Addr: addr, Class: ClassLoopback}
	poller := newCardPoller(nil, time.Minute, time.Now, nil, nil)
	record := poller.exchange(context.Background(), target, "")
	if record.Outcome != protocol.PollRefused {
		t.Fatalf("outcome = %q", record.Outcome)
	}
	if strings.Contains(record.Reason, "127.0.0.1") || strings.Contains(record.Reason, ":") {
		t.Fatalf("reason = %q", record.Reason)
	}
}

// TestCardPollIntervalClamp: the flag range.
func TestCardPollIntervalClamp(t *testing.T) {
	tests := map[time.Duration]time.Duration{
		0: DefaultCardPollInterval, time.Second: MinCardPollInterval, 10 * time.Second: 10 * time.Second,
		time.Minute: time.Minute, 24 * time.Hour: MaxCardPollInterval,
	}
	for in, want := range tests {
		if got := ClampCardPollInterval(in); got != want {
			t.Errorf("Clamp(%v) = %v, want %v", in, got, want)
		}
	}
}

// TestGraphSurvivesAFailingEdgeWalk: a ledger the walk cannot read does not take the hosts down; the
// answer is 200 with every host, and the edges are marked partial.
func TestGraphSurvivesAFailingEdgeWalk(t *testing.T) {
	now := time.Unix(1700000000, 0)
	store, _ := openStore(t, false)
	credential := pairHostFrom(t, store, "vmb", now)
	server := NewServer(store, Options{Now: func() time.Time { return now }})
	server.streamOpen = func(string) bool { return true }

	// Make the walk fail: with the table gone, the query errors.
	if _, err := store.db.Exec(`DROP TABLE message_events`); err != nil {
		t.Fatalf("drop message_events: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE messages`); err != nil {
		t.Fatalf("drop messages: %v", err)
	}

	recorder := call(server, http.MethodGet, protocol.GraphPath, version(), credential, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("graph = %d: %s", recorder.Code, recorder.Body.String())
	}
	var snapshot protocol.GraphSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(snapshot.Hosts) != 1 || snapshot.Hosts[0].Name != "vmb" {
		t.Fatalf("hosts = %+v", snapshot.Hosts)
	}
	if !snapshot.EdgesPartial || len(snapshot.Edges) != 0 {
		t.Fatalf("edges_partial = %t, edges = %+v", snapshot.EdgesPartial, snapshot.Edges)
	}

	// A second compute inside the cache window stays partial and does not retry the walk.
	again := call(server, http.MethodGet, protocol.GraphPath, version(), credential, "")
	var second protocol.GraphSnapshot
	_ = json.Unmarshal(again.Body.Bytes(), &second)
	if again.Code != http.StatusOK || !second.EdgesPartial {
		t.Fatalf("second graph = %d partial=%t", again.Code, second.EdgesPartial)
	}
}

// TestUseAfterCloseIsAnErrorNotAPanic: a recompute that outlives the store gets database/sql's error.
func TestUseAfterCloseIsAnErrorNotAPanic(t *testing.T) {
	store, _ := openStore(t, false)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := store.Hosts(context.Background()); err == nil {
		t.Fatal("a closed store answered")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
