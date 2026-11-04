// Package pane owns one harness PTY, emulator and child session.
//
// UNTESTED under GR-3. Only source review and compile checks have been applied;
// no tests, harness, PTY probe or application binary has been run by this work.
//
// The emulator has one owning worker. Snapshot returns an atomic last-good
// frame; Err, InputReady and MouseEnabled also read atomics. RequestResize only
// records a coalesced desired size and wakes its worker. None of these UI-facing
// methods calls x/vt, waits for PTY progress or takes a pane mutex. A stall inside
// x/vt Write/Render costs that pane: an operation watchdog marks input unavailable
// and the cached frame remains available. Go cannot preempt a stuck library call.
//
// The pipe reader and PTY writer are separate. The reader copies into a bounded
// queue without waiting for PTY progress; overflow reports backpressure rather
// than permanently closing input. Actual OS write failure disables input visibly.
// User input has a three-second whole-operation bound (untested). Terminal query
// writes have the same bound, and timeout does not kill the drain. A parser or
// renderer timeout is latched unavailable for this run; automatic recovery is
// deliberately not attempted.
//
// There is no synthetic empty-write barrier. The pinned key/mouse encoder writes
// once; a nonempty paste writes once or three times in bracketed-paste mode. The
// pipe buffer fits each supported write. A batch counts these real writes and
// places its completion fence behind the bytes; empty encoding is an error, not
// a completion. This pinned API assumption is untested and covered by unrun tests.
// SendKey/Paste/SendMouse report completion only after actual PTY progress. Input
// transactions preserve ordering; router PasteSubmit fences previously buffered
// input, then writes bracketed paste and a separate Enter under one writer lock.
// It rejects ESC, honors context cancellation and reports cumulative accepted
// bytes through WriteError. The 50ms paste/Enter delay remains untested.
//
// Mouse tracking mirrors x/vt mode callbacks, including reset/RIS. Unsupported
// motion is filtered; legacy coordinates clamp to 222 to prevent byte overflow.
// Shift+Enter maps to Ctrl+J for the known harnesses, because x/vt has no Kitty or
// CSI-u encoder. Custom bindings, Unicode, terminal queries and visual rendering
// remain unverified. User paste is limited to 1MiB; transport buffering is 2MiB
// and 512 slots. Scrollback is bounded to one line (zero is a no-op in x/vt).
//
// Start takes a complete environment and absolute working directory. It records
// whether the owned child leads its own process group. Close sends SIGTERM, then
// SIGKILL after an untested grace interval, to that verified group only while
// the child is unreaped. Signalling and Wait4(WNOHANG) reaping share lifeMu, so
// PID reuse cannot cross the ownership check. Wait polling adds up to 100ms exit
// notification latency; exit status is preserved, signals map to -1, and onExit
// fires once. CloseContext is idempotent and bounds its caller's wait. Timeouts
// wrap context.DeadlineExceeded. A wrapper that exits before escalation, or tools
// in another process group, may outlive it; no unrelated group is signalled.
package pane
