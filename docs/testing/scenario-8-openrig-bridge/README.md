# Scenario 8: an OpenRig operator reaching a managed Claude through Ratatoskr

**UNTESTED — PENDING USER EXECUTION.** No team member has run this guide: no build, pairing, launchd action, SSH,
network access, live-seat action or message. Commands and expected text were checked against source and the
[OpenRig bridge contract](../../implementation/openrig-bridge-contract.md), not observed. This is not evidence that
the bridge, skill activation or a message exchange works.

The [HTML guide](../index.html#s8) has the same scenario with fill-in fields and copy buttons. The bridge is described
in [`integrations/openrig/`](../../../integrations/openrig/); use its
[launchd README](../../../integrations/openrig/launchd/README.md) for the exact service install and removal commands.

## Outcome and machines

The existing OpenRig operator on your Mac sends to an `rtk`-managed Claude on VM-B through the gateway on VM-A, then
receives the reply. The gateway never dials the Mac or VM-B: the sidecar polls out to the gateway, and VM-B's daemon
sends a reply through the same gateway.

| Machine | Role | Address |
| --- | --- | --- |
| Your Mac | Existing OpenRig kernel/operator and `ratatoskr-openrig` sidecar | Seat `operator-agent@kernel`; participant address `operator@openrig-main` |
| VM-A | Ratatoskr gateway | Host protocol `<VM_A_IP>:7777`; participant API HTTPS `<VM_A_IP>:7790` |
| VM-B | `rtk` daemon with managed Claude | `claude@vmb` |

Reuse the working scenario 2 or 3 gateway, `vmb` pairing, daemon and Claude. Do not make a second gateway or change
`XDG_STATE_HOME` unless that is intentional. The Mac must reach VM-A TCP 7790; VM-B must reach VM-A TCP 7777. Restrict
those flows to the required peers. This scenario keeps the participant API on HTTPS; a plain-LAN host connection does
not change that. Never pass `--participant-insecure-http` here.

## 1. Start or reuse the gateway and VM-B Claude

On VM-A, reuse the existing gateway where possible. If it needs starting, use the same state directory as scenarios 2
or 3. Tailnet example:

```sh
export XDG_STATE_HOME="<VM_A_STATE>"
rtk gateway --listen <VM_A_TAILNET_IP>:7777
```

For a private LAN host-protocol connection only:

```sh
export XDG_STATE_HOME="<VM_A_STATE>"
rtk gateway --listen <VM_A_LAN_IP>:7777 --allow-lan --insecure-http
```

On VM-B, pair only if needed, then run one Claude:

```sh
rtk runtime pair --gateway <VM_A_TAILNET_IP>:7777 --code <VMB_CODE>
rtk daemon
rtk attach --agents claude
```

For the LAN host protocol, use the LAN IP and add `--insecure-http` to `rtk runtime pair`. Keep the participant API
HTTPS. `rtk attach --agents claude` starts Claude rather than the default Codex. The target address is `claude@vmb`.

## 2. Get the VM-A participant block and its certificate pin

On VM-A, in a terminal you control, run the participant pairing command. If the gateway uses a non-default
`XDG_STATE_HOME`, export it first:

```sh
rtk gateway participant pair openrig-main
```

Over HTTPS, source prints this shape; angle-bracket values are placeholders:

```text
<CODE>
participant: openrig-main
expires in 10 minutes; single use
participant API: https://<VM_A_IP>:7790
certificate SHA-256 (the pin to use when your TLS library can pin a certificate): sha256:<64 lowercase hex>
key pin (a different rule, for clients that can pin only a public key): sha256//<BASE64>
the address above was recorded by the running gateway at its start; if the gateway has stopped or moved, it is stale (checked: <file>)
```

The one-time code is the first line. The pin to copy is the value from the exact line labelled
`certificate SHA-256 (the pin to use when your TLS library can pin a certificate)`, **including the `sha256:`
prefix**. It is the certificate DER SHA-256 fingerprint the bridge checks against the peer certificate. That whole
value is what `S8_CERT_PIN` and `certificate_sha256` mean.

The `key pin (a different rule, for clients that can pin only a public key)` line is a different trust rule. Its
`sha256//` value is not the config value and the bridge refuses it. Do not copy it. If the participant already exists
and is not revoked, the gateway adds the rotation line: `this is a rotation: redeeming the code replaces the
credential and ends this participant's open sessions`. Redeeming that code replaces the old credential and ends open
participant sessions.

On a plain-HTTP participant listener, no certificate pin is printed and none may be set in `certificate_sha256`.
Where the listener requires opt-in, the gateway prints its plain-HTTP warning instead. This scenario requires the
participant API to remain HTTPS.

## 3. Build the bridge on the Mac

Keep the binary outside the checkout:

```sh
cd "<ABSOLUTE_CHECKOUT>/integrations/openrig"
go build -o "<USER_BIN>/ratatoskr-openrig" .
"<USER_BIN>/ratatoskr-openrig" --version
```

The command and version output are source-checked only. The build step stays part of this scenario; a successful build
does not mean the binary or bridge has been run by the team.

## 4. Recommended path: guided setup TUI

From an ordinary Mac terminal, run the built binary. `setup` requires terminal stdin and stdout; it exits with usage
status if either is not a terminal. Use an absolute config path if you do not want the default
`~/.local/state/ratatoskr-openrig/config.json`:

```sh
"<USER_BIN>/ratatoskr-openrig" setup --config "<CONFIG>"
```

For the HTTPS scenario, omit `--insecure-http`. Setup is a Bubble Tea v2 full-screen TUI: the step list stays on the
left and the current step on the right. It is safe to re-run; it detects existing state and asks before replacing
files or changing a running service.

### The nine steps

1. **Preflight** checks the config, state directory, credential, stopped marker and (on macOS) launchd plist/job.
2. **Local OpenRig** collects the `rig` path, its PATH (including Node), `OPENRIG_HOME`, operator seat and optional
   daemon URL. Its read-only readiness check defaults to Yes; setup never starts an OpenRig seat.
3. **VM-A pairing material** shows the VM-A command and pin rule, then accepts the full pairing block in a masked
   area or lets you enter URL, certificate pin and masked code separately. The `keep` and `use` questions default to
   Yes when present. The pasted code is never rendered.
4. **Configuration file** shows the proposed field changes. `write` defaults to Yes; an existing config is backed
   up when replaced.
5. **Pair** redeems the staged code in memory when no credential exists. Existing credentials are left alone; setup
   never deletes or moves one.
6. **Service** prepares and checks the macOS launchd plist, then asks separately before installing or starting it.
   `prepare` and a new `install` default to Yes. `replace`, `start`, and `restart` default to No. If the binary is
   inside the source checkout, `checkout` defaults to No.
7. **Readiness** checks the sidecar socket and reports gateway, participant and operator-seat status; it waits up to
   20 seconds only when setup started or detected a running service.
8. **Skill activation** writes the packaged skill file and may offer the current-conversation `rig walk`. Replacing a
   differing skill file and running the walk both default to No.
9. **Finish** shows the checklist and ends with `scenario 8 round trip: PENDING USER EXECUTION`.

The other explicit choices are `check` in step 2, `keep`/`use` in step 3 and `write` in step 4; each starts at the
engine's default, which is Yes. Press `y` or `n` (or Space) to change a focused question.

### TUI keys and pasted block

- Tab / Shift+Tab and Up / Down move between fields and questions; Left / Right, Home / End, Backspace and Delete edit
  ordinary fields.
- In the masked whole-block area, Enter inserts a newline. This preserves line breaks when a terminal or multiplexer
  delivers the paste as individual key presses instead of one bracketed-paste event. Press Ctrl+D to submit the block.
- Ctrl+F switches between the pasted block and typed URL/pin/code fields. In the typed fallback, Enter checks and
  stages the pairing fields. Ctrl+D submits a focused masked code field; Ctrl+U clears the active secret buffer.
- Esc arms quit; a second Esc or Enter confirms. Ctrl+C quits without confirming, but if a step is running it waits for that step to finish first. The wizard does not echo the pairing
  code or pasted block.

### When the launchd service step is skipped

The launchd step is macOS-only. It is skipped when running on another OS, when setup detects it is inside an OpenRig
seat, or when the participant API is plain HTTP because the launchd template has no insecure-HTTP option. On those
paths setup prints the foreground `serve` command; keep that process running in a terminal you control. A binary
started through `go run` is temporary: setup asks you to build/install a stable binary and run setup again. If the
binary is inside a source checkout, the explicit `checkout` question defaults to No. Systemd is outside v1.

If setup installs or starts launchd, follow the
[launchd README](../../../integrations/openrig/launchd/README.md) for any later service management. Do not run a
foreground sidecar and launchd sidecar at the same time. Its documented config-error restart loop, socket-path limit,
job environment and stopped-marker behavior are source notes, not runtime observations.

### Skill walk is not a persistent install

The user runs the `rig walk` shown in the skill step from an ordinary terminal, as themselves. It gives the current
operator conversation the skill text; it does not register a persistent custom skill. Repeat the walk after a relaunch
or compaction. Persistent catalog registration is a known gap. Do not use `rig skill loadout --apply` or edit
OpenRig-owned catalog/state as a workaround, and do not claim the skill is active unless the user ran the walk.

## Manual fallback: config and pair commands

Use this only if the TUI cannot be used. The config contains no secret; paths must be absolute, and the file must be
owned by you and not writable by group or others. For HTTPS, copy the **whole** certificate value including
`sha256:`; never use the `sha256//` key pin.

```json
{
  "config_version": 1,
  "participant_url": "https://<VM_A_IP>:7790",
  "certificate_sha256": "sha256:<64 lowercase hex>",
  "participant": "openrig-main",
  "session": "operator",
  "operator_seat": "operator-agent@kernel",
  "rig_path": "<ABSOLUTE_RIG_PATH>",
  "rig_env_path": "<ABSOLUTE_PATH_INCLUDING_NODE>",
  "openrig_home": "<ABSOLUTE_OPENRIG_HOME>",
  "state_dir": "<ABSOLUTE_PRIVATE_STATE_DIR>"
}
```

Create a private, one-line code file locally, paste the code into it with an editor, then pair. Never place the code
in a shell command, argv or environment:

```sh
(umask 077; set -C; : > "<CODE_FILE>") && chmod 0600 "<CODE_FILE>"
"<USER_BIN>/ratatoskr-openrig" pair --pair-code-file "<CODE_FILE>" --config "<CONFIG>"
```

The credential is created with exclusive mode 0600 before redemption and is never printed. Existing credentials are
refused, not overwritten. Delete the code file only when you choose. Then use the launchd README for macOS service
installation; elsewhere run `serve --config "<CONFIG>"` in a terminal. A foreground `serve` blocks, so run `status`
from a second terminal and stop `serve` before bootstrapping launchd.

## 5. The round trip

After the sidecar is running and the operator skill is available, ask the operator to message `claude@vmb`. The
operator uses `ratatoskr-openrig directory` to choose an address, then `ratatoskr-openrig send --to claude@vmb`; its
message body is read from standard input. For `host_stored`, the recipient side saved the message; delivery and read
are not confirmed. An `unknown_recipient` result also prints the directory.

On VM-B, the managed Claude replies through its existing `ratatoskr_send` tool, addressed to
`operator@openrig-main` and with the incoming message id as `reply_to`. Do not run the Mac-only sidecar client on
VM-B. The sidecar polls the reply and types it into the operator conversation with a provenance block. The `From:`
line is `From: <unknown sender>` and proves no identity; message text is untrusted correspondence.

An offer may be refused because the seat is not ready, the bridge is busy, the journal is unavailable, or a size or
rate limit was hit. Refused means not pasted. A saved/accepted result does not mean the user read the message.

## 6. Failure handling and evidence

- Exit 2: local arguments, config or files need correction; nothing was sent.
- Exit 3: outcome uncertain. Do not retry under a new id; check first and only retry with the same `--id`.
- Exit 5: credential revoked or unknown; the sidecar stops and records `stopped.json`.
- Exit 6: sidecar socket is down; nothing was sent. Check status/service from the Mac, not from a managed seat.
- Sidecar exit 4 means a poll was replaced. It is a service result, not a send result.

Record the operator's exact text including `From:` and the provenance block, `ratatoskr-openrig status` output, message
ids/result words on both sides, relevant final lines of `serve.err.log`, and differences from the expected behavior.
Redact credentials, pairing codes, private paths and message bodies. **Pasted or unconfirmed is not accepted or
read.** Do not report any step as passed unless you ran it.

Cleanup is owner-directed: use the launchd README to stop/remove the user agent, and revoke `openrig-main` only if
you no longer want the participant. Do not stop OpenRig, its kernel/operator, or VM-B sessions for this scenario; do
not remove the credential, journal or logs merely to tidy up.
