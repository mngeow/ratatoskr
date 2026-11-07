package tui

// The runtime (connected) entry points that do not depend on the network: agent
// selection and the model constructor that allows a single pane.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"fmt"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// The harness names a runtime run may select. They are the same two names
// internal/agent launches, repeated here so the UI package needs no import of it.
const (
	agentClaude = "claude"
	agentCodex  = "codex"
)

// ParseAgents reads the --agents value.
//
// An empty value means both agents, in the order a person reads them. Otherwise
// the value is a comma-separated list of exact lowercase names: order is kept,
// and an empty entry, an unknown name or a duplicate is an error naming the
// offending value rather than being dropped silently. Nothing here discovers or
// launches a harness; backend owns the flag and the launch.
//
// UNTESTED.
func ParseAgents(value string) ([]string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return []string{agentClaude, agentCodex}, nil
	}

	names := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, raw := range strings.Split(trimmed, ",") {
		name := strings.TrimSpace(raw)
		switch {
		case name == "":
			return nil, fmt.Errorf("tui: ParseAgents: empty agent name in %q", value)
		case name != agentClaude && name != agentCodex:
			return nil, fmt.Errorf("tui: ParseAgents: unknown agent %q in %q, want %s or %s", name, value, agentClaude, agentCodex)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, fmt.Errorf("tui: ParseAgents: duplicate agent %q in %q", name, value)
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

// NewRuntime builds the model for a runtime run: one selected agent, or both.
//
// A connected run with one agent gets one full-width pane; the plain local run
// still builds its two panes through New, whose contract is unchanged. Which one
// was called - not Snapshot.Runtime, which arrives later - decides the layout, so
// a connected run can be laid out before Init receives its first snapshot.
//
// UNTESTED.
func NewRuntime(router Router, events <-chan protocol.Event, slots []Slot) (*Model, error) {
	if len(slots) < 1 || len(slots) > 2 {
		return nil, fmt.Errorf("tui: NewRuntime: need one or two slots, got %d", len(slots))
	}
	return buildModel("NewRuntime", router, events, slots, true)
}

// buildModel is the one constructor both entry points use. It is not named
// newModel because a test helper already owns that name.
func buildModel(name string, router Router, events <-chan protocol.Event, slots []Slot, allowOne bool) (*Model, error) {
	if router == nil {
		return nil, fmt.Errorf("tui: %s: nil router", name)
	}
	if len(slots) < 1 || len(slots) > 2 {
		return nil, fmt.Errorf("tui: %s: need one or two slots, got %d", name, len(slots))
	}
	if !allowOne && len(slots) != 2 {
		return nil, fmt.Errorf("tui: %s: need exactly two slots, got %d", name, len(slots))
	}

	seen := make(map[protocol.AgentID]struct{}, len(slots))
	for i, slot := range slots {
		if slot.ID == "" {
			return nil, fmt.Errorf("tui: %s: slot %d has an empty agent ID", name, i+1)
		}
		if _, duplicate := seen[slot.ID]; duplicate {
			return nil, fmt.Errorf("tui: %s: duplicate agent ID %q", name, slot.ID)
		}
		seen[slot.ID] = struct{}{}
	}

	return allocateModel(router, events, slots), nil
}

// allocateModel builds the bare model: the slots copied into the fixed array, the draft map, the
// clock and the mouse state. It validates nothing.
//
// The constructors validate first and then call it, and NewDaemon's zero-session branch calls it with
// no slots at all - which is why the slot copying is guarded rather than assumed.
func allocateModel(router Router, events <-chan protocol.Event, slots []Slot) *Model {
	model := &Model{
		router: router,
		events: events,
		active: len(slots),
		drafts: make(map[protocol.AgentID]bool, 2),
		// The clock is injectable so the Ctrl+C window can be tested without a
		// sleep; production always uses time.Now.
		now:   time.Now,
		mouse: mouseState{owner: -1},
		theme: newUITheme(),
	}
	if len(slots) > 0 {
		model.slots[0] = slots[0]
	}
	if len(slots) > 1 {
		model.slots[1] = slots[1]
	}
	return model
}

// activeSlots returns the slot indices that exist.
func (m *Model) activeSlots() []int {
	count := m.activeCount()
	indices := make([]int, 0, count)
	for i := 0; i < count; i++ {
		indices = append(indices, i)
	}
	return indices
}
