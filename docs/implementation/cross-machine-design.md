# Cross-machine messaging: backend design

Owner: `engineering.backend`. Written 2026-11-05 under GR-3. Contract: [cross-machine-contract.md](cross-machine-contract.md) (architect's). Everything here describes code that does not exist yet unless marked LANDED, and **nothing has been run**.

## Shape

One binary, three modes. Plain `rtk` is untouched and is the regression bar: `Config.Link == nil` selects local mode, and every local code path stays byte-for-byte as it is.

```
internal/protocol   connected.go, wire.go   LANDED (shared types, wire shapes)
internal/router     link.go                 LANDED (Link seam, Config.Link); behaviour in slice 5
internal/gateway/   store, pairing, listen check, directory, routing, ledger, HTTP, admin CLI
internal/link/      runtime side: pairing client, sessions, stream, ping, reconnect, Link impl
internal/store      migration 0002 (sends, inbound columns)
cmd/rtk/main.go     dispatch: gateway, runtime (plus existing hook, --version)
internal/app        a second Run path for `runtime`; Run for plain rtk unchanged
```

`internal/gateway` imports only `protocol` and the standard library plus the SQLite driver. It must not import pane, tui, agent, router or store (the contract's component split, enforced by review of imports).

## The six fit problems

**1. Names.** Three names, each with one job. The *local name* (`claude`, `codex`) is the display name, the hook-file stem and the `--agent` in the hook command; its pattern `^[a-z0-9_-]{1,32}$` and `HookCommand` do not change, so Codex's hook trust is not disturbed. The *network name* is `name@host` (host `[a-z0-9-]+`), built from the local name and the host in the credential; the runtime never types it, the gateway composes it. The *session ID* is new per registration and never shown to a model. `Params.Peers` and the MCP instructions carry network names, so peers get a second, wider pattern (`name@host`) while `DisplayName` keeps the narrow one. The agent's own instructions say "You are claude@mac". `server.AddAgent` gains a label for that; the hook file and token map stay keyed by the local name.

**2. One runtime as sender and recipient.** Separate tables, one message ID. `messages` stays the *inbox and delivery record* with exactly today's lifecycle (`queued` to `interrupted`) and its `UNIQUE(id)`, which is what stops a double paste. A new `sends` table holds *outbound attempts* (`sending` then a result, plus the recipient's reports). The same ID lives once in each table, so a same-host send has no key conflict, and no inbox rule is weakened: the inbound insert is `INSERT ... ON CONFLICT(id) DO NOTHING`, and a duplicate offer re-acks without queueing. Migration 0002 adds to `messages`: `origin TEXT` (the sender's network name; empty for local), `from_name TEXT` (for display), `reported_state TEXT` (last state reported to the gateway). `from_agent_id` keeps holding the network name for remote senders; History reads `from_name` when there is no agent row. `MessageExists` for `reply_to` looks in both tables. Crash recovery also marks any `sends` row still `sending` as `gateway_uncertain` (reason `crashed`), because a crash mid-request cannot say whether it left the machine.

**3. Remote recipients and `reply_to`.** In connected mode the router resolves nothing about the recipient locally: `to` is a network name (a bare name means "this host", the one addition I propose, to keep `to: "codex"` working; architect to confirm), and `unknown_recipient`, `destination_unreachable` and `session_gone` all come from the gateway. Local checks that stay: unknown sender, empty body, size, budget, self-send, `reply_to` existence in this run. The gateway adds the check only it can make: `reply_to` naming a message whose sender session has ended gives `session_gone`.

**4. The actor must not wait on the network.** The actor does the local checks and the `sends` insert (`sending`), then returns a pending handle and hands the request to a goroutine that calls `Link.Send` under the 8 s bound. The result comes back as an actor closure that commits the result and emits the view. `Router.Send` already waits off the actor for its reply; in connected mode it waits for the final result instead of the insert. An offer arriving from the stream enters the actor as one closure (insert inbox row, then ack from a goroutine after the commit). Hooks and delivery never wait on a socket.

**5. Admin commands while the gateway runs.** No admin route. The gateway database is SQLite in WAL mode with a busy timeout; only `rtk gateway --listen` takes the gateway's process lock (`gateway/gateway.lock`, distinct from the runtime's). `pair`, `unpair`, `hosts`, `log` and `show` open the database without that lock and read or write it directly, so they work while the server runs. Authentication reads the database on every request (no cache), so `unpair` refuses requests immediately. The open stream re-checks the host's credential row on every ping tick and closes when it is gone: closure within one ping interval (5 s), as the contract now says. `hosts` state comes from the database: each `POST /v1/heartbeat` writes `last_heartbeat_at` (the gateway's own receive time), and `hosts` shows `live` if it is under 15 s old and `last heard N ago` otherwise. So "live" in `hosts` can lag by up to 15 s. UNTESTED.

**6. Startup order.** `rtk runtime` does: load pairing (error and exit if missing) → `store.Open` (lock) → `Recover` → `StartRun` → dial the gateway and `POST /v1/sessions` for the chosen agents → on any failure, finish the run and exit before any harness starts → start the router with the link → `startAgents` using the directory from the response for the peer list. Agent IDs are created before registration, not inside the launch loop. Plain `rtk` keeps its present order.

## Send path (connected mode)

1. Local checks as today, in the same order, minus recipient resolution. `r.used++` at record time, as now.
2. Insert the `sends` row as `sending`; return nothing yet.
3. Goroutine: `Link.Send` with an 8 s deadline. `ErrNotSent` means `gateway_unreachable`. Any other error after a write began means `gateway_uncertain`. A gateway answer carries the result. The tool result is the final result: `host_stored` is success; every other result is a tool error whose text says whether the message could still arrive.
4. Commit the result on the actor, emit `SendView`. Late reports (`POST .../status` mirrored from the gateway, or `Lookup` after a reconnect) update `Result` for the two uncertain results and `Report` for any, and never touch a gate.
5. No retry, no outbox, no resend. An unsent attempt stays in the local record.

## Receive path

`Receive(offer)` on the actor: refuse if the named agent is not on this host or exited (ack refused with a reason); insert the inbox row with `origin` set; if it was already there, ack again and stop; else build a normal `msg` (sender shown as `name@host`) and queue it, so the gate, frame, paste and hook confirmation are today's code unchanged. Ack goes out after the commit. State changes after that are reported by a goroutine: `Report` per transition, and on every reconnect the runtime reports every inbound message whose `state` differs from `reported_state`. Messages are never resent.

## Link (runtime side, `internal/link`)

One long-lived `GET /v1/stream`, newline-delimited JSON, with the Link methods as plain authenticated POSTs. Dials out only. The gateway pings every 5 s; the runtime counts the connection dead when no ping has arrived for 15 s and then redials with backoff (1 s doubling to 15 s), state `reconnecting`. While the stream is open the runtime also sends `POST /v1/heartbeat` every 5 s (architect's decision: a write to a peer proves nothing about the peer reading, so liveness comes from what the gateway receives). A failed heartbeat is not shown to the user by itself; it only feeds the same dead-connection rule. State changes are delivered to the router as `Event.Connection`; the directory in each `directory` frame replaces `ConnectionView.Peers`. UNTESTED guess for every timing, as the contract lists.

## Gateway (`internal/gateway`)

Tables: `hosts` (name, credential hash, created, last_heartbeat_at), `pairing_codes` (hash, host, expires, used), `sessions` (id, host, name, started, ended), `messages` (id, from address, to address, reply_to, body, result, report, reason, created, updated), `message_events`. Auth middleware reads `hosts` per request. Registration ends the host's earlier sessions. `POST /v1/messages` is: validate, store the full body, resolve (`unknown_recipient`; no live stream gives `destination_unreachable`; no current session gives `session_gone`), offer once on the stream, wait up to 5 s for the ack (`delivery_uncertain` on timeout), record every outcome. Duplicate ID: no second store, no second offer, return the stored result. Failure records are never delivered later. No retention code.

**`--listen`** (DevOps review, static): `netip.ParseAddrPort` on the exact string; require a numeric non-zero port, empty zone, not 4in6; accept `IsLoopback()` or literal prefix `Contains` for `100.64.0.0/10` or `fd7a:115c:a1e0::/48`; reject hostnames, unspecified, missing port and mapped IPv4 *before* any unmap or bind. Bind the parsed `AddrPort.String()` with `tcp4` or `tcp6` by family, never a re-resolved original string. After `Listen`, check the listener's actual address. Prefix membership does not prove this machine owns that Tailscale address; the bind succeeding on the local interface does, and the error says so. The gateway listener is separate from the MCP/hook server.

## CLI

`rtk gateway --listen ADDR`; `rtk gateway pair HOST` (prints a 10-minute single-use code; replaces an existing host's credential); `unpair HOST`; `hosts`; `log [--follow] [--limit N]`; `show ID`. `rtk runtime pair --gateway ADDR --code CODE`; `rtk runtime [--agents claude,codex]`. State: `<state dir>/gateway/gateway.db` and `gateway.lock` (0700 directory, 0600 files); the runtime's credential file `<state dir>/pairing.json` (0600: gateway address, host, credential). `--follow` polls the database; no stream.

## Slices

1. **LANDED:** `protocol/connected.go`, `protocol/wire.go`, the `Event`/`Snapshot` fields, `router/link.go`. Types and a seam only, no behaviour. Written by me directly as declarations; the behaviour slices below go through monkey.
2. Store: migration 0002, `InsertSend`, `FinishSend`, `ReportSend`, `InsertInbound` (dedupe), `InboundUnreported`, History `from_name`, `MessageExists` over both tables, `Recover` for `sending`. Tests unrun.
3. Gateway (split): 3a store, pairing, listen check, admin CLI; 3b HTTP routes, stream, routing, ledger.
4. `internal/link`: pairing client, sessions, stream, ping, reconnect, `Link`.
5. Router connected paths: `sendRemote`, `Receive`, reports, views and events. This is the regression-risk slice: review is "local-mode behaviour unchanged", checked by diffing that every local path is untouched.
6. Server: network-name instructions and label, `host_stored` and result mapping, the `StatusInbox` assertion.
7. `cmd/rtk` dispatch and `app` wiring for `runtime` and `gateway`.

## Raised to architect (through lead)

- **Bare names** in `to` mean this host (proposal above).
- **`hosts` freshness** is up to 15 s stale (settled): it reads the last heartbeat row, not the live process.
- **`unpair` and the stream:** as the contract now says, within one ping interval.
- **Unsent attempts** show in the sender's TUI only, never upload (as the contract defaults); the `sends` table is local.
- A **second `sends` row is not made** for a repeated call: each call is a new message with a new ID (the contract's double-send caveat stands).
- **Same-ID dedupe at the gateway** returns the stored result, so a runtime that lost a response and asks again learns the real outcome rather than a second delivery.

## Network and file rules adopted from the DevOps static review

Source: `~/.openrig/shared-docs/rigs/team-ratatoskr/state/platform/gateway-network-review.md` (static, untested). Adopted as binding for the gateway and link code:

- **Transport.** One dedicated `http.Transport` for pairing and every authenticated call: `Proxy: nil`, bounded dial and header timeouts, and a `CheckRedirect` that refuses every redirect. The saved gateway address is validated by the same rule as `--listen` on load, not only when it is typed. No fallback to another address.
- **Stream versus POSTs.** The stream has no whole-response write timeout, no `Client.Timeout` and no request deadline; it has a cancellable context. Every other call is a separate bounded POST (8 s sender, 5 s ack, as the contract says). Backoff re-dials the stream only; it never resends a message.
- **Stream writes.** One serialized writer per host with a bounded queue. Each frame sets a write deadline with `http.ResponseController`, writes, flushes and checks both errors; the first frame is flushed at once. No `TimeoutHandler`, no buffering wrapper, no database transaction or lock held across network I/O. The client reads with a bounded line size (1 MiB, above the 64 KiB body limit after JSON escaping); a longer line closes the stream and is shown as an error.
- **Liveness.** Decided by architect: heartbeat. The gateway counts a host live only if its stream is open and it heard a heartbeat within 15 s. All of that sits behind one function, `hostLive(host, now) bool` in `internal/gateway`, used by routing, `hosts` and the stream; nothing else decides liveness. Honest window: for up to 15 s after a silent drop the gateway still counts the host live, so a send in that window ends `delivery_uncertain` (offered, no ack), never `destination_unreachable`. The runtime's own dead-connection rule is ping silence for 15 s.
- **Files.** The gateway directory is created or repaired to 0700 and the database pre-created at 0600 before SQLite opens, with errors checked; I also chmod `-wal` and `-shm` after they exist but do not claim that proves every sidecar mode, and never delete a live sidecar. Credential and temp files are created 0600 in the private directory and renamed into place. Existing symlinks and non-regular files at those paths are refused. No process-wide `umask` change: it would be inherited by harness children and change local behaviour. The gateway never calls `store.Open`, so it takes neither the runtime lock nor its migrations or retention; it has its own lock file. Gateway state must be on a local filesystem.
- **Listen.** Port 0 is refused (stable endpoint). Userspace-networking Tailscale (no host route) is not supported by the direct bind; the error says to use a routed interface.

## Decisions recorded after slice 1

- **Heartbeat** (architect): `POST /v1/heartbeat` every 5 s while the stream is open; shape in `protocol/wire.go` (`HeartbeatPath`, `HeartbeatRequest`, whose content the gateway ignores except for authentication). Folded into slices 3b (route, `hostLive`, `last_heartbeat_at` write) and 4 (the runtime's heartbeat loop). Slice 3a (store, pairing, listen check, admin CLI) only creates the column.
- **Port 0** refused for `--listen`.
- **Bare names** (architect, approved): a bare name in `to` is `name@thishost` and still goes through the gateway, with no local shortcut, so the central view is complete.
- **Connection states** (closed set): `connecting`, `connected`, `reconnecting`, `refused`. `Snapshot.Runtime` marks a connected run; `AgentView.Address` carries `name@host`; `SendView.Report` and `SendEventView.Reported` mark recipient reports apart from what this host observed.

## Slice 2: runtime store, additive only

Migration `0002_connected.sql` adds three tables and changes none: `sends` (`seq`, `id` UNIQUE, `run_id` FK cascade, `from_agent_id`, `to_address`, `reply_to`, `body`, `result`, `report`, `report_reason`, `created_at`, `updated_at`), `send_events` (`id`, `send_id` FK cascade, `at`, `kind` = `result`|`report`, `value`, `reason`), and `inbound` (`message_id` PK, FK to `messages` cascade, `origin` = the sender's network name, `reported_state`). A remote sender has no agent row, so an inbound row's `from_agent_id` holds the network name and the read queries show `COALESCE(f.display_name, i.origin, '')` through a LEFT JOIN on `inbound`; with no inbound rows (local mode) results are identical. Retention cascades by run as before.

Effect on local mode, stated plainly: the first plain `rtk` run after this build migrates an existing database from version 1 to 2, with the existing `VACUUM INTO` backup (`ratatoskr.db.bak-v1-<time>`) written next to it, and an older `rtk` binary will then refuse the newer database (`ErrNewerSchema`). No existing table, column or query result changes.

Operations (all UNTESTED): `InsertSend` (row `sending` plus first event), `FinishSend(id, from, to, reason, at)` as a compare-and-set over the result graph below, `ReportSend` (stores the recipient host's report and an event; never changes `result`), `Sends(runID, limit)`, `Send(id)` with events, `InsertInbound(m, origin) (inserted bool, seq, err)` (one transaction: `messages` row `queued` plus its event plus the `inbound` row; `ON CONFLICT(id) DO NOTHING`, and `inserted=false` for a duplicate with nothing written), `MarkReported(id, state)`, `InboundUnreported(runID)` (inbound messages whose state differs from `reported_state`, with the latest reason and time). `MessageExists` also finds ids in `sends`. `finishRun` (clean exit and `Recover`) also moves every `sending` row to `gateway_uncertain` with reason `run_ended` or `crashed`.

Result graph for `FinishSend`: `sending` to any of the other seven results; `gateway_uncertain` to any of the other six results (a `Lookup` after reconnect reports the gateway's truth; a gateway that never stored it is recorded as `gateway_unreachable`); `delivery_uncertain` to `host_stored` (a late ack) or `session_gone` (a late refusal, with its reason); every other result is final. Anything else is `ErrIllegalTransition`; a changed row is `ErrStateChanged`, as for messages.

## Slice 3b: gateway server

`internal/gateway/server.go` (+ `hub.go`): `New(store, opts)`, `Handler()`, `Serve(listener)`, `Shutdown`. Routes as the contract; every route except `/v1/pair` first checks the protocol header (a mismatch is `version_mismatch`) and then calls `Store.Authenticate` with the bearer credential, every request, no cache. Bodies are capped (send bodies at 64 KiB raw, 512 KiB on the wire after JSON escaping).

- **Sessions.** `POST /v1/sessions` ends the host's earlier sessions, creates one session per validated local name, and returns the sessions and the directory. `DELETE` ends them. The directory lists every agent with an open session, `Live` from `HostLive`; a changed directory is pushed to every open stream as a `directory` frame.
- **Stream.** One per host; a new one cancels the old. Headers, then a first `directory` frame flushed at once. One writer goroutine per stream reads a bounded queue (64) of frames and sets a write deadline and flushes for each frame, checking both errors; a failed or stalled write ends the stream. A ping goes out every 5 s. On each ping tick the stream re-runs `Authenticate`; a removed credential ends it (the unpair rule: within one ping interval). `streamOpen` for `HostLive` comes from the hub; the heartbeat time comes from the database.
- **Heartbeat.** `POST /v1/heartbeat` authenticates, then `TouchHeartbeat` with the gateway's clock. The request body is ignored.
- **Send.** `POST /v1/messages`: validate (id, sender has an open session on the credential's host, `to` is `name@host`, non-empty body, size), then **store the full body before anything else** with result `stored`, then resolve in this order: a repeated id returns the stored result and does not store or offer again (if the first request is still waiting for its ack, the repeat waits on the same completion for the remaining time); an address no session has ever carried is `unknown_recipient` with the directory; `HostLive` false is `destination_unreachable`; a live host whose name has no open session is `session_gone`; a `reply_to` whose sender session is no longer the current session of the name being replied to is `session_gone`. Otherwise record `to_session`, enqueue one `offer`, and wait up to 5 s for the ack: saved gives `host_stored`; refused gives `session_gone` with the host's reason in `Detail` (raised below); no ack gives `delivery_uncertain`. If the offer could not even be enqueued (stream gone or queue full) nothing was written to the host, so the result is `destination_unreachable`. A write failure after enqueue is `delivery_uncertain`, never a definite failure. The 15 s silent-drop window follows: a host that vanished without closing its stream still has a stream entry and a heartbeat under 15 s old, so the offer is written and no ack comes, which ends `delivery_uncertain`.
- **Ack and status.** `POST /v1/messages/{id}/ack` and `/status` are accepted only from the recipient's host and are idempotent. A late ack corrects `delivery_uncertain` to `host_stored` with an event; a status stores the report and an event and forwards a `report` frame to the sender's open stream. `GET /v1/messages/{id}` is answered to the sender's or recipient's host only, as a `MessageInfo`.
- **Ledger writes** are small store methods (`InsertMessage`, `SetResult`, `SetReport`, each appending a `message_events` row in the same transaction). Message rows get `from_session` and `to_session` columns; the schema has never been created anywhere, so slice 3b edits `0001_init.sql` instead of adding a second migration.
- Constants, all `UNTESTED guess`: ack wait 5 s, ping 5 s, write deadline 5 s, queue 64.

**Contract additions I need from architect (raised through lead).** (a) A `report` frame on the stream, so a sender learns the recipient host's reports without polling: `StreamFrame` gets `Report *MessageInfo`, constant `FrameReport` (added to `protocol/wire.go`). (b) A recipient host's explicit refusal of an offer has no named result in the contract's table; I map it to `session_gone` with the reason in `Detail` (certain not delivered, nothing queued). (c) Enqueue failure is `destination_unreachable`.

## Slice 5: router connected paths (the regression-risk slice)

All new behaviour lives in a new file `internal/router/connected.go`. `Config.Link == nil` must leave every local path byte-for-byte as it is. The complete list of edits to existing router files, which the review will diff against:

1. `send.go`, `Send`: one added first statement, `if r.cfg.Link != nil { return r.sendConnected(ctx, from, to, body, replyTo) }`. `send()` is untouched. `SendError` gains `Detail string` and `Uncertain() bool` (the zero value is today's behaviour).
2. `router.go`: the `Store` interface gains the slice 2 methods; the agent and message structs gain fields (`address`; `origin`), whose zero values are local mode.
3. `deliver.go`: `move()` gets one trailing line, `if m.origin != "" { r.queueReport(m) }`; the frame build and the views read the sender name through a helper `senderName(m)` that returns `m.from.name` when `m.from != nil` (every local message) and `m.origin` otherwise.
4. `views.go` / `history.go`: the snapshot and event builders fill `Runtime`, `Connection`, `Sends` and `AgentView.Address` only when `Link != nil`; `storeMessageRowToView` already receives the sender name from the store query.
5. `fakes_test.go` gains the new store methods.

Nothing else changes: not `gate`, `nextState`, `hook`, `pump`, `startWrite`, `onWriteResult`, `tick`, `RegisterAgent`, or any `Close` path. If a diff shows more than these edits, it is a defect.

**Send (connected).** `sendConnected` runs the local checks on the actor in the same order as `send()` minus recipient lookup: storage, sender, bare name to `name@thishost`, self-send (`unknown_recipient`), empty, size, `reply_to` exists in `messages` or `sends`, budget. Then `NewID`, `InsertSend` (`sending`), `r.used++`, emit the `SendView`. It returns a pending handle to the caller's goroutine, which runs `Link.Send` under the 8 s bound off the actor; `ErrNotSent` is `gateway_unreachable`, any other error after a write began is `gateway_uncertain`, otherwise the gateway's result. The result is committed back on the actor with `FinishSend`, and `Send` returns it: `host_stored` is `(id, nil)`; every other result is a `*SendError` whose `Reason` is the result name, `Uncertain()` is true for the two uncertain results, and `Detail` carries the directory for `unknown_recipient`. If the caller's context ends first, the goroutine still finishes and records the result: an abandoned wait must not leave a `sending` row.

**Receive.** `Router.Receive(ctx, offer)` enqueues one actor closure: unknown or exited local agent gives a refused ack (`no_such_agent` or `exited`); storage down gives a refused ack (`storage_unavailable`); else `InsertInbound`. A duplicate offer acks saved again and does nothing else. A new one builds a `msg` with `from == nil`, `origin = offer.From`, appends it to the log and the agent's queue, emits it, and then `pump`s; the ack goes out from a goroutine after the insert commits. From there the gate, frame, paste and hook confirmation are the existing code. Inbound messages do not touch the budget.

**Reports.** `queueReport` hands `(id, state, reason, at)` to one reporter goroutine over a bounded channel; it calls `Link.Report` and on success `MarkReported`. A full channel drops the report; it is recovered because `Router.LinkUp()` (called by the link after every successful (re)connect) reads `InboundUnreported` and queues them all. Reports never touch a gate. Incoming `report` frames arrive through `Router.LinkReport(info)`, which applies `FinishSend` (for the two uncertain results) or `ReportSend` and emits the `SendView`.

**Link callbacks into the router** (all enqueue and return): `Receive`, `LinkState(view)`, `LinkDirectory(peers)`, `LinkUp()`, `LinkReport(info)`. `LinkState` updates the `ConnectionView` and emits `Event.Connection`; the snapshot carries it with `Runtime: true`.

**Quit order** gains one step: `Router.Close`, then the link closes (`DELETE /v1/sessions`, heartbeat stopped), then `store.FinishRun`, then the panes. Reports for messages that become `interrupted` at quit are not sent; the gateway learns of them late or never. UNTESTED, and recorded as a known gap.

## Amendment after architect's answers (slice 3b additions)

- **Refusal reason reaches the tool result and the ledger.** An ack refusal's reason is kept in three places: the gateway's `message_events` row for the result (its `reason`), `SendResponse.Detail` and `MessageInfo.ResultReason` (new, in `protocol/wire.go`), and on the sender's side the `sends` result event's reason and `SendError.Detail`, which the tool result shows ("session_gone: the recipient host refused it: storage_unavailable"). `session_gone` is the result name; the reason is what tells storage-down from an ended session.
- **Ended sessions in the central view.** 3b also changes `admin.go`: `gateway log` and `gateway show` show `(session ended)` beside a message's last reported state when its `to_session` has `ended_at` set in `sessions` (a LEFT JOIN at read time; `Log`, `Tail` and `Message` return an `ToSessionEnded bool` in `MessageRow`). No new reporting and no new column beyond `to_session`. An unrun test covers both commands: a message with report `held` whose session ended prints the marker, one whose session is open does not.

- **Late refusal** (lead and architect): after `delivery_uncertain`, an ack that arrives late with a refusal corrects the result to `session_gone` with the host's reason, in the ledger, in the `report` frame to the sender (`MessageInfo.Result` and `ResultReason`), and in the sender's `sends` record. `FinishSend` allows `delivery_uncertain` to `host_stored` or `session_gone`. Gateway side (slice 3b-ii): a late ack applies the same two corrections and nothing else.

## Slice 4: the runtime link (`internal/link`), split in two

`internal/link` imports `protocol`, `router` (for `ErrNotSent` and the `Link` interface) and `gateway` (for the address rule, `ParseAddress`, and the fingerprint helpers, so a saved gateway address is checked by the same rule as `--listen`). It writes nothing to a pane and holds no hook data or MCP token.

**4a: pairing and the request client.** `Pairing{Gateway, Host, Credential}` saved atomically as `<state dir>/pairing.json` (0600, private directory, symlink and non-regular refused, a wider mode refused on load, gateway address re-validated on load). `Pair(gateway, code)` posts the code with no host (the code names its host). `Client` has one dedicated transport pair (`Proxy: nil`, bounded dial and header timeouts, redirects refused) and the `router.Link` methods.

`Send` mapping, stated explicitly (lead's point): a failure before the request was written (dial or connect error, nothing on the wire) is `ErrNotSent`; a 3xx, 400, 401, 403 (a `version_mismatch` included) means the gateway refused before storing, and is `ErrNotSent` wrapped with the gateway's reason, which the router shows as the detail of `gateway_unreachable`; a 404 on a send route is the same refusal. Everything after the request was written is uncertain and never a definite result: a timeout or reset, a 5xx, a 200 whose body does not parse or carries a result the runtime does not know. Only a 200 with a valid `SendResponse` gives a result. `Ack` and `Report` retry at most 3 attempts in all (200 ms and 400 ms waits, `UNTESTED guess`) on a network error or 5xx, never on a 4xx, and never resend a message. Whether the request was written is detected with `httptrace.WroteRequest`.

**4b: the stream.** `Run(ctx, Receiver)` keeps one `GET /v1/stream` with its own transport (no response or request timeout, a cancellable context), reads newline-delimited frames with a 1 MiB line bound, runs a 15 s ping-silence watchdog that closes the connection, and redials with backoff 1 s doubling to 15 s. While connected it posts `/v1/heartbeat` every 5 s. State changes reach the router as `ConnectionView` (`connecting`, `connected` after the first frame, `reconnecting`, `refused` for 401/403/version mismatch with the gateway's reason in `Err`, still retried); `LinkUp` after each successful (re)connect; directory frames update `Peers`. `Receiver` is the router's side (`Receive`, `LinkState`, `LinkDirectory`, `LinkUp`, `LinkReport`); every method only enqueues. `Close` stops `Run` and sends `DELETE /v1/sessions` within 2 s.

Router connected path (slice 5), fixed by lead: after resolving a bare name to `name@thishost`, sending to the agent's own address is refused as `unknown_recipient`, as local mode refuses it today (`send.go`); only the exact self address is refused, so claude to codex on one host goes through the gateway.

## Amendments after slice 5c

- **"The gateway has no record" stays uncertain** (lead and contract: an uncertain case is never reported as a definite failure). A lookup that answers `ErrUnknownMessage` after a reconnect leaves a `gateway_uncertain` send as it is: the certainty would rest on timing. `gateway_uncertain` to `gateway_unreachable` remains a legal edge in the store graph but the router does not use it.
- **A new session is never given an older message.** The runtime keeps, for each agent, the session id the gateway returned when the run registered (`Router.SetGatewaySession`, called by app right after `Register` and before the link starts). `receive()` compares `Offer.ToSession` with it and refuses a mismatch, an empty `ToSession`, or an unset session with reason `session_ended` (the gateway maps any refusal to `session_gone`, with the reason, at the sender). Nothing is inserted, queued or reported for a refused offer. This closes the race where the gateway looks up a session, the runtime restarts and registers, and the offer lands on the new stream.
- **Test note for the user's test instructions:** the connected router tests touch actor-owned fields (`r.sends`, `r.conn`, `r.used`, `r.byName`) from the test goroutine on a started router. When testing is allowed, run the router and link tests with `go test -race` and fix any flagged read by going through `r.submit` or `Snapshot`.

## Final: what the user runs (slice 7), as built and untested

Commands (all UNTESTED): `rtk` (unchanged local mode); `rtk gateway --listen ADDR`; `rtk gateway pair HOST | unpair HOST | hosts | log [--follow] [--limit N] | show ID`; `rtk runtime pair --gateway ADDR --code CODE`; `rtk runtime [--agents claude,codex]`; `rtk --version`, `rtk --help` (also `rtk gateway --help`, `rtk runtime --help`).

State, under the state directory (`$XDG_STATE_HOME/ratatoskr`, else `~/.local/state/ratatoskr`): `ratatoskr.db` and `ratatoskr.lock` (the per-machine runtime store and lock, so plain `rtk` and `rtk runtime` exclude each other); `pairing.json` (0600: gateway address, host, credential); `session/` (hook files, removed on exit); `gateway/gateway.db` and `gateway/gateway.lock` (0700 directory, 0600 files).

Startup order of `rtk runtime`: validate `--agents`; load the pairing; open the per-machine store (lock); register the agents with the gateway; only then the router, the local MCP/hook server, the harnesses; the link starts last. Any failure before the harnesses exits with "No agent was started."

Test note: run the router and link tests with `go test -race` when testing is allowed; several connected router tests touch actor-owned fields from the test goroutine.

## Direct LAN (backend design, UNTESTED)

Source: `cross-machine-contract.md`, "Direct LAN". This section is how the code is shaped; it adds no behaviour the contract does not state.

**Transport is decided in one place per side, from what the user typed, never from the network.**

- `gateway.ParseAddress(value) (netip.AddrPort, AddrClass, error)` is the shared address rule (IP literal, explicit non-zero port, no zone, no 4-in-6, not unspecified) plus the class: `ClassLoopback`, `ClassTailscale`, `ClassLAN`. The Tailscale ranges are tested before `IsPrivate`, because `fd7a:115c:a1e0::/48` sits inside `fc00::/7`. Public and link-local are refused here, with any flags. The old strict `ParseListen` was removed after review: with no callers it could not make any path fail closed.
- `gateway.PlanListen(value, allowLAN, insecureHTTP) (ListenPlan, error)` applies the gateway flag table. `ListenPlan{Addr, Class, Transport}` with `Transport` one of `TransportPlain` (tailnet or loopback), `TransportHTTPS` (LAN with `--allow-lan`), `TransportLANHTTP` (LAN with both flags). Every refusal names the flag or address to use instead. It runs first in `gateway.Run`, before any file, lock or socket.
- `link.Target{Gateway, Fingerprint, InsecureHTTP}` is what `runtime pair` and a saved pairing both carry. `link.planTransport(Target) (gateway.Transport, error)` applies the runtime flag table. `validatePairing` calls it, so a saved pairing is checked by the same table (a LAN pairing with neither or both is refused, and a tailnet or loopback pairing with either is refused). `Pairing` gains `Fingerprint string json:"fingerprint,omitempty"` and `InsecureHTTP bool json:"insecure_http,omitempty"`; an old `pairing.json` has neither and still loads. `DisallowUnknownFields` stays.
- The scheme is a method of the pairing: `https` iff `Fingerprint != ""`, else `http`. It is read in exactly two places, `NewClient` (the base URL) and the pair request. There is no second attempt with the other scheme.

**Pinned client TLS (`internal/link/tls.go`).** `pinnedTLSConfig(fingerprint) (*tls.Config, error)`:

- decodes `sha256:<64 lowercase hex>` into `[32]byte`; an empty or malformed fingerprint returns an error, and the config is never built without one (fail closed);
- `MinVersion: tls.VersionTLS13`, `NextProtos: []string{"http/1.1"}`, `InsecureSkipVerify: true` with a comment that it only turns off the system-roots check this design replaces, and `VerifyConnection` in the same literal: it requires at least one peer certificate, hashes `PeerCertificates[0].Raw` and compares with `subtle.ConstantTimeCompare`; mismatch returns the package sentinel `errPinMismatch`. `VerifyConnection` also fails if the pinned value is not 32 bytes, so a zero value cannot match;
- no environment variable, flag or option turns it off. `Options.Transport` (tests) is the only way to bypass the built transport and production never sets it.

Both built transports (`newTransport`, `streamTransport`) get `TLSClientConfig` when the pairing has a fingerprint, plus `TLSHandshakeTimeout = DialTimeout` (UNTESTED guess) and a non-nil empty `TLSNextProto` map so HTTP/2 can never be enabled. `Proxy: nil`, `DisableKeepAlives` (requests) and keep-alives (stream), no `ResponseHeaderTimeout`, and `noRedirect` are unchanged. The pair request builds its transport from the same `Target`, so `POST /v1/pair` is pinned like every other call. The code and the credential are request bytes; the handshake (and so the pin check) completes before the transport writes any request byte.

**Classification.** The transport dials and handshakes before writing, so every handshake failure (including `errPinMismatch`) leaves `WroteRequest` unfired: `wrote=false`, `router.ErrNotSent`, `gateway_unreachable`. `causeKind` gains kinds, all without URL or certificate text: pin mismatch (long form: certificate regenerated or something else answered; re-pair after checking the fingerprint on the gateway machine), `tls.RecordHeaderError` ("the gateway did not answer with TLS"), `tls.AlertError`/other handshake failure ("the TLS handshake failed"). `runtime pair` and runtime startup already surface the `causeKind` text as the failure and exit; a running runtime keeps its reconnect loop with the mismatch as the visible reason.

**Gateway TLS (`internal/gateway/tls.go`).**

- `LoadOrCreateIdentity(dir string) (tls.Certificate, fingerprint string, error)`: `dir` is `gateway/tls/`, created `0700` with the symlink and mode rules `prepareOwnedDir` already applies. Both files present: read and parse them, refuse a symlink, a non-regular file, a key readable by group or other, or a parse error. Exactly one present: refuse, naming which and saying that nothing was changed. Neither present: generate ECDSA P-256, a random 128-bit serial, `CN=ratatoskr-gateway`, validity 10 years from now minus one hour, `KeyUsageDigitalSignature`, `ExtKeyUsageServerAuth`, self-signed; write the key then the certificate, each through a `0600` temp file in the same directory, synced, renamed. The key never reaches an error text, a log or the database: errors name the path only.
- `Fingerprint(der []byte) string` and `ParseFingerprint(string) ([32]byte, error)` are exported for the runtime side. The printed fingerprint is `sha256:` plus lowercase hex of SHA-256 of the leaf DER.
- `tlsConfig(cert)`: `MinVersion TLS13`, `NextProtos {"http/1.1"}`, the certificate, no client auth.
- **No custom handshake listener.** `net/http`'s `tlsHandshakeTimeout()` takes the minimum positive of `ReadHeaderTimeout`, `ReadTimeout` and `WriteTimeout` (checked in the installed Go source, `net/http/server.go`; DevOps corrected my earlier belief that only the last two count). `ReadHeaderTimeout` is already 5 s and `ReadTimeout`/`WriteTimeout` stay 0 for the stream, so a stalled handshake is bounded and the deadline is cleared once it completes. The listener is wrapped with plain `tls.NewListener(listener, cfg)`. `Shutdown` is the existing `http.Server.Shutdown`; it closes the wrapped listener.
- `Server.http` gets a non-nil empty `TLSNextProto` in every mode, so HTTP/2 is never configured.
- `endpoint.json` (`gateway/endpoint.json`, `0600`, atomic write like the pairing file): `{"listen","scheme","fingerprint","lan"}` written after a successful bind and before the status lines. `gateway pair HOST` reads it with `LoadEndpoint(dir)`, re-validates every field with the same address and fingerprint rules, and prints the ready `rtk runtime pair` line: `--fingerprint FP` when scheme is `https`, `--insecure-http` when scheme is `http` and `lan` is true, no extra flag otherwise. Missing, unreadable, symlinked or invalid: today's `ADDR` placeholder line (the file is advice, never a requirement).

**`gateway.Run` order** (validate before touching state): `PlanListen`; open the store under the lock; `PrepareDir`; for HTTPS `LoadOrCreateIdentity`; `Listen`; wrap with the handshake listener; `NewServer`/`Serve`; write `endpoint.json`; print the listening line and the transport line. `Listen`'s bind error gives the Tailscale advice only for a Tailscale address.

**Printed text** (verbatim in the return to lead): the warning constant `gateway.PlainHTTPWarning` is shared by `rtk gateway` and `rtk runtime pair`, so the two cannot drift.

**TUI seam.** `protocol.ConnectionView.LANPlainHTTP bool`, true only for a LAN pairing with `insecure_http`. Set in `stateEmitter.emit` from the client's pairing. False for tailnet, loopback and HTTPS, and absent in local mode (the view is nil).

**Not done, on purpose:** no fingerprint rotation or `regenerate` command (delete `gateway/tls/` and re-pair), no session resumption cache, no client certificates, no mDNS, no firewall configuration.

**DevOps consultation outcomes (static, 2026-11-06).** Pin check runs on every connection, resumed ones included (`VerifyConnection` is called on resumption, and there is no session cache anyway). Classification rests on actual request-write progress (`WroteRequest`), not on keep-alive being off. Scheme comes only from the saved pairing; a gateway restarted in the other mode cannot upgrade or downgrade an existing pairing, and an HTTP-paired runtime reaching a TLS gateway sends its plaintext credential bytes before any error comes back (no output may claim the TLS port prevents that); re-pair is the only change. Printed advice stays conditional: no promise of a macOS Local Network prompt for terminal-launched CLIs (Apple TN3179 auto-allows them), and the firewall line reads "if a runtime cannot reach this address, check that this machine's firewall allows incoming TCP on the port", naming no tool. A changed LAN address means re-pairing with the same gateway and the same fingerprint, never regenerating the certificate; the README should suggest a router DHCP reservation.
