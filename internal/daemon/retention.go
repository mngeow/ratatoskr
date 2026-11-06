package daemon

// Retention while the daemon runs.
//
// Retention also runs once at start, in Run, and is a store-internal operation
// serialised by the store's writer, so this loop needs no coordination with the router or
// the socket.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No tick has been waited for.

import (
	"context"
	"time"

	"github.com/mngeow/ratatoskr/internal/store"
)

// retentionLoop prunes old history once a day until the run ends.
//
// A failed pass is logged and retried at the next tick: a retention failure must not stop
// the daemon, and it must not be silent either.
func retentionLoop(ctx context.Context, st *store.Store, log *logger, done chan struct{}) {
	defer close(done)

	ticker := time.NewTicker(retentionEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			passCtx, cancel := context.WithTimeout(ctx, shutdownBudget)
			_, err := st.Retain(passCtx, time.Now().Add(-retention))
			cancel()
			if err != nil {
				log.logf("retention failed: %v", err)
				continue
			}
			log.logf("retention ran")
		}
	}
}
