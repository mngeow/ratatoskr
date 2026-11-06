# Per-host daemon: backend design

Owner: engineering-backend. Source: [daemon-contract.md](daemon-contract.md) (architect) and ADR-0003. This is how the code is shaped. It adds no behaviour the contract does not state; where I made a choice the contract left open, it is marked **Choice**. **Nothing here has been run (GR-3). Every behaviour is UNTESTED.**

## Principles that decide the shape

1. **Plain `rtk` does not change.** `app.Run` and everything it calls stay as they are, to the byte. The daemon gets its own helpers (a few small functions are copied, not moved) rather than touching `app.go`. The only edits to shared packages are additive: new files and new methods that local mode never calls.
2. **One owner per thing.** The daemon process owns the store lock, router, MCP/hook server, PTYs, gateway link and credential. A window owns drawing and keys and nothing else.
3. **Nothing waits on a window.** Each window connection has its own reader and writer goroutines and its own bounded outbound state. Router and pane code never call into a connection.
4. **A window closing is not an event for the router or the gateway.** Closing a socket releases that window's input and nothing else.

## Packages

| Package | Role | May import |
| --- | --- | --- |
| `internal/daemon/wire` | Framing, message types, codes (written, declaration-only) | stdlib, `protocol`, `pane` |
| `internal/daemon` | The daemon: lifecycle, socket, sessions, input gate, control commands, self-detach | `router`, `store`, `server`, `link`, `agent`, `pane`, `protocol`, `wire` |
| `internal/windowclient` (frontend) | Socket-backed `tui.Pane` and `tui.Router` | `wire`, `protocol`, `pane`, `uv`, stdlib. **Never** `app`, `router`, `store`, `server`, `link`, `daemon` |
| `cmd/rtk` | `rtk daemon ...`, `rtk attach`, `rtk runtime` alias | all of the above |

`internal/app` keeps `Run` (plain `rtk`). `app.RunRuntime` and `runtime.go` are deleted in slice 5, once `rtk runtime` is the alias, so there is only one way to run connected agents.

## Five slices

| # | Slice | Owner of the first version |
| --- | --- | --- |
| 0 | Wire surface (this document, `wire` package) | backend, with frontend |
| 1 | Lifecycle: `rtk daemon`, detach, lock, socket, handshake, peer checks, retention, status, stop | monkey, then backend |
| 2 | Sessions: gateway protocol 2, router/server add and end, session manager, `stop --session` | monkey, then backend |
| 3 | Attach: socket server for windows (frames, events, queries, FIFO acks), `rtk attach` command; frontend builds `windowclient` against the same types | monkey, then backend; frontend the client |
| 4 | Input ownership: one gate for every input operation, take/release, size rule, draft rule | backend |
| 5 | Window keys and removal of the old path: F10/F6/Ctrl+C wiring is frontend; backend deletes `RunRuntime`, aliases `rtk runtime`, final docs input | both |

## Slice 1: lifecycle

### Start order (`daemon.Run`)

1. Resolve the state directory; capture the environment (`os.Environ`) **once**: it is what harnesses will get. `rtk daemon` prints that this is the environment of the terminal that started the daemon.
2. Ignore SIGHUP (`signal.Ignore`) before anything else, in both detached and `--foreground` mode.
3. Load the pairing (`link.LoadPairing`); `ErrNotPaired` exits with the existing text. No gateway is contacted yet.
4. **Lock first:** `store.Open(ctx, stateDir)`. Nothing below touches a live run's files before this succeeds. Plain `rtk` and the daemon exclude each other through it.
5. `Recover`, then `Retain` (retention at start), `PrepareSessionDir`, new run ID, `StartRun`.
6. Build `link.Client`, `router.New` (with `Link: client`, banner from `Recover`), `server.New`. Start router and server.
7. Prepare `daemon/` (0700), open `daemon.log` for append at 0600 (when foreground, the log is stderr's and nothing is opened), then **bind the socket** (below).
8. Start: the accept loop, the daily retention timer, the gateway connector (slice 2) and `client.Run(runCtx, receiver)`.
9. Serve until SIGTERM/SIGINT, a `stop` request, or a fatal error.

### Shutdown order (`stop`, SIGTERM, SIGINT)

Mirrors `shutdownRuntime`, with windows first: stop accepting; send every window `bye` (`shutting_down`) and close; `closeRouter` (so exits cannot change a message); cancel the link run; `client.Close` (ends all this host's sessions at the gateway, bounded); `finishRun(endReason)`; close all panes (SIGTERM then SIGKILL, as `pane` does); shut down the server; remove the socket; remove `session/`; close the store (releases the lock). Every step bounded by `shutdownBudget` with `noteOverrun`, as today.

### The socket

- Path `<state>/daemon/daemon.sock`. **Path limit:** Unix socket paths are limited (104 bytes on macOS, 108 on Linux); the daemon refuses a longer path with an error naming the path, before binding.
- **Stale socket (review point 6):** only after the lock is held. `os.Lstat`; if it does not exist, bind. If its mode has `ModeSocket` and is not a symlink, remove it and bind. **Anything else (symlink, file, directory) is refused with an error naming the path and nothing is removed.** The check and the removal happen in one function, `removeStaleSocket`, the only place that removes a socket. A live daemon cannot exist here because it would hold the lock.
- After `net.Listen("unix")` the file is `chmod 0600`. The directory is 0700 and created through the existing owned-directory rules, so the permissive window between bind and chmod is not reachable by another user.
- No TCP listener, anywhere.

### Peer checks (every connection, before `hello` is read)

- **UID:** `SO_PEERCRED` on Linux (`unix.GetsockoptUcred`), `LOCAL_PEERCRED` on macOS (`unix.GetsockoptXucred`), through the connection's `SyscallConn`. A different UID gets `refused: not_authorized` and is closed. If the credentials cannot be read, the connection is refused (fail closed; this is the security check).
- **Parent chain (best effort, not a boundary):** peer PID from `SO_PEERCRED` / `LOCAL_PEERPID`; walk parents (`/proc/<pid>/stat` on Linux, `unix.SysctlKinfoProc("kern.proc.pid", pid)` on macOS) until PID 1 or a bounded depth (64); if any ancestor is in the set of PIDs of harnesses this daemon started, refuse with `agent_refused` and a message saying so, and log it. If the walk cannot complete, **allow** and log that. Comments and docs say plainly: a process that double-forks gets past it. The set comes from the session manager (`HarnessPIDs`), empty until slice 2; this needs one new accessor, `pane.Pane.PID()`.
- Reading the first message has a deadline (5 s, UNTESTED guess), so a silent connection cannot hold a goroutine.

### Handshake

First message must be `hello` (`Role` window or control). A **window** with a different build is refused `version_mismatch` with: "this window is build X and the daemon is build Y: run `rtk daemon stop`, then start the daemon again with the new build". **Choice:** a **control** connection is allowed across builds, otherwise the refusal's own advice could not work; control messages are few and a stale one cannot start a session. The reply to `status` carries the daemon's build, and `rtk daemon status` prints both.

### Commands (`cmd/rtk` and `internal/daemon`)

- `rtk daemon`: if a daemon already answers (control `hello`) → "a daemon is already running (pid N)" and exit 1. Otherwise **self-detach**: the parent opens `daemon/daemon.log` (append, 0600), runs `os.Executable()` resolved (as `executablePath` does) with `daemon --foreground`, `SysProcAttr{Setsid: true}`, stdin `/dev/null`, stdout and stderr the log, environment `os.Environ()`. The parent then polls the socket with a control `hello` (every 100 ms, up to 15 s, UNTESTED guess) while a goroutine `Wait`s on the child. Socket answered **and** the answering PID equals the child's: print `daemon started (pid N)`, `log: PATH`, and the environment note. The child exited first: print "the daemon did not start" with the log path and the last few log lines, exit 1. Answered by a different PID (a race with another start): "a daemon is already running (pid M)".
- `rtk daemon --foreground`: the same body, staying in the terminal, logging to stderr.
- `rtk daemon status`: control `status` → PID, builds, uptime, log path, link state, windows, and a table of sessions with holder. Not running: says so and exits 1.
- `rtk daemon stop [--yes]`: control `status` first; with sessions running and no `--yes`, prints them and refuses (exit 1); otherwise sends `stop` and waits (bounded 15 s) for the socket to go away, then says so.
- `rtk daemon stop --session NAME`: control `stop_session`.
- Signals in the daemon: SIGTERM, SIGINT → graceful stop; SIGHUP ignored.

### Retention

`Retain` runs once at start (step 5) and then on a 24-hour ticker in a goroutine started in step 8; errors are logged and retried at the next tick. Retention is store-internal and serialised by the store's writer, so it needs no coordination with the router.

### Log

`daemon.log` gets start/stop, link state changes, session start/end (names and exit codes), refused connections (reason, peer UID/PID) and errors. **Never** message bodies, credentials, tokens or terminal contents. A single `logf` helper is the only writer, so that rule has one place.

## Slice 2: sessions

### Gateway protocol 2

`protocol.WireVersion` becomes 2; a version 1 runtime gets the existing `version_mismatch` error. The gateway database needs **no migration**: the `sessions` table is unchanged. Paths keep the `/v1/` prefix (it names the URL family, the header names the protocol).

| Request | Meaning |
| --- | --- |
| `POST /v1/connect` | The daemon starting: ends this host's open sessions once, answers the directory. |
| `POST /v1/sessions` body `{"name"}` | Add one session. New ID. Ends only an open session of the same name. At most `maxAgentsPerHost` open per host. Answers the session and the directory. |
| `DELETE /v1/sessions/{name}` | End that session by name. Others untouched. 204 whether or not it was open. |
| `DELETE /v1/sessions` | Unchanged: end all (clean daemon stop). |

Gateway store: `StartSessions` is replaced by `AddSession` and `EndSession`; `EndSessions` stays. The old multi-name registration and `SessionsRequest` go (no unused code). Add and end each push a directory update as registration does today. `link.Client` gets `Connect`, `AddSession`, `EndSession`; `Register` goes.

### Connect, once, and the add-before-connect rule

The daemon runs a **connector** goroutine: `client.Connect` with backoff 1 s doubling to 15 s until it succeeds, then it stops for the life of the daemon (a second connect would end live sessions). Until then an add fails at once with "the gateway has not been reached yet (last error: ...)" and **no session starts** (architect's decision: no waiting, no queueing). The stream (`client.Run`) starts at once and is independent.

### Session manager (`internal/daemon/sessions.go`)

One manager, one mutex serialising add/end (never held while a client is written to). Sessions are keyed by name; at most one per name; names are `claude` and `codex`.

`Add(ctx, name, dir, size)`, in this order, so a failure leaves nothing behind:

1. Refuse an unknown name, a running name (the answer is "already running", not an error for `attach`), or "not connected yet".
2. `client.AddSession(ctx, name)` → session ID and directory. **Failure ends here: no harness.**
3. New agent ID; `server.AddConnectedAgent` (token); create `session/<name>/` (0700, no agent id: the hook command carries this path and a harness asks to re-trust a changed one; a leftover directory fails the add and is not deleted) and write the hook file there; `buildSpec`-equivalent with that directory.
4. `router.AddSession(spec)`; `router.SetGatewaySession(name, sessionID)`. These precede `pane.Start`, so the first offer already knows its session.
5. `pane.Start` with the exit callback; `router.PaneStarted`.
6. Any failure after step 2: `client.EndSession(name)` (best effort), `router.PaneFailed`, remove the token and the session directory; the caller is told why.

`End(name, reason)`: `client.EndSession` first (new sends get `session_gone` promptly; on failure the name is queued in `pendingEnd` and retried on the next link-up, and the router already refuses offers to an ended agent), then close the pane (SIGTERM, SIGKILL, as `pane` does), `server.RemoveAgent(id)`, remove the session directory. The pane exit callback calls `router.PaneExited`, which marks unfinished messages `interrupted` as today. A harness that exits by itself takes the same path (callback → `End`).

### Router and server changes (additive only)

- `router.AddSession(ctx, spec)` (new file `session.go`): inside the actor, if `byName` holds an agent in the exited state, drop it from `byName` and `order` (it stays in `agents` so messages that name it resolve), then call the existing `registerAgent`. A live duplicate is still refused by `registerAgent`. `RegisterAgent` itself is not edited.
- `server.RemoveAgent(id)`: deletes the token and the agent entry; its MCP and hook requests are refused from then on.
- Hook files for an ended session are deleted with its directory.
- `Pane.PID()`: a read-only accessor.

### Peers

A session's MCP instructions list peers from the directory at add time, as today. Peers that appear later are not added to a running harness's instructions (unchanged limitation; the gateway answers names it does not know with `unknown_recipient`).

## Slice 3: attach

The socket server serves windows. Per connection: a reader goroutine (decodes requests, one worker per session executes them in order), a writer goroutine (the only writer on the socket), and a `Window` holding the outbound state:

- **Coalesced:** one slot per pane holding the latest complete `PaneState`. The pane watcher (one goroutine per session, ticking at the pane's change rate with `Snapshot()` compared to the last sent) replaces slots; the writer drains them. Intermediate frames are dropped, never queued.
- **Reliable:** a bounded queue (256, UNTESTED guess) for `event`, `ownership`, `session_ended`, `pending`, `ack` and `result`. Overflow disconnects that window (`bye`). A write that misses its 5 s deadline disconnects it.
- **Events:** the daemon is the sole consumer of `router.Events()`; it fans each event out to every window's reliable queue. `Resync` makes a window request a fresh `snapshot` query.
- **Pending:** a 500 ms ticker calls `PendingOnQuit` while any window is attached and pushes `pending` when it changed. The attach answer carries the first value.
- **Attach** starts the listed missing sessions through the manager (sequentially, claude then codex), then answers `attached` with all current slots, start errors, snapshot and pending. A window then has the current frame for each session immediately; there is no scrollback.
- **Per-session FIFO:** each (window, session) has one worker reading its request queue, so a draft precedes its bytes, a submit follows its Enter, and `draft(false)` follows earlier writes. Acks are sent in that order. A request addressed to an ended session or an old ID gets `session_ended` / `no_such_session` and never reaches a replacement.
- **Queries** run on their own goroutines with a deadline and answer `result`. A disconnect cancels them.

## Slice 4: input ownership

One gate, `session.admit(window, op)`, called first by every input operation (key, paste, mouse, resize, draft, submitted, deliver_held, request_resize): not the holder → `not_holder`, nothing written, no router call. `take_input` is the only operation that is not gated by holding; it moves the holder, bumps `Rev`, and answers with the authoritative ownership; the previous holder gets an `ownership` message. Attach assigns an unheld session to the attaching window (so a lone window needs no keypress). A window's close releases its sessions and nothing takes them. The holder's size sets the PTY (only holder resize is applied; releasing keeps the size). The draft flag survives a holder leaving; `Drafting` is in `PaneState`. The loop budget is the router's, unchanged. The daemon never answers a prompt and has no "approve" operation.

## Slice 5: window keys, removal of the old path

Frontend wires F10, F6, Ctrl+C and the header labels. Backend: `rtk attach` and `rtk runtime [--agents]` (alias) call the same function; `rtk runtime pair` is untouched; `app.RunRuntime` and `runtime.go` are deleted, with `RunRuntime`'s tests. `rtk attach`/`rtk runtime` never start a daemon and exit with an error naming `rtk daemon` when none answers. `rtk attach` without `--agents` starts nothing, and with no sessions running says how to start one.

## Wire surface (slice 0)

Types are in `internal/daemon/wire` (compiled, declaration-only). Decisions:

- **Framing:** 4-byte big-endian length then a JSON `Envelope{type, id, body}`; one Write per message; reads refuse a length over `MaxMessageBytes` (8 MiB) before allocating. Paste text is at most 1 MiB.
- **Order:** `hello` → `hello_ack` (or `refused`, then close) → window sends `attach` → `attached`. The window's `id` on a request is echoed in its `ack` or `result`.
- **Coalesced vs reliable:** `pane` messages are complete and coalesced (latest per pane). `ownership`, `session_ended`, `event`, `pending`, `ack`, `result` are reliable. Because the two classes are not ordered against each other, `PaneState.Rev` (and `Ownership.Rev`, `SessionEnded.Rev`) increases on every holder or ended change, and a window keeps the highest `Rev` seen for those fields. A coalesced pane message can therefore never lose an ownership or ended change, and a reliable one can never be undone by a stale pane message.
- **Viewer vs ready:** `InputReady` is the transport only. Holding is `YouHold`. A viewer shows a ready transport and cannot send; the daemon refuses with `not_holder`.
- **Acks:** every request is acknowledged in per-session order, including `draft`, `submitted` and `deliver_held`; `Ack.OK` means the operation completed (for key/paste/mouse, the bytes reached the transport, with `Written`). Errors are `Ack.Error{code,message}`: `not_holder`, `session_ended`, `no_such_session`, `input_failed`, `too_large`, `bad_request`, `shutting_down`.
- **Mouse:** window sends pane-relative cell coordinates with a kind (`click`, `release`, `wheel`, `motion`); the frame message carries `Mouse` mode bits so the client's `MouseEnabled` decides locally; the daemon rechecks mode and ownership.
- **Order of slots and start errors:** `Slots` in fixed order (claude, codex), ended ones included; `StartErrors` in the order requested; a session that failed to register has no slot. The initial selection is the window's decision.
- **Disconnect:** the daemon never reconnects or replays for a window. A lost socket is final for that window; attaching again gets the current picture.
- **Control:** `status`, `stop`, `stop_session` on a `control` hello.

## How this meets the review points

1. **Plain `rtk` unchanged:** no edit to `app.go`, `tui`'s local constructor, or `RegisterAgent`; additions are new files and new methods. `Run` is not touched in slices 1 to 4; slice 5 deletes only `RunRuntime`.
2. **No client can block a pane or the router:** per-window goroutines, coalesced frames, bounded reliable queue with disconnect on overflow, write deadlines; the router's event channel has one consumer (the fan-out), which never blocks on a window.
4. **Window close changes nothing at the gateway:** a closed connection only releases input. No code path from a connection close reaches `client`, `router.PaneExited` or the store.
5. **Failure semantics unchanged:** the gateway's send/offer/ack/report code is not edited; a new session gets a new gateway session ID and `SetGatewaySession` precedes its first offer, so an older message is refused `session_ended` (existing rule). No queue appears: delivery is the router's, and a session that is not running has no agent to deliver to.
6. **Stale socket:** see "The socket". Under the lock only; Lstat; sockets only.
7. **Self-detach survives a lost terminal:** `Setsid`, stdin `/dev/null`, stdio the log, SIGHUP ignored in the child. Environment is the starting terminal's, captured once.

## Risks to name now (all untested)

- `Setsid` re-exec readiness polling and the PID cross-check.
- `LOCAL_PEERCRED` / `LOCAL_PEERPID` and `kern.proc.pid` through `x/sys` on macOS, `/proc` parsing on Linux.
- A pane watcher polling `Snapshot()` per session per window: cost and wake-up pattern unmeasured.
- A daemon crash leaves PTY children that nothing adopts (contract). A later `rtk daemon` after a crash runs `Recover` and shows the existing banner.
