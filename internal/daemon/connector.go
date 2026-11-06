package daemon

// The connector: reaching the gateway once, and retrying the session ends that could not be
// reported.
//
// A daemon connects exactly once. A second connect would end the sessions this daemon just
// started, so there is no reconnect here: the stream has its own reconnect loop, and that
// is the only thing that redials.
//
// Until the connect succeeds, starting a session fails at once. Nothing waits and nothing
// is queued: a user who asks for a session while the gateway is unreachable is told so,
// rather than watching a prompt that never returns.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No connection has been attempted.

import (
	"context"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// connectBackoffMin is the first wait after a failed connect, doubled on each failure
	// up to connectBackoffMax. UNTESTED guesses.
	connectBackoffMin = 1 * time.Second  // UNTESTED guess
	connectBackoffMax = 15 * time.Second // UNTESTED guess

	// pendingEndEvery is how often a queued session end is retried once the link is up.
	pendingEndEvery = 15 * time.Second // UNTESTED guess
)

// connectorLink is the part of the gateway client the connector needs.
type connectorLink interface {
	Connect(ctx context.Context) (protocol.ConnectResponse, error)
}

// connector owns the one connect and the pending-end retries.
type connector struct {
	link connectorLink
	log  *logger
	// sleep waits, or gives up early when the context ends. It is a field so a test can
	// observe the waits without spending them.
	sleep func(ctx context.Context, d time.Duration) bool

	mu        sync.Mutex
	connected bool
	lastErr   string
	pending   pendingEnder
}

// newConnector builds the connector. Nothing is attempted until run.
//
// UNTESTED.
func newConnector(link connectorLink, log *logger) *connector {
	return &connector{
		link:  link,
		log:   log,
		sleep: sleepOrCancel,
	}
}

// setPending registers the retry target. The manager is built after the connector, because
// the manager needs to know whether the gateway has been reached.
func (c *connector) setPending(p pendingEnder) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = p
}

// Connected reports whether the gateway has accepted this daemon.
//
// UNTESTED.
func (c *connector) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// LastError is the most recent connect failure, or empty.
//
// UNTESTED.
func (c *connector) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

// run connects once, then retries queued ends until the context ends.
//
// UNTESTED.
func (c *connector) run(ctx context.Context) {
	delay := connectBackoffMin
	for {
		if ctx.Err() != nil {
			return
		}

		connected, err := c.link.Connect(ctx)
		if err == nil {
			c.noteConnected(connected)
			break
		}
		if ctx.Err() != nil {
			// Cancelled while the attempt was running: not a gateway failure.
			return
		}
		c.noteFailure(err)

		if !c.sleep(ctx, delay) {
			return
		}
		delay *= 2
		if delay > connectBackoffMax {
			delay = connectBackoffMax
		}
	}

	ticker := time.NewTicker(pendingEndEvery)
	defer ticker.Stop()

	// Once, immediately: an end that failed during the connect is already waiting.
	c.retryPending(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.retryPending(ctx)
		}
	}
}

// noteConnected records a successful connect.
func (c *connector) noteConnected(connected protocol.ConnectResponse) {
	c.mu.Lock()
	c.connected = true
	c.lastErr = ""
	c.mu.Unlock()

	c.log.logf("connected to the gateway as %s (%d agents in its directory)", connected.Host, len(connected.Directory))
}

// noteFailure records a failed connect.
func (c *connector) noteFailure(err error) {
	text := err.Error()

	c.mu.Lock()
	c.lastErr = text
	c.mu.Unlock()

	c.log.logf("the gateway has not been reached yet: %s", text)
}

// retryPending asks the manager to try its queued ends once.
func (c *connector) retryPending(ctx context.Context) {
	c.mu.Lock()
	pending := c.pending
	c.mu.Unlock()

	if pending != nil {
		pending.retryPendingEnd(ctx)
	}
}

// sleepOrCancel waits for d, or returns false as soon as the context ends.
func sleepOrCancel(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
