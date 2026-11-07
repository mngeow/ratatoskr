package daemon

// Untested: under GR-3 nothing here has been run. The planner is driven through the real query path
// with fakes, so no socket, no ssh and no harness is involved.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// enterUUID is a valid session id for these tests.
const enterUUID = "0192f2b4-1111-2222-3333-444455556666"

// writeEnterHosts writes a hosts.json for one host into a state directory.
func writeEnterHosts(t *testing.T, stateDir, host string) {
	t.Helper()
	body := `{"version":1,"hosts":{"` + host + `":{"ssh":"me@192.168.1.23","rtk":"/usr/local/bin/rtk"}}}`
	path := filepath.Join(stateDir, "hosts.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write hosts.json: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod hosts.json: %v", err)
	}
}

// networkWithSnapshot installs one snapshot in the service.
func networkWithSnapshot(t *testing.T, fixture *netsvcFixture, host, localID string) {
	t.Helper()
	fixture.service.install(&protocol.NetworkSnapshot{
		Schema:   protocol.NetworkSchema,
		Revision: 2,
		Hosts: []protocol.NetworkHost{{
			Host:         host,
			Connection:   protocol.NetworkConnection{Live: true},
			ReportStatus: protocol.ReportStatusFresh,
		}},
		Sessions: []protocol.NetworkSession{{
			Address:   "codex@" + host,
			Host:      host,
			Name:      "codex",
			SessionID: "gw-1",
			Directory: protocol.DirectoryFacts{Registered: true},
			Reported:  &protocol.ReportedSession{LocalID: localID, State: protocol.SessionStateIdle},
		}},
	})
}

// TestEnterPlanThroughTheQuery builds an ssh plan via the real query path.
func TestEnterPlanThroughTheQuery(t *testing.T) {
	fixture := newNetsvcFixture(t)
	stateDir := t.TempDir()
	fixture.service.setStateDir(stateDir)
	writeEnterHosts(t, stateDir, "vmb")
	networkWithSnapshot(t, fixture, "vmb", enterUUID)

	hub := &hub{gate: newGate(), log: &logger{out: io.Discard}, network: fixture.service}
	w := newWindow("w1", &recordingConn{}, hub, &logger{out: io.Discard})

	envelope, err := encodeRequest(wire.TypeQuery, 1, wire.Query{
		Kind:  wire.QueryEnter,
		Enter: &wire.EnterRequest{Host: "vmb", SessionID: "gw-1"},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	result := w.answer(context.Background(), envelope)
	if result.Error != nil {
		t.Fatalf("the enter query failed: %+v", result.Error)
	}
	if result.Plan == nil {
		t.Fatal("the enter query carried no plan")
	}
	if result.Plan.Kind != wire.EnterSSH {
		t.Fatalf("kind = %q, reason %q", result.Plan.Kind, result.Plan.Reason)
	}
	if len(result.Plan.Argv) != 7 || result.Plan.Argv[0] != "ssh" {
		t.Fatalf("argv = %#v", result.Plan.Argv)
	}
	if !strings.Contains(result.Plan.Argv[6], "--expect-session "+enterUUID) {
		t.Fatalf("remote command = %q", result.Plan.Argv[6])
	}
}

// TestEnterWithoutHostsFile: no hosts.json means a manual plan, not a failure.
func TestEnterWithoutHostsFile(t *testing.T) {
	fixture := newNetsvcFixture(t)
	fixture.service.setStateDir(t.TempDir())
	networkWithSnapshot(t, fixture, "vmb", enterUUID)

	hub := &hub{gate: newGate(), log: &logger{out: io.Discard}, network: fixture.service}
	w := newWindow("w1", &recordingConn{}, hub, &logger{out: io.Discard})

	envelope, err := encodeRequest(wire.TypeQuery, 1, wire.Query{
		Kind:  wire.QueryEnter,
		Enter: &wire.EnterRequest{Host: "vmb", SessionID: "gw-1"},
	})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	result := w.answer(context.Background(), envelope)
	if result.Error != nil {
		t.Fatalf("the enter query failed: %+v", result.Error)
	}
	if result.Plan == nil || result.Plan.Kind != wire.EnterManual {
		t.Fatalf("plan = %+v, want manual", result.Plan)
	}
	if !strings.Contains(result.Plan.Reason, "no hosts.json") {
		t.Fatalf("reason = %q", result.Plan.Reason)
	}
}

// TestEnterQueryNeedsARequest: a query without one is a bad request.
func TestEnterQueryNeedsARequest(t *testing.T) {
	fixture := newNetsvcFixture(t)
	hub := &hub{gate: newGate(), log: &logger{out: io.Discard}, network: fixture.service}
	w := newWindow("w1", &recordingConn{}, hub, &logger{out: io.Discard})

	envelope, err := encodeRequest(wire.TypeQuery, 1, wire.Query{Kind: wire.QueryEnter})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	result := w.answer(context.Background(), envelope)
	if result.Error == nil || result.Error.Code != wire.CodeBadRequest {
		t.Fatalf("result = %+v, want bad_request", result)
	}
}
