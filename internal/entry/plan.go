package entry

// Building the entry plan: what pressing Enter on one Network row does.
//
// The order of the rules is the security order. A request is exactly one form; a local row is a
// local plan; a remote row must be in the snapshot, must have a reported local id, and that id must
// be a canonical UUID; only then does the owner's host map decide between an exact ssh command and a
// manual line. Nothing from the gateway reaches an argv except that validated id.

import (
	"fmt"
	"strings"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// PlanInput is everything the plan is built from.
type PlanInput struct {
	Request wire.EnterRequest
	// LocalHost is this machine's name, so a row for this host becomes a local plan.
	LocalHost string
	// Local is this daemon's own sessions, with the local id and the gateway session id.
	Local []protocol.ReportSession
	// InWindow reports whether this window already shows a session. A window's sessions are fixed
	// when it attaches, so a session started afterwards needs a reattach.
	InWindow func(localID string) bool
	// Snapshot is the gateway's snapshot, or nil when the daemon has none.
	Snapshot *protocol.NetworkSnapshot
	// Hosts is the owner's host map, and HostsErr is why it could not be read (ErrNoHostMap when
	// there is no file).
	Hosts    HostMap
	HostsErr error
}

// BuildPlan decides what one row means.
//
// UNTESTED. See the package comment.
func BuildPlan(in PlanInput) wire.EnterPlan {
	if refusal := checkRequestForm(in.Request); refusal != "" {
		return refusedPlan(refusal)
	}

	if in.Request.LocalID != "" {
		return buildLocalPlan(in)
	}
	return buildRemotePlan(in)
}

// checkRequestForm enforces the contract's "exactly one form".
func checkRequestForm(request wire.EnterRequest) string {
	remote := request.Host != "" && request.SessionID != "" && request.LocalID == ""
	local := request.LocalID != "" && request.Host == "" && request.SessionID == ""
	if remote || local {
		return ""
	}
	return "that row cannot be entered"
}

// refusedPlan is the one refusal shape.
func refusedPlan(reason string) wire.EnterPlan {
	return wire.EnterPlan{Kind: wire.EnterRefused, Reason: cleanText(reason, maxPath)}
}

// buildLocalPlan handles a row from the local section: the id is the daemon's own.
func buildLocalPlan(in PlanInput) wire.EnterPlan {
	localID := in.Request.LocalID
	if !IsUUID(localID) {
		return refusedPlan("that session's id is not one this build can enter")
	}

	found := false
	for _, session := range in.Local {
		if session.LocalID == localID {
			found = true
			break
		}
	}
	if !found {
		return refusedPlan("that session is not running here")
	}

	return localPlan(in, localID)
}

// localPlan is the one builder for a row on this machine, whichever row was pressed (a local row or
// a snapshot row for this host), so the same session always reads the same: the in-window flag and
// the "reattach" note come from here and nowhere else.
func localPlan(in PlanInput, localID string) wire.EnterPlan {
	inWindow := false
	if in.InWindow != nil {
		inWindow = in.InWindow(localID)
	}

	plan := wire.EnterPlan{
		Kind:     wire.EnterLocal,
		Host:     cleanOrDefault(in.LocalHost, "unknown host"),
		LocalID:  localID,
		InWindow: inWindow,
	}
	if !inWindow {
		plan.Note = "not in this window; reattach to see it"
	}
	return plan
}

// buildRemotePlan handles a row from the gateway snapshot.
func buildRemotePlan(in PlanInput) wire.EnterPlan {
	if in.Snapshot == nil {
		return refusedPlan("no network snapshot yet")
	}

	session, ok := findSnapshotSession(in.Snapshot, in.Request.Host, in.Request.SessionID)
	if !ok {
		return refusedPlan("that session is not in the current snapshot")
	}

	if in.Request.Host == in.LocalHost && in.LocalHost != "" {
		// The row is on this machine after all: a local plan, found by the gateway's session id.
		for _, local := range in.Local {
			if local.SessionID == in.Request.SessionID {
				return localPlan(in, local.LocalID)
			}
		}
		return refusedPlan("that session is not running here")
	}

	if session.Reported == nil || session.Reported.LocalID == "" {
		return refusedPlan("that host has not reported an ID for this session")
	}
	localID := session.Reported.LocalID
	if !IsUUID(localID) {
		// A non-UUID id never reaches a command.
		return refusedPlan("that host reported an id this build cannot enter")
	}

	host := cleanText(in.Request.Host, 0x80)
	if host == "" {
		host = "unknown host"
	}

	// The owner's map decides: a usable entry is an ssh plan, and everything else is a manual line.
	entry, reason := lookupHost(in, in.Request.Host)
	if reason != "" {
		return manualPlan(host, localID, "", reason, in.Snapshot, session)
	}

	if problem := validateHost(entry); problem != "" {
		return manualPlan(host, localID, "", problem, in.Snapshot, session)
	}

	remote := remoteCommand(entry.RTK, localID)
	if entry.StateHome != "" {
		remote = "env XDG_STATE_HOME=" + entry.StateHome + " " + remote
	}

	argv := []string{"ssh", "-t", "-o", "ConnectTimeout=5", "--", entry.SSH, remote}
	return wire.EnterPlan{
		Kind:    wire.EnterSSH,
		Host:    host,
		LocalID: localID,
		Argv:    argv,
		Display: displayLine(argv),
		Manual:  manualLine(host, entry.RTK, localID),
		Note:    entryNote(in.Snapshot, session),
	}
}

// lookupHost finds the host's entry, or the reason it cannot be used.
func lookupHost(in PlanInput, host string) (HostEntry, string) {
	switch {
	case in.HostsErr != nil:
		if in.HostsErr == ErrNoHostMap {
			return HostEntry{}, "there is no hosts.json on this machine"
		}
		return HostEntry{}, cleanText(in.HostsErr.Error(), maxPath)
	case in.Hosts.Problem != "":
		return HostEntry{}, cleanText(in.Hosts.Problem, maxPath)
	}

	result, ok := in.Hosts.Hosts[host]
	if !ok {
		return HostEntry{}, "that host is not listed in hosts.json"
	}
	if result.Reason != "" {
		return HostEntry{}, cleanText(result.Reason, maxPath)
	}
	return result.Entry, ""
}

// manualPlan is the by-hand plan: a command to run on that host, and why there is nothing to run here.
func manualPlan(host, localID, rtk, reason string, snapshot *protocol.NetworkSnapshot, session protocol.NetworkSession) wire.EnterPlan {
	return wire.EnterPlan{
		Kind:    wire.EnterManual,
		Host:    host,
		LocalID: localID,
		Manual:  manualLine(host, rtk, localID),
		Reason:  reason,
		Note:    entryNote(snapshot, session),
	}
}

// remoteCommand is the one remote argument: the rtk binary and the exact-session attach.
func remoteCommand(rtk, localID string) string {
	if rtk == "" {
		rtk = "rtk"
	}
	return rtk + " attach --expect-session " + localID + " --entered-via-ssh"
}

// manualLine is the command a person types on the remote machine themselves.
func manualLine(host, rtk, localID string) string {
	return "on " + host + ": " + remoteCommand(rtk, localID)
}

// displayLine renders the argv for reading only: the destination as it is, the remote argument
// single-quoted. It is never run.
func displayLine(argv []string) string {
	parts := make([]string, 0, len(argv))
	for i, part := range argv {
		if i == len(argv)-1 {
			// The remote command is one argument and contains spaces.
			parts = append(parts, "'"+strings.ReplaceAll(part, "'", `'\''`)+"'")
			continue
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, " ")
}

// entryNote says what weakens the preview, if anything.
//
// The report's own staleness comes first, because it is about the fact that the id was reported at
// all; a host that is not live follows. Both weaken only the preview: the remote check decides.
func entryNote(snapshot *protocol.NetworkSnapshot, session protocol.NetworkSession) string {
	notes := make([]string, 0, 2)
	if session.Reported != nil && session.Reported.Stale {
		age := session.Reported.AgeMS
		if age < 0 {
			age = 0
		}
		notes = append(notes, fmt.Sprintf("report is %d s old; the host will check the ID", age/1000))
	}
	if snapshot != nil && !hostLive(snapshot, session.Host) {
		notes = append(notes, "host is not live; the host will check the ID")
	}
	return strings.Join(notes, " ")
}

// hostLive reports what the gateway said about one host's liveness.
func hostLive(snapshot *protocol.NetworkSnapshot, host string) bool {
	for _, candidate := range snapshot.Hosts {
		if candidate.Host == host {
			return candidate.Connection.Live
		}
	}
	return false
}

// findSnapshotSession finds one row by host and gateway session id, exactly.
func findSnapshotSession(snapshot *protocol.NetworkSnapshot, host, sessionID string) (protocol.NetworkSession, bool) {
	for _, session := range snapshot.Sessions {
		if session.Host == host && session.SessionID == sessionID {
			return session, true
		}
	}
	return protocol.NetworkSession{}, false
}

// cleanOrDefault cleans a display string and falls back when nothing usable is left.
func cleanOrDefault(value, fallback string) string {
	cleaned := cleanText(value, 0x80)
	if cleaned == "" {
		return fallback
	}
	return cleaned
}
