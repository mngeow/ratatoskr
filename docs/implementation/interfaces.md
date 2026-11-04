# Integration contract (working draft)

Owner: `delivery.lead`. Status: **draft, nothing implemented or tested**. Implements the seams in design v0.4 (`docs/design/mvp-two-pane-bridge.html`); the design wins on any conflict. Engineers propose changes to lead. Changes to a design contract go through lead to architect.

**Testing is stopped by the user (GR-3, `docs/design/ground-rules.md`).** Spikes 1, 2 and 3 were cancelled before producing results, so every behaviour here is unverified. Details marked UNVERIFIED are the ones the spikes would have settled: keep each behind one small function, with a code comment saying it is untested, so it is cheap to change once the user has tests run.

## Layout and ownership

| Path | Owner | Holds |
| --- | --- | --- |
| `internal/protocol` | lead | Shared value types for these seams. No behaviour. Propose changes to lead. |
| `cmd/rtk` | lead | Binary entry point, subcommand dispatch (`rtk`, `rtk hook`) |
| `internal/app` | lead | Startup order, wiring, shutdown, top-level exit errors |
| `internal/tui` | frontend | Bubble Tea model: layout, focus, status, log view, key bindings |
| `internal/pane` | frontend | PTY, `x/vt` emulator, input translation, paste primitive |
| `internal/router` | backend | Queues, agent state, delivery gate, relay budget, frame building |
| `internal/store` | backend | SQLite, migrations, recovery, retention, lock file |
| `internal/server` | backend | Localhost HTTP: MCP tool, hook events, per-agent tokens |
| `internal/agent` | backend | Per-harness launch spec and session directory |
| `go.mod`, `go.sum` (root) | lead | Engineers name what they need; lead serializes edits. Module: `github.com/mngeow/ratatoskr` |
| `README.md` | lead | Setup README. Commands stay marked unverified until the user has tests run. |

Dependency direction: `tui` → `pane` and the `router` interface below. `router` → `store` and the `PaneWriter` interface. `pane` imports no `router`, `store` or `server`. `app` is the only package that knows them all. Interfaces live with the consumer. All packages may import `protocol`; the types in the sketches below are defined there.

## router → pane: writing to a pane

```go
// PaneWriter is everything the router may do to a pane. Defined in router.
type PaneWriter interface {
    // PasteSubmit writes one bracketed paste, then Enter as a separate write.
    // It returns nil only if every byte was written to the PTY, and otherwise
    // the error, saying whether any bytes were written. If ctx ends first it
    // returns an error and must not write Enter afterwards.
    // It does not gate, retry or sanitize-and-continue.
    PasteSubmit(ctx context.Context, text string) error
}
```

- The router builds the frame and strips control characters (invariant 3). As a second guard, `PasteSubmit` refuses text containing ESC, without writing anything, and returns an error. A refusal means a router bug and becomes `failed`.
- The paste-to-Enter timing is UNVERIFIED. It stays inside `PasteSubmit`.
- It writes directly to the PTY, not through `x/vt`'s `Paste`, which reports no write errors.
- A timeout or partial write becomes `failed`, never `pasted`, and the reason says whether bytes were written (invariant 7).
- This is the only code path that sends messages to a pane (invariant 1). The only other PTY writes are forwarded user input and the emulator's replies to terminal queries. Those replies carry no message content.

## tui → router: what the UI tells the router

```go
// Defined in tui, implemented by router.
type Router interface {
    UserDraft(agent AgentID, drafting bool)  // from tracked keystrokes, see below
    UserSubmitted(agent AgentID)             // user pressed Enter in the pane: resets relay budget only
    DeliverHeld(agent AgentID)               // F4: explicit user override for `unknown`
    Snapshot() Snapshot                      // full state for first paint and after a resize
    History(q HistoryQuery) ([]MessageView, error) // F3 log, reads the store
    Runs(limit int) ([]RunView, error)       // F3 log: this run and older ones, newest first
    MessageDetail(id string) (MessageDetail, error) // F3 log: full sanitized body and state history
    PendingOnQuit() int                      // quit warning
}
```

`UserSubmitted` also fires when the user answers a permission prompt with Enter, which resets the budget. That is intended, since a human acted.

`UserSubmitted` doesn't clear the draft. `tui` calls `UserDraft(agent, false)` itself, only after Enter has been written to the PTY and no later input is queued for that pane, and calls `UserDraft(agent, true)` before forwarding any input. Input moves through a bounded, ordered queue off the UI thread, so a clear can't overtake keys typed after Enter.

Draft tracking belongs to `tui`, the only place that sees keystrokes. Its exact rules are UNVERIFIED. Uncertainty must report `drafting=true`, because holding is the safe side.

## router → tui: what the UI shows

The router publishes on a buffered channel consumed through a Bubble Tea `Cmd`. The router must never block on the UI: on overflow it drops the oldest event and sets `Resync`, which makes the TUI call `Snapshot()`.

```go
type Event struct {
    Agent   *AgentView   // state, held count
    Message *MessageView // id, from, to, state, reason, sanitized preview
    Budget  *BudgetView
    Banner  string       // e.g. storage failure
    Resync  bool
}
```

All message text reaching the UI is sanitized before display (design section 9).

## app → pane and router: lifecycle

```go
// Produced by agent, consumed by app and pane.
type LaunchSpec struct {
    ID, DisplayName, Harness string
    Argv, Env []string // Env is complete, not inherited implicitly
    Dir string
}
```

```go
// Defined in app, implemented by router.
type Lifecycle interface {
    RegisterAgent(ctx context.Context, spec protocol.LaunchSpec) error // creates the agents row
    PaneStarted(id protocol.AgentID, w router.PaneWriter)
    PaneFailed(id protocol.AgentID, err error)
    PaneExited(id protocol.AgentID, code int)
}
```

- `pane` reports process exit through a callback, and `app` forwards it. The router alone applies the message transitions in design section 11.
- Tokens: `server.AddAgent(id, displayName) (token string)` mints one per agent. `app` calls it after the server starts and passes the token to `agent.Build`. Tokens are never stored in the database.
- Startup order: lock → open store and migrate → recover old runs and retention → empty session dir → create and `Start` router (its calls block until then) → start server → for each agent `AddAgent`, `agent.Build`, `RegisterAgent` → start panes → run TUI. Any failure before panes start exits with a clear error and launches no harness.

## server → router

```go
// Defined in server, implemented by router.
type Inbox interface {
    Send(ctx context.Context, from AgentID, to, body, replyTo string) (id string, err error) // returns only after commit; errors map to section 8 reasons
    HookEvent(ctx context.Context, from AgentID, ev HookEvent)  // SessionStart, UserPromptSubmit, Stop, PermissionRequest, PostToolUse
}
```

`rtk hook` must **never influence the harness's decision**. It reports the event and exits 0 with no decision output, even if the report fails. A `PermissionRequest` hook that answered the prompt would break invariant 2.

The hook command uses the absolute path of the running binary and passes `--event`. It reads the server URL and token from a 0600 file in the session directory, because env inheritance into hooks is unverified.

An agent with no hook event a set time after `PaneStarted` moves from `starting` to `unknown` and raises a banner. It still holds, and F4 can deliver. Without this, skipped hooks would leave messages stuck with no way out. `DeliverHeld` overrides `unknown`, and `drafting` on an idle agent, clearing that flag (design v0.10). It never delivers into `needs_you` or `busy`.

## Test seams

Kill and storage-failure acceptance checks (design section 12, checks 10 and 12) use seams compiled only with the `rtktest` build tag. The release build contains none of them. Test files may be written but not run under GR-3.

## Ground rules gate

The ground rules apply to everything above. Under GR-3, gofmt, go vet and `go build` are the only checks that run, and nothing built is run. The root `go.mod` must have no direct dependency on `github.com/charmbracelet/bubbletea` or on another TUI framework. See `docs/design/ground-rules.md`.
