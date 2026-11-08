# Ratatoskr participant API (contract 1.0)

**Status: implemented on the branch `feat/adapter-integration`, never run. Nothing here is evidence of behaviour, and no external harness (OpenRig, Pi or any other) is claimed to work with it.** This directory is the language-neutral description of the contract in [`docs/implementation/participant-api-contract.md`](../../implementation/participant-api-contract.md) (the engineering document, with every decision and its date) and of the proposal in [ADR-0007](../../adr/0007-adapter-integration-contract/index.html). The schemas and examples are normative for field names, types and enums; this README is normative for meaning.

## What this is

A small HTTP+JSON API on the Ratatoskr gateway, **separate from the host protocol** that Ratatoskr's own daemons speak (which is unchanged). It lets an **adapter**, written in any language, take part in a Ratatoskr network as a **participant**: it pairs once, registers named **sessions**, sends messages to any registered session, and receives messages through a **long-poll inbox**. An adapter translates between this API and one harness; the gateway knows nothing of the harness.

The API is **off unless the gateway owner starts its listener** (`--participant-listen`), and it exists only on LAN and tailnet addresses (and loopback). There is no public listener.

## Reference adapter

A single-file, standard-library-only Python program, [`integrations/participant-reference-py/adapter.py`](../../../integrations/participant-reference-py/README.md), pairs, registers a session, long-polls, acknowledges, and sends. It pins the certificate SHA-256 before sending any byte. It is an example for adapter authors, **never run**, and not a claim that any harness works.

## Contents

| Path | What it is |
| --- | --- |
| `schemas/*.schema.json` | JSON Schema (draft 2020-12), one per request and response type. Normative for field names, types and enums |
| `examples/*.json` | 20 conformance exchanges: `{name, description, request:{method,path,headers,body}, response:{status,headers,body}}`. Every secret in them is an obvious fake |
| `examples/index.json` | The examples in reading order, with the flow each belongs to |
| `CHANGELOG.md` | One line per contract change |

## Concepts

- **Participant.** An enrolled principal with a stable id (`^[a-z0-9-]{1,63}$`) and its **own** credential, revocable on its own. It is not a host and never uses a host credential. A host credential does not work here, and a participant credential does not work on the host protocol.
- **Session.** A named endpoint of a participant (`^[a-z0-9_-]{1,32}$`), at most **8 open** per participant. Each registration returns a new opaque **session id** (32 hex digits). Registering a name again ends the earlier open session of that name; the other sessions are untouched, and a replacement never inherits anything.
- **Address.** `name@participant`, in the same `name@host` form the rest of Ratatoskr uses. A participant id and a host name never collide (the gateway enforces it; a name that has ever been a host stays reserved). A `to` must always carry the `@`; a bare name is refused.
- **Identity.** The participant is the one its credential names. A `from` that is not a current open session of that participant is refused. The sender a receiver sees is written by the gateway, never taken from a body.
- **Who may contact whom.** Any authenticated participant may send to any registered session (host sessions and participant sessions), and any host session may send to a participant session. There are no per-session grants in version 1.

## Operations

All routes are under `/participant/v1`. JSON in and out, UTF-8, `Content-Type: application/json`. `Authorization: Bearer <credential>` on every route except pair and discovery. Times are RFC 3339 UTC.

| Operation | Request | Success |
| --- | --- | --- |
| Discovery | `GET /participant/v1` (no credential) | `200 {contract, versions, now}` |
| Pair | `POST /participant/v1/pair` `{code, participant?}` (no credential; the code is the secret) | `200 {participant, credential}`, the credential shown once |
| Register a session | `POST /participant/v1/sessions` `{name}` | `200 {participant, session:{name,address,session_id}, directory}` |
| End one / all sessions | `DELETE /participant/v1/sessions/{name}` / `DELETE /participant/v1/sessions` | `204` (ending one that is not open is not an error) |
| Directory | `GET /participant/v1/directory` | `200 {directory:[{address,host,live}]}` |
| Send | `POST /participant/v1/messages` `{id, from, to, reply_to?, body}` | `200` with a send result, for every outcome the gateway decided |
| Inbox (long poll) | `GET /participant/v1/inbox?wait=N` | `200 {offers:[...], replaced}` |
| Acknowledge | `POST /participant/v1/messages/{id}/ack` `{saved, reason?}` | `200 {}` |
| Report status | `POST /participant/v1/messages/{id}/status` `{state, reason?, at?}` | `200 {}` |
| Read a message | `GET /participant/v1/messages/{id}` | `200 {id, result, ...}` for a message you sent or received; anything else is the same `404` as an unknown id |

**Pairing.** The owner runs `rtk gateway participant pair ID` on the gateway machine, which prints a single-use code (valid 10 minutes, stored only as a hash) and, when the participant listener is running, the API address and the certificate pins. The adapter redeems the code once and keeps the credential like a password (not in a log, not in argv). Pairing an id that already exists **rotates** its credential and ends its sessions.

## Sending

`id` is a client-chosen message id (`^[A-Za-z0-9-]{1,64}$`); the same id from the same sender stores and offers once. `body` is 1 byte to 64 KiB. The gateway answers **200 for every outcome it decided, never a 4xx for a message it stored**, with one of these `result` values:

| `result` | Meaning | Could it still arrive? |
| --- | --- | --- |
| `host_stored` | The recipient's host, or the recipient participant's adapter, saved it. **Success.** Not delivered, not read, not acted on | Normal path |
| `unknown_recipient` | The gateway has never seen that name. The response lists the current directory | No |
| `destination_unreachable` | The recipient is offline (a host with no live connection; a participant that is not polling). Nothing was offered | No |
| `session_gone` | The recipient is known but has no current session, or its adapter refused the message (the `detail` says why) | No |
| `delivery_uncertain` | Offered once, no acknowledgement in time. It may still have been saved. A late acknowledgement corrects it | **Yes** |
| `gateway_uncertain` | The gateway has no outcome yet for this id (for example a repeat of a send still in progress) | **Yes** |

**A send whose answer did not arrive is uncertain, never failed.** Use an HTTP timeout above the gateway's 8 s send wait (10 s is recommended). **A client treats an unknown `result` string as uncertain, never as failed.** Nothing is retried by the gateway, and nothing is ever delivered later: a failure is final.

## Receiving: the inbox

One inbox per participant, not per session. Each offer carries `to` (the session name) and `to_session` (the id the gateway meant).

- `GET /participant/v1/inbox?wait=N`: `N` is whole seconds, `0` to `20`, default `20`; anything else is `400`. The gateway holds the request until an offer exists or `N` elapses, then answers `200 {"offers":[...],"replaced":false}`. An empty list is the normal timeout; ask again at once. At most **8** offers per answer.
- **One open poll per participant.** A second poll replaces the first, which answers at once with `{"offers":[],"replaced":true}`.
- **Acknowledge each offer** with `ack` within **5 seconds** of receiving it: `saved:true` once it is stored durably, or `saved:false` with a `reason` to refuse. Repeats are ignored. Silence makes the sender's result `delivery_uncertain`; an offer is never offered again.
- **Online** means a poll is parked now or the last one began or ended within **15 seconds**. A participant that is not online has `live:false` in the directory, and a send to it fails at once with `destination_unreachable`. Nothing is queued for it. Sessions are not ended by silence; registering the same name again replaces them.
- **A short in-memory hand-over, not a queue.** If a send arrives in the instant between two polls, the gateway holds the offer in memory for at most the sender's own wait (5 s). The next poll takes it, or it is withdrawn and the send ends `destination_unreachable`. A restart loses nothing that was promised, because no sender is still waiting.
- If the connection breaks after the gateway wrote an answer, the offers in it are uncertain: they are not offered again.

## Reports

After the acknowledgement an adapter may report what happened to a message, with `status`: `held`, `delivering`, `pasted`, `accepted`, `unconfirmed`, `failed` or `interrupted` (`queued` is refused). A report is stored with a rank (`held` < `delivering` < `pasted` < `unconfirmed` < `accepted`, `failed` and `interrupted` equal), so an out-of-order report cannot undo a later one, and a repeat is harmless. `at` is optional; the gateway stores its own receive time when it is absent. A report is **the adapter's own statement**, shown as a report. `accepted` means the adapter says it handed the message to its harness; it never means the message was read or acted on.

A host session that sent the message sees the report on its own connection. A participant that sent a message reads the result and reports with `GET /participant/v1/messages/{id}`; nothing is pushed to it.

## Errors

The body is `{"code": "...", "message": "..."}`. Switch on `code`, not on `message`, and ignore unknown fields.

| Status | `code` | When |
| --- | --- | --- |
| 400 | `bad_request` | A field fails validation; the message names the field only. Also a `wait` outside 0 to 20, a bare `to`, a body over its limit |
| 400 | `unsupported_contract` | The `X-Ratatoskr-Contract` request header names a major other than `1` (or is unparseable) |
| 400 | `bad_pairing_code` | The code is unknown, used or expired. It never says which. (The host protocol answers 403 here; this API answers 400) |
| 401 | `unauthorized` | No credential, an unknown one, a revoked participant, or a host credential. **Identical in every case** |
| 404 | `not_found` | An unknown route, or a message you may not read |
| 409 | `too_many_sessions` | The participant already has 8 open sessions after replacing a same-named one |
| 500 | `internal` | Never reveals the cause |

Error text never contains a code, a credential or a hash.

## Limits

8 open sessions per participant; 64 KiB per message body; 512 KiB per request body; 8 offers per inbox answer; poll `wait` 0 to 20 s; 5 s to acknowledge; 8 s for the gateway to answer a send; 15 s online window; pairing code valid 10 minutes, single use. These are starting values, **untested guesses**. There is **no rate limiting** in version 1.

## Versioning

The major version is the path prefix, `/participant/v1`. The minor is the `X-Ratatoskr-Contract` response header on every response (for example `1.0`), and `GET /participant/v1` returns `{"contract":"1.0","versions":["1"]}` without a credential, so an adapter can check compatibility before pairing. A client may send the same header; a major other than `1` is refused with `400 unsupported_contract`. Within a major, changes only add: the gateway ignores unknown request fields, and a client must ignore unknown response fields. A removed or changed meaning is a new major and a new prefix. This is independent of the host protocol's version and of any adapter's own version.

## Owner commands (on the gateway machine)

| Command | What it does |
| --- | --- |
| `rtk gateway --listen ADDR --participant-listen ADDR [--participant-allow-lan] [--participant-insecure-http]` | Also serves this API on its own address. Without `--participant-listen` it is not served at all. HTTPS with the gateway's certificate is the default on tailnet and LAN addresses; plain HTTP needs `--participant-insecure-http` and prints a warning. Loopback is plain HTTP. LAN addresses need `--participant-allow-lan`. The two listeners must be different addresses |
| `rtk gateway participant pair ID` | Prints a single-use code, the API address and, over HTTPS, both pins. Refuses an id that is a host, or that has ever been a host, and mints nothing when the participant listener is not running |
| `rtk gateway participant list` | Id, enrolled or revoked, paired time, last poll, live, open sessions. `live` is derived from the last poll only (the command cannot see a poll that is parked right now) |
| `rtk gateway participant revoke ID` | Ends its sessions, drops unused codes and refuses the credential from the next request. A parked poll in the running gateway notices within about 2 seconds and is answered `401`. A revoked id stays reserved |

## Transport and certificate pinning

The participant API is served over **HTTPS by default** on tailnet and LAN addresses, on its own listener, with the gateway's one self-signed certificate. Plain HTTP is possible only if the owner opts in explicitly, and the gateway then warns that the bearer credential and message bodies are readable on the network path.

The certificate carries no host name or address, so "trust the system store and check the name" will refuse it. **An adapter must pin the certificate.** `rtk gateway participant pair` prints two values for the same certificate, and they are **not equivalent**:

1. **The certificate SHA-256** (over the DER encoding), printed as `sha256:<64 hex digits>`. This is **normative**: it is what host pairing already pins. Use it whenever your TLS library can check a certificate.
2. **The SPKI pin**, `sha256//BASE64`, the base64 of the SHA-256 of the SubjectPublicKeyInfo. This is a separate **key-pin rule for clients that can pin a public key** (Go or Node with a custom verifier, for example). It proves the same key holder, not the same certificate file.

### Recipes (UNTESTED: written from documentation, never run)

Both recipes pin before any credential or message is sent. Neither disables checks anywhere else.

**Python (standard library), certificate SHA-256.** Verification is switched off only on the dedicated pinning socket, whose first act is to complete the handshake and compare the peer certificate with the pin.

```python
import hashlib, hmac, http.client, ssl

PIN_HEX = "<the 64 hex digits after 'sha256:'>"

class PinnedConnection(http.client.HTTPSConnection):
    """Connects, completes the TLS handshake, checks the pin, and only then lets a request through.
    The check runs on every new connection. No proxy, no redirect following, no fallback."""
    def __init__(self, host, port):
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.minimum_version = ssl.TLSVersion.TLSv1_3
        context.check_hostname = False      # the pin is the identity
        context.verify_mode = ssl.CERT_NONE # only on this pinning socket
        super().__init__(host, port, context=context, timeout=10)

    def connect(self):
        http.client.HTTPConnection.connect(self)          # plain TCP only
        self.sock = self._context.wrap_socket(self.sock)  # handshake completes here
        der = self.sock.getpeercert(binary_form=True)
        if not der or not hmac.compare_digest(hashlib.sha256(der).hexdigest(), PIN_HEX):
            self.sock.close()
            raise ssl.SSLError("certificate does not match the pin")
```

**curl is not supported.** A stock `curl` cannot be pinned-only against this certificate: `--pinnedpubkey` is checked in addition to normal certificate verification, which this self-signed certificate fails, and the only way past that is to disable verification, which this document does not show or endorse. Use the Python recipe above, or a client that can verify a pin itself.

The SPKI pin is for clients that can pin a public key, such as Go or Node with a custom verifier. It is a different trust rule from the certificate SHA-256, not a replacement for it.

## Known gaps (everything is untested)

- **Nothing has been run:** not the listener, not the migration (`0003`), not a single route, not the commands.
- A revoke made by the command line pushes no directory update by itself; hosts learn at the gateway's next directory push. Liveness decays by silence with no push. A send to a revoked participant's old address ends `destination_unreachable`, with wording written for hosts.
- If the gateway wrote a poll answer and the connection broke, the offers in it stay uncertain by design.
- The long-poll behaviour across proxies; the hand-over withdrawal race; the pairing-name rules under concurrent pairing; reply handling with participant addresses; the Network and Graph tabs with a participant address in the ledger (read, not run); the pinning recipe; the listener and its flags.
- No rate limiting. No tailnet or loopback HTTPS exception exists beyond what is written above. An owner-supplied certificate is not supported.
- A delegated-work (task) profile does not exist. This contract carries conversation only.

## What is not claimed

That any harness works with this API; that any listed behaviour has been observed; conformance of any adapter. The conformance examples are exchanges to compare against, not a test suite anyone has run.
