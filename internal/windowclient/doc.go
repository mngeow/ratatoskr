// Package windowclient is the window side of the daemon socket: the adapter the TUI talks to
// when its panes live in a daemon.
//
// It speaks exactly the messages in internal/daemon/wire and nothing else. It imports wire,
// protocol, pane and ultraviolet, plus the standard library; it never imports the daemon, app,
// router, store, server, link or tui packages, because a window is a client and holds none of
// them.
//
// The rules this package exists to keep:
//
//   - One writer goroutine owns the connection. Every frame is written there, under a deadline,
//     in the order the caller queued it.
//   - One read pump owns the other direction. It never waits on a request, a getter or a
//     downstream consumer: a full event queue fails the window instead of blocking the pump.
//   - Reads are cached. Snapshot, PendingOnQuit, a pane's frame, state, readiness, mouse
//     eligibility and errors are immutable or atomic copies, so the UI can ask at any moment
//     without touching the socket or a lock held during IO.
//   - Nothing is retried. An input request whose outcome is unknown ends the connection, because
//     resending it could type twice.
//   - A failed input latches, and later input does not silently proceed past it. Only a
//     successful take_input clears a recoverable latch.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run: only gofmt,
// go vet and go build have been applied. No socket has been dialled, no window attached and no
// frame sent.
package windowclient
