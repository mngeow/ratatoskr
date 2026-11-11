package dashboard

// The view model: what the templates and the graph JSON receive. These are presentation types built from
// the two snapshots the gateway already builds (the network snapshot and the graph snapshot); there is no
// second store or query source. The names and fields were agreed with engineering-frontend.
//
// The four facts of ADR-0009 section 9 stay separate and are never merged into one status: connection
// (live, source, heartbeat age), card freshness (state, age, source), the card declaration (the card
// itself) and the sessions with their directory facts. A value the gateway does not know stays empty or
// absent here; it is never turned into false or a default claim.
//
// Template names and their data: "overview" gets *OverviewPage, "network" gets *NetworkPage, "status" gets
// *StatusPartial, "card" gets *CardPartial. The graph endpoint returns a GraphDocument as JSON.
//
// UNTESTED. Under GR-3 nothing here has been run.

import "github.com/mngeow/ratatoskr/internal/protocol"

// Connection labels of HostRow.ConnectionLabel. A host never heard from is "never seen".
const (
	ConnectionLive      = "live"
	ConnectionNotLive   = "not live"
	ConnectionNeverSeen = "never seen"
)

// PageChrome is what every full page shows around its content.
type PageChrome struct {
	Title          string
	CurrentPage    string // "overview" or "network"
	DashboardURL   string
	NetworkURL     string
	GeneratedAt    string
	OpenAccessText string
}

// OverviewPage is the data of the "overview" template.
type OverviewPage struct {
	Chrome PageChrome

	HostCount, LiveHostCount                                                         int
	FreshCardCount, StaleCardCount, OfflineCardCount, NoCardCount, RejectedCardCount int
	SessionCount, ConnectedSessionCount                                              int
	Attention                                                                        []HostRow
}

// NetworkPage is the data of the "network" template.
type NetworkPage struct {
	Chrome PageChrome

	GraphURL, StatusURL, CardURL string // CardURL is the prefix; a row's own CardURL opens one host
	Graph                        GraphDocument
	HostRows                     []HostRow
}

// StatusPartial is the data of the "status" template (the strip htmx refreshes).
type StatusPartial struct {
	GeneratedAt  string
	GatewayState string // "up 3 d 4 h", or "up" when the start time is not known

	HostCount, LiveHostCount                                                           int
	CardFreshCount, CardStaleCount, CardOfflineCount, CardNoneCount, CardRejectedCount int
}

// HostRow is one host in the accessible table and in the lists.
type HostRow struct {
	Name            string
	ConnectionLive  bool
	ConnectionLabel string // ConnectionLive, ConnectionNotLive or ConnectionNeverSeen
	CardState       string // fresh, stale, offline, none or rejected (the gateway's words)
	CardAge         string // "4 s", "4 m": empty when there was never a good poll
	CardSource      string // where the card state comes from
	SessionCount    int
	CardURL         string
}

// GraphDocument is the JSON the Cytoscape.js graph reads. It is independent of the HTML templates.
type GraphDocument struct {
	GeneratedAt  string         `json:"generated_at"`
	Truncated    bool           `json:"truncated,omitempty"`
	EdgesPartial bool           `json:"edges_partial,omitempty"`
	Elements     []GraphElement `json:"elements"`
}

// GraphElement is one node or edge, in Cytoscape's element shape. Classes carries the style words:
// nodes "gateway", or "host live|notlive|neverseen card-<state>"; edges "spoke live|notlive|neverseen" or
// "seen" (the optional messages_seen overlay: sends, not deliveries).
type GraphElement struct {
	Data    GraphElementData `json:"data"`
	Classes string           `json:"classes"`
}

// GraphElementData is the data of one element. Source and Target are set on edges only.
type GraphElementData struct {
	ID             string `json:"id"`
	Label          string `json:"label,omitempty"`
	Source         string `json:"source,omitempty"`
	Target         string `json:"target,omitempty"`
	ConnectionLive bool   `json:"connectionLive"`
	CardState      string `json:"cardState,omitempty"`
	CardAge        string `json:"cardAge,omitempty"`
	// Host nodes only, so the accessible table can refresh from the same document as the graph: the
	// connection word, where the card state comes from, and the session count (a pointer, so a real 0 is
	// kept and other elements omit it).
	ConnectionLabel string `json:"connectionLabel,omitempty"`
	CardSource      string `json:"cardSource,omitempty"`
	SessionCount    *int   `json:"sessionCount,omitempty"`
	Count           int    `json:"count,omitempty"`      // messages_seen edges: sends in the window
	HostStored      int    `json:"hostStored,omitempty"` // messages_seen edges: the subset a host saved
}

// CardPartial is the data of the "card" template: one host's Agent Card with the facts around it.
type CardPartial struct {
	HostName string

	ConnectionLive   bool
	ConnectionSource string
	HeartbeatAge     string // empty when no heartbeat was ever seen

	CardState     string
	CardAge       string
	CardSource    string
	CardRevision  string
	CardVersion   string
	CardFetchedAt string
	CardOmitted   bool // the body was left out to fit the graph size bound; the state is still true
	Card          *protocol.HostCard

	Sessions []SessionRow
	Error    string // set only for a host that is not paired (the handler answers 404)
}

// SessionRow is one session of a host, with the exact id.
type SessionRow struct {
	Address            string
	Name               string
	Host               string
	SessionID          string
	State              string // the host's reported state; empty when none was reported
	Input              string
	DirectoryLiveKnown bool // false: the gateway's directory has no live fact for this session
	DirectoryLive      bool
}
