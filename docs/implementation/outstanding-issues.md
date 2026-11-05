# Outstanding issues

Owner: `delivery.lead`. This is the current list for the `rtk` first version. It replaces the lists in earlier reports where they differ.

## What has and hasn't been checked

**Checked by hand by the user, 2026-11-04, on macOS:** mouse click focus and Ctrl+C both work. That is the user's report of those two behaviours only. It doesn't show that any test or other acceptance check passes.

**The user's run of Claude asking Codex to say hi, 2026-11-04 on macOS, failed (run `01a1a744`).** Codex got messages late and Claude got no reply; see confirmed defects. The architect traced that run read-only from the database and Codex's records. It's the first evidence that these work with Codex: its nested `-c` config loads, its MCP client connects to our server, hooks fire once trusted, session binding accepts them, hook subprocesses reach loopback, paste and Enter land, messages go `pasted` then `accepted`, and Stop returns it to idle.

**The user's later run on macOS, 2026-11-05 (run `01a1a918`), carried a reply from Codex to Claude.** The user had to type into Codex before it responded. The architect traced the run read-only: Codex called `mcp__ratatoskr__ratatoskr_send` at +102.0 s and that message reached Claude, `accepted` at +102.1 s. So the tool rename was seen working: Codex can see the tool and its approval key works, as far as these user runs show.

**The user reports that the first-message hold is startup only, 2026-11-05 on macOS:** "Yeah it is startup only, after doing it the first time, it will continue to work." The architect's reading of the message history for run `01a1a91c` agrees: Claude to Codex was held on `starting` once, then Codex to Claude, Claude to Codex and Codex to Claude went through with no holds, all `accepted` on `prompt_submitted`. That is one user-reported exchange in both directions. It doesn't pass any acceptance check, and no automated test ran.

**The user checked automatic startup and messaging by hand, 2026-11-05 on macOS (run `01a1a932`, started 00:53:50Z), and reports that it works:** "Ok this is good, I tested it and it works." `rtk`'s history for that run shows a message from Claude to Codex with no hold, and Codex's reply, both `accepted`. That is the user's manual result for one run. It doesn't show that any test or acceptance check passes.

**Compile checks by the team:** `gofmt`, `go vet` and `go build` pass on macOS, and the code cross-compiles for Linux.

**Has not run:**

- All automated tests. They compile but have never been executed.
- Linux at runtime, and use over SSH.
- Recovery after a crash, and storage failure handling. The runners for acceptance checks 10 and 12 aren't written.
- The other acceptance checks in design section 12. Mouse focus and Ctrl+C aren't numbered checks.
- Terminal behaviour beyond the user's run: wide characters, colours, redraw speed, modified keys, and the limits on terminal size.
- The paste limits and every time limit below.

**The earlier UI freeze is an open risk, not a closed defect.** The user saw the whole UI freeze on Ctrl+C and no longer reports it. Its cause was never established. The fix was a redesign so that pane problems can't block the UI, whatever the cause. If it comes back, record what was on screen and which key was pressed, and capture stacks with `kill -QUIT <rtk pid>` from another terminal.

## Confirmed defects

These two are confirmed by reading the code and have been left alone:

| Issue | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| `stripControl` keeps U+2028, U+2029 and bidi controls | Display only: a message can look reordered or wrapped oddly in the log | Open, low | Strip them in `internal/router` |
| `Send` or `RegisterAgent` before the router's `Start` waits until its context ends | None on today's startup order, which starts the router first | Open, low | Return an error instead of waiting |

Fixed, listed here so they aren't reported again:

| Issue | Status |
| --- | --- |
| Clicks didn't change pane focus | Fixed. User checked by hand. |
| Ctrl+C didn't quit `rtk` | Fixed. User checked by hand. A second press within 1 second quits, and one press quits if nothing can receive it. |
| A held message could be pasted into a Codex turn that had just started, because Codex's SessionStart lifted it to idle and `rtk` delivered at once (run `01a1a918`: pasted +92.0 s, turn started +91.7 s). Codex took it as extra input, so no harm that time | Fixed in source, static only: compile-checked, tests written and unrun, not seen in a run. A Codex SessionStart now binds and clears the banner but leaves the agent `starting` or `unknown`, and Codex becomes idle on Stop. The first message now arrives when the first turn ends. A bound Codex agent that goes silent shows `unknown` without a banner, which is accepted for now. |
| The first message to Codex waited until the user typed any prompt into it or pressed F4, because Codex seems to fire SessionStart only on its first turn (runs `01a1a744`, `01a1a918`). The user confirmed on macOS that it is startup only, and run `01a1a91c` agrees: one hold on `starting`, then no more | Decided by the user (design decision 11): "Add the automatic prompt please." Fixed: `rtk` launches Codex with a startup prompt as its trailing argument, and held messages deliver when that turn ends. Seen working in the user's run on macOS (`01a1a932`), where a message to Codex went through with no hold and nobody typing first. Tests are written and unrun. Not seen on Linux. See the harness assumptions below. |
| Codex replied with the wrong tool: its built-in `send_message` (namespace `collaboration`) instead of ours, so Claude got no reply (run `01a1a744`) | Fixed by renaming ours to `ratatoskr_send` and rewording the frame. Seen working in the user's runs on macOS (`01a1a918`, `01a1a91c`). Not seen on Linux. |

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
| Codex's model can see and call our MCP tool, and accepts the per-tool approval key under the new name | Replies never reach the peer, whatever the tool is called | Seen working in the user's runs on macOS (`01a1a918`, `01a1a91c`). Not seen on Linux. | A run on Linux |
| Codex runs its launch prompt without waiting behind a trust or hook-review dialog | The automatic first turn waits for the user to answer the dialog, and messages wait with it. `rtk` answers no dialog | Unverified | A launch with hooks not yet trusted |
| Codex accepts the prompt as a trailing argument after our `-c` options, and that turn fires SessionStart with source `startup`, then UserPromptSubmit, then Stop | Codex may fail to start or not bind, and messages hold. If the order differs, the agent may stay held until a later Stop | Automatic startup seen working in the user's run on macOS (`01a1a932`). The hook order hasn't been checked. Not seen on Linux. | The run's hook record, and a run on Linux |
| The startup prompt doesn't change Codex's hook trust. Our hook command is unchanged, but what Codex hashes hasn't been checked | The user may be asked to review hooks again | Unverified | A launch with hooks already trusted |
| The cost of the startup turn is acceptable | One extra Codex model turn on every launch, even if no message is sent | Unmeasured. The user chose this knowing it costs a turn | Check usage after a launch |
| Claude resolves `mcp__ratatoskr__ratatoskr_send` as the allowed tool, and the new wording steers both models to it | Claude prompts for each send, or a model uses the wrong tool | Unverified | A run where each agent replies |
| Stop fires after an interrupt | Agent goes to `unknown` and needs F4 | Unverified | Interrupt run |
| What a paste does at a permission prompt, mid-draft or just after an interrupt | The gate holds in these states, except that F4 overrides `unknown` and a draft on an idle agent, where text may merge | Unverified | Acceptance checks 5 and 6 |
| `/clear` and `/compact` keep the session ID | That agent needs F4 for the rest of the run | Unverified | Run both in each harness |
| Claude's flags and JSON shapes, and that its settings hooks don't re-prompt | Claude pane may fail or lose messaging | Unverified beyond the user's run | First delivery run |

### Delivery, storage and shutdown

| Risk | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| Busy-recipient order, budget, log history and second-instance refusal haven't been checked, and delivery has only been seen in one failed exchange | Core purpose unproven | Unverified | Run `go test -race ./...`, then acceptance checks 3 to 9 and 11 |
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
