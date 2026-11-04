// Package tui is the Bubble Tea client: two panes side by side, a status line,
// focus, the message log and conservative draft tracking.
//
// # Ownership
//
// internal/tui owns layout, focus, the message log, draft flags and key
// handling. It does not start, stop or kill anything: App supplies panes that
// are already running, or the error that stopped them starting, and keeps
// ownership of process cleanup. The UI reads a pane through the narrow Pane
// interface and talks to the router through Router; both interfaces are defined
// here, because the consumer defines the seam it needs.
//
// Two slots, no more: F1 and F2 mean first and second, so the order App passes
// is the order on screen.
//
// # What the UI is allowed to show
//
// Child screens are the only place styling survives. Everything else - agent
// names, state badges, errors, hold reasons and message previews - is stripped
// of ANSI sequences and of every remaining control character before it reaches
// the screen, because a label must never be able to move the cursor or clear the
// screen. Pane content is measured and cut by cell width, not by bytes or runes,
// so a wide character cannot break the layout.
//
// Nothing about pane content is persisted. The UI renders whatever the pane's
// last frame holds; once that is gone, it is gone.
//
// # Input rules
//
// Control keys belong to the application: F1 and F2 focus, F3 toggles the log,
// F4 asks the router to deliver a held message to the focused agent, F10 quits.
// Esc and ordinary keys go to the focused child. The user-decided Ctrl+C policy
// forwards the first press to a ready pane; a second within one second requests
// quit, using the draft state from before the first interrupt. With no receiver
// (including log, too-small, exited or unavailable input), Ctrl+C requests quit
// immediately. Ctrl+C confirms an armed quit warning. The timing is untested.
//
// A draft is reported to the router before a keystroke is written, and only
// Enter with no modifier clears it - after the write succeeded. Deletion,
// interruption, a navigation key, a focus change or a log visit never clear a
// draft, because this UI cannot know what those keys did inside the child.
// Reporting a draft that is not there only holds a message back; missing one
// would deliver a message into a half-typed prompt, so the uncertain answer is
// always "drafting".
//
// The log consumes all input while it is open, so no keystroke reaches a child
// from it. A terminal smaller than 40x10 shows a warning instead of the layout
// and pauses child input, but F1, F2, F3 and F10 keep working.
// Left/Right browses older/newer runs; Up/Down selects a message; Enter opens
// its whole body and state history. Detail is scrollable with Up/Down, page
// keys and Home/End. Esc returns to the list, R refreshes/reloads. Read errors
// remain in feedback and never terminate the UI. All of this is untested.
//
// Quitting warns once when a delivery is pending or a draft is open: the first
// F10 arms the warning, Esc cancels it, the second F10 quits.
//
// # Untested (GR-3)
//
// Nothing here has been run. Under GR-3 (docs/design/ground-rules.md) only
// gofmt, go vet and go build have been applied; no application, harness or test
// binary has been executed, so no behaviour in this package is verified.
//
// View reads cached pane frames/status only. Update submits coalesced resize
// requests and uses the router's atomic Snapshot/PendingOnQuit reads. Router
// writes (draft, submission and explicit delivery) run inside the ordered input
// Cmd, with draft sent before child bytes. A blocked router or third-party input
// Cmd stays isolated; after three seconds the UI labels it INPUT UNAVAILABLE and
// Ctrl+C takes the quit path. There is at most one input Cmd per pane; Bubble Tea
// does not join blocked Cmd goroutines on quit. Go cannot forcibly cancel them.
//
// Senior refinement (all UNTESTED):
//
//   - A banner is set by an event and cleared only by a snapshot, so a router
//     that clears it without Resync leaves it on screen.
//   - One frame snapshot per pane is reused for body and cursor. Snapshot may
//     still wait on the emulator lock during terminal queries or resize.
//   - Input uses one ordered command per pane, bounded to 128 pending actions.
//     Overflow is refused visibly and keeps draft held. An Enter only clears
//     draft after actual transport completion with no newer queued/rejected input.
//   - Active pane headers use colour and bold; text labels also identify focus
//     and state, so colour is supplementary.
//   - Log refresh after message events is coalesced at 250ms; history request
//     generations prevent a closed/reopened log from installing stale results.
//   - Intercepted pastes show feedback, except while the quit warning owns input.
package tui
