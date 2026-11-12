package discover

// The discovery directory (ADR-0009 sections 1 to 5): the hosts of the network, each host's Agent Card as the
// host owner wrote it, and the exact sessions that can be contacted, joined from the two views the daemon
// already keeps (the network view and the graph view). The gateway does no search and no ranking; the model
// reads this and chooses a recipient for ratatoskr_send.
//
// Four facts stay separate and nothing is turned into a claim: connection (the gateway's stream and
// heartbeat), card freshness (the gateway's poll), the declaration (what the host owner wrote, never
// verified) and the session list. An unknown value is the TOON literal null, never a guess.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Format is the one switch: Render is the boundary function of ADR-0009 section 8.
type Format int

const (
	// FormatTOON is the model-facing form, and the default.
	FormatTOON Format = iota
	// FormatJSON is the fallback if TOON is not worth keeping or a model misreads a status: compact JSON
	// of the same Directory, behind the same function.
	FormatJSON
)

// Note is the line every reply carries, so the model reads the declarations for what they are.
const Note = "declared means what each host owner wrote; nothing here verifies that a session can do it; connection, card freshness and sessions are separate facts; null means unknown"

// Directory is the whole reply.
type Directory struct {
	Note         string    `json:"note"`
	Gateway      string    `json:"gateway"`                  // the daemon's own state: connected, not_connected, ...
	SnapshotAgeS *int64    `json:"snapshot_age_s,omitempty"` // how old the network snapshot is; absent when there is none
	Hosts        []Host    `json:"hosts"`
	Skills       []Skill   `json:"skills"`
	Sessions     []Session `json:"sessions"`
}

// Host is one paired host.
type Host struct {
	Host        string  `json:"host"`
	Connection  string  `json:"connection"`            // live, not_live or never_seen
	Card        string  `json:"card"`                  // fresh, stale, offline, none or rejected
	CardAgeS    *int64  `json:"card_age_s"`            // null when there was never a good poll
	Description *string `json:"description,omitempty"` // declared by the host owner; null when no card body
}

// Skill is one declared skill of a host's card.
type Skill struct {
	Host        string `json:"host"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Session is one contactable session, with the exact address and id.
type Session struct {
	Address    string  `json:"address"`
	Host       string  `json:"host"`
	SessionID  string  `json:"session_id"`
	Connected  string  `json:"connected"`   // live, not_live or unknown (the gateway's directory fact)
	State      *string `json:"state"`       // the host's reported state; null when none was reported
	StateStale *bool   `json:"state_stale"` // null when no state was reported
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func ageS(ms *int64) *int64 {
	if ms == nil {
		return nil
	}
	s := *ms / 1000
	if s < 0 {
		s = 0
	}
	return &s
}

// FromViews joins the daemon's network view and graph view. Either snapshot may be missing: the directory is
// then smaller and the gateway state says why; nothing is invented.
func FromViews(network protocol.NetworkView, graph protocol.GraphView, now time.Time) Directory {
	dir := Directory{Note: Note, Gateway: network.Gateway.State}
	if dir.Gateway == "" {
		dir.Gateway = graph.Gateway.State
	}
	if fetched, ok := protocol.ParseTime(network.FetchedAt); ok && network.Snapshot != nil {
		age := int64(now.Sub(fetched).Seconds())
		if age < 0 {
			age = 0
		}
		dir.SnapshotAgeS = &age
	}

	type joined struct {
		host    string
		graph   *protocol.GraphHost
		network *protocol.NetworkHost
	}
	byName := map[string]*joined{}
	get := func(name string) *joined {
		if j, ok := byName[name]; ok {
			return j
		}
		j := &joined{host: name}
		byName[name] = j
		return j
	}
	if graph.Snapshot != nil {
		for i := range graph.Snapshot.Hosts {
			get(graph.Snapshot.Hosts[i].Name).graph = &graph.Snapshot.Hosts[i]
		}
	}
	if network.Snapshot != nil {
		for i := range network.Snapshot.Hosts {
			get(network.Snapshot.Hosts[i].Host).network = &network.Snapshot.Hosts[i]
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	dir.Hosts, dir.Skills, dir.Sessions = []Host{}, []Skill{}, []Session{}
	for _, name := range names {
		j := byName[name]
		host := Host{Host: name, Connection: "not_live", Card: protocol.CardNone}
		var heartbeat *int64
		if j.network != nil {
			host.Connection = liveWord(j.network.Connection.Live)
			heartbeat = j.network.Connection.HeartbeatAgeMS
		}
		if j.graph != nil {
			if j.network == nil {
				host.Connection = liveWord(j.graph.Connection.Live)
				heartbeat = j.graph.Connection.HeartbeatAgeMS
			}
			host.Card, host.CardAgeS = j.graph.Card.State, ageS(j.graph.Card.AgeMS)
			if c := j.graph.Card.Card; c != nil {
				host.Description = strPtr(c.Description)
				for _, skill := range c.Skills {
					dir.Skills = append(dir.Skills, Skill{Host: name, ID: skill.ID, Name: skill.Name, Description: skill.Description})
				}
			}
		}
		if host.Connection == "not_live" && heartbeat == nil {
			host.Connection = "never_seen"
		}
		dir.Hosts = append(dir.Hosts, host)
	}
	if network.Snapshot != nil {
		for _, s := range network.Snapshot.Sessions {
			session := Session{Address: s.Address, Host: s.Host, SessionID: s.SessionID, Connected: "unknown"}
			if s.Directory.Live != nil {
				session.Connected = liveWord(*s.Directory.Live)
			}
			if s.Reported != nil && s.Reported.State != "" {
				session.State, session.StateStale = strPtr(s.Reported.State), &s.Reported.Stale
			}
			dir.Sessions = append(dir.Sessions, session)
		}
		sort.SliceStable(dir.Sessions, func(i, j int) bool { return dir.Sessions[i].Address < dir.Sessions[j].Address })
	}
	return dir
}

func liveWord(live bool) string {
	if live {
		return "live"
	}
	return "not_live"
}

// Render is THE presentation boundary: the one function that turns a Directory into the model-facing text.
// Removing TOON later means changing this function.
func Render(dir Directory, format Format) string {
	if format == FormatJSON {
		body, err := json.Marshal(dir)
		if err != nil {
			return `{"error":"the directory could not be encoded"}`
		}
		return string(body)
	}
	var snapshotAge any
	if dir.SnapshotAgeS != nil {
		snapshotAge = *dir.SnapshotAgeS
	}
	hosts := table{key: "hosts", fields: []string{"host", "connection", "card", "card_age_s", "description"}}
	for _, h := range dir.Hosts {
		hosts.rows = append(hosts.rows, []any{h.Host, h.Connection, h.Card, optInt(h.CardAgeS), optStr(h.Description)})
	}
	skills := table{key: "skills", fields: []string{"host", "id", "name", "description"}}
	for _, s := range dir.Skills {
		skills.rows = append(skills.rows, []any{s.Host, s.ID, s.Name, s.Description})
	}
	sessions := table{key: "sessions", fields: []string{"address", "host", "session_id", "connected", "state", "state_stale"}}
	for _, s := range dir.Sessions {
		var stale any
		if s.StateStale != nil {
			stale = *s.StateStale
		}
		sessions.rows = append(sessions.rows, []any{s.Address, s.Host, s.SessionID, s.Connected, optStr(s.State), stale})
	}
	return encodeTOON(
		[][2]any{{"note", dir.Note}, {"gateway", dir.Gateway}, {"snapshot_age_s", snapshotAge}},
		[]table{hosts, skills, sessions},
	)
}

func optInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func optStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
