# Outstanding issues

Owner: `delivery.lead`. This is the current list for the `rtk` first version. It replaces the lists in earlier reports where they differ.

## What has and hasn't been checked

**Checked by hand by the user, 2026-11-04, on macOS:** mouse click focus and Ctrl+C both work. That is the user's report of those two behaviours only. It doesn't show that any test or other acceptance check passes.

**The user's run of Claude asking Codex to say hi, 2026-11-04 on macOS, failed (run `01a1a744`).** Codex got messages late and Claude got no reply; see confirmed defects. The architect traced that run read-only from the database and Codex's records. It's the first evidence that these work with Codex: its nested `-c` config loads, its MCP client connects to our server, hooks fire once trusted, session binding accepts them, hook subprocesses reach loopback, paste and Enter land, messages go `pasted` then `accepted`, and Stop returns it to idle.

**The user's later run on macOS, 2026-11-05 (run `01a1a918`), carried a reply from Codex to Claude.** The user had to type into Codex before it responded. The architect traced the run read-only: Codex called `mcp__ratatoskr__ratatoskr_send` at +102.0 s and that message reached Claude, `accepted` at +102.1 s. So the tool rename was seen working: Codex can see the tool and its approval key works, as far as these user runs show.

**The user reports that the first-message hold is startup only, 2026-11-05 on macOS:** "Yeah it is startup only, after doing it the first time, it will continue to work." The architect's reading of the message history for run `01a1a91c` agrees: Claude to Codex was held on `starting` once, then Codex to Claude, Claude to Codex and Codex to Claude went through with no holds, all `accepted` on `prompt_submitted`. That is one user-reported exchange in both directions. It doesn't pass any acceptance check, and no automated test ran.

**The user checked automatic startup and messaging by hand, 2026-11-05 on macOS (run `01a1a932`, started 00:53:50Z), and reports that it works:** "Ok this is good, I tested it and it works." `rtk`'s history for that run shows a message from Claude to Codex with no hold, and Codex's reply, both `accepted`. That is the user's manual result for one run. It doesn't show that any test or acceptance check passes.

**The user tested the cross-machine candidate by hand, 2026-11-06, and reports that it works:** "Ok, I tested the cross-machine functionality of ratatoskr and it all works as expected." That is the user's manual result for the basic path. The report doesn't say which commit, machines, platforms or transport were used. It doesn't separately confirm any failure result, the certificate mismatch cases, a machine really sleeping, or the time limits. The team has run nothing, and no automated test has run.

**Compile checks by the team:** `gofmt`, `go vet` and `go build` pass on macOS, and the code cross-compiles for Linux.

**Has not run:**

- All automated tests. They compile but have never been executed.
- Anything in the cross-machine candidate (`rtk gateway`, `rtk runtime`) by the team, and any automated test of it. The user's manual test is recorded above. What it doesn't separately confirm stays open in [Cross-machine candidate](#cross-machine-candidate) and [Direct LAN](#direct-lan).
- All of the per-host daemon (`rtk daemon`, `rtk attach`). See [Per-host daemon](#per-host-daemon).
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

### Cross-machine candidate

`rtk gateway` and `rtk runtime`, built from the [contract](cross-machine-contract.md) and [design](cross-machine-design.md). The user reports that the basic path works, from their manual test on 2026-11-06. The team has only run `gofmt`, `go vet` and `go build`, which pass for macOS and Linux. Tests are written and unrun, and that plain `rtk` is unchanged is argued from the diffs, not shown. Time limits are `UNTESTED guess`. "Not separately confirmed" below means that the user's general report may cover the row, but doesn't say so.

| Risk or gap | If it's wrong, or what it means | Status | What would resolve it |
| --- | --- | --- | --- |
| Plain `rtk` is unchanged apart from its database migration | A regression in the mode that the user has checked by hand | Unverified. Argued from the diffs. | Plain `rtk`, one message round trip |
| The first plain `rtk` run after this build migrates its database from schema 1 to 2, writing a backup beside it | An older `rtk` then refuses that database | Accepted. Not separately confirmed | Run it on a copy of an existing database |
| Connected router and link tests read actor-owned fields from the test goroutine | Tests may report races | Known limitation of tests that have never run | `go test -race`, then read through `submit` or `Snapshot` where flagged |
| `modernc.org/sqlite` pragmas, multi-statement `Exec` and `_txlock=immediate` for both databases, and two processes migrating at once | Gateway or runtime store may fail to start | Starting one of each is implied by the user's report. Concurrent starts not confirmed | Concurrent admin commands, and two processes migrating at once |
| Race between a late ack and the gateway's 5 s wait | The stored result could be wrong | Reasoned from a compare-and-set. Not separately confirmed | A slow-ack test |
| "Written to the network" detection through `httptrace`, and the request transport without keep-alives | A send could be labelled not sent when it may have arrived, or the reverse. One TCP connection per request, including a heartbeat about every 5 s | Not separately confirmed | Requests against a stopped and a restarted gateway |
| The stream: write deadlines and flushes through the HTTP wrapper, watchdog, heartbeat, reconnect | A stalled stream may not end, or a live one may be dropped | A working stream is implied by the user's report. Stalls, restarts and network loss not confirmed | A long idle stream, a gateway restart, a dropped network |
| A silent drop (machine sleep, network loss) | For up to 15 s a send ends `delivery_uncertain`, as the contract specifies. After that, `destination_unreachable` | Designed. Not separately confirmed | Put a recipient machine to sleep and send |
| A stopped host's stream can stay attached until TCP gives up | Routing treats the host as not live after 15 s, so results stay honest | Known gap, accepted | Observe after a silent drop |
| `gateway_uncertain` with no record at the gateway stays uncertain | Some records may stay uncertain for good, on purpose | Decided. Not separately confirmed | None needed unless it misleads in practice |
| Reports for messages that become `interrupted` at quit aren't sent | The gateway learns late or never. `gateway log` and `show` say "(session ended)" beside the last report | Known gap, accepted | Quit with a message held |
| `gateway hosts` reads the last heartbeat | State can be up to 15 s stale | Accepted | None needed |
| A gateway refusal of a send (3xx, 400, 401, 403, 404) reports as `gateway_unreachable` with the gateway's reason | The contract defines that result as no connection, and the reason text tells them apart | Accepted reading. Not separately confirmed | Send from an unpaired host |
| `pairing.json` is `0600` and never passed to a harness, but harnesses run as the same user | An agent with file access can read the gateway credential | Known limit | None in this phase. `rtk` isn't a security boundary between agents |
| `--code` is on the command line | The pairing code shows in the process list and shell history for its 10-minute single use | Known gap | Prompt for it if this matters |
| Pairing codes have no attempt limit | 60 bits, 10 minutes. With `--allow-lan`, every device on the LAN can try, not just the tailnet | Accepted for now: guessing stays impractical | Add a limit if guessing becomes realistic |
| The `--listen` check tests the address range, not Tailscale itself | `100.64.0.0/10` is shared address space, so an address in it isn't proof of Tailscale or its encryption. Userspace Tailscale isn't supported, because the bind is direct | Known limit | Document the OS-level Tailscale prerequisite |
| Claude and Codex use `ratatoskr_send` with `name@host`, and Codex's startup prompt works with network names | Agents may address peers wrongly | The user reports the basic path works. Which agents and address forms were used isn't confirmed | A recorded run naming them |
| One- and two-pane rendering, connection labels and the F3 outbound view | Layout or wording may be wrong, including local mode's look | Not confirmed. The user's report doesn't mention layout or wording, and the team has rendered nothing | Run `rtk runtime` with one and two agents, and plain `rtk` |
| Linux at runtime, and two real machines | The team has only compiled for Linux | Open. The user's report doesn't say which machines or platforms were used | A run between macOS and Ubuntu, with the platforms recorded |

### Direct LAN

`--allow-lan`, `--insecure-http` and `--fingerprint`, built on the cross-machine candidate from the contract's [Direct LAN](cross-machine-contract.md#direct-lan) section. The user's report of 2026-11-06 doesn't say which transport was used, so it doesn't show that either LAN mode was exercised, and no row here is closed on it. The team has made no handshake and contacted no LAN address. The same compile checks pass, and the tests are written and unrun.

| Risk or gap | If it's wrong, or what it means | Status | What would resolve it |
| --- | --- | --- | --- |
| A real TLS handshake, with the pin check before any request byte | The pairing code or credential could be sent to the wrong machine, or a good gateway refused. Tests call `VerifyConnection` directly, with no listener | Reviewed against the Go source. Not separately confirmed | Pair with the right and a wrong fingerprint, watching for the code at the gateway |
| The pin mismatch reason reaching the user through `net/http`'s error wrapping | The user would see a generic failure and not the advice to re-pair | Reviewed against the Go source. Not separately confirmed | Regenerate the certificate, then start a runtime |
| Handshake failures count as not sent, so `gateway_unreachable` | A send could be labelled wrongly | Reviewed from the transport's ordering. Not separately confirmed | Send with a wrong pin while a runtime is running |
| A pinned runtime meeting a plain HTTP gateway says "the gateway did not answer with TLS" | Only the message depends on this: no request bytes are sent | Reviewed against the Go source. Not separately confirmed | Restart a gateway with `--insecure-http` |
| TLS server: handshake bounded by `ReadHeaderTimeout`, the long-lived stream over TLS, HTTP/1.1 only, `Shutdown` with a TLS listener | A stalled handshake, a stream that doesn't flush, or a slow exit | Reviewed against the Go source. Not separately confirmed | A long idle stream over HTTPS, then stop the gateway |
| Certificate and key written, then reloaded through `tls.X509KeyPair` | The gateway might not restart on its own identity, and regenerating would force every runtime to re-pair | Not separately confirmed | Start the gateway twice and compare fingerprints |
| One TLS handshake per request, including a heartbeat about every 5 s | Cost on a LAN unmeasured. Handshake budget is 3 s, an `UNTESTED guess` | Not separately confirmed | Watch a long HTTPS run |
| A plain HTTP pairing still sends its credential if the gateway later moves to HTTPS | The gateway answers 400, but the credential has already crossed the LAN in plaintext. The saved transport never changes by itself | Known gap, by design | Re-pair before or right after changing the gateway's mode |
| Interrupted first start: a key with no certificate | The next start refuses by name and says what to remove, so it needs a manual step | Known gap, accepted | None needed unless it happens |
| `/v1/pair` needs no credential and has no read deadline after the headers | Any LAN device can hold a connection open by sending a slow body. It gains no access, but it isn't bounded | Known gap | Add a read deadline on that route if it matters |
| Both LAN modes expose the port to every device on the LAN | rtk configures no firewall and doesn't limit which devices connect. Pairing is still needed to do anything | Accepted in the contract | None in this phase |
| The address comes from what was typed, and hostnames are refused | If the gateway's DHCP address changes, runtimes must re-pair at the new address. The fingerprint stays the same, so don't regenerate the certificate | Known limit | A router DHCP reservation |
| `Gateway connected (plain HTTP)` title | Wording and fit have only been worked out: 30 cells against a minimum usable width of 40 | Not separately confirmed. The team has rendered nothing | Run `rtk runtime` paired with `--insecure-http` |
| Reaching a real LAN address on macOS and Linux | A firewall may block it. Apple's documentation exempts terminal tools from the Local Network prompt, but that hasn't been seen here | Not separately confirmed. The report doesn't name the transport or firewall setup | A run between two machines on the LAN |

### Per-host daemon

`rtk daemon`, `rtk attach` and `rtk runtime` as its alias, built from the [daemon contract](daemon-contract.md) and [design](daemon-design.md). All of it is written and never run: `gofmt`, `go vet` and `go build` pass for macOS and Linux, tests are written and unrun, and that plain `rtk` is unchanged is argued from the diffs, not shown. Time limits are `UNTESTED guess`.

| Risk or gap | If it's wrong, or what it means | Status | What would resolve it |
| --- | --- | --- | --- |
| Whether a daemon survives a macOS logout | `setsid` isn't service supervision, so macOS may end it with the login session. The promise is only a closed window or dropped SSH | Unverified, and a known limit | Log out and back in, then `rtk daemon status` |
| Self-detach, and readiness through the socket | `rtk daemon` might report failure for a healthy child, or hang | Never run | `rtk daemon`, close the terminal, `rtk daemon status` |
| Peer credentials and the parent-chain walk on macOS and Linux | Connections wrongly refused, or the agent check doing nothing. The walk isn't a security boundary | Never run | `rtk attach` from a shell, and from inside an agent |
| Harnesses start with default SIGHUP handling while the daemon ignores it | Harness tools could ignore hangups and be left behind | Fixed by review, never run | On Linux, read `SigIgn` in `/proc/<harness pid>/status` |
| The user's cross-machine test of 2026-11-06 ran the old in-process `rtk runtime` | That path is gone, so the test doesn't carry over to the daemon, and gateway protocol 2 changes registration | Open: connected messaging needs testing again | The two-machine run again, through `rtk daemon` and `rtk attach` |
| Gateway protocol 2 | Version 1 runtimes are refused, so every machine and the gateway must move together. No schema change | Never run | Restart the gateway on the new build with its existing database, then a daemon reconnecting with its existing pairing |
| Plain `rtk` is unchanged by this work | `app.go` has no diff, and shared files gained only additions or guarded branches. A regression would be in the mode the user has checked by hand | Argued from the diffs | Plain `rtk`, one message round trip |
| `rtk attach`, `windowclient` and the socket surface end to end | A window may fail to open, misdraw or disconnect. Static review found four bugs here that would have stopped everything, and more may remain | Never run | `rtk attach --agents claude,codex` |
| Input ownership: viewers refused, F6 takeover, a closing holder releasing input | Input could reach a session from the wrong window, or a window could be stuck as viewer | Never run | Two windows on one session |
| A window closing or SSH dropping changes nothing at the gateway | A session could end, or messages be marked `interrupted` | By review, never run | Close a window with a message held, then re-attach |
| Slow windows: latest-wins frames, bounded queues, disconnect on overflow | A window could be dropped too eagerly, or lag | By review, never run | A window on a slow SSH link under heavy output |
| Pane polling every 30 ms per session | Cost unmeasured | Known limit | Watch CPU with idle sessions |
| The first daemon Codex session may ask to trust its hooks once | Its `--session-dir` differs from plain `rtk`'s. Later sessions use the same fixed `session/<name>/`, so they shouldn't ask. Whether Codex's trust covers the whole command hasn't been checked | Unverified | Start a Codex session, end it, and start it again |
| A hook left over from an ended harness could read the replacement's hook file | It would report under the new session. It needs a straggler hook at the moment of a re-add | Accepted for now | None unless seen |
| `rtk daemon stop` waits for the process to exit: 30 s against a worked-out worst case of 22 s, an `UNTESTED guess` | A slower stop prints that it's still finishing, and the lock stays held until it does. A reused PID can only cause that message, never a false `daemon stopped` | Reviewed, never run | `rtk daemon stop --yes`, then plain `rtk` at once |
| A session directory left behind blocks that name | The add fails with `file exists` until the directory goes, and the log says why it couldn't be removed. A daemon restart clears `session/` | Known gap, accepted | None unless seen |
| A window shows the sessions that exist when it attaches | A session started later needs a re-attach. The title says so | Accepted first limit | None in this phase |
| A re-add straight after ending a session can be refused as a duplicate | It fails with the reason, and a retry works | Accepted, never run | `rtk daemon stop --session NAME`, then attach at once |
| `rtk daemon stop --yes` is checked by the client only | A session started between the status and the stop is stopped too | Accepted | None needed |
| Control connections work across builds | `status` and `stop` from a new build reach an old daemon. Their messages may only gain fields | Decided by architect, never run | Stop an old daemon with a new build |
| Peers are fixed when a session is added | An agent's listed peers can go stale. `unknown_recipient` returns the current list | Known limit, as before | None needed unless it misleads |
| `daemon.log` has no rotation | It grows slowly, with one line per connection and session change | Known gap | Rotate it if it matters |
| A daemon crash ends or orphans agents, and nothing resumes | Survivors aren't tracked or killed, by contract | Accepted | None in this phase |

## Deferred features

| Feature | Impact | Status | What would resolve it |
| --- | --- | --- | --- |
| Startup warning for untested harness versions | Users get no warning on a version that hasn't been tested | Not implemented, because no version list exists yet | Add it once testing gives a list |
| Automatic recovery of a dead pane's input | Restart `rtk` to recover | Not implemented | Decide if testing shows it matters |
| Resume, resend, and scrollback in panes | Sessions end with `rtk`. Interrupted messages are passed on by hand from the log. | Out of scope for this version | Future design |
| Discovery, teams and other harnesses | Two agents only, one Claude per run | Product direction in the README, not built | Future design |
| Windows | Not supported | Deferred | Future design |
