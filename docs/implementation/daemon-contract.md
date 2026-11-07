# Per-host daemon: implementation contract

Owner: architect. Design source: [ADR-0003](../adr/0003-per-host-daemon/index.html). Authorized by the user on 2026-11-06: "Begin the implementation for daemon".

This is the contract for the daemon transition. **It's built. The user reports testing it by hand on 2026-11-06, and that it works apart from one open bug. The team has run nothing, and each item here isn't individually confirmed.** Engineers own the code structure. If something here is wrong or doesn't fit the code, raise it through lead to architect instead of quietly diverging.

## Rules

- **GR-3 stands:** `gofmt`, `go vet`, `go build` and static checks are allowed. Write tests, but don't run them. Don't run `rtk`, a daemon, harnesses, probes or SSH sessions.
- **Each commit or push needs the user's say-so.** This contract authorizes neither.
- **GR-1 and GR-2:** Go, and Bubble Tea v2 for the TUI. No new direct dependency without asking lead first; the design below needs none.
- **Plain `rtk` is local only and stays as it is.** It uses no daemon or gateway, and it's the regression bar.
- **Gateway behaviour stays:** transport, pairing, the ledger, every send result, immediate failure and no replay. Only session registration changes, described below.
- **Mark behaviour as untested** in code notes and reports.

## In scope, and not

| In this work | Not in this work |
| --- | --- |
| `rtk daemon`, its stop and status commands, `rtk attach`, and `rtk runtime` as an alias | **Any worker feature:** no spawn tool, worker records, `RTK_MAX_WORKERS`, permission mappings or Pi adapter |
| The daemon owning PTYs, MCP and hook server, gates, router, store and gateway link | Worktrees and remote dispatch |
| Starting and ending sessions individually, including at the gateway | Crash survival, resume or adopting surviving processes |
| Several windows, with explicit input ownership | Service setup with `launchd` or `systemd` |
| Protocol version 2 for per-session registration | Attaching across the network, or PTY bytes through the gateway |

Don't add unused worker code or settings. Nothing may claim workers exist. The loop budget stays as it is, per router, and resets on Enter from the input holder.

## Commands

```
rtk                                    # unchanged, local only
rtk daemon                             # start this machine's daemon in the background
rtk daemon --foreground                # the same, staying in this terminal
rtk daemon status                      # running or not, its sessions, windows and log path
rtk daemon stop [--yes]                # stop the daemon and every session
rtk daemon stop --session NAME         # end one session
rtk attach [--agents claude,codex]     # open a window, starting any listed session that's missing
rtk runtime [--agents claude,codex]    # an alias for rtk attach
rtk runtime pair ...                   # unchanged
rtk gateway ...                        # unchanged, apart from protocol version 2
```

- **Starting is always explicit.** `rtk attach` and `rtk runtime` never start a daemon. With none running, they exit with a clear error naming `rtk daemon`.
- **`rtk daemon` needs a saved pairing,** or it exits with the existing not-paired error. It doesn't need a reachable gateway to start, and its link keeps trying as `Client.Run` does today.
- **`rtk daemon` detaches itself:** it re-executes with a new session (`setsid`), with stdio going to `daemon/daemon.log`, waits until the socket answers or the child fails, then prints the PID and log path. A second `rtk daemon` reports that one is already running and fails. `--foreground` is for debugging and for later service managers.
- **`rtk attach` with no `--agents`** starts nothing. With no sessions running, it exits and says how to start one.
- **`rtk attach --agents ...`** starts each listed session that isn't running, in the directory `attach` ran from, and leaves running ones alone, even if they started elsewhere. If a session can't start, the window says so. Session names are only `claude` and `codex` for now, so a host has at most one of each.
- **`rtk daemon stop`** lists running sessions and refuses unless `--yes` is given or none is running. SIGTERM and SIGINT stop it gracefully, as `stop --yes` does. It ignores SIGHUP, because surviving a lost terminal is the point, and that also applies under `--foreground`.

## Components

| | Daemon | Window |
| --- | --- | --- |
| Owns | PTYs, emulators, router, gates, store, MCP and hook server, gateway link and credential, session lifecycle, input ownership | Drawing, keys, focus, which screen it's on |
| Must not | Decide what work to do, answer a prompt, retry a message | Hold a harness, open the database, read the credential, paste a peer's message |

- **PTYs stay in the daemon process.** This is ADR-0003's option A. **A daemon crash ends or orphans agents, and nothing resumes.** Don't track or kill survivors from an earlier daemon: `pane` only signals children it owns, and that rule stays.
- **Delivery stays only in the daemon.** A window never receives a message to paste, so more windows can't mean more pastes.
- **The TUI seam:** `tui.Pane` and `tui.Router` plus the event channel (`internal/tui/model.go`) are the intended boundary. A socket-backed client implements them, so the Bubble Tea model changes as little as possible. `tui.New` with in-process panes stays for plain `rtk`. A dependency from the client on `app`, `router`, `store`, `server` or `link` is a mistake.
- **`rtk runtime`'s old in-process path goes away** once the alias works: don't keep two ways of running connected agents. Plain `rtk`'s path stays.

## State directory

Under the existing state directory, same `0700` and `0600` rules:

- `daemon/daemon.sock`, `daemon/daemon.log`.
- The existing per-machine lock is taken **first**, through `store.Open`. Only the lock holder may remove a stale socket, and it must check that it's a socket and not a symlink before removing it. Plain `rtk` and the daemon still exclude each other.
- Each session gets its own subdirectory, `session/<name>/`, removed when that session ends. The whole `session/` directory goes when the daemon stops, as now. **The path must stay the same from one session to the next, so it can't contain an ID.** It appears in the hook command, and Codex asks the user to trust a changed hook again, skipping it until they do (`internal/agent/hooks.go`). A name stays reserved until its directory is removed, so two sessions never share one.
- `daemon.log` holds no message bodies, credentials, tokens or terminal contents.
- If the socket path is too long for the OS's limit, fail with a clear error naming the path.
- Retention runs at start and then on a daily timer, because a daemon rarely restarts.

The run model carries over: one daemon instance is one run in the store, with `Recover` at start and `interrupted` marking as today.

## Local socket

A Unix domain socket only. **No TCP, and no network path to it.** Framed JSON is fine. Engineers choose the framing and bound every message size.

- **Authentication:** the peer's UID must match the daemon's, checked with the OS's peer credentials (`SO_PEERCRED` on Linux, `LOCAL_PEERCRED` on macOS, through `golang.org/x/sys/unix`). Refuse anything else.
- **Refuse the daemon's own agents, as a best effort.** Get the peer's PID, walk its parent chain (`/proc` on Linux, `kern.proc.pid` on macOS), and refuse if it reaches a harness this daemon started. Say why in the refusal, and log it. If the walk can't complete, allow the connection, and log that. **This isn't a security boundary:** a process that double-forks can get past it, and code comments and the README must say so. It stops the accidental case, and the straightforward case of an agent running `rtk attach`.
- **Version handshake first.** The client sends its exact build version, and says whether it's a window or a control connection. A window with a different build is refused, with a message saying to run `rtk daemon stop`, then restart with the new build. There's no compatibility layer for windows.
- **Control connections work across builds,** so that a new build can stop an old daemon. Only `status`, `stop` and `stop --session` use them. They carry no input and no frames, and they follow the same peer checks and `--yes` rule. Their messages must stay readable across builds, so change them only by adding fields. `status` shows both versions when they differ.
- **Slow windows can't hold up the daemon.** Frames are latest-wins per pane per client, so intermediate frames are dropped, never queued. Other events use a bounded queue. A client that overflows it, or misses a write deadline, is disconnected. It reconnects by attaching again. Nothing in router or pane code waits on a client.
- **Frames:** send `pane.Frame` only when it changes, with the mouse mode, input-ready and error state that the `Pane` interface reports. Attaching gets the current frame and snapshot straight away. No earlier screen contents, because panes keep no scrollback.
- **Queries:** `History`, `Runs`, `MessageDetail`, outbound views and `PendingOnQuit` are requests to the daemon, which keeps sole access to the database.

## Input ownership

One holder per session. The daemon decides, and it applies the rule to every input operation: keys, paste, mouse, draft, submit, F4 and resize.

- **A window attaching takes input for any session nobody holds.** Sessions already held make it a viewer for those. *This changes ADR-0003's "new windows start as viewers", so that one window on its own doesn't need a keypress per session on every attach. The ADR will be updated.*
- **Viewers' input is refused by the daemon,** not just hidden by the window. A viewer's keys aren't forwarded to the harness.
- **F6 takes input** for the focused session, and works straight away. The previous holder sees that it happened, and becomes a viewer.
- **A window closing releases its input,** and nothing else gets it automatically.
- **Size:** the holder's size sets the PTY. With no holder, the size stays. Viewers show the frame as it is, cropped if it doesn't fit.
- **Drafts:** a draft flag stays set when its holder goes, because `rtk` can't see the input box. Attaching shows that the session is held as drafting, and F4 or Enter by the new holder clears it, as today.
- **Prompts:** nothing answers a prompt except a key press from the holder. The daemon doesn't know what a prompt asks. No request type means "approve".
- **The window must show:** whether it holds input or is viewing, per session.

## Window keys

Frontend owns the details and reports what's built:

- **F10 detaches,** with no agents stopped, and doesn't need the waiting-messages warning, since nothing is lost. The hint must say that it detaches.
- **The double Ctrl+C quit** also detaches. The first Ctrl+C still goes to the focused pane if this window holds input.
- **Ending a session from the window** isn't required, because of `rtk daemon stop --session` and quitting the harness itself.
- Plain `rtk`'s keys and text must not change.

## Session lifecycle

| Event | Session | Gateway | Messages |
| --- | --- | --- | --- |
| Window closes, SSH drops | Runs on | Nothing changes | Keep arriving and being gated |
| Harness exits, for any reason | Ends, and the window shows it | That session ends | New sends to it get `session_gone` |
| `rtk daemon stop --session NAME` | Ends: SIGTERM, then SIGKILL, as `pane` does today | That session ends | As above; unfinished ones become `interrupted` |
| `rtk attach --agents NAME` when ended | A **new** session with a new ID | Registered | Never given an older message |
| `rtk daemon stop --yes` | All end | All end | Unfinished ones become `interrupted` |
| Daemon crash | Ended or unknown | Liveness times out | `interrupted` on next start, never pasted again |

- A failed gateway registration means **that session doesn't start,** with the reason shown. No harness starts first, as today. So an unreachable gateway can't start sessions, but sessions already running keep running.
- Starting a session after the router is running needs router and server support for adding, and for ending and later replacing, an agent by name. Tokens and hook files for an ended session must stop working.

## Gateway protocol version 2

Same rules as version 1. Version 1 runtimes are refused with the existing explicit version error, so all machines upgrade together. `rtk gateway` must be restarted on the new build, and the existing database must keep working, with a migration if one is needed.

- **Connect:** a daemon starting ends this host's earlier sessions, once, because they belonged to the previous daemon instance. Its stream replaces any earlier one, as now.
- **Add:** register one session at a time. A new ID each time. It replaces only an earlier session of the same name.
- **End:** end one session by name. The host's other sessions are untouched.
- `maxAgentsPerHost` stays as a limit on open sessions per host.
- A clean daemon stop ends all the host's sessions, as `DELETE /v1/sessions` does now.
- Nothing about sends, offers, acks, reports, heartbeats or the result names changes.

## Done means

Reviewed, integrated code that builds on darwin and linux, passes `gofmt` and `go vet`, has tests written but not run, and keeps plain `rtk` unchanged by review. A report separates what's implemented from what's untested, and gives architect the actual commands, keys and printed lines, so that the README and ADR-0003 can be finalized. **Only the user's own runs count as verification.**
