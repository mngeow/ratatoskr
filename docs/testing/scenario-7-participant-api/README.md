# Scenario 7: the participant API (adapters from any language)

A small HTTP+JSON API on the gateway lets an external **adapter** pair as a **participant**, register named sessions, send to any registered session and receive through a long-poll inbox. This scenario builds on the machines of scenario 2 or 3: a gateway VM, the Mac and VM-B, each with a running managed agent. You add one participant, an adapter, and check the flows between it and a managed Claude or Codex.

**This is the short form, for reading on a VM or on GitHub.** The [full HTML guide](../index.html#s7) has the same steps with a diagram, fill-in fields and copy buttons. The authoritative description is the [spec](../../spec/participant-api/README.md) and the [implementation contract](../../implementation/participant-api-contract.md). Run it when you choose; nothing here is ordered.

> **Expected results come from reading the code and the contract.** The team has not run any of this, and no external harness is claimed to work. Tell the team when something differs. Replace each `<PLACEHOLDER>`.

> **One step changes a real database.** Migration `0003` (two new tables) has never run against any database, and runs at `rtk gateway --listen` with the automatic backup. **Copy the gateway state directory first.** Do the migration on a copy if you can, with `XDG_STATE_HOME` set to it in the gateway's terminal.

> **The adapter used here is the Python reference adapter** (`integrations/participant-reference-py/adapter.py`, standard library only, its own [README](../../../integrations/participant-reference-py/README.md)). It has never been run either. Below, `ADAPTER` stands for `python3 integrations/participant-reference-py/adapter.py` and `<URL>`, `<PIN>`, `<CRED_FILE>` for the values `participant pair` printed and the credential file you choose. Its common flags are `--url`, `--pin sha256:<hex>` (the **certificate SHA-256**; it refuses the SPKI pin), `--credential-file` and, only for a plain-HTTP listener, `--insecure-http`; the environment variables `RATATOSKR_URL`, `RATATOSKR_PIN` and `RATATOSKR_CREDENTIAL_FILE` do the same. Every command runs discovery first.

> **Planned flag change: decided, not built.** On 2026-11-11 the owner decided ([ADR-0009](../../adr/0009-agent-discovery-tool/index.html), D18 to D20) to retire the gateway's `--allow-lan` and `--insecure-http`. Every gateway service will listen on `0.0.0.0` by default; the host port 7777 will be plain HTTP by default, with pinned HTTPS (`--fingerprint` pairing) kept as an opt-in; the participant API on 7790 stays HTTPS by default, with `--participant-insecure-http` as its plain opt-in. **Nothing has changed in `rtk` yet: the commands on this page are correct for the current build and still need those flags.** **Breaking, decided but not built:** every existing command line or script that passes either flag changes behaviour, and a LAN gateway that relied on the old HTTPS default becomes plain HTTP unless the HTTPS opt-in is chosen. Whether the retired flags are then refused or accepted with a warning is not decided.

## 1. Protect your data first

**G2 gateway admin**

```sh
rtk gateway hosts
# stop the gateway with Ctrl+C in its terminal
cp -a "<GW_STATE>" "<GW_STATE>.copy-before-participants"
ls -l "<GW_STATE>.copy-before-participants"
```

Expect: a full copy of the gateway state directory exists and the gateway is stopped. Restarting a daemon ends its agents, so decide whether you need to restart any.

## 2. Build on all three machines

Use a **new** build identifier. As in scenario 5: `go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk" ./cmd/rtk` on the Mac, and the Linux build copied to the other two. Never leave a binary in the source tree.

## 3. Start the gateway with the participant listener

The participant API has **its own listener**, **on by default** at the IP of `--listen` on **port 7790**, so the usual start command is enough. On a tailnet or LAN address it is **HTTPS by default**. `--participant-listen <ADDR>` picks another address; `--participant-listen off` turns it off.

**G1 gateway**

```sh
export XDG_STATE_HOME="<GW_STATE>"
rtk gateway --listen <GW_IP>:7777
# the participant API is now at <GW_IP>:7790; a LAN address needs --allow-lan as before
```

Expect:

- The host lines first, unchanged, then participant lines: the address marked as the **default** (with how to change or disable it), a statement that the transport is HTTPS, the **certificate SHA-256** and the **SPKI key pin**, each labelled as a different rule.
- `participant-endpoint.json` in the gateway directory. The host `endpoint.json` is as before.
- The host side keeps working: `rtk gateway hosts` and your managed agents behave as in scenario 2 or 3.
- With `--participant-listen off` the gateway starts exactly as before this feature, and a stale `participant-endpoint.json` is removed.
- With `--participant-listen <GW_IP>:7795` the API moves there, and the status line no longer says default.
- On a **LAN** address the host's `--allow-lan` is enough for the default (HTTPS). `--allow-lan --insecure-http` keeps the host plain but the participant API stays **HTTPS**; it is plain only with `--participant-insecure-http`, and then prints a warning. An explicit LAN `--participant-listen` needs `--participant-allow-lan`.

Check the refusals as well:
- `--listen <GW_IP>:7790` (the host on the default port): refused, naming `--participant-listen ADDR` and `off`.
- Something else already on port 7790: startup fails; there is no fallback port.
- `--participant-listen off --participant-insecure-http`: refused as a contradiction.
- `--participant-insecure-http` on a **loopback** address: refused. A **public** address: refused.

## 4. Pair a participant

**G2 gateway admin**

```sh
rtk gateway participant pair pi-lab
rtk gateway participant list
```

Expect:

- A single-use code on the first line (valid 10 minutes), `participant: pi-lab`, the API address, and, over HTTPS, both pins with labels.
- `list` shows `pi-lab` as enrolled, no last poll, not live, 0 sessions.
- **Names:** `rtk gateway participant pair <a host name>` is refused; `rtk gateway pair pi-lab` is refused once `pi-lab` exists; a name that was once a host (even after `unpair`) is refused.
- With the participant listener **off** or the gateway stopped, `participant pair` says so and prints no code.

## 5. The adapter pairs, registers and polls

**ADAPTER** (on the machine that will host the adapter; `<CODE>` is the code from step 4)

```sh
printf '%s' "<CODE>" > "$HOME/pi-lab.code"        # the code stays out of argv and shell history
ADAPTER pair --url "<URL>" --pin "<PIN>" --pair-code-file "$HOME/pi-lab.code" --participant pi-lab --credential-file "<CRED_FILE>"
rm "$HOME/pi-lab.code"
ADAPTER register coder --url "<URL>" --pin "<PIN>" --credential-file "<CRED_FILE>"
ADAPTER poll --url "<URL>" --pin "<PIN>" --credential-file "<CRED_FILE>"
```

Leave the last command running in its own terminal: it is the long poll, prints each offer it receives, then acknowledges it as saved.

Expect:

- `pair` writes the credential file with mode 0600 and **refuses to overwrite an existing file**; rotating a credential means moving the old file away first. Redeeming the same code again fails with `bad_pairing_code` (400).
- The adapter checks the certificate SHA-256 against `<PIN>` **before sending anything**, on every new connection. A wrong pin must make it stop with a pin failure and send nothing.
- `participant list` shows a last poll, `live` within 15 seconds, and 1 session.
- A managed Claude or Codex sees `coder@pi-lab` among its peers (no restart needed; the gateway pushes the directory).
- The Network and Graph tabs show **no** participant (hosts only, by design).
- `poll` exits with code 4 if another poll replaced it, and 5 if the participant was revoked.

## 6. A managed session sends to the participant

**M1 Mac** (in a managed Claude)

```
Use ratatoskr_send to message coder@pi-lab: "hello from Claude".
```

Expect:

- The poll terminal prints one offer whose origin is `claude@mac` (written by the gateway), and acknowledges it within 5 seconds. Claude's tool result is `host_stored`.
- `host_stored` means the adapter saved it, not that anyone read it.

## 7. The participant sends to a managed session

**ADAPTER** (a second terminal)

```sh
printf 'Is the build green?\n' | ADAPTER send --from coder --to claude@mac --url "<URL>" --pin "<PIN>" --credential-file "<CRED_FILE>"
echo "exit: $?"
```

Expect:

- The body is one line from standard input; the command prints the message id it generated. Exit `0` means `host_stored`: saved, not read.
- The managed session receives the message with the origin `coder@pi-lab`, pasted when its gate allows, like any other message.
- A reply by `ratatoskr_send` to `coder@pi-lab` appears in the poll terminal.
- The other exit codes, if you provoke them: `1` for `session_gone`, `unknown_recipient`, `destination_unreachable`, a 4xx refusal, or **not sent** (a connect or pin failure); `3` for anything uncertain (`delivery_uncertain`, `gateway_uncertain`, an unknown result, a 5xx, an ambiguous transport error); `5` for a 401.

## 8. Offline fails at once

Stop the `poll` command (Ctrl+C). Wait about 20 seconds. Send from a managed Claude to `coder@pi-lab`.

Expect: `destination_unreachable` quickly (no 5-second wait). Start `poll` again: **nothing arrives**; a failure is never replayed. The session stays open but not live until you register the same name again.

## 9. An uncertain result is never a failure

The reference adapter **has no way to withhold an acknowledgement**: `poll` acknowledges each offer right after printing it. So this case cannot be produced with it, and **this step is skipped** unless someone writes an adapter that does. What you can check instead is the adapter's own rule: kill `poll` (not Ctrl+C: use `kill -9`) the instant after a message is printed; if the acknowledgement was lost, the sender sees `delivery_uncertain` after about 5 seconds, not a failure, and `rtk gateway show <ID>` may show it corrected later. It is a race you may not win; do not report it as a pass unless you saw it.

## 10. Refusals

Expect each of these to be refused with the stated answer (the adapter's `send` exit code is in brackets where it applies):

- A send with a bare `to` (no `@`): `400 bad_request`.
- A host credential used on a participant route: `401 unauthorized`, identical to an unknown credential.
- A request with `X-Ratatoskr-Contract: 2`: `400 unsupported_contract`.
- A ninth open session: `409 too_many_sessions`.
- A send from a session name the participant has not registered: refused.
- `/participant/v1/...` sent to the **host** address: refused (`400 version_mismatch` or `404`), never served.

## 11. Revoke

**G2 gateway admin**

```sh
rtk gateway participant revoke pi-lab
rtk gateway participant list
```

Expect: the command says what it ended (sessions, unused codes) and that a parked poll notices within about 2 seconds. The adapter's parked poll is answered `401` within a few seconds, its next call is `401`, `pi-lab` is shown revoked, and the name stays reserved (a host cannot take it). Managed sessions may keep listing `coder@pi-lab` until the gateway's next directory push, which is a known gap.

## 12. Clean shutdown

Stop the gateway with Ctrl+C while an adapter's poll is parked.

Expect: the gateway exits within about 5 seconds (the parked poll is cancelled and answered empty), no listener is left bound, and restarting with `--participant-listen off` removes the stale `participant-endpoint.json`.

## 13. What to report

The build identifier; which steps you ran; for each, what you saw against the Expect lines; any `-32` style surprise in the gateway output; and the adapter's own log. Leave the gateway state copy in place until you are satisfied. **Do not state that a step passed unless you ran it.**
