package protocol

import "time"

// The participant API, contract 1.0: a second, language-neutral HTTP+JSON surface on the gateway for an
// external adapter. Routes and rules are in docs/implementation/participant-api-contract.md; the JSON
// Schemas under docs/spec/participant-api/schemas are derived from the types below and are normative.
//
// Only the types that exist for the participant API are here. A send, an ack, a status report, an offer,
// a directory entry, a session and an error reuse the host protocol's types (SendRequest, SendResponse,
// AckRequest, StatusReport, Offer, DirectoryEntry, SessionInfo, MessageInfo, WireError) and their JSON
// names, so there is one definition of each. This file adds nothing to the host protocol: WireVersion
// stays 2 and nothing under /v1/ changes.
//
// A slice that the contract says is always present (offers, directory, versions) must be non-nil when it
// is encoded: a nil slice would encode as null, which the schemas refuse.
//
// UNTESTED.

const (
	// ParticipantPathPrefix is the major version of the contract as a path prefix. A removed or
	// changed meaning is a new major and a new prefix.
	ParticipantPathPrefix = "/participant/v1"

	// ParticipantContract is the contract's major.minor, sent in ParticipantContractHeader on every
	// participant response. Within a major, changes only add.
	ParticipantContract = "1.0"
	// ParticipantContractMajor is the one major the gateway serves.
	ParticipantContractMajor = "1"
	// ParticipantContractHeader carries the minor on a response. A client may send it to say what
	// it speaks; a different major is refused with WireErrUnsupportedContract.
	ParticipantContractHeader = "X-Ratatoskr-Contract"

	// Routes under ParticipantPathPrefix.
	ParticipantPairPath      = ParticipantPathPrefix + "/pair"
	ParticipantSessionsPath  = ParticipantPathPrefix + "/sessions"
	ParticipantDirectoryPath = ParticipantPathPrefix + "/directory"
	ParticipantMessagesPath  = ParticipantPathPrefix + "/messages"
	ParticipantInboxPath     = ParticipantPathPrefix + "/inbox"

	// Error codes in WireError.Code that only the participant API uses.
	WireErrUnsupportedContract = "unsupported_contract"
	WireErrTooManySessions     = "too_many_sessions"

	// Limits (UNTESTED guesses; contract section 7).
	//
	// ParticipantMaxSessions is the open sessions one participant may hold after replacement.
	ParticipantMaxSessions = 8
	// ParticipantMaxOffers is the most offers one inbox answer carries.
	ParticipantMaxOffers = 8
	// ParticipantMaxWait is the longest inbox wait, in whole seconds. The default is the same value.
	ParticipantMaxWait = 20
)

// ParticipantPairRequest exchanges a one-time code for a credential. Participant is optional: the code
// was minted for one participant id, and the gateway answers with it. If it is sent, the gateway refuses
// a mismatch.
type ParticipantPairRequest struct {
	Code        string `json:"code"`
	Participant string `json:"participant,omitempty"`
}

// ParticipantPairResponse carries the credential once. The gateway keeps only its hash.
type ParticipantPairResponse struct {
	Participant string `json:"participant"`
	Credential  string `json:"credential"`
}

// ParticipantDiscovery answers GET /participant/v1, which needs no credential and carries no secret, so
// an adapter can check compatibility before pairing.
type ParticipantDiscovery struct {
	Contract string    `json:"contract"` // major.minor, ParticipantContract
	Versions []string  `json:"versions"` // the majors served, ["1"]
	Now      time.Time `json:"now"`
}

// ParticipantAddSessionRequest registers one session by name. The participant comes from the credential,
// never from the request. Registering a name again ends the earlier open session of that name.
type ParticipantAddSessionRequest struct {
	Name string `json:"name"`
}

// ParticipantAddSessionResponse answers a registration: the new session (a new id every time) and the
// current directory.
type ParticipantAddSessionResponse struct {
	Participant string           `json:"participant"`
	Session     SessionInfo      `json:"session"`
	Directory   []DirectoryEntry `json:"directory"`
}

// ParticipantDirectoryResponse answers GET /participant/v1/directory: every registered session the
// participant may contact, live or not.
type ParticipantDirectoryResponse struct {
	Directory []DirectoryEntry `json:"directory"`
}

// InboxResponse answers one long poll. An empty Offers is the normal timeout answer, and the adapter asks
// again at once. Replaced is true only when a second poll from the same participant displaced this one.
// Each Offer's To is the session name and ToSession the id the gateway meant, because one inbox serves
// every session of the participant.
type InboxResponse struct {
	Offers   []Offer `json:"offers"`
	Replaced bool    `json:"replaced"`
}
