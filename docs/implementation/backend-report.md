# Backend report: store, router, server, agent

Owner: `engineering.backend`. Written 2026-11-04 under GR-3. **Nothing described here has been run.** The only checks applied are `gofmt`, `go vet` and `go build`, each with and without the `rtktest` tag, over `internal/store`, `internal/router`, `internal/server` and `internal/agent`; all four succeed, and `go build ./...` succeeds. `go vet` type-checks the test files, so they compile; none has been executed. Nothing below should be read as "works" or "verified".

## What exists

| Package | Contents | Provenance |
| --- | --- | --- |
| `internal/store` | SQLite schema and embedded migration, single writer goroutine, commit-order API (`InsertMessage`, `Transition` with the lifecycle graph), `FinishRun`, `Recover`, `Retain`, history reads, per-machine lock, `UnavailableCause`, `FailNextCommit` (rtktest) | Written by backend directly (process deviation, settled by the operator) |
| `internal/router` | Pure helpers (`stripControl`, `isEmptyBody`, `buildFrame`, `preview`, `gate`, `overrideOK`, `nextState`), the actor (send, lifecycle, views, banners, events), delivery pump, write results, hooks and session binding, timers, `markExited`, rtktest kill points | Monkey briefs 1, 2a, 2b; reviewed and corrected by backend |
| `internal/server` | Loopback HTTP, per-agent tokens, stateless MCP `ratatoskr_send`, `/hook`, `RunHook`, `WriteHookFile` | Monkey brief 3; reviewed by backend |
| `internal/agent` | `Build` (Claude and Codex), `HookCommand`, env filtering, `childTerminalEnv`, `PrepareSessionDir`, `Version` | Monkey brief 4; reviewed by backend |

Unrun tests are written for every package, plus the `rtktest` seam tests. Design documents: `backend-structure.md`, `router-design.md`, `server-agent-design.md` in this directory.

## Defects found in review (all fixed, all by reading)

- Router 2a: tick used the ticker's wall time instead of the injected clock; `Resync` was set on the wrong event after a drop; `Send` delivered before replying; `PendingOnQuit` counted a capped log.
- Router 2b: `onWriteResult` released the in-flight slot after a successful paste, so a pasted message was never tracked: no timeout, no hook confirmation, and a second message could be pasted on top. Two draft tests encoded the bug. Fixed with tests for the slot staying taken.
- Agent: an unused function removed.
- Server: none found beyond a missing `recover()` in `RunHook`, added.

## Departures from, and refinements of, the design

1. **Fixed session directory** (`<state dir>/session`) instead of a random one, so the hook command is identical across runs and Codex's trust review does not re-prompt (lead and architect agreed).
2. **Idle to unknown on a session-ID mismatch** (fail to hold). Extends v0.8's "no state change". Architect approved (v0.9). Consequence stated plainly: if `/clear` or `/compact` changes a harness's own session ID, that agent stays `unknown` and needs F4 for the rest of the run.
3. **`empty_body` send reason**, checked after sanitising (v0.7).
4. **F4 also overrides an idle agent over a draft**, and `DeliverHeld` clears the draft (v0.10). `UserSubmitted` resets the budget only; `UserDraft` alone controls the draft flag.
5. **`RATATOSKR_RUN` removed**; session binding replaces it.
6. **The held reason is stored once** (on `queued` to `held`); later reason changes are in memory and the UI only.
7. **`hook` events from an unbound agent are ignored without counting** (the agent is waiting, not being attacked).
8. **One Claude per run:** the Claude files are `claude-mcp.json` and `claude-settings.json`, not per agent. A second Claude agent in the same session directory would overwrite them.

## Assumptions, by location (all untested)

Harness behaviour:

- Codex nested `-c` shapes for MCP and hooks: `internal/agent/codex.go:30` (`codexSpec`). Least certain item in the package. Fallback if it fails would put a file in the user's project, which needs a design decision.
- Codex hook timeout unit: `internal/agent/hooks.go:22`.
- Claude `--mcp-config` and `--settings` JSON shapes, `${VAR}` not relied on, no re-prompt for hooks from `--settings` assumed: `internal/agent/claude.go:62`, `:101`, `:115`.
- Claude accepts `--session-id <uuid>`: from `claude --help` 2.1.311, `claude.go:62`.
- `session_id`, `prompt`, `source` present in hook stdin for both harnesses (docs read, partly through summaries): `internal/server/hook.go:70`.
- Codex reports `source` = `startup` on a normal launch, so binding works: `internal/router/hooks.go:14`.
- Hooks fire in documented order and complete before the harness continues; `nextState` rules: `internal/router/gate.go:101`.
- A paste at a permission prompt: not tested and not assumed safe; the gate holds in every state except idle (not drafting) and the F4 cases: `internal/router/gate.go` (`gate`, `overrideOK` at `:80`).
- Whether a hook subprocess can reach loopback inside Codex's sandbox, and whether Stop fires after an interrupt: unknown; both end in `unknown` and F4.

Libraries and platform:

- `modernc.org/sqlite` DSN pragma syntax and multi-statement `Exec`: `internal/store/store.go:122` (`dsn`), `internal/store/migrate.go:92`.
- `VACUUM INTO ?` accepts a bound parameter: `internal/store/migrate.go:75`.
- `flock` semantics (second open in the same process conflicts; OS releases on crash): `internal/store/lock_unix.go:14`.
- The SDK handler honours `Stateless`, `JSONResponse` and `MaxRequestBodyBytes` as read from v1.8.0 source: `internal/server/mcp.go:125`.

Guessed numbers (each marked in code): budget 20, startup 60 s, busy 5 min, confirm 20 s, write 10 s (`internal/router/router.go:68-73`); server header timeout and body limit (`internal/server/server.go`); hook payload cap and POST timeout (`internal/server/runhook.go:21`, `:25`); version probe 5 s (`internal/agent/version.go:13`).

## What is not covered

- No app wiring, no `cmd/rtk`: lead's. Lead must call `Router.Close`, then `store.FinishRun`, then close panes; `os.Exit(0)` after `RunHook`; `store.Recover` before `StartRun`; `agent.PrepareSessionDir` only after `store.Open`; `server.WriteHookFile` after `AddAgent`; check that `AddAgent` did not return an empty token.
- Acceptance checks 3 to 14 need running software. The kill points (`RTK_TEST_KILL_AT` = `afterDelivering`, `afterPasteWrite`, `afterPasted`) and `store.FailNextCommit` exist for checks 10 and 12, with no runner written.
- Windows: not supported (deferred); `lock_other.go` returns an error.
- No retry or ordering guarantees beyond FIFO per recipient inside one process.
- Known low items left alone: `stripControl` keeps U+2028/2029 and bidi controls (display only); `Send` or `RegisterAgent` before `Start` waits until their context ends.
