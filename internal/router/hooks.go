package router

import (
	"context"
	"fmt"
	"strings"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// bindingSessionSource is the only SessionStart source that binds an agent's
// session. resume, fork, clear and compact never bind: an orphan process could
// fire one of those with its own session ID.
const bindingSessionSource = "startup"

// HookEvent reports one harness hook event for one agent.
//
// It only enqueues, so events apply in the order they were sent and a slow store
// cannot make the hook fail: `rtk hook` must never influence the harness's
// decision, and it exits 0 whether or not this call did anything.
//
// UNTESTED.
func (r *Router) HookEvent(ctx context.Context, from protocol.AgentID, ev protocol.HookEvent) {
	_ = ctx // The contract's signature; the enqueue is what matters here.
	r.enqueue(func() {
		a, ok := r.agents[from]
		if !ok {
			return
		}
		r.hook(a, ev)
	})
}

// hook applies one accepted event: the session check first, then the state
// machine, then the prompt rules, then a pump.
//
// UNTESTED.
func (r *Router) hook(a *agent, ev protocol.HookEvent) {
	if !r.acceptHook(a, ev) {
		r.pump(a, false)
		return
	}

	a.lastEvent = r.now()
	// An accepted event proves the hooks are alive and trusted, so whatever the
	// banner said is no longer true.
	r.setBanner(r.hooksBannerKey(a), "")

	// Codex sends its first hook when its first turn starts, so a SessionStart
	// there means "a turn is beginning", not "ready for a message": it binds the
	// session and clears the banner above, but the state stays starting or
	// unknown until the first Stop. Claude's SessionStart is the launch signal
	// and still lifts to idle.
	if ev.Kind != protocol.HookSessionStart || sessionStartLifts(a.harness) {
		a.state = nextState(a.state, ev.Kind)
	}
	r.emit(protocol.Event{Agent: r.agentViewPtr(a)})

	if ev.Kind == protocol.HookUserPromptSubmit {
		r.promptSubmitted(a, ev.Prompt)
	}
	r.pump(a, false)
}

// acceptHook applies the session binding rules and reports whether the event may
// be used.
//
// An unbound agent accepts nothing but its binding SessionStart, and an event
// from another session is never applied: it is counted, shown in a banner and
// only allowed to move an idle agent to unknown. That is the failure-to-hold
// rule - a stray session can push an agent towards holding, never towards
// delivery.
//
// UNTESTED.
func (r *Router) acceptHook(a *agent, ev protocol.HookEvent) bool {
	if a.boundSession == "" {
		if ev.Kind != protocol.HookSessionStart || ev.Source != bindingSessionSource || ev.SessionID == "" {
			// Nothing can be trusted before the session is bound. These are not
			// counted: an agent that has not bound yet is not ignoring a stranger,
			// it is simply waiting.
			return false
		}
		a.boundSession = ev.SessionID
		return true
	}

	if ev.SessionID == a.boundSession {
		return true
	}

	a.ignoredHooks++
	r.setBanner(r.hooksBannerKey(a), hookMismatchBanner(a.name, a.ignoredHooks))
	if a.state == protocol.AgentIdle {
		a.state = protocol.AgentUnknown
		r.emit(protocol.Event{Agent: r.agentViewPtr(a)})
	}
	return false
}

// promptSubmitted applies the three submit rules: confirm a pasted message, arm
// an early confirm for a message still being written, and credit a late submit
// for a message that timed out.
//
// Only one late message is credited per event, and only when the prompt carries
// its ID. A prompt with text but without the ID is somebody submitting something
// else, and credits nothing.
//
// UNTESTED.
func (r *Router) promptSubmitted(a *agent, prompt string) {
	trimmed := strings.TrimSpace(prompt)

	if m := a.inflight; m != nil {
		switch m.state {
		case protocol.MessagePasted:
			if trimmed == "" || strings.Contains(prompt, m.id) {
				a.inflight = nil
				_ = r.move(m, protocol.MessageAccepted, "prompt_submitted")
			}
		case protocol.MessageDelivering:
			if trimmed == "" || strings.Contains(prompt, m.id) {
				// The hook beat the write result. Hold it and apply it after
				// pasted commits; there is no delivering-to-accepted edge.
				a.earlyConfirm = true
			}
		}
	}

	if trimmed == "" {
		return
	}

	// Newest first, first match only.
	for i := len(a.unconfirmed) - 1; i >= 0; i-- {
		m := a.unconfirmed[i]
		if !strings.Contains(prompt, m.id) {
			continue
		}
		a.unconfirmed = append(a.unconfirmed[:i], a.unconfirmed[i+1:]...)
		_ = r.move(m, protocol.MessageAccepted, "prompt_submitted_late")
		return
	}
}

// rememberUnconfirmed keeps a message that timed out or died with its pane, so a
// later submit that carries its ID can still credit it. Only the most recent
// unconfirmedLimit are kept.
func (r *Router) rememberUnconfirmed(a *agent, m *msg) {
	a.unconfirmed = append(a.unconfirmed, m)
	if len(a.unconfirmed) > unconfirmedLimit {
		a.unconfirmed = append([]*msg(nil), a.unconfirmed[len(a.unconfirmed)-unconfirmedLimit:]...)
	}
}

// hooksBannerKey is the banner slot for one agent's hook trouble. The no-hooks
// and wrong-session messages share it, because only one of them can be true.
func (r *Router) hooksBannerKey(a *agent) string {
	return hooksBannerPrefix + a.name
}

// noHooksBanner is shown when an agent never reported a hook in time.
func noHooksBanner(name string) string {
	return fmt.Sprintf("no hook events from %s: hooks may be missing or not trusted. Messages are held; F4 delivers.", name)
}

// hookMismatchBanner is shown when hooks arrive from a session the router does
// not trust. It says what the consequence is, because the consequence lasts for
// the rest of the run.
func hookMismatchBanner(name string, ignored int) string {
	return fmt.Sprintf("%s: hook events from another session were ignored (%d). If you ran /clear or /compact in this pane, ratatoskr can no longer track it for the rest of this run. Messages to it are held; F4 delivers.", name, ignored)
}
