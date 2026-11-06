package link

// Close: stop the stream, then tell the gateway this run's sessions are over.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"fmt"
)

// Close ends a connected run's link.
//
// It is idempotent: the second call does nothing and returns nil. If Run is going,
// it is cancelled and waited for, bounded by ctx; then this run's sessions are ended
// at the gateway, bounded separately, and that error is returned. A wait that ctx cut
// short does not skip the unregister: leaving a stale session at the gateway is worse
// than returning an error, so both are attempted and the first failure wins.
//
// UNTESTED.
func (c *Client) Close(ctx context.Context) error {
	var closeErr error

	c.closeOnce.Do(func() {
		c.closed.Store(true)

		c.mu.Lock()
		cancel := c.runCancel
		done := c.runDone
		c.mu.Unlock()

		if cancel != nil && done != nil {
			cancel()
			select {
			case <-done:
			case <-ctx.Done():
				closeErr = ctx.Err()
			}
		}

		// Detached from ctx on purpose: if ctx is what cut the wait short, an
		// unregister under the same ctx would fail at once and the stale session
		// the comment above promises not to leave would be left. It has its own
		// bound.
		unregisterCtx, unregisterCancel := context.WithTimeout(context.WithoutCancel(ctx), unregisterTimeout)
		defer unregisterCancel()

		if err := c.Unregister(unregisterCtx); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("link: ending this run's sessions: %w", err)
		}
	})

	return closeErr
}
