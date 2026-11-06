# ratatoskr

![Ratatoskr, drawn as a fierce gold beast in the style of Viking Age carving, with a drill-tooth tusk and a rune-stick message in its claws, encircled by the serpent Níðhöggr carved with its name in runes. Around the ring, a network links Claude Code, Codex, pi, OpenCode and other agents, along with app development, devops and infrastructure work.](assets/ratatoskr-banner.svg)

**Ratatoskr is an agent network.** Define agents built on the harnesses you already use, such as Claude Code, Codex, pi and OpenCode, and put them on a common network where they can discover each other and talk to each other. From there, you can spawn whole teams of agents to work together on a shared task.

> [!WARNING]
> Ratatoskr is at a very early stage and **its first version is largely unproven.** The project's owner has run it by hand on macOS, and reports that mouse click focus and Ctrl+C work and that Codex's automatic startup and messaging between Claude Code and Codex in both directions work. No automated test has been run, and recovery, Linux and the other acceptance checks haven't been verified, so don't assume the rest works. See [outstanding issues](docs/implementation/outstanding-issues.md). The product sections describe what the project is setting out to build; [Setup](#setup) covers the first version. [Cross-machine messaging](#cross-machine-messaging) is newly written and has never been run.

## Why

Coding agents are good at working alone, but a lot of real work calls for more than one of them: one writes the code, another reviews it, and a third deploys it. Today, each harness runs in its own silo, and you end up passing context between them by hand.

Ratatoskr gives them a shared network, so agents can work together directly, whichever harness each one runs on.

## Core ideas

This is where the project is heading. The first version covers only what is under [Setup](#setup).

- **Agents:** You define the agents you want and choose the harness each one runs on. A team can mix harnesses.
- **Network:** Every agent sits on the same network, regardless of its harness.
- **Discovery:** Agents can find out who else is on the network.
- **Messaging:** Agents talk to each other directly, without a person relaying messages between them.
- **Teams:** You can spawn a group of agents to work together on a common task.

## What teams can work on

| Area | Example |
| --- | --- |
| **App development** | Build a feature with agents working on the implementation, tests and code review. |
| **DevOps maintenance** | Keep pipelines and services healthy, with agents that investigate problems and hand off fixes. |
| **Infrastructure provisioning** | Have one agent draft the infrastructure changes while another reviews them. |

## Setup

> [!WARNING]
> The project's owner has checked mouse click focus and Ctrl+C by hand on macOS, and has checked Codex's automatic startup and messaging between Claude Code and Codex in both directions, also by hand on macOS. No automated test has run. Every other command and behaviour in this section is **unverified**: it is written from the design and from each harness's docs and `--help`. Expect corrections once testing starts.

The first version is one command, `rtk`. It is meant to open one terminal window with Claude Code and Codex side by side, so that they can message each other while you stay in control. It doesn't include discovery, teams or other harnesses. For agents on different machines, see [Cross-machine messaging](#cross-machine-messaging).

### Prerequisites

- macOS or Ubuntu. Windows is not supported.
- Go 1.27.3 or newer, to build from source.
- [Claude Code](https://code.claude.com/docs) and [Codex](https://learn.chatgpt.com/docs) on your `PATH`. The design was written against Claude Code 2.1.311 and Codex 0.166.0, and `rtk` doesn't check harness versions.
- A terminal at least wide enough for two usable panes.

### Build and install

From a checkout of this source:

```sh
mkdir -p ~/.local/bin
go build -o ~/.local/bin/rtk ./cmd/rtk
```

Make sure `~/.local/bin` is on your `PATH`. Rebuilding and moving the binary changes the hook command, so Codex may ask you to review its hooks again.

### Log in to each harness

`rtk` uses your existing logins and stores no credentials. Sign in to each one on its own first:

```sh
claude    # sign in when prompted, then exit
codex login
```

`rtk` passes its MCP and hook settings at launch and is designed not to change `~/.claude` or `~/.codex`.

### Run

From the project you want both agents to work in:

```sh
rtk
```

Both harnesses start as new sessions in that directory. Each one keeps its own permission prompts, and you answer them in its pane. Only the `ratatoskr_send` tool is pre-approved.

Either harness may ask its usual first-run questions, such as whether to trust the directory. Answer them in its pane.

Codex requires you to review and trust hooks before running them, so it may ask on first launch. Until they are trusted, `rtk` can't see Codex's state and holds messages to it. `rtk` doesn't use Codex's hook-trust bypass flag.

| Key | Action |
| --- | --- |
| F1 / F2, or click a pane | Focus the Claude Code / Codex pane |
| F3 | Toggle the message log |
| F4 | Deliver held messages to the focused agent when its state is `unknown` or it looks mid-draft. Never at a permission prompt or while it is busy. |
| F10 | Quit, with a warning if messages are waiting or a draft is open |
| Ctrl+C | Goes to the focused pane, so it still interrupts a turn. A second press within a second quits `rtk`, like F10. If nothing can receive it (the pane has exited or its input has failed, the terminal is too small, or the log is open), one press quits. |

Once a quit warning is showing, F10 or Ctrl+C confirms it and Esc cancels. All other keys go to the focused pane. This also means a double Ctrl+C can't exit just one harness from inside `rtk`.

The first click on an unfocused pane only moves focus. Other clicks and drags in the focused pane reach its harness if the harness has turned on mouse tracking, and the wheel goes to whichever pane is under the pointer without moving focus. The mouse does nothing in the log, at a quit warning, or when the terminal is too small.

Because `rtk` captures the mouse, selecting text needs your terminal's bypass modifier: usually hold Shift, or Option in some macOS terminals.

In the log, Left and Right step to older and newer runs, Up and Down select a message, and Enter opens its full body and state history. R reloads, and Esc goes back. The log is read-only.

### First use

Ask one agent to message the other, for example in the Claude Code pane:

```text
Ask Codex what files are in this directory, using ratatoskr_send.
```

Codex's first turn is automatic: `rtk` launches it with a short startup prompt, which you'll see in its pane, telling it which agents it can reach and asking for a one-line ready reply. That costs one extra Codex model turn on every launch, even if no message is ever sent. Messages sent during startup wait for that turn to end. Trust and permission prompts are still yours to answer, and the startup turn may wait behind them. If Codex shows `unknown`, F4 delivers held messages. After startup, messages appear in the Codex pane once Codex is idle, and the log shows their status. These statuses describe delivery only: `accepted` means the harness took the input, not that the model read or acted on it.

Agents can send at most 20 messages in a row. The budget resets when you press Enter in a pane.

### Troubleshooting

| Symptom | What to check |
| --- | --- |
| A pane shows a start error | Is that harness installed and on `PATH`? The other agent keeps running. |
| `no hook events` banner, messages held | Codex hooks may need review, and its automatic first turn may be waiting behind that prompt. Answer it in the Codex pane, or press F4 if Codex shows `unknown`. |
| An agent says it replied but the other got nothing | It may have used a built-in messaging tool, which doesn't reach its peer. Ask it to use `ratatoskr_send`, and check the log (F3). Whether Codex can call this tool is still unverified. |
| Agent stuck on `unknown` | A hook may not have fired, for example after an interrupt. Messages wait until the next hook event, or F4. |
| Messages held while the agent is idle | `rtk` thinks you have an unsent draft in that pane. It can't see the harness's input box, so Esc, Ctrl+C, deleting text, menu use and clicks passed to the harness all count. Press Enter to submit, or F4 to deliver. F4 pastes after any text in the input box and submits both. |
| Refuses to start: already running | Only one `rtk` can run per machine. |
| Refuses to start: database newer than this binary | Update `rtk`. The database is left untouched. |
| `storage unavailable` banner | A database write failed, for example on a full disk. Delivery stops until you restart `rtk`. |
| `rtk: stopping agents...` after quitting | Normal. `rtk` is giving each harness a few seconds to stop. If it says an agent didn't stop in time, check for a leftover process. |
| `INPUT UNAVAILABLE` on the feedback row | That pane can't take keys right now. A pane that exited or hit an emulator timeout or write error stays that way for the rest of the run, while a slow child can clear. The other agent keeps working. Ctrl+C or F10 still quits; unfinished messages are in the log (F3) next time. |
| `input error (may be partial)` | A keystroke or paste didn't fully reach the harness, usually because it stopped reading for 3 seconds. If later keys land in the input box as pasted text, the harness may still be waiting for the end of the paste. Check its input box before submitting. |
| Can't select text with the mouse | Hold your terminal's bypass modifier while dragging: usually Shift, or Option in some macOS terminals. |
| `did not exit cleanly` banner | Harness processes from the crashed run may still be running. `rtk` doesn't kill them; check and stop them yourself. |

### Data

Message bodies and delivery history are stored in `$XDG_STATE_HOME/ratatoskr/`, or `~/.local/state/ratatoskr/` by default. Bodies are **plain text and may contain anything an agent wrote, including secrets**; file permissions are the only protection, and backups of your home directory will include them. Runs that ended more than 30 days ago are deleted at startup. To erase everything, delete that directory while `rtk` isn't running.

### Known limitations

- **Barely tested:** the project's owner checked mouse click focus and Ctrl+C by hand on macOS. No automated test has been run, and nothing has been verified on Linux or over SSH. The full list is in [outstanding issues](docs/implementation/outstanding-issues.md).
- **Embedded panes are the biggest risk.** They use a young terminal emulator (`x/vt` v0.1.0) that has no Kitty or CSI-u keyboard support, so harnesses see a legacy terminal. Colours, wide characters, redraw speed and modified keys may misbehave. Shift+Enter is mapped to a newline fallback.
- **No scrollback in panes,** beyond what the harness itself redraws. Pane contents aren't saved.
- **`/clear` or `/compact` may leave an agent needing F4** for the rest of that run, if those commands change the harness's session ID. Unverified.
- **`rtk` can't see a harness's input box.** Any key except a plain Enter, and any click passed to the harness, counts as a draft, so messages can wait when nothing is typed. F4 delivers them, but it pastes after any text in the box and submits both.
- **Messaging between the agents isn't working reliably yet.** In the one recorded attempt, messages reached Codex late and its reply never reached Claude. See [outstanding issues](docs/implementation/outstanding-issues.md).
- **Pastes have limits.** A paste over 1 MiB is refused with a message. A slow paste can stop after 3 seconds with only part of it delivered.
- **Pasting is how messages arrive,** so they look like user turns. The message frame says they came from a peer.
- **No resume or resend.** Agent sessions end with `rtk` and a new run knows nothing about the last one. Unfinished messages are marked `interrupted` and never resent; read them in the log (F3) and pass them on yourself.
- **If an SSH connection drops,** `rtk` and both agents may end with it. Running `rtk` inside `tmux` or `screen` on the server is the suggested stopgap.
- **Not a security boundary.** Both agents run as you and could read each other's files or tokens. A message can't approve anything for the recipient, but it can still persuade its model, so its permission prompts remain the real check.

## Cross-machine messaging

> [!WARNING]
> **This has been written and compile-checked, but never run.** The team isn't allowed to run it, so your first run is its first test, and these instructions may need correcting. Plain `rtk` still has its own limits, described under [Setup](#setup).

Claude Code and Codex can run on different machines and message each other through a central gateway. The gateway keeps every message that reaches it, with its full text, so you can read them in one place. [ADR-0002](docs/adr/0002-cross-machine-messaging/index.html) explains why it works this way.

| Implemented, never run | Not implemented |
| --- | --- |
| `rtk gateway` and `rtk runtime`, pairing, routing, the message ledger | One window for several machines: each machine has its own terminal |
| Immediate failure when a machine can't be reached | Retries, or holding messages for a machine that's away |
| Choosing which agents a machine runs | Attaching to a Claude Code or Codex you started separately |
| A command-line view of the ledger | A web view, automatic deletion or multiple users |

### What runs where

| Command | Where | What it does |
| --- | --- | --- |
| `rtk gateway --listen ADDR` | Any one machine on your tailnet or, with [Direct LAN](#direct-lan-without-tailscale), your LAN. It can run without agents | Pairs machines, routes messages and stores them |
| `rtk runtime [--agents claude,codex]` | Each machine with agents, in a terminal you keep open | Runs the agents and pastes their messages, as `rtk` does. Default: both agents |

Every message goes through the gateway, including between two agents on one machine. Runtimes dial out to the gateway, so only the gateway needs a listening port. Permission prompts stay in the window of the machine running that agent, and the gateway can't answer them.

Plain `rtk` doesn't use the gateway, and its messages aren't in the ledger.

### Prerequisites

- Everything under [Prerequisites](#prerequisites), on each machine with agents, with each harness logged in.
- The same build of `rtk` on every machine. A gateway-only machine needs `rtk` but no harness.
- [Tailscale](https://tailscale.com/) on each machine, using a normal network interface, unless you use [Direct LAN](#direct-lan-without-tailscale). Userspace-networking mode isn't supported. Find the gateway's address with `tailscale ip -4`.
- Tailscale access between the machines on the port you choose. `rtk` doesn't change your tailnet's policy. Any device your policy allows can reach the port, though it can't do anything without pairing.
- For a server, run `rtk runtime` inside `tmux` or `screen`, because `rtk` and its agents may end if SSH drops.

### Set up

**1. On the gateway machine**, start the gateway and leave it running. Use its Tailscale address and a port you choose:

```sh
rtk gateway --listen 100.x.y.z:7777
```

**2. Still on the gateway machine, in another terminal,** create a one-time code for each machine with agents. The name you give becomes that machine's address. Use lowercase letters, digits and hyphens:

```sh
rtk gateway pair mac
rtk gateway pair ubuntu-b
```

Each code works once and expires after 10 minutes.

**3. On each machine with agents,** use its code:

```sh
rtk runtime pair --gateway 100.x.y.z:7777 --code XXXX-XXXX-XXXX
```

**4. In your project on each of those machines,** start the runtime:

```sh
rtk runtime --agents claude   # on the Mac
rtk runtime --agents codex    # on the server
```

A runtime exits without starting any agent if it isn't paired, or if it can't reach the gateway.

**5. Ask one agent to message the other.** Other machines' agents need the full address, and a bare name such as `codex` only means an agent on the same machine:

```text
Ask codex@ubuntu-b what files are in its directory, using ratatoskr_send.
```

### Direct LAN, without Tailscale

If your machines share a private network, a gateway can listen on its LAN address instead. This takes an explicit opt-in, and plain HTTP takes a second one. These examples use a gateway at `192.168.50.217`, so replace the port if you prefer another.

| You want | On the gateway machine | On each machine with agents |
| --- | --- | --- |
| **HTTPS** (recommended) | `rtk gateway --listen 192.168.50.217:7777 --allow-lan` | `rtk runtime pair --gateway 192.168.50.217:7777 --code CODE --fingerprint sha256:...` |
| **Plain HTTP** | `rtk gateway --listen 192.168.50.217:7777 --allow-lan --insecure-http` | `rtk runtime pair --gateway 192.168.50.217:7777 --code CODE --insecure-http` |

Install the same new build on every machine first, then:

1. Start the gateway with one of the commands above. With HTTPS, its first start creates a self-signed certificate in `gateway/tls/` and prints its fingerprint.
2. In another terminal on the gateway machine, run `rtk gateway pair mac`, using a name you choose for each machine. Its last line is the complete pairing command, with the address, code and right flags.
3. Run that line on the machine with agents. Then start `rtk runtime` as in [Set up](#set-up).

A machine that will also run agents alongside the gateway pairs the same way, with the same LAN address.

**What's allowed:** private addresses only: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`. Public addresses and `0.0.0.0` are always refused. `--allow-lan`, `--insecure-http` and `--fingerprint` are refused on Tailscale and loopback addresses, which work as they did before.

**Each machine has to choose HTTPS or plain HTTP itself,** because an attacker could fake an announcement from the gateway. The choice is saved in `pairing.json`, and nothing falls back from one to the other. To change it, quit the runtimes, restart the gateway with the new flags, then create a new code and pair each machine again before starting its runtime. A machine still paired for plain HTTP sends its credential unencrypted when it contacts a gateway that has moved to HTTPS, and the gateway only refuses it afterwards.

**What plain HTTP costs you:**

- **Anyone who can see the traffic can read it,** including every message, the pairing code and the credential. A stolen credential works until you run `rtk gateway unpair HOST`. Wi-Fi security such as WPA2 doesn't protect against other devices that know the network password.
- **Anyone who can redirect the traffic can pose as the gateway,** and inject messages that get pasted into your agents. Permission prompts still appear only on the agent's own machine, but anything that agent is already allowed to do is exposed.
- **A laptop keeps trying to reconnect.** On another network, it sends its credential to whatever answers at that address, so quit the runtime before you leave. Pinned HTTPS doesn't have this problem, because the certificate check fails first.

**In both modes:**

- **Every device on the LAN can reach the port.** It can't do anything without pairing, but a device could hold a connection open with a slow request. `rtk` doesn't configure a firewall, so if a runtime can't connect, check that the gateway machine allows incoming TCP on your port.
- **Hostnames aren't accepted,** so give the gateway a fixed address with a router DHCP reservation. If its address changes, start it on the new address and pair each machine again. The fingerprint stays the same, so don't regenerate the certificate.
- **If the certificate doesn't match,** the runtime refuses to connect. Check the fingerprint printed by the gateway before pairing again, because the certificate may have been regenerated, or another machine may have answered at that address.
- **If a first start is interrupted,** the gateway may refuse to start and name a leftover key file to remove. Remove that file and start again.

### Read the messages

Run these on the gateway machine. They read its ledger directly, and they print message text, which may include secrets.

| Command | Shows |
| --- | --- |
| `rtk gateway log [--follow] [--limit N]` | Recent messages, and new ones with `--follow` |
| `rtk gateway show ID` | One message in full, with its history |
| `rtk gateway hosts` | Paired machines and their state, up to 15 seconds out of date |
| `rtk gateway unpair HOST` | Removes a machine. Requests are refused at once, and its open connection closes within about 5 seconds |

In a runtime's TUI, F3 opens received messages, and Tab switches to what this machine sent in the current run. The title shows the gateway state, such as `Gateway connected`.

### What each result means

The agent sees this result when it sends, and you see it in the TUI and the ledger.

| Result | Meaning | Could it still arrive? | In the ledger? |
| --- | --- | --- | --- |
| `host_stored` | The recipient's machine has it. It may wait there until the agent can take it | Normal path | Yes |
| `gateway_unreachable` | There was no connection to the gateway, or it refused the request | No | No, this machine only |
| `gateway_uncertain` | Sent, but the gateway didn't answer in time | **Yes** | Possibly |
| `unknown_recipient` | No agent by that name has been seen. The result lists who is known | No | Yes |
| `destination_unreachable` | That machine isn't connected. It won't be delivered when the machine returns | No | Yes, with the text |
| `session_gone` | That agent's session has ended, or its machine refused the message. A new session doesn't get old messages | No | Yes, with the text |
| `delivery_uncertain` | Offered, with no confirmation in time. A later report can correct this | **Yes** | Yes |

Nothing is resent automatically. If an agent sends again after an uncertain result, that's a new message, and the recipient can get both. `accepted` still means only that the harness took the input, not that the model read it or acted on it.

### Test it

Please run these in order, because each one relies on the one before. Tell the team what you see, especially anything that differs. **Expected results come from reading the code.**

**Before you start:** the first run of this build updates your existing message database, and an older `rtk` then refuses it. It leaves a backup beside the database, named `ratatoskr.db.bak-v1-<time>`. Keep your old binary and that backup if you want to be able to go back.

#### A. Plain `rtk` hasn't changed

1. Build and install, then run `rtk` in a project. Claude Code and Codex should open side by side, with no gateway label in the title.
2. Run the [First use](#first-use) message and a reply. Check F3 and Ctrl+C.

This is the most important check, because everything else is new, and this already worked for you.

#### B. Everything on one machine

Use this to separate `rtk` problems from network problems.

1. Run `rtk gateway --listen 127.0.0.1:7777`.
2. In a second terminal, run `rtk gateway pair local`. Then run `rtk runtime pair --gateway 127.0.0.1:7777 --code ...`.
3. Run `rtk runtime` in a project. The title should say `Gateway connected`, and the agents should appear as `claude@local` and `codex@local`.
4. Have the agents message each other. Each message should appear in `rtk gateway log`, with its full text in `rtk gateway show ID`.
5. Stop the gateway with Ctrl+C. The title should say `Gateway reconnecting — sends unavailable`, and a send should return `gateway_unreachable`, even between these two agents. Nothing appears in the log for it.
6. Start the gateway again. The title should return to `Gateway connected`, and the failed message shouldn't arrive.

#### C. Across machines

Set up as above, with the gateway on any machine and an agent on two machines. A third machine for the gateway is the fullest test.

1. Have Claude on one machine message `codex@<host>` on another, and ask it to reply. Check both TUIs and the gateway's log.
2. Confirm that a permission prompt appears only on that agent's own machine.
3. Run `rtk gateway hosts`. Both machines should show `live`.

#### D. Failure cases

| # | Do this | Expect |
| --- | --- | --- |
| 1 | Quit the recipient's runtime with F10, then send to it | `destination_unreachable`, with the text in the gateway's log |
| 2 | Start that runtime again | The earlier message doesn't arrive |
| 3 | Restart the recipient with only the other agent, such as `--agents claude`, then send to `codex@<host>` | `session_gone` |
| 4 | Send to a name that's never existed, such as `nobody@<host>` | `unknown_recipient`, and a list of known agents |
| 5 | Close the recipient's laptop or cut its network, then send straight away | `delivery_uncertain` for up to about 20 seconds, as the gateway hasn't noticed yet |
| 6 | Keep it away, wait 20 seconds and send again | `destination_unreachable` |
| 7 | Bring it back | The runtime reconnects. Nothing from step 6 is delivered. See whether a message from step 5 turned up |
| 8 | Send to an agent while it's busy, or while you're typing in its pane | `host_stored`, then `held` on its machine, then pasted when safe. This isn't a failure |
| 9 | Run `rtk gateway unpair <host>` while that runtime is open | Within seconds its title says `Gateway refused — retrying`, and its sends fail with the reason |
| 10 | Start `rtk runtime` with the gateway stopped | Exits with `cannot start`, with no agent started |
| 11 | Run `rtk gateway --listen 0.0.0.0:7777`, then `--listen 100.x.y.z:0` | Both refused with a clear error |
| 12 | Run `rtk` while `rtk runtime` is open under the same state directory | `another ratatoskr is already running` |

Also worth watching in step C: whether agents actually use `ratatoskr_send` with `name@host`, and whether the timeouts feel right. All of them are guesses.

#### E. Automated tests

None of these have ever run, so expect some to fail on the tests themselves. From a checkout:

```sh
go test -race ./...
go test -race -tags rtktest ./internal/router ./internal/store
```

Use `-race`, because some router tests read state that's owned by another goroutine, and a report there may be a bug in the test.

#### F. Direct LAN

Run this after C works, or instead of it if you aren't using Tailscale. Use your gateway's address in place of `192.168.50.217`.

| # | Do this | Expect |
| --- | --- | --- |
| 1 | Run `rtk gateway --listen 192.168.50.217:7777` | Refused, naming `--allow-lan` |
| 2 | Add only `--insecure-http` | Refused, saying it also needs `--allow-lan` |
| 3 | Run `rtk gateway --listen 127.0.0.1:7777 --allow-lan` | Refused, because the flag applies only to a LAN address |
| 4 | Run `rtk gateway --listen 192.168.50.217:7777 --allow-lan`, stop it, then start it again | Prints `transport: HTTPS...`, with the same fingerprint both times |
| 5 | Run `rtk gateway pair mac`, then run its pairing command without `--fingerprint` | Refused, showing both forms |
| 6 | Run it with a wrong fingerprint, changing one digit | Refused with `does not match the pinned fingerprint` |
| 7 | Run it exactly as printed, with the same code | Paired, showing `certificate pinned`. Step 6 shouldn't have used up the code, because nothing is sent before the check |
| 8 | Start `rtk runtime`, and have agents message each other across machines | Title says `Gateway connected`, and messages appear in `rtk gateway log` |
| 9 | Leave it idle for several minutes, then send again | Still connected, and the message arrives. This tests the long-lived connection over TLS |
| 10 | Stop the gateway, run `mv` on its `gateway/tls` directory to set it aside, and start it again | A new fingerprint, and the running runtime doesn't reconnect. Its title stays `Gateway reconnecting — sends unavailable`, with the mismatch reason, and a send returns `gateway_unreachable` |
| 11 | Stop the gateway, delete the new `gateway/tls`, move the original back, and start it again | The original fingerprint, and the runtime reconnects without pairing again |
| 12 | Stop the runtime, then restart the gateway with `--allow-lan --insecure-http`, and start the runtime | Exits with `cannot start`, saying the gateway didn't answer with TLS |
| 13 | Run `rtk gateway pair mac`, and run its pairing command | It has `--insecure-http`, and pairing prints the warning |
| 14 | Start `rtk runtime`, and have agents message each other | Title says `Gateway connected (plain HTTP)`, and messages arrive |

Tell the team if a command or message differs from this, especially step 7, or any plain HTTP traffic in HTTPS mode.

### Cross-machine data and limits

- **Files:** the gateway keeps its ledger in `gateway/gateway.db` inside the state directory shown under [Data](#data). With HTTPS, its certificate and private key are in `gateway/tls/`. A paired machine keeps its credential in `pairing.json` there, readable only by you.
- **The gateway never deletes messages automatically,** and full message text can include secrets. Its disk use grows until a retention policy exists. The 30-day limit applies only to each machine's local database. To erase the ledger, stop the gateway and delete its `gateway` directory.
- **A message can be on three machines:** the sender, the gateway and the recipient.
- **A compromised gateway** can read stored messages and inject prompts into connected agents. It can't answer permission prompts or bypass a machine's delivery gate.
- **The address check is only an address check.** Without `--allow-lan`, `--listen` accepts Tailscale's address ranges and loopback, which doesn't prove that Tailscale is what's listening. On those addresses `rtk` uses plain HTTP, so encryption depends on that being Tailscale.
- **Agents can read the credential.** It's never given to them, but they run as you, so one with file access could read `pairing.json`.
- **`--code` appears in your shell history and process list** until the code is used or expires.
- **A failure after a send returns** reaches you and the ledger, but not the sending model.
- **The ledger may miss a final state.** If a runtime quits before reporting `interrupted`, its log entry stays at the last reported state, marked `(session ended)`.
- **One runtime per state directory,** and plain `rtk` counts as one. The loop budget of 20 is per runtime, and resets when you press Enter on that machine.

## Architecture decisions

Key decisions are kept in [`docs/adr/`](docs/adr/) as Architecture Decision Records (ADRs). Each one says what was chosen, why, what was rejected and what it costs.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](docs/adr/0001-mcp-agent-messaging/index.html) | Agents send through an MCP tool and receive through a gated paste | Accepted |
| [0002](docs/adr/0002-cross-machine-messaging/index.html) | Agents on different machines message through a central gateway on your tailnet or, by opt-in, your LAN, with pairing, full message storage and immediate failure when a machine is unreachable. Written and compile-checked, never run | Accepted |

GitHub shows `.html` files as source, so open a record from a local checkout to use its diagrams.

Every record follows the same rules:

- **One folder, one file.** Each decision has a numbered folder, such as `docs/adr/0001-mcp-agent-messaging/`, holding a single `index.html` with its CSS and JavaScript embedded. It uses no separate assets, build step or CDN.
- **Interactive diagrams.** Diagrams are drawn in HTML and CSS, and the reader can select and step through them. JavaScript only handles the interaction, so the content still reads with it off.
- **Scenarios and data flows.** Concepts are explained by walking through what actually happens, including when things go wrong.
- **Clear sourcing.** A record separates what the user decided from the architect's reasoning, and what was seen running from what was only read in the code. It gives its status, date and the commit it inspected.
- **Numbers stay fixed.** To change a decision, add a new record that replaces the old one. Don't rewrite history.

## The name

In Norse mythology, [Ratatoskr](https://en.wikipedia.org/wiki/Ratatoskr) is the squirrel that runs up and down the world tree Yggdrasil, carrying messages between the eagle perched at its top and the dragon Níðhöggr beneath its roots. The name is usually glossed as "drill-tooth".

This project does the same job for your agents, minus one detail: the Prose Edda says the original spread slanderous gossip to provoke the eagle and the dragon. Ours is meant to keep agents working together.
