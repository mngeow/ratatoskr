package daemon

// The enter query: what pressing Enter on one Network row should do.
//
// The daemon answers a plan and never runs anything. Everything executable comes from the owner's
// hosts.json, read fresh at each Enter; the gateway snapshot may only contribute a host name and a
// session id, and even that id is re-validated before it can appear in a command.
//
// The hosts.json read is file IO, so it happens on the query goroutine, never under a daemon lock.

import (
	"path/filepath"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/entry"
)

// enterPlan builds the plan for one row.
//
// UNTESTED.
func (s *networkService) enterPlan(request wire.EnterRequest, w *window) wire.EnterPlan {
	// The local picture and the kept snapshot, from the same view the Network page draws. Asking for
	// it does NOT make this window a fetch consumer: enter reads, it does not pull.
	view, _ := s.view()

	stateDir := s.stateDirectory()
	hosts, hostsErr := entry.ReadHostMap(filepath.Join(stateDir, "hosts.json"))

	var inWindow func(string) bool
	if w != nil {
		inWindow = func(localID string) bool {
			w.mu.Lock()
			defer w.mu.Unlock()
			for agent := range w.sessions {
				if string(agent) == localID {
					return true
				}
			}
			return false
		}
	}

	return entry.BuildPlan(entry.PlanInput{
		Request:   request,
		LocalHost: view.Local.Host,
		Local:     view.Local.Sessions,
		InWindow:  inWindow,
		Snapshot:  view.Snapshot,
		Hosts:     hosts,
		HostsErr:  hostsErr,
	})
}
