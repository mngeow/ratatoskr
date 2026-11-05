# Backend structure note (draft)

Owner: `engineering.backend`. Status: **design only; nothing implemented, nothing run** (GR-3). Behaviour claims below are assumptions from docs, `--help` and SDK source until the user allows testing. Design v0.4 wins on conflict; changes to it go through lead to architect.

## Packages

| Package | Holds | Imports |
| --- | --- | --- |
| `internal/store` | SQLite, migrations, recovery, retention, lock file. One writer goroutine. | `protocol` |
| `internal/router` | One actor goroutine owning agent state, per-recipient queues, gate, budget, frame building. | `protocol`, `store` (via a small interface it defines), `PaneWriter` |
| `internal/server` | Loopback HTTP: stateless MCP `ratatoskr_send`, hook endpoint, per-agent bearer tokens. `RunHook` for `rtk hook`. | `protocol`, `go-sdk` |
| `internal/agent` | `LaunchSpec` builder and session dir per harness. Pure functions plus file writes. | `protocol` |

Interfaces live with the consumer. `store` knows nothing of `router`.

## store

- `modernc.org/sqlite`, WAL, `synchronous=FULL`, `busy_timeout`. One goroutine owns the `*sql.DB` writes; callers send a closure-free request struct on a channel and wait on a reply channel or `ctx`. Reads (history) use a second read-only handle.
- API is operation-shaped, not generic SQL: `InsertMessage(ctx, m)`, `Transition(ctx, id, from, to, reason)` (UPDATE ... WHERE state = from, plus event row, one tx; returns `ErrStateChanged` if no row matched), `InsertAgent`, `EndAgent`, `StartRun`, `EndRun`, `Recover`, `Retain`, `History`.
- `Open` order: dir 0700 → `flock` lock file → open db 0600 → check `user_version` (newer → `ErrNewerSchema`, older → `VACUUM INTO` backup then migrate in one tx). Lock via `syscall.Flock` (stdlib, macOS and Linux). Released by the OS on crash.
- Recovery and retention run in `Open`'s caller-visible `Recover()` before any harness starts, each in one tx, using the table in design section 9.
- Embedded migrations via `embed`; `0001_init.sql`.
- Storage failure: the writer goroutine marks the store `unavailable` after a failed commit; every later call returns `ErrUnavailable` and the router raises a banner. No retry that could double-apply.

## router

- Single actor goroutine; inputs (Send, HookEvent, UserDraft, UserSubmitted, DeliverHeld, PaneStarted/Failed/Exited, delivery results, timer ticks) arrive on one channel. No locks in the router.
- The actor never does a PTY write or a long store call inline. Commit-order flow for a delivery: actor picks a message → store `Transition(held→delivering)` (waits on writer; short) → hands `(id, frame)` to a per-agent delivery goroutine → goroutine calls `PaneWriter.PasteSubmit` → posts result back → actor commits `pasted` or `failed`. One in-flight message per recipient is enforced in the actor, so a late confirmation cannot be credited to another message.
- Time is injected (`Clock` with `After`/`Now`) so timeouts (busy→unknown, pasted→unconfirmed) are table-testable without sleeping.
- Gate (`canDeliver`) is a pure function of `(agentState, drafting, inFlight, pendingOverride)`; default branch is hold. `DeliverHeld` overrides only `unknown`; it refuses `needs_you`.
- Frame building and control-character stripping are pure functions (`buildFrame`, `stripControl`): drop C0 (except `\n`, `\t`), DEL, C1, ESC, and the paste-end sequence's parts; attribute values (from, id, reply-to) are escaped.
- UI events: buffered channel, drop oldest, set `Resync`.

## server

- `net.Listen("tcp", "127.0.0.1:0")`; refuses non-loopback binds.
- MCP via `mcp.NewStreamableHTTPHandler` with `Stateless: true`, `JSONResponse: true`; `getServer` resolves the agent from the bearer token on each request, so the sender is never a parameter (invariant 4). Token compare is constant-time. Unknown token → 401, no detail.
- Hook endpoint `POST /hook` with the same token scheme. Body: harness hook JSON from stdin, plus the event name from `--event`.
- `RunHook(args, stdin)` reads `<session-dir>/<agent>.hook` (url + token, mode 0600) rather than relying on the harness passing an env var to hook subprocesses, which is unverified. It always returns nil; the only output is an optional stderr line.

## agent

- `Build(harness, id, displayName, dir, serverAddr, token, sessionDir) (LaunchSpec, error)`; writes `mcp.json`, `settings.json` (Claude) and the hook files into the 0700 session dir. Complete `Env` (clean base plus `RATATOSKR_TOKEN`); `TMUX*`, `OPENRIG_*`, `RIG_*` never copied.
- Everything unverified (Codex nested `-c` shape, hook trust, flag names) sits in one small function per harness with an `UNTESTED:` comment citing its source.

## What is wrong or missing in the seams (`interfaces.md`)

1. **No lifecycle interface on the router.** `PaneStarted/PaneFailed/PaneExited` are described in prose but are in no Go interface. Proposal: `app`-consumed `Lifecycle` interface in router, plus `RegisterAgent(spec)` called before `PaneStarted` so the `agents` row exists.
2. **`PasteSubmit(text)` has no context or deadline.** A child that stops reading its PTY blocks the write forever. Proposal: `PasteSubmit(ctx, text) error`; on timeout the result is `failed` with a reason, never `pasted`.
3. **`Inbox.Send` and `HookEvent` take no context.** Send must be cancellable at shutdown. Proposal: `Send(ctx, ...)`, `HookEvent(ctx, ...)`.
4. **Tokens have no owner.** The server must mint them and `agent` must receive them, but the contract names neither. Proposal: `server.AddAgent(id, displayName) (token string)`, called by `app` after the server starts and before `agent.Build`. This forces the order already in the contract (server → session dir → panes).
5. **`HookEvent` needs a source of the event name.** The payload's own `hook_event_name` is unverified for Codex, so the hook command passes `--event` too. `protocol.HookEvent` is fine; the server maps both.
6. **`UserSubmitted` doubles as the budget reset.** It also fires when the user answers a permission prompt with Enter, which resets the budget. Acceptable (a human acted) but should be stated in the design.
7. **Hooks silently skipped (Codex trust review).** The result is an agent stuck in `starting` and every message held, which is honest but opaque. Proposal: the router raises a banner when an agent has no hook event a set time after `PaneStarted`. No contract change; one `Banner` string.
8. **`Event.Agent`/`Message` pointers** are fine, but `AgentView.State` should carry the held reason for the log (`held: codex busy`). Proposal: add `HeldReason string` to `MessageView` (already `Reason`; just define it).

## Tests (written, unrun)

- Router table tests drive `buildFrame`, `stripControl`, `canDeliver`, the state machine and budget with a fake `Clock`, fake `PaneWriter` and in-memory fake store.
- Commit-order invariants: a fake store records the call sequence; assert `Transition(→delivering)` precedes `PasteSubmit`, and that a failing `delivering` commit means zero writes (check 12). Assert `queued` is returned only after `InsertMessage` returns (invariant 8).
- Kill points (check 10): `//go:build rtktest` file in `router` exposing `KillAt(point)` hooks (`AfterDelivering`, `AfterPaste`) that `os.Exit` the process; `store` exposes `FailNextCommit()` the same way. Release build has neither. The harness that would run these is not written until testing is allowed.
- Store tests use a temp dir and real SQLite: recovery table, retention, `user_version`, lock contention, permissions.
- Server tests use `httptest`; `RunHook` tests assert exit-0 and no stdout on every failure.

Nothing above has been executed.
