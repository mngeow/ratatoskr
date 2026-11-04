package router

import (
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// TestSessionBindingRules walks the binding table from the design: unbound agents
// accept nothing but a startup SessionStart, matching events apply, and anything
// from another session is counted, bannered and can only fail to hold.
func TestSessionBindingRules(t *testing.T) {
	tests := []struct {
		name        string
		bound       string // session bound at registration; empty means unbound
		kind        protocol.HookKind
		source      string
		sessionID   string
		startState  protocol.AgentState
		wantState   protocol.AgentState
		wantBound   string
		wantIgnored int
		wantBanner  bool
	}{
		{
			name:       "unbound ignores a non-session-start event",
			kind:       protocol.HookUserPromptSubmit,
			sessionID:  "s1",
			startState: protocol.AgentStarting,
			wantState:  protocol.AgentStarting,
		},
		{
			name:       "unbound ignores resume",
			kind:       protocol.HookSessionStart,
			source:     "resume",
			sessionID:  "s1",
			startState: protocol.AgentStarting,
			wantState:  protocol.AgentStarting,
		},
		{
			name:       "unbound ignores a startup with no session id",
			kind:       protocol.HookSessionStart,
			source:     "startup",
			startState: protocol.AgentStarting,
			wantState:  protocol.AgentStarting,
		},
		{
			name:       "binding startup binds and goes idle",
			kind:       protocol.HookSessionStart,
			source:     "startup",
			sessionID:  "s1",
			startState: protocol.AgentStarting,
			wantState:  protocol.AgentIdle,
			wantBound:  "s1",
		},
		{
			name:       "matching submit becomes busy",
			bound:      "s1",
			kind:       protocol.HookUserPromptSubmit,
			sessionID:  "s1",
			startState: protocol.AgentIdle,
			wantState:  protocol.AgentBusy,
			wantBound:  "s1",
		},
		{
			name:        "mismatch while idle fails to hold",
			bound:       "s1",
			kind:        protocol.HookUserPromptSubmit,
			sessionID:   "orphan",
			startState:  protocol.AgentIdle,
			wantState:   protocol.AgentUnknown,
			wantBound:   "s1",
			wantIgnored: 1,
			wantBanner:  true,
		},
		{
			name:        "mismatch with no session id is a mismatch",
			bound:       "s1",
			kind:        protocol.HookStop,
			startState:  protocol.AgentIdle,
			wantState:   protocol.AgentUnknown,
			wantBound:   "s1",
			wantIgnored: 1,
			wantBanner:  true,
		},
		{
			name:        "mismatch while busy leaves the state alone",
			bound:       "s1",
			kind:        protocol.HookStop,
			sessionID:   "orphan",
			startState:  protocol.AgentBusy,
			wantState:   protocol.AgentBusy,
			wantBound:   "s1",
			wantIgnored: 1,
			wantBanner:  true,
		},
		{
			name:        "a different session never rebinds",
			bound:       "s1",
			kind:        protocol.HookSessionStart,
			source:      "startup",
			sessionID:   "s2",
			startState:  protocol.AgentStarting,
			wantState:   protocol.AgentStarting,
			wantBound:   "s1",
			wantIgnored: 1,
			wantBanner:  true,
		},
		{
			name:       "recovery from unknown by a matching stop",
			bound:      "s1",
			kind:       protocol.HookStop,
			sessionID:  "s1",
			startState: protocol.AgentUnknown,
			wantState:  protocol.AgentIdle,
			wantBound:  "s1",
		},
		{
			name:       "recovery from unknown by a matching submit",
			bound:      "s1",
			kind:       protocol.HookUserPromptSubmit,
			sessionID:  "s1",
			startState: protocol.AgentUnknown,
			wantState:  protocol.AgentBusy,
			wantBound:  "s1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := newBareRouter(t)
			s := boundSpec("a", "alpha", tc.bound)
			if err := r.registerAgent(s); err != nil {
				t.Fatalf("registerAgent: %v", err)
			}
			a := r.agents["a"]
			a.state = tc.startState

			r.hook(a, protocol.HookEvent{
				Kind:      tc.kind,
				Source:    tc.source,
				SessionID: tc.sessionID,
			})

			if a.boundSession != tc.wantBound {
				t.Fatalf("boundSession = %q, want %q", a.boundSession, tc.wantBound)
			}
			if a.state != tc.wantState {
				t.Fatalf("state = %q, want %q", a.state, tc.wantState)
			}
			if a.ignoredHooks != tc.wantIgnored {
				t.Fatalf("ignoredHooks = %d, want %d", a.ignoredHooks, tc.wantIgnored)
			}
			banner, hasBanner := r.banners[r.hooksBannerKey(a)]
			if hasBanner != tc.wantBanner {
				t.Fatalf("banner present = %v, want %v (banner %q)", hasBanner, tc.wantBanner, banner)
			}
			if tc.wantBanner && !strings.Contains(banner, "hook events from another session were ignored (1)") {
				t.Fatalf("mismatch banner = %q", banner)
			}
		})
	}
}

// TestMatchingEventClearsTheHooksBanner: after the agent recovers, the warning
// must stop claiming hooks are missing.
func TestMatchingEventClearsTheHooksBanner(t *testing.T) {
	r, _, _ := newBareRouter(t)
	if err := r.registerAgent(boundSpec("a", "alpha", "s1")); err != nil {
		t.Fatalf("registerAgent: %v", err)
	}
	a := r.agents["a"]
	a.state = protocol.AgentIdle

	r.hook(a, protocol.HookEvent{Kind: protocol.HookStop, SessionID: "orphan"})
	if _, ok := r.banners[r.hooksBannerKey(a)]; !ok {
		t.Fatal("no banner after a mismatch")
	}

	r.hook(a, protocol.HookEvent{Kind: protocol.HookStop, SessionID: "s1"})
	if banner, ok := r.banners[r.hooksBannerKey(a)]; ok {
		t.Fatalf("banner survived an accepted event: %q", banner)
	}
}

// TestMismatchBannerNamesTheConsequence pins the user-facing sentence, because it
// is the only warning that this pane is untracked for the rest of the run.
func TestMismatchBannerNamesTheConsequence(t *testing.T) {
	got := hookMismatchBanner("beta", 3)
	want := "beta: hook events from another session were ignored (3). If you ran /clear or /compact in this pane, ratatoskr can no longer track it for the rest of this run. Messages to it are held; F4 delivers."
	if got != want {
		t.Fatalf("mismatch banner =\n%q\nwant\n%q", got, want)
	}

	got = noHooksBanner("beta")
	want = "no hook events from beta: hooks may be missing or not trusted. Messages are held; F4 delivers."
	if got != want {
		t.Fatalf("no-hooks banner =\n%q\nwant\n%q", got, want)
	}
}

// TestPromptSubmitConfirmRules covers the pasted and delivering halves of the
// submit rules, including the case that must credit nothing.
func TestPromptSubmitConfirmRules(t *testing.T) {
	tests := []struct {
		name         string
		state        protocol.MessageState
		prompt       string
		wantState    protocol.MessageState
		wantReason   string
		wantInFlight bool
		wantEarly    bool
	}{
		{
			name:       "pasted with no prompt text is confirmed",
			state:      protocol.MessagePasted,
			wantState:  protocol.MessageAccepted,
			wantReason: "prompt_submitted",
		},
		{
			name:       "pasted with the id in the prompt is confirmed",
			state:      protocol.MessagePasted,
			prompt:     "please handle <ratatoskr-message id=\"m1\">",
			wantState:  protocol.MessageAccepted,
			wantReason: "prompt_submitted",
		},
		{
			name:         "pasted with a foreign prompt is not credited",
			state:        protocol.MessagePasted,
			prompt:       "something else entirely",
			wantState:    protocol.MessagePasted,
			wantInFlight: true,
		},
		{
			name:         "delivering with no prompt text arms the early confirm",
			state:        protocol.MessageDelivering,
			wantState:    protocol.MessageDelivering,
			wantInFlight: true,
			wantEarly:    true,
		},
		{
			name:         "delivering with the id arms the early confirm",
			state:        protocol.MessageDelivering,
			prompt:       "m1 confirmed",
			wantState:    protocol.MessageDelivering,
			wantInFlight: true,
			wantEarly:    true,
		},
		{
			name:         "delivering with a foreign prompt arms nothing",
			state:        protocol.MessageDelivering,
			prompt:       "unrelated",
			wantState:    protocol.MessageDelivering,
			wantInFlight: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := newBareRouter(t)
			if err := r.registerAgent(boundSpec("a", "alpha", "s1")); err != nil {
				t.Fatalf("registerAgent: %v", err)
			}
			a := r.agents["a"]
			a.state = protocol.AgentIdle

			m := &msg{id: "m1", from: a, to: a, state: tc.state}
			a.inflight = m

			r.promptSubmitted(a, tc.prompt)

			if m.state != tc.wantState {
				t.Fatalf("message state = %q, want %q", m.state, tc.wantState)
			}
			if tc.wantReason != "" && m.reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", m.reason, tc.wantReason)
			}
			if got := a.inflight != nil; got != tc.wantInFlight {
				t.Fatalf("in flight = %v, want %v", got, tc.wantInFlight)
			}
			if a.earlyConfirm != tc.wantEarly {
				t.Fatalf("earlyConfirm = %v, want %v", a.earlyConfirm, tc.wantEarly)
			}
		})
	}
}

// TestLateAcceptRule covers the unconfirmed-to-accepted edge: newest first, one
// message per event, and nothing at all for an empty prompt.
func TestLateAcceptRule(t *testing.T) {
	tests := []struct {
		name       string
		prompt     string
		wantOld    protocol.MessageState
		wantNew    protocol.MessageState
		wantReason string
		wantLeft   int
	}{
		{
			name:     "empty prompt credits nothing",
			prompt:   "   ",
			wantOld:  protocol.MessageUnconfirmed,
			wantNew:  protocol.MessageUnconfirmed,
			wantLeft: 2,
		},
		{
			name:       "the older id is credited",
			prompt:     "m-old submitted",
			wantOld:    protocol.MessageAccepted,
			wantNew:    protocol.MessageUnconfirmed,
			wantReason: "prompt_submitted_late",
			wantLeft:   1,
		},
		{
			name:       "the newer id is credited",
			prompt:     "m-new submitted",
			wantOld:    protocol.MessageUnconfirmed,
			wantNew:    protocol.MessageAccepted,
			wantReason: "prompt_submitted_late",
			wantLeft:   1,
		},
		{
			name:       "only the newest is credited when both appear",
			prompt:     "m-old and m-new",
			wantOld:    protocol.MessageUnconfirmed,
			wantNew:    protocol.MessageAccepted,
			wantReason: "prompt_submitted_late",
			wantLeft:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _ := newBareRouter(t)
			if err := r.registerAgent(boundSpec("a", "alpha", "s1")); err != nil {
				t.Fatalf("registerAgent: %v", err)
			}
			a := r.agents["a"]
			a.state = protocol.AgentIdle

			old := &msg{id: "m-old", from: a, to: a, state: protocol.MessageUnconfirmed}
			newer := &msg{id: "m-new", from: a, to: a, state: protocol.MessageUnconfirmed}
			a.unconfirmed = []*msg{old, newer}

			r.promptSubmitted(a, tc.prompt)

			if old.state != tc.wantOld {
				t.Fatalf("older message state = %q, want %q", old.state, tc.wantOld)
			}
			if newer.state != tc.wantNew {
				t.Fatalf("newer message state = %q, want %q", newer.state, tc.wantNew)
			}
			if tc.wantReason != "" {
				if old.state == protocol.MessageAccepted && old.reason != tc.wantReason {
					t.Fatalf("older reason = %q, want %q", old.reason, tc.wantReason)
				}
				if newer.state == protocol.MessageAccepted && newer.reason != tc.wantReason {
					t.Fatalf("newer reason = %q, want %q", newer.reason, tc.wantReason)
				}
			}
			if len(a.unconfirmed) != tc.wantLeft {
				t.Fatalf("unconfirmed list has %d entries, want %d", len(a.unconfirmed), tc.wantLeft)
			}
		})
	}
}

// TestPermissionRequestNeedsYou: the router never answers a prompt and the state
// it records is needs_you.
func TestPermissionRequestNeedsYou(t *testing.T) {
	r, _, _ := newBareRouter(t)
	if err := r.registerAgent(boundSpec("a", "alpha", "s1")); err != nil {
		t.Fatalf("registerAgent: %v", err)
	}
	a := r.agents["a"]
	a.state = protocol.AgentIdle

	r.hook(a, protocol.HookEvent{Kind: protocol.HookPermissionRequest, SessionID: "s1"})

	if a.state != protocol.AgentNeedsYou {
		t.Fatalf("state = %q, want %q", a.state, protocol.AgentNeedsYou)
	}
}

// TestHookEventForUnknownAgentIsIgnored keeps a late hook from a seat that was
// never registered harmless.
func TestHookEventForUnknownAgentIsIgnored(t *testing.T) {
	r, _, _ := newBareRouter(t)
	r.HookEvent(nil, "ghost", protocol.HookEvent{Kind: protocol.HookStop})
	fn := <-r.in
	fn()

	if len(r.agents) != 0 {
		t.Fatal("a hook created an agent")
	}
}

// TestRememberUnconfirmedIsBounded keeps the late-rule list small.
func TestRememberUnconfirmedIsBounded(t *testing.T) {
	r, _, _ := newBareRouter(t)
	a := &agent{id: "a", name: "alpha"}

	for i := 0; i < unconfirmedLimit+5; i++ {
		r.rememberUnconfirmed(a, &msg{id: strings.Repeat("x", i+1)})
	}

	if len(a.unconfirmed) != unconfirmedLimit {
		t.Fatalf("unconfirmed holds %d messages, want %d", len(a.unconfirmed), unconfirmedLimit)
	}
	// The newest survive.
	if got := a.unconfirmed[len(a.unconfirmed)-1].id; got != strings.Repeat("x", unconfirmedLimit+5) {
		t.Fatalf("newest remembered message = %q", got)
	}
}
