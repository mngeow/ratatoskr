package entry

// Untested: under GR-3 nothing here has been run. Plans are pure values, so no ssh and no daemon is
// involved.

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	planUUID   = "0192f2b4-1111-2222-3333-444455556666"
	otherUUID  = "0192f2b4-aaaa-bbbb-cccc-ddddeeeeffff"
	planHost   = "vmb"
	planRemote = "/usr/local/bin/rtk"
)

// snapshotWith builds a snapshot with one host and one session.
func snapshotWith(host, sessionID, localID string, stale, live bool) *protocol.NetworkSnapshot {
	snapshot := &protocol.NetworkSnapshot{
		Schema: protocol.NetworkSchema,
		Hosts: []protocol.NetworkHost{{
			Host:         host,
			Connection:   protocol.NetworkConnection{Live: live},
			ReportStatus: protocol.ReportStatusFresh,
		}},
		Sessions: []protocol.NetworkSession{{
			Address:   "codex@" + host,
			Host:      host,
			Name:      "codex",
			SessionID: sessionID,
			Directory: protocol.DirectoryFacts{Registered: true},
		}},
	}
	if localID != "" {
		snapshot.Sessions[0].Reported = &protocol.ReportedSession{
			LocalID: localID, State: protocol.SessionStateIdle, AgeMS: 12000, Stale: stale,
		}
	}
	return snapshot
}

// usableHosts is a host map with one usable entry.
func usableHosts() HostMap {
	return HostMap{Hosts: map[string]HostResult{
		planHost: {Entry: HostEntry{SSH: "me@192.168.1.23", RTK: planRemote}},
	}}
}

// TestBuildPlanRemoteSSH: the argv is the exact seven-element list.
func TestBuildPlanRemoteSSH(t *testing.T) {
	plan := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith(planHost, "gw-1", planUUID, false, true),
		Hosts:     usableHosts(),
	})

	if plan.Kind != wire.EnterSSH {
		t.Fatalf("kind = %q, want ssh (reason %q)", plan.Kind, plan.Reason)
	}

	want := []string{
		"ssh", "-t", "-o", "ConnectTimeout=5", "--",
		"me@192.168.1.23",
		planRemote + " attach --expect-session " + planUUID + " --entered-via-ssh",
	}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", plan.Argv, want)
	}
	if len(plan.Argv) != 7 {
		t.Fatalf("argv has %d elements, want 7", len(plan.Argv))
	}
	if plan.Argv[0] != "ssh" {
		t.Fatalf("argv[0] = %q, want the program name", plan.Argv[0])
	}
	if plan.Argv[3] != "ConnectTimeout=5" || plan.Argv[2] != "-o" || plan.Argv[4] != "--" {
		t.Fatalf("the -o pair or -- moved: %#v", plan.Argv)
	}

	// Nothing else may follow -o, and nothing may sit before -- except ssh, -t and the -o pair.
	beforeDash := plan.Argv[:4]
	wantBefore := []string{"ssh", "-t", "-o", "ConnectTimeout=5"}
	if !reflect.DeepEqual(beforeDash, wantBefore) {
		t.Fatalf("the options before -- are %#v, want %#v", beforeDash, wantBefore)
	}

	if plan.Display == "" || !strings.Contains(plan.Display, "'"+plan.Argv[6]+"'") {
		t.Fatalf("display = %q, want the remote argument single-quoted", plan.Display)
	}
	if !strings.HasPrefix(plan.Manual, "on "+planHost+": ") {
		t.Fatalf("manual = %q", plan.Manual)
	}
	if plan.Note != "" {
		t.Fatalf("note = %q, want none for a fresh live report", plan.Note)
	}
	if plan.LocalID != planUUID {
		t.Fatalf("local id = %q", plan.LocalID)
	}
}

// TestBuildPlanWithStateHome: the env prefix is part of the ONE remote argument.
func TestBuildPlanWithStateHome(t *testing.T) {
	hosts := HostMap{Hosts: map[string]HostResult{
		planHost: {Entry: HostEntry{SSH: "build-vmc", RTK: "/opt/rtk/bin/rtk", StateHome: "/home/me/.local/state"}},
	}}

	plan := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith(planHost, "gw-1", planUUID, false, true),
		Hosts:     hosts,
	})

	want := []string{
		"ssh", "-t", "-o", "ConnectTimeout=5", "--", "build-vmc",
		"env XDG_STATE_HOME=/home/me/.local/state /opt/rtk/bin/rtk attach --expect-session " + planUUID + " --entered-via-ssh",
	}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("argv = %#v, want %#v", plan.Argv, want)
	}
}

// TestBuildPlanLocalRow: a local row, in and out of this window.
func TestBuildPlanLocalRow(t *testing.T) {
	local := []protocol.ReportSession{{SessionID: "gw-1", LocalID: planUUID, Name: "claude"}}

	inside := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{LocalID: planUUID},
		LocalHost: "mac",
		Local:     local,
		InWindow:  func(string) bool { return true },
	})
	if inside.Kind != wire.EnterLocal || !inside.InWindow || inside.Note != "" {
		t.Fatalf("plan = %+v", inside)
	}
	if len(inside.Argv) != 0 {
		t.Fatalf("a local plan carried an argv: %#v", inside.Argv)
	}

	outside := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{LocalID: planUUID},
		LocalHost: "mac",
		Local:     local,
		InWindow:  func(string) bool { return false },
	})
	if outside.Kind != wire.EnterLocal || outside.InWindow {
		t.Fatalf("plan = %+v", outside)
	}
	if outside.Note != "not in this window; reattach to see it" {
		t.Fatalf("note = %q", outside.Note)
	}
}

// TestBuildPlanRefusals: every refusal, and never an argv.
func TestBuildPlanRefusals(t *testing.T) {
	hosts := usableHosts()
	fresh := snapshotWith(planHost, "gw-1", planUUID, false, true)

	cases := []struct {
		name  string
		input PlanInput
	}{
		{
			name:  "a request with two forms",
			input: PlanInput{Request: wire.EnterRequest{Host: planHost, SessionID: "gw-1", LocalID: planUUID}, Snapshot: fresh, Hosts: hosts},
		},
		{
			name:  "an empty request",
			input: PlanInput{Request: wire.EnterRequest{}, Snapshot: fresh, Hosts: hosts},
		},
		{
			name:  "a local id that is not a UUID",
			input: PlanInput{Request: wire.EnterRequest{LocalID: "not-a-uuid"}, Local: []protocol.ReportSession{{LocalID: "not-a-uuid"}}, Hosts: hosts},
		},
		{
			name:  "a local id that is not running",
			input: PlanInput{Request: wire.EnterRequest{LocalID: planUUID}, Local: nil, Hosts: hosts},
		},
		{
			name:  "no snapshot",
			input: PlanInput{Request: wire.EnterRequest{Host: planHost, SessionID: "gw-1"}, Hosts: hosts},
		},
		{
			name:  "a session not in the snapshot",
			input: PlanInput{Request: wire.EnterRequest{Host: planHost, SessionID: "gw-9"}, Snapshot: fresh, Hosts: hosts},
		},
		{
			name:  "a host that has not reported an id",
			input: PlanInput{Request: wire.EnterRequest{Host: planHost, SessionID: "gw-1"}, Snapshot: snapshotWith(planHost, "gw-1", "", false, true), Hosts: hosts},
		},
		{
			name:  "a reported id that is not a UUID",
			input: PlanInput{Request: wire.EnterRequest{Host: planHost, SessionID: "gw-1"}, Snapshot: snapshotWith(planHost, "gw-1", "not-a-uuid", false, true), Hosts: hosts},
		},
		{
			name:  "this host's row but not running here",
			input: PlanInput{Request: wire.EnterRequest{Host: "mac", SessionID: "gw-1"}, LocalHost: "mac", Snapshot: snapshotWith("mac", "gw-1", planUUID, false, true), Local: nil, Hosts: hosts},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlan(tc.input)
			if plan.Kind != wire.EnterRefused {
				t.Fatalf("kind = %q, want refused", plan.Kind)
			}
			if plan.Reason == "" {
				t.Fatal("a refusal carried no reason")
			}
			if len(plan.Argv) != 0 || plan.Manual != "" {
				t.Fatalf("a refusal carried a command: %#v %q", plan.Argv, plan.Manual)
			}
		})
	}
}

// TestBuildPlanManual: a missing map, a file problem, an unlisted host and an unusable host.
func TestBuildPlanManual(t *testing.T) {
	fresh := snapshotWith(planHost, "gw-1", planUUID, false, true)

	cases := []struct {
		name     string
		hosts    HostMap
		hostsErr error
		wantWord string
	}{
		{name: "no hosts.json", hosts: HostMap{}, hostsErr: ErrNoHostMap, wantWord: "no hosts.json"},
		{name: "a file problem", hosts: HostMap{Problem: "hosts.json is not valid JSON"}, wantWord: "not valid JSON"},
		{name: "an unlisted host", hosts: HostMap{Hosts: map[string]HostResult{}}, wantWord: "not listed"},
		{
			name:     "an unusable host",
			hosts:    HostMap{Hosts: map[string]HostResult{planHost: {Reason: "ssh must be set"}}},
			wantWord: "ssh must be set",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlan(PlanInput{
				Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
				LocalHost: "mac",
				Snapshot:  fresh,
				Hosts:     tc.hosts,
				HostsErr:  tc.hostsErr,
			})
			if plan.Kind != wire.EnterManual {
				t.Fatalf("kind = %q, want manual", plan.Kind)
			}
			if !strings.Contains(plan.Reason, tc.wantWord) {
				t.Fatalf("reason = %q, want it to mention %q", plan.Reason, tc.wantWord)
			}
			if !strings.HasPrefix(plan.Manual, "on "+planHost+": rtk attach --expect-session "+planUUID) {
				t.Fatalf("manual = %q", plan.Manual)
			}
			if len(plan.Argv) != 0 {
				t.Fatalf("a manual plan carried an argv: %#v", plan.Argv)
			}
		})
	}
}

// TestBuildPlanNotes: staleness and a not-live host weaken the preview, in that order.
func TestBuildPlanNotes(t *testing.T) {
	stale := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith(planHost, "gw-1", planUUID, true, true),
		Hosts:     usableHosts(),
	})
	if stale.Kind != wire.EnterSSH {
		t.Fatalf("kind = %q", stale.Kind)
	}
	if !strings.Contains(stale.Note, "report is 12 s old; the host will check the ID") {
		t.Fatalf("note = %q", stale.Note)
	}

	down := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith(planHost, "gw-1", planUUID, false, false),
		Hosts:     usableHosts(),
	})
	if !strings.Contains(down.Note, "host is not live; the host will check the ID") {
		t.Fatalf("note = %q", down.Note)
	}

	both := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith(planHost, "gw-1", planUUID, true, false),
		Hosts:     usableHosts(),
	})
	if !strings.HasPrefix(both.Note, "report is 12 s old; the host will check the ID") ||
		!strings.Contains(both.Note, "host is not live; the host will check the ID") {
		t.Fatalf("note = %q, want both sentences with the stale one first", both.Note)
	}
}

// TestBuildPlanCleansGatewayStrings: a control character never reaches a plan.
func TestBuildPlanCleansGatewayStrings(t *testing.T) {
	dirty := snapshotWith(planHost+"\x1b[31m", "gw-1", planUUID, false, true)
	plan := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost + "\x1b[31m", SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  dirty,
		Hosts:     HostMap{Hosts: map[string]HostResult{}},
	})

	if strings.ContainsRune(plan.Host, 0x1b) || strings.ContainsRune(plan.Manual, 0x1b) || strings.ContainsRune(plan.Reason, 0x1b) {
		t.Fatalf("a control character survived: %+v", plan)
	}

	// A host cleaned to nothing reads as "unknown host" rather than an empty cell.
	empty := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: "\x1b", SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith("\x1b", "gw-1", planUUID, false, true),
		Hosts:     HostMap{Hosts: map[string]HostResult{}},
	})
	if empty.Host != "unknown host" {
		t.Fatalf("host = %q, want the fallback", empty.Host)
	}
}

// TestBuildPlanUnsafeDestinationFallsBackToManual: a pattern failure never becomes an argv.
func TestBuildPlanUnsafeDestinationFallsBackToManual(t *testing.T) {
	plan := BuildPlan(PlanInput{
		Request:   wire.EnterRequest{Host: planHost, SessionID: "gw-1"},
		LocalHost: "mac",
		Snapshot:  snapshotWith(planHost, "gw-1", planUUID, false, true),
		Hosts: HostMap{Hosts: map[string]HostResult{
			planHost: {Entry: HostEntry{SSH: "-oProxyCommand=curl evil", RTK: planRemote}},
		}},
	})

	if plan.Kind != wire.EnterManual {
		t.Fatalf("kind = %q, want manual", plan.Kind)
	}
	if len(plan.Argv) != 0 {
		t.Fatalf("argv = %#v, want none", plan.Argv)
	}
	if !strings.Contains(plan.Reason, "ssh must be") {
		t.Fatalf("reason = %q", plan.Reason)
	}
}

// TestClassifySSHExit: the contract's outcomes table.
func TestClassifySSHExit(t *testing.T) {
	cases := []struct {
		code    int
		kind    string
		message string
		refresh bool
	}{
		{code: 0, kind: OutcomeOK, refresh: true},
		{code: 3, kind: OutcomeNotRunning, message: "ended or replaced; not opened. Choose again.", refresh: true},
		{code: 4, kind: OutcomeNoDaemon, message: "no rtk daemon on vmb"},
		{code: 2, kind: OutcomeTooOld, message: "the rtk on vmb is too old for exact-session entry; upgrade it. Not attached."},
		{code: 255, kind: OutcomeSSHFailed, message: "ssh couldn't connect or the connection ended"},
		{code: 7, kind: OutcomeOther, message: "the remote command exited 7."},
	}

	for _, tc := range cases {
		outcome := ClassifySSHExit(tc.code, "vmb")
		if outcome.Kind != tc.kind {
			t.Fatalf("code %d: kind = %q, want %q", tc.code, outcome.Kind, tc.kind)
		}
		if outcome.Message != tc.message {
			t.Fatalf("code %d: message = %q, want %q", tc.code, outcome.Message, tc.message)
		}
		if outcome.RefreshTab != tc.refresh {
			t.Fatalf("code %d: refresh = %t", tc.code, outcome.RefreshTab)
		}
	}

	// A cleaned-to-empty host in a message reads as the fallback.
	if outcome := ClassifySSHExit(4, ""); !strings.Contains(outcome.Message, "that host") {
		t.Fatalf("message = %q", outcome.Message)
	}
}

// TestClassifyStart: a failure to start ssh says so and leaves the tab alone.
func TestClassifyStart(t *testing.T) {
	outcome := ClassifyStart(errors.New("exec: ssh: not found"), "vmb")
	if outcome.Kind != OutcomeCannotStart {
		t.Fatalf("kind = %q", outcome.Kind)
	}
	if outcome.RefreshTab {
		t.Fatal("a failure to start asked for a tab refresh")
	}
	if !strings.Contains(outcome.Message, "vmb") {
		t.Fatalf("message = %q", outcome.Message)
	}
}

// TestLocalPlanReadsTheSameWhicheverRowWasPressed: a session on this machine gives the same plan
// from its local row and from its snapshot row, including the "reattach" note when the window does
// not have it.
func TestLocalPlanReadsTheSameWhicheverRowWasPressed(t *testing.T) {
	local := []protocol.ReportSession{{SessionID: "gw-1", LocalID: planUUID, Name: "claude"}}
	snapshot := snapshotWith("mac", "gw-1", planUUID, false, true)

	for _, inWindow := range []bool{true, false} {
		present := inWindow
		common := PlanInput{
			LocalHost: "mac",
			Local:     local,
			Snapshot:  snapshot,
			InWindow:  func(string) bool { return present },
		}

		fromLocalRow := common
		fromLocalRow.Request = wire.EnterRequest{LocalID: planUUID}

		fromSnapshotRow := common
		fromSnapshotRow.Request = wire.EnterRequest{Host: "mac", SessionID: "gw-1"}

		a, b := BuildPlan(fromLocalRow), BuildPlan(fromSnapshotRow)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("in window %t: local row %+v, snapshot row %+v", inWindow, a, b)
		}
		if !inWindow && a.Note != "not in this window; reattach to see it" {
			t.Fatalf("the reattach note is missing: %+v", a)
		}
		if inWindow && a.Note != "" {
			t.Fatalf("an in-window session carried a note: %+v", a)
		}
	}
}
