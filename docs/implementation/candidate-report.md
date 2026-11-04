# Integrated candidate report

> This is the first candidate's report, kept as history. [outstanding-issues.md](outstanding-issues.md) is current.

Owner: `delivery.lead`, 2026-11-04. Working tree only: **no commits, no push.** Design v0.11.

**Nothing has been run.** Under GR-3 the only checks applied are `gofmt`, `go vet`, `go build` and module commands. This report does not claim that `rtk` works: it has never been started, no test has ever been executed, and no harness has ever been launched by this code. `go test -race` was not run. No Ubuntu check was made.

## What exists

| Path | Holds | First version from |
| --- | --- | --- |
| `cmd/rtk`, `internal/app` | Entry point, startup and shutdown order | Lead, directly |
| `internal/protocol` | Shared value types | Lead, directly |
| `internal/store` | SQLite record, recovery, retention, lock | Backend, directly (see Process correction) |
| `internal/router` | Gate, delivery, hooks, session binding, budget | Monkey, reviewed and corrected by backend |
| `internal/server` | Loopback HTTP, MCP, `/hook`, `rtk hook` | Monkey, reviewed by backend |
| `internal/agent` | Harness launch specs and session directory | Monkey, reviewed by backend |
| `internal/pane`, `internal/tui` | PTY, `x/vt`, Bubble Tea UI, F3 log | Monkey, reviewed and refined by frontend |
| `README.md` | Setup, with commands marked unverified | Lead |

About 7,800 lines of Go plus about 6,500 lines of tests, which compile under `go vet` and have never run.

## Evidence: compile checks, run by lead on the final tree

macOS (darwin/arm64), Go 1.27.3:

| Check | Result |
| --- | --- |
| `gofmt -l cmd internal` | no files listed |
| `go build ./internal/... ./cmd/...` | exit 0 |
| `GOOS=linux go build ./internal/... ./cmd/...` | exit 0; this is cross-compilation only, not a Linux run |
| `go build -tags rtktest ./internal/... ./cmd/...` | exit 0 |
| `go vet ./...` and `go vet -tags rtktest ./...` | exit 0 |
| `go mod verify`; `go mod tidy` leaves `go.mod` unchanged | all modules verified; stable |

Ground rules: all application source is Go. `go list -m all` shows Bubble Tea only as `charm.land/bubbletea/v2` v2.1.1, and no other TUI framework. GR-3: no tests or binaries run by any seat, per each seat's report.

## What lead actually read

Read: all of `store` non-test source (`store-review.md`), `pane.PasteSubmit`, `pane.Close` as first delivered, `router/gate.go`, `tui` draft handling, `agent/claude.go`, `agent/codex.go`, and parts of `server` and `router`. Not read line by line by lead, and resting on the owner's review: the rest of `router`, `server`, `tui` including the F3 log and the refined `Close`, and all test files.

## Acceptance checks (design section 12)

"Path exists" means the code for it exists. **It doesn't mean the check passes. All 14 remain unrun.**

| # | Check | Status |
| --- | --- | --- |
| 1 | One window with both harnesses, macOS and Ubuntu, over SSH | Path exists. Not run. Biggest unknowns: `x/vt` and Codex `-c` shapes |
| 2 | Each pane usable as on its own terminal | Path exists. **At risk:** `x/vt` has no Kitty/CSI-u; see `pane-risks.md` |
| 3, 4 | Claude asks Codex and the reply returns; and the reverse | Path exists. Not run |
| 5 | Busy recipient gets it after its turn, once, in order | Path exists. Not run |
| 6 | Permission prompt untouched; delivered after you answer | Gate holds on `needs_you`; what a paste does there has never been seen |
| 7 | ESC body arrives as harmless text | Path exists; tests unrun |
| 8 | Agents stop at the budget | Path exists; tests unrun |
| 9 | Log shows previous run with state history | Path exists, added at integration. Not run |
| 10 | `kill -9` gives the matching reason, nothing resent | `rtktest` kill points exist. **No runner written** |
| 11 | Second instance refuses | Path exists. Not run |
| 12 | Forced write failure stops delivery | `FailNextCommit` exists. **No runner written** |
| 13 | Permissions, no tokens in DB, retention | Path exists; tests unrun |
| 14 | `~/.claude`, `~/.codex` unchanged | By construction nothing here writes there; never observed |

## Least certain assumptions, most likely to break first

1. Embedded panes under real harnesses: query replies, redraw, keys (`pane-risks.md`).
2. Codex nested `-c` config for MCP and hooks (`internal/agent/codex.go`). The docs show hooks only as file tables. If it fails, the fallback puts a file in the user's project, which needs a design decision.
3. Codex reporting `source=startup`, and `session_id` in hook stdin (`internal/router/hooks.go`). If not, Codex stays on F4-only delivery.
4. Whether hook subprocesses can reach loopback under Codex's sandbox.
5. What paste does at a permission prompt, mid-draft and just after an interrupt. Spike 2 never ran; the gate holds in all of these unless the user presses F4.
6. Whether `/clear` or `/compact` changes a harness's session ID, which would leave that agent on F4.
7. Whether Bubble Tea's `Run` returns cleanly with an event-wait command outstanding at quit (`internal/app`). If not, quit could hang before the run is recorded; recovery would then mark it crashed.
8. All time limits and sizes are guesses, each marked in code.

Full lists by file and line: `backend-report.md`, `frontend-handoff.md`.

## Design items not implemented or differing

- **Startup warning for untested harness versions (section 11): not implemented.** No version has been tested, so there is nothing to compare against. Recommend adding it once testing gives a list.
- All other departures are recorded in design v0.7 to v0.11 (`empty_body`, fixed session directory, `starting`→`unknown`, session binding, F4 over drafts).
- Known low items left alone: `stripControl` keeps U+2028/2029 and bidi controls (display only); one Claude per run (shared config file names).

## Process correction

- Backend wrote `internal/store` directly, skipping the required monkey first version, and disclosed it. My instruction "don't wait idle" was too loose and contributed. The operator's decision: keep it, no rewrite for process, not a precedent. Lead reviewed it statically (`store-review.md`, no blockers).
- Backend had also written router helpers directly before the correction reached it. It deleted them itself and disclosed it; they came from monkey.
- From then on, every frontend and backend task went senior design, supervised monkey, senior takeover, one monkey task at a time. Lead authored `cmd/rtk`, `internal/app` and `internal/protocol` directly: lead doesn't dispatch to monkey under the team model.
- The review step earned its keep: seniors found real defects in monkey drafts, including a router bug that could paste a second message over an unconfirmed first, with two draft tests encoding it.
- Lead also sent architect one mistaken report of an open prompt on frontend (10:47Z) and corrected it. Spikes 1 to 3 were cancelled under GR-3 with no results.

## Left in the tree

Uncommitted: all of the above, `docs/`. `spikes/1-panes/BRIEF.md` is retained as a cancelled record, although my contract said spikes are removed before the candidate; I kept it because you said not to clean up uncertain files, and it needs a decision. DevOps's pre-stop evidence is outside the repo at `/var/folders/xz/qhdyjzfs3qjb389j2x2q4dmw0000gn/T/rtk-devops-sandbox-t4lt9u8l/`. Preserved and untouched: `rigs/`, `.claude/`, `.codex/`, `.openrig/`, `AGENTS.md`, `CLAUDE.md`, `.DS_Store`. There is no `.gitignore`.

## When the user allows testing

Commands are unverified. Suggested order, cheapest and most decisive first:

```sh
go test -race ./...                     # none of these tests has ever run; expect failures
go build -o ~/.local/bin/rtk ./cmd/rtk
rtk                                     # in a throwaway directory
```

Then check, in order: panes (`pane-risks.md`, first-check column), Codex hooks and MCP, delivery in each state, then checks 9 to 14. Shift+Enter and visual checks need a person at a real terminal. Checks 10 and 12 need a runner written first.
