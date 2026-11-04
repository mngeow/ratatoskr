# Outstanding issues

Owner: `delivery.lead`. This is the current list for the `rtk` first version. It replaces the lists in earlier reports where they differ.

## What has and hasn't been checked

**Checked by hand by the user, 2026-11-04, on macOS:** mouse click focus and Ctrl+C both work. That is the user's report of those two behaviours only. It doesn't show that any test or other acceptance check passes.

**Compile checks by the team:** `gofmt`, `go vet` and `go build` pass on macOS, and the code cross-compiles for Linux.

**Has not run:**

- All automated tests. They compile but have never been executed.
- Linux at runtime, and use over SSH.
- Message delivery between the agents, by the team or as a reported check.
- Recovery after a crash, and storage failure handling. The runners for acceptance checks 10 and 12 aren't written.
- The other acceptance checks in design section 12. Mouse focus and Ctrl+C aren't numbered checks.
- Terminal behaviour beyond the user's run: wide characters, colours, redraw speed, modified keys, and the limits on terminal size.
- The paste limits and every time limit below.

**The earlier UI freeze is an open risk, not a closed defect.** The user saw the whole UI freeze on Ctrl+C and no longer reports it. Its cause was never established. The fix was a redesign so that pane problems can't block the UI, whatever the cause. If it comes back, record what was on screen and which key was pressed, and capture stacks with `kill -QUIT <rtk pid>` from another terminal.

## Confirmed defects

No defect is currently known from running `rtk`. These are confirmed by reading the code and have been left alone:

| Issue | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| `stripControl` keeps U+2028, U+2029 and bidi controls | Display only: a message can look reordered or wrapped oddly in the log | Open, low | Strip them in `internal/router` |
| `Send` or `RegisterAgent` before the router's `Start` waits until its context ends | None on today's startup order, which starts the router first | Open, low | Return an error instead of waiting |

Fixed, listed here so they aren't reported again:

| Issue | Status |
| --- | --- |
| Clicks didn't change pane focus | Fixed. User checked by hand. |
| Ctrl+C didn't quit `rtk` | Fixed. User checked by hand. A second press within 1 second quits, and one press quits if nothing can receive it. |

## Unverified risks

None of these has been observed. Each comes from reading the code or from an assumption about something we couldn't run.

### Input and panes

| Risk | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| A partial paste leaves the child in paste mode: after a timeout the rest of the paste, including its end marker, is dropped (`internal/pane/transport.go`, `writerLoop`) | Later keys, possibly including Ctrl+C, may reach the harness as pasted text until its next paste ends. `rtk`'s own quit should still work. Message delivery can leave the same state after a partial write. | Unverified | Slow large paste test; then always send the end marker |
| A pane's input can be wrongly marked dead: the 3-second limit in `sendEncoded` also covers waiting for the input lock | A key that waited nearly 3 seconds behind a slow delivery could time out in the emulator step, and that pane stays `INPUT UNAVAILABLE` for the run | Unverified, narrow timing | Start a new time limit after the lock is taken, about one line |
| `replyLoop` can count a terminal query reply against the next key's completion | Enter could be overtaken by a message paste. It needs a reply, a key and a delivery within an instant. | Unverified, very unlikely | Separate reply bytes from input bytes reliably |
| The screen stops after an emulator timeout: `readLoop` returns | That pane's display freezes and its harness may block on output. The pane is marked unavailable and quit should still work. There is no automatic recovery. | Unverified | Decide on recovery once a timeout has actually been seen |
| Embedded panes under real harnesses (`x/vt` v0.1.0) | Unsupported terminal queries, no Kitty or CSI-u keys, wide characters, colours and redraw speed may misbehave. Each pane may not match its own terminal (acceptance check 2). | Unverified beyond the user's run | Use both harnesses at length; see `pane-risks.md` |
| Rendering after every output chunk on the emulator worker | Could slow under heavy output. A query reply can be dropped if the queue fills or times out. | Unverified | Streaming output test at normal terminal sizes |
| Shift+Enter maps to Ctrl+J | Custom key bindings may differ | Unverified | Check with each harness |
| Text selection needs a bypass modifier while `rtk` captures the mouse | Shift or Option, depending on the terminal | Accepted cost, not checked across terminals | Try common terminals |

### Paste limits

| Limit | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| A paste over 1 MiB is refused, with a message, and the draft stays held | User must split it or send it another way; messages wait until Enter or F4 | By design, unrun | Change the limit if it gets in the way |
| A slow paste can stop after 3 seconds with part of it delivered | The harness may hold a partial paste, and the bytes can't be taken back | Accepted, unrun | Test with a slow child; see the paste mode risk |

### Time limits and sizes

Every value here is a guess, marked in code, and none has been measured. A wrong value would show up as false timeouts, a slow quit or messages held for too long.

| Area | Values |
| --- | --- |
| Ctrl+C second press | 1 s |
| Input and query operation, stuck command watchdog | 3 s each |
| Paste then Enter delay | 50 ms |
| Input queue, transport queue | 128 actions; 512 slots and 2 MiB |
| Shutdown | UI stop 2 s, each step 2 s, panes 5 s in total, SIGTERM grace 2 s |
| Router | Budget 20, startup 60 s, busy 5 min, confirm 20 s, write 10 s |
| Other | Exit notice polling 100 ms, version probe 5 s, server and hook limits |

**To resolve:** measure against real harnesses, then change the ones that are wrong.

### Harness assumptions

| Assumption | If it's wrong | Status | What would resolve it |
| --- | --- | --- | --- |
| Codex accepts our nested `-c` settings for MCP and hooks (`internal/agent/codex.go`) | No messaging from Codex or no state tracking. The fallback puts a file in the user's project, which needs a design decision. | Unverified, least certain | First delivery run |
| Codex reports `source=startup`, and hook input includes `session_id` | That agent stays on F4 delivery | Unverified | Inspect hook events |
| Hook subprocesses can reach loopback under Codex's sandbox, and Stop fires after an interrupt | Agent goes to `unknown` and needs F4 | Unverified | Delivery and interrupt runs |
| What a paste does at a permission prompt, mid-draft or just after an interrupt | The gate holds in these states, except that F4 overrides `unknown` and a draft on an idle agent, where text may merge | Unverified | Acceptance checks 5 and 6 |
| `/clear` and `/compact` keep the session ID | That agent needs F4 for the rest of the run | Unverified | Run both in each harness |
| Claude's flags and JSON shapes, and that its settings hooks don't re-prompt | Claude pane may fail or lose messaging | Unverified beyond the user's run | First delivery run |

### Delivery, storage and shutdown

| Risk | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| Delivery, budget, log history and second-instance refusal have never run | Core purpose unproven | Unverified | Run `go test -race ./...`, then acceptance checks 3 to 9 and 11 |
| Crash recovery and forced storage failure | Wrong reasons on messages, or delivery continuing when it shouldn't | Unverified, no runner | Write runners for checks 10 and 12 |
| `modernc.org/sqlite` DSN pragmas, multi-statement `Exec`, `VACUUM INTO` with a bound parameter, `flock` semantics | Store may fail at startup or refuse to lock | Unverified | Store tests |
| Security checks: file permissions, no tokens in the database, retention, `~/.claude` and `~/.codex` unchanged | Secrets exposure or changed owner config | Unverified, by construction only | Acceptance checks 13 and 14 |
| Leftover processes: `Wait4` polling replaced `cmd.Wait`, and group signals only reach an unreaped child's own group | A wrapper that exits first, or a tool in another group, can leave a process running. Exit code handling is new. | Unverified | Check `ps` after quitting, with each harness busy |
| A stuck parser, renderer or router call can't be interrupted | Its goroutine stays until `rtk` exits. It's isolated from the UI and shown as unavailable. | Accepted | None planned |
| Linux and SSH | PTY and terminal behaviour may differ from macOS | Unverified | Run on Ubuntu, directly and over SSH |

## Deferred features

| Feature | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| Startup warning for untested harness versions | Users get no warning on a version that hasn't been tested | Not implemented, because no version list exists yet | Add it once testing gives a list |
| Cross-machine messaging | Both agents must be on one machine | Designed in outline as the planned next step (design section 14), not built | Link design and implementation |
| Automatic recovery of a dead pane's input | Restart `rtk` to recover | Not implemented | Decide if testing shows it matters |
| Resume, resend, and scrollback in panes | Sessions end with `rtk`. Interrupted messages are passed on by hand from the log. | Out of scope for this version | Future design |
| Discovery, teams and other harnesses | Two agents only, one Claude per run | Product direction in the README, not built | Future design |
| Windows | Not supported | Deferred | Future design |
