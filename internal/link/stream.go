package link

// The runtime's stream: one long-lived GET /v1/stream, a heartbeat loop, a
// watchdog, and a redial loop with backoff.
//
// The gateway writes NDJSON frames; this reader is the only place that decides
// whether the link is up, and it tells the router through Receiver.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No frame has been read.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// streamPath is the one stream route.
	streamPath = "/v1/stream"

	// streamMediaType is the answer that must come back: NDJSON.
	streamMediaType = "application/x-ndjson"

	// frameReadBuffer is the starting read buffer. Frames are small; a long one is
	// accumulated by appending.
	frameReadBuffer = 4 << 10

	// frameAcceptedMediaPrefix is enough of a Content-Type to accept a charset
	// parameter.
	frameAcceptedMediaPrefix = "application/x-ndjson"

	// refusalBodyBytes is how much of a refusal's body is read to find the
	// gateway's own message.
	refusalBodyBytes = 4 << 10
)

// errFrameTooLarge reports a frame line over the cap.
var errFrameTooLarge = errors.New("the gateway sent a frame that was too large")

// Run keeps one stream open until the context ends or Close is called.
//
// It returns nil in every termination: a cancelled context and a Close are both
// normal shutdown, and every failure on the way is reported through LinkState
// instead. A nil Receiver is a programming error, documented and refused, because
// there would be nobody to tell anything to.
//
// UNTESTED.
func (c *Client) Run(ctx context.Context, recv Receiver) {
	if recv == nil {
		return
	}
	if !c.ran.CompareAndSwap(false, true) {
		// A second Run is a no-op: one stream per runtime.
		return
	}
	if c.closed.Load() {
		return
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.mu.Lock()
	c.runCancel = cancel
	c.runDone = done
	c.mu.Unlock()
	// Close may have run between the check above and publishing the cancel: it
	// would then have found nothing to cancel, so look again.
	if c.closed.Load() {
		cancel()
	}

	defer func() {
		close(done)
		c.mu.Lock()
		c.runCancel = nil
		c.mu.Unlock()
		cancel()
	}()

	state := &stateEmitter{client: c, recv: recv}
	state.emit(protocol.ConnConnecting, "")

	backoff := c.opts.BackoffMin
	for {
		if runCtx.Err() != nil {
			return
		}

		outcome := c.runOnce(runCtx, recv, state)

		switch {
		case outcome.refused != "":
			state.emit(protocol.ConnRefused, outcome.refused)
		case outcome.cause != "":
			state.emit(protocol.ConnReconnecting, outcome.cause)
		default:
			state.emit(protocol.ConnReconnecting, "the gateway closed the connection")
		}

		if outcome.delivered {
			// Something worked: start the backoff over, so a single drop does not
			// leave the runtime waiting 15 s to redial.
			backoff = c.opts.BackoffMin
		}

		if err := c.wait(runCtx, backoff); err != nil {
			return
		}
		backoff = minDuration(backoff*2, c.opts.BackoffMax)
	}
}

// streamOutcome is what one connection attempt produced.
type streamOutcome struct {
	// delivered is true when at least one frame arrived, which resets the backoff.
	delivered bool
	// cause is the reconnecting reason, already short and sanitised.
	cause string
	// refused is the refusal reason when the gateway answered the request with a
	// refusal; it is empty otherwise.
	refused string
}

// runOnce performs one connection attempt and reads frames until it ends.
//
// The watchdog is created BEFORE the request is issued: a gateway that accepts the
// connection and never answers is then closed after DeadAfter and reported as a
// reconnect, rather than waiting on a header timeout the transport does not have.
//
// UNTESTED.
func (c *Client) runOnce(ctx context.Context, recv Receiver, state *stateEmitter) streamOutcome {
	connCtx, cancelConn := context.WithCancel(ctx)

	var causeMu sync.Mutex
	connCause := ""
	connRefused := ""
	setCause := func(cause string) {
		causeMu.Lock()
		if connCause == "" {
			connCause = cause
		}
		causeMu.Unlock()
	}
	// setRefused records that the gateway no longer accepts this host (a heartbeat
	// answered 401 or 403). It is reported as `refused`, not as a plain reconnect.
	setRefused := func(reason string) {
		causeMu.Lock()
		if connRefused == "" {
			connRefused = reason
		}
		causeMu.Unlock()
	}
	refusedNow := func() string {
		causeMu.Lock()
		defer causeMu.Unlock()
		return connRefused
	}

	// One watchdog per connection, and it owns this connection's cancel. A late
	// fire can therefore only ever end the connection it belongs to, which is why
	// no generation number is needed on the client itself.
	watchdog := time.AfterFunc(c.opts.DeadAfter, func() {
		setCause("no frame for " + c.opts.DeadAfter.String())
		cancelConn()
	})

	var heartbeatWG sync.WaitGroup
	started := make(chan struct{})

	defer func() {
		watchdog.Stop()
		cancelConn()
		heartbeatWG.Wait()
	}()

	outcome := streamOutcome{}

	request, err := newStreamRequest(connCtx, c.baseURL+streamPath, c.pairing.Credential)
	if err != nil {
		outcome.cause = "the stream request could not be built"
		return outcome
	}

	response, err := c.streamHTTP.Do(request)
	if err != nil {
		causeMu.Lock()
		stalled := connCause
		causeMu.Unlock()
		if stalled != "" {
			outcome.cause = stalled
			return outcome
		}
		outcome.cause = causeKind(err)
		return outcome
	}
	defer func() {
		_ = response.Body.Close()
	}()

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		if !mediaTypeIs(response.Header.Get("Content-Type"), streamMediaType) {
			outcome.cause = "the gateway answered the stream request without NDJSON (status 200)"
			return outcome
		}
	case response.StatusCode >= 300 && response.StatusCode < 500:
		// Read a little of the body: the gateway's own WireError message (a version
		// mismatch, an unpaired credential) is what the user needs to see.
		refusalBody, _ := io.ReadAll(io.LimitReader(response.Body, refusalBodyBytes))
		outcome.refused = refusalFrom(response.StatusCode, refusalBody).Error()
		return outcome
	default:
		outcome.cause = "the gateway could not serve the stream (status " + itoa(response.StatusCode) + ")"
		return outcome
	}

	heartbeatWG.Add(1)
	go c.heartbeatLoop(connCtx, &heartbeatWG, started, setRefused, cancelConn)

	reader := bufio.NewReaderSize(response.Body, frameReadBuffer)
	first := true

	for {
		line, err := readFrameLine(reader, c.opts.MaxFrameBytes)
		if err != nil {
			if reason := refusedNow(); reason != "" {
				// The heartbeat found this host unpaired and ended the connection.
				outcome.refused = reason
				return outcome
			}
			switch {
			case errors.Is(err, errFrameTooLarge):
				outcome.cause = "the gateway sent a frame that was too large"
			default:
				causeMu.Lock()
				stalled := connCause
				causeMu.Unlock()
				if stalled != "" {
					outcome.cause = stalled
				} else {
					outcome.cause = "the gateway closed the connection"
				}
			}
			return outcome
		}

		if len(line) == 0 {
			// An empty line is not a frame.
			continue
		}

		// Every frame - ping, offer, report, directory or one this build does not
		// know - resets the watchdog: the connection is alive.
		watchdog.Reset(c.opts.DeadAfter)

		var frame protocol.StreamFrame
		if err := json.Unmarshal(line, &frame); err != nil {
			outcome.cause = "the gateway sent something that was not JSON"
			return outcome
		}

		if first {
			first = false
			outcome.delivered = true
			state.emit(protocol.ConnConnected, "")
			recv.LinkUp()
			close(started)
		}

		switch frame.Type {
		case protocol.FrameDirectory:
			peers := make([]string, 0, len(frame.Directory))
			for _, entry := range frame.Directory {
				peers = append(peers, entry.Address)
			}
			// The receiver hears the peers first, then the state that carries them.
			recv.LinkDirectory(peers)
			state.setPeers(peers)
		case protocol.FrameOffer:
			if frame.Offer != nil {
				recv.Receive(*frame.Offer)
			}
		case protocol.FrameReport:
			if frame.Report != nil {
				recv.LinkReport(*frame.Report)
			}
		case protocol.FramePing:
			// Nothing to do: the reset above is the whole point.
		default:
			// Forward compatibility: an unknown frame type still proves the
			// connection is alive, and nothing else.
		}
	}
}

// heartbeatLoop posts a heartbeat every HeartbeatEvery once the first frame has
// arrived. It ends with the connection, and runOnce waits for it.
//
// A transport error or a 5xx changes nothing: the watchdog decides whether the
// connection is dead. A 401 or 403 means this host was unpaired, which is a refusal,
// so the connection ends and Run reports it.
func (c *Client) heartbeatLoop(ctx context.Context, wg *sync.WaitGroup, started <-chan struct{}, setRefused func(string), cancelConn context.CancelFunc) {
	defer wg.Done()

	select {
	case <-started:
	case <-ctx.Done():
		return
	}

	// beat posts one heartbeat and reports whether the loop should end (the gateway
	// said this host was unpaired). A transport failure or a 5xx changes nothing: the
	// stream's watchdog is the only judge of whether the connection is alive.
	beat := func() (stop bool) {
		beatCtx, cancel := context.WithTimeout(ctx, heartbeatRequestTimeout)
		err := c.Heartbeat(beatCtx)
		cancel()

		if err == nil {
			return false
		}
		refusal, ok := Refusal(err)
		if !ok {
			return false
		}
		if refusal.Status == 401 || refusal.Status == 403 {
			setRefused(refusal.Error())
			cancelConn()
			return true
		}
		return false
	}

	// The first beat is immediate. The gateway counts a host live only if it has
	// heard a heartbeat within 15 s, and nothing else sets that time, so waiting one
	// interval would leave a newly connected host reading as not live for a few
	// seconds while its stream is already open, and a send to it in that window would
	// get a definite destination_unreachable.
	if beat() {
		return
	}

	ticker := time.NewTicker(c.opts.HeartbeatEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if beat() {
				return
			}
		}
	}
}

// newStreamRequest builds the stream request.
func newStreamRequest(ctx context.Context, url, credential string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set(protocol.WireVersionHeader, itoa(protocol.WireVersion))
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Accept", streamMediaType)
	return request, nil
}

// readFrameLine reads one newline-terminated frame, accumulating segments up to the
// cap. The newline is trimmed; an empty line comes back with length zero.
func readFrameLine(reader *bufio.Reader, maxBytes int) ([]byte, error) {
	line := make([]byte, 0, 128)

	for {
		segment, err := reader.ReadSlice('\n')
		line = append(line, segment...)

		if maxBytes > 0 && len(line) > maxBytes {
			return nil, errFrameTooLarge
		}

		switch {
		case err == nil:
			return bytes.TrimSuffix(line, []byte("\n")), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err != nil:
			return nil, err
		}
	}
}

// mediaTypeIs reports whether a Content-Type's media type matches, ignoring
// parameters such as charset.
func mediaTypeIs(contentType, mediaType string) bool {
	trimmed := strings.TrimSpace(contentType)
	if trimmed == "" {
		return false
	}
	media, _, _ := strings.Cut(trimmed, ";")
	return strings.EqualFold(strings.TrimSpace(media), mediaType)
}

// itoa renders a small int without importing strconv for one call site.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}

	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

// minDuration returns the smaller of two durations.
func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// stateEmitter is the only place that calls LinkState, so "emit when something
// changed" is decided once.
type stateEmitter struct {
	client *Client
	recv   Receiver

	lastState protocol.ConnState
	lastErr   string
	lastPeers string
	haveLast  bool
	since     time.Time
	peers     []string
}

// emit reports a state, unless the state, the reason and the peers are all
// unchanged. Since stays fixed while state and reason are unchanged, so the UI can
// say how long the current state has lasted.
func (e *stateEmitter) emit(state protocol.ConnState, reason string) {
	joined := strings.Join(e.peers, ",")
	if e.haveLast && state == e.lastState && reason == e.lastErr && joined == e.lastPeers {
		return
	}

	if !e.haveLast || state != e.lastState || reason != e.lastErr {
		e.since = e.client.opts.Now()
	}

	e.lastState, e.lastErr, e.lastPeers, e.haveLast = state, reason, joined, true
	e.recv.LinkState(protocol.ConnectionView{
		State:   state,
		Host:    e.client.Host(),
		Gateway: e.client.GatewayAddr(),
		Since:   e.since,
		Err:     reason,
		Peers:   append([]string(nil), e.peers...),
		// Set on every emit, not only while connected: the saved transport is a fact
		// about this run, and the UI reads it only for the connected label.
		LANPlainHTTP: e.client.pairing.InsecureHTTP,
	})
}

// setPeers records a new directory and re-emits the current state, so the UI sees
// the peers. It emits only while the connection is connected: during reconnecting
// the view already carries the last known peers.
func (e *stateEmitter) setPeers(peers []string) {
	e.peers = peers
	if e.haveLast && e.lastState == protocol.ConnConnected {
		e.emit(protocol.ConnConnected, "")
	}
}
