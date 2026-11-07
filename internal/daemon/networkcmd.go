package daemon

// `rtk daemon network`: the language-neutral way to see the network answer without a tab.
//
// The output follows the contract's Vocabulary, which is binding: a missing key is a blank cell and
// never a placeholder, "unknown" is printed as the word, and a stale report says how old it is. The
// ages come from the answer's own fields, never from subtracting a host's clock from this one.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No daemon has been asked and no table printed.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// networkFirstWait is how long a one-shot call waits for this daemon's first snapshot, so the
	// answer is useful rather than a null the call itself caused.
	networkFirstWait = 3000
	// networkWatchWait is how long each --watch call waits for a change.
	networkWatchWait = 8000
	// networkCellMax bounds one printed value from another machine.
	networkCellMax = 128
)

// noDaemonText is what this command says when nothing is running. It is its own sentence rather
// than the daemon status text, because the exit code (4) already distinguishes the case.
const noDaemonText = "no rtk daemon is running"

// RunNetwork asks a running daemon for the network answer and prints it.
//
// With watch it repeats, printing only when the daemon's version changes, until the context ends.
// A one-shot call waits briefly for a first snapshot; a watch call waits for a change.
//
// UNTESTED.
func RunNetwork(ctx context.Context, stateDir string, asJSON, watch bool, out io.Writer) error {
	if watch {
		return runNetworkWatch(ctx, stateDir, asJSON, out)
	}

	view, _, err := Network(ctx, stateDir, wire.NetworkRequest{FirstWaitMS: networkFirstWait})
	if err != nil {
		if errors.Is(err, ErrNotRunning) {
			return &ExitError{Code: 4, Err: errors.New(noDaemonText)}
		}
		return err
	}

	return printNetwork(out, view, asJSON, time.Now())
}

// runNetworkWatch repeats the request, printing each new version.
func runNetworkWatch(ctx context.Context, stateDir string, asJSON bool, out io.Writer) error {
	var lastVersion uint64
	first := true

	for {
		request := wire.NetworkRequest{WaitMS: networkWatchWait}
		if first {
			// The first answer waits briefly for a snapshot, as a one-shot does; later ones wait for
			// a change.
			request.FirstWaitMS = networkFirstWait
		} else {
			request.Since = lastVersion
		}

		view, version, err := Network(ctx, stateDir, request)
		if err != nil {
			if ctx.Err() != nil {
				// A signal is how a watch ends.
				return nil
			}
			// The daemon went away: one line, and exit 1.
			return err
		}

		if first || version != lastVersion {
			if err := printNetwork(out, view, asJSON, time.Now()); err != nil {
				return err
			}
			lastVersion = version
			first = false
		}
	}
}

// printNetwork writes one answer, as JSON exactly or as the table.
func printNetwork(out io.Writer, view protocol.NetworkView, asJSON bool, now time.Time) error {
	if asJSON {
		encoded, err := json.Marshal(view)
		if err != nil {
			return fmt.Errorf("daemon: encode the network answer: %w", err)
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}

	_, err := io.WriteString(out, FormatNetworkTable(view, now))
	return err
}

// FormatNetworkTable renders the answer as the table a person reads.
//
// Every cell that the contract calls "not available" is blank; "unknown" is the word; a stale report
// prints its state followed by its age; a host with no report says so and still lists its directory
// sessions.
//
// UNTESTED.
func FormatNetworkTable(view protocol.NetworkView, now time.Time) string {
	var builder strings.Builder

	// The header: what the gateway is doing, and how old the snapshot is.
	header := "gateway: " + blank(view.Gateway.State)
	if view.Gateway.Error != "" {
		header += " (" + cleanNetworkError(view.Gateway.Error) + ")"
	}
	if view.FetchedAt != "" {
		header += "   snapshot fetched " + ageFromString(view.FetchedAt, now)
	} else {
		header += "   no snapshot yet"
	}
	builder.WriteString(header)
	builder.WriteString("\n")

	// This host first: it is usable with no gateway at all.
	builder.WriteString("local")
	if view.Local.Host != "" {
		builder.WriteString("@")
		builder.WriteString(blank(view.Local.Host))
	}
	if view.Local.Daemon.Build != "" {
		builder.WriteString("  build ")
		builder.WriteString(blank(view.Local.Daemon.Build))
	}
	builder.WriteString("\n")
	builder.WriteString("  name  state  input  session\n")
	for _, session := range view.Local.Sessions {
		builder.WriteString("  ")
		builder.WriteString(blank(session.Name))
		builder.WriteString("  ")
		builder.WriteString(blank(session.State))
		builder.WriteString("  ")
		builder.WriteString(blank(session.Input))
		builder.WriteString("  ")
		builder.WriteString(blank(session.SessionID))
		builder.WriteString("\n")
	}

	if view.Snapshot == nil {
		builder.WriteString("no gateway snapshot yet\n")
		return builder.String()
	}

	builder.WriteString("\nhosts\n")
	for _, host := range view.Snapshot.Hosts {
		builder.WriteString("  ")
		builder.WriteString(blank(host.Host))
		builder.WriteString("  ")
		if host.Connection.Live {
			builder.WriteString("live")
		} else {
			builder.WriteString("not live")
		}
		builder.WriteString("  ")
		switch host.ReportStatus {
		case protocol.ReportStatusNone:
			builder.WriteString("no report")
		case protocol.ReportStatusStale:
			builder.WriteString("report stale")
			if host.Report != nil {
				builder.WriteString(", " + ageFromMillis(host.Report.AgeMS))
			}
		default:
			builder.WriteString("report fresh")
			if host.Report != nil {
				builder.WriteString(", " + ageFromMillis(host.Report.AgeMS))
			}
		}
		builder.WriteString("\n")
	}

	builder.WriteString("\nsessions\n")
	builder.WriteString("  address  state  input  registered  session\n")
	for _, session := range view.Snapshot.Sessions {
		builder.WriteString("  ")
		builder.WriteString(blank(session.Address))
		builder.WriteString("  ")
		builder.WriteString(reportedState(session.Reported))
		builder.WriteString("  ")
		builder.WriteString(reportedInput(session.Reported))
		builder.WriteString("  ")
		if session.Directory.Registered {
			builder.WriteString("yes")
		} else {
			builder.WriteString("no")
		}
		builder.WriteString("  ")
		builder.WriteString(blank(session.SessionID))
		builder.WriteString("\n")
	}

	return builder.String()
}

// reportedState renders one reported state, with its age when the report is stale.
func reportedState(reported *protocol.ReportedSession) string {
	if reported == nil {
		return ""
	}
	state := blank(reported.State)
	if reported.Stale {
		return state + " (stale, " + ageFromMillis(reported.AgeMS) + ")"
	}
	return state
}

// reportedInput renders one reported input value, or a blank cell.
func reportedInput(reported *protocol.ReportedSession) string {
	if reported == nil {
		return ""
	}
	return blank(reported.Input)
}

// blank is the contract's "not available": a missing value is an empty cell, never a placeholder.
//
// It is also where every string from another machine is cleaned for the terminal: the table prints
// whatever the paired gateway sent, including an older build that never stripped anything, so
// control characters (an escape sequence among them) are removed and the length is bounded.
func blank(value string) string { return cleanCell(value) }

// cleanCell removes control characters and line separators and bounds the length.
func cleanCell(value string) string {
	cleaned := make([]rune, 0, len(value))
	for _, r := range value {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			continue
		}
		cleaned = append(cleaned, r)
		if len(cleaned) == networkCellMax {
			break
		}
	}
	return string(cleaned)
}

// ageFromMillis renders an age in whole seconds, as the table shows it.
func ageFromMillis(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%ds", ms/1000)
}

// ageFromString renders how old a wire time is, from the view's own fields only.
func ageFromString(value string, now time.Time) string {
	parsed, ok := protocol.ParseTime(value)
	if !ok {
		return ""
	}
	age := now.Sub(parsed)
	if age < 0 {
		age = 0
	}
	return ageFromMillis(age.Milliseconds())
}
