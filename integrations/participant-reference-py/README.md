**UNTESTED; never run by the team; no claim that any harness works.**

# Ratatoskr participant adapter (Python reference)

A single-file, standard-library-only Python program that shows what the Ratatoskr **participant API**
(contract 1.0) looks like from outside the gateway: it pairs with a one-time code, registers one
session name, long-polls an inbox, prints what arrives, acknowledges it, and sends one message. It is
an example to copy, not a harness integration: it knows nothing about any particular agent, and
nothing here has been executed.

Read the contract itself in `docs/spec/participant-api/` (README, schemas, examples). Where this
adapter and the contract disagree, the contract wins, and this adapter is wrong.

Python 3.8 or newer. No third-party packages, no `internal/` imports, no network library beyond
`socket`, `ssl` and `http.client`.

## What the owner does first, on the gateway

The participant API has its own listener and must be running before pairing can print anything usable.
It is on by default, at the IP of `--listen` on port 7790 (HTTPS on a tailnet or LAN address), so a plain
gateway start is enough:

    rtk gateway --listen 100.x.y.z:7777        # participant API at https://100.x.y.z:7790

`--participant-listen ADDR` picks another address, and `--participant-listen off` turns the API off.

Then mint a participant and a one-time pairing code. This prints the code once, the participant API
URL the adapter should use, and, because that listener is HTTPS, two pins:

    rtk gateway participant pair pi-lab

- **certificate SHA-256**, printed as `sha256:<64 hex digits>` - this is the one the adapter takes
  (`--pin`), and it is the normative pin;
- **SPKI pin**, printed as `sha256//...` - a different trust rule for clients that can pin a public
  key. The adapter does **not** accept it: it checks the certificate, not the key.

The owner command's output is the only place the code appears. It is a secret: treat it like a
password. Put it into a private file with a text editor (mode 0600), or set `RATATOSKR_PAIR_CODE`
without putting the value in a command-line argument or shell history.

## Usage

All four subcommands take the same connection arguments, and each one also reads them from the
environment (a flag wins):

| Flag | Environment | Meaning |
| --- | --- | --- |
| `--url` | `RATATOSKR_URL` | participant API base URL, e.g. `https://100.x.y.z:7801/` |
| `--pin` | `RATATOSKR_PIN` | `sha256:<64 hex>` certificate SHA-256; required for HTTPS, refused for HTTP |
| `--credential-file` | `RATATOSKR_CREDENTIAL_FILE` | the file the credential is written to and read from |
| `--insecure-http` | - | allow a plain-HTTP URL; warns on every start |

Every invocation first requests `GET /participant/v1` without credentials and refuses an incompatible
contract before doing its operation; HTTPS discovery is pinned just like every other request.

### 1. Pair

    ./adapter.py pair --url https://100.x.y.z:7801/ --pin sha256:... \
        --pair-code-file ~/pair-code --credential-file ~/pi-lab.credential

The code comes from a file or from `RATATOSKR_PAIR_CODE`; it is never a command-line argument, and it
is trimmed of surrounding whitespace before use. `pair` refuses to continue unless discovery says the
gateway serves contract major `1`.

The credential is written to `--credential-file` with mode 0600 and an exclusive create: **an existing
file is never overwritten**, so a rotation is explicit - move the old file away first. The credential is
printed nowhere, and no log line, error or usage text contains it.

### 2. Register a session

    ./adapter.py register pi-lab-coder --url https://100.x.y.z:7801/ --pin sha256:... \
        --credential-file ~/pi-lab.credential

Session names match `^[a-z0-9_-]{1,32}$`. The address the gateway prints is what other sessions send to
(`name@participant`). Registering the same name again ends the earlier session of that name.

### 3. Poll the inbox

    ./adapter.py poll --url https://100.x.y.z:7801/ --pin sha256:... \
        --credential-file ~/pi-lab.credential

Each offer is printed with `from`, `to`, the body, and the `id`/`reply_to` needed to answer it, and is
then acknowledged `saved:true`. An empty answer is the normal timeout and the adapter asks again at
once. It stops with exit code 4 if another poll for the same participant displaces this one (check that
the adapter is not running twice), and with exit code 5 if the credential has been revoked. A failed
ack is **not** retried: the sender sees `delivery_uncertain`, which is the honest record.

If printing an offer fails, the offer is **not** acknowledged and the adapter stops: telling the
gateway `saved:true` for a message nobody saw would be a lie the sender's ledger believes.

### 4. Send one message

    printf 'Is the build green?\n' | ./adapter.py send --from pi-lab-coder --to claude@vm-b \
        --url https://100.x.y.z:7801/ --pin sha256:... --credential-file ~/pi-lab.credential

The body is one line on stdin and must be 1 byte to 65536 bytes of UTF-8. `--to` must be a complete
`name@host` or `name@participant`; a bare name is refused, because a participant has no "this host". A
fresh message id (`uuid4().hex`) is generated and printed so the result can be looked up with
`GET /participant/v1/messages/{id}`; add `--reply-to ID` to answer a specific message.

## How a send result is reported

The gateway answers `200` with one of six result words, and this adapter maps each to a line and an exit
code. Nothing is ever retried.

| Result | Printed as | Exit |
| --- | --- | --- |
| `host_stored` | the other side saved it; **delivery and read are not confirmed** | 0 |
| `session_gone` | the recipient session is gone; not saved | 1 |
| `unknown_recipient` | no agent has that address, with the directory | 1 |
| `destination_unreachable` | the destination was not reachable; it was not queued | 1 |
| `delivery_uncertain` | **it may have been saved** - do not resend under a new id | 3 |
| `gateway_uncertain` | the first send with this id is still in progress | 3 |

Everything else is uncertain, never failed: a result word this adapter does not know, a missing or
non-string `result`, a body that is not JSON, a truncated read, a socket timeout or reset after the
request was written, and any `5xx`. All of them exit 3 with the same warning, because none of them
proves the message was not stored. The one exception is a transport failure **before** anything was
written - a refused connection or a pin mismatch - which exits 1 and says `NOT SENT`, because nothing
was sent.

| Exit | Meaning |
| --- | --- |
| 0 | recipient side saved it (`host_stored`); delivery and read are not confirmed |
| 1 | the gateway refused it (an error `code` is printed) or decided it was not delivered (`session_gone`, `unknown_recipient`, `destination_unreachable`), or it provably was not sent |
| 2 | this adapter's own usage, files or configuration |
| 3 | the outcome is unknown: it may have been stored |
| 4 | another poll displaced this one |
| 5 | the credential was refused: revoked or unknown |
| 130 | interrupted |

Switch on the gateway's error `code`, never on its `message`; ignore response fields you do not know.

## Pinning: why it is done this way

The gateway's certificate is self-signed and carries no host name, so "trust the system store and
check the name" will refuse it. The pin is the identity instead.

This adapter uses `http.client` with a pinned `HTTPSConnection` subclass rather than writing HTTP/1.1
by hand, because it needs the pin check to happen in exactly one place and to be impossible to skip:

- each request builds a **fresh connection**, so the check runs on every connection;
- `connect()` opens the TCP socket, wraps it in TLS, **completes the handshake**, reads the peer
  certificate's DER encoding, and compares `sha256(der)` with the pin using `hmac.compare_digest`;
- only after that comparison does it assign `self.sock`, and `http.client` writes nothing until
  `connect()` has returned - so no request, not even discovery, can be sent on an unverified connection;
- certificate verification is disabled **only** inside that connection's context, which exists for one
  request and is never reused or passed anywhere else; the pin check replaces it;
- `http.client` never follows a redirect and never uses a proxy, so neither can happen by accident, and
  there is no fallback: a pin mismatch closes the socket and stops the command.

The TLS floor is 1.3, as the spec's Python recipe sets it.

Incoming message text and gateway-provided labels have terminal control and format characters
rendered as visible escapes, so message content cannot issue terminal control sequences.

Plain HTTP always needs `--insecure-http`, including on loopback, and prints a warning on every HTTP
start because the credential and message bodies are readable by anything on the network path.

## Known gaps

- Nothing here has been run: not the pairing, not a poll, not a send, not the pin check.
- A generic HTTP client without certificate pinning cannot safely use this self-signed endpoint; no
  verification-disabling flag appears anywhere in this repository's adapter docs.
- No rate limiting, no reconnect backoff tuning (a failed poll is retried after 2 seconds), no
  multiple sessions over one adapter, no reply threading beyond `--reply-to`, no `status` reports and no
  `ack saved:false` path - the four subcommands are the whole program.
- A revoked credential is noticed on the next request, which is the next poll; nothing is pushed.
- Python 3.8+ is assumed, not tested on any interpreter.

Status: not run; conformance is unverified.
