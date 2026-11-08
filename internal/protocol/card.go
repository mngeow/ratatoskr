package protocol

// The host card's wire shape: the public JSON a daemon serves, the limits that bound it, and the
// views the owner's own daemon answers to its window about the card.yaml file it serves from.
//
// Authority: docs/implementation/host-cards-contract.md (ADR-0005). The card is an "A2A-style
// card": field names follow A2A v1.0.0 section 4.4 as read on 2026-11-07, and only fields verified
// in that read are emitted. Nothing here claims A2A conformance, and no securitySchemes field
// exists on purpose.
//
// A card is built from an allow-list and is public. It never carries a token, key, pairing datum,
// credential, skill body, absolute path, username, environment, session text, prompt or working
// directory. The types below have no field for any of those, which is the first line of defence;
// internal/card checks the strings as well.
//
// Every time on this wire is a string, RFC 3339 in UTC to whole seconds (FormatTime), like the
// network API. An unknown fact is an omitted key.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

const (
	// CardPath is where a daemon serves its card. CardAliasPath serves the same bytes and exists
	// only as a compatibility alias.
	CardPath      = "/.well-known/agent-card.json"
	CardAliasPath = "/.well-known/agent.json"

	// CardExtensionURI names the one extension a card carries: evidence about found skills.
	CardExtensionURI = "urn:ratatoskr:host-card:v1"
	// CardProtocolBinding and CardProtocolVersion say "discovery metadata only; no task
	// operations" in the one supported interface.
	CardProtocolBinding = "RATATOSKR-CARD"
	CardProtocolVersion = "1"

	// MaxCardBytes bounds a card, served or fetched. The gateway reads at most MaxCardPollBytes of
	// a response, then holds the card to MaxCardBytes.
	MaxCardBytes     = 64 << 10 // UNTESTED guess; contract default
	MaxCardPollBytes = 256 << 10

	// Bounds on card text, in runes unless stated.
	MaxCardName             = 64
	MaxCardDescription      = 280
	MaxCardSkills           = 64 // declared skills in one card
	MaxCardSkillID          = 64
	MaxCardSkillName        = 64
	MaxCardSkillDescription = 280
	MaxCardSkillTags        = 8
	MaxCardSkillTag         = 32

	// MaxCardFileBytes bounds the owner's card.yaml.
	MaxCardFileBytes = 64 << 10
)

// HostCard is the public card. The same type is what the daemon publishes, what the gateway stores
// and what the window shows; parsing rules live in internal/card.
//
// SupportedInterfaces holds exactly one entry. Its URL is empty in a card as stored and edited and
// is filled in by the daemon at serve time from the listener's bound address (never from a request
// header), so a card can be published while the listener is off.
type HostCard struct {
	Name                string           `json:"name"`
	Description         string           `json:"description"`
	SupportedInterfaces []CardInterface  `json:"supportedInterfaces"`
	Version             string           `json:"version"`
	Capabilities        CardCapabilities `json:"capabilities"`
	DefaultInputModes   []string         `json:"defaultInputModes"`
	DefaultOutputModes  []string         `json:"defaultOutputModes"`
	Skills              []CardSkill      `json:"skills"`
}

// CardInterface is the one supported interface: the card's own URL.
type CardInterface struct {
	URL             string `json:"url,omitempty"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// CardCapabilities are all false: nothing here streams, pushes or serves an extended card.
type CardCapabilities struct {
	Streaming         bool            `json:"streaming"`
	PushNotifications bool            `json:"pushNotifications"`
	ExtendedAgentCard bool            `json:"extendedAgentCard"`
	Extensions        []CardExtension `json:"extensions"`
}

// CardExtension is the one extension, CardExtensionURI.
type CardExtension struct {
	URI         string              `json:"uri"`
	Description string              `json:"description,omitempty"`
	Required    bool                `json:"required"`
	Params      CardExtensionParams `json:"params"`
}

// CardExtensionParams carries evidence per declared skill id. Evidence says a skill file was FOUND;
// it never says the skill is allowed or works.
type CardExtensionParams struct {
	Skills map[string]SkillEvidence `json:"skills"`
}

// SkillEvidence is what the owner's daemon found for one declared skill.
type SkillEvidence struct {
	Harness        string `json:"harness,omitempty"`
	HarnessVersion string `json:"harnessVersion,omitempty"`
	Scope          string `json:"scope,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	ScannedAt      string `json:"scannedAt,omitempty"`
}

// CardSkill is one skill the owner DECLARED. "Declared" is the owner's statement; nothing checks it.
type CardSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Poll outcomes the gateway records for one poll of a card endpoint. They say what the card
// endpoint did and nothing about sessions, and the heartbeat and stream stay the authority on
// connection.
const (
	PollOK          = "200"          // valid card
	PollNotModified = "304"          // endpoint reachable, card unchanged
	PollTimeout     = "timeout"      // connect or total time ran out
	PollRefused     = "refused"      // connection refused or otherwise not made
	PollRedirect    = "redirect"     // a 3xx; never followed
	PollHTTPStatus  = "http-status"  // any other status
	PollInvalid     = "invalid-card" // answered, but not a card this build accepts; last good card kept
	PollOversize    = "oversize"     // body over the poll cap
	PollCertificate = "certificate"  // the pinned certificate did not match; polling stops until the target is set again
)

// Card freshness states in a graph host. They are card-endpoint states only.
//
//	fresh:    a good poll within 2 intervals
//	stale:    older; the last good card is kept and its age shown
//	offline:  no good poll for max(15 minutes, 4 intervals)
//	none:     no card has ever been stored (no target, or never answered)
//	rejected: the latest answer was refused; the last good card, if any, is kept
const (
	CardFresh    = "fresh"
	CardStale    = "stale"
	CardOffline  = "offline"
	CardNone     = "none"
	CardRejected = "rejected"
)

// CardHost is the local host's identity. Name is the paired host name; Paired is false only when
// the daemon has no pairing to read (a daemon normally refuses to run unpaired).
type CardHost struct {
	Name   string `json:"name,omitempty"`
	Paired bool   `json:"paired"`
}

// CardProblem is one reason a card (or a card.yaml) is not acceptable. Field names the field; Message
// is our own sentence and never repeats a rejected value.
type CardProblem struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// SkillInventory is what the last scan found on this host. It is evidence of files, not of ability.
// Only user-scope locations are scanned; NotScanned says so rather than implying a full inventory.
type SkillInventory struct {
	ScannedAt   string       `json:"scanned_at,omitempty"`
	Found       []FoundSkill `json:"found"`
	NotScanned  []string     `json:"not_scanned"`
	RootsAbsent []string     `json:"roots_absent,omitempty"`
	Notes       []SkillNote  `json:"notes,omitempty"`
	Truncated   bool         `json:"truncated,omitempty"`
}

// FoundSkill is one skill file found. Label is a relative label with no absolute path and no
// username. Declared says card.yaml declares a skill with this directory name. LocationVerified is false for a location the
// official documentation does not mention (an undocumented location); it says nothing about the skill (nothing here checks
// ability, and "verified" is a word this feature never applies to a skill).
type FoundSkill struct {
	Harness          string `json:"harness"`
	HarnessVersion   string `json:"harness_version,omitempty"`
	Scope            string `json:"scope"`
	Label            string `json:"label"`
	DirName          string `json:"dir_name"`
	Name             string `json:"name,omitempty"`
	Description      string `json:"description,omitempty"`
	SHA256           string `json:"sha256"`
	LocationVerified bool   `json:"location_verified"`
	Declared         bool   `json:"declared"`
}

// SkillNote is a "could not read" note. Reason is our sentence.
type SkillNote struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// CardFileViewSchema is the schema of CardFileView.
const CardFileViewSchema = 1

// CardReadableNotice is the one sentence the window states beside the card: the endpoint has no
// authentication, so nothing in a card may be sensitive.
const CardReadableNotice = "This card is readable by anyone who can reach this port."

// Listener states. They are first-class: a card is never called ready or published to the Graph
// unless the state is CardListenerListening. A bind failure does not stop the daemon; it is the
// not_bound state with a reason.
const (
	CardListenerListening = "listening"
	CardListenerOff       = "off"
	CardListenerNotBound  = "not_bound"
)

// DefaultCardListen is where a daemon serves its card when --card-listen is not given. The gateway
// is 7777.
const DefaultCardListen = "0.0.0.0:7778"

// States of the owner's card.yaml.
const (
	CardFileOK               = "ok"                // valid; this is the served card
	CardFileNeedsDescription = "needs_description" // the template, or a file whose only fault is a missing description
	CardFileInvalid          = "invalid"           // does not parse or validate; the last good card (if any) stays served
	CardFileMissing          = "missing"           // no file; the daemon writes the template at start
)

// CardListener is whether this daemon serves its card, and where. Addr and URL are set only when
// listening; Reason only when not bound.
type CardListener struct {
	State  string `json:"state"`
	Addr   string `json:"addr,omitempty"`   // the bound address when listening
	URL    string `json:"url,omitempty"`    // the card's own URL when listening
	Reason string `json:"reason,omitempty"` // our sentence, when not bound
}

// CardFileView is what the owner's own daemon answers about its card.yaml. It needs no gateway,
// no graph and no network.
//
// Token changes whenever the file's bytes change. Accepting a draft
// carries the Token it was made against; a stale one is refused with card_conflict and the answer
// holds the current view.
type CardFileView struct {
	Schema   int              `json:"schema"`
	Host     CardHost         `json:"host"`
	Path     string           `json:"path"` // absolute path of card.yaml, for the owner
	Token    string           `json:"token"`
	File     CardFileStatus   `json:"file"`
	Served   *CardServed      `json:"served,omitempty"` // the last good card; nil when none is served
	Listener CardListener     `json:"listener"`
	Skills   SkillInventory   `json:"skills"` // evidence only; detected skills are NOT added to the card
	Drafting CardDraftingInfo `json:"drafting"`
	Readable string           `json:"readable"` // CardReadableNotice
}

// CardFileStatus is the state of card.yaml. Line is 1-based, 0 for none. Message is our sentence and
// never repeats a rejected value.
type CardFileStatus struct {
	State   string `json:"state"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

// CardServed is the last good card. Current is false when card.yaml has moved on and is not valid
// (the older card keeps being served). Since is when it became the served card.
type CardServed struct {
	Card    HostCard `json:"card"`
	Since   string   `json:"since"`
	Current bool     `json:"current"`
}

// CardDraftingInfo says whether a draft can be prepared and which preset would run. Preset is empty
// when card.yaml names none.
type CardDraftingInfo struct {
	Preset    string `json:"preset,omitempty"`
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

// Draft states. A draft is private to the window that prepared it.
const (
	CardDraftPrepared  = "prepared"  // confirmation built; nothing has run
	CardDraftRunning   = "running"   // the command is running
	CardDraftReady     = "ready"     // a valid candidate exists; nothing is written until accept
	CardDraftFailed    = "failed"    // see Error
	CardDraftCancelled = "cancelled" // discarded by the owner, by disconnect, or expired (Error "expired")
)

// Limits of a draft run.
const (
	CardDraftTimeoutSec     = 120
	CardDraftOutputCapBytes = 64 << 10
)

// CardDraft is one model-draft attempt. Prepared drafts carry the confirmation the owner must see:
// the exact command, the exact payload on stdin and its size.
type CardDraft struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Error string `json:"error,omitempty"` // our sentence; never a rejected value or command output

	// Confirmation, present from prepared on.
	Preset         string   `json:"preset"`
	Binary         string   `json:"binary"`           // the resolved executable, absolute
	Argv           []string `json:"argv"`             // exact, argv[0] included, no shell
	Payload        string   `json:"payload"`          // the exact stdin text
	PayloadBytes   int      `json:"payload_bytes"`    // len(Payload)
	EnvNames       []string `json:"env_names"`        // names of the variables passed, never values
	TimeoutSec     int      `json:"timeout_sec"`      // CardDraftTimeoutSec
	OutputCapBytes int      `json:"output_cap_bytes"` // CardDraftOutputCapBytes
	Caveats        []string `json:"caveats"`          // e.g. what the preset does not stop the harness loading

	// Result, present when ready.
	Diff           []CardDiffEntry `json:"diff,omitempty"`
	CandidateBytes int             `json:"candidate_bytes,omitempty"`
	BackupNotice   string          `json:"backup_notice,omitempty"`
	RewriteNotice  string          `json:"rewrite_notice,omitempty"`
	BaseToken      string          `json:"base_token,omitempty"` // the file token the draft was made against
	Stale          bool            `json:"stale,omitempty"`      // card.yaml changed after the draft was made
}

// CardDiffEntry is one changed field. Path is like "description" or "skills[2].tags". Old and New are
// short text renderings. Generated marks a field the model produced.
type CardDiffEntry struct {
	Path      string `json:"path"`
	Old       string `json:"old,omitempty"`
	New       string `json:"new,omitempty"`
	Generated bool   `json:"generated"`
}
