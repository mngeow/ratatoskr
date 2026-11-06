# Cross-machine messaging: implementation contract

Owner: architect. Design source: [ADR-0002](../adr/0002-cross-machine-messaging/index.html). Authorized by the user on 2026-11-05: "Ok please begin implementation now, remember to update the main readme with testing instructions".

This is the contract the current candidate was built to. **That candidate is written and compile-checked, but has never been run.** Engineers own the code structure. If something here is wrong or doesn't fit the code, raise it through lead to architect instead of quietly diverging.

## Rules

- **GR-3 stands:** `gofmt`, `go vet`, `go build` and static checks are allowed. Write tests, but don't run them. Don't run `rtk`, harnesses, probes or SSH sessions.
- **No commit or push** is authorized by this work.
- **GR-1 and GR-2:** Go, and Bubble Tea v2 for any TUI. No new direct dependency without asking lead first; the design below needs none.
- **Local usage stays as it is.** Plain `rtk`, with no gateway, must keep its current behaviour, and it's the regression bar. The one accepted exception: its first run after this build migrates the database from schema 1 to 2, with a backup beside it, and an older `rtk` then refuses that database.
- **Mark behaviour as untested** in code notes and reports.

## Defaults chosen by architect

The user hasn't decided these. They're chosen so that work can continue, and the user can overrule them.

| Open item | Default |
| --- | --- |
| Plain `rtk` | Stays local: two agents, no gateway, outside the central view |
| Connected versus local | `rtk runtime` is always connected. Never mixed within a run, and no local bypass |
| Windows | One terminal per host. No PTY streaming |
| Central view | A read-only CLI on the gateway machine: `rtk gateway log` and `rtk gateway show` |
| Gateway retention | **No automatic deletion is built.** It's not a decision to keep data forever |
| Unsent attempts | Stay in the sending host's local record. Not uploaded later |
| Prompt status at the gateway | Not sent |
| Later failures | Shown in the TUI and the central view. The sending model isn't notified |
| Loop budget | Stays per runtime, as it works today |
| Timeouts | Fixed constants, marked `UNTESTED guess` |

## Commands

Intended forms. Lead may propose small changes, and architect finalizes the README from what's actually built.

```
rtk                                    # unchanged local mode
rtk gateway --listen ADDR [--allow-lan [--insecure-http]]   # run the gateway
rtk gateway pair HOST                  # print a one-time code for HOST
rtk gateway unpair HOST                # remove HOST
rtk gateway hosts                      # list paired hosts and their state
rtk gateway log [--follow] [--limit N] # read the ledger
rtk gateway show ID                    # one message, full body and events
rtk runtime pair --gateway ADDR --code CODE [--fingerprint SHA256 | --insecure-http]
rtk runtime [--agents claude,codex]    # run the TUI, connected; default both
```

- **`--listen` is required.** Without `--allow-lan`, accept only a Tailscale address (`100.64.0.0/10` or `fd7a:115c:a1e0::/48`) or loopback, and refuse anything else with a clear error. There's no public gateway mode. Loopback is for a gateway and runtime on one machine. Direct LAN is a separate opt-in, covered in [Direct LAN](#direct-lan).
- **It binds the literal address.** A Tailscale address needs an OS-level Tailscale interface, and Tailscale in userspace-networking mode isn't supported. The bind error must only give that advice for a Tailscale address.
- **The port must be explicit.** Refuse port 0, because a gateway needs a stable address.
- **Starting `rtk runtime`** needs a saved pairing and a reachable gateway. If either is missing, exit with a clear error before starting any harness.
- **One runtime per machine,** because of the existing per-machine lock. Plain `rtk` and `rtk runtime` can't run together under one state directory.

## Components

| | Gateway | Runtime |
| --- | --- | --- |
| Owns | Pairing, session directory, routing, ledger with full bodies, delivery reports | Harnesses, PTYs, MCP and hook server, gate, paste, local history, inbox and duplicate check, TUI |
| Must not | Write to a PTY, read hook data, decide an agent is ready, answer prompts, infer that a model acted, or import pane, TUI, agent or router packages | Trust a peer's readiness, send hook payloads or MCP tokens over the network, or give the gateway credential to a harness |

- The existing MCP and hook server stays on loopback, unchanged. **Never expose it.** The gateway is a separate listener.
- The gateway has its own database and directory under the state directory, so it can share a machine with a runtime. Directories `0700`, files `0600`.
- Don't sync database files. Don't import or apply the local 30-day retention to the gateway.

## Identity and pairing

- `gateway pair HOST` creates a single-use code that expires in 10 minutes. Host names match `[a-z0-9-]+`. Pairing an existing host replaces its credential.
- `runtime pair` exchanges the code for a random 32-byte credential, and saves it with the gateway address and host name in a `0600` file in the state directory. The gateway stores only a hash, and only in its database.
- **The host comes from the credential, never from the request.** A runtime can only register or send as agents on its own host.
- `gateway hosts` shows state from when the gateway last heard from each host, so it can be up to 15 s stale.
- Removing a host refuses its requests at once, because each request checks the database. Its open stream closes within one ping interval, because `unpair` runs in a separate process from the gateway.
- Agent names are `name@host`, such as `codex@ubuntu-b`. Each registration creates new session IDs, which are distinct from the name.
- In `to`, a bare name means an agent on this host, and other hosts need `name@host`. So a bare name can't be misdelivered to another host. It still goes through the gateway as `name@thishost`, with no local bypass.
- Being on the tailnet authorizes nothing. On a Tailscale address or loopback there's no TLS, PKI, `tsnet` or Tailscale client dependency. rtk speaks plain HTTP there, and relies on the tailnet for encryption, or on traffic staying on the machine. The address check doesn't prove Tailscale is what's listening, so no output may call that path encrypted.

## Direct LAN

Authorized by the user on 2026-11-06: "Ok please add the opt-in option, add an additional opt-in to use only in http". **These are two separate choices.** Allowing a LAN listener never authorizes plaintext.

A LAN address means `netip.Addr.IsPrivate()`: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`. Tailscale's ranges are checked first and stay Tailscale. Public, link-local and unspecified addresses, such as `0.0.0.0`, are refused with any flags.

### Flags

Each refusal names the flag or address to use instead. It happens before any file, lock or socket is touched, as `--listen` validation does today.

| `rtk gateway --listen` | Flags | Result |
| --- | --- | --- |
| Tailscale or loopback | None | Plain HTTP, as today |
| Tailscale or loopback | `--allow-lan` or `--insecure-http` | Refused: they apply only to a LAN address |
| LAN | None | Refused: names `--allow-lan` |
| LAN | `--insecure-http` alone | Refused: also needs `--allow-lan` |
| LAN | `--allow-lan` | **HTTPS,** verified by a pinned certificate |
| LAN | `--allow-lan --insecure-http` | **Plain HTTP,** with the warning below |
| Anything else | Any | Refused |

| `rtk runtime pair --gateway` | Flags | Result |
| --- | --- | --- |
| Tailscale or loopback | None | Plain HTTP, as today |
| Tailscale or loopback | `--fingerprint` or `--insecure-http` | Refused: they apply only to a LAN address |
| LAN | `--fingerprint SHA256` | **HTTPS.** The code is sent only after the certificate matches |
| LAN | `--insecure-http` | **Plain HTTP,** with the warning below |
| LAN | Neither, or both | Refused: says that the user must choose, and shows both forms |

The runtime never learns the transport from the gateway, because a network attacker could downgrade that. It comes only from what the user typed, and is saved in the pairing.

### Saved pairing

- `pairing.json` gains `fingerprint` and `insecure_http`, both omitted when unset, so an existing Tailscale or loopback pairing stays valid.
- Loading applies the runtime table above to the saved values, and refuses an invalid combination. A LAN pairing with neither is refused.
- A fingerprint always means `https://`, and `insecure_http` always means `http://`. **There's no fallback between them.** Re-pairing is the only way to change transport.

### HTTPS mode

Scope is one self-signed certificate per gateway, pinned by its runtimes. No CA, ACME, hostname checks, rotation command, client certificates or new dependency.

- **Gateway:** on the first `--allow-lan` start without `--insecure-http`, generate an ECDSA P-256 self-signed certificate with the standard library. Store it in `gateway/tls/`, with the directory `0700` and files `0600`. Reuse it on later starts, and never regenerate it automatically. If only one of the two files is present, or either is unreadable, refuse to start. The private key never leaves that directory, and never reaches the database, a log or the output.
- **Fingerprint:** `sha256:` plus the lowercase hex SHA-256 of the certificate's DER bytes.
- **Runtime:** TLS 1.3 minimum. Every connection, including `POST /v1/pair`, checks the leaf certificate against the pinned fingerprint in constant time, using `VerifyConnection`. Go needs `InsecureSkipVerify: true` for a pinned self-signed certificate. That setting may appear only where the same config sets the pin check, it must be commented there, and no flag or environment variable may turn the pin check off. An empty fingerprint must fail closed. No system roots are consulted, so expiry and address changes don't affect the pin.
- **Same HTTP behaviour:** HTTP/1.1 only on both sides, so the stream behaves as on the tailnet. Don't offer `h2` in ALPN. The per-request fresh connection, no-proxy and no-redirect rules are unchanged.
- **A mismatch is a refusal, not a retry with less checking.** `runtime pair` and runtime startup exit with a clear error saying that either the certificate was regenerated or something else answered at that address. A running runtime keeps its existing reconnect loop, with the mismatch as the visible reason. It reports that as `gateway_unreachable`, because the handshake fails before the request is written. The fix is to re-pair after checking the fingerprint on the gateway machine.
- **Tailscale and loopback stay plain HTTP.** HTTPS for them is out of scope.

### What the commands print

- `rtk gateway` prints one transport line beside its listening line. It never contains a credential, code or key:
  - HTTPS: the fingerprint.
  - LAN HTTP: the warning below.
  - Tailscale or loopback: `plain HTTP; encrypted only if this address really is Tailscale` for Tailscale, or `plain HTTP; loopback only` for loopback.
- On each start, the gateway writes `gateway/endpoint.json` (`0600`), holding the listen address, scheme and fingerprint. `rtk gateway pair HOST` reads it and prints a ready-to-run `rtk runtime pair` line with the real address and the right flags. If the file is missing, it keeps today's `ADDR` placeholder. The line reflects the last start, and doesn't show that the gateway is running.
- **Plaintext warning,** shown by `rtk gateway` and `rtk runtime pair`, with the same meaning in both: `WARNING: plain HTTP on the LAN. Anyone who can see or redirect this network traffic can read messages, the pairing code and the credential, and can inject messages into your agents.`
- The TUI title in LAN HTTP mode reads `Gateway connected (plain HTTP)`. All other labels and modes look the same as they do today, and HTTPS mode needs no extra label.
- Existing rules hold: the credential and code are never logged.

### Plain HTTP costs, for the README

The user chose this deliberately for a home network. State these plainly, and don't describe them as blocking:

- Anyone able to read the traffic gets every message, the pairing code and the credential. A stolen credential works until `rtk gateway unpair HOST`.
- Anyone able to redirect the traffic can pose as the gateway, and inject messages that get pasted into an agent. The delivery gate doesn't check who wrote a message. Permission prompts remain on the agent's own machine, but anything the agent is already allowed to do is exposed.
- WPA2 and similar Wi-Fi security doesn't protect against other devices that know the network password.
- **A laptop changing networks:** a runtime keeps reconnecting, so on another network it sends the credential to whatever answers at that address. Quit the runtime before leaving the network. Pinned HTTPS doesn't have this problem, because the handshake fails first.

Both LAN modes expose the port to every device on the LAN. A device can't do anything without pairing, but rtk doesn't configure a firewall, or limit which devices can connect.

## Protocol, version 1

JSON over HTTP, using the standard library, or over HTTPS in LAN HTTPS mode. Every request has `Authorization: Bearer <credential>` and a protocol version header. A version mismatch returns an explicit error, which the runtime shows to the user.

| Request | Purpose |
| --- | --- |
| `POST /v1/pair` | Exchange a code for a credential. The only unauthenticated route |
| `POST /v1/sessions` | Register this run and its agents. Ends this host's earlier sessions. Returns session IDs and the directory |
| `DELETE /v1/sessions` | Clean exit |
| `GET /v1/stream` | The runtime's one long-lived connection, newline-delimited JSON: `offer`, `directory`, `ping`, `report`. A new stream replaces an old one for that host |
| `POST /v1/messages` | Send. Blocks for the result below. The same ID applied twice doesn't store or offer twice |
| `POST /v1/heartbeat` | Sent by each runtime every 5 s, while its stream is open. Liveness only |
| `POST /v1/messages/{id}/ack` | The recipient host saved it, or refused it with a reason |
| `POST /v1/messages/{id}/status` | Recipient host's delivery report. Idempotent |
| `GET /v1/messages/{id}` | Current state, used to correct an uncertain record |

Runtimes always dial out. The gateway never dials a runtime, and runtimes have no new listening port.

A gateway write only shows the kernel took the bytes, so pings can't show that a host is alive. Liveness comes from what the gateway receives from the runtime. For up to 15 s after a host silently vanishes, the gateway can still count it live, so a send in that window ends `delivery_uncertain`. After that it ends `destination_unreachable`. A live but slow host can also get `destination_unreachable`, which stays true because that message was never offered.

## Timeouts (`UNTESTED guess`)

| What | Value |
| --- | --- |
| Offer waits for ack | 5 s |
| Runtime waits for `POST /v1/messages` | 8 s |
| Ping interval | 5 s |
| Runtime heartbeat interval | 5 s |
| Gateway counts a host live only if it heard from it within | 15 s |
| Runtime counts its connection dead with no ping received | 15 s |
| Reconnect backoff | 1 s up to 15 s |

## Send: the results are the design

1. The runtime applies today's local checks, then records the attempt as `sending` **before** any network call, and returns the result to the tool call. The tool call now blocks for a result, unlike local mode.
2. Nothing is retried automatically. No retry window, no outbox replay and no store-and-forward.

| Result | When | Could it arrive? | At gateway? |
| --- | --- | --- | --- |
| `gateway_unreachable` | No gateway connection, the connection failed before anything could be written, or the gateway refused the request before storing it, with its reason in the result. Must stay true: the gateway never returns a 4xx for a message it stored from that request | No | No, local record only |
| `gateway_uncertain` | Request sent, no complete response in time. Includes a connection lost mid-request | **Yes** | Possibly |
| `unknown_recipient` | Gateway has never seen that name. Result lists the current directory | No | Yes, as a failure |
| `destination_unreachable` | Host has no open stream, the gateway hasn't heard from it within 15 s, or the offer couldn't be queued to its stream, with the reason in the result. Never offered | No | Yes, as a failure, with its full body |
| `session_gone` | Host is live and the name exists, but it has no current session, or `reply_to` points to an ended session. Also used when the recipient host explicitly refuses an offer, with the reason in the result | No | Yes, as a failure, with its full body |
| `delivery_uncertain` | Offered once, no ack in time | **Yes** | Yes. A late report corrects it |
| `host_stored` | Recipient host saved it. **Tool success.** It means the host has it, not that it's delivered | Normal path | Yes |

Rules that must hold:

- **Never report an uncertain case as a definite failure.** Late acks and reports correct the ledger and the sender's record.
- **The gateway stores the full body before routing,** then records every outcome. A failure record is never delivered later, including when the host returns.
- **All connected messages go through the gateway,** including two agents on one host. If it's unreachable, they get `gateway_unreachable`.
- **The ledger shows a reported state as a report.** A message can stay at its last report, for example if its runtime quits before reporting `interrupted`. Once that session has ended, `gateway log` and `gateway show` must say so beside the state, so an old `held` isn't read as still waiting.
- **A plain name reaches the current session.** A new session is never given an older message.

## Recipient: keep what prevents a double paste

- On an offer, save to the inbox by message ID, then ack. A repeated ID acks again and has no other effect.
- After that, today's gate, frame, paste and hook confirmation apply unchanged: `held`, `delivering`, `pasted`, `accepted`, `unconfirmed`, `failed`, `interrupted`. **Holding for a busy, approval-blocked or drafting agent isn't an offline case.**
- Keep the record made before each write. Crash recovery still marks `interrupted`, and it never pastes again.
- If the gateway connection drops, messages already saved stay with the local gate. Status reports are resent after reconnect. **Messages are never resent.**
- The frame shows the sender as `name@host`, from the gateway. A peer's reports are stored and displayed as reports, and never affect a gate.

## Agents and display

- Agent instructions explain the `name@host` form and list the peers known at launch. The peer list can change, and `unknown_recipient` returns the current one. **Don't add a second MCP tool** without asking architect, because that changes what's pre-approved.
- The TUI shows gateway connection state, the new results and a single-agent layout. Local mode looks the same as it does today.
- Bodies appear only in the ledger, `gateway log`, `gateway show` and existing local views. Don't write them to logs.

## Out of scope

PTY streaming, attaching to a harness started separately, multi-user, public gateway, TLS other than the LAN HTTPS mode above, retries or store-and-forward, central-view UI beyond the CLI, retention, gateway-side loop budget, and daemon split.

## Done means

Reviewed, integrated code that builds on darwin and linux, passes `gofmt` and `go vet`, has tests written but not run, and preserves local mode by review. A report separates what's implemented from what's untested, and gives architect the actual commands and flags, so that the README can be finalized. **Nothing is verified until the user runs it.**
