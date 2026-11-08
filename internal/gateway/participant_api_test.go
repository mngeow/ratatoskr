package gateway

// Tests for the participant API routes, the inbox hub and the shared send routine. Written, never run
// (GR-3): UNTESTED. They drive ParticipantHandler and the host Handler directly with httptest recorders;
// no listener is bound here. The shutdown cases at the end bind loopback sockets.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// papi is a store, a server, two participants and one host ("vmb") with a session and an open stream.
type papi struct {
	t          *testing.T
	store      *Store
	server     *Server
	piLab      string // credential of participant "pi-lab"
	lab2       string // credential of participant "lab-2"
	hostCred   string // credential of host "vmb"
	hostFrames chan protocol.StreamFrame
}

func newPAPI(t *testing.T, ackWait time.Duration) *papi {
	t.Helper()
	store, _ := openStore(t, false)
	now := time.Now()
	p := &papi{t: t, store: store, server: NewServer(store, Options{AckWait: ackWait})}
	p.piLab = pairParticipant(t, store, "pi-lab", now)
	p.lab2 = pairParticipant(t, store, "lab-2", now)
	p.hostCred = pairHostFrom(t, store, "vmb", now)

	ctx := context.Background()
	if _, err := store.AddSession(ctx, "vmb", "claude", now, randomID); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchHeartbeat(ctx, "vmb", now); err != nil {
		t.Fatal(err)
	}
	p.hostFrames = make(chan protocol.StreamFrame, 8)
	p.server.hub.attach("vmb", &stream{host: "vmb", frames: p.hostFrames, cancel: func() {}, done: make(chan struct{})})
	return p
}

// call sends one request to the participant handler.
func (p *papi) call(method, path, credential, body string) *httptest.ResponseRecorder {
	p.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	recorder := httptest.NewRecorder()
	p.server.ParticipantHandler().ServeHTTP(recorder, request)
	return recorder
}

// hostCall sends one request to the host handler with the protocol header.
func (p *papi) hostCall(method, path, credential, body string) *httptest.ResponseRecorder {
	p.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set(protocol.WireVersionHeader, "2")
	request.Header.Set("Authorization", "Bearer "+credential)
	recorder := httptest.NewRecorder()
	p.server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func decodeJSON[T any](t *testing.T, recorder *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("body %q: %v", recorder.Body.String(), err)
	}
	return value
}

// register registers a session and returns its id.
func (p *papi) register(credential, name string) string {
	p.t.Helper()
	recorder := p.call(http.MethodPost, protocol.ParticipantSessionsPath, credential, `{"name":"`+name+`"}`)
	if recorder.Code != http.StatusOK {
		p.t.Fatalf("register %s: %d %s", name, recorder.Code, recorder.Body.String())
	}
	return decodeJSON[protocol.ParticipantAddSessionResponse](p.t, recorder).Session.SessionID
}

// poll starts a long poll in the background.
func (p *papi) poll(credential, query string) <-chan *httptest.ResponseRecorder {
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- p.call(http.MethodGet, protocol.ParticipantInboxPath+query, credential, "")
	}()
	return result
}

func sendBody(id, from, to, body string) string {
	return `{"id":"` + id + `","from":"` + from + `","to":"` + to + `","body":"` + body + `"}`
}

func within(t *testing.T, what string, c <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case recorder := <-c:
		return recorder
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// TestParticipantDiscoveryAndTheContractCheck.
func TestParticipantDiscoveryAndTheContractCheck(t *testing.T) {
	p := newPAPI(t, time.Second)

	recorder := p.call(http.MethodGet, protocol.ParticipantPathPrefix, "", "")
	if recorder.Code != http.StatusOK || recorder.Header().Get(protocol.ParticipantContractHeader) != "1.0" {
		t.Fatalf("discovery: %d %v", recorder.Code, recorder.Header())
	}
	discovery := decodeJSON[protocol.ParticipantDiscovery](t, recorder)
	if discovery.Contract != "1.0" || len(discovery.Versions) != 1 || discovery.Versions[0] != "1" || discovery.Now.IsZero() {
		t.Fatalf("discovery = %+v", discovery)
	}

	for declared, want := range map[string]int{"1.0": 200, "1.9": 200, "1": 200, "2.0": 400, "0.9": 400, "x": 400} {
		request := httptest.NewRequest(http.MethodGet, protocol.ParticipantPathPrefix, nil)
		request.Header.Set(protocol.ParticipantContractHeader, declared)
		recorder := httptest.NewRecorder()
		p.server.ParticipantHandler().ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("declared %q: %d, want %d", declared, recorder.Code, want)
		}
		if want == 400 && decodeJSON[protocol.WireError](t, recorder).Code != protocol.WireErrUnsupportedContract {
			t.Fatalf("declared %q: %s", declared, recorder.Body.String())
		}
	}
}

// TestParticipantPairingAndAuthentication: pair, then the credential works on participant routes and on no
// host route; every authentication failure is the same 401.
func TestParticipantPairingAndAuthentication(t *testing.T) {
	p := newPAPI(t, time.Second)
	now := time.Now()

	code, err := p.store.CreateParticipantCode(context.Background(), "fresh", now)
	if err != nil {
		t.Fatal(err)
	}
	recorder := p.call(http.MethodPost, protocol.ParticipantPairPath, "", `{"code":"`+code+`","participant":"fresh"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pair: %d %s", recorder.Code, recorder.Body.String())
	}
	paired := decodeJSON[protocol.ParticipantPairResponse](t, recorder)
	if paired.Participant != "fresh" || paired.Credential == "" {
		t.Fatalf("pair response = %+v", paired)
	}
	// The code is single use, and a wrong one is the 400 of section 8.
	for _, body := range []string{`{"code":"` + code + `"}`, `{"code":"NOSUCHCODE000"}`, `{"code":"` + code + `","participant":"other"}`} {
		recorder = p.call(http.MethodPost, protocol.ParticipantPairPath, "", body)
		if recorder.Code != http.StatusBadRequest || decodeJSON[protocol.WireError](t, recorder).Code != protocol.WireErrBadCode {
			t.Fatalf("pair %s: %d %s", body, recorder.Code, recorder.Body.String())
		}
	}

	if recorder := p.call(http.MethodGet, protocol.ParticipantDirectoryPath, paired.Credential, ""); recorder.Code != http.StatusOK {
		t.Fatalf("directory with the new credential: %d", recorder.Code)
	}

	// One 401 for no credential, an unknown one, a host's, and a revoked participant's.
	if _, err := p.store.RevokeParticipant(context.Background(), "lab-2", now); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, credential := range []string{"", "not-a-credential", p.hostCred, p.lab2} {
		recorder := p.call(http.MethodGet, protocol.ParticipantDirectoryPath, credential, "")
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("credential %q: %d", credential, recorder.Code)
		}
		bodies = append(bodies, recorder.Body.String())
	}
	for _, body := range bodies[1:] {
		if body != bodies[0] {
			t.Fatalf("authentication failures differ: %q vs %q", body, bodies[0])
		}
	}

	// A participant credential works on no host route.
	if recorder := p.hostCall(http.MethodPost, protocol.ConnectPath, p.piLab, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a participant credential on a host route: %d", recorder.Code)
	}
}

// TestParticipantSessions: register, the limit, bad names, end.
func TestParticipantSessions(t *testing.T) {
	p := newPAPI(t, time.Second)

	recorder := p.call(http.MethodPost, protocol.ParticipantSessionsPath, p.piLab, `{"name":"coder"}`)
	registered := decodeJSON[protocol.ParticipantAddSessionResponse](t, recorder)
	if recorder.Code != 200 || registered.Participant != "pi-lab" || registered.Session.Address != "coder@pi-lab" || registered.Session.SessionID == "" {
		t.Fatalf("register: %d %+v", recorder.Code, registered)
	}
	found := false
	for _, entry := range registered.Directory {
		found = found || entry.Address == "coder@pi-lab"
	}
	if !found || registered.Directory == nil {
		t.Fatalf("the directory does not list the new session: %+v", registered.Directory)
	}

	if recorder := p.call(http.MethodPost, protocol.ParticipantSessionsPath, p.piLab, `{"name":"Bad Name"}`); recorder.Code != 400 {
		t.Fatalf("a bad name: %d", recorder.Code)
	}
	for i := 2; i <= protocol.ParticipantMaxSessions; i++ {
		p.register(p.piLab, "worker-"+string(rune('0'+i)))
	}
	recorder = p.call(http.MethodPost, protocol.ParticipantSessionsPath, p.piLab, `{"name":"worker-9"}`)
	if recorder.Code != http.StatusConflict || decodeJSON[protocol.WireError](t, recorder).Code != protocol.WireErrTooManySessions {
		t.Fatalf("the ninth session: %d %s", recorder.Code, recorder.Body.String())
	}

	if recorder := p.call(http.MethodDelete, protocol.ParticipantSessionsPath+"/coder", p.piLab, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("end one: %d", recorder.Code)
	}
	if recorder := p.call(http.MethodDelete, protocol.ParticipantSessionsPath+"/coder", p.piLab, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("ending one that is not open: %d", recorder.Code)
	}
	if recorder := p.call(http.MethodDelete, protocol.ParticipantSessionsPath, p.piLab, ""); recorder.Code != http.StatusNoContent {
		t.Fatalf("end all: %d", recorder.Code)
	}
	if row, _, _ := p.store.GetParticipant(context.Background(), "pi-lab"); row.OpenSessions != 0 {
		t.Fatalf("%d sessions still open", row.OpenSessions)
	}
}

// TestParticipantSendRefusals: a bare `to`, a `from` that is not an open session, an unknown name.
func TestParticipantSendRefusals(t *testing.T) {
	p := newPAPI(t, time.Second)
	p.register(p.piLab, "coder")

	recorder := p.call(http.MethodPost, protocol.ParticipantMessagesPath, p.piLab, sendBody("m1", "coder", "claude", "hi"))
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "invalid to") {
		t.Fatalf("a bare to: %d %s", recorder.Code, recorder.Body.String())
	}
	recorder = p.call(http.MethodPost, protocol.ParticipantMessagesPath, p.piLab, sendBody("m2", "ghost", "claude@vmb", "hi"))
	if recorder.Code != 400 {
		t.Fatalf("a from with no session: %d", recorder.Code)
	}
	// A body field never sets an origin: the extra "participant" field is ignored.
	recorder = p.call(http.MethodPost, protocol.ParticipantMessagesPath, p.piLab, `{"id":"m3","from":"coder","to":"nobody@vmb","body":"hi","participant":"lab-2"}`)
	response := decodeJSON[protocol.SendResponse](t, recorder)
	if recorder.Code != 200 || response.Result != protocol.SendUnknownRecipient || len(response.Directory) == 0 {
		t.Fatalf("an unknown name: %d %+v", recorder.Code, response)
	}
	row, _, err := p.store.Message(context.Background(), "m3")
	if err != nil || row.From != "coder@pi-lab" {
		t.Fatalf("the ledger origin = %q %v, want coder@pi-lab", row.From, err)
	}
}

// TestParticipantSendsToAHostSession: the offer reaches the host's stream with the origin the gateway set,
// and the host's ack makes the result host_stored.
func TestParticipantSendsToAHostSession(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	p.register(p.piLab, "coder")

	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- p.call(http.MethodPost, protocol.ParticipantMessagesPath, p.piLab, sendBody("m1", "coder", "claude@vmb", "please review"))
	}()

	var frame protocol.StreamFrame
	select {
	case frame = <-p.hostFrames:
	case <-time.After(3 * time.Second):
		t.Fatal("no offer reached the host stream")
	}
	if frame.Type != protocol.FrameOffer || frame.Offer == nil || frame.Offer.From != "coder@pi-lab" || frame.Offer.To != "claude" || frame.Offer.Body != "please review" {
		t.Fatalf("frame = %+v", frame)
	}

	if recorder := p.hostCall(http.MethodPost, "/v1/messages/m1/ack", p.hostCred, `{"saved":true}`); recorder.Code != http.StatusNoContent {
		t.Fatalf("host ack: %d", recorder.Code)
	}
	response := decodeJSON[protocol.SendResponse](t, within(t, "the send answer", result))
	if response.Result != protocol.SendHostStored {
		t.Fatalf("result = %+v", response)
	}
}

// TestAHostSessionSendsToAParticipantSession: the full round trip through the inbox: a parked poll receives
// the offer, the adapter acks, and the sender is told host_stored with the adapter's wording.
func TestAHostSessionSendsToAParticipantSession(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	sessionID := p.register(p.piLab, "coder")

	polled := p.poll(p.piLab, "?wait=5")
	waitFor(t, "a parked poll", func() bool { return p.server.phub.parked("pi-lab") })

	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sent <- p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("h1", "claude", "coder@pi-lab", "hello adapter"))
	}()

	inbox := decodeJSON[protocol.InboxResponse](t, within(t, "the poll answer", polled))
	if inbox.Replaced || len(inbox.Offers) != 1 {
		t.Fatalf("inbox = %+v", inbox)
	}
	offer := inbox.Offers[0]
	if offer.ID != "h1" || offer.From != "claude@vmb" || offer.To != "coder" || offer.ToSession != sessionID || offer.Body != "hello adapter" {
		t.Fatalf("offer = %+v", offer)
	}

	// Someone else cannot ack it.
	if recorder := p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/h1/ack", p.lab2, `{"saved":true}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("an ack by another participant: %d", recorder.Code)
	}
	if recorder := p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/h1/ack", p.piLab, `{"saved":true}`); recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "{}" {
		t.Fatalf("ack: %d %q", recorder.Code, recorder.Body.String())
	}
	response := decodeJSON[protocol.SendResponse](t, within(t, "the send answer", sent))
	if response.Result != protocol.SendHostStored || !strings.Contains(response.Detail, "adapter saved it") {
		t.Fatalf("send answer = %+v", response)
	}

	// A second ack is idempotent; reading is limited to the two parties.
	if recorder := p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/h1/ack", p.piLab, `{"saved":true}`); recorder.Code != http.StatusOK {
		t.Fatalf("second ack: %d", recorder.Code)
	}
	info := decodeJSON[protocol.MessageInfo](t, p.call(http.MethodGet, protocol.ParticipantMessagesPath+"/h1", p.piLab, ""))
	if info.Result != protocol.SendHostStored {
		t.Fatalf("message info = %+v", info)
	}
	if recorder := p.call(http.MethodGet, protocol.ParticipantMessagesPath+"/h1", p.lab2, ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("a third party reading: %d", recorder.Code)
	}
}

// TestAnAdapterRefusalIsSessionGone.
func TestAnAdapterRefusalIsSessionGone(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	p.register(p.piLab, "coder")
	polled := p.poll(p.piLab, "?wait=5")
	waitFor(t, "a parked poll", func() bool { return p.server.phub.parked("pi-lab") })

	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sent <- p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("h2", "claude", "coder@pi-lab", "hi"))
	}()
	within(t, "the poll", polled)

	p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/h2/ack", p.piLab, `{"saved":false,"reason":"harness is not running"}`)
	response := decodeJSON[protocol.SendResponse](t, within(t, "the send answer", sent))
	if response.Result != protocol.SendSessionGone || !strings.Contains(response.Detail, "harness is not running") {
		t.Fatalf("send answer = %+v", response)
	}
}

// TestAnOfflineParticipantFailsAtOnceAndNothingIsQueued.
func TestAnOfflineParticipantFailsAtOnceAndNothingIsQueued(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	p.register(p.piLab, "coder") // registered, but it has never polled

	started := time.Now()
	recorder := p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("o1", "claude", "coder@pi-lab", "anyone?"))
	response := decodeJSON[protocol.SendResponse](t, recorder)
	if response.Result != protocol.SendDestinationUnreachable || !strings.Contains(response.Detail, "not polling") {
		t.Fatalf("response = %+v", response)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("an offline send waited %v", time.Since(started))
	}

	// Nothing waits for a later poll.
	p.server.phub.mu.Lock()
	pending := len(p.server.phub.inboxes["pi-lab"].pendingOrNil())
	p.server.phub.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d offers are pending for an offline participant", pending)
	}
	later := decodeJSON[protocol.InboxResponse](t, p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, ""))
	if len(later.Offers) != 0 {
		t.Fatalf("a later poll received %+v", later.Offers)
	}
}

// pendingOrNil reads an inbox's pending list, tolerating a missing inbox (test helper).
func (ib *inbox) pendingOrNil() []*pendingOffer {
	if ib == nil {
		return nil
	}
	return ib.pending
}

// TestTheBoundedHandOverIsWithdrawnWhenNoPollComes: the participant is online by the window (it polled a
// moment ago) but no poll is parked, so the offer is held for the sender's wait only, then withdrawn
// atomically, and a poll that arrives later cannot receive it.
func TestTheBoundedHandOverIsWithdrawnWhenNoPollComes(t *testing.T) {
	p := newPAPI(t, 300*time.Millisecond)
	p.register(p.piLab, "coder")
	// An empty poll that has just ended: online by the 15 s window, nothing parked.
	if recorder := p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, ""); recorder.Code != 200 {
		t.Fatalf("poll: %d", recorder.Code)
	}
	if p.server.phub.parked("pi-lab") {
		t.Fatal("a poll is still parked")
	}

	recorder := p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("w1", "claude", "coder@pi-lab", "late"))
	response := decodeJSON[protocol.SendResponse](t, recorder)
	if response.Result != protocol.SendDestinationUnreachable || !strings.Contains(response.Detail, "did not poll") {
		t.Fatalf("response = %+v", response)
	}
	later := decodeJSON[protocol.InboxResponse](t, p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, ""))
	if len(later.Offers) != 0 {
		t.Fatalf("a poll after the withdrawal received %+v", later.Offers)
	}
}

// TestAPollThatArrivesDuringTheHandOverClaimsIt: online, nothing parked, the next poll takes the offer.
func TestAPollThatArrivesDuringTheHandOverClaimsIt(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	p.register(p.piLab, "coder")
	p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, "")

	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sent <- p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("g1", "claude", "coder@pi-lab", "in the gap"))
	}()
	waitFor(t, "a pending offer", func() bool {
		p.server.phub.mu.Lock()
		defer p.server.phub.mu.Unlock()
		return len(p.server.phub.inboxes["pi-lab"].pendingOrNil()) == 1
	})

	inbox := decodeJSON[protocol.InboxResponse](t, p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, ""))
	if len(inbox.Offers) != 1 || inbox.Offers[0].ID != "g1" {
		t.Fatalf("inbox = %+v", inbox)
	}
	p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/g1/ack", p.piLab, `{"saved":true}`)
	if response := decodeJSON[protocol.SendResponse](t, within(t, "the send", sent)); response.Result != protocol.SendHostStored {
		t.Fatalf("response = %+v", response)
	}
}

// TestAClaimedOfferWithNoAckIsUncertainAndLateAckCorrectsIt.
func TestAClaimedOfferWithNoAckIsUncertainAndLateAckCorrectsIt(t *testing.T) {
	p := newPAPI(t, 300*time.Millisecond)
	p.register(p.piLab, "coder")
	polled := p.poll(p.piLab, "?wait=5")
	waitFor(t, "a parked poll", func() bool { return p.server.phub.parked("pi-lab") })

	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sent <- p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("u1", "claude", "coder@pi-lab", "x"))
	}()
	within(t, "the poll", polled)
	response := decodeJSON[protocol.SendResponse](t, within(t, "the send", sent))
	if response.Result != protocol.SendDeliveryUncertain {
		t.Fatalf("response = %+v", response)
	}

	p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/u1/ack", p.piLab, `{"saved":true}`)
	row, _, _ := p.store.Message(context.Background(), "u1")
	if row.Result != string(protocol.SendHostStored) {
		t.Fatalf("the late ack left the result at %s", row.Result)
	}
}

// TestPollTimeoutAndReplacement: an empty list is the normal answer; a second poll displaces the first.
func TestPollTimeoutAndReplacement(t *testing.T) {
	p := newPAPI(t, time.Second)

	recorder := p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, "")
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"offers":[]`) || !strings.Contains(recorder.Body.String(), `"replaced":false`) {
		t.Fatalf("timeout answer: %d %s", recorder.Code, recorder.Body.String())
	}
	for _, bad := range []string{"?wait=21", "?wait=-1", "?wait=abc"} {
		if recorder := p.call(http.MethodGet, protocol.ParticipantInboxPath+bad, p.piLab, ""); recorder.Code != 400 {
			t.Fatalf("%s: %d", bad, recorder.Code)
		}
	}

	first := p.poll(p.piLab, "?wait=5")
	waitFor(t, "the first poll", func() bool { return p.server.phub.parked("pi-lab") })
	second := p.poll(p.piLab, "?wait=1")
	displaced := decodeJSON[protocol.InboxResponse](t, within(t, "the displaced poll", first))
	if !displaced.Replaced || len(displaced.Offers) != 0 {
		t.Fatalf("displaced poll = %+v", displaced)
	}
	timedOut := decodeJSON[protocol.InboxResponse](t, within(t, "the second poll", second))
	if timedOut.Replaced {
		t.Fatalf("the newer poll was marked replaced: %+v", timedOut)
	}
}

// TestStatusReports: stored with rank, an older one changes nothing, queued is refused, and a report never
// changes the result.
func TestStatusReports(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	p.register(p.piLab, "coder")
	polled := p.poll(p.piLab, "?wait=5")
	waitFor(t, "a parked poll", func() bool { return p.server.phub.parked("pi-lab") })
	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sent <- p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("s1", "claude", "coder@pi-lab", "x"))
	}()
	within(t, "the poll", polled)
	p.call(http.MethodPost, protocol.ParticipantMessagesPath+"/s1/ack", p.piLab, `{"saved":true}`)
	within(t, "the send", sent)

	path := protocol.ParticipantMessagesPath + "/s1/status"
	if recorder := p.call(http.MethodPost, path, p.piLab, `{"state":"accepted"}`); recorder.Code != 200 {
		t.Fatalf("accepted: %d", recorder.Code)
	}
	if recorder := p.call(http.MethodPost, path, p.piLab, `{"state":"pasted"}`); recorder.Code != 200 {
		t.Fatalf("out of order: %d", recorder.Code)
	}
	info := decodeJSON[protocol.MessageInfo](t, p.call(http.MethodGet, protocol.ParticipantMessagesPath+"/s1", p.piLab, ""))
	if info.Report != protocol.MessageAccepted || info.Result != protocol.SendHostStored {
		t.Fatalf("info = %+v", info)
	}
	for _, body := range []string{`{"state":"queued"}`, `{"state":"nonsense"}`} {
		if recorder := p.call(http.MethodPost, path, p.piLab, body); recorder.Code != 400 {
			t.Fatalf("%s: %d", body, recorder.Code)
		}
	}
	if recorder := p.call(http.MethodPost, path, p.lab2, `{"state":"accepted"}`); recorder.Code != 404 {
		t.Fatalf("a report by a non-recipient: %d", recorder.Code)
	}
}

// TestRevocationClosesAParkedPollWith401: the revoke is made in another process, so the poll notices it on
// its own schedule; its pending offers are dropped.
func TestRevocationClosesAParkedPollWith401(t *testing.T) {
	previous := participantRevokeCheck
	participantRevokeCheck = 20 * time.Millisecond
	t.Cleanup(func() { participantRevokeCheck = previous })

	p := newPAPI(t, time.Second)
	polled := p.poll(p.piLab, "?wait=20")
	waitFor(t, "a parked poll", func() bool { return p.server.phub.parked("pi-lab") })

	if _, err := p.store.RevokeParticipant(context.Background(), "pi-lab", time.Now()); err != nil {
		t.Fatal(err)
	}
	recorder := within(t, "the revoked poll", polled)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("revoked poll: %d %s", recorder.Code, recorder.Body.String())
	}
	if p.server.phub.parked("pi-lab") {
		t.Fatal("a revoked participant's poll is still parked")
	}
	if recorder := p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("the next request with the revoked credential: %d", recorder.Code)
	}
}

// TestTheDirectoryShowsParticipantsAndTheirLiveness: live only while polling or just after; hosts are
// unchanged.
func TestTheDirectoryShowsParticipantsAndTheirLiveness(t *testing.T) {
	p := newPAPI(t, time.Second)
	p.register(p.piLab, "coder")

	live := func() map[string]bool {
		entries := decodeJSON[protocol.ParticipantDirectoryResponse](t, p.call(http.MethodGet, protocol.ParticipantDirectoryPath, p.piLab, "")).Directory
		out := map[string]bool{}
		for _, entry := range entries {
			out[entry.Address] = entry.Live
		}
		return out
	}
	before := live()
	if live, found := before["coder@pi-lab"]; !found || live {
		t.Fatalf("before any poll: %v", before)
	}
	if !before["claude@vmb"] {
		t.Fatalf("the host session should be live: %v", before)
	}

	p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, "")
	if after := live(); !after["coder@pi-lab"] {
		t.Fatalf("after a poll: %v", after)
	}
}

// TestShutdownBeforeServeStartsAndWithParkedWork: Shutdown before Serve makes Serve return at once; a parked
// poll and a pending offer leave through the closing signal, and the participant handlers are joined.
func TestShutdownBeforeServeStartsAndWithParkedWork(t *testing.T) {
	p := newPAPI(t, 3*time.Second)
	p.server.EnableParticipantServer()
	p.register(p.piLab, "coder")

	polled := p.poll(p.piLab, "?wait=20")
	waitFor(t, "a parked poll", func() bool { return p.server.phub.parked("pi-lab") })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	recorder := within(t, "the cancelled poll", polled)
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), `"offers":[]`) {
		t.Fatalf("cancelled poll: %d %s", recorder.Code, recorder.Body.String())
	}

	// Serve after Shutdown returns without error and without serving.
	for name, serve := range map[string]func(net.Listener) error{"host": p.server.Serve, "participant": p.server.ServeParticipant} {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := serve(listener); err != nil {
			t.Fatalf("%s Serve after Shutdown: %v", name, err)
		}
	}
}

// TestAPendingOfferLeavesOnShutdown: the sender waiting for an unclaimed offer is answered
// destination_unreachable through the closing signal, not after its full wait.
func TestAPendingOfferLeavesOnShutdown(t *testing.T) {
	p := newPAPI(t, 10*time.Second)
	p.register(p.piLab, "coder")
	p.call(http.MethodGet, protocol.ParticipantInboxPath+"?wait=0", p.piLab, "") // online, nothing parked

	sent := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		sent <- p.hostCall(http.MethodPost, "/v1/messages", p.hostCred, sendBody("sd1", "claude", "coder@pi-lab", "x"))
	}()
	waitFor(t, "a pending offer", func() bool {
		p.server.phub.mu.Lock()
		defer p.server.phub.mu.Unlock()
		return len(p.server.phub.inboxes["pi-lab"].pendingOrNil()) == 1
	})

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
	response := decodeJSON[protocol.SendResponse](t, within(t, "the send", sent))
	if response.Result != protocol.SendDestinationUnreachable || time.Since(started) > 2*time.Second {
		t.Fatalf("response = %+v after %v", response, time.Since(started))
	}
}

// failingListener returns an error from Accept at once, so Serve fails.
type failingListener struct{ net.Listener }

func (failingListener) Accept() (net.Conn, error) { return nil, errFailingListener }

var errFailingListener = &net.OpError{Op: "accept", Err: net.ErrClosed}

// TestEitherServeFailingIsReportedAndShutdownStillStopsTheOther: the participant Serve fails, and a Shutdown
// afterwards stops the host Serve too. (The Run-level teardown rests on this and on Run's select; Run
// itself is not driven here.)
func TestEitherServeFailingIsReportedAndShutdownStillStopsTheOther(t *testing.T) {
	p := newPAPI(t, time.Second)
	p.server.EnableParticipantServer()

	hostListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hostDone := make(chan error, 1)
	go func() { hostDone <- p.server.Serve(hostListener) }()

	broken, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.server.ServeParticipant(failingListener{broken}); err == nil {
		t.Fatal("ServeParticipant on a failing listener returned nil")
	}
	_ = broken.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case err := <-hostDone:
		if err != nil {
			t.Fatalf("host Serve: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the host server was left running")
	}
}

// TestABlockedResponseCrossingTheSharedDeadlineIsClosedAndJoined: a participant handler that never returns on
// its own is cut when the one deadline passes; its connection is closed, Shutdown reports it, and the handler
// has ended before Shutdown's join wait gives up.
func TestABlockedResponseCrossingTheSharedDeadlineIsClosedAndJoined(t *testing.T) {
	p := newPAPI(t, time.Second)
	p.server.EnableParticipantServer()

	var ended atomic.Bool
	entered := make(chan struct{})
	p.server.participantHTTP.Handler = p.server.trackParticipantWork(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done() // returns only when the connection is closed
		ended.Store(true)
	}))

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = p.server.ServeParticipant(listener) }()

	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Get("http://" + listener.Addr().String() + "/anything")
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("the request never reached the handler")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := p.server.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown reported success with a handler still blocked")
	}
	waitFor(t, "the handler to end after the connection closed", ended.Load)
}
