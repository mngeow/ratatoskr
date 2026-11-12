package dashboard

// Builders: the two gateway snapshots in, view-model values out. Nothing here reads a store or a socket.
// Every function is a pure function of its arguments, so it can be tested without a gateway.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	dashboardPath = "/ui/"
	networkPath   = "/ui/network"
	graphPath     = "/ui/network/graph.json"
	statusPath    = "/ui/network/status"
	cardPrefix    = "/ui/hosts/"

	// OpenAccessHTTP and OpenAccessHTTPS are the page-chrome strip (ADR-0009 D7, D11, D13): open access is
	// stated on every page, and the transport is stated as it really is.
	OpenAccessHTTP  = "Open access, no login, plain HTTP. Anyone who can reach this address can see this page, and it travels in cleartext. The URL is not a password. Read-only."
	OpenAccessHTTPS = "Open access, no login. HTTPS with a self-signed certificate encrypts the traffic but does not add a login: anyone who can reach this address can see this page. Read-only."
)

// Info is what the dashboard knows about how it is served.
type Info struct {
	HTTPS bool
}

// Data is one consistent read of the gateway: the network snapshot, the graph snapshot and the time.
type Data struct {
	Network protocol.NetworkSnapshot
	Graph   protocol.GraphSnapshot
	Now     time.Time
}

// hostView joins the two snapshots by host name.
type hostView struct {
	name     string
	graph    protocol.GraphHost
	network  *protocol.NetworkHost
	sessions []protocol.NetworkSession
}

// hosts joins the graph hosts (every paired host, with its card) with the network hosts and sessions. A
// host named in only one snapshot is still listed; sorted by name.
func (d Data) hosts() []hostView {
	byName := map[string]*hostView{}
	order := []string{}
	get := func(name string) *hostView {
		if v, ok := byName[name]; ok {
			return v
		}
		v := &hostView{name: name}
		byName[name] = v
		order = append(order, name)
		return v
	}
	for _, h := range d.Graph.Hosts {
		get(h.Name).graph = h
	}
	for i := range d.Network.Hosts {
		h := d.Network.Hosts[i]
		v := get(h.Host)
		v.network = &h
		if v.graph.Name == "" {
			v.graph.Name = h.Host
			v.graph.Card.State = protocol.CardNone
			v.graph.Connection.Live = h.Connection.Live
			v.graph.Connection.HeartbeatAgeMS = h.Connection.HeartbeatAgeMS
			v.graph.Connection.Source = protocol.GraphSourceStream
		}
	}
	for _, s := range d.Network.Sessions {
		v := get(s.Host)
		v.sessions = append(v.sessions, s)
	}
	sort.Strings(order)
	out := make([]hostView, 0, len(order))
	for _, name := range order {
		out = append(out, *byName[name])
	}
	return out
}

func connectionLabel(v hostView) string {
	switch {
	case v.graph.Connection.Live:
		return ConnectionLive
	case v.graph.Connection.HeartbeatAgeMS == nil && (v.network == nil || v.network.Connection.HeartbeatAgeMS == nil):
		return ConnectionNeverSeen
	}
	return ConnectionNotLive
}

func connectionClass(label string) string {
	switch label {
	case ConnectionLive:
		return "live"
	case ConnectionNeverSeen:
		return "neverseen"
	}
	return "notlive"
}

// FormatAge renders milliseconds as the coarsest whole unit: "4 s", "4 m", "3 h", "2 d". A negative age
// (a clock step) is shown as "0 s".
func FormatAge(ms int64) string {
	if ms < 0 {
		ms = 0
	}
	switch s := ms / 1000; {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d m", s/60)
	case s < 86400:
		return fmt.Sprintf("%d h", s/3600)
	default:
		return fmt.Sprintf("%d d", s/86400)
	}
}

func agePtr(ms *int64) string {
	if ms == nil {
		return ""
	}
	return FormatAge(*ms)
}

// CardURL is the partial's URL for one host name.
func CardURL(host string) string { return cardPrefix + url.PathEscape(host) + "/card" }

func hostRow(v hostView) HostRow {
	label := connectionLabel(v)
	return HostRow{
		Name: v.name, ConnectionLive: v.graph.Connection.Live, ConnectionLabel: label,
		CardState: v.graph.Card.State, CardAge: agePtr(v.graph.Card.AgeMS), CardSource: v.graph.Card.Source,
		SessionCount: len(v.sessions), CardURL: CardURL(v.name),
	}
}

func chrome(title, current string, now time.Time, info Info) PageChrome {
	text := OpenAccessHTTP
	if info.HTTPS {
		text = OpenAccessHTTPS
	}
	return PageChrome{
		Title: title, CurrentPage: current, DashboardURL: dashboardPath, NetworkURL: networkPath,
		GeneratedAt: now.UTC().Format("15:04:05") + " UTC", OpenAccessText: text,
	}
}

type counts struct {
	hosts, live                           int
	fresh, stale, offline, none, rejected int
	sessions, connected                   int
}

func (d Data) counts(hosts []hostView) counts {
	var c counts
	c.hosts = len(hosts)
	for _, v := range hosts {
		if v.graph.Connection.Live {
			c.live++
		}
		switch v.graph.Card.State {
		case protocol.CardFresh:
			c.fresh++
		case protocol.CardStale:
			c.stale++
		case protocol.CardOffline:
			c.offline++
		case protocol.CardRejected:
			c.rejected++
		default:
			c.none++
		}
		for _, s := range v.sessions {
			c.sessions++
			if s.Directory.Live != nil && *s.Directory.Live {
				c.connected++
			}
		}
	}
	return c
}

// BuildOverview is the Overview page: counts, and the hosts that need a look (not live, or a card that is
// not fresh).
func BuildOverview(d Data, info Info) *OverviewPage {
	hosts := d.hosts()
	c := d.counts(hosts)
	page := &OverviewPage{
		Chrome:    chrome("Overview", "overview", d.Now, info),
		HostCount: c.hosts, LiveHostCount: c.live,
		FreshCardCount: c.fresh, StaleCardCount: c.stale, OfflineCardCount: c.offline, NoCardCount: c.none, RejectedCardCount: c.rejected,
		SessionCount: c.sessions, ConnectedSessionCount: c.connected,
	}
	for _, v := range hosts {
		if !v.graph.Connection.Live || v.graph.Card.State != protocol.CardFresh {
			page.Attention = append(page.Attention, hostRow(v))
		}
	}
	return page
}

// BuildStatus is the strip htmx refreshes every few seconds.
func BuildStatus(d Data) *StatusPartial {
	hosts := d.hosts()
	c := d.counts(hosts)
	state := "up"
	if started, ok := protocol.ParseTime(d.Network.Gateway.StartedAt); ok {
		state = "up " + upFor(d.Now.Sub(started))
	}
	return &StatusPartial{
		GeneratedAt: d.Now.UTC().Format("15:04:05") + " UTC", GatewayState: state,
		HostCount: c.hosts, LiveHostCount: c.live,
		CardFreshCount: c.fresh, CardStaleCount: c.stale, CardOfflineCount: c.offline, CardNoneCount: c.none, CardRejectedCount: c.rejected,
	}
}

func upFor(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	days, hours, minutes := int(d.Hours())/24, int(d.Hours())%24, int(d.Minutes())%60
	switch {
	case days > 0:
		return fmt.Sprintf("%d d %d h", days, hours)
	case hours > 0:
		return fmt.Sprintf("%d h %d m", hours, minutes)
	}
	return fmt.Sprintf("%d m", minutes)
}

// BuildNetwork is the Network page shell: the graph document rides along so the first paint needs no
// second request, and the accessible host table is the keyboard and screen-reader path to the same cards.
func BuildNetwork(d Data, info Info) *NetworkPage {
	hosts := d.hosts()
	page := &NetworkPage{
		Chrome:   chrome("Network", "network", d.Now, info),
		GraphURL: graphPath, StatusURL: statusPath, CardURL: cardPrefix,
		Graph: BuildGraph(d),
	}
	for _, v := range hosts {
		page.HostRows = append(page.HostRows, hostRow(v))
	}
	return page
}

// BuildGraph is the hub-and-spoke document: the gateway is the hub, one spoke per host styled from
// connection.live (solid, dashed, dotted by class), no host-to-host connectivity edge, and the
// messages_seen edges as an overlay element class the page keeps off by default.
func BuildGraph(d Data) GraphDocument {
	doc := GraphDocument{
		GeneratedAt:  d.Now.UTC().Format(time.RFC3339),
		Truncated:    d.Graph.Truncated,
		EdgesPartial: d.Graph.EdgesPartial,
		Elements:     []GraphElement{{Data: GraphElementData{ID: "gateway", Label: "Gateway"}, Classes: "gateway"}},
	}
	known := map[string]bool{}
	for _, v := range d.hosts() {
		known[v.name] = true
		label := connectionLabel(v)
		class := connectionClass(label)
		age := agePtr(v.graph.Card.AgeMS)
		sessions := len(v.sessions)
		doc.Elements = append(doc.Elements,
			GraphElement{
				Data: GraphElementData{ID: "host:" + v.name, Label: v.name, ConnectionLive: v.graph.Connection.Live,
					CardState: v.graph.Card.State, CardAge: age,
					ConnectionLabel: label, CardSource: v.graph.Card.Source, SessionCount: &sessions},
				Classes: "host " + class + " card-" + cssWord(v.graph.Card.State),
			},
			GraphElement{
				Data:    GraphElementData{ID: "spoke:" + v.name, Source: "gateway", Target: "host:" + v.name, ConnectionLive: v.graph.Connection.Live},
				Classes: "spoke " + class,
			})
	}
	for _, e := range d.Graph.Edges {
		if e.Kind != protocol.GraphEdgeMessagesSeen || !known[e.From] || !known[e.To] {
			continue
		}
		doc.Elements = append(doc.Elements, GraphElement{
			Data:    GraphElementData{ID: "seen:" + e.From + ">" + e.To, Source: "host:" + e.From, Target: "host:" + e.To, Count: e.Count, HostStored: e.HostStored},
			Classes: "seen",
		})
	}
	return doc
}

// cssWord keeps a gateway word usable as a class name; an unexpected value becomes "unknown".
func cssWord(s string) string {
	if s == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

// BuildCard is one host's Agent Card partial, or false when no snapshot knows the host.
func BuildCard(d Data, host string) (*CardPartial, bool) {
	for _, v := range d.hosts() {
		if v.name != host {
			continue
		}
		g := v.graph
		out := &CardPartial{
			HostName: v.name, ConnectionLive: g.Connection.Live, ConnectionSource: g.Connection.Source,
			CardState: g.Card.State, CardAge: agePtr(g.Card.AgeMS), CardSource: g.Card.Source,
			CardRevision: g.Card.Revision, CardFetchedAt: g.Card.LastOKAt, CardOmitted: g.Card.CardOmitted, Card: g.Card.Card,
		}
		if v.network != nil {
			out.HeartbeatAge = agePtr(v.network.Connection.HeartbeatAgeMS)
		}
		if out.HeartbeatAge == "" {
			out.HeartbeatAge = agePtr(g.Connection.HeartbeatAgeMS)
		}
		if g.Card.Card != nil {
			out.CardVersion = g.Card.Card.Version
		}
		for _, s := range v.sessions {
			row := SessionRow{Address: s.Address, Name: s.Name, Host: s.Host, SessionID: s.SessionID}
			if s.Directory.Live != nil {
				row.DirectoryLiveKnown, row.DirectoryLive = true, *s.Directory.Live
			}
			if s.Reported != nil {
				row.State, row.Input = s.Reported.State, s.Reported.Input
			}
			out.Sessions = append(out.Sessions, row)
		}
		return out, true
	}
	return nil, false
}
