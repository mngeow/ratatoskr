package server

// Untested: under GR-3 nothing here has been run. The server binds a loopback listener in
// New, as the package's other tests already do; nothing is dialled.

import (
	"context"
	"testing"
)

// TestRemoveAgentStopsTheToken: after RemoveAgent the token is refused, and a second
// removal reports that there was nothing to remove.
func TestRemoveAgentStopsTheToken(t *testing.T) {
	inbox := &fakeInbox{}
	s, err := New(inbox, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	token := s.AddConnectedAgent("alpha", "alpha", "alpha@mac", nil)
	if token == "" {
		t.Fatal("AddConnectedAgent returned no token")
	}
	if _, found := s.authenticate(token); !found {
		t.Fatal("the token did not authenticate before removal")
	}

	if !s.RemoveAgent("alpha") {
		t.Fatal("RemoveAgent reported that alpha did not exist")
	}
	if _, found := s.authenticate(token); found {
		t.Fatal("the removed token still authenticates")
	}
	if _, ok := s.byID["alpha"]; ok {
		t.Fatal("the removed agent is still indexed by id")
	}

	if s.RemoveAgent("alpha") {
		t.Fatal("a second RemoveAgent reported success")
	}
	if s.RemoveAgent("never-existed") {
		t.Fatal("RemoveAgent invented an agent")
	}
}

// TestRemoveAgentLeavesTheOthers: removing one agent does not disturb another's token.
func TestRemoveAgentLeavesTheOthers(t *testing.T) {
	inbox := &fakeInbox{}
	s, err := New(inbox, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	alphaToken := s.AddConnectedAgent("alpha", "alpha", "alpha@mac", nil)
	betaToken := s.AddConnectedAgent("beta", "beta", "beta@mac", nil)

	if !s.RemoveAgent("alpha") {
		t.Fatal("RemoveAgent: alpha was not found")
	}
	if _, found := s.authenticate(alphaToken); found {
		t.Fatal("alpha's token still authenticates")
	}
	if entry, found := s.authenticate(betaToken); !found || entry.id != "beta" {
		t.Fatalf("beta's token stopped working: %+v %v", entry, found)
	}
}
