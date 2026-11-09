package sidecar

// The status report (contract section 13). It carries no credential, no full pin and no message text.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"context"
	"os"
	"time"
)

// StatusReport is what `status` prints (and `status --json` prints as JSON).
type StatusReport struct {
	PID           int       `json:"pid"`
	Version       string    `json:"version"`
	StartedAt     time.Time `json:"started_at"`
	Gateway       string    `json:"gateway"` // reachable | unreachable (...) | pin mismatch | refused (401, revoked)
	Session       string    `json:"session"`
	Registered    bool      `json:"registered"`
	Poll          string    `json:"poll"` // parked | between polls | not polling since <time>
	OperatorSeat  string    `json:"operator_seat"`
	SeatReady     bool      `json:"seat_ready"`
	SeatWord      string    `json:"seat_word,omitempty"`
	RigPath       string    `json:"rig_path"`
	Stopped       string    `json:"stopped,omitempty"`
	Received      int       `json:"received"`
	Refused       int       `json:"refused"`
	Pasted        int       `json:"pasted"`
	Failed        int       `json:"failed"`
	Unconfirmed   int       `json:"unconfirmed"`
	LastInboundAt time.Time `json:"last_inbound_at,omitempty"`
	LastInboundID string    `json:"last_inbound_id,omitempty"`
	LastInbound   string    `json:"last_inbound_state,omitempty"`
	InlineBytes   int       `json:"limit_inline_bytes"`
	PerSender     int       `json:"limit_per_sender_per_minute"`
	Total         int       `json:"limit_total_per_minute"`
}

// pollSilence is how long without a good poll answer counts as "not polling" (a poll answers every 20 s).
const pollSilence = 60 * time.Second

// Status builds the report, including a live ready check.
func (s *Sidecar) Status(ctx context.Context) StatusReport {
	ready, word := s.Seat.Ready(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	r := StatusReport{
		PID: os.Getpid(), Version: s.Version, StartedAt: s.state.StartedAt,
		Session: s.Cfg.Session + "@" + s.Cfg.Participant, Registered: s.state.Registered,
		OperatorSeat: s.Cfg.OperatorSeat, SeatReady: ready, SeatWord: word, RigPath: s.RigPath,
		Stopped:  s.state.StoppedReason,
		Received: s.stats.Received, Refused: s.stats.Refused, Pasted: s.stats.Pasted,
		Failed: s.stats.Failed, Unconfirmed: s.stats.Unconfirmed,
		LastInboundAt: s.stats.LastInboundAt, LastInboundID: s.stats.LastInboundID, LastInbound: s.stats.LastInboundState,
		InlineBytes: s.Cfg.Limits.InlineBodyBytes, PerSender: s.Cfg.Limits.PerSenderPerMinute, Total: s.Cfg.Limits.TotalPerMinute,
	}
	if s.state.GatewayOK {
		r.Gateway = "reachable"
	} else {
		r.Gateway = s.state.GatewayWhy
		if r.Gateway == "" {
			r.Gateway = "unreachable (no answer yet)"
		}
	}
	switch {
	case s.state.PollParked:
		r.Poll = "parked"
	case s.state.LastPollOKAt.IsZero() || s.now().Sub(s.state.LastPollOKAt) > pollSilence:
		since := s.state.StartedAt
		if !s.state.LastPollOKAt.IsZero() {
			since = s.state.LastPollOKAt
		}
		r.Poll = "not polling since " + since.Format(time.RFC3339)
	default:
		r.Poll = "between polls"
	}
	return r
}
