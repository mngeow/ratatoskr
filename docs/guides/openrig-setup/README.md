# Setting up the OpenRig bridge: Mac operator, gateway on VM-A, Claude on VM-B

**UNTESTED — PENDING USER EXECUTION.** Nobody on the team has run any command in this guide: no build, gateway,
pairing, launchd, network or message. The commands and expected output were checked against the source on
`feat/openrig-bridge`, not observed. If what you see differs, the source and your terminal are right and this page is
wrong. Please say what you saw.

This guide takes you from nothing to one round trip: the OpenRig operator on your Mac sends a message to a
Ratatoskr-managed Claude on VM-B, and Claude's reply appears in the operator's conversation.
- [How the OpenRig integration works](../openrig-integration/index.html) explains why each piece exists.
- [Scenario 8](../../testing/scenario-8-openrig-bridge/README.md) is the test record. It has more detail on the
  setup keys, the manual fallback and the failure codes.

This guide does not repeat those, and where they disagree, scenario 8 wins.

## The three machines

| Machine | Runs | You will need |
| --- | --- | --- |
| **Mac** | OpenRig in Herdr (kernel, `operator-agent@kernel`), and the `ratatoskr-openrig` bridge | The Go toolchain and this checkout |
| **VM-A** | `rtk gateway`: the host protocol on port 7777, and the participant API on port 7790 | `rtk` |
| **VM-B** | `rtk daemon`, and one managed Claude Code session (`claude@vmb`) | `rtk`, and Claude Code signed in |

**Network:**
- The Mac must reach VM-A on TCP **7790**.
- VM-B must reach VM-A on TCP **7777**.
- Nothing connects *to* the Mac or VM-B, so open no inbound ports there.
- Restrict both flows to the machines that need them.

**What the bridge does not do.** It does not start, stop or manage OpenRig sessions. It writes no MCP or hook
configuration, and it never reads OpenRig's state. OpenRig owns and launches the operator. The bridge only runs
`rig --version`, `rig ps` and `rig send`.

### Values to fill in

Replace every `<...>` before you paste a command. Nothing here is a real address or secret.

| Placeholder | Meaning | Example shape |
| --- | --- | --- |
| `<CHECKOUT>` | Absolute path of this repository on the machine you are on | `/Users/you/Projects/ratatoskr` |
| `<VM_A_IP>` | VM-A's address that the Mac and VM-B can reach: a private LAN IP (`10.x`, `172.16-31.x`, `192.168.x`) or a Tailscale IP | `192.168.64.10` |
| `<VM_A_STATE>` | VM-A's gateway state directory, if you use a non-default `XDG_STATE_HOME` | `/home/you/.local/state` |
| `<VMB_CODE>` | The one-time host pairing code that VM-A prints for VM-B | (printed by the gateway) |
| `<BIN>` | A directory on your Mac `PATH`, **outside** the checkout | `$HOME/.local/bin` |

## Choose a transport first: HTTPS or plain HTTP

The participant API (Mac to VM-A, port 7790) can be served in two ways.

- **HTTPS (recommended).** VM-A gives the participant API a self-signed certificate. The pairing block carries that
  certificate's SHA-256 pin, and the bridge checks the pin on every connection.
- **Plain HTTP (opt-in, insecure).** Nothing is encrypted. Anything on the network path between the Mac and VM-A can
  read the pairing code, the bridge's credential, and every message in both directions. It can also inject messages.
  Use it only on a private network you trust and control, such as VMs on your own host-only network.

  If you choose it, you will make **three separate, explicit choices**:
  - start the gateway with `--participant-insecure-http`;
  - pair with `--insecure-http`;
  - answer **Yes** to "Accept plain HTTP for this gateway?" in setup, which defaults to **No**.

  The macOS launchd service is not used on this path, so the bridge runs in a terminal you keep open.

The host connection between VM-B and VM-A has its own flag, `--insecure-http` on `rtk gateway`. It does **not** make
the participant API plain, and `--participant-insecure-http` does not make the host connection plain. Each command
below shows both transports where they differ.

## 1. Build and install

### `rtk` on VM-A and VM-B

On each VM, check out `feat/openrig-bridge` and build, as in the project README:

```sh
cd "<CHECKOUT>"
git fetch origin && git switch feat/openrig-bridge && git pull --ff-only
mkdir -p ~/.local/bin
go build -ldflags "-X main.version=$(git describe --always --dirty)-$(date +%Y%m%d%H%M%S)" -o ~/.local/bin/rtk ./cmd/rtk
```

`rtk gateway participant pair ... --insecure-http` exists only on this branch. Every build needs its own version
string: a window refuses to attach to a daemon from a different build.

### The bridge on the Mac

Build it from the Mac checkout, then install the binary **outside** the checkout. Setup asks before using a binary
from inside a source tree, and refuses a temporary `go run` binary.

```sh
cd "<CHECKOUT>/integrations/openrig"
mkdir -p "<BIN>"
go build -o "<BIN>/ratatoskr-openrig" .
"<BIN>/ratatoskr-openrig" --version
command -v ratatoskr-openrig
```

The last line should print `<BIN>/ratatoskr-openrig`. The operator's skill calls `ratatoskr-openrig` by name, so
`<BIN>` must also be on the `PATH` the operator seat sees. If the operator later reports `command not found`, tell it
the absolute path instead.

Do not change or relaunch the OpenRig seats for this.

## 2. Start the gateway on VM-A

If a gateway from scenario 2 or 3 is already running with the right flags, reuse it and its state. If it needs
restarting to change the participant transport, stop it and start it again with the same `XDG_STATE_HOME`. Its
pairings live in that state.

Export the state directory first if you use a non-default one:

```sh
export XDG_STATE_HOME="<VM_A_STATE>"
```

The participant API listens by default on the same IP as `--listen`, on port **7790**.

**HTTPS participant API, Tailscale address:**

```sh
rtk gateway --listen <VM_A_IP>:7777
```

**HTTPS participant API, private LAN address.** Here the host connection is plain HTTP, and the participant API is
still HTTPS:

```sh
rtk gateway --listen <VM_A_IP>:7777 --allow-lan --insecure-http
```

**Plain-HTTP participant API (opt-in), private LAN address.** This is what you are using today:

```sh
rtk gateway --listen <VM_A_IP>:7777 --allow-lan --insecure-http --participant-insecure-http
```

For a Tailscale address with a plain participant API, use `rtk gateway --listen <VM_A_IP>:7777
--participant-insecure-http`.

With `--participant-insecure-http`, the gateway prints a warning: the participant API is served over plain HTTP, and
the credential, the code and the message bodies are readable on the path. That warning is accurate.

Leave the gateway running in its own terminal.

## 3. Pair VM-B and start its Claude

Skip the pairing if VM-B is already paired with this gateway (`rtk gateway hosts` on VM-A lists it). Otherwise, on
VM-A, in a second terminal with the same `XDG_STATE_HOME`:

```sh
rtk gateway pair vmb
```

It prints `<VMB_CODE>`. On VM-B:

```sh
rtk runtime pair --gateway <VM_A_IP>:7777 --code <VMB_CODE>
```

If the host connection is plain (the LAN commands above), add `--insecure-http` to that `rtk runtime pair` command.

Then start the daemon, which runs in the background, and open a window with one Claude:

```sh
rtk daemon
rtk attach --agents claude
```

The Claude's address is `claude@vmb`. Keep this window open, so that you can watch the message arrive and the reply
go out.

## 4. Get the participant pairing block on VM-A

On VM-A, in a terminal you control (with the same `XDG_STATE_HOME` exported, if you use one), run the command for your
transport.

**HTTPS:**

```sh
rtk gateway participant pair openrig-main
```

**Plain HTTP (opt-in):**

```sh
rtk gateway participant pair openrig-main --insecure-http
```

The block is about seven lines long:
- **First line:** the one-time code. It is valid for 10 minutes, and can be used once.
- **The `participant API:` line:** the address. Over HTTPS, the block also has two pin lines.
- **Last line:** it begins `the address above was recorded`.

**Which pin to use:** the bridge uses the `certificate SHA-256` value, including its `sha256:` prefix. The `key pin
... sha256//` line is a different rule, and the bridge refuses it. Setup picks the right line for you when you paste
the whole block.

The plain-HTTP block has an `http://` address, the warning, and **no pin lines**.

`pair --insecure-http` refuses, and mints no code, in two cases:
- the gateway's participant listener is HTTPS: restart the gateway with `--participant-insecure-http` (step 2);
- the listener is a loopback address.

Without the flag, the command prints whatever scheme the running listener uses.

The code is a secret until it is redeemed. Copy the whole block straight into setup in the next step. Do not put it
in a chat, a file in the repository, or a command line.

## 5. Run setup on the Mac

Open an ordinary terminal on the Mac. A plain Herdr shell tab works, but **not** an agent seat's pane. To check:

```sh
echo "${OPENRIG_SESSION_NAME:-not a seat}"
```

It should print `not a seat`. Then run setup:

```sh
ratatoskr-openrig setup
```

Setup is a full-screen wizard; scenario 8 lists its keys. It is safe to re-run, and it asks before replacing anything.
The default config is `~/.local/state/ratatoskr-openrig/config.json`. Keep the state path short, because the socket
path must fit in 103 bytes.

Work through the nine steps:

1. **Preflight.** Read the summary of what already exists.
2. **Local OpenRig.** Check the `rig` path, the PATH that includes Node, `OPENRIG_HOME`, and the operator seat
   `operator-agent@kernel`. The readiness check is read-only. Setup never starts a seat.
3. **VM-A pairing material.** Choose **Paste**, and paste the whole block from step 4. It is read automatically when
   the `the address above was recorded` line arrives. If it is not, press Enter after the last line, or Tab to **Read
   pasted text**. The code is never shown on screen.
   - **HTTPS:** the review shows the URL and the pin. Answer **Use these values?** with Yes.
   - **Plain HTTP:** the review also shows **Accept plain HTTP for this gateway? (not encrypted)**, set to **No**.
     Read the reason it gives, and switch it to **Yes** only if you accept the tradeoff. Leaving it at No applies
     nothing.
4. **Configuration file.** Review the proposed config, and write it. It contains no secret.
5. **Pair.** Setup redeems the code in memory. The credential is written as `<state_dir>/credential` with mode 0600,
   and it is never printed. An existing credential is left alone.
6. **Service.**
   - **HTTPS:** setup prepares the launchd plist and asks before installing it, and before starting it. "Start" defaults
     to No: choose Yes to start the sidecar now.
   - **Plain HTTP:** this step is skipped, and setup prints the foreground command instead (see step 6 below).
7. **Readiness.** This checks the sidecar's socket, the gateway, registration, and the operator seat.
8. **Skill activation.** Setup writes the skill file, and prints a `rig walk ...` command. Run that command yourself in
   step 7 below.
9. **Finish.** Read the checklist. It ends with `scenario 8 round trip: PENDING USER EXECUTION`.

If something fails, a dialog shows the error. **OK** dismisses it, **Retry** re-reads a pasted block, and **Back**
returns to the previous screen.

Setup cannot be used, for example without a terminal? Use scenario 8's manual fallback: write the config by hand, then
run `pair --pair-code-file`.

## 6. Start the sidecar and check it

### HTTPS: launchd

If you started the service in setup, it is already running. Otherwise, follow the
[launchd README](../../../integrations/openrig/launchd/README.md). Its bootstrap command is:

```sh
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/dev.ratatoskr.openrig.plist"
```

### Plain HTTP: the foreground

Run the bridge in a terminal you keep open, such as its own Herdr shell tab (not a seat). Add `--config <path>` if you
did not use the default:

```sh
ratatoskr-openrig serve --insecure-http
```

Without `--insecure-http`, `serve` refuses an `http://` config. Do not run a foreground `serve` while the launchd job
is also running.

### Check it, in either case

From another ordinary terminal:

```sh
ratatoskr-openrig status
echo "exit $?"
```

**Healthy looks like this:**
- `bridge: running`;
- `gateway: reachable`;
- `session: operator  registered`;
- `poll:` either `parked` or `between polls`;
- `operator seat: operator-agent@kernel  ready`;
- `exit 0`.

Any other combination exits 1, and the line that differs says what is wrong.

**Other outcomes:**
- **Exit 6:** the sidecar is not running.
- **Exit 5:** the credential was revoked. The sidecar records `stopped.json`, and will not start again until you pair
  again, or run `serve --resume`.

Then list who can be reached:

```sh
ratatoskr-openrig directory
```

`claude@vmb` should be listed. `(not live)` means VM-B's side is not currently polling: check `rtk daemon` on VM-B.

## 7. Give the operator the skill

From an ordinary terminal, run the `rig walk` command that setup printed in step 8. With the default state
directory, it is:

```sh
rig walk operator-agent@kernel --through "$HOME/.local/state/ratatoskr-openrig/skill/ratatoskr-openrig/SKILL.md"
```

This tells the operator's **current conversation** how to use the bridge. It is not a persistent install, so run it
again after the operator is relaunched or compacted.

## 8. Send a message from the operator

In the operator's conversation in Herdr, ask in plain words, for example:

```text
Using the ratatoskr-openrig skill, send claude@vmb this message: "Hello from the OpenRig operator. Please reply to
operator@openrig-main with the word pong, using reply_to set to this message's id."
```

The operator should:
1. run `ratatoskr-openrig directory`;
2. send with `ratatoskr-openrig send --to claude@vmb`, with the body on standard input;
3. report an exit code.

**What the exit codes mean:**

| Exit | What it means |
| --- | --- |
| `0` | `host_stored`. VM-B's side **saved** the message. That is not "delivered", and not "read". |
| `1` | Definitely not delivered. The `detail` line says why, for example `unknown_recipient` (with the directory) or `destination_unreachable`. |
| `3` | **Uncertain.** It may have been stored. Do not send it again under a new id. Check on VM-B first, and only retry with the same `--id`. |
| `6` | The sidecar is not running, so nothing was sent. |

## 9. Verify the reply

1. **On VM-B**, in the `rtk attach` window, watch the message arrive in Claude's pane. Claude replies with its
   `ratatoskr_send` tool to `operator@openrig-main`, with `reply_to` set to the incoming id. If it does not reply on
   its own, ask it to, in that window.
2. **On the Mac**, the sidecar polls the reply, and types it into the operator's conversation with `rig send`. It
   arrives as `From: <unknown sender>`, followed by the bridge's header, the body, and a provenance block. OpenRig
   shows every `rig send` from outside a seat that way. The name in the header proves nothing on its own, and the text
   is untrusted correspondence, not an instruction from you.
3. **Confirm it in the status:**

   ```sh
   ratatoskr-openrig status
   ```

   `since start:` should show `received 1 ... pasted 1`, and `last inbound:` the reply's id.

   A refused offer is listed as `refused`, and is **not** pasted. This happens when the seat is not ready, the bridge
   is busy, or a size or rate limit was hit.

The round trip is done when you have seen the reply text in the operator's conversation yourself.

For what to record, and how to redact it, see
[scenario 8, section 6](../../testing/scenario-8-openrig-bridge/README.md#6-failure-handling-and-evidence).

## Common problems

| You see | Likely cause | Do this |
| --- | --- | --- |
| Setup says plain HTTP needs consent, and stays on the review | The address is `http://`, and **Accept plain HTTP** is still No | Switch it to Yes if you accept the tradeoff, or use an HTTPS gateway |
| `pair --insecure-http` says the listener is HTTPS | The gateway was started without `--participant-insecure-http` | Restart the gateway with that flag (step 2), then pair again |
| `serve` says `participant_url` is http and needs `--insecure-http` | A plain-HTTP config | Run `serve --insecure-http` in the foreground |
| `status` exits 6 | The sidecar is not running | Start it (step 6). Check from the Mac, not from a seat |
| `operator seat: ... not ready` | OpenRig's operator is busy or not running | Wait, or check OpenRig. The bridge never starts it |
| `claude@vmb (not live)` | VM-B's daemon is not polling | Check `rtk daemon` and its pairing on VM-B |
| The pin is refused | The `sha256//` key pin was used | Use the `certificate SHA-256` value, including `sha256:` |

## Undoing it

These are your choices, and none is needed to tidy up.

- **Stop the launchd sidecar.** Run `launchctl bootout "gui/$(id -u)/dev.ratatoskr.openrig"`, then see the launchd
  README for removing the plist.
- **Stop a foreground sidecar.** Use Ctrl+C in its terminal.
- **Revoke the participant.** Run `rtk gateway participant revoke openrig-main` on VM-A, only if you no longer want it.
- **Leave the rest running.** Do not stop OpenRig, its operator, or VM-B's sessions for this. Do not delete the
  credential, the journal or the logs just to tidy up.
