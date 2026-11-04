# TUI initial version brief

Supervisor engineering.frontend; selected parent qitem-20261104095414-220ec49432d2f84d. Await a dispatch and explicit go; backend has next monkey slot after pane. This is a design, not permission to edit yet.

Goal: Bubble Tea two-pane client with focus, status, log and conservative draft tracking; app supplies already started panes or their launch errors. All behavior untested. Read interfaces.md, ground-rules.md, protocol.go, pane.go and design sections 2/7/10.

Allowed edits internal/tui/model.go and internal/tui/doc.go only. Go application code (GR-1); charm.land/bubbletea/v2 with existing lipgloss/v2 and x/ansi companions only (GR-2). No module/protocol/other-package/doc/rig/spike edits. GR-3: no tests or app/harness/binary runs, probes, SSH testing, commits or push. gofmt/go vet/go build compile-only allowed; no go mod tidy. Senior authors tests and refines after draft.

Public API:

    type Pane interface {
        Resize(int,int) error
        Snapshot() pane.Frame
        SendKey(uv.KeyPressEvent) error
        Paste(string) error
        Err() error
    }
    type Slot struct { ID protocol.AgentID; Name string; Pane Pane; StartErr error }
    type Router interface { /* exact protocol-typed methods in interfaces.md */ }
    func New(router Router, events <-chan protocol.Event, slots []Slot) (*Model,error)

New validates nonnil router, exactly two unique nonempty slot IDs. Nil pane with StartErr is valid (show failure); nil pane without error should show offline. Model implements tea.Model pointer receiver; app owns process cleanup, UI must not launch/kill anything. Init takes Snapshot and schedules one event wait plus 30Hz refresh tick. Closed/nil event channel stops consumption; only one event wait at a time. Event Resync calls Snapshot. Apply other event values by agent/message ID; keep latest 200 messages. No producer changes or polling router every tick. View obtains pane frames; no pane output persistence. Wire names and badges from protocol, show Held and relay budget, failed starts remain visible while other pane usable.

Layout: title/budget; two pane headers; side-by-side PTY screens separated by one column; two recent message rows; feedback/banner row; hints row. Reserve 7 rows total for chrome, use remaining for screen; exactly fit view width/height. Use x/ansi cell-aware truncate/pad on emulator content (preserve SGR), not byte/rune slicing. Only child screens may contain ANSI; strip ANSI and all remaining control characters from labels/errors/message metadata/previews before display. Do not persist pane data. If terminal <40 cols or <10 rows, show size warning and intercept all child input until usable; still allow F10 quit. Resize both panes; no resize when invalid. Cursor on focused child with position offset and hidden flag respected. View AltScreen=true; outer terminal keyboard enhancements default, no event-type/all-key request needed.

F1/F2 focus panes, F3 toggle log, F4 deliver held for focused agent (router owns gate), F10 quit. Never map Ctrl+C to app quit: forward to child. Quit with PendingOnQuit()>0 OR any draft displays warning and requires second F10; Esc cancels warning, other input dismissed (not forwarded). Log mode consumes all input; Up/Down/PgUp/PgDown scroll, F3/Esc leave, F1/F2 return panes, F10 quit. History fetch through tea.Cmd, errors shown, no input goes to child from log. Render state/reason/time/from/to/id/preview, bounded query Limit=200 and explicit current-run history.

Draft tracking UNTESTED: before forwarding any user key/paste call UserDraft(id,true), including cursor/navigation/history/deletion/Esc/Ctrl+C; never infer that deletion/interrupt cleared a draft. Bare Enter only (no modifiers) after successful SendKey calls UserSubmitted, clears draft and UserDraft(false). Shift+Enter and Ctrl+J retain draft. Failed writes never call UserSubmitted. Focus/log changes do not clear drafts. On PasteStartMsg mark draft; PasteMsg forwards Content via Pane.Paste; PasteEndMsg does not clear it. Call draft=true even when an input might mutate only a harness menu; conservative holding is intentional. Do not call UserSubmitted for log/dialog/global keys. Do not try to infer child state from screen text.

Forward tea.KeyPressMsg by converting Code/Text/Mod/ShiftedCode/BaseCode/IsRepeat explicitly to uv.KeyPressEvent (inspect actual struct). Use pane's fallback; surface unsupported-key errors in feedback. Preserve input order. Synchronous pane input is acceptable for first draft but name responsiveness risk; senior will refine bounded ordered input if necessary. No unbounded goroutines per key.

Acceptance: gofmt your files, go build -mod=readonly ./internal/tui, go vet -mod=readonly ./internal/tui. Nothing built runs. Stop and return draft, exact checks, known gaps, files changed. No tests in first draft, no behavior claims.
