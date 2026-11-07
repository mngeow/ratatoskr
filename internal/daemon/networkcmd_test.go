package daemon

// Untested: under GR-3 nothing here has been run. The table printer is pure, so nothing is dialled.

import (
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// tableNow is the clock the table's ages are measured from.
var tableNow = time.Date(2026, 11, 6, 14, 0, 30, 0, time.UTC)

// TestNetworkTableMissingKeysAreBlank: the Vocabulary rules, in one table.
func TestNetworkTableMissingKeysAreBlank(t *testing.T) {
	view := protocol.NetworkView{
		Schema:    protocol.NetworkSchema,
		FetchedAt: "2026-11-06T14:00:20Z",
		Local: protocol.NetworkLocal{
			Host:   "mac",
			Daemon: protocol.ReportDaemon{Build: "build-1"},
			Sessions: []protocol.ReportSession{
				{Name: "claude", State: protocol.SessionStateIdle, Input: protocol.InputHeld, SessionID: "gw-1"},
				// No session id: the cell must be blank, not a placeholder.
				{Name: "codex", State: protocol.SessionStateUnknown, Input: protocol.InputUnknown},
			},
		},
		Gateway: protocol.NetworkGatewayView{State: protocol.GatewayConnected},
		Snapshot: &protocol.NetworkSnapshot{
			Schema:   protocol.NetworkSchema,
			Revision: 3,
			Hosts: []protocol.NetworkHost{
				{Host: "vmb", Connection: protocol.NetworkConnection{Live: true}, ReportStatus: protocol.ReportStatusFresh,
					Report: &protocol.HostReport{AgeMS: 2000, Seq: 4}},
				{Host: "vmc", Connection: protocol.NetworkConnection{Live: false}, ReportStatus: protocol.ReportStatusNone},
			},
			Sessions: []protocol.NetworkSession{
				{Address: "codex@vmb", Host: "vmb", Name: "codex", SessionID: "s1",
					Directory: protocol.DirectoryFacts{Registered: true},
					Reported:  &protocol.ReportedSession{State: protocol.SessionStateBusy, Input: protocol.InputFree, AgeMS: 2000}},
				// Stale: the state carries its age.
				{Address: "claude@vmc", Host: "vmc", Name: "claude", SessionID: "s2",
					Directory: protocol.DirectoryFacts{Registered: true},
					Reported:  &protocol.ReportedSession{State: protocol.SessionStateIdle, Input: protocol.InputHeld, AgeMS: 61000, Stale: true}},
				// No report at all: the state cell is blank and the directory still lists it.
				{Address: "agent@vmc", Host: "vmc", Name: "agent", SessionID: "s3",
					Directory: protocol.DirectoryFacts{Registered: false}},
			},
		},
	}

	table := FormatNetworkTable(view, tableNow)

	for _, want := range []string{
		"gateway: connected",
		"snapshot fetched 10s",
		"local@mac",
		"claude  idle  held  gw-1",
		"codex  unknown  unknown", // unknown is the word; the missing id leaves the cell blank
		"vmb  live  report fresh, 2s",
		"vmc  not live  no report",
		"codex@vmb  busy  free  yes  s1",
		"claude@vmc  idle (stale, 61s)  held  yes  s2",
		"agent@vmc    no  s3", // no report -> blank state and input
	} {
		if !strings.Contains(table, want) {
			t.Fatalf("the table does not contain %q:\n%s", want, table)
		}
	}

	for _, forbidden := range []string{"n/a", "N/A", "-  ", "null"} {
		if strings.Contains(table, forbidden) {
			t.Fatalf("the table contains the placeholder %q:\n%s", forbidden, table)
		}
	}
}

// TestNetworkTableWithoutSnapshot: a daemon with no gateway snapshot says so and still shows local.
func TestNetworkTableWithoutSnapshot(t *testing.T) {
	view := protocol.NetworkView{
		Schema:  protocol.NetworkSchema,
		Local:   protocol.NetworkLocal{Host: "mac", Sessions: []protocol.ReportSession{{Name: "claude", State: protocol.SessionStateIdle, Input: protocol.InputFree}}},
		Gateway: protocol.NetworkGatewayView{State: protocol.GatewayUnreachable, Error: "connection refused"},
	}

	table := FormatNetworkTable(view, tableNow)
	if !strings.Contains(table, "gateway: unreachable (connection refused)") {
		t.Fatalf("the header lost the state or the error:\n%s", table)
	}
	if !strings.Contains(table, "no snapshot yet") {
		t.Fatalf("the table did not say there is no snapshot:\n%s", table)
	}
	if !strings.Contains(table, "claude  idle  free") {
		t.Fatalf("the local session is missing:\n%s", table)
	}
}

// TestNetworkTableAgesComeFromTheView: a host clock that disagrees cannot change an age.
func TestNetworkTableAgesComeFromTheView(t *testing.T) {
	view := protocol.NetworkView{
		Schema:    protocol.NetworkSchema,
		FetchedAt: "2026-11-06T14:00:20Z",
		Local:     protocol.NetworkLocal{Host: "mac"},
		Gateway:   protocol.NetworkGatewayView{State: protocol.GatewayConnected},
		Snapshot: &protocol.NetworkSnapshot{
			Schema: protocol.NetworkSchema,
			Hosts: []protocol.NetworkHost{{
				Host: "vmb", Connection: protocol.NetworkConnection{Live: true},
				ReportStatus: protocol.ReportStatusFresh,
				Report:       &protocol.HostReport{AgeMS: 5000},
			}},
		},
	}

	// The table is rendered with a much later clock, but the report's age is its own field.
	table := FormatNetworkTable(view, tableNow.Add(time.Hour))
	if !strings.Contains(table, "report fresh, 5s") {
		t.Fatalf("the age was recomputed from a wall clock:\n%s", table)
	}
}

// TestNetworkJSONIsExactlyTheView: the JSON form is one object with the schema's fields.
func TestNetworkJSONIsExactlyTheView(t *testing.T) {
	view := protocol.NetworkView{
		Schema:  protocol.NetworkSchema,
		Local:   protocol.NetworkLocal{Host: "mac"},
		Gateway: protocol.NetworkGatewayView{State: protocol.GatewayNotConfigured},
	}

	encoded := encodeJSON(t, view)
	for _, want := range []string{`"schema":1`, `"local"`, `"gateway"`, `"state":"not_configured"`, `"snapshot":null`} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("the JSON %s does not contain %s", encoded, want)
		}
	}
}

// TestNoDaemonText: the sentence and its exit code.
func TestNoDaemonText(t *testing.T) {
	if noDaemonText != "no rtk daemon is running" {
		t.Fatalf("noDaemonText = %q", noDaemonText)
	}

	coded := &ExitError{Code: 4, Err: errNoDaemonForTest{}}
	if coded.ExitCode() != 4 {
		t.Fatalf("ExitCode = %d", coded.ExitCode())
	}
	if !strings.Contains(coded.Error(), "no daemon") {
		t.Fatalf("the error text = %q", coded.Error())
	}
}

// errNoDaemonForTest is a stand-in cause for the exit-code check.
type errNoDaemonForTest struct{}

// Error renders the stand-in.
func (errNoDaemonForTest) Error() string { return "no daemon" }

// TestTableCleansStringsFromOtherMachines: the gateway (even an older build) may send control
// characters; the table prints to a terminal, so an escape sequence must not reach it.
func TestTableCleansStringsFromOtherMachines(t *testing.T) {
	view := protocol.NetworkView{
		Schema:  protocol.NetworkSchema,
		Gateway: protocol.NetworkGatewayView{State: protocol.GatewayConnected},
		Snapshot: &protocol.NetworkSnapshot{
			Schema: protocol.NetworkSchema,
			Hosts:  []protocol.NetworkHost{{Host: "vm\x1b[2Jb", ReportStatus: protocol.ReportStatusNone}},
			Sessions: []protocol.NetworkSession{{
				Address: "codex@vm\x1b]0;x\x07b", Host: "vmb", Name: "codex",
				Directory: protocol.DirectoryFacts{Registered: true},
				Reported:  &protocol.ReportedSession{State: "busy\r\nforged line", Input: "held"},
			}},
		},
	}

	table := FormatNetworkTable(view, time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC))
	for _, r := range table {
		if r != '\n' && unicode.IsControl(r) {
			t.Fatalf("a control character %U reached the terminal: %q", r, table)
		}
	}
	if strings.Contains(table, "\r") {
		t.Fatalf("a carriage return reached the table: %q", table)
	}
}
