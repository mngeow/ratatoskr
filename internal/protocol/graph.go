package protocol

// The graph API's wire shape: what the gateway serves at GET /v1/graph, and what the owner's own
// daemon answers to its window about it.
//
// Authority: docs/implementation/host-cards-contract.md section 5 (ADR-0005).
//
// Two separate signals ride in every host, with their own source label and age, and nothing here
// merges them: Connection is the heartbeat and stream (the authority on whether a host is
// connected); Card is whether the card endpoint answered a poll. A poll result says the endpoint
// answered; it says nothing about sessions.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

const (
	// GraphSchema is the schema of the snapshot and view bodies. A reader that sees a larger one
	// treats the data as unavailable.
	GraphSchema = 1

	// GraphPath is the graph snapshot with an optional long poll, like NetworkPath. A gateway that
	// predates it answers 404, which the daemon reports as GatewayTooOld.
	GraphPath = "/v1/graph"

	// MaxGraphWait bounds a long poll's wait.
	MaxGraphWait = MaxNetworkWait

	// MaxGraphBytes bounds one graph response. It sits under the link client's 1 MiB answer cap.
	// Over it, card bodies are omitted (largest first, ties by host name) and Truncated is set.
	MaxGraphBytes = 768 << 10 // UNTESTED guess; proposed by backend, approved

	// GraphEdgeWindowHours is the rolling window of an edge count.
	GraphEdgeWindowHours = 24

	// Source labels. They say where a fact came from.
	GraphSourceStream = "gateway-stream"
	GraphSourcePoll   = "gateway-poll"
	GraphSourceLedger = "gateway-ledger"

	// GraphEdgeMessagesSeen is the only edge kind in v1.
	GraphEdgeMessagesSeen = "messages_seen"
)

// GatewayTooOld is the daemon's own gateway state for a gateway that answers 404 on GraphPath. It
// sits beside the Gateway* states of the network view.
const GatewayTooOld = "gateway_too_old"

// GraphSnapshot is the gateway's graph body.
//
// Truncated says card bodies were left out to fit MaxGraphBytes; each affected host also sets
// card_omitted. EdgesPartial says the ledger walk hit its row cap, so edge counts may be low.
type GraphSnapshot struct {
	Schema       int         `json:"schema"`
	Revision     uint64      `json:"revision"`
	GeneratedAt  string      `json:"generated_at"`
	Truncated    bool        `json:"truncated,omitempty"`
	EdgesPartial bool        `json:"edges_partial,omitempty"`
	Hosts        []GraphHost `json:"hosts"`
	Edges        []GraphEdge `json:"edges"`
}

// GraphHost is one paired host.
type GraphHost struct {
	Name        string          `json:"name"`
	Connection  GraphConnection `json:"connection"`
	Card        GraphCard       `json:"card"`
	ReportAgeMS *int64          `json:"report_age_ms,omitempty"`
}

// GraphConnection is the heartbeat and stream signal, the authority on connection.
type GraphConnection struct {
	Live           bool   `json:"live"`
	HeartbeatAgeMS *int64 `json:"heartbeat_age_ms,omitempty"`
	Source         string `json:"source"`
}

// GraphCard is the card-poll signal for one host.
//
// State is one of the Card* freshness states. Restored is true from gateway start until the first
// poll since boot: the row is a record of what the gateway last saw, never proof of anything live.
// Reason is a short fixed-vocabulary sentence for a failed or rejected poll; it never repeats card
// text. UnknownFields counts fields of a polled card that this build does not know and ignored (a
// host on a newer build), which are not stored or served. CardOmitted says the body was left out
// to fit the size limit; Card is then null.
type GraphCard struct {
	State         string    `json:"state"`
	Restored      bool      `json:"restored"`
	Revision      string    `json:"revision,omitempty"`
	ETag          string    `json:"etag,omitempty"`
	LastOKAt      string    `json:"last_ok_at,omitempty"`
	LastAttemptAt string    `json:"last_attempt_at,omitempty"`
	LastResult    string    `json:"last_result,omitempty"`
	AgeMS         *int64    `json:"age_ms,omitempty"`
	Source        string    `json:"source"`
	Reason        string    `json:"reason,omitempty"`
	UnknownFields int       `json:"unknown_fields,omitempty"`
	CardOmitted   bool      `json:"card_omitted,omitempty"`
	Card          *HostCard `json:"card"`
}

// GraphEdge counts the sends the gateway decided between two currently paired hosts over the
// window. Count is every row whose result is not "stored" (the gateway decided it), so it includes
// failed sends and never means delivered. HostStored is the subset a host saved. No body is read.
type GraphEdge struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Kind        string `json:"kind"`
	WindowHours int    `json:"window_hours"`
	Count       int    `json:"count"`
	HostStored  int    `json:"host_stored"`
	LastAt      string `json:"last_at,omitempty"`
	Source      string `json:"source"`
}

// GraphView is what the owner's own daemon answers to `graph`. Snapshot is present and null until
// one has been fetched; an old snapshot is never presented as current. Gateway.State says what is
// true now and FetchedAt says how old the kept snapshot is.
type GraphView struct {
	Schema    int                `json:"schema"`
	FetchedAt string             `json:"fetched_at,omitempty"`
	Local     GraphLocal         `json:"local"`
	Gateway   NetworkGatewayView `json:"gateway"`
	Snapshot  *GraphSnapshot     `json:"snapshot"`
}

// GraphLocal is this host's own identity, available with no gateway at all.
type GraphLocal struct {
	Host string `json:"host,omitempty"`
}
