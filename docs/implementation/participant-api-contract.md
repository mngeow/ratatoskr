# Participant API: implementation contract (first delivery)

Owner: `design.architect`. Branch: `feat/adapter-integration` (from `main` at `5cdd6ec`). Authority: the user's approval of the seven recommendations of [ADR-0007](../adr/0007-adapter-integration-contract/index.html), relayed 2026-11-08 by the operator (including, at 10:51Z, HTTPS on tailnet and LAN for the participant API), with the request to implement on a new branch and to commit and push that branch when done (normal push, no merge). Standing restriction GR-3: no tests are run, no application is started, no probes. `gofmt`, `go vet` and `go build` (never leaving a binary in the tree) are allowed; tests are written, not executed.

This is the contract backend builds against and the lead reviews against. It fixes decisions that must not be re-argued, and leaves low-level choices to the engineers, who record them in section 14. It was written from a static read of the gateway (`internal/gateway/{sessions,send,route,server,listen,pairing}.go`, `internal/protocol/{wire,connected,network}.go`, `internal/router/connected.go`). **Nothing here has been run.** Every limit marked `UNTESTED guess` is a starting value.

## 1. What is being built, and the hard rules

A separate, small HTTP+JSON **participant API** on the gateway that lets an external adapter, written in any language, enrol as a **participant**, register named **sessions**, send messages to any registered session, and receive messages through a **long-poll inbox**. It is the W2 option the user chose.

Hard rules (decided; do not change without the user):

1. **The host protocol is untouched.** `WireVersion` stays 2. No new frame, field, route or result value is added to anything under `/v1/`, and no daemon, runtime, router, MCP or hook code changes for this feature, except what section 12 lists as unavoidable (nothing is expected). The participant API is a different surface with its own routes, credentials, errors and version.
2. **A participant credential never works on a host route and a host credential never works on a participant route.** Separate tables, separate middleware, separate path prefix.
3. **Identity comes from the credential.** The participant is the one its credential names; a `from` that is not a current session of that participant is refused; a body field never sets an origin.
4. **Origin is written by the gateway.** The receiver of any message sees `name@participant` or `name@host` as the gateway established it.
5. **Offline fails at once; nothing is queued.** No persistent inbox, no replay. See section 6 for the one bounded in-memory hand-over.
6. **Honest results.** The existing send result vocabulary is reused unchanged; an uncertain case is never reported as a failure; stored is not delivered, delivered is not read.
7. **Any authenticated participant may contact any registered session** (host sessions and participant sessions), and any host session may contact a participant session. There are no per-session grants in v1. This is the user's decision 7 and is not inherited from any A2A answer.
8. **No public listener.** LAN and tailnet (and loopback) only, owner-configured, with the same address-class rules `PlanListen` enforces. The participant API has its **own listener**, off unless the owner asks for it (section 2).
9. **No A2A.** Nothing from the deferred A2A branch is imported or reused. `feat/a2a-participants` is not merged.
10. **No claim that any external harness works.** Docs describe the contract, how to integrate and what is untested.
11. Do not touch host cards, `card.yaml`, the Network and Graph tabs' behaviour, card polling, SSH entry or the open prompt-box issue. No unrelated refactor.
12. **No live-state mutation.** The migration is additive, applies only at `rtk gateway --listen` with the existing `VACUUM INTO` backup, and has never been run. Nothing here is run against a real database.

## 2. Decision 3 (TLS): HTTPS by default, on its own listener

The user's decision 3 reads "HTTPS by default; plain HTTP only as an explicit opt-in, consistent with existing Ratatoskr LAN behavior". The architect's first reading was option A (serve the participant API on the existing gateway listener and inherit its table, which is plain HTTP on a tailnet). **That ruling is withdrawn.** The operator relayed the user's instruction at 10:51Z the same day: *"use HTTPS on tailnet and LAN; do not inherit the existing tailnet HTTP listener behavior for participant API traffic. Keep the separate host daemon protocol unchanged."* This section follows that instruction. No code had been written under the withdrawn ruling.

Because the existing listener has one transport per address (the host protocol stays plain on a tailnet and must stay unchanged), the participant API cannot share it and be HTTPS. It therefore has **its own listener**, started by the same `rtk gateway --listen` process:

| Flag | Meaning |
| --- | --- |
| `--participant-listen ADDR` | Serve the participant API on ADDR (IP literal with an explicit port, the same parsing as `--listen`; hostnames, port 0, the unspecified address and public addresses are refused). **Without this flag the participant API is not served at all.** The host listener never serves `/participant/*`: it refuses it, with `400 version_mismatch` when the request lacks the host protocol header and `404 not_found` when it has it, because the untouched host `Handler` runs `withVersion` before routing |
| `--participant-allow-lan` | Required for a LAN address (10.x, 172.16-31.x, 192.168.x, fc00::/7), as `--allow-lan` is for the host listener. Refused for any other class |
| `--participant-insecure-http` | The explicit opt-in for plain HTTP. Allowed on a tailnet or LAN address; refused on loopback (where plain HTTP needs no opt-in) |

Transport by address class, planned by a new `PlanParticipantListen` that reuses `parseAddressAs` and the class logic of `planListenAs` (subject "participant listener"):

| Participant address | Default | With `--participant-insecure-http` |
| --- | --- | --- |
| Tailnet | **HTTPS**, pinned certificate | Plain HTTP, with a startup warning that the credential is readable on the path |
| LAN (with `--participant-allow-lan`) | **HTTPS**, pinned certificate | Plain HTTP, same warning |
| Loopback | Plain HTTP (the traffic never leaves the machine); the flags are refused | n/a |
| Anything else | Refused with the reason | Refused |

*Loopback is the architect's reading,* since the instruction names the tailnet and the LAN only; it changes nothing for those two. If the user wants HTTPS on loopback as well, it is a one-line change to the table.

**Certificate.** The participant listener reuses the gateway's existing identity (`LoadOrCreateIdentity(<gateway dir>/tls)`): one self-signed certificate, one fingerprint, the same file the host listener's LAN mode pins. No new key, no CA, no ACME, no hostname checks, no rotation command. The listener is built by the same identity-before-bind rule as `run.go` (a refused identity leaves no bound address) and uses `serverTLSConfig` (no HTTP/2). It is a **separate `http.Server`** with the same timeout settings as `NewServer`, sharing the one `Store` and the one hub, and it is shut down with the gateway.

**What an any-language adapter must do.** The certificate carries no hostname or address, so a normal "trust the system store and check the name" client will refuse it. An adapter must **pin the certificate's SHA-256 fingerprint** (and may skip name checking, since the pin is the identity). `participant pair` prints **two values, which are not equivalent** (section 11). (1) The **certificate SHA-256** (over the DER encoding) is **normative**: it is what the host pairing already pins, and an adapter that can pin a certificate must use it. (2) The **SPKI pin**, `sha256//BASE64(SHA256(SubjectPublicKeyInfo))`, is a separate, clearly labelled **key-pin rule for curl-like clients**: `curl --pinnedpubkey` accepts only that form (or a public key) and can never check a certificate hash. Pinning the key proves the same key holder, not the same certificate file; the docs say so and do not call the two equal.

**Pinning recipes (in the spec README, marked untested) must never show** `curl -k` or `--insecure`, an unpinned preflight request, or a certificate hash given to `--pinnedpubkey`. **There is no curl recipe in v1** (ruling below). The Python recipe: certificate verification disabled **only on a dedicated pinning socket**; complete the TLS handshake; read `getpeercert(binary_form=True)`; compare its SHA-256 to the pinned value with a constant-time comparison (`hmac.compare_digest`); then send the request **on the same socket**; repeat the check on **every new connection**; no proxy, no redirect following, no fallback to an unpinned or plain connection if the check fails. `docs/spec/participant-api/README.md` must give a recipe for this for at least `curl` and Python, marked untested, because it is the main cost the language-neutral requirement pays for self-signed pinning. Whether the owner may later supply their own certificate instead is not decided and not built.

**Plain HTTP is never silent.** The gateway prints, at start, one line per listener stating its transport and, for plain HTTP, that the bearer credential and message bodies are readable by anything on the path. The docs say the same.

**Curl ruling (architect, 2026-11-08, after backend's P0).** The gateway certificate has CN `ratatoskr-gateway`, no SAN and no CA flag, and curl checks the chain and the name *in addition to* `--pinnedpubkey`, so a stock curl will most likely refuse it before the pin matters. **Option (a) is ruled:** the spec states that stock curl has no pinned-only recipe in v1 and shows none; Python is the worked example. Option (b), adding a SAN or CA flag, is **rejected**: it changes the certificate and therefore its fingerprint, which would break every pin already held by paired hosts. Option (c) (`--cacert` with `--resolve`) is unverified and not offered. Option (d), `-k` together with `--pinnedpubkey`, may be a genuinely pinned connection if libcurl checks the key pin with peer verification off, but that is unverified and it shows `-k`, which this contract forbids; DevOps may check the *documented* behaviour (docs only, no run) and, if it is confirmed, the architect revisits this as a labelled exception. The SPKI pin is still printed, for clients that can pin a key (a Go or Node client with a custom verifier), and the README says curl is not one of them.

### Lifecycle of the two servers (required)

Both servers belong to one `rtk gateway --listen` process. Required behaviour, from the DevOps review of the listener:

1. **One shutdown signal.** Shutdown first **cancels every parked poll and withdraws every pending offer** through a shared signal, *then* drains. `http.Server.Shutdown` does not cancel a handler parked for up to 20 s, so without this the 5 s `shutdownGrace` would always expire. A cancelled poll answers at once with an empty offer list; a withdrawn pending offer ends its send `destination_unreachable` as in section 6.
2. **One grace deadline** (the existing `shutdownGrace`, 5 s) shared by both servers, not 5 s each.
3. **One hub, one shutdown** of it.
4. **Either `Serve` failing tears down the other** and returns the error; neither server is left running alone.
5. **Partial-start rollback.** If the second listener cannot be bound or its identity refused, every listener already bound is closed and nothing is left bound (the same identity-before-bind rule as `run.go`).
6. **One `Store`:** one open, one lock, one migration, shared by both servers. The participant listener never opens the database itself.
7. **Endpoint metadata:** participant endpoint metadata (the address and scheme the owner commands print) is written to its **own file** and never overwrites the host endpoint file, which stays byte-for-byte as today.
8. **API off means unchanged, with one narrow exception.** Without `--participant-listen`, the startup path, its output and its files are exactly today's, **except** that a stale `participant-endpoint.json` left by an earlier run with the API on is removed, so that no file claims a listener that is not running. The removal happens under the gateway lock, only when the path is a regular file (anything else is left alone and reported), and never touches the host endpoint file.

## 3. Identity and addressing

**Participant id.** A stable name, pattern `^[a-z0-9-]{1,63}$` (the host-name pattern, `hostNamePattern`). It is chosen by the owner when the pairing code is minted.

**One namespace with hosts.** A participant id and a host name must never be equal. Enforcement, all three required:

1. `rtk gateway participant pair ID` refuses an id that is a paired host, **and an id that appears as `sessions.host` or as the host part of any `from_address` or `to_address` in the ledger without being a participant id**. A host that was removed with `rtk gateway unpair` (which deletes its `hosts` row) therefore stays reserved: its ended sessions and ledger history still carry the name, and a participant under it would make `EverSeen` answer `session_gone` instead of `unknown_recipient` and mix two kinds of history. The refusal names the reason and asks for a different id. There is no override in v1. Re-pairing an existing participant id (rotation, or a revoked one) is not affected.
2. `rtk gateway pair HOST` (existing) refuses a name that is **any** participant id, **revoked ones included**. A revoked id stays reserved, so a released name can never put two kinds of endpoint under one `sessions.host` value (architect ruling, confirming backend's row 14). This is the only change to host-side code and it is in the admin path, not the protocol.
3. Redemption re-checks inside the same transaction that writes the row, so a race between the two commands cannot create both.

**Session name.** `^[a-z0-9_-]{1,32}$` (`agentNamePattern`), the same as hosts.

**Address.** `name@participant`, e.g. `coder@pi-lab`. This is the shared `name@host` namespace the ADR recommended and the user approved. Existing code parses it unchanged (`parseAddress`), and a managed session sends to it with the existing `ratatoskr_send` (section 12).

**Session id.** Opaque, 128-bit hex from `crypto/rand` (`randomID`), new on every registration. Registering a name again ends the earlier open session of that name; other sessions are untouched.

**Storage.** Participant sessions live in the **existing `sessions` table**, with `sessions.host` holding the participant id. This works because ids and host names cannot collide, and it keeps `CurrentSession`, `EverSeen`, the ledger's `from_session` / `to_session` and the replacement rules identical. The table gets no new column.

**Two directories.** The existing `Store.Directory` (inner join on `hosts`) stays as it is and keeps feeding the Network snapshot and the Graph. A new `Store.WireDirectory` returns host sessions plus the open sessions of non-revoked participants, and is what `directoryEntries` uses (connect, add-session, directory frames, the `unknown_recipient` list) and what the participant directory route returns. A `DirectoryEntry` for a participant session is `{address: "coder@pi-lab", host: "pi-lab", live: ...}`. **Participants do not appear in `/v1/network` or `/v1/graph` in v1.** Backend must read both builders and confirm by static reading that a ledger row addressed to a participant id cannot break them (an unknown "host" in an edge); if it can, those readers skip ids that are not hosts. Record the finding in section 14.

**Rotation and revocation.** `participant pair ID` on an existing, non-revoked id is a **rotation**: a new code replaces the credential when redeemed and ends all the participant's sessions (the same behaviour as re-pairing a host). `participant revoke ID` sets `revoked_at`, ends every session at once, refuses the credential on the very next request (authentication reads the database on every request and caches nothing), closes a parked poll with 401, and withdraws unclaimed offers. A revoked id is kept (history, reserved name). A revoked id may be paired again, which clears `revoked_at`.

## 4. Surface

All routes are under **`/participant/v1`**. All bodies are JSON (`Content-Type: application/json`), UTF-8. `Authorization: Bearer <credential>` on everything except pair. Times are RFC 3339 UTC in JSON.

**Mounting.** The participant routes are served by a new `ParticipantHandler()` on the participant listener (section 2), **not** inside `Handler()`. It is wrapped in `rewriteBareErrors` only and **never** in `withVersion`: a third-party adapter must not be forced to send the host protocol header `X-Ratatoskr-Protocol: 2`. Participant routes use their own `withParticipantAuth`. As today, no chi middleware is used (a wrapper must not hide `http.Flusher` from the long poll). The host `Handler()` gains no participant route and its behaviour is unchanged.

| Method and path | Auth | Request | Success response |
| --- | --- | --- | --- |
| `POST /participant/v1/pair` | none (the code is the secret) | `{code, participant}` | `200 {participant, credential}` |
| `GET /participant/v1` | none | none | `200 {contract, versions, now}` (discovery; no secrets) |
| `POST /participant/v1/sessions` | participant | `{name}` | `200 {participant, session:{name,address,session_id}, directory:[...]}` |
| `DELETE /participant/v1/sessions/{name}` | participant | none | `204` (ending one that is not open is not an error) |
| `DELETE /participant/v1/sessions` | participant | none | `204` (ends all) |
| `GET /participant/v1/directory` | participant | none | `200 {directory:[DirectoryEntry]}` |
| `POST /participant/v1/messages` | participant | `{id, from, to, reply_to?, body}` | `200 SendResponse` (below) |
| `GET /participant/v1/inbox?wait=N` | participant | query `wait` seconds | `200 {offers:[Offer], replaced}` (section 6) |
| `POST /participant/v1/messages/{id}/ack` | participant | `{saved, reason?}` | `200 {}` |
| `POST /participant/v1/messages/{id}/status` | participant | `{state, reason?, at?}` | `200 {}` (idempotent) |
| `GET /participant/v1/messages/{id}` | participant | none | `200 MessageInfo` for a message the caller sent or received |

**Field shapes reuse the host protocol's Go types and JSON names** (`protocol.SendRequest`, `SendResponse`, `Offer`, `DirectoryEntry`, `AckRequest`, `StatusReport`, `MessageInfo`, `SessionInfo`) so there is one definition of each. The participant-only types (`ParticipantPairRequest`, `ParticipantPairResponse`, `ParticipantAddSession*`, `InboxResponse`, `ParticipantDiscovery`) are new, in `internal/protocol/participant.go`, and they are the only new wire types. The JSON Schemas (section 10) are derived from these and are the normative description.

**`send`.** `from` must be the name of a current open session of the caller. `to` is `name@host` or `name@participant`; **a bare name is refused** (`bad_request`, `to`) because a participant has no "this host". `id` is a client-chosen message id, pattern as today (`messageIDPatternMatcher`); the same id from the same sender applied twice stores and offers once, exactly as the host route does. The response is `200` with a `SendResponse` for every outcome the gateway decided, **never a 4xx for a message it stored**.

**`ack`.** Allowed only by the participant that owns the message's `to_session`. `saved:true` records the success result (section 5); `saved:false` records `session_gone` with the reason. A second ack of the same message is idempotent.

**`status`.** Allowed only by the participant that owns the message's `to_session`. `state` is one of the existing `MessageState` values (`held`, `delivering`, `pasted`, `accepted`, `unconfirmed`, `failed`, `interrupted`; `queued` is refused). It is stored as a **report** with the existing rank logic (`reportRank`), so an out-of-order report cannot undo a later one. It is shown as a report and never changes a gate.

**`GET message`.** The caller may read a message only if it is the sender or the recipient (by participant id or by session ownership). Anything else is `404 not_found` identical to an unknown id.

## 5. Send pipeline and status mapping

`handleSend` is split: the body validation and the sequence `InsertMessage` → reply check → `EverSeen` → liveness → `CurrentSession` → `SetToSession` → offer → wait for ack → `recordTimeout` becomes one shared routine parameterised by **sender endpoint** (host or participant) and by a **recipient delivery** (host stream or participant inbox). The only differences are who the sender is (credential, `from` session lookup, `fromAddress`) and how the offer reaches the recipient. A recipient is a participant if its `@` part is a non-revoked participant id (a lookup in `participants`).

The vocabulary is **unchanged**. No new `SendResult` value, no change to `protocol.SendResult`. The ADR's draft words map as follows:

| ADR-0007 term | Wire value | Notes |
| --- | --- | --- |
| `participant_stored` | `host_stored` | The adapter acked `saved:true`. The `detail` says "the participant's adapter saved it". Tool success. Means the adapter has it, nothing more |
| `participant_refused` | `session_gone` | Ack with `saved:false`; the reason goes in `detail` (the existing rule for a refusing host) |
| `participant_delivered` (optional) | a report, state `accepted` | The adapter's own assertion that it handed the message to its harness. Not read, not acted on |
| adapter offline | `destination_unreachable` | No live poll (section 6). Never offered |
| adapter claimed, no ack in `AckWait` | `delivery_uncertain` | May have been saved. A late ack or report corrects it |
| unknown name | `unknown_recipient` | With the directory, as today |
| name registered earlier, no current session | `session_gone` | As today |
| gateway unreachable, gateway uncertain | `gateway_unreachable`, `gateway_uncertain` | Client-side, as today. An adapter must apply the same rule: **a send whose answer did not arrive is uncertain, never failed** |

Constants are the existing ones: `AckWait` 5 s and `SendWait` 8 s (`internal/protocol/connected.go`). **An adapter's HTTP timeout for `send` must exceed `SendWait`**; the docs recommend 10 s. The compile-time ordering check between the two stays as it is.

Reports for a message **sent by a participant** are not pushed (it has no stream); the sender reads them with `GET /participant/v1/messages/{id}`. Reports for a message **sent by a host session to a participant** go to the sending host's stream as today.

## 6. The inbox: long poll, online, offline

**One inbox per participant**, not per session. A poll returns offers for any of its sessions; each `Offer` carries `to` (the session name) and `to_session` (the id the gateway meant). This keeps an adapter with several sessions on one connection.

`GET /participant/v1/inbox?wait=N`: `wait` is whole seconds, `0..20`, default `20` (the `MaxNetworkWait` precedent). The gateway holds the request until an offer is available or `wait` elapses, then answers `200 {"offers":[...], "replaced":false}`; an empty `offers` array is the normal timeout answer, and the adapter asks again at once. At most **8** offers per answer (`UNTESTED guess`).

**One open poll per participant.** A second poll replaces the first: the first is answered at once with `{"offers":[],"replaced":true}`. This prevents split delivery and mirrors how a new stream replaces an old one.

**Online means a poll is parked now, or the last poll began or ended within 15 seconds.** Implement it with the existing `HostLive(lastSeen, open, now)` (the same 15 s window): `lastSeen` is a new `participants.last_poll_at` written at poll start and end, `open` is whether a poll is parked in memory. A participant that has registered sessions but is not online is shown `live:false` in the directory, and sends to it fail at once with `destination_unreachable` ("the participant is not polling"). No session is ended by silence in v1 (a host's sessions are not either); the adapter's next registration of the same name replaces it.

**The bounded hand-over (this is not queueing).** When a send finds the participant online but no poll currently parked (the instant between two polls), the gateway holds the offer **in memory, only for as long as the sender's request waits (at most `AckWait`, 5 s)**. The next poll claims it. Offer states: `pending` (in memory, unclaimed), `claimed` (written into a poll answer). Rules:

1. A `pending` offer that is still unclaimed when the sender's wait ends is **atomically withdrawn** and the send ends `destination_unreachable` ("the adapter did not poll within 5 s"). A poll that arrives after the withdrawal cannot receive it. Nothing is stored for later; a restart loses `pending` offers, which is correct because their senders have already been answered.
2. When a poll claims an offer, the sender's `AckWait` timer is already running from when the offer was created; the ack must arrive within it. An ack after the sender was answered `delivery_uncertain` is still recorded and corrects the ledger.
3. If the poll answer cannot be written (connection lost mid-write), the offer is `claimed` and the outcome is unknowable: it ends `delivery_uncertain`. It is never re-offered.
4. Nothing is replayed to a participant that comes back online. A failure record is never delivered later.

The hub gets a participant sibling of `expectAck`/`enqueue` (`internal/gateway/participanthub.go`), reusing `expectAck` for the ack wake, so the same `recordTimeout` logic applies.

## 7. Limits (all `UNTESTED guess` except where noted)

| Limit | Value |
| --- | --- |
| Open sessions per participant | 8 (the host value) |
| Message body | 64 KiB (`maxMessageBody`, existing) |
| Any request body | 512 KiB (`defaultMaxBodyBytes`, existing) |
| Offers per poll answer | 8 |
| Poll `wait` | 0 to 20 s, default 20 |
| Online window after a poll | 15 s (existing `HostLive`) |
| Ack wait / send wait | 5 s / 8 s (existing constants) |
| Pairing code | single use, 10 min (`codeTTL`), stored as a hash only |
| Credential | 32 random bytes, base64url, stored as a hash only |
| Rate limiting | none in v1; stated in the docs as a known gap |
| Participants | no cap in v1 (only the owner creates them) |

Exceeding a size limit is `413`/`bad_request` as the host routes do (`decodeBody` already enforces `MaxBodyBytes`).

## 8. Errors

The error body is the host protocol's `WireError`, `{ "code": "...", "message": "..." }`, so there is one error shape. HTTP statuses and codes:

| Status | `code` | When |
| --- | --- | --- |
| 400 | `bad_request` | A field fails validation; the message names the field only |
| 400 | `unsupported_contract` | The caller declared a contract major the gateway does not serve (section 9) |
| 401 | `unauthorized` | No credential, an unknown one, or a revoked participant. **Never says which** |
| 400 | `bad_pairing_code` | Unknown, used or expired code |
| 404 | `not_found` | Unknown route, or a message the caller may not read |
| 409 | `too_many_sessions` | The participant already has 8 open sessions after replacement |
| 413 | `bad_request` | Body over the limit |
| 500 | `internal` | Never reveals the cause |

Error and log text never contains a code, a credential or a hash (the existing rule for `pairing.go`).

## 9. Versioning

The **major** version is the path prefix, `/participant/v1`. The **minor** is a response header on every participant response: `X-Ratatoskr-Contract: 1.0`. A client may send the same header to say what it speaks; if it declares a major other than `1` the gateway answers `400 unsupported_contract` and names the supported majors in the message. `GET /participant/v1` returns `{"contract":"1.0","versions":["1"]}` and needs no credential, so an adapter can check compatibility before pairing. Within a major, changes only add: unknown request fields are ignored by the gateway, and adapters must ignore unknown response fields. A removed or changed meaning is a new major and a new prefix. This is independent of `WireVersion` and of any adapter's own version.

## 10. Schemas and conformance examples (decision 1)

Location: **`docs/spec/participant-api/`**.

```
docs/spec/participant-api/
  README.md             normative prose: the operations, semantics, statuses, limits, errors, versioning
  schemas/*.schema.json JSON Schema (draft 2020-12), one per request and response type in section 4
  examples/*.json       conformance exchanges: {name, description, request:{method,path,headers,body}, response:{status,headers,body}}
  examples/index.json   ordered list of the examples with the flow each belongs to
  CHANGELOG.md          one line per contract change, starting at 1.0
```

The examples to write (each is one request and one response; secrets are obviously fake placeholders, never real ones): discovery; pair ok; pair bad code; register session; register ninth session (409); send to a managed session (`host_stored`); send to an unknown name (`unknown_recipient` with directory); send to an offline participant (`destination_unreachable`); send with a bare `to` (400); poll with an offer; poll timeout (empty); poll replaced; ack saved; ack refused; status accepted; status out of order; read message; revoked credential (401); unsupported contract (400). The spec README also carries the pinning recipes of section 2. Backend also writes (does not run) one Go test that decodes every example with the Go types and checks the field names, so the examples and the code cannot drift.

The schemas and examples are reviewed by the architect before any route is written (phase 0).

## 11. Owner commands

Mirroring host pairing (`rtk gateway pair|unpair|hosts`), under the existing `gateway` admin command:

| Command | Behaviour |
| --- | --- |
| `rtk gateway participant pair ID` | Validates ID, refuses a host name, mints a single-use code (10 min, hash only) and prints it once with the participant API URL the adapter should use (scheme from the participant listener's transport) and, whenever that transport is HTTPS, both pins, labelled as in section 2: the certificate SHA-256 (normative) and, separately, the SPKI pin `sha256//BASE64` for curl-like clients, computed from the same certificate. If the participant listener is not running it says so and prints nothing usable. Rotation if ID exists |
| `rtk gateway participant list` | ID, state (enrolled, revoked), paired time, last poll time, live or not, number of open sessions |
| `rtk gateway participant revoke ID` | Section 3 revocation. Says exactly what it ended |

These run in a separate process from the gateway (like `unpair`), so effects that are not direct database writes happen within the poll's next check, and the command says so. The code is never written to a log or to a file by the command. `rtk gateway hosts` is unchanged.

## 12. The managed-session side: what must change

**Expected: nothing in the daemon, runtime, router, MCP or hooks.** Backend verifies this by static reading and records the result in section 14. The reading so far:

1. **Sending to a participant.** `prepareSend` (`internal/router/connected.go`) composes `name@thishost` for a bare name and passes a full `name@host` through; the gateway resolves it. `ratatoskr_send("coder@pi-lab", ...)` therefore reaches the gateway unchanged, and the gateway's new branch routes it to the inbox. `unknown_recipient` answers with the directory, which now includes participants. **To verify:** that nothing client-side validates the host part against the hosts the daemon knows.
2. **Receiving from a participant.** The recipient host receives an ordinary `Offer` whose `From` is `name@participant`, set by the gateway. The frame header, the gate and the paste are unchanged. A reply by `ratatoskr_send` to that address goes back through the participant branch. **To verify:** the reply's `reply_to` check (`ReplyTarget` compares `from == request.To`) holds with a participant address on either side.
3. **Peers in prompts.** `peersFor` lists every directory address, so participants appear in the "peers at launch" text and in the directory frames pushed to streams. **Required:** any change to the participant sessions (register, end, rotate, revoke, liveness change) must call `pushDirectory()` so hosts learn of them, as host registrations do.
4. **The Network and Graph tabs** are unchanged: they read `Store.Directory` (hosts only). Participants have no tab in v1.
5. **Reports.** A participant's `status` report for a message from a host session is pushed to that host as a `report` frame, exactly like a host recipient's.

If any of these needs a change to the host protocol, **stop and ask the architect**: the rule is that the host protocol stays unchanged.

## 13. Persistence and migration

One new migration, `0003_participants.sql`, additive only (`main` has `0001` and `0002`; the unmerged A2A branch also has a `0003`, which is irrelevant here and must not be read or copied). It never edits `0001` or `0002`.

```sql
CREATE TABLE participants (
  id TEXT PRIMARY KEY,
  credential_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  last_poll_at INTEGER,
  revoked_at INTEGER
);
CREATE TABLE participant_pairing_codes (
  code_hash TEXT PRIMARY KEY,
  participant TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER
);
```

`Authenticate` for participants is a separate store method that queries `participants WHERE credential_hash=? AND revoked_at IS NULL`. The ledger's `from_address` and `to_address` are text and need no change. Migration `0003` has **never run**; the existing backup (`VACUUM INTO` before migrating, at `rtk gateway --listen` only) applies unchanged. No code path may open the real database during development.

## 14. Engineers' decisions (fill in, do not erase)

| # | Decision | By | Date |
| --- | --- | --- | --- |
| 1 | P0: wire types are in `internal/protocol/participant.go`: `ParticipantPairRequest`, `ParticipantPairResponse`, `ParticipantDiscovery`, `ParticipantAddSessionRequest`, `ParticipantAddSessionResponse`, `InboxResponse`, plus `ParticipantDirectoryResponse` (`{directory}`), which section 4 shows as a body but does not list as a type. Path, header, limit and the two participant-only error-code constants live beside them. Nothing in the host protocol changed. | backend | 2026-11-08 |
| 2 | P0: ack and status answer `{}`; no Go type, one `empty.schema.json` (`maxProperties: 0`). | backend | 2026-11-08 |
| 3 | P0: `status` reuses `protocol.StatusReport`, whose `at` has no `omitempty`. The schema makes `at` optional as the contract says; an absent `at` decodes to the zero time and P2 must store the gateway's own receive time instead (the host route's rule). | backend | 2026-11-08 |
| 4 | P0: a bad pairing code is `400 bad_pairing_code` here, as section 8 says. The host route `/v1/pair` answers 403 with the same code (`server.go`); the two stay different and the participant route follows this contract. | backend | 2026-11-08 |
| 5 | P0: the pair response carries only `{participant, credential}`. The certificate SHA-256 and the SPKI pin are printed by the owner command (section 11), not returned over HTTP, so no pin field exists in the wire types or examples. | backend | 2026-11-08 |
| 6 | P0: schemas never set `additionalProperties: false` (the gateway ignores unknown request fields and clients must ignore unknown response fields). `result` is the five values the gateway produces (`host_stored`, `session_gone`, `delivery_uncertain`, `unknown_recipient`, `destination_unreachable`); `gateway_unreachable`, `gateway_uncertain` and `sending` are client-side and are not in the response enum. `body` has `maxLength 65536` but the limit is bytes (`maxMessageBody`), so the schema is only a first check. | backend | 2026-11-08 |
| 7 | P0: 19 examples as `NN-name.json` plus `index.json` (`{contract, examples:[{file,name,flow}]}`), all secrets fake (`FAKE-...`), session ids `0000...01`. Error `message` texts are illustrative; clients switch on `code`. I wrote them myself rather than use the monkey slot: they must agree field-for-field with the Go types, and briefing that cost more than writing them. | backend | 2026-11-08 |
| 8 | P0: one Go test file, `internal/protocol/participant_spec_test.go` (written, never run): decodes every example body with `DisallowUnknownFields` and re-encodes it to the same JSON; checks the contract header, which routes carry `Authorization`, fake secrets, index and directory agree; checks each schema's properties equal the Go type's JSON names and every `$ref` resolves. | backend | 2026-11-08 |
| 9 | P0: the spec README is a skeleton (layout, outline, rules, versioning, pinning). Its curl recipe follows section 2 literally (`--pinnedpubkey` with the SPKI pin, nothing relaxed) and carries an open-problem note: curl also verifies the chain and name, the gateway certificate has no SAN and is not a CA (`tls.go`), so a stock curl will likely refuse it before the pin is consulted, and the only way past is `-k`, which the recipes must not show. Raised to the lead for the architect; not resolved here. | backend | 2026-11-08 |

| 10 | Architect, P0 review: rows 1 to 9 accepted. Row 4 (400 for a bad code, unlike the host route's 403) stands and the spec says so. Row 6 is **amended**: `result` has six values, adding `gateway_uncertain`, which `sendResponseFor` returns for a duplicate id whose result is still `stored` (`send.go`); the README adds the rule that a client treats an unknown `result` string as uncertain, never as failed. | architect | 2026-11-08 |
| 11 | Architect: curl ruling (section 2): option (a); no curl recipe in v1; (b) rejected. Row 9's open-problem note is replaced by that statement. | architect | 2026-11-08 |
| 15 | Architect: lifecycle point 8 amended as above to accept backend's row 18 (stale `participant-endpoint.json` removed when the API is off), with DevOps's conditions: gateway lock held, regular files only. | architect | 2026-11-08 |
| 14 | Architect: the host listener refusing `/participant/*` with 400 (no host header) or 404 (with it) is accepted; the host `Handler` is not changed for this (hard rule 1). The requirement is only that it never serves the participant surface; a test asserts that, not a status code. | architect | 2026-11-08 |
| 13 | Architect, naming rulings (section 3): (1) confirmed: host pair refuses any participant id, revoked included; contract rule 2 amended. (2) option (a): participant pair also refuses a name found in `sessions.host` or in ledger `from_address`/`to_address` host parts unless it is already a participant id; a former host stays reserved; no override in v1. | architect | 2026-11-08 |
| 12 | Architect: add one example, `20-send-delivery-uncertain` (offered once, no ack in time), the central honesty case; `session_gone` is already shown by `14-ack-refused`. | architect | 2026-11-08 |
| 13 | P1: layout. `migrations/0003_participants.sql` is section 13's SQL plus a header comment. All participant store methods are in `internal/gateway/participants.go` (`CreateParticipantCode`, `RedeemParticipantCode`, `AuthenticateParticipant`, `GetParticipant`, `Participants`, `TouchPoll`, `RevokeParticipant`, `AddParticipantSession`, `WireDirectory`). `sessions.go`: `AddSession`'s body moved into `addSession` with an optional in-transaction check; `AddSession` behaves as before. `pairing.go`: one check added in `CreatePairingCode` and one in `redeem`. Participant sessions reuse `CurrentSession`, `EverSeen`, `EndSession` and `EndSessions` with the participant id as the host. | backend | 2026-11-08 |
| 14 | P1: name collisions. The host side refuses ANY participant row, revoked included. Section 3 rule 2 says 'non-revoked' but section 3's last paragraph says a revoked id is a 'reserved name', and a released name would put two kinds in one `sessions.host` value; I took the stricter reading. **Architect: confirm or amend.** Mint-time refusals are `ErrNameIsHost` / `ErrNameIsParticipant` (texts name no code). At redemption the same check runs in the transaction that writes the row and fails as plain `ErrBadCode`, without consuming the code, so the redeemer learns nothing the code did not already imply. Atomicity across the two admin processes and the gateway rests on `_txlock=immediate` (every transaction takes the write lock at BEGIN). | backend | 2026-11-08 |
| 15 | P1: semantics. `AuthenticateParticipant` reads the database on every call (`revoked_at IS NULL`); `RevokeParticipant` sets `revoked_at`, ends the sessions, removes unused codes and returns the counts, and a second revoke is `ErrNoSuchParticipant`; `TouchPoll` refuses a revoked or unknown id; `AddParticipantSession` checks the participant exists and is active inside its transaction (so a registration cannot race a revocation) and uses the host limit (`maxAgentsPerHost`, 8). Re-pairing an existing id resets `created_at` (shown as the paired time), clears `last_poll_at` and `revoked_at`, and ends its sessions, as re-pairing a host does. `ErrTooManySessions`'s text mentions `rtk daemon stop`, so P2 must map it to `409 too_many_sessions` with its own message. | backend | 2026-11-08 |
| 16 | P1: `WireDirectory` is `UNION ALL` of host sessions (inner join `hosts`) and sessions of non-revoked participants, ordered by host then name; `LastSeen` is the host heartbeat or the participant's last poll. It does not compute `live`: P2 does, with the existing `HostLive` over `LastSeen` plus whether a poll is parked. `Store.Directory` is untouched. | backend | 2026-11-08 |
| 17 | P1 (architect ruling, row 13 part 2): `CreateParticipantCode` and `RedeemParticipantCode` also refuse a name with host history: a row in `sessions.host`, or the name as the host part (after the first `@`) of any ledger `from_address` or `to_address`, unless it is already a participant id (so rotation and re-pairing after a revoke are unaffected). It runs in the same transaction as the code insert and again at redemption; at mint it is `ErrNameHasHostHistory` (the message asks for a different id), at redemption plain `ErrBadCode` without consuming the code. The ledger check is a scan of `messages` (an owner command, not a hot path); no index was added. | backend | 2026-11-08 |
| 18 | P2a: layout. `internal/gateway/participant_listen.go` holds `ParticipantOptions`, `PlanParticipantListen` (its own table, not `planListenAs`; every refusal names the participant flags), `planBoth` (validates the host and the participant plan together and refuses identical bind endpoints, before any file, lock or socket), `SPKIPin`, the participant endpoint file (`participant-endpoint.json`, own validator because HTTPS is allowed on a tailnet there), `participantStatusLines` and `ParticipantHandler`. `run.go`: `Run` is now a wrapper over `RunWith`, which takes `ParticipantOptions`; with the options zero the sequence is the old one, and the only added step is deleting a stale participant endpoint file if one exists. `server.go`: `EnableParticipantServer`, `ServeParticipant`, and `Shutdown` drains both servers under the one deadline. `cmd/rtk`: the three flags, `main.go` call and usage text. | backend | 2026-11-08 |
| 19 | P2a: startup order. Plans validated, then the one Store open (lock, migration, backup), then the directory, then the identity loaded ONCE if either listener is HTTPS (before any bind; the host listener need not be HTTPS for the participant one to be), then host bind, then participant bind (if it fails the host listener is closed again), then two `serverTLSConfig` instances (TLS 1.3, `http/1.1`), then both servers. Status lines: host lines unchanged and first; participant lines after, only when it is on, with one transport statement (HTTPS prints the certificate SHA-256 and the SPKI key pin; plain HTTP prints a warning that the bearer credential and bodies are readable on the path). Both pins are also written to `participant-endpoint.json` for the P3 owner command. The host `endpoint.json` is never touched by the participant path. | backend | 2026-11-08 |
| 20 | P2a: shutdown. `Server.Shutdown` closes the existing `closing` channel first (P2's inbox polls and pending offers will select on it, so they are cancelled and withdrawn before any drain), then `hub.closeAll`, then, if the participant server exists, drains both http servers concurrently under the caller's ONE deadline and `Close`s both if it expires. With the participant API off the code path is the old single `Shutdown`. Either `Serve` returning (an error, or nil before a shutdown) triggers `Shutdown` of the other when the participant API is on, and the error is returned. A failed endpoint write also shuts both down. No `WriteTimeout` or `ReadTimeout` on the participant server (as the host's); `ReadHeaderTimeout` 5 s still bounds a TLS handshake; each P2 route must bound its own body read and its own write after the wait (DevOps: if a `WriteTimeout` is ever added it must be well over 20 s, for example 30 s, never 20). | backend | 2026-11-08 |
| 21 | P2a: discrepancy with section 2: it says the host listener 'answers 404' to `/participant/*`. The host `Handler` is unchanged, and its `withVersion` runs before routing, so a request WITHOUT `X-Ratatoskr-Protocol: 2` gets `400 version_mismatch`, and one with it gets `404 not_found`. Changing that would touch the host handler, so I left it. **Architect: accept, or tell me to special-case the prefix.** | backend | 2026-11-08 |
| 22 | P2a: scope. `ParticipantHandler` has no route yet and answers 404 `not_found` to everything, with `X-Ratatoskr-Contract: 1.0`, wrapped in `rewriteBareErrors` and never in `withVersion` (P2 adds the routes). The participant hub is not built yet (P2); the shutdown ordering above is written so it plugs into `closing`. | backend | 2026-11-08 |
| 23 | Static reading, Network and Graph (section 3): read, not run. `Store.MessageEdges` (`graph.go:429-470`) parses both ledger addresses and skips the row unless BOTH host parts are in `paired`, which `graph.go:225` builds from the paired hosts only, so a ledger row to or from a participant id is skipped and cannot add an unknown host to an edge. The network snapshot builder reads no ledger addresses and reads `Store.Directory`, which stays hosts-only. No change to either reader is needed. | backend | 2026-11-08 |
| 24 | Static reading, the managed-session side (section 12): read, not run. (1) `Router.prepareSend` (`router/connected.go:382`) appends this host to a bare name and passes `name@host` through as typed; its only checks are empty recipient, own address, empty body, size, budget and a local reply-to; it validates the host part against nothing, so `coder@pi-lab` reaches the gateway unchanged. (2) An offer's `From` is used as an opaque origin (`router/connected.go:720,749`). (3) `peersFor` lists every directory address, so participants appear in prompts once `directoryEntries` includes them. (4) `Store.ReplyTarget` (`ledger_write.go:182`) returns the stored strings and `send.go:120-135` compares `from == request.To` and then `CurrentSession(toHost, toName)`; with the participant id in `sessions.host` both work with participant addresses on either side. Conclusion: no daemon, runtime, router, MCP, hook or host-protocol change. P2 must change `directoryEntries` (`server.go:594`) to read `WireDirectory`, compute `live` for participants from `LastSeen` and whether a poll is parked, and call `pushDirectory` on every participant registration, end, rotation, revoke and liveness change. | backend | 2026-11-08 |
| 25 | P2a follow-up (lead relay, architect's text): with the API off, `RemoveStaleParticipantEndpoint` deletes `participant-endpoint.json` only if `Lstat` says it is a regular file. A directory, a symlink, anything else, or a regular file that cannot be removed is left alone and reported as one warning line; it never fails startup and the host endpoint file is never touched. It replaces row 18's unconditional removal. Written cases: symlink (target untouched), directory, regular file, host file untouched. | backend | 2026-11-08 |
| 26 | P2: layout. New `participant.go` (`ParticipantHandler` with all eleven routes, auth, contract check, handlers), `participanthub.go` (inboxes, the one parked poll, pending offers), `participants.go` gains `ParticipantState`. `send.go`: `handleSend` now decodes and calls `routeSend(w, r, endpoint, request)`, the one send routine for both senders; its host-to-host path is the old code, with one added branch before step 3 (`ParticipantState` of the recipient's host part) that calls `deliverToParticipant`. `server.go`: `directoryEntries` reads `WireDirectory` and computes `live` for a participant from its last poll and whether a poll is parked (`HostLive`), for a host as before. Tests: `participant_api_test.go`. | backend | 2026-11-08 |
| 27 | P2: the bounded hand-over, as built. A participant is online if a poll is parked or its last poll began or ended within 15 s (`HostLive`). Offline: `destination_unreachable` at once ("the participant is not polling"), nothing queued. Online with no poll parked: the offer waits in memory only while the sender waits (at most `AckWait`); a poll claims it, or the sender withdraws it under the hub lock and the send ends `destination_unreachable` ("the adapter did not poll in time"). Claimed (a poll took it): the ack decides, silence is `delivery_uncertain`, a late ack corrects it, and it is never re-offered. A sender whose request context ends or whose gateway is closing withdraws an unclaimed offer the same way. Pending offers per participant are capped at 64 (`errInboxFull`, answered `destination_unreachable`; UNTESTED guess). | backend | 2026-11-08 |
| 28 | P2: the poll. `wait` is 0..20 whole seconds, default 20, anything else `400 bad_request`. A second poll displaces the first (`replaced:true`). Offers already waiting are claimed at once, at most 8 per answer. At the wait's end a final claim and leaving the inbox happen under one lock, so no offer is left pending with nobody polling. The poll's start and end are written to `last_poll_at`; a poll that makes the participant live (it was not) calls `directoryChanged`. A parked poll re-checks `ParticipantState` every 2 s (`participantRevokeCheck`, a variable so a test can shorten it) because a revoke is made by another process; a revoked participant gets `401`, its pending offers are dropped and the directory is pushed. A cancelled poll (the client left) writes nothing; a poll cancelled by the closing signal answers `200` with an empty list. | backend | 2026-11-08 |
| 29 | P2: bounds. The participant `http.Server` now has `WriteTimeout` 30 s (`participantWriteTimeout`), well over the longest poll plus the work around it; this supersedes row 19's 'no WriteTimeout'. There is still NO `ReadTimeout`, because net/http cancels a request's context when the read deadline passes and that would cut a parked poll. A body is bounded by `MaxBytesReader` and by a 15 s read deadline set around the read and cleared after (`decodeParticipantBody`); the inbox answer sets a 10 s write deadline. Parked polls and pending offers select on the request context AND `s.closing`. `ParticipantHandler` counts running handlers in a `WaitGroup`; `Shutdown` joins them for up to 2 s after the drain (and the forced close) and reports it if any is still running, because the caller closes the shared Store next and `http.Server.Close` does not join handlers. | backend | 2026-11-08 |
| 30 | P2: wire details. `ack` and `status` answer `200 {}` (the host routes answer 204); delete answers 204. A bad pairing code is `400 bad_pairing_code`. Every authentication failure is the same `401` text (missing, unknown, revoked and a host credential look alike). The contract check runs before routing and before authentication and applies to every route: no header is served, a major other than `1` (or an unparseable value) is `400 unsupported_contract`. `ErrTooManySessions` is `409 too_many_sessions` with its own sentence (the store error's text mentions `rtk daemon stop`). A participant ack records `host_stored` with the detail "the participant's adapter saved it", a refusal records `session_gone` with "the participant's adapter refused it: REASON". `pushDirectory` (via `directoryChanged`) is called on pair, register, end, end-all, a poll that makes the participant live, and a revoke noticed by a poll. | backend | 2026-11-08 |
| 31 | P2: known gaps. (a) A revoke made by `rtk gateway participant revoke` in another process pushes no directory by itself; `WireDirectory` omits the revoked participant on the next read, and hosts learn at the next push, which a parked poll's 2 s check triggers if one was parked. (b) Participant liveness decays by silence with no push, as a host's does. (c) A send to a revoked participant's old address falls through to the host steps and ends `destination_unreachable` with the host wording. (d) The inbox answer's write failure leaves a claimed offer uncertain by design; no test can show that without a socket. Nothing in P2 touches the host protocol, Network, Graph, cards or the daemon. | backend | 2026-11-08 |
| 32 | P3: layout. `participant_admin.go` (`runParticipant`, `pair`, `list`, `revoke`), dispatched from `RunAdmin` (`participant` added to its command list and to `adminUsage`) and from `cmd/rtk` (`participant` added to `adminCommands`, one usage line). Tests: `participant_admin_test.go`, one case in `cmd/rtk/command_test.go`. | backend | 2026-11-08 |
| 33 | P3: `participant pair ID` checks the participant listener FIRST. With no readable `participant-endpoint.json` it says the listener is not running, names `--participant-listen ADDR`, and mints NO code (I read section 11's 'prints nothing usable' as 'make nothing', since a code with no address is not usable). Otherwise it mints the code and prints, in order: the code alone on the first line, `participant: ID`, `expires in 10 minutes; single use`, a rotation note if the id is an active participant, `participant API: SCHEME://ADDR`, and for HTTPS both pins on their own labelled lines (the certificate SHA-256 as the pin to use when a library can pin a certificate; the `sha256//BASE64` key pin as a different rule for clients that can pin only a public key). Plain HTTP on a tailnet or LAN adds the plain-HTTP warning; loopback does not. A last line says the address was recorded by the running gateway at its start and is stale if the gateway has stopped. No probe of the address is made: the command opens no socket. | backend | 2026-11-08 |
| 34 | P3: `participant list` prints ID, STATE (enrolled or revoked), PAIRED, LAST POLL, LIVE, SESSIONS. `LIVE` is the 15 s window over `last_poll_at` (`HeartbeatFresh`); a parked poll lives in the gateway's memory and this process cannot see it. `participant revoke ID` prints exactly what it ended (sessions, unused codes), that the credential is refused from the next request, that a parked poll in the running gateway notices within the check interval (2 s) and is answered 401 with its untaken offers dropped, and that hosts learn at the gateway's next directory push. An unknown or already revoked id is an error. Neither command prints a credential, a hash or (except `pair`, once) a code. | backend | 2026-11-08 |
| 35 | P3: row-30 gap (a CLI revoke pushes no directory when no poll is parked) is NOT fixed: it needs a cross-process signal to the gateway (a revision row the gateway watches, or a control socket), which is a new mechanism and not a one-line change. It stays documented, and `revoke` says it. Also fixed in passing: `GetParticipant("")` returned the first participant because `listParticipants` reads an empty id as 'all'; it now reports not found. | backend | 2026-11-08 |

*Numbering note:* rows marked Architect keep the numbers quoted in messages to the lead (10 to 15); backend's rows 13 onward were numbered in sequence and so repeat 13 to 15. Cite a row by number and author.

Record here: the Network/Graph static reading (section 3), the client-side `to` validation reading (section 12), the exact Go layout, and any ruling asked of the architect.

## 15. Documentation and claims

1. `docs/spec/participant-api/README.md` (normative) and the schemas and examples of section 10.
2. `README.md`: a short section "Participant API (proposal under construction)" saying what it is, how an adapter integrates in outline, the commands of section 11, **and that nothing has been run and that no external harness is claimed to work**. The ADR table row for 0007 says "implemented on a branch, never run".
3. `docs/adr/0007-adapter-integration-contract/index.html` gets an "As built" note in section 13 once the branch is reviewed (the architect writes it).
4. A test guide, scenario 7, written by the architect after the build, for the user to run when they choose. No other test guide is updated.
5. `docs/design/README.md` and the docs home list the contract. `docs/build-docs-home.py` is re-run by the architect.
6. **A reference adapter in a language other than Go** is part of the delivery: a single-file, standard-library-only Python program under `integrations/participant-reference-py/`, no imports from `internal/`, that pairs, registers one session, long-polls, acknowledges, prints what it receives and sends one message on request. It exists to show the contract is language-neutral. It is never run by the team. The lead decides who writes it; it follows the same review as any code.

## 16. Phasing and owners

The lead sequences; the supervised-monkey rule applies as always.

| Phase | Content | Owner |
| --- | --- | --- |
| P0 | This contract reviewed; schemas and conformance examples written; architect reviews them | architect, backend |
| P1 | Migration `0003`, store methods (participants, codes, authenticate, wire directory, last-poll), name-collision rules in both pairing paths | backend |
| P2a | Participant listener: `PlanParticipantListen`, the three flags, the separate `http.Server`, TLS from the existing identity, startup lines, shutdown; no participant route yet | backend, with DevOps consulted |
| P2 | `ParticipantHandler()`; pair, discovery, sessions, directory, send (shared routine), inbox with the participant hub and the bounded hand-over, ack, status, get message; `pushDirectory` calls | backend |
| P3 | CLI `rtk gateway participant pair|list|revoke` | backend |
| P4 | Reference adapter (Python), README, spec README, ADR as-built note, scenario 7 | lead, architect |
| Listener review | DevOps reviews P2a: address-class rules, flag refusals, that the host listener and its flag table are unchanged, and the pinning recipe for adapters | devops |

Each phase ends with `gofmt`, `go vet` (with and without the `rtktest` tag, darwin and linux) and `go build -o /dev/null`-style checks only. Tests are written, never run.

## 17. Known gaps and untested items (state them in the docs)

Everything. In particular: the long-poll behaviour across proxies; the hand-over withdrawal race; the claimed-but-unwritten offer case; migration `0003` on a real database; the name-collision rules under concurrent pairing; the Network/Graph builders with a participant address in the ledger; reply handling with participant addresses; no rate limiting; the participant listener and its three flags, TLS from the shared identity on a second listener, and the any-language pinning recipes (the main usability cost of a self-signed pinned certificate); a revoked participant's parked poll closing promptly (the revoke command runs in another process).

## 18. Status

Contract written 2026-11-08 on `feat/adapter-integration` before any code, and revised the same day when the user's instruction replaced the first TLS reading (section 2). Nothing has been run.
