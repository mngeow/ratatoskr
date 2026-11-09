// Package sidecar is the long-running half of ratatoskr-openrig: it registers the operator session, parks the
// inbox long poll, runs every offer through the pipeline of docs/implementation/openrig-bridge-contract.md
// section 8.3, serves the local socket and records why it stopped.
//
// UNTESTED. Under GR-3 nothing here has been run.
package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/gw"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/journal"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/limits"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/rigcli"
)

// Exit codes (contract section 4).
const (
	ExitOK       = 0
	ExitFailed   = 1
	ExitUsage    = 2
	ExitReplaced = 4
	ExitRevoked  = 5
)

// Fixed constants of the pipeline (contract section 8.3). Not configurable.
const (
	ackBudget       = 4 * time.Second // everything up to and including the acknowledgement
	slotDeadline    = 1 * time.Second
	readyDeadline   = 2500 * time.Millisecond
	journalDeadline = 3 * time.Second
	statusAttempts  = 3
	statusRetryGap  = time.Second
	statusMaxAge    = 24 * time.Hour
)

// Gateway is the participant API as the sidecar uses it. *gw.Client implements it.
type Gateway interface {
	Discover(ctx context.Context) (gw.Discovery, *gw.Error)
	AddSession(ctx context.Context, name string) (gw.SessionInfo, []gw.DirectoryEntry, *gw.Error)
	Directory(ctx context.Context) ([]gw.DirectoryEntry, *gw.Error)
	Poll(ctx context.Context) (gw.Inbox, *gw.Error)
	Ack(ctx context.Context, id string, saved bool, reason string) *gw.Error
	ReportStatus(ctx context.Context, id, state, reason string) *gw.Error
	Send(ctx context.Context, r gw.SendRequest) (gw.SendResult, *gw.Error)
}

// Seat is OpenRig as the sidecar uses it. *rigcli.Rig implements it.
type Seat interface {
	Version(ctx context.Context) (string, error)
	Ready(ctx context.Context) (bool, string)
	Send(ctx context.Context, text string) rigcli.SendResult
}

// Sidecar is one running bridge.
type Sidecar struct {
	Cfg     *config.Config
	GW      Gateway
	Seat    Seat
	RigPath string // for status only
	Version string
	Logf    func(format string, args ...any)
	// Clock and Sleep are replaced by tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration)

	journal *journal.Journal
	limiter *limits.Limiter
	slot    chan struct{}
	wg      sync.WaitGroup

	mu    sync.Mutex
	stats stats
	state liveState
}

type stats struct {
	Received, Refused, Pasted, Failed, Unconfirmed int
	LastInboundAt                                  time.Time
	LastInboundID, LastInboundState                string
}

type liveState struct {
	StartedAt     time.Time
	Registered    bool
	GatewayOK     bool
	GatewayWhy    string // when not OK
	Revoked       bool
	PollParked    bool
	LastPollOKAt  time.Time
	StoppedReason string
}

func (s *Sidecar) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *Sidecar) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Sidecar) sleep(ctx context.Context, d time.Duration) {
	if s.Sleep != nil {
		s.Sleep(ctx, d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// Stopped is the stopped.json record.
type Stopped struct {
	Reason string `json:"reason"` // "revoked" or "replaced"
	At     string `json:"at"`
}

// ReadStopped returns the stopped.json record, if the file exists and is valid.
func ReadStopped(path string) (Stopped, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return Stopped{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Stopped{}, false
	}
	var st Stopped
	if json.Unmarshal(raw, &st) != nil || (st.Reason != "revoked" && st.Reason != "replaced") {
		return Stopped{}, false
	}
	return st, true
}

func (s *Sidecar) writeStopped(reason string) {
	raw, _ := json.Marshal(Stopped{Reason: reason, At: s.now().UTC().Format(time.RFC3339)})
	if err := os.WriteFile(s.Cfg.StoppedPath(), append(raw, '\n'), 0o600); err != nil {
		s.logf("could not record why the bridge stopped: %v", err)
	}
	s.mu.Lock()
	s.state.StoppedReason = reason
	s.mu.Unlock()
}

// setGateway records what the last gateway call said, for status.
func (s *Sidecar) setGateway(e *gw.Error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case e == nil:
		s.state.GatewayOK, s.state.GatewayWhy = true, ""
	case e.Kind == gw.KindRevoked:
		s.state.GatewayOK, s.state.GatewayWhy, s.state.Revoked = false, "refused (401, revoked)", true
	case e.Kind == gw.KindPin:
		s.state.GatewayOK, s.state.GatewayWhy = false, "pin mismatch"
	default:
		s.state.GatewayOK, s.state.GatewayWhy = false, "unreachable ("+e.Msg+")"
	}
}

// backoff doubles from 1 s to 30 s; a pin mismatch waits 5 minutes (logged once a minute by the caller).
type backoff struct{ step time.Duration }

func (b *backoff) next(e *gw.Error) time.Duration {
	if e != nil && e.Kind == gw.KindPin {
		return 5 * time.Minute
	}
	if b.step == 0 {
		b.step = time.Second
	} else if b.step < 30*time.Second {
		b.step *= 2
		if b.step > 30*time.Second {
			b.step = 30 * time.Second
		}
	}
	return b.step
}
func (b *backoff) reset() { b.step = 0 }

// Start does everything before the poll loop: version check, journal, discovery, registration, recovery.
// It returns an exit code and a message when the sidecar must not run.
func (s *Sidecar) Start(ctx context.Context) (code int, err error) {
	s.state.StartedAt = s.now()
	if v, verr := s.Seat.Version(ctx); verr != nil {
		return ExitUsage, fmt.Errorf("cannot run rig: %v", verr)
	} else {
		s.logf("rig version: %s", v)
	}
	j, jerr := journal.Open(s.Cfg.JournalPath())
	if jerr != nil {
		return ExitUsage, jerr
	}
	s.journal = j
	s.limiter = limits.New(limits.Config{
		InlineBodyBytes:    s.Cfg.Limits.InlineBodyBytes,
		PerSenderPerMinute: s.Cfg.Limits.PerSenderPerMinute,
		TotalPerMinute:     s.Cfg.Limits.TotalPerMinute,
	}, s.Now)
	s.slot = make(chan struct{}, 1)

	// Discovery and registration retry until they work: after a reboot the gateway may come up later.
	var bo backoff
	for {
		d, e := s.GW.Discover(ctx)
		if e == nil {
			s.setGateway(nil)
			s.logf("gateway offers participant contract %s", d.Contract)
			break
		}
		s.setGateway(e)
		if e.Kind == gw.KindProtocol || (e.Kind == gw.KindRefused) {
			return ExitFailed, fmt.Errorf("discovery failed: %s", e.Msg)
		}
		s.logf("discovery: %s; retrying", e.Msg)
		s.sleep(ctx, bo.next(e))
		if ctx.Err() != nil {
			return ExitOK, nil
		}
	}
	bo.reset()
	for {
		info, _, e := s.GW.AddSession(ctx, s.Cfg.Session)
		if e == nil {
			s.setGateway(nil)
			s.mu.Lock()
			s.state.Registered = true
			s.mu.Unlock()
			s.logf("registered session %s", info.Address)
			break
		}
		s.setGateway(e)
		if e.Kind == gw.KindRevoked {
			s.writeStopped("revoked")
			return ExitRevoked, fmt.Errorf("%s", e.Msg)
		}
		if e.Kind == gw.KindRefused || e.Kind == gw.KindProtocol {
			return ExitFailed, fmt.Errorf("registration failed: %s", e.Msg)
		}
		s.logf("registration: %s; retrying", e.Msg)
		s.sleep(ctx, bo.next(e))
		if ctx.Err() != nil {
			return ExitOK, nil
		}
	}
	s.recover(ctx)
	return ExitOK, nil
}

// recover applies the restart rule (contract 8.4). Nothing is typed here.
func (s *Sidecar) recover(ctx context.Context) {
	for _, r := range s.journal.Unfinished() {
		var event journal.Event
		var state, reason string
		switch r.Event {
		case journal.Typing:
			event, state, reason = journal.Unconfirmed, "unconfirmed", "bridge restarted while typing"
		case journal.Accepted:
			event = journal.AckFailed
		case journal.Acked:
			event, state, reason = journal.Expired, "failed", "bridge restarted before typing"
		}
		if err := s.journal.Append(journal.Record{ID: r.ID, Event: event, From: r.From, Reason: reason}); err != nil {
			s.logf("recovery: cannot record %s for %s: %v", event, r.ID, err)
		}
		s.logf("recovery: %s -> %s", r.ID, event)
		if state != "" && s.now().Sub(r.At) < statusMaxAge {
			s.reportStatus(ctx, r.ID, state, reason)
		}
	}
}

// reportStatus tells the gateway what happened, up to statusAttempts times.
func (s *Sidecar) reportStatus(ctx context.Context, id, state, reason string) {
	for attempt := 0; attempt < statusAttempts; attempt++ {
		if attempt > 0 {
			s.sleep(ctx, statusRetryGap)
		}
		if ctx.Err() != nil {
			return
		}
		e := s.GW.ReportStatus(ctx, id, state, reason)
		if e == nil {
			return
		}
		if e.Kind == gw.KindRefused || e.Kind == gw.KindRevoked {
			s.logf("status for %s refused: %s", id, e.Msg)
			return
		}
	}
	s.logf("status for %s was not accepted after %d tries", id, statusAttempts)
}

// PollLoop parks the long poll until ctx ends or the sidecar must stop, and returns the exit code.
func (s *Sidecar) PollLoop(ctx context.Context) int {
	var bo backoff
	lastPinLog := time.Time{}
	for ctx.Err() == nil {
		s.mu.Lock()
		s.state.PollParked = true
		s.mu.Unlock()
		inbox, e := s.GW.Poll(ctx)
		received := s.now()
		s.mu.Lock()
		s.state.PollParked = false
		s.mu.Unlock()

		if e != nil {
			s.setGateway(e)
			switch e.Kind {
			case gw.KindRevoked:
				s.writeStopped("revoked")
				s.logf("credential revoked: stopping")
				return ExitRevoked
			case gw.KindRefused:
				s.logf("poll refused: %s", e.Msg)
				return ExitFailed
			case gw.KindPin:
				if received.Sub(lastPinLog) >= time.Minute {
					s.logf("LOUD: %s", e.Msg)
					lastPinLog = received
				}
			}
			if ctx.Err() != nil {
				break
			}
			s.sleep(ctx, bo.next(e))
			continue
		}
		bo.reset()
		s.setGateway(nil)
		s.mu.Lock()
		s.state.LastPollOKAt = received
		s.mu.Unlock()

		if inbox.Replaced {
			s.writeStopped("replaced")
			s.logf("another poll replaced this one: stopping")
			return ExitReplaced
		}
		for _, offer := range inbox.Offers {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handleOffer(ctx, offer, received)
			}()
		}
	}
	return ExitOK
}

// Wait waits for in-flight offers.
func (s *Sidecar) Wait() { s.wg.Wait() }

// Close releases the journal.
func (s *Sidecar) Close() {
	if s.journal != nil {
		_ = s.journal.Close()
	}
}
