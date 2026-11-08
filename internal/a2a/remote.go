package a2a

// The gateway's outbound A2A client: fetch a service's card, send it a message, read a task.
//
// This file never dials. Every connection goes through an *http.Client the gateway builds on its card
// dial rules (IP literal, dial-time deny list, no proxy, no redirects, no cookies, HTTP/1.1, TLS pin),
// which is why it asks a ClientFactory rather than constructing a transport of its own. That client is
// wrapped here for two things only: a reply may not exceed 256 KiB, and the send path must be able to
// tell whether the request was written, because that is the whole difference between "not delivered"
// and "outcome unknown".
//
// The SDK's own client does the protocol. It is built from the REGISTERED destination URL, never from a
// card: a card must not be able to widen where the gateway talks.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No service has been called.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

const (
	// remoteMaxBytes bounds every reply body: the card, a send's answer and a task. It is read as
	// limit+1, so a body that is exactly at the limit is accepted and one byte past it is refused.
	remoteMaxBytes = 256 << 10

	// remoteMaxCardBytes bounds the canonical card this package returns.
	remoteMaxCardBytes = 64 << 10

	// remoteMaxSkills bounds the skills a card may declare.
	remoteMaxSkills = 64

	// remoteMaxSendBytes bounds one outgoing text.
	remoteMaxSendBytes = 64 << 10

	// remoteStatusTextRunes and remoteArtifactRunes bound the text mirrored from a remote task.
	remoteStatusTextRunes = 4 << 10
	remoteArtifactRunes   = 16 << 10

	// remoteErrorMessageBytes bounds a service's own error text.
	remoteErrorMessageBytes = 200
)

// ErrReplyTooLarge reports a reply body over remoteMaxBytes.
var ErrReplyTooLarge = errors.New("a2a: the service's reply is larger than the limit")

// RemoteError is a JSON-RPC (or A2A) error the SERVICE answered with.
//
// The message was received: the service read the request and refused it, which is why this is neither
// "not delivered" nor "unknown".
type RemoteError struct {
	Code    int
	Message string
}

// Error renders the service's refusal: its code and its own message, bounded.
func (e *RemoteError) Error() string {
	return fmt.Sprintf("a2a: the service answered with an error (code %d): %s", e.Code, boundRunesBytes(e.Message, remoteErrorMessageBytes))
}

// RemoteTask is the part of a service's task the gateway mirrors.
type RemoteTask struct {
	ID        string
	ContextID string
	// State is our short name: SUBMITTED, WORKING, INPUT_REQUIRED, COMPLETED, FAILED, REJECTED, with
	// CANCELED and AUTH_REQUIRED passing through. A task without a status has an empty State, because
	// guessing one would be inventing a fact.
	State string
	// Text is the status message's text, or "".
	Text string
	// Results is one entry per artifact: its text parts joined, or a data part as JSON.
	Results []string
}

// RemoteSend is one outgoing message.
type RemoteSend struct {
	// MessageID is required and chosen by the caller, so a retried send can be recognised by the
	// receiver rather than looking like a second message.
	MessageID string
	// Text is required.
	Text string
	// RemoteTaskID answers an existing remote task.
	RemoteTaskID string
	// ContextID continues a remote context.
	ContextID string
	// Origin is the gateway-written, authenticated sender ("name@host" or "ext:NAME"). A standard service
	// has no field for it, so it travels in the message metadata under OriginMetadataKey.
	Origin string
}

// OriginMetadataKey is the message-metadata key that carries the gateway-verified origin to a service.
const OriginMetadataKey = "ratatoskr.origin"

// Remote is the outbound client. Factory builds the dial-ruled client for one destination.
type Remote struct {
	Factory ClientFactory
}

// The gateway registers a Remote as the card fetcher, so the interface is pinned here.
var _ CardFetcher = (*Remote)(nil)

// writeTracker records whether a request reached the wire.
//
// It is the only evidence this package has about "sent or not", and it is read from the trace callback
// rather than guessed from an error, which is why it is an atomic.
type writeTracker struct {
	wrote atomic.Bool
	// status is the HTTP status of the response, or 0 when none arrived. It is what tells "the service
	// refused our credential" (401/403) from every other failure, whatever the SDK made of it.
	status atomic.Int32
}

// refusedCredential reports whether the service answered 401 or 403.
func (t *writeTracker) refusedCredential() bool {
	status := t.status.Load()
	return status == http.StatusUnauthorized || status == http.StatusForbidden
}

// limitTransport is the one transport this package contributes.
//
// It does two things and nothing else: it notes when the request was written, and it wraps the response
// body so a reply cannot exceed the limit. Everything else - dial rules, TLS pin, no redirects, no proxy
// - is the transport it wraps, which the gateway built.
type limitTransport struct {
	base    http.RoundTripper
	tracker *writeTracker
}

// RoundTrip attaches the trace and wraps the body.
func (t *limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	trace := &httptrace.ClientTrace{
		WroteRequest: func(httptrace.WroteRequestInfo) {
			// The request reached the wire. From here on a missing answer is uncertainty, never "not
			// sent".
			t.tracker.wrote.Store(true)
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	response, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.tracker.status.Store(int32(response.StatusCode))
	if response.Body != nil {
		response.Body = &limitedBody{body: response.Body}
	}
	return response, nil
}

// limitedBody refuses to read past the limit.
//
// A body over the limit is refused rather than truncated: a truncated card or task would look valid and
// be wrong.
type limitedBody struct {
	body io.ReadCloser
	read int64
}

// Read reads at most limit+1 bytes in total: the read is clamped to what remains plus one, so the byte past
// the limit is seen (and refused) but nothing beyond it is ever pulled in.
func (b *limitedBody) Read(p []byte) (int, error) {
	if b.read > remoteMaxBytes {
		return 0, ErrReplyTooLarge
	}
	if remaining := remoteMaxBytes - b.read + 1; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := b.body.Read(p)
	b.read += int64(n)
	if b.read > remoteMaxBytes {
		return n, ErrReplyTooLarge
	}
	return n, err
}

// Close closes the underlying body.
func (b *limitedBody) Close() error { return b.body.Close() }

// boundClient wraps the factory's client and returns it with the tracker for one call.
//
// The factory is called once per call and its client is never reused: it is bound to one destination and
// one pin.
func (r *Remote) boundClient(ctx context.Context, dest Destination) (*http.Client, *writeTracker, error) {
	if r.Factory == nil {
		return nil, nil, errors.New("the client could not be built")
	}
	client, err := r.Factory(ctx, dest)
	if err != nil {
		// The factory's own text is not quoted: it describes a dial and may name an address.
		return nil, nil, errors.New("the client could not be built")
	}
	if client == nil {
		return nil, nil, errors.New("the client could not be built")
	}

	tracker := &writeTracker{}
	base := client.Transport
	if base == nil {
		// A client with no transport would dial with net/http's default transport, which has none of the
		// gateway's rules. Refusing is the only safe answer.
		return nil, nil, errors.New("the client could not be built")
	}

	copied := *client
	copied.Transport = &limitTransport{base: base, tracker: tracker}
	return &copied, tracker, nil
}

// endpointClient is the SDK client for one destination, built from the registered URL alone.
//
// The endpoints list is a single JSON-RPC interface at dest.URL: never a card's supportedInterfaces,
// because a card must not be able to redirect where the gateway talks.
func (r *Remote) endpointClient(ctx context.Context, dest Destination) (*a2aclient.Client, *writeTracker, error) {
	client, tracker, err := r.boundClient(ctx, dest)
	if err != nil {
		return nil, nil, err
	}

	endpoints := []*a2a.AgentInterface{a2a.NewAgentInterface(dest.URL, a2a.TransportProtocolJSONRPC)}
	sdk, err := a2aclient.NewFromEndpoints(ctx, endpoints, a2aclient.WithJSONRPCTransport(client))
	if err != nil {
		return nil, nil, errors.New("the client could not be built")
	}
	return sdk, tracker, nil
}

// cardURLFor is the card address for one destination: the origin plus the well-known path.
//
// A path in the registered URL is deliberately ignored: the card lives at the origin's well-known path,
// not under whatever path the A2A endpoint uses.
func cardURLFor(dest Destination) (string, error) {
	parsed, err := url.Parse(dest.URL)
	if err != nil {
		return "", errors.New("the address is not a URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		// Refused before any resolver call: the SDK's resolver reads local files for a file:// URL, and
		// nothing here may read a file.
		return "", errors.New("the address is not http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("the address has no host")
	}
	origin := url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
	return origin.JoinPath("/.well-known/agent-card.json").String(), nil
}

// FetchCard fetches and strictly validates a service's card.
//
// The validation is ours because a card is a claim, not a proof: it must name its own origin, declare a
// JSON-RPC 1.0 interface reachable where the gateway registered it, and say what its skills are.
//
// UNTESTED.
func (r *Remote) FetchCard(ctx context.Context, dest Destination) (cardJSON string, auth CardAuth, err error) {
	cardAddress, err := cardURLFor(dest)
	if err != nil {
		return "", CardAuthNone, fmt.Errorf("%w: %s", ErrCardInvalid, err.Error())
	}

	client, _, err := r.boundClient(ctx, dest)
	if err != nil {
		return "", CardAuthNone, fmt.Errorf("%w: the card could not be fetched", ErrCardInvalid)
	}

	card, err := agentcard.NewResolver(client).Resolve(ctx, cardAddress)
	if err != nil {
		if errors.Is(err, ErrReplyTooLarge) {
			return "", CardAuthNone, fmt.Errorf("%w: the card is larger than the limit", ErrCardInvalid)
		}
		// The resolver's own text may quote a URL or a body; it is not repeated.
		return "", CardAuthNone, fmt.Errorf("%w: the card could not be read", ErrCardInvalid)
	}

	if reason := validateRemoteCard(card, dest); reason != "" {
		return "", CardAuthNone, fmt.Errorf("%w: %s", ErrCardInvalid, reason)
	}

	canonical, err := json.Marshal(card)
	if err != nil {
		return "", CardAuthNone, fmt.Errorf("%w: the card could not be encoded", ErrCardInvalid)
	}
	if len(canonical) > remoteMaxCardBytes {
		return "", CardAuthNone, fmt.Errorf("%w: the card is larger than the limit", ErrCardInvalid)
	}

	return string(canonical), classifyCardAuth(card), nil
}

// validateRemoteCard applies the strict card rules and returns a fixed reason, or "".
//
// No reason quotes the card: a rejected card is a stranger's text.
func validateRemoteCard(card *a2a.AgentCard, dest Destination) string {
	if card == nil {
		return "the service sent no card"
	}
	if strings.TrimSpace(card.Name) == "" || strings.TrimSpace(card.Description) == "" {
		return "the card has no name or description"
	}
	if len(card.DefaultInputModes) == 0 || len(card.DefaultOutputModes) == 0 {
		return "the card declares no input or output modes"
	}
	if len(card.Skills) > remoteMaxSkills {
		return "the card declares too many skills"
	}
	for _, skill := range card.Skills {
		// AgentSkill is a value, so an absent one is the zero value.
		if strings.TrimSpace(skill.ID) == "" || strings.TrimSpace(skill.Name) == "" {
			return "a skill has no id or name"
		}
	}

	// The card must point at the address the owner registered, and nowhere else.
	registered, err := url.Parse(dest.URL)
	if err != nil {
		return "the registered address is not a URL"
	}
	for _, iface := range card.SupportedInterfaces {
		if iface == nil {
			continue
		}
		// a2a.Version is the protocol version this build speaks; a card offering anything else is not
		// something we can call correctly.
		if iface.ProtocolBinding != a2a.TransportProtocolJSONRPC || iface.ProtocolVersion != a2a.Version {
			continue
		}
		offered, err := url.Parse(iface.URL)
		if err != nil {
			continue
		}
		if offered.Scheme == registered.Scheme && offered.Host == registered.Host {
			return ""
		}
	}
	return "the card does not offer a JSON-RPC 1.0 interface at this address"
}

// Send makes one SendMessage call. It is never retried.
//
// The three outcomes are the point: a service error is a RemoteError (received and refused), a request
// that provably never reached the wire wraps ErrNotDelivered, and anything else wraps ErrUncertain - which
// the rest of the gateway treats as "it may have arrived", never as a failure to replay.
//
// UNTESTED.
func (r *Remote) Send(ctx context.Context, dest Destination, in RemoteSend) (RemoteTask, error) {
	if strings.TrimSpace(in.MessageID) == "" {
		// Without an id the receiver cannot recognise a second copy, so nothing is sent.
		return RemoteTask{}, fmt.Errorf("%w: the message has no id", ErrNotDelivered)
	}
	if in.Text == "" {
		return RemoteTask{}, fmt.Errorf("%w: the message is empty", ErrNotDelivered)
	}
	if len(in.Text) > remoteMaxSendBytes {
		return RemoteTask{}, fmt.Errorf("%w: the message is larger than the limit", ErrNotDelivered)
	}

	sdk, tracker, err := r.endpointClient(ctx, dest)
	if err != nil {
		return RemoteTask{}, fmt.Errorf("%w: %s", ErrNotDelivered, err.Error())
	}

	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(in.Text))
	message.ID = in.MessageID
	if in.RemoteTaskID != "" {
		message.TaskID = a2a.TaskID(in.RemoteTaskID)
	}
	if in.ContextID != "" {
		message.ContextID = in.ContextID
	}
	if in.Origin != "" {
		message.Metadata = map[string]any{OriginMetadataKey: in.Origin}
	}

	request := &a2a.SendMessageRequest{
		Message: message,
		Config: &a2a.SendMessageConfig{
			ReturnImmediately:   true,
			AcceptedOutputModes: []string{"text/plain"},
		},
	}

	result, err := sdk.SendMessage(ctx, request)
	if err != nil {
		if tracker.refusedCredential() {
			// The service definitely read and refused our credential: a refusal, never "uncertain".
			return RemoteTask{}, fmt.Errorf("%w: %w", ErrNotDelivered, ErrServiceAuth)
		}
		if remote, ok := asRemoteError(err); ok {
			return RemoteTask{}, remote
		}
		if tracker.wrote.Load() {
			return RemoteTask{}, fmt.Errorf("%w: no complete answer was read", ErrUncertain)
		}
		return RemoteTask{}, fmt.Errorf("%w: the request was not written", ErrNotDelivered)
	}

	switch value := result.(type) {
	case *a2a.Task:
		return remoteTaskFrom(value), nil
	case *a2a.Message:
		// A direct answer needs no task: the service replied with a message.
		return RemoteTask{State: "COMPLETED", Text: boundRunes(CleanText(statusText(value)), remoteStatusTextRunes)}, nil
	}
	// A written request whose answer is not a shape this build understands: the outcome is unknown.
	return RemoteTask{}, fmt.Errorf("%w: the answer was not a task or a message", ErrUncertain)
}

// Get reads one remote task by id. A read is safe to repeat, so a transport problem is a plain error.
//
// UNTESTED.
func (r *Remote) Get(ctx context.Context, dest Destination, remoteTaskID string) (RemoteTask, error) {
	if strings.TrimSpace(remoteTaskID) == "" {
		return RemoteTask{}, errors.New("a2a: the task id is empty")
	}

	sdk, tracker, err := r.endpointClient(ctx, dest)
	if err != nil {
		return RemoteTask{}, fmt.Errorf("a2a: %s", err.Error())
	}

	task, err := sdk.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(remoteTaskID)})
	if err != nil {
		if tracker.refusedCredential() {
			return RemoteTask{}, ErrServiceAuth
		}
		if remote, ok := asRemoteError(err); ok {
			return RemoteTask{}, remote
		}
		return RemoteTask{}, errors.New("a2a: the remote task could not be read")
	}
	return remoteTaskFrom(task), nil
}

// asRemoteError turns an SDK service error into this package's RemoteError.
//
// The SDK maps a JSON-RPC error object to an *a2a.Error carrying a sentinel and the service's message; it
// keeps the number to itself. The codes below are therefore OUR copy of the spec's numbers, keyed by the
// SDK's sentinels - not read from the service - and an unmapped sentinel is the spec's internal error.
func asRemoteError(err error) (*RemoteError, bool) {
	var sdkErr *a2a.Error
	if !errors.As(err, &sdkErr) {
		return nil, false
	}

	code := -32603
	for _, candidate := range remoteErrorCodes {
		if sdkErr.Err != nil && errors.Is(sdkErr.Err, candidate.err) {
			code = candidate.code
			break
		}
	}
	return &RemoteError{Code: code, Message: boundRunesBytes(CleanText(sdkErr.Message), remoteErrorMessageBytes)}, true
}

// remoteErrorCodes is the spec's number for each sentinel the SDK can produce.
//
// The SDK's own table lives in its internal jsonrpc package; this is a copy, and it is why a code here is
// labelled as ours rather than the service's. A number the SDK does not know becomes the internal error
// sentinel, so it lands on the default above.
var remoteErrorCodes = []struct {
	err  error
	code int
}{
	{err: a2a.ErrParseError, code: -32700},
	{err: a2a.ErrInvalidRequest, code: -32600},
	{err: a2a.ErrMethodNotFound, code: -32601},
	{err: a2a.ErrInvalidParams, code: -32602},
	{err: a2a.ErrInternalError, code: -32603},
	{err: a2a.ErrServerError, code: -32000},
	{err: a2a.ErrTaskNotFound, code: -32001},
	{err: a2a.ErrTaskNotCancelable, code: -32002},
	{err: a2a.ErrPushNotificationNotSupported, code: -32003},
	{err: a2a.ErrUnsupportedOperation, code: -32004},
	{err: a2a.ErrUnsupportedContentType, code: -32005},
	{err: a2a.ErrInvalidAgentResponse, code: -32006},
	{err: a2a.ErrExtendedCardNotConfigured, code: -32007},
	{err: a2a.ErrExtensionSupportRequired, code: -32008},
	{err: a2a.ErrVersionNotSupported, code: -32009},
	{err: a2a.ErrUnauthenticated, code: -31401},
	{err: a2a.ErrUnauthorized, code: -31403},
}

// remoteTaskFrom mirrors one SDK task.
func remoteTaskFrom(task *a2a.Task) RemoteTask {
	if task == nil {
		return RemoteTask{}
	}

	remote := RemoteTask{
		ID:        string(task.ID),
		ContextID: task.ContextID,
		State:     stateName(task.Status.State),
		Text:      boundRunes(CleanText(statusText(task.Status.Message)), remoteStatusTextRunes),
	}

	for _, artifact := range task.Artifacts {
		if artifact == nil {
			continue
		}
		remote.Results = append(remote.Results, boundRunes(CleanText(artifactText(artifact)), remoteArtifactRunes))
	}
	return remote
}

// statusText joins a message's text parts. A data part is not text, so it is ignored here.
func statusText(message *a2a.Message) string {
	if message == nil {
		return ""
	}
	texts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part == nil {
			continue
		}
		if _, ok := part.Content.(a2a.Text); ok {
			texts = append(texts, part.Text())
		}
	}
	return strings.Join(texts, "\n")
}

// artifactText renders one artifact: its text parts joined, and a data part as JSON.
func artifactText(artifact *a2a.Artifact) string {
	pieces := make([]string, 0, len(artifact.Parts))
	for _, part := range artifact.Parts {
		if part == nil {
			continue
		}
		if _, ok := part.Content.(a2a.Text); ok {
			pieces = append(pieces, part.Text())
			continue
		}
		if value := part.Data(); value != nil {
			if encoded, err := json.Marshal(value); err == nil {
				pieces = append(pieces, string(encoded))
			}
		}
	}
	return strings.Join(pieces, "\n")
}

// boundRunes cuts a value to a rune count.
func boundRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	count := 0
	for index := range value {
		if count == max {
			return value[:index]
		}
		count++
	}
	return value
}

// boundRunesBytes cuts a value to a byte count without splitting a UTF-8 sequence.
func boundRunesBytes(value string, max int) string {
	if len(value) <= max {
		return value
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}

// classifyCardAuth says what the card asks of a caller, as far as this gateway can meet it.
//
// A card with no security schemes and no requirements declares none. Otherwise the requirements are an OR of
// ANDs: the gateway can meet one when EVERY scheme that requirement names is an HTTP scheme named Bearer
// (case-insensitive) with no scopes. A card that declares schemes but no requirement that can be met, or a
// requirement naming a scheme it does not define, is unsupported. Scope lists on HTTP schemes are not a thing
// the gateway can satisfy, so a non-empty list makes that option unusable.
func classifyCardAuth(card *a2a.AgentCard) CardAuth {
	if len(card.SecuritySchemes) == 0 && len(card.SecurityRequirements) == 0 {
		return CardAuthNone
	}
	for _, option := range card.SecurityRequirements {
		if len(option) == 0 {
			continue
		}
		satisfiable := true
		for name, scopes := range option {
			scheme, defined := card.SecuritySchemes[name]
			http, isHTTP := scheme.(a2a.HTTPAuthSecurityScheme)
			if !defined || !isHTTP || !strings.EqualFold(strings.TrimSpace(http.Scheme), "bearer") || len(scopes) > 0 {
				satisfiable = false
				break
			}
		}
		if satisfiable {
			return CardAuthBearer
		}
	}
	return CardAuthUnsupported
}
