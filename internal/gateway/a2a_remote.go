package gateway

// The dial-ruled HTTP client for A2A services, and the loop that mirrors their tasks.
//
// The client is the card poller's client (carddial.go, unchanged): IP literal, dial-time deny list and
// class check, no proxy, no redirects, no cookies, HTTP/1.1, per-target TLS pin, header cap. Only the
// timeouts differ in effect: A2A calls use connect 3 s and total 5 s (the card poller derives them from its
// interval, and 6.25 s gives exactly those numbers; they are untested guesses).
//
// UNTESTED.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/a2a"
)

// a2aClientInterval yields connect 3 s and total 5 s from cardTimeouts.
const a2aClientInterval = 6250 * time.Millisecond

// a2aClientFactoryFor builds the a2a.ClientFactory over this database. It re-applies the owner-facing
// transport rules before it builds anything, so a stored row that no longer passes cannot be dialled, and it
// builds the client from the ORIGIN only: the dial rules judge an address, the SDK adds the endpoint path.
//
// This factory is the ONLY reader of a registration's outbound secret. For a Bearer registration it wraps the
// bound client's transport so every request carries `Authorization: Bearer <secret>`; the client is bound to
// that one destination and refuses redirects, so the header cannot go anywhere else. A destination with no Auth
// (the public card fetch at registration) carries no credential at all.
func a2aClientFactoryFor(store *Store) a2a.ClientFactory {
	return func(ctx context.Context, dest a2a.Destination) (*http.Client, error) {
		if err := a2a.CheckDestination(dest.URL, dest.Fingerprint, dest.InsecureHTTP); err != nil {
			return nil, err
		}
		parsed, err := url.Parse(dest.URL)
		if err != nil {
			return nil, a2a.ErrBadURL
		}
		origin := url.URL{Scheme: parsed.Scheme, Host: parsed.Host}
		// insecureHTTP is false on purpose: CheckDestination above already enforced the A2A rule (a LAN http
		// destination needs the owner's opt-in), and the card-target parser rejects the flag for http because
		// it does not need one.
		target, err := ParseCardTarget(origin.String(), dest.Fingerprint, false)
		if err != nil {
			return nil, a2a.ErrBadURL
		}
		client, err := newCardClient(target, a2aClientInterval)
		if err != nil {
			return nil, err
		}
		if dest.Auth != a2a.AuthBearer {
			return client, nil
		}
		secret, err := store.A2AStore().OutboundSecret(ctx, dest.ID)
		if err != nil || secret == "" {
			// Fixed text: nothing about why, and never the secret.
			return nil, errors.New("the outbound credential is not available")
		}
		client.Transport = &bearerTransport{base: client.Transport, host: parsed.Host, header: "Bearer " + secret}
		return client, nil
	}
}

// bearerTransport adds the registration's Authorization header to requests for ITS host only, on a copy of
// the request, and removes the header's value from any error before the error leaves.
type bearerTransport struct {
	base   http.RoundTripper
	host   string
	header string
}

func (t *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != t.host {
		return nil, errors.New("the request is not for the registered service")
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", t.header)
	response, err := t.base.RoundTrip(clone)
	if err != nil {
		// A transport error can quote the request; it does not leave this function.
		return nil, errors.New("the request to the service failed")
	}
	return response, nil
}

// destinationOf is where a registered participant lives.
func destinationOf(p a2a.Participant) a2a.Destination {
	return a2a.Destination{ID: p.ID, URL: p.URL, Fingerprint: p.Fingerprint, InsecureHTTP: p.InsecureHTTP, Auth: p.Auth}
}

// dispatchToService forwards a message to an external service and mirrors the task it returns. It is
// never retried: a lost or invalid reply is uncertain, and uncertainty is recorded, not replayed.
func (d a2aDispatcher) dispatchToService(ctx context.Context, request a2a.Dispatch) error {
	s := d.server
	participant, found, err := s.store.A2AStore().GetParticipant(ctx, request.Task.Target.ID)
	if err != nil {
		return err
	}
	if !found || participant.Revoked() || participant.URL == "" {
		return errors.Join(a2a.ErrNotDelivered, errors.New("the service is not registered"))
	}
	origin, _, err := d.sender(ctx, request.From)
	if err != nil {
		return err
	}
	messageID, err := randomID()
	if err != nil {
		return errors.Join(a2a.ErrNotDelivered, errors.New("no message id"))
	}

	// A continuation answers the REMOTE task, so its remote id comes from the stored record.
	remoteTaskID := ""
	if request.Continuation {
		if stored, ok, err := s.store.A2AStore().GetTask(ctx, request.Task.ID); err == nil && ok {
			remoteTaskID = stored.RemoteTaskID
		}
		if remoteTaskID == "" {
			return errors.Join(a2a.ErrNotDelivered, errors.New("the remote task is not known yet"))
		}
	}

	remote := &a2a.Remote{Factory: a2aClientFactoryFor(s.store)}
	result, err := remote.Send(ctx, destinationOf(participant), a2a.RemoteSend{
		MessageID: messageID, Text: request.Text, RemoteTaskID: remoteTaskID, Origin: origin,
	})
	var refused *a2a.RemoteError
	switch {
	case errors.As(err, &refused):
		// The service read it and refused: a definite non-acceptance.
		return errors.Join(a2a.ErrNotDelivered, errors.New("the service refused the message"))
	case errors.Is(err, a2a.ErrServiceAuth):
		// A definite refusal: the task fails with a reason the owner can act on, and an observation says why.
		// Never retried; the text names neither the secret nor the header.
		_ = s.a2aObserve(ctx, request.Task.ID, a2a.ObsAuth, "refused")
		return a2a.NotDelivered("the service refused the gateway's credential (HTTP 401/403); re-register the service and update its token")
	case errors.Is(err, a2a.ErrNotDelivered):
		return err
	case err != nil:
		_ = s.a2aObserve(ctx, request.Task.ID, a2a.ObsDelivery, "uncertain")
		return err
	}
	_ = s.a2aObserve(ctx, request.Task.ID, a2a.ObsDelivery, "stored")

	// The executor yielded the task a moment ago and the SDK writes its row asynchronously.
	if err := s.waitForTask(ctx, request.Task.ID); err != nil {
		return err
	}
	_, err = s.a2aService.ApplyRemote(ctx, request.Task.ID, result)
	return err
}

// waitForTask waits briefly for the SDK to have written a task's row.
func (s *Server) waitForTask(ctx context.Context, id string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, found, err := s.store.A2AStore().GetTask(ctx, id); err != nil {
			return err
		} else if found {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("a2a: the task row was not written in time")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

const (
	// mirrorTick is how often the mirror looks for tasks that are due.
	mirrorTick = time.Second
	// mirrorFastFor and mirrorFastEvery: a young task is read every 5 s; mirrorSlowEvery after that.
	mirrorFastFor   = 5 * time.Minute
	mirrorFastEvery = 5 * time.Second
	mirrorSlowEvery = 30 * time.Second
	// mirrorGiveUpAfter: a task still open after a day is no longer polled; an observation says so and
	// the owner can delete or inspect it. Untested guesses, like the intervals.
	mirrorGiveUpAfter = 24 * time.Hour
	// mirrorConcurrency bounds polls in flight across all tasks.
	mirrorConcurrency = 4
)

// a2aMirror reads external services' tasks while they are not final. One poll per task at a time, a bounded
// number across tasks, and the dial rules apply. A lost connection is an observation, never a state.
type a2aMirror struct {
	server *Server
	remote *a2a.Remote

	mu       sync.Mutex
	next     map[string]time.Time // task id -> next poll
	inFlight map[string]bool
	lost     map[string]bool // task ids whose connection is currently recorded as lost
	stopped  map[string]bool // task ids whose mirror was given up
	// authRefused: task ids whose last read was refused for credential, so the observation is not repeated
	// every five seconds.
	authRefused map[string]bool
	slots       chan struct{}
	wg          sync.WaitGroup
}

// StartA2AMirror runs the mirror until the returned stop function is called.
func (s *Server) StartA2AMirror(parent context.Context) (stop func()) {
	mirror := newA2AMirror(s, a2aClientFactoryFor(s.store))
	ctx, cancel := context.WithCancel(parent)
	mirror.wg.Add(1)
	go func() {
		defer mirror.wg.Done()
		ticker := time.NewTicker(mirrorTick)
		defer ticker.Stop()
		for {
			mirror.scan(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		mirror.wg.Wait()
	}
}

func newA2AMirror(s *Server, factory a2a.ClientFactory) *a2aMirror {
	return &a2aMirror{
		server: s, remote: &a2a.Remote{Factory: factory},
		next: map[string]time.Time{}, inFlight: map[string]bool{}, lost: map[string]bool{}, stopped: map[string]bool{}, authRefused: map[string]bool{},
		slots: make(chan struct{}, mirrorConcurrency),
	}
}

func (m *a2aMirror) scan(ctx context.Context) {
	tasks, err := m.server.store.A2AStore().OpenServiceTasks(ctx)
	if err != nil {
		return
	}
	now := m.server.opts.Now()
	live := map[string]bool{}
	for _, task := range tasks {
		if ctx.Err() != nil {
			return
		}
		live[task.ID] = true
		if task.RemoteTaskID == "" {
			continue // not accepted by the service yet: nothing to read
		}
		m.mu.Lock()
		due := !m.inFlight[task.ID] && !m.stopped[task.ID] && !now.Before(m.next[task.ID])
		age := now.Sub(task.CreatedAt)
		if due && age > mirrorGiveUpAfter {
			m.stopped[task.ID] = true
			due = false
			go func(id string) {
				_ = m.server.a2aObserve(context.WithoutCancel(ctx), id, a2a.ObsRemoteState, "mirror stopped after 24 hours")
			}(task.ID)
		}
		if due {
			// Capacity is taken BEFORE anything is launched or marked: a task that finds no free slot
			// stays due and is picked up by a later scan, so waiters never pile up behind the limit.
			select {
			case m.slots <- struct{}{}:
			default:
				due = false
			}
		}
		if due {
			m.inFlight[task.ID] = true
			every := mirrorFastEvery
			if age > mirrorFastFor {
				every = mirrorSlowEvery
			}
			m.next[task.ID] = now.Add(every)
		}
		m.mu.Unlock()
		if !due {
			continue
		}
		m.wg.Add(1)
		go func(task a2a.Task) {
			defer m.wg.Done()
			defer func() { <-m.slots }()
			m.poll(ctx, task)
			m.mu.Lock()
			delete(m.inFlight, task.ID)
			m.mu.Unlock()
		}(task)
	}
	m.mu.Lock()
	for id := range m.next {
		if !live[id] {
			delete(m.next, id)
			delete(m.lost, id)
			delete(m.stopped, id)
			delete(m.authRefused, id)
		}
	}
	m.mu.Unlock()
}

func (m *a2aMirror) poll(ctx context.Context, task a2a.Task) {
	s := m.server
	participant, found, err := s.store.A2AStore().GetParticipant(ctx, task.Target.ID)
	if err != nil || !found || participant.Revoked() {
		return
	}
	remote, err := m.remote.Get(ctx, destinationOf(participant), task.RemoteTaskID)

	m.mu.Lock()
	wasLost := m.lost[task.ID]
	m.mu.Unlock()

	var refused *a2a.RemoteError
	switch {
	case errors.Is(err, a2a.ErrServiceAuth):
		// The service refused our credential on a read: an observation only, never a state change, and
		// recorded once until it changes.
		m.markConnected(ctx, task.ID, wasLost)
		m.mu.Lock()
		already := m.authRefused[task.ID]
		m.authRefused[task.ID] = true
		m.mu.Unlock()
		if !already {
			_ = s.a2aObserve(ctx, task.ID, a2a.ObsAuth, "refused")
		}
	case errors.As(err, &refused):
		// The service answered, so the connection is fine; it just does not know the task (or refuses).
		_ = s.a2aObserve(ctx, task.ID, a2a.ObsRemoteState, "the service answered with an error to GetTask")
		m.markConnected(ctx, task.ID, wasLost)
	case err != nil:
		if !wasLost {
			_ = s.a2aObserve(ctx, task.ID, a2a.ObsConnection, "lost")
			m.mu.Lock()
			m.lost[task.ID] = true
			m.mu.Unlock()
		}
	default:
		m.markConnected(ctx, task.ID, wasLost)
		m.mu.Lock()
		if m.authRefused[task.ID] {
			delete(m.authRefused, task.ID)
			defer func() { _ = s.a2aObserve(ctx, task.ID, a2a.ObsAuth, "accepted again") }()
		}
		m.mu.Unlock()
		_, _ = s.a2aService.ApplyRemote(ctx, task.ID, remote)
	}
}

func (m *a2aMirror) markConnected(ctx context.Context, taskID string, wasLost bool) {
	if !wasLost {
		return
	}
	m.mu.Lock()
	delete(m.lost, taskID)
	m.mu.Unlock()
	_ = m.server.a2aObserve(ctx, taskID, a2a.ObsConnection, "connected")
	_ = m.server.a2aObserve(ctx, taskID, a2a.ObsReconciled, "by the next successful read")
}
