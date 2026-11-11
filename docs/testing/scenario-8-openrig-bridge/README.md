# Scenario 8: an OpenRig operator reaching a managed Claude through Ratatoskr

**UNTESTED — PENDING USER EXECUTION.** No team member has run this guide: no build, pairing, launchd action, SSH,
network access, live-seat action or message. Commands and expected text were checked against source and the
[OpenRig bridge contract](../../implementation/openrig-bridge-contract.md), not observed. This is not evidence that
the bridge, skill activation or a message exchange works.

The [HTML guide](../index.html#s8) has the same scenario with fill-in fields and copy buttons. The bridge is described
in [`integrations/openrig/`](../../../integrations/openrig/); use its
[launchd README](../../../integrations/openrig/launchd/README.md) for the exact service install and removal commands.
For a first install, the [setup guide](../../guides/openrig-setup/README.md) walks through the same three machines
end to end, and [How the OpenRig integration works](../../guides/openrig-integration/index.html) explains the pieces.

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
those flows to the required peers. **HTTPS is the primary path for the participant API**, and a plain-LAN host
connection does not change that. An unencrypted participant listener is an **optional, deliberately insecure variant**
for a trusted private network - how to ask for it, and what it costs, is under
[Optional: a plain-HTTP participant listener](#optional-a-plain-http-participant-listener).

> **Planned flag change: decided, not built.** On 2026-11-11 the owner decided ([ADR-0009](../../adr/0009-agent-discovery-tool/index.html), D18 to D20) to retire the gateway's `--allow-lan` and `--insecure-http`. Every gateway service will listen on `0.0.0.0` by default; the host port 7777 will be plain HTTP by default, with pinned HTTPS (`--fingerprint` pairing) kept as an opt-in; the participant API on 7790 stays HTTPS by default, with `--participant-insecure-http` as its plain opt-in. **Nothing has changed in `rtk` yet: the commands on this page are correct for the current build and still need those flags.** **Breaking, decided but not built:** every existing command line or script that passes either flag changes behaviour, and a LAN gateway that relied on the old HTTPS default becomes plain HTTP unless the HTTPS opt-in is chosen. Whether the retired flags are then refused or accepted with a warning is not decided.

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
Where the listener requires opt-in, the gateway prints its plain-HTTP warning instead.

**The participant API may be `http://` as well as `https://`.** Paste whichever address the gateway prints on its
`participant API:` line; nothing asks you to type a command-line flag into a field. This scenario's primary path is
still HTTPS, and an `http://` gateway needs one explicit consent inside the wizard, described under
[TUI keys and pairing input](#tui-keys-and-pairing-input).

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

For the HTTPS scenario, omit `--insecure-http`. Setup is a Bubble Tea v2 full-screen wizard with a title, progress,
and a step sidebar at 80 columns or wider. Below that width the step marks fold into the header; below 40×12 it
keeps its state and asks you to resize. The current step has a scrolling form and a persistent key footer. It is safe
to re-run; it detects existing state and asks before replacing files or changing a running service.

### The nine steps

1. **Preflight** checks the config, state directory, credential, stopped marker and (on macOS) launchd plist/job.
2. **Local OpenRig** collects the `rig` path, its PATH (including Node), `OPENRIG_HOME`, operator seat and optional
   daemon URL. Its read-only readiness check defaults to Yes; setup never starts an OpenRig seat.
3. **VM-A pairing material** shows the VM-A command and pin rule, then accepts the full pairing block in a masked
   area or lets you enter URL, certificate pin and masked code separately. The `keep` and `use` questions default to
   Yes when present. For a staged plain-HTTP address, `allow_http` is shown on review and defaults to No. The pasted
   code is never rendered.
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
engine's default, which is Yes. When offered for a staged plain-HTTP address, `allow_http` starts at No. Press `y` or
`n` (or Space) to change a focused question.

### TUI keys and pairing input

- Tab / Shift+Tab move focus. Up / Down move between controls, except in the pairing-method list where they choose
  Paste or typed fields. Left / Right edit a field, switch a Yes/No answer, or choose a focused button. Home / End,
  Backspace and Delete edit ordinary fields. Enter advances from a field or question and activates the selected
  button. PageUp / PageDown scroll the pane; `?` opens full help (in a text field or the paste area it is typed
  instead).
- The pairing step first asks how to provide the values. In the masked paste area, bracketed paste is read as one
  block; Enter adds a newline when typing it line by line. The block is parsed automatically when a line begins with
  `the address above was recorded`, or you can focus **Read pasted text**. **Clear** wipes it. Parse errors keep the
  content masked so you can retry or clear it. The code and block are never rendered.
- The typed fallback has URL, certificate pin and a masked code field. A pasted URL may include the gateway's
  `participant API:` label and one trailing LF or CRLF; setup trims those before validating. Real interior line breaks
  are refused without changing the field. The code stays in a wipeable byte buffer and is not passed through field
  validation. Enter advances through fields; Continue stages them. No Ctrl+D, Ctrl+F or Ctrl+U shortcuts are used.
- **A plain `http://` address needs an explicit consent.** On the review screen, directly above **Use these
  values?**, setup shows **Accept plain HTTP for this gateway? (not encrypted)**, which starts at **No** and lists
  why: the address is `http://`, so nothing is encrypted and anything on the network path can read the pairing code,
  the credential and every message; choose Yes only on a network you trust, otherwise give the gateway an `https`
  listener. Continuing with it left at No does not fail the step - the wizard stays on the review with the gateway's
  explanation and applies nothing. Choosing Yes lets the wizard use the address; the bridge must then run with
  `--insecure-http`, and the launchd service step is skipped. `setup --insecure-http` is an optional way to accept
  plain HTTP for the whole run in advance. Leave the certificate pin empty for `http://`: there is no certificate to
  pin, and a pin typed by mistake is refused.

### Optional: a plain-HTTP participant listener

This is not the primary path, it is **not** recommended, and nothing here has been exercised. It exists because a
private VM network sometimes has no TLS story at all, and saying "never" left no supported way to do it.

Two different flags are involved, and they are not interchangeable:

- `rtk gateway --listen ... --insecure-http` makes the **host** protocol plain. It does **not** apply to the
  participant API.
- `rtk gateway --listen ... --participant-insecure-http` makes the **participant API** plain. Only this one changes
  what the pairing block contains, and the gateway prints a warning that the credential and every message are readable
  on the network path.

The sequence:

1. Restart the gateway with `--participant-insecure-http` (the participant listener must be on, as usual). Loopback
   needs no flag and refuses it; a tailnet or LAN address needs it, plus `--participant-allow-lan` for a LAN address.
2. On VM-A, pair with the flag that asks for a plain block:
   `rtk gateway participant pair openrig-main --insecure-http`. The block then carries an `http://` address and **no
   certificate pin lines**, because there is no certificate to pin.
3. In the setup wizard, the address stages normally and the review screen asks **Accept plain HTTP for this gateway?
   (not encrypted)**, which starts at **No** and explains that the pairing code, the credential and every message are
   readable on the path. Accept it **only** on a network you trust. `certificate_sha256` stays unset.
4. The bridge then runs with `--insecure-http` in the foreground, and the launchd service step is skipped. `setup
   --insecure-http` remains an optional way to accept plain HTTP for the whole run in advance.
- Esc backs out of a pairing subview to the method choice and discards that subview's input; from review it returns
  to the source without discarding the staged review. Esc at a step's top level opens **Quit setup?**, with **Stay**
  focused by default. Ctrl+C uses the same safe quit path; if an engine operation is running, choosing Quit waits
  for it to finish. The final checklist exits with Enter or `q`.

### When something needs your attention

Engine errors appear in a dialog over the screen you are on, with the engine's own safe text and never the pairing
code or a credential. **OK** dismisses it, **Retry** re-reads the retained pasted block where that is offered (a failed
paste keeps its masked content), and **Back** returns to the previous screen. Inline field errors stay on the form as
well, and pressing Continue on a step that is blocked explains why in the same dialog. Ctrl+C still opens the quit
confirmation above it: **Stay** closes only that, and the error dialog is still there.

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
