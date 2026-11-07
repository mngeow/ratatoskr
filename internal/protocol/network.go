package protocol

// The network API's wire shape: what a host daemon reports about itself, and what the gateway
// aggregates into a snapshot.
//
// The rules that keep "unknown" from becoming a value are in the contract
// (docs/implementation/network-api-contract.md) and are implemented here as far as they are type
// level: every optional string is omitempty, so a missing key stays missing, and no field ever
// carries a default state.
//
// Every time on this wire is a string, RFC 3339 in UTC, to whole seconds: the readers are other
// languages and other clocks, and a Go time value is neither.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import "time"

const (
	// NetworkSchema is the schema of the report and snapshot bodies. It is bumped only for a
	// change an older reader cannot ignore; a reader that sees a larger schema treats the data as
	// unavailable.
	NetworkSchema = 1

	// ReportPath is where a host daemon reports about itself.
	ReportPath = "/v1/report"
	// NetworkPath is the aggregated snapshot, with an optional long poll.
	NetworkPath = "/v1/network"

	// MaxReportBytes bounds one report body.
	MaxReportBytes = 16 << 10
	// MaxReportSessions bounds the sessions in one report.
	MaxReportSessions = 16
	// MaxReportString bounds one string in a report, in runes.
	MaxReportString = 128
	// MaxNetworkWait bounds a long poll's wait.
	MaxNetworkWait = 20 * time.Second
)

// Session state values. "unknown" is an explicit answer, never idle.
const (
	SessionStateStarting = "starting"
	SessionStateIdle     = "idle"
	SessionStateBusy     = "busy"
	SessionStateNeedsYou = "needs_you"
	SessionStateUnknown  = "unknown"
	SessionStateExited   = "exited"
)

// Input values: who holds the session's input, never which window.
const (
	InputHeld    = "held"
	InputFree    = "free"
	InputUnknown = "unknown"
)

// Report status values, in the gateway's snapshot.
const (
	ReportStatusNone  = "none"
	ReportStatusFresh = "fresh"
	ReportStatusStale = "stale"
)

// networkTimeLayout is RFC 3339 in UTC with whole seconds.
const networkTimeLayout = "2006-01-02T15:04:05Z"

// FormatTime renders one time for the wire, or "" for the zero time, which omits the field.
//
// UNTESTED.
func FormatTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(networkTimeLayout)
}

// ParseTime reads one wire time. A false result means the value is not a usable time, and the
// caller must treat the fact as unavailable rather than guessing.
//
// UNTESTED.
func ParseTime(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	// Any RFC 3339 form is read (an offset or fractional seconds included) and normalised to UTC
	// whole seconds, so a reporter that formats differently loses nothing; FormatTime is the only
	// form this package writes.
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC().Truncate(time.Second), true
}

// ReportRequest is one host's report about itself. It replaces that host's previous report.
type ReportRequest struct {
	Schema     int             `json:"schema"`
	Seq        uint64          `json:"seq"`
	ReportedAt string          `json:"reported_at,omitempty"`
	Daemon     ReportDaemon    `json:"daemon"`
	Sessions   []ReportSession `json:"sessions"`
}

// ReportDaemon identifies the daemon that reported.
type ReportDaemon struct {
	Build     string `json:"build,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
}

// ReportSession is one session the reporting daemon runs.
//
// SessionID is the gateway's ID for the registration, which is what joins this to the directory;
// LocalID is the daemon's own ID, reserved for a later exact-session attach.
type ReportSession struct {
	SessionID      string `json:"session_id,omitempty"`
	LocalID        string `json:"local_id"`
	Name           string `json:"name"`
	Harness        string `json:"harness"`
	HarnessVersion string `json:"harness_version,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	State          string `json:"state"`
	StateAt        string `json:"state_at,omitempty"`
	Input          string `json:"input"`
}

// NetworkSnapshot is the gateway's answer to GET NetworkPath.
type NetworkSnapshot struct {
	Schema      int              `json:"schema"`
	Revision    uint64           `json:"revision"`
	GeneratedAt string           `json:"generated_at,omitempty"`
	Gateway     NetworkGateway   `json:"gateway"`
	Hosts       []NetworkHost    `json:"hosts"`
	Sessions    []NetworkSession `json:"sessions"`
}

// NetworkGateway describes the gateway itself, so a reader knows which windows the numbers mean.
type NetworkGateway struct {
	Protocol           int    `json:"protocol"`
	StartedAt          string `json:"started_at,omitempty"`
	LiveWindowMS       int64  `json:"live_window_ms"`
	ReportStaleAfterMS int64  `json:"report_stale_after_ms"`
}

// NetworkHost is one paired host.
type NetworkHost struct {
	Host         string            `json:"host"`
	Connection   NetworkConnection `json:"connection"`
	ReportStatus string            `json:"report_status"`
	Report       *HostReport       `json:"report"`
}

// NetworkConnection is a host's connection facts, all from the gateway's own clock.
type NetworkConnection struct {
	Live            bool   `json:"live"`
	StreamOpen      bool   `json:"stream_open"`
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty"`
	HeartbeatAgeMS  *int64 `json:"heartbeat_age_ms,omitempty"`
}

// HostReport is what the gateway keeps about a host's last report.
type HostReport struct {
	ReceivedAt string       `json:"received_at,omitempty"`
	AgeMS      int64        `json:"age_ms"`
	Seq        uint64       `json:"seq"`
	Daemon     ReportDaemon `json:"daemon"`
}

// NetworkSession is one session, the directory joined with the host's report.
type NetworkSession struct {
	Address   string           `json:"address"`
	Host      string           `json:"host"`
	Name      string           `json:"name"`
	SessionID string           `json:"session_id"`
	Directory DirectoryFacts   `json:"directory"`
	Reported  *ReportedSession `json:"reported"`
}

// DirectoryFacts is what the gateway's own directory knows about a session.
type DirectoryFacts struct {
	Registered bool  `json:"registered"`
	Live       *bool `json:"live,omitempty"`
}

// ReportedSession is what the host's report said about one session, plus how old it is.
type ReportedSession struct {
	LocalID        string `json:"local_id,omitempty"`
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harness_version,omitempty"`
	StartedAt      string `json:"started_at,omitempty"`
	State          string `json:"state,omitempty"`
	StateAt        string `json:"state_at,omitempty"`
	Input          string `json:"input,omitempty"`
	AgeMS          int64  `json:"age_ms"`
	Stale          bool   `json:"stale"`
}

// Gateway states in NetworkView.Gateway.State, as the owner's daemon reports its own view of the
// gateway.
const (
	GatewayConnected     = "connected"      // the link is up and a snapshot is current as of fetched_at
	GatewayUnreachable   = "unreachable"    // the gateway cannot be reached; Error says why, cleaned
	GatewayNotConfigured = "not_configured" // this host has no pairing, so there is no gateway to ask
	GatewayUnsupported   = "unsupported"    // the gateway answered 404 for the network routes: an older gateway
)

// NetworkView is what the owner's own daemon answers to `network`: the schema-1 body that
// `rtk daemon network --json` prints exactly. Unknown fields must be ignored by every reader, and a
// reader that sees a larger Schema than it knows treats the data as unavailable.
//
// Local always comes from the daemon itself, so one host's state is usable with no gateway at all.
// Snapshot is present and null until a snapshot has been fetched, and an old snapshot is never
// presented as current: Gateway.State says what is true now, and the ages say how old it is.
type NetworkView struct {
	Schema    int                `json:"schema"`
	FetchedAt string             `json:"fetched_at,omitempty"` // when Snapshot was fetched; empty when there is none
	Local     NetworkLocal       `json:"local"`
	Gateway   NetworkGatewayView `json:"gateway"`
	Snapshot  *NetworkSnapshot   `json:"snapshot"`
}

// NetworkLocal is this host's own sessions as its daemon sees them. It is the same shape a report
// carries, so what this host says about itself is what the gateway receives. An unknown fact is an
// omitted key: Host is empty with no pairing, and SessionID is empty for a session with no gateway
// registration.
type NetworkLocal struct {
	Host     string          `json:"host,omitempty"`
	Daemon   ReportDaemon    `json:"daemon"`
	Sessions []ReportSession `json:"sessions"`
}

// NetworkGatewayView is the daemon's current relationship with the gateway.
type NetworkGatewayView struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	AsOf  string `json:"as_of,omitempty"` // when State was last established
}
