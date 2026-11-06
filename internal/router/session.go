package router

// Adding one session to a running router.
//
// The daemon starts a replaced session - the same name, after the old one ended - without
// restarting the router, so the exited agent has to leave the name index while staying
// addressable by ID: messages already addressed to it must still resolve.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No session has been added.

import (
	"context"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// AddSession registers one agent in a running router, replacing an exited agent of the
// same display name.
//
// Only an agent in the exited state is replaced: a live one is still refused by
// registerAgent, which is what keeps two harnesses from sharing a name. The replaced
// agent stays in r.agents, so a message that names it still resolves to it rather than to
// its replacement.
//
// UNTESTED.
func (r *Router) AddSession(ctx context.Context, spec protocol.LaunchSpec) error {
	return r.call(ctx, func() error {
		name := stripControl(spec.DisplayName)
		if name == "" {
			name = stripControl(string(spec.ID))
		}

		if existing, ok := r.byName[name]; ok && existing.state == protocol.AgentExited {
			delete(r.byName, name)
			for i, ordered := range r.order {
				if ordered == existing {
					r.order = append(r.order[:i], r.order[i+1:]...)
					break
				}
			}
		}
		return r.registerAgent(spec)
	})
}
