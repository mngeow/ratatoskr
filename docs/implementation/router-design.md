# internal/router design and monkey brief 1 (draft)

Owner: `engineering.backend`. Status: **design only. No router code exists** (GR-3: nothing run; and the monkey step comes first). Behaviour named here is untested. Design v0.4 and `interfaces.md` win on conflict.

## 1. Router design

### Shape

One actor goroutine owns all router state; every input is a `func()` sent on one channel. No mutexes. Public methods only submit work and, where the contract returns a value, wait for the reply.

Files: `router.go` (types, `New`, `Start`, `Close`, actor loop, tick), `send.go` (`Inbox.Send`, `SendError` reasons), `hooks.go` (`HookEvent`, `nextState` use, timeouts), `deliver.go` (pump, write goroutine, results), `lifecycle.go` (`Lifecycle`), `views.go` (snapshot, events, history, banners), plus the pure helpers in `frame.go` and `gate.go` (brief 1).

`Config`: `RunID`, `Store` (interface below), `Now func() time.Time`, limits (`BudgetLimit` 20, `MaxBody` 64 KiB, `StartupTimeout`, `BusyTimeout`, `ConfirmTimeout`, `WriteTimeout`, `TickEvery`, `EventBuffer`). All durations are guesses until testing is allowed and must be commented as such.

`Store` interface (defined in router, satisfied by `*store.Store`): `InsertAgent`, `EndAgent`, `InsertMessage`, `Transition`, `MessageExists`, `History`.

### State per agent and message

Agent: id, display name, harness, state, `drafting`, `PaneWriter`, `startedAt`, `lastEvent`, `sawHook`, FIFO `queue`, at most one `inflight`, `confirmDeadline`, `earlyConfirm`, a short list of recent `unconfirmed`, exit code, error text.

Message: id, from, to, reply-to, body, state, reason, time. State is changed only through one helper `move(m, to, reason)` that calls `Store.Transition` first and updates memory and emits an event only if it returned nil.

### Delivery (commit order, invariants 1, 8)

`pump(agent, override)` runs in the actor after Send, any hook event, `UserDraft(false)`, a write result, a tick and `DeliverHeld`:

1. If storage is down, or an `inflight` exists, or the queue is empty: stop.
2. `gate(...)` (or `overrideOK` for F4). If it holds: `move(queued→held, reason)` if still queued; update the in-memory reason otherwise; stop.
3. `move(→delivering)`. **If this fails, nothing is written.** Pop the message into `inflight`.
4. Start a goroutine: `PasteSubmit(ctx, frame)` with `WriteTimeout`, then submit the result to the actor. The actor never writes to a PTY and never waits on one.
5. Result, success: `move(delivering→pasted)`, set `confirmDeadline`. If `earlyConfirm` is set, `move(pasted→accepted)` immediately. Result, error: `move(delivering→failed, "write_failed:<no_bytes_written|partial_write|unknown_if_written>")`. The detail comes from `interface{ BytesWritten() bool }` on the error (pane must not import router, so this is structural), and defaults to unknown_if_written. Then clear `inflight` and pump.
6. A result for a message that is no longer `inflight` (agent exited) is ignored.

### Hooks (design Figure 5)

`HookEvent`: first apply the session binding rules above; for an accepted event update `lastEvent`, `sawHook`; clear that agent's "no hooks" banner; `state = nextState(state, kind)`; then:

- `UserPromptSubmit` with an `inflight` message:
  - `pasted` and (no prompt text, or the prompt contains the message id): `move(→accepted)`, clear inflight.
  - `pasted` and prompt text present but without the id: do **not** credit it. The user submitted something else.
  - `delivering` and (no prompt text or contains id): set `earlyConfirm`. The hook can reach the actor before the write result does.
- `UserPromptSubmit` with prompt text containing the id of a recently `unconfirmed` message: `move(unconfirmed→accepted)` (the late rule; only possible when the payload carries the prompt, which is unverified).
- Then pump.

`PermissionRequest` ⇒ `needs_you`. The router never answers it and the hook never outputs a decision (invariant 2).

### Session binding (design v0.8; all UNTESTED)

Each agent has a `boundSession`. Claude: set at `RegisterAgent` from `LaunchSpec.SessionID`. Codex: set from the first `SessionStart` whose `Source` is `startup` after `PaneStarted`; `resume`, `fork`, `clear`, `compact` never bind. An orphan cannot fire a fresh `startup` SessionStart, which closes most of the Codex first-event gap; whether Codex reports `startup` for a normal interactive launch is unverified.

For every hook event:

1. Unbound agent: ignore everything except the binding SessionStart. State stays `starting` and times out to `unknown` (held, F4).
2. Bound, event `session_id` equals it: process normally.
3. Bound, `session_id` missing or different: **do not apply the event.** Count it (`ignored hook events`, shown in a banner `hooks:<agent>` and kept in the in-memory log), and **fail to hold**: if the state is `idle` it moves to `unknown`; other states are left alone. This slightly extends v0.8's "without changing state". Reason: if the harness itself changes its session ID (`/clear`, `/compact`, resume and fork are suspected, not established, to do so), the router would otherwise keep a stale `idle` while the agent is busy and paste into a running turn. An orphan can then only push an agent towards holding, never towards delivery. Needs architect's OK.
3b. **Recovery (architect, v0.9):** a later event carrying the bound ID moves the agent by the normal `nextState` table from whatever state it is in, including `unknown` (Stop ⇒ idle, UserPromptSubmit ⇒ busy, PermissionRequest ⇒ needs_you, PostToolUse ⇒ busy, SessionStart ⇒ idle). No saved prior state is kept. A stray orphan event therefore costs one held message, not the rest of the run; only a real ID change leaves the agent on F4.
4. A `SessionStart` from a different session never rebinds (an orphan could fire one with its own ID). Known consequence, stated plainly: if `/clear` or `/compact` changes a harness's session ID, every later hook mismatches, so that agent stays `unknown` and needs F4 for the rest of the run. The banner `hooks:<agent>` therefore reads: "<name>: hook events from another session were ignored (<N>). If you ran /clear or /compact in this pane, ratatoskr can no longer track it for the rest of this run. Messages to it are held; F4 delivers."

After a crash recovery that finds unclosed runs, `app` passes that to the router, which raises the banner "Previous run did not exit cleanly. Harness processes may still be running; check for them yourself. ratatoskr does not kill them." The session directory is emptied only after the lock is held (`agent.PrepareSessionDir`).

`RATATOSKR_RUN` is **removed**: it does not help with the Codex first-bind limit, and two mechanisms cost more than they save.

### Timers (one `tick(now)`, driven by a ticker; tests call it directly)

- `starting` for more than `StartupTimeout` after `PaneStarted` ⇒ `unknown`, and a banner "no hook events from <name>: hooks may be missing or not trusted. Messages are held; F4 delivers." (lead's addition; Codex skips untrusted hooks).
- `busy` with no event for `BusyTimeout` ⇒ `unknown`.
- `needs_you` never times out.
- `pasted` past `confirmDeadline` ⇒ `move(→unconfirmed, "no_confirmation")`, remember it for the late rule, clear inflight, pump.

### Send (`Inbox.Send`), in order

storage down ⇒ `storage_unavailable`; unknown sender ⇒ internal error; `to` not exactly one agent, or self ⇒ `unknown_recipient`; recipient exited ⇒ `recipient_offline`; body empty once sanitised and trimmed (`isEmptyBody`) ⇒ `empty_body` (architect-approved, design v0.7 section 8; rejected at acceptance, no row stored); body over `MaxBody` ⇒ `too_large`; `reply_to` set but not in this run ⇒ `unknown_reply_to`; budget used ≥ limit ⇒ `budget_exhausted`. Then `NewMessageID`, `Store.InsertMessage`; on `ErrUnavailable` ⇒ storage banner and `storage_unavailable`. On success increment the budget, enqueue, reply `id`, **then** pump. The reply goes out before delivery, so the sender never blocks on the recipient.

### Lifecycle

`RegisterAgent` inserts the agents row and creates the agent in `starting`. `PaneStarted(id, w)` records the writer and `startedAt`. `PaneFailed` and `PaneExited` set `exited`, call `EndAgent`, move `queued/held` ⇒ `failed ("recipient_exited")` and an inflight `delivering/pasted` ⇒ `unconfirmed ("recipient_exited")`, and emit events. No automatic restart.

### Views

After every actor step the router rebuilds an immutable `protocol.Snapshot` into an `atomic.Pointer`, so `Snapshot()` and `PendingOnQuit()` never touch the actor (a stuck disk must not freeze the UI). Events go on a buffered channel with drop-oldest and `Resync`. Banners are a keyed map ("storage", "hooks:<agent>"); a change emits `Event{Banner, Resync: true}`. `History` calls the store's read handle directly; every body goes through `preview` or `stripControl` before it reaches a `MessageView`.

### Budget

`used` counts accepted sends. `UserSubmitted(agent)` resets the budget **only** (frontend's request, so the gate cannot open between a completed Enter and later keys already queued in the UI); the draft flag is controlled solely by `UserDraft`, which the UI calls with `false` only when no later input is queued. Consequence to state: a UI that forgets `UserDraft(false)` leaves that agent's messages held with reason `drafting`; there is no router-side draft timeout. `DeliverHeld` is `pump(agent, override=true)` and does nothing unless `overrideOK`.

### Open points (need lead / architect)

1. ~~`empty_body` reason~~ approved by architect (design v0.7), with the check after sanitising.
2. `UserSubmitted` fires on any Enter, including answering a prompt; the contract already says this is intended.
3. Hook events from separate `rtk hook` processes may arrive out of order; the rules err toward `busy`, which holds. Untested.
4. The `held` reason is stored only on the first `queued→held` event; later changes of reason are in memory and the UI only.

## 2. Test plan (written by me after the draft, unrun)

Table tests with a fake `Store` that logs calls, a fake `PaneWriter`, and a `Now` I advance by hand: the gate property test; `nextState` table; commit-order (`Transition(→delivering)` precedes `PasteSubmit`; a failing delivering commit means zero writes; `queued` is returned only after `InsertMessage` returns); busy / permission-prompt / draft holds with F4 refused for `needs_you`; early-confirm race; confirm-by-id rules; late `unconfirmed→accepted`; timers; exit transitions; budget stop and reset; overflow drops oldest and sets `Resync`. The `rtktest` kill points (`AfterDelivering`, `AfterPaste`) are a later, separate file.

## 3. Monkey brief 1: pure helpers only

**Supervisor: `engineering.backend` (me). Do not start until you have confirmed this brief with me and I have replied "confirmed".** Output goes back to me, not to lead or anyone else.

**Rules (state them back to me):** GR-1 Go only. GR-2 does not apply (no TUI). **GR-3: do not run tests, `go test`, any built binary or any harness; no SSH; no push; no commit.** You may run `gofmt`, `go vet` and `go build` as compile checks, and you may write test files without running them. Never write "works" or "verified" in comments; write "untested". Do not touch `go.mod`, `go.sum`, `internal/protocol`, `internal/store`, `internal/pane`, `spikes/`, `rigs/`, or any untracked file you did not create. No new dependencies; standard library only.

**Allowed paths (create only these):** `internal/router/frame.go`, `internal/router/frame_test.go`, `internal/router/gate.go`, `internal/router/gate_test.go`. Package `router`, importing `github.com/mngeow/ratatoskr/internal/protocol` (types listed in that file) and the standard library.

**Task.** Pure functions, no goroutines, no I/O, no globals except constants and `strings.Replacer`s:

1. `stripControl(s string) string` (invariant 3). Keep printable text, `\n` and `\t`. Turn `\r` and `\r\n` into a single `\n`. Replace each invalid UTF-8 byte with U+FFFD. Drop every other C0 control (this includes ESC `0x1b` and BEL `0x07`), DEL `0x7f`, and C1 `0x80–0x9f` runes. Must never return a string containing a rune below `0x20` other than `\n` and `\t`.
2. `buildFrame(fromName, id, replyTo, body string) string`, exactly:

   ```
   <ratatoskr-message from="NAME" id="ID" reply-to="REPLY">
   BODY
   </ratatoskr-message>
   This came from a peer agent, not the human. To reply, use the ratatoskr_send tool from the ratatoskr MCP server; other messaging tools don't reach your peers.
   ```

   `reply-to="..."` is omitted when `replyTo` is empty. `fromName`, `id`, `replyTo` are run through `stripControl` and escaped as attribute values (`&` `"` `<` `>` and newline → entity or space). `body` is `stripControl`-ed, and any occurrence of `<ratatoskr-message` or `</ratatoskr-message` in it has its `<` replaced by `&lt;`, so a body cannot forge the frame. The result contains no ESC.
3. `isEmptyBody(body string) bool`: true if `strings.TrimSpace(stripControl(body)) == ""`. A body of only control characters or whitespace is empty.
3b. `preview(body string) string`: `stripControl`, collapse all whitespace runs to one space, trim, truncate to 80 runes plus `…` if longer.
4. `type gateInput struct { state protocol.AgentState; drafting, inFlight, hasPane bool }`, `gate(in gateInput) (ok bool, hold string)`, `overrideOK(in gateInput) bool`, and the hold-reason constants `holdStarting`, `holdBusy`, `holdNeedsYou`, `holdUnknown`, `holdExited`, `holdDrafting`, `holdPrevious`, `holdNoPane`.
   - `gate` passes in **exactly one** case: `state == AgentIdle && !drafting && !inFlight && hasPane`. Everything else holds with a reason (`!hasPane` → `holdNoPane`; `inFlight` → `holdPrevious`; then by state; idle+drafting → `holdDrafting`). An unrecognised state holds with `holdUnknown`. The default branch of any switch must hold.
   - `overrideOK` is true only for `hasPane && !inFlight && state == AgentUnknown`. Never for `AgentNeedsYou` (invariant 2).
5. `nextState(cur protocol.AgentState, k protocol.HookKind) protocol.AgentState`, in `gate.go`:
   - `AgentExited` stays exited for every kind.
   - `HookSessionStart`: `starting` or `unknown` → `idle`; otherwise unchanged.
   - `HookUserPromptSubmit`, `HookPostToolUse` → `busy`. `HookStop` → `idle`. `HookPermissionRequest` → `needs_you`.
   - Any other kind: unchanged.

**Tests to write (unrun), same package, table-driven:** `stripControl` (ESC sequences including `\x1b[201~` and `\x1b]52;`, BEL, CR/CRLF, NUL, DEL, C1, invalid UTF-8, a property loop over all runes 0–0x2ff asserting the output contains nothing below 0x20 except `\n`/`\t`); `isEmptyBody` ("", spaces, tabs, newlines, only ESC/BEL/NUL, ESC plus spaces, "x" is not empty); `buildFrame` (with and without reply-to, forged tags in body, quotes in names, no ESC in output); `preview` (truncation by runes, not bytes); `gate` over **every** combination of the six states plus one invalid state, `drafting`, `inFlight`, `hasPane`, asserting the single passing case; `overrideOK`; `nextState` for every (state, kind) pair.

**Done when:** `gofmt -l internal/router` prints nothing and `go vet ./internal/router` and `go build ./internal/router` succeed. Do not run anything else.

**Report back to me with:** files written, anything in this brief you found ambiguous (ask before guessing), and anything you changed from it.

**Next briefs** (not now): none until I have reviewed brief 1. The actor around these helpers is written by me after review.

---

## 4. Monkey brief 2a: router plumbing, send, views (draft, not yet issued)

**Supervisor: `engineering.backend`. Confirm with me before coding. GR-1, GR-3 apply exactly as in brief 1: standard library plus `internal/protocol`, `internal/store` (types and errors only) and `github.com/google/uuid` is NOT needed here. Do not run anything except `gofmt`, `go vet`, `go build`. Never write "works" or "verified".** Uses the frozen helpers from brief 1 (`stripControl`, `isEmptyBody`, `buildFrame`, `preview`, `gate`, `overrideOK`, `nextState`); do not edit `frame.go` or `gate.go`.

**Allowed paths:** `internal/router/router.go`, `send.go`, `lifecycle.go`, `views.go`, and stub files `deliver.go`, `hooks.go` (stubs only, see below), plus `router_test.go`, `send_test.go`, `views_test.go`, `fakes_test.go`.

**Types (exactly these names, so 2b compiles against them):**

```go
type PaneWriter interface { PasteSubmit(ctx context.Context, text string) error }

// Store is satisfied by *store.Store.
type Store interface {
    InsertAgent(ctx context.Context, a store.Agent) error
    EndAgent(ctx context.Context, id protocol.AgentID, exitCode *int, at time.Time) error
    InsertMessage(ctx context.Context, m store.Message) (int64, error)
    Transition(ctx context.Context, id string, from, to protocol.MessageState, reason string, at time.Time) error
    MessageExists(ctx context.Context, runID, id string) (bool, error)
    History(ctx context.Context, runID string, limit int) ([]store.MessageRow, error)
}

type Config struct {
    RunID string
    Store Store
    Now   func() time.Time // default time.Now
    NewID func() (string, error) // default store.NewMessageID; tests inject
    BudgetLimit int; MaxBody int
    StartupTimeout, BusyTimeout, ConfirmTimeout, WriteTimeout, TickEvery time.Duration
    EventBuffer int
}
// New fills zero fields with defaults (BudgetLimit 20, MaxBody 64<<10, StartupTimeout 60s,
// BusyTimeout 5m, ConfirmTimeout 20s, WriteTimeout 10s, TickEvery 1s, EventBuffer 256).
// Every default carries the comment "UNTESTED guess".
func New(cfg Config) (*Router, error)   // error if RunID or Store is empty
func (r *Router) Start()
func (r *Router) Close()                 // idempotent; stops the actor, cancels write contexts, waits
func (r *Router) Events() <-chan protocol.Event

type agent struct { // actor-owned
    id protocol.AgentID; name, harness string
    state protocol.AgentState; drafting bool
    w PaneWriter
    startedAt, lastEvent time.Time
    boundSession string        // set from LaunchSpec.SessionID or by 2b
    ignoredHooks int
    queue []*msg; inflight *msg
    confirmDeadline time.Time; earlyConfirm bool
    unconfirmed []*msg          // most recent 16, for the late rule
    exitCode *int; err string
}
type msg struct {
    id string; from, to *agent; replyTo, body string
    state protocol.MessageState; reason string; at time.Time
}
```

**Router fields (actor-owned unless noted):** `in chan func()` (buffered, 256, FIFO; `UserDraft`, `UserSubmitted`, `DeliverHeld` and `HookEvent` only enqueue and return, so they are applied in call order and a slow store call cannot freeze the UI; a full buffer blocks the caller, which is acceptable back-pressure), `quit`, `done`, `ctx/cancel`, `events chan protocol.Event`, `snap atomic.Pointer[protocol.Snapshot]`, `pending atomic.Int32`, `agents map[protocol.AgentID]*agent`, `byName map[string]*agent`, `order []*agent`, `log []*msg` (newest 500), `used int`, `banners map[string]string`, `storageDown bool`.

**Behaviour to write in 2a:**
- Actor loop: `select` on `in` (run the func), a ticker (call `r.tick(now)`), and `quit`. After every step call `r.refresh()` (rebuild and store the snapshot, update `pending`). `submit(ctx, fn)` sends on `in` or returns `ErrClosed`/`ctx.Err()`; it never blocks after `Close`.
- `RegisterAgent(ctx, spec)`: `Store.InsertAgent`, create the agent in `starting`, index by id and name (duplicate display name ⇒ error), set `boundSession = spec.SessionID` (new protocol field; if it does not exist yet when you build, tell me, do not edit protocol).
- `PaneStarted(id, w)`, `PaneFailed(id, err)`, `PaneExited(id, code)`: set fields, call `r.markExited(a, ...)` for the last two. `markExited` is **a stub in 2a** that sets state `exited`, calls `Store.EndAgent`, and emits an agent event; 2b adds the message transitions.
- `Send(ctx, from, to, body, replyTo)`: exactly the order in section 1 ("Send, in order"), using `isEmptyBody`, `MaxBody`, `MessageExists`, budget, `NewID`, `InsertMessage`. Reply value is `(id, nil)`. Errors are `*SendError{Reason string}` with exported constants `ReasonUnknownRecipient`, `ReasonUnknownReplyTo`, `ReasonRecipientOffline`, `ReasonBudgetExhausted`, `ReasonTooLarge`, `ReasonStorageUnavailable`, `ReasonEmptyBody` (values are the snake_case strings of design section 8). A `store.ErrUnavailable` from `InsertMessage` sets `storageDown`, adds banner key `"storage"` with the store error text, and returns `ReasonStorageUnavailable`. After the reply is sent, call `r.pump(recipient, false)`.
- `UserDraft`, `UserSubmitted` (resets `used` only, emits budget event, `pump`; it does NOT touch the draft flag), `DeliverHeld` (`pump(a, true)`), `Snapshot`, `PendingOnQuit`, `History` (default run = `Config.RunID`, default limit 200; every body through `preview`).
- Events: `emit` is non-blocking, drop-oldest, and sets `Resync` on the event that follows a drop. Banner changes emit `Event{Banner: joined, Resync: true}`.
- `move(m, to, reason) error` helper: `Store.Transition` first; update memory and emit only on nil; on `store.ErrUnavailable` set `storageDown` and the banner.
- **Stubs for 2b, with these exact signatures and a body that only does nothing:** `func (r *Router) pump(a *agent, override bool)`, `func (r *Router) hook(a *agent, ev protocol.HookEvent)` (and `HookEvent(ctx, from, ev)` which submits `r.hook`), `func (r *Router) tick(now time.Time)`. `markExited` as above.

**Tests to write (unrun), with a fake `Store` that records an ordered call log:** send validation order and each reason; `queued` is returned only after `InsertMessage` returned (the fake blocks `InsertMessage` and the test asserts `Send` has not returned); no row stored for refused sends (fake sees no `InsertMessage`); storage failure ⇒ banner and `ReasonStorageUnavailable`; budget stops at the limit and `UserSubmitted` resets it; event channel overflow drops oldest and sets `Resync`; `Snapshot` and `PendingOnQuit` do not block while the actor is blocked in the fake store; `Close` is idempotent and unblocks waiting `Send`s with an error.

## 5. Monkey brief 2b: delivery, hooks, timers, binding (draft, not yet issued)

Same supervision and rules. **Allowed paths:** `deliver.go`, `hooks.go`, `lifecycle.go` (only `markExited`), `deliver_test.go`, `hooks_test.go`, plus additions to `fakes_test.go`. Do not change 2a signatures; if one must change, ask me.

Starting point is the accepted, reviewed 2a code in `internal/router` (read it first; `Send` already calls `pump` after replying, `tick` receives `r.now()`, `emit` marks the event after a drop with `Resync`, `pendingCount` counts queues plus in-flight). Implement `pump`, `hook`, `tick`, write-result handling and `markExited` exactly as in section 1 ("Delivery", "Hooks", "Session binding", "Timers", "Lifecycle"). Key rules to restate: `move(→delivering)` must succeed before the write goroutine starts, and its failure means no write; the actor never calls `PasteSubmit`; the write goroutine uses a context with `WriteTimeout` derived from the router context; a write error becomes `failed` with reason `write_failed:` plus `no_bytes_written`, `partial_write` or `unknown_if_written`, from `interface{ BytesWritten() bool }` via `errors.As`; a result for a message that is no longer `inflight` is ignored; `UserPromptSubmit` during `delivering` sets `earlyConfirm`, applied straight after `pasted` commits (no `delivering→accepted` edge); with prompt text, credit only a prompt containing the message id; the late `unconfirmed→accepted` rule needs the id in the prompt; session binding rules 1–4 including idle ⇒ unknown on a mismatch; `starting` past `StartupTimeout` ⇒ `unknown` with banner `hooks:<agent>`; `busy` past `BusyTimeout` ⇒ `unknown`; `needs_you` never times out; `pasted` past `confirmDeadline` ⇒ `unconfirmed("no_confirmation")`; `markExited` fails queued messages and makes in-flight ones `unconfirmed("recipient_exited")`.

**Tests (unrun):** table tests driving `hook`, `pump` and `tick` directly in the actor goroutine's place (tests call the methods on a router that was never `Start`ed, which is why they are unexported and single-threaded); a fake `PaneWriter` that records calls and can block, fail, or report `BytesWritten`; fake clock. Cover: the commit-order sequence in the fake store's call log; failing `delivering` commit ⇒ zero `PasteSubmit` calls; busy / needs_you / drafting / unknown holds; F4 allowed for `unknown`, refused for `needs_you`; early-confirm race; foreign prompt not credited; late accept; every timer; exit transitions; session binding table including Codex `startup` bind and non-`startup` SessionStart not binding; mismatch while idle ⇒ unknown, then a matching Stop ⇒ idle and a matching UserPromptSubmit ⇒ busy (recovery uses `nextState`, no saved state); a mismatch while busy or needs_you leaves the state alone.

I write the `rtktest` kill points and the extra race-sensitive tests myself afterwards.


## Amendment 2026-11-05: Codex SessionStart binds but does not make the agent idle

Codex sends its first hook only when its first turn starts, so for Codex a SessionStart means "a turn is starting", not "ready". Delivering on it pasted a held message into a turn that had just begun (run 01a1a918). Rules, all UNTESTED:

- `nextState` is unchanged and pure. `hook()` skips the `nextState` call for a SessionStart when `sessionStartLifts(a.harness)` is false, which is true only for the harness named exactly `codex` (`internal/router/gate.go`). Every other harness name keeps the old rule.
- A Codex SessionStart still binds the session (`acceptHook`), sets `lastEvent`, clears the hooks banner and pumps; the gate then holds because the state is still `starting` or `unknown`. Codex becomes busy on UserPromptSubmit and idle on Stop, as before.
- A late SessionStart no longer lifts a Codex `unknown` agent to idle. F4 on `unknown` is unchanged.
- The startup timeout still moves a `starting` agent to `unknown`, but raises the "no hook events" banner only when no event was ever accepted (`lastEvent` zero). A bound Codex agent that then goes silent shows `unknown` without a banner; accepted for now.
- Not traced: how the TUI words `starting`; it is outside the router.
