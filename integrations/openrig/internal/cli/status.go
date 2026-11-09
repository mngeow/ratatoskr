package cli

// status: ask the sidecar (a live ready check included). With the sidecar down it prints the configuration
// summary and the files it can read, makes no network call, and exits 6.

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/ipc"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/sidecar"
)

const unknownSenderNote = `OpenRig shows "From: <unknown sender>"; any local process could imitate this bridge's header.`

func runStatus(args []string, io IO) int {
	fs := newFlags("status")
	configPath := fs.String("config", "", "")
	asJSON := fs.Bool("json", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fail(io, ExitUsage, "usage: status [--json] [--config PATH]")
	}
	cfg, code := loadConfig(*configPath, true, io)
	if cfg == nil {
		return code
	}
	resp, callCode := call(*configPath, io, ipc.Request{Op: ipc.OpStatus})
	if callCode == ExitBridgeDown || callCode == ExitUncertain || (callCode == -1 && resp.Outcome != ipc.OutcomeOK) {
		return statusDown(cfg, io, *asJSON)
	}
	if callCode != -1 {
		return callCode
	}
	var st sidecar.StatusReport
	if json.Unmarshal(resp.Status, &st) != nil {
		return fail(io, ExitFailed, "the bridge answered a status this program cannot read")
	}
	if *asJSON {
		out, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintln(io.Stdout, string(out))
	} else {
		printStatus(io, cfg.ParticipantURL, pinSummary(cfg), st)
	}
	switch {
	case strings.Contains(st.Gateway, "revoked"):
		return ExitRevoked
	case st.Gateway == "reachable" && (st.Poll == "parked" || st.Poll == "between polls") && st.SeatReady:
		return ExitOK
	}
	return ExitFailed
}

func printStatus(io IO, url, pin string, st sidecar.StatusReport) {
	w := io.Stdout
	seat := fmt.Sprintf("%s  ready", st.OperatorSeat)
	if !st.SeatReady {
		seat = fmt.Sprintf("%s  not ready (%s)", st.OperatorSeat, st.SeatWord)
	}
	registered := "registered"
	if !st.Registered {
		registered = "not registered"
	}
	fmt.Fprintf(w, "bridge:          running (pid %d, version %s, up %s)\n", st.PID, st.Version, time.Since(st.StartedAt).Round(time.Second))
	fmt.Fprintf(w, "gateway:         %s\n", terminalText(st.Gateway))
	fmt.Fprintf(w, "participant API: %s  contract 1.0  pin %s\n", terminalText(url), pin)
	fmt.Fprintf(w, "session:         %s  %s\n", terminalText(st.Session), registered)
	fmt.Fprintf(w, "poll:            %s\n", terminalText(st.Poll))
	fmt.Fprintf(w, "operator seat:   %s\n", terminalText(seat))
	fmt.Fprintf(w, "rig:             %s\n", terminalText(st.RigPath))
	if st.Stopped != "" {
		fmt.Fprintf(w, "stopped:         %s\n", terminalText(st.Stopped))
	}
	fmt.Fprintf(w, "since start:     received %d, refused %d, pasted %d, failed %d, unconfirmed %d\n",
		st.Received, st.Refused, st.Pasted, st.Failed, st.Unconfirmed)
	if st.LastInboundID != "" {
		fmt.Fprintf(w, "last inbound:    %s  %s  %s\n", st.LastInboundAt.Format(time.RFC3339), terminalText(st.LastInboundID), terminalText(st.LastInbound))
	} else {
		fmt.Fprintf(w, "last inbound:    none\n")
	}
	fmt.Fprintf(w, "limits:          inline %d B, %d/min/sender, %d/min total (untested guesses)\n", st.InlineBytes, st.PerSender, st.Total)
	fmt.Fprintf(w, "note:            %s\n", unknownSenderNote)
}

func statusDown(cfg interface {
	SocketPath() string
	StoppedPath() string
}, io IO, asJSON bool) int {
	stopped := ""
	if st, ok := sidecar.ReadStopped(cfg.StoppedPath()); ok {
		stopped = st.Reason + " at " + st.At
	}
	if asJSON {
		out, _ := json.MarshalIndent(map[string]any{"bridge": "not running", "stopped": stopped}, "", "  ")
		fmt.Fprintln(io.Stdout, string(out))
		return ExitBridgeDown
	}
	fmt.Fprintln(io.Stdout, "bridge:          not running")
	if stopped != "" {
		fmt.Fprintf(io.Stdout, "stopped:         %s\n", terminalText(stopped))
	}
	fmt.Fprintf(io.Stdout, "note:            %s\n", unknownSenderNote)
	return ExitBridgeDown
}
