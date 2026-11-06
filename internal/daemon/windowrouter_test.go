package daemon

// Untested: under GR-3 nothing here has been run. A fake router, so no store is read.

import (
	"context"
	"errors"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// fakeWindowRouter is the router as a window sees it, with recorded calls.
type fakeWindowRouter struct {
	drafts []bool
	// drafting is the router's own flag: UserDraft sets it, and DeliverHeld clears it only
	// when deliverClears says the override applied. UserSubmitted never touches it.
	drafting      bool
	deliverClears bool
	submitted     int
	delivered     int
	snapshot      protocol.Snapshot
	pending       int
	history       []protocol.MessageView
	runs          []protocol.RunView
	detail        protocol.MessageDetail
	sends         []protocol.SendView
	sendDetail    protocol.SendDetail
	err           error
}

// UserDraft records the draft flag.
func (f *fakeWindowRouter) UserDraft(_ protocol.AgentID, drafting bool) {
	f.drafts = append(f.drafts, drafting)
	f.drafting = drafting
}

// UserSubmitted records a submit.
func (f *fakeWindowRouter) UserSubmitted(protocol.AgentID) { f.submitted++ }

// DeliverHeld records a deliver-held.
func (f *fakeWindowRouter) DeliverHeld(protocol.AgentID) {
	f.delivered++
	if f.deliverClears {
		f.drafting = false
	}
}

// Drafting returns the router's own flag.
func (f *fakeWindowRouter) Drafting(context.Context, protocol.AgentID) (bool, error) {
	return f.drafting, nil
}

// Snapshot returns the recorded snapshot.
func (f *fakeWindowRouter) Snapshot() protocol.Snapshot { return f.snapshot }

// PendingOnQuit returns the recorded count.
func (f *fakeWindowRouter) PendingOnQuit() int { return f.pending }

// History returns the recorded history.
func (f *fakeWindowRouter) History(protocol.HistoryQuery) ([]protocol.MessageView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.history, nil
}

// Runs returns the recorded runs.
func (f *fakeWindowRouter) Runs(int) ([]protocol.RunView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.runs, nil
}

// MessageDetail returns the recorded detail.
func (f *fakeWindowRouter) MessageDetail(string) (protocol.MessageDetail, error) {
	if f.err != nil {
		return protocol.MessageDetail{}, f.err
	}
	return f.detail, nil
}

// Sends returns the recorded outbound list.
func (f *fakeWindowRouter) Sends(int) ([]protocol.SendView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.sends, nil
}

// SendDetail returns the recorded outbound detail.
func (f *fakeWindowRouter) SendDetail(string) (protocol.SendDetail, error) {
	if f.err != nil {
		return protocol.SendDetail{}, f.err
	}
	return f.sendDetail, nil
}

// errNoMessageRecorded is what a query test injects to check the not-found text.
var errNoMessageRecorded = errors.New("router: no such message")
