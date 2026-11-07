# Network information API: contract

Status: **implemented in the uncommitted working tree on top of f411629 (2026-11-07), and never run.** It was written and statically checked only: `gofmt`, `go vet` (also with `-tags rtktest`) and `go build` were reported clean for darwin/arm64 and linux/amd64, and nothing was executed. The tests that were written have never run ([GR-3](../design/ground-rules.md)). Every behaviour below is what the code is written to do, not something observed. See "As built" at the end for the differences from the design. The user authorized building it ("Build an api for the api gateway to expose all necessary information to populate the network tab, ssh will handle the interactive remote entry"). Authority: [ADR-0004](../adr/0004-network-tab/index.html). Where this file and the code disagree, say so; don't edit one silently.

## What this is, and is not

- **Is:** a versioned, language-neutral way for host daemons to report bounded state to the gateway, for the gateway to aggregate it, and for the owner's interface to read it **through its own local daemon**. It exists to populate the Network tab.
- **Is not a terminal path.** The terminal, the keystrokes and the screen never touch the gateway. Entering a remote session is `ssh`, run by the owner's own machine with the owner's keys; see "SSH entry" below and [ADR-0004](../adr/0004-network-tab/index.html).
- **Is not:** remote worker dispatch (still deferred), a message path, or a way to read prompts, screens, credentials or environments.
- **Data path:** host daemons report state, the gateway aggregates it, the owner interface consumes it through the local daemon. **Nothing polls a host over SSH to fill the tab.**

## Vocabulary: unknown is not idle

Every consumer must obey these rules. They are part of the contract.

1. **A missing key means "not available".** It may be unsupported by that daemon version, or not known. It must never be drawn as a value, a placeholder or a default. Example: `model` is **never reported in v1**, because `rtk` doesn't know the model.
2. **`"unknown"` is an explicit state value,** used when the reporter knows it doesn't know. It is not `idle`, and nothing derives `idle` from it.
3. **A report is a claim about the past.** `age_ms` says how old. A report is **stale** when older than `report_stale_after_ms`, or when the host isn't live. A stale report is history: **never** read process survival from it, and never treat `busy`/`idle` in it as current.
4. **A host with no report is "no report",** not "no sessions" and not "idle". The directory still lists its registered sessions.
5. **Nothing here is globally authoritative readiness.** A reported state describes what that daemon observed at `state_at`. The gateway adds nothing to it.
6. **Times:** every `*_at` is RFC 3339 UTC. Times named `received_at`, `generated_at`, `last_heartbeat_at` and anything computed as an age come from the **gateway's clock**. `reported_at` and `state_at` come from the **host's clock**, are informational, and are never used to decide freshness.

## Versions

| Thing | Value | Rule |
|---|---|---|
| Gateway wire protocol (`X-Ratatoskr-Protocol`) | **stays 2** | The new routes are additive. They need no version bump, and an older peer simply doesn't use them. |
| Network schema (`schema` in bodies) | **1** | Bumped only for a change an old reader can't ignore. Unknown JSON fields must be ignored by every reader. A reader that sees a larger `schema` than it knows treats the data as unavailable and says so. |
| Daemon window/control messages | add-only | A window must still match its daemon's build exactly. |

## Gateway routes (new, protocol 2)

Both need a paired host's credential, like every other route (`withAuth`). The host is taken from the credential, never from a body.

### `PUT /v1/report`

A host daemon reports about **itself**. Replaces that host's previous report. Responds `204`.

```json
{
  "schema": 1,
  "seq": 41,
  "reported_at": "2026-11-06T14:02:11Z",
  "daemon": { "build": "9a585f7-20261106", "started_at": "2026-11-06T12:01:00Z" },
  "sessions": [
    {
      "session_id": "0192f2b4-…",        // the gateway's ID for this registration (joins the directory)
      "local_id":   "0192f2b4-…",        // the daemon's own ID. Reserved for a later exact-session attach (not built here)
      "name": "codex",
      "harness": "codex",                // "claude" | "codex"
      "harness_version": "0.9.1",        // only when known
      "started_at": "2026-11-06T12:01:05Z",
      "state": "busy",                   // starting | idle | busy | needs_you | unknown | exited
      "state_at": "2026-11-06T14:02:08Z",
      "input": "held"                    // held | free | unknown. Never which window
    }
  ]
}
```

- **`state_at`** is stamped by the reporting daemon, on its own clock, **when it first observed the current `state` value**. It isn't when the harness changed state, and it needs no router change.
- **Sent** on connect, within 1 s of any change in the daemon's sessions, their state, or who holds input (coalesced), and at least every **10 s** while the gateway link is up. `seq` rises by one per report within a daemon run.
- **Bounds:** body ≤ 16 KiB; ≤ 16 sessions; each string ≤ 128 runes. The gateway **re-sanitizes**: control characters stripped, over-long strings cut, unknown `state`/`input` values stored as `unknown`. A body over 16 KiB gets `413`; malformed JSON, or **more than 16 sessions**, gets `400`; both leave the previous report in place.
- **Never included:** prompts, message bodies, screen or PTY content, working directories, environment, credentials, tokens, file paths, window identities or counts of drafts.
- **Not stored in the database.** The gateway keeps the latest report per host **in memory** with its `received_at`. A gateway restart forgets all reports, and every host reads "no report" until it reports again (at most 10 s after its stream reconnects). This is deliberate: a persisted "running" record would outlive the process it describes.
- Unpairing a host drops its report. Unpair runs in the admin process, so the gateway gets no event: it compares each host's `PairedAt` at every compute and discards a report from an earlier pairing, which also covers re-pairing under the same name. The report is therefore dropped **by the next compute**: immediately for a fetch, within about a second for a waiting long-poll.

### `GET /v1/network?since=R&wait_ms=W`

Returns the aggregated snapshot. `since` and `wait_ms` are optional.

- Without `since`, it answers immediately.
- With `since=R`: if the gateway's `revision` is still `R`, it answers `204` (no change) after waiting up to `wait_ms` (default 0, **max 20000**). Otherwise it answers `200` with the new snapshot at once. This gives live updates without a new stream.
- `revision` is a counter held in memory that rises whenever anything in the snapshot changes, **other than** the raw ages, `received_at`, `last_heartbeat_at`, `seq` and `reported_at` (those change on every report or heartbeat and would wake every long-poll). Consumers keep ages current from `generated_at` and their own `fetched_at`. It rises for: a report's content, a host's `live` flag, a `stale` flag, `report_status`, or the directory.
- **Time alone changes `live` and `stale`,** with no event to announce it (a heartbeat just stops arriving). So the gateway recomputes every host's `live`, `stale` and `report_status` **on each `GET /v1/network`, and about once a second while any long-poll is waiting**, and raises `revision` if any differ from the last computation. A long-poll therefore wakes within about a second of a host going stale. Liveness still comes only from `HostLive`.
- `revision` resets when the gateway restarts, so a client treats **any revision lower than the last one seen as a restart** and refetches.

```json
{
  "schema": 1,
  "revision": 118,
  "generated_at": "2026-11-06T14:02:13Z",
  "gateway": { "protocol": 2, "started_at": "2026-11-06T11:00:00Z",
               "live_window_ms": 15000, "report_stale_after_ms": 30000 },
  "hosts": [
    {
      "host": "vmb",
      "connection": { "live": true, "stream_open": true,
                      "last_heartbeat_at": "2026-11-06T14:02:10Z", "heartbeat_age_ms": 3000 },
      "report_status": "fresh",          // none | fresh | stale
      "report": {
        "received_at": "2026-11-06T14:02:11Z", "age_ms": 2000, "seq": 41,
        "daemon": { "build": "9a585f7-20261106", "started_at": "2026-11-06T12:01:00Z" }
      }
    }
  ],
  "sessions": [
    {
      "address": "codex@vmb", "host": "vmb", "name": "codex", "session_id": "0192f2b4-…",
      "directory": { "registered": true, "live": true },
      "reported": {                       // null when the host has no report that lists this session
        "local_id": "0192f2b4-…", "harness": "codex", "harness_version": "0.9.1",
        "started_at": "…", "state": "busy", "state_at": "…", "input": "held",
        "age_ms": 2000, "stale": false
      }
    }
  ]
}
```

- `hosts` has one entry per paired host. `report` is `null` when `report_status` is `none`.
- `sessions` is the **directory joined with reports**. A directory entry with no reported session has `reported: null` (never a default state). A reported session the directory doesn't list is included with `"directory": {"registered": false}`.
- `report_status`/`stale`: `stale` when `age_ms > report_stale_after_ms` **or** `connection.live` is false.
- `live` and the 15 s window come only from the existing `HostLive`. No second liveness test is written.
- **Compatibility:** the existing stream's `directory` frames are unchanged. A client may use one as a hint to fetch sooner.
- A response is bounded by the number of paired hosts × 16 sessions.

## What the local daemon exposes (the owner-facing surface)

The owner's window or command never calls the gateway directly. They ask their **own daemon**, over the existing Unix socket with its user-ID and ancestry checks. That is the owner-facing authorization boundary.

- New window/control request **`network`**. Answer (the version number rides in the daemon's result envelope, not in this body, so `--json` is exactly this schema):

```json
{
  "schema": 1,
  "fetched_at": "2026-11-06T14:02:13Z",
  "local": { "host": "mac", "daemon": {"build": "…", "started_at": "…"},
             "sessions": [ { "session_id": "…", "local_id": "…", "name": "claude", "harness": "claude",
                             "started_at": "…", "state": "idle", "state_at": "…", "input": "held" } ] },
  "gateway": { "state": "connected", "error": "", "as_of": "2026-11-06T14:02:13Z" },
  "snapshot": { "…the gateway snapshot above, or null…" }
}
```

- `local` always comes from the daemon itself, **so one host's state is usable with no gateway at all.**
- `gateway.state`: `connected` · `unreachable` (with `error`, cleaned) · `not_configured` · `unsupported` (the gateway answered 404 for the new routes: an older gateway). The tab then shows **bare peer addresses with no live flag**, from the existing peer list. That is less than the directory a current gateway sends, and no wire change adds it back.
- `snapshot` is `null` only until the first successful fetch. After that the daemon **keeps the last snapshot** with its fetch time, even when `gateway.state` becomes `unreachable`, and `gateway.state` says it isn't current. Consumers use `generated_at` and `fetched_at` with the ages and never present a kept snapshot as live.
- The daemon fetches with `GET /v1/network?since=R&wait_ms=10000` (half the gateway's 20 s maximum, which is unchanged) **only while at least one window or `rtk daemon network --watch` has asked**, and stops when none remain. Because ages and `received_at` don't move the revision, a quiet healthy network answers only `204`, and a kept body would look minutes old. So **after a `204` the daemon fetches the full body once with no `since`** and installs it **without moving its remembered revision**. A shown age therefore overstates by at most about 10 s.
- A one-shot `rtk daemon network` waits for a fetch that **completed after the request began**, never for an earlier one, so repeated calls don't print the first snapshot ever fetched. It reports itself to the gateway whenever the link is up, regardless.
- New command **`rtk daemon network [--json] [--watch]`**: prints this answer (`--json` is exactly the schema above; the default is a table; `--watch` repeats on change). It's the language-neutral surface and the user's way to test the data without any tab.
- `rtk daemon status` is **not changed**: it has no `--json` and no session ID. `rtk daemon network --json` carries `local_id`, which is all this work needs. (ADR-0004's first draft wrongly said status listed the ID.)
- Window protocol: a `network` query on the window connection returns the same body, for the tab.

## Authorization, and what it doesn't protect

- **Gateway:** `PUT /v1/report` and `GET /v1/network` accept a paired host's credential, like sending a message. Any paired host can therefore **read every host's snapshot**. That discloses session names, harness, build, state and whether input is held: nothing that lets a reader act, type, enter or send as someone else.
- **Local:** the daemon exposes the answer only through its socket (same user, best-effort refusal of its own harnesses' descendants). It is **not** an MCP tool, a message type or a hook, so no agent can ask for it through its harness.
- **Honest limit:** agents run as the same OS user as the owner. Any of them can read the host credential file (`pairing.json`, mode 0600) and call the gateway route, or get past the descendant check and call the socket. **The API does not isolate same-user agents, and nothing here claims it does.** What it does is keep the data free of anything that grants control, and keep entry on SSH, which needs the owner's own keys.
- **Not granted:** this API gives no way to start, stop, enter, type into, or answer prompts of any session, and no new credential is created. Whether to add a separate read-only "observer" credential, so a messaging credential can't read the network, is **not decided** and not needed for this version.

## Known limits (recorded, not fixed)

- **No cap on parked long-polls.** Each is one authenticated request held for up to 20 s. Only paired hosts can park one.
- **A poll parked when its host is unpaired can still answer for up to 20 s,** because credentials are checked on arrival, not while waiting. The next request is refused.

- **Exit code 4** (no daemon) applies to plain `rtk attach` as well, not only to `--expect-session`.
- **`state_at` for a session that is already busy** when the daemon starts watching is when this daemon first saw it, not when it became busy.
- Reports flow only while the host's stream is connected. `seq` can have gaps after failed attempts.
- A one-shot `rtk daemon network` may answer from a body up to about 10 s old while a window's loop is running. This is slightly weaker than "a fetch completed after the request began" above.
- A window stays a fetch consumer until it closes.
- An older gateway gives bare peer addresses with no live flag.
- SSH entry works on Unix only (`hosts_other` refuses elsewhere). `internal/attach` builds only for darwin and linux, because its terminal code exists only for those two; other Unix builds fail.

## Failure behaviour

- Gateway unreachable: `gateway.state = "unreachable"`, the last `snapshot` kept with its `generated_at`, `local` unaffected.
- Old gateway: `unsupported`, directory facts continue through the existing link. The daemon stops sending reports after the first 404 and retries after the next reconnect, quietly.
- Old daemon on a host: that host's `report_status` stays `none`.
- Report rejected (400/413): logged once, previous report stays until it ages out.
- No offline replay: nothing is queued or resent. A report is replaced, never accumulated.

## Upgrade notes (for the user)

- Either machine may be upgraded first. Full data needs the **gateway and each daemon** on the new build.
- The gateway database schema doesn't change. No migration.
- A window must still match its daemon's build exactly, as today. Restarting the daemon with `rtk daemon stop --yes` ends its sessions, as today.
- Transport is unchanged: loopback and Tailscale use plain HTTP, a LAN address needs `--allow-lan`. **No HTTPS over a tailnet is added.**

## SSH entry (user-authorized 2026-11-06: "Implement SSH entry as well please")

The Network tab's Enter opens a remote session over SSH. **The gateway still carries no terminal and no keystrokes.** This section is the design the code follows. It changes one thing from ADR-0004's first proposal: **there is no separate relayed "can I enter this?" question.** The snapshot above already carries each session's `local_id`, state and `input`, so the plan is computed locally from the snapshot and the owner's host map. The remote `rtk attach` does the authoritative check.

### Owner host map: `hosts.json`

An owner-edited file in the state directory, beside `pairing.json` (`<state dir>/hosts.json`). It holds no secrets. It is read **at each Enter**, so edits apply without a restart. The daemon refuses a file that is a link, or writable by group or others, and says why.

```json
{
  "version": 1,
  "hosts": {
    "vmb": { "ssh": "me@192.168.1.23", "rtk": "/home/me/.local/bin/rtk" },
    "vmc": { "ssh": "build-vmc", "rtk": "/usr/local/bin/rtk", "state_home": "/home/me/.local/state" }
  }
}
```

- Keys are host names **exactly as the gateway lists them**. No guessing, no case folding, no default: a host without an entry has no SSH destination.
- `ssh`: a plain host name, `user@host`, a tailnet name, or an alias from the owner's `~/.ssh/config`. Pattern `^[A-Za-z0-9_][A-Za-z0-9_.@:-]*$`, at most 253 characters, so it can't read as an option.
- `rtk`: an absolute path on the remote machine, characters `A-Za-z0-9._/-` only. (A non-interactive SSH command often has no `~/.local/bin` on its path, so a path is required.)
- `state_home`: optional, same characters, absolute. It becomes `XDG_STATE_HOME` for the remote command only.
- Any other field, any value outside its pattern, or an unknown `version`: that host is **unusable**, with the reason shown. Other hosts still work.

### Entry plan (built by the local daemon, run by the window's caller)

The window sends its daemon `enter {host, session_id}` for a row from the gateway snapshot (the gateway's session ID), or `enter {local_id}` for a **local row**. A local row comes from the daemon's own `local` section, which exists with no gateway and so has no gateway session ID. The `local` section's sessions therefore carry `local_id` (already in the schema above as `local_id`), and the window addresses them by it. The daemon answers a **plan**, and the window builds no command of its own:

| Plan `kind` | When | What it carries |
|---|---|---|
| `local` | The row is on this host | The local ID and `in_window` (true/false). If true, the window focuses it on the Terminals tab. If false (a window's sessions are fixed when it attaches, and a window opened with `--expect-session` has only one), the note says "not in this window; reattach to see it". No SSH. |
| `ssh` | Mapped host, session found in the snapshot with a reported `local_id` | `argv`, a display string, host, a note, the by-hand fallback line |
| `manual` | Host not in `hosts.json`, or its entry is invalid | The command to run **on that host yourself** and the reason |
| `refused` | Session not in the snapshot, host unknown, no `local_id` reported, or no snapshot | A reason. **Never an attach without an expected ID.** |

The `ssh` argument list is exactly:

```
ssh -t -o ConnectTimeout=5 -- <ssh> <remote command>
remote command (one argument): [env XDG_STATE_HOME=<state_home> ]<rtk> attach --expect-session <local_id> --entered-via-ssh
```

- `<local_id>` must be a canonical UUID (36 characters, hex and hyphens) taken from the **snapshot**, never typed by the gateway as free text, and re-checked by pattern.
- The daemon adds **no other `-o` options**. In particular it never sets `StrictHostKeyChecking`, `UserKnownHostsFile`, `BatchMode`, identity files or ports: host-key checks, passwords, key passphrases and Tailscale SSH checks happen in the owner's terminal and follow the owner's own config.
- If the snapshot's report for that host is stale or the host isn't live, the plan is still built, with a note ("report is N s old; the host will check the ID"). Staleness weakens only the preview. The remote check is what decides.
- Strings from the gateway that appear in a note (names, reasons) are cleaned of control characters.
- Nothing from the gateway becomes a destination, a path or an option.

### `rtk attach` additions (add-only)

- **`--expect-session ID`**: attach only if the daemon on that machine has a **running** session whose local ID is exactly `ID`. It then attaches **only that session**. It refuses **before taking input** otherwise. Without this flag, behaviour is unchanged.
- **`--entered-via-ssh`**: the window knows it runs inside an SSH entry. Its Network tab disables Enter for other hosts' rows, with the note "press F10 to return to <the machine you came from>, then choose from there". It changes nothing else.
- **Exit codes** (today every attach failure exits `1` and an unknown flag exits `2`, so `3` and `4` are new): `0` normal (including F10 detach); `3` the expected session isn't running (ended or replaced); `4` no daemon is running; `2` for a usage or unknown-flag error. A remote build without `--expect-session` therefore exits `2`, and the caller must **not** retry without the flag.

### Handover and return

`rtk attach` becomes a loop in `internal/attach`, not a suspended program:

1. The window runs. On Enter for an `ssh` plan it **quits** (releasing the daemon connection and its input holds, as any close does) and returns the plan to the loop.
2. The loop prints one line (`rtk · opening codex@vmb over ssh. F10 inside returns here.`) and runs `ssh` with the terminal's stdin, stdout and stderr. **SIGINT is caught and discarded by the loop, not set to ignore** (`signal.Notify` to a drained channel, never `SIG_IGN`), because an ignored signal is inherited across exec, and `ssh` would then not be interruptible while connecting. **SIGHUP and SIGTERM end the loop:** it stops `ssh`, opens no new window, and exits.
3. When `ssh` exits, the loop starts a **new window**: it connects to the local daemon again and attaches **with an empty start list**, so a session that ended meanwhile is not restarted. "The same sessions" means whichever are still running. It opens on the Network tab with the same row selected, and refetches the snapshot. If no session is running any more, there's no window to open (a window needs at least one), so `rtk attach` exits with the existing "no sessions are running" guidance.
4. If reconnecting fails, `rtk attach` exits with the reason. The local agents are unaffected.

**Terminal reset.** After any **non-zero** `ssh` exit, or a signal, nobody has reset the modes the remote window set (alternate screen, mouse reporting, bracketed paste, keyboard protocol), and the next local window doesn't reset modes it didn't set. So the loop writes a **targeted reset** to the terminal before opening the new window: leave the alternate screen (`CSI ?1049 l`), turn off mouse modes (`?1000 l`, `?1002 l`, `?1003 l`, `?1006 l`), bracketed paste (`?2004 l`), pop the keyboard protocol (`CSI < u`), show the cursor (`?25 h`) and reset attributes (`CSI 0 m`). It must **not** send a full reset (`RIS`), which would clear the user's screen and scrollback. After a clean exit `0` the remote window has already restored its own modes. Owned by the attach loop.

This is chosen over `tea.ExecProcess` because a suspended program's window event channels may stop draining, and the daemon might drop the window. That risk is real but unverified, and quitting and reopening avoids it. It uses no terminal other than the one the user is in, and no Herdr.

### Outcomes the caller must tell apart

| `ssh` exit | Meaning | Message |
|---|---|---|
| `0` | Remote window ended normally (F10 or the session's end) | Back on the tab; refresh. |
| `3` | The expected session is not running there | "ended or replaced; not opened. Choose again." The tab refreshes. |
| `4` | The remote daemon isn't running | "no rtk daemon on <host>". |
| `2` | Remote `rtk` doesn't know `--expect-session` | "the rtk on <host> is too old for exact-session entry; upgrade it. Not attached." |
| `255` | SSH couldn't connect or log in: unreachable, timeout, host key, auth, or the link dropped mid-session | "ssh couldn't connect or the connection ended" with the by-hand command. rtk cannot tell these apart, and SSH's own text above explains. |
| other | Anything else | "the remote command exited N." |

- A dropped connection and an unreachable host both end in `255`. The remote window, if any, ends on the remote side and releases its input.
- The snapshot is refetched on return, because everything may have changed.

### What is deliberately not done

No Herdr dependency; no opening or switching of the user's other terminals; no SSH policy changes, trust shortcuts or credential handling; no agent path to any of this (window protocol only); no nested second hop (`--entered-via-ssh` disables it); no remote worker dispatch; no HTTPS over a tailnet.

## HTTP routing: chi

**User requirement (2026-11-06):** "Please also use chi framework for the api". Adding `github.com/go-chi/chi/v5` is authorized by the user. It is the only router; nothing else is substituted.

Facts, from chi's own README, release notes, `go.mod` and `path_value_test.go` at v5.5.0 (read 2026-11-06, not run):

- **Version:** v5.5.0 is the latest v5 release (14 Oct 2026). Its minimum Go is 1.24 and this module is on 1.27.3, so there is no Go-version issue. chi has no external dependencies, so the only additions are the module line and `go.sum`.
- **Compatibility with the gateway as it is:** `chi.Router` is an `http.Handler`, and chi middleware is plain `func(http.Handler) http.Handler`. `Server.Handler()` can return a chi router wrapped in the existing `withVersion` and `rewriteBareErrors`, in the same order. Auth stays `withAuth` on each handler, not chi's middleware.
- **Path parameters:** `{name}` syntax is the same as today's patterns. chi v5 also fills `r.PathValue`, shown by its own test, so `handleEndSession` and the message handlers may keep `r.PathValue("name")`/`("id")`. Either way works. Backend should confirm on build and use `chi.URLParam` if not.
- **Routes to preserve exactly:** the eleven in `Handler()` today, with the same methods, paths, version check, bearer auth and bare-error rewriting. The new routes `PUT /v1/report` and `GET /v1/network` use the same wrappers.
- **Things to check, not assume:** (1) chi's default 404 and 405 bodies differ from `ServeMux`'s, so set `NotFound` and `MethodNotAllowed` explicitly so that `rewriteBareErrors` still produces the wire error shape. I haven't read chi's default bodies or status codes. (2) Don't add chi's `Timeout` or `Compress` middleware on `/v1/stream` or `/v1/network`; the server deliberately has no write timeout, and the long-poll and stream must keep `http.Flusher`. chi's own response-writer wrapper preserves it, but a custom wrapper may not. (3) `{id}` and `{name}` containing escaped characters: keep the existing validation.
- **Behaviour differences accepted with the move to chi** (recorded by backend, agreed by the architect): `HEAD` on a `GET` route is now `405` (nothing in `rtk` sends `HEAD`); an unclean path (for example a double slash) is a `404` and no longer a `301`, so no credential can follow a redirect; `405` keeps chi's default behaviour with its `Allow` header and the wire error body.
- **Not changed:** transport, TLS pinning, pairing, credentials, the 15 s liveness window, wire protocol 2.
- **Dependency note for the lead:** `go get github.com/go-chi/chi/v5@v5.5.0` changes `go.mod` and `go.sum`. That fetches source and runs nothing; building with `go build` and `go vet` is allowed under GR-3, and no test is run.

## As built (2026-11-07, static review only)

Differences and additions relative to the design above. The code is the lead's accepted candidate; none of it has run.

- **Routes:** all 13 gateway routes, including the two new ones, are on chi v5.5.0 (a direct dependency).
- **Window:** F5 toggles the Network view inside any attached session (a tab, not a dashboard). Keys: F5 or Esc back to terminals, arrows and PgUp/PgDn select, R refresh, F6 input, F10 detach. Enter asks the daemon for a plan and never types into a session.
- **Own host:** a snapshot row whose session ID matches a local row is dropped; a disagreeing row stays. A session with no report reads "no report".
- **`local` plan:** focuses the slot if this window shows it, otherwise says "not in this window; reattach to see it". `manual` and `refused` show their reason and run nothing. `ssh` closes the window and the attach loop runs the daemon's seven-element argv with no shell; any other argv shape is refused.
- **`hosts.json`:** also refused if it is not a regular file, is owned by someone else, or is over 64 KiB. A missing file makes every remote row `manual`. `..` is refused in remote paths. A manual plan for an unusable host entry prints plain `rtk`.
- **Second hop:** in an SSH-entered window Enter on another host says "press F10 to return to the machine you came from, then choose from there".
- **Outcomes:** printed after ssh and repeated in the reopened tab. 255 also prints the plan's manual line. Any other code reports the remote exit code.
- **Handover (as built):** echo off, a 600 ms wait, input-only flush, exact termios restored, then ssh. A failed flush does not launch ssh and returns to the tab with the reason. The 600 ms mitigation rests on reading Bubble Tea v2.1.1 source, not on proof. It discards typeahead during the handover and cannot catch an arbitrarily late terminal reply. There is no flush on return.
- **Signals (as built):** SIGINT is caught and drained while the attempt runs and aborts the attempt during the handover. SIGHUP or SIGTERM stops ssh and exits with no new window. **New since the design:** ssh is stopped with SIGTERM and a kill after 2 s (`WaitDelay`), not os/exec's default SIGKILL, because a killed ssh cannot put the local tty back.
- **Terminal after an unclean ssh (new since the design):** the termios saved just before start is restored after any started ssh that did not exit cleanly, then the targeted reset is written (never RIS). A clean exit gets neither. A failed termios restore or a failed reset ends rtk rather than opening another window on an unknown terminal. The reset sequence alone does not touch termios, which is why both are needed.
- **Gaps in the short windows:** SIGINT has its default action between window end and the ssh attempt, and between the attempt's end and the next window's ping. rtk then exits to the shell with the tty cooked, no ssh running and the daemon's sessions continuing.
- **Known wrong text:** if ssh exits 0 after being sent SIGTERM, os/exec reports the context error and the printed line wrongly says ssh could not be started. This only happens on the way out, and ssh normally exits non-zero there.
- **Narrow terminals:** manual and outcome text crop in the feedback line. They are also printed between windows.
- **A remote exit of 255** cannot be told apart from ssh's own failure.
- Evidence notes from the build seats are in this directory (`network-tui-*`, `ssh-entry-*`). They are working papers, not contract.

## Work packages (original proposal; all five were built)

1. **Protocol and gateway** (backend): move `Handler()` to chi (see above), then types in `internal/protocol`, in-memory report store with revision and long-poll, the two routes, sanitization and bounds, unpair clearing.
2. **Daemon reporter and local answer** (backend): build the report from the session manager, router state and the gate; send on change plus every 10 s; the fetch loop; the `network` request; `rtk daemon network`; `id` in status.
3. **Window client method** (frontend): a `network` query on the window client.
4. **Network view** (frontend): the F5 view over (3). Read-only first. It may be left out without affecting 1 and 2.
4a. **SSH entry** (both, after 1-4): `internal/entry` (host map reader and validator, plan builder, pure and table-tested), `rtk attach --expect-session` and `--entered-via-ssh` with exit codes (backend), the attach loop and the Enter key (frontend), written tests for the quoting and the failure table.
5. **Docs**: README commands and schemas; this file; ADR-0004; user-run test steps.

Written tests are expected for 1 and 2 (table-driven for sanitization, bounds, staleness, revision, long-poll, unsupported-gateway). They are written, not run.
