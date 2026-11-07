package gateway

// Host reports: the bounded, in-memory, per-host claim about what a daemon saw.
//
// A report is never stored in the database. A persisted "running" row would outlive the process
// it describes, and the contract is explicit that a report is a claim about the past: the gateway
// adds its own received time and nothing else.
//
// A report is also re-sanitized here, because the gateway trusts a paired host to be honest about
// its own sessions but not to be well-behaved about bounds.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
	"unicode"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// reportStaleAfter is how old a stored report may be before it is history rather than current
// state. It is the contract's report_stale_after_ms.
const reportStaleAfter = 30 * time.Second // UNTESTED guess; contract value

// storedReport is one host's latest report, with the gateway's own facts about it.
type storedReport struct {
	request    protocol.ReportRequest
	receivedAt time.Time
	// epoch is the host's PairedAt when the report was stored. A re-pair rewrites it, which is how
	// an unpair or a re-pair drops the report without an event to hook.
	epoch time.Time
}

// reportStore is every host's latest report, in memory and nowhere else.
type reportStore struct {
	mu      sync.Mutex
	reports map[string]storedReport
}

// newReportStore builds an empty store.
func newReportStore() *reportStore {
	return &reportStore{reports: make(map[string]storedReport, 4)}
}

// put stores one report and reports whether anything changed.
//
// "Changed" means the content differs or the epoch does; a report that says exactly the same thing
// again must not bump the snapshot revision, or every heartbeat would wake every long-poll.
//
// UNTESTED.
func (r *reportStore) put(host string, epoch time.Time, request protocol.ReportRequest, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	previous, existed := r.reports[host]
	changed := !existed || !previous.epoch.Equal(epoch) || !sameReport(previous.request, request)

	r.reports[host] = storedReport{request: request, receivedAt: now, epoch: epoch}
	return changed
}

// get returns one host's report.
func (r *reportStore) get(host string) (storedReport, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	report, ok := r.reports[host]
	return report, ok
}

// prune drops every report the keep function rejects. It is called from the snapshot computation,
// so an unpair or a re-pair is noticed the next time anyone looks, with no sweeper to run.
//
// UNTESTED.
func (r *reportStore) prune(keep func(host string, epoch time.Time) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for host, report := range r.reports {
		if !keep(host, report.epoch) {
			delete(r.reports, host)
		}
	}
}

// sameReport reports whether two sanitized reports say the same thing.
func sameReport(a, b protocol.ReportRequest) bool {
	// Seq and ReportedAt change on every report and say nothing new, so they are not compared:
	// a report that repeats its content must not count as a change (it would start a recompute
	// for nothing; the revision itself is protected by the snapshot signature).
	return a.Schema == b.Schema && a.Daemon == b.Daemon && sameSessions(a.Sessions, b.Sessions)
}

// sameSessions compares two sanitized session lists field by field.
func sameSessions(a, b []protocol.ReportSession) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// HostPairedAt is when the gateway paired this host, which is the epoch a report belongs to.
//
// A false result means there is no such host: the host was unpaired between the authentication
// check and here, which the caller answers as an unauthorized request rather than a server error.
//
// UNTESTED.
func (s *Store) HostPairedAt(ctx context.Context, host string) (time.Time, bool, error) {
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT created_at FROM hosts WHERE name=?`, host).Scan(&created)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("gateway: host paired at: %w", err)
	}
	return time.UnixMilli(created), true, nil
}

// sanitizeReport cleans one report into exactly what the gateway will keep.
//
// What it does, in the contract's words: control characters stripped, every string cut to 128
// runes, unknown state and input values stored as "unknown", and every time parsed and re-emitted
// or dropped. What it refuses: a schema it does not know, more than 16 sessions, or a session with
// no name left after cleaning. Nothing else from the request is copied.
//
// UNTESTED.
func sanitizeReport(request protocol.ReportRequest) (protocol.ReportRequest, error) {
	if request.Schema != protocol.NetworkSchema {
		return protocol.ReportRequest{}, fmt.Errorf("gateway: the report schema is %d; this gateway understands schema %d", request.Schema, protocol.NetworkSchema)
	}
	if len(request.Sessions) > protocol.MaxReportSessions {
		return protocol.ReportRequest{}, fmt.Errorf("gateway: the report has %d sessions; at most %d are accepted", len(request.Sessions), protocol.MaxReportSessions)
	}

	clean := protocol.ReportRequest{
		Schema:     protocol.NetworkSchema,
		Seq:        request.Seq,
		ReportedAt: cleanTime(request.ReportedAt),
		Daemon: protocol.ReportDaemon{
			Build:     cleanString(request.Daemon.Build),
			StartedAt: cleanTime(request.Daemon.StartedAt),
		},
		Sessions: make([]protocol.ReportSession, 0, len(request.Sessions)),
	}

	seenSession := make(map[string]struct{}, len(request.Sessions))
	seenLocal := make(map[string]struct{}, len(request.Sessions))

	for _, session := range request.Sessions {
		cleaned := protocol.ReportSession{
			SessionID:      cleanString(session.SessionID),
			LocalID:        cleanString(session.LocalID),
			Name:           cleanString(session.Name),
			Harness:        cleanString(session.Harness),
			HarnessVersion: cleanString(session.HarnessVersion),
			StartedAt:      cleanTime(session.StartedAt),
			State:          cleanState(session.State),
			StateAt:        cleanTime(session.StateAt),
			Input:          cleanInput(session.Input),
		}
		if cleaned.Name == "" {
			return protocol.ReportRequest{}, errors.New("gateway: every reported session needs a name")
		}

		// The first entry for an ID wins: a later duplicate cannot quietly replace it.
		if cleaned.SessionID != "" {
			if _, duplicate := seenSession[cleaned.SessionID]; duplicate {
				continue
			}
			seenSession[cleaned.SessionID] = struct{}{}
		}
		if cleaned.LocalID != "" {
			if _, duplicate := seenLocal[cleaned.LocalID]; duplicate {
				continue
			}
			seenLocal[cleaned.LocalID] = struct{}{}
		}

		clean.Sessions = append(clean.Sessions, cleaned)
	}

	return clean, nil
}

// cleanString strips control characters and cuts the result to MaxReportString runes.
func cleanString(value string) string {
	cleaned := make([]rune, 0, len(value))
	for _, r := range value {
		if unicode.IsControl(r) {
			continue
		}
		cleaned = append(cleaned, r)
		if len(cleaned) == protocol.MaxReportString {
			break
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return string(cleaned)
}

// cleanTime re-emits one wire time, or "" when it cannot be read. "" is omitted, which is the
// contract's "not available".
func cleanTime(value string) string {
	parsed, ok := protocol.ParseTime(value)
	if !ok {
		return ""
	}
	return protocol.FormatTime(parsed)
}

// cleanState maps a state to the closed set, or to "unknown".
func cleanState(value string) string {
	switch value {
	case protocol.SessionStateStarting, protocol.SessionStateIdle, protocol.SessionStateBusy,
		protocol.SessionStateNeedsYou, protocol.SessionStateUnknown, protocol.SessionStateExited:
		return value
	}
	return protocol.SessionStateUnknown
}

// cleanInput maps an input value to the closed set, or to "unknown".
func cleanInput(value string) string {
	switch value {
	case protocol.InputHeld, protocol.InputFree, protocol.InputUnknown:
		return value
	}
	return protocol.InputUnknown
}

// decodeReportBody reads one report body, splitting too-large from malformed.
//
// The difference matters to a client: a 413 says the report is too big to be accepted, and the
// previous report stays; a 400 says the body could not be read at all. decodeBody answers 400 for
// both, which is why this variant exists rather than a change to it.
//
// UNTESTED.
func decodeReportBody(w http.ResponseWriter, r *http.Request, destination any) bool {
	reader := http.MaxBytesReader(w, r.Body, protocol.MaxReportBytes)
	decoder := json.NewDecoder(reader)

	if err := decoder.Decode(destination); err != nil {
		return reportBodyFailed(w, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		// A body that overran the limit while holding a second value is still too large: the two
		// cannot be told apart, and "too large" is the answer that leaves the previous report.
		return reportBodyFailed(w, err)
	}
	return true
}

// reportBodyFailed writes the one answer a refused body gets.
func reportBodyFailed(w http.ResponseWriter, err error) bool {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeWireError(w, http.StatusRequestEntityTooLarge, protocol.WireErrBadRequest,
			"the report is too large; the previous report was kept")
		return false
	}
	writeWireError(w, http.StatusBadRequest, protocol.WireErrBadRequest,
		"the request body is not valid JSON for this route")
	return false
}
