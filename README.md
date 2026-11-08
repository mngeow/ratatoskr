# ratatoskr

![Ratatoskr, drawn as a fierce gold beast in the style of Viking Age carving, with a drill-tooth tusk and a rune-stick message in its claws, encircled by the serpent Níðhöggr carved with its name in runes. Around the ring, a network links Claude Code, Codex, pi, OpenCode and other agents, along with app development, devops and infrastructure work.](assets/ratatoskr-banner.svg)

**Ratatoskr is an agent network.** Define agents built on the harnesses you already use, such as Claude Code, Codex, pi and OpenCode, and put them on a common network where they can discover each other and talk to each other. From there, you can spawn whole teams of agents to work together on a shared task.

> [!WARNING]
> Ratatoskr is at a very early stage and **its first version is largely unproven.** The project's owner has run it by hand on macOS, and reports that mouse click focus and Ctrl+C work and that Codex's automatic startup and messaging between Claude Code and Codex in both directions work. No automated test has been run, and recovery, Linux and the other acceptance checks haven't been verified, so don't assume the rest works. See [outstanding issues](docs/implementation/outstanding-issues.md). The product sections describe what the project is setting out to build; [Setup](#setup) covers the first version. [Cross-machine messaging](#cross-machine-messaging) is new, and the owner reports that it worked in their own testing on 2026-11-06. That test used the earlier `rtk runtime`. [The daemon](#the-daemon-and-its-windows) has since replaced it, and the owner reports that it works too, with one [open bug](#the-daemon-and-its-windows).

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
go build -ldflags "-X main.version=$(git describe --always --dirty)-$(date +%Y%m%d%H%M%S)" -o ~/.local/bin/rtk ./cmd/rtk
```

The version makes each build identifiable, which matters for the [daemon](#the-daemon-and-its-windows): it refuses a window from a different build, and builds without a version are all called `dev`, so it can't tell them apart.

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

- **Lightly tested:** the project's owner has checked mouse click focus, Ctrl+C, Codex's automatic startup and messaging in both directions by hand on macOS. No automated test has been run, and plain `rtk` hasn't been confirmed on Linux or over SSH. The full list is in [outstanding issues](docs/implementation/outstanding-issues.md).
- **Embedded panes are the biggest risk.** They use a young terminal emulator (`x/vt` v0.1.0) that has no Kitty or CSI-u keyboard support, so harnesses see a legacy terminal. Colours, wide characters, redraw speed and modified keys may misbehave. Shift+Enter is mapped to a newline fallback.
- **No scrollback in panes,** beyond what the harness itself redraws. Pane contents aren't saved.
- **`/clear` or `/compact` may leave an agent needing F4** for the rest of that run, if those commands change the harness's session ID. Unverified.
- **`rtk` can't see a harness's input box.** Any key except a plain Enter, and any click passed to the harness, counts as a draft, so messages can wait when nothing is typed. F4 delivers them, but it pastes after any text in the box and submits both.
- **Messaging has only been checked by hand.** An early run on 2026-11-04 failed, with messages reaching Codex late and no reply reaching Claude. The owner reports that it worked in later runs on macOS. See [outstanding issues](docs/implementation/outstanding-issues.md).
- **Pastes have limits.** A paste over 1 MiB is refused with a message. A slow paste can stop after 3 seconds with only part of it delivered.
- **Pasting is how messages arrive,** so they look like user turns. The message frame says they came from a peer.
- **No resume or resend.** Agent sessions end with `rtk` and a new run knows nothing about the last one. Unfinished messages are marked `interrupted` and never resent; read them in the log (F3) and pass them on yourself.
- **If an SSH connection drops,** plain `rtk` and both agents may end with it. Running `rtk` inside `tmux` or `screen` on the server is the suggested stopgap. [The daemon](#the-daemon-and-its-windows) is designed to survive this, but it needs a gateway.
- **Not a security boundary.** Both agents run as you and could read each other's files or tokens. A message can't approve anything for the recipient, but it can still persuade its model, so its permission prompts remain the real check.

## Cross-machine messaging

> [!WARNING]
> **The project's owner tested this by hand on 2026-11-06, and reports:** "Ok, I tested the cross-machine functionality of ratatoskr and it all works as expected." That's the only run so far, and the report doesn't say which setup, transport or failure cases it covered, so don't assume any particular one has been checked. No automated test has run, and the team hasn't run it. Plain `rtk` still has its own limits, described under [Setup](#setup).
>
> **That report doesn't cover what's described below.** It ran the earlier in-process `rtk runtime`. [The daemon](#the-daemon-and-its-windows) has replaced that and changed how sessions register with the gateway. The owner has since tested the daemon too, as that section describes.

Claude Code and Codex can run on different machines and message each other through a central gateway. The gateway keeps every message that reaches it, with its full text, so you can read them in one place. [ADR-0002](docs/adr/0002-cross-machine-messaging/index.html) explains why it works this way.

| Implemented | Not implemented |
| --- | --- |
| `rtk gateway`, `rtk daemon` and `rtk attach`, pairing, routing, the message ledger | One window for several machines: each machine has its own windows |
| Immediate failure when a machine can't be reached | Retries, or holding messages for a machine that's away |
| Choosing which agents a machine runs | Attaching to a Claude Code or Codex you started separately |
| A command-line view of the ledger | A web view, automatic deletion or multiple users |
| Agents you start from a window and leave running | Parents spawning workers, which [ADR-0003](docs/adr/0003-per-host-daemon/index.html) proposes |

### What runs where

| Command | Where | What it does |
| --- | --- | --- |
| `rtk gateway --listen ADDR` | Any one machine on your tailnet or, with [Direct LAN](#direct-lan-without-tailscale), your LAN. It can run without agents | Pairs machines, routes messages and stores them |
| `rtk daemon` | Each machine with agents, in the background | Runs the agents and pastes their messages, with or without a window open |
| `rtk attach [--agents claude,codex]` | Each machine with agents, whenever you want to see them | Opens a window on the daemon's agents, starting any you list that aren't running. `rtk runtime` is an alias for it |

Every message goes through the gateway, including between two agents on one machine. Daemons dial out to the gateway, so only the gateway needs a listening port. Permission prompts stay on the machine running that agent, and the gateway can't answer them.

Plain `rtk` doesn't use the gateway, and its messages aren't in the ledger.

### Prerequisites

- Everything under [Prerequisites](#prerequisites), on each machine with agents, with each harness logged in.
- `rtk` built from the same source on every machine, including the gateway's. Machines only have to agree on gateway protocol 2, and the build must match exactly only between a window and its own machine's daemon. A gateway-only machine needs `rtk` but no harness.
- [Tailscale](https://tailscale.com/) on each machine, using a normal network interface, unless you use [Direct LAN](#direct-lan-without-tailscale). Userspace-networking mode isn't supported. Find the gateway's address with `tailscale ip -4`.
- Tailscale access between the machines on the port you choose. `rtk` doesn't change your tailnet's policy. Any device your policy allows can reach the port, though it can't do anything without pairing.

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

**4. On each of those machines,** start the daemon, then open a window from your project and start its agents:

```sh
rtk daemon
rtk daemon status
```

Check that `link:` says connected. Then, on the Mac:

```sh
rtk attach --agents claude
```

And on the server:

```sh
rtk attach --agents codex
```

Each agent runs in the directory you ran `rtk attach` from. `rtk daemon` exits with an error if the machine isn't paired. It starts without a gateway and keeps trying, but no agent can start until it has reached one: `rtk attach --agents` then says why, and starts nothing.

**5. Ask one agent to message the other.** Other machines' agents need the full address, and a bare name such as `codex` only means an agent on the same machine:

```text
Ask codex@ubuntu-b what files are in its directory, using ratatoskr_send.
```

### The daemon and its windows

> [!WARNING]
> **The project's owner tested this by hand on 2026-11-06, and reports:** "Ok I tested it and it all works! There is a small bug though: Whenever I type something in claude code / codex chat and get an answer, some text is auto pasted into the prompt box". **That bug is open, and its cause is unknown.** The report doesn't say which scenarios, machines or failure cases it covered, so don't assume any particular one has been checked. No automated test has run, and the team hasn't run it.

Each machine with agents runs one daemon, which owns the agents and their connection to the gateway. A window is only a view of them, so closing it doesn't stop anything. Starting the daemon is always a separate step: `rtk attach` and `rtk runtime` never start one.

| Command | What it does |
| --- | --- |
| `rtk daemon` | Starts this machine's daemon in the background, and prints its PID and log path |
| `rtk daemon --foreground` | The same, staying in this terminal |
| `rtk daemon status` | Shows its PID, build, uptime, log, gateway link, windows and agents |
| `rtk attach` | Opens a window on the agents already running. If none are, it says so and exits |
| `rtk attach --agents claude,codex` | The same, first starting any listed agent that isn't running, in this directory. Running agents stay where they started |
| `rtk runtime [--agents claude,codex]` | An alias for `rtk attach`. **This has changed:** without `--agents` it no longer starts both agents, and it needs a running daemon |
| `rtk daemon network [--json] [--watch]` | Prints what this machine and the gateway know about every host's sessions. See [The Network tab and SSH entry](#the-network-tab-and-ssh-entry) |
| `rtk attach --expect-session ID` | Attaches only the running session with that ID, and refuses before taking input otherwise. This is what SSH entry runs on the remote machine |
| `rtk daemon stop --session NAME` | Ends one agent |
| `rtk daemon stop` | Stops the daemon and every agent. With agents running, it lists them and refuses unless you add `--yes` |

**A window's keys.** Everything else works as in [Run](#run), including F1, F2, F3 and F4.

| Key | Action |
| --- | --- |
| F10 | Detaches at once. The agents keep running, so there's no waiting-messages warning |
| Ctrl+C | If this window holds input, the first press goes to the agent, and a second within a second detaches. **Otherwise the first press detaches.** That includes when you're a viewer, the agent has ended, the log is open or the terminal is too small |
| F6 | Takes input for the focused agent |

**Who can type.** One window at a time holds input for each agent. Each pane's header starts with `INPUT HOLDER`, `VIEWER` or `SESSION ENDED`.

- A window that attaches takes input for any agent nobody holds, and is a viewer for the rest. Viewers can change focus and read the log, but the daemon refuses their keys, paste and mouse input.
- F6 works straight away, with no request to the other window, which becomes a viewer and shows that it has.
- When a window closes, its input is released, and nobody gets it automatically.
- The holder's window sets the agent's terminal size. A viewer sees the same screen, cropped if it doesn't fit.
- Nothing answers a permission prompt except a key press from the holder.

**What happens when:**

| Event | The agent | Its messages |
| --- | --- | --- |
| A window closes, or SSH drops | Keeps running | Keep arriving, and are held or pasted as usual |
| It exits, or you run `rtk daemon stop --session NAME` | Ends, and the window shows it | New sends get `session_gone`. Unfinished ones become `interrupted` |
| You start it again with `rtk attach --agents NAME` | A **new** session, with no memory of the last one | It never gets an older message |
| `rtk daemon stop --yes` | Ends, as do all the others | New sends get `destination_unreachable`. Unfinished ones become `interrupted` |
| The daemon crashes | **Ends, or is left running with nothing managing it.** It doesn't resume | Unfinished ones become `interrupted` at the next start, and are never pasted again |
| The gateway is unreachable | Keeps running | Sends fail straight away. New agents can't start until it's back |

**Limits you should know about:**

- **The promise is only a closed window or a dropped SSH connection.** The daemon detaches itself but isn't a system service, so a reboot ends it and it doesn't restart by itself. Whether it survives logging out of macOS is unknown. `launchd` and `systemd` setup isn't included.
- **Agents get the environment of the terminal that started the daemon,** fixed when it starts. Started over SSH, they keep that session's `SSH_AUTH_SOCK` and `SSH_CONNECTION`, which stop being valid when it ends, so `git` over SSH can start failing with no clear reason. To change an agent's environment, stop the daemon and start it from the right terminal.
- **Codex may ask you to review its hooks once more.** The hook command names a fixed directory for each agent, so it should stay the same between daemon sessions, but it differs from plain `rtk`'s. A Codex used both ways may ask once for each. Until you trust them, Codex's hooks don't run and its messages are held. Whether this holds in practice hasn't been checked.
- **The daemon needs a gateway.** It won't start without a pairing, so plain `rtk` remains the only way to run without one. For one machine on its own, a [loopback gateway](#b-everything-on-one-machine) works.
- **One of each agent per machine,** because agents are named `claude` and `codex`. The daemon and plain `rtk` also exclude each other: `another ratatoskr is already running`.
- **A window shows the agents that exist when it attaches.** An agent started later needs a new window, as its title says.
- **Starting an agent straight after ending it can be refused.** The reason is shown, and a retry should work.
- **The daemon's own agents are refused its socket, but this isn't a security boundary.** The socket is only for your user, and it refuses a connection whose parent processes include an agent. A process that double-forks gets past that check, and could then type into any agent, answer its prompts or stop the daemon. Agents already run as you, so treat this as protection against accidents.
- **Time limits are guesses,** and `daemon.log` isn't rotated. It holds no message text, credentials or terminal contents.

**To upgrade,** move every machine together, because the gateway protocol is now version 2 and refuses version 1. Quit any old `rtk runtime` windows and stop any daemons, install the new build everywhere, and restart `rtk gateway` with the same flags. Existing pairings and the gateway's database carry over. After a later rebuild, a window refuses to open against the running daemon and says to run `rtk daemon stop`, which works across builds, and then start the daemon again.

### Direct LAN, without Tailscale

If your machines share a private network, a gateway can listen on its LAN address instead. This takes an explicit opt-in, and plain HTTP takes a second one. These examples use a gateway at `192.168.50.217`, so replace the port if you prefer another.

| You want | On the gateway machine | On each machine with agents |
| --- | --- | --- |
| **HTTPS** (recommended) | `rtk gateway --listen 192.168.50.217:7777 --allow-lan` | `rtk runtime pair --gateway 192.168.50.217:7777 --code CODE --fingerprint sha256:...` |
| **Plain HTTP** | `rtk gateway --listen 192.168.50.217:7777 --allow-lan --insecure-http` | `rtk runtime pair --gateway 192.168.50.217:7777 --code CODE --insecure-http` |

Install the same new build on every machine first, then:

1. Start the gateway with one of the commands above. With HTTPS, its first start creates a self-signed certificate in `gateway/tls/` and prints its fingerprint.
2. In another terminal on the gateway machine, run `rtk gateway pair mac`, using a name you choose for each machine. Its last line is the complete pairing command, with the address, code and right flags.
3. Run that line on the machine with agents. Then start the daemon and a window as in [Set up](#set-up).

A machine that will also run agents alongside the gateway pairs the same way, with the same LAN address.

**What's allowed:** private addresses only: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` and `fc00::/7`. Public addresses and `0.0.0.0` are always refused. `--allow-lan`, `--insecure-http` and `--fingerprint` are refused on Tailscale and loopback addresses, which work as they did before.

**Each machine has to choose HTTPS or plain HTTP itself,** because an attacker could fake an announcement from the gateway. The choice is saved in `pairing.json`, and nothing falls back from one to the other. To change it, stop the daemons, restart the gateway with the new flags, then create a new code and pair each machine again before starting its daemon. A machine still paired for plain HTTP sends its credential unencrypted when it contacts a gateway that has moved to HTTPS, and the gateway only refuses it afterwards.

**What plain HTTP costs you:**

- **Anyone who can see the traffic can read it,** including every message, the pairing code and the credential. A stolen credential works until you run `rtk gateway unpair HOST`. Wi-Fi security such as WPA2 doesn't protect against other devices that know the network password.
- **Anyone who can redirect the traffic can pose as the gateway,** and inject messages that get pasted into your agents. Permission prompts still appear only on the agent's own machine, but anything that agent is already allowed to do is exposed.
- **A laptop keeps trying to reconnect.** On another network, it sends its credential to whatever answers at that address, so stop the daemon before you leave. Pinned HTTPS doesn't have this problem, because the certificate check fails first.

**In both modes:**

- **Every device on the LAN can reach the port.** It can't do anything without pairing, but a device could hold a connection open with a slow request. `rtk` doesn't configure a firewall, so if a daemon can't connect, check that the gateway machine allows incoming TCP on your port.
- **Hostnames aren't accepted,** so give the gateway a fixed address with a router DHCP reservation. If its address changes, start it on the new address and pair each machine again. The fingerprint stays the same, so don't regenerate the certificate.
- **If the certificate doesn't match,** the daemon refuses to connect. Check the fingerprint printed by the gateway before pairing again, because the certificate may have been regenerated, or another machine may have answered at that address.
- **If a first start is interrupted,** the gateway may refuse to start and name a leftover key file to remove. Remove that file and start again.

### Read the messages

Run these on the gateway machine. They read its ledger directly, and they print message text, which may include secrets.

| Command | Shows |
| --- | --- |
| `rtk gateway log [--follow] [--limit N]` | Recent messages, and new ones with `--follow` |
| `rtk gateway show ID` | One message in full, with its history |
| `rtk gateway hosts` | Paired machines and their state, up to 15 seconds out of date |
| `rtk gateway unpair HOST` | Removes a machine. Requests are refused at once, and its open connection closes within about 5 seconds |

In a daemon window, F3 opens received messages, and Tab switches to what this machine sent in the current run. The title shows the gateway state, such as `Gateway connected`.

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

> **For a step-by-step walkthrough, use the [daemon test guide](docs/testing/index.html).** Its expected results come from reading the code. It covers [one Mac](docs/testing/scenario-1-local/README.md), including several copies of each harness, a [LAN with plain HTTP](docs/testing/scenario-2-lan-http/README.md) and a [tailnet](docs/testing/scenario-3-tailnet/README.md), with diagrams and copy buttons. **`rtk` doesn't support HTTPS on a Tailscale address,** and the guide explains the alternatives. Open it from a local checkout, because GitHub shows `.html` as source.

Please run these in order, because each one relies on the one before. Tell the team what you see, especially anything that differs. **Expected results come from reading the code,** not from a record of each check passing.

**Before you start:**

- **Build again, on every machine.** A binary from before the daemon has no daemon commands.
- **Restart the gateway on the new build.** This work doesn't change its database.
- **If this machine's database predates cross-machine messaging,** the first run updates it, and an older `rtk` then refuses it. It leaves a backup beside the database, named `ratatoskr.db.bak-v1-<time>`. Keep your old binary and that backup if you want to be able to go back.

#### A. Plain `rtk` hasn't changed

1. Build and install, then run `rtk` in a project. Claude Code and Codex should open side by side, with no gateway label in the title.
2. Run the [First use](#first-use) message and a reply. Check F3 and Ctrl+C.

3. Keep it open and, in a second terminal, try `rtk daemon` if this machine is paired, or `rtk daemon status` if not. `rtk daemon` should fail, naming `another ratatoskr is already running` and the log. `rtk daemon status` should say `no daemon is running`.

This is the most important check, because everything else is new, and this already worked for you.

#### B. Everything on one machine

Use this to separate `rtk` problems from network problems.

1. Run `rtk gateway --listen 127.0.0.1:7777`.
2. In a second terminal, run `rtk gateway pair local`. Then run `rtk runtime pair --gateway 127.0.0.1:7777 --code ...`.
3. Run `rtk daemon`. It should print `daemon started (pid N)` and the log path. `rtk daemon status` should show the link as connected, and a second `rtk daemon` should say `a daemon is already running`. Read `daemon.log`.
4. In a project, run `rtk attach --agents claude,codex`. The title should say `Gateway connected`, and the agents should appear as `claude@local` and `codex@local`.
5. Have the agents message each other. Each message should appear in `rtk gateway log`, with its full text in `rtk gateway show ID`.
6. Stop the gateway with Ctrl+C. The title should say `Gateway reconnecting — sends unavailable`, and a send should return `gateway_unreachable`, even between these two agents. Nothing appears in the log for it.
7. Start the gateway again. The title should return to `Gateway connected`, and the failed message shouldn't arrive.

#### C. The daemon and its windows

This is the point of the work. Continue from B, with the gateway running.

| # | Do this | Expect |
| --- | --- | --- |
| 1 | Type in each pane. Press F10 | Typing reaches the agent. F10 prints `detached; the agents keep running (rtk attach returns to them, rtk daemon stop ends them)` |
| 2 | Run `rtk attach` | The same agents, on their current screens |
| 3 | **Close the terminal window.** In a new one, run `rtk daemon status`, then `rtk attach` | Both agents still listed, and you return to them |
| 4 | Open a second terminal and run `rtk attach` | Both headers say `VIEWER`. Typing doesn't reach the agent |
| 5 | Press F6 in the second window | It becomes `INPUT HOLDER`, and the first shows `VIEWER` |
| 6 | Press Ctrl+C once in the viewer pane of the first window | It detaches straight away |
| 7 | Close the holder window, then run `rtk daemon status` | The agents show `no holder` |
| 8 | Ask an agent to message the other while no window is open, then attach | The message arrived or is held, and F3 shows it |
| 9 | From inside an agent's pane, ask it to run `rtk daemon status` | Refused, because it comes from an agent this daemon started |
| 10 | Run `rtk daemon stop --session codex` | `session ended: codex`, and `claude` keeps running |
| 11 | Run `rtk attach --agents codex` | A new Codex session, which gets none of the earlier messages. **See whether Codex asks you to review its hooks.** It may ask once, and shouldn't ask on later starts |
| 12 | Run `rtk daemon stop` | Lists the agents and refuses |
| 13 | Run `rtk daemon stop --yes`, then plain `rtk` straight away | `daemon stopped` appears only after a few seconds at most, with no agents left running, and `rtk` opens. If it says `still finishing its shutdown`, tell the team |
| 14 | On a server, attach over SSH and then drop the connection. Reconnect, and run `rtk attach` | The agents are still there |
| 15 | Log out of macOS and back in, if you want to know, then run `rtk daemon status` | **Unknown.** The README doesn't promise this |

#### D. Across machines

Set up as above, with the gateway on any machine and an agent on two machines. A third machine for the gateway is the fullest test.

1. Have Claude on one machine message `codex@<host>` on another, and ask it to reply. Check both TUIs and the gateway's log.
2. Confirm that a permission prompt appears only on that agent's own machine.
3. Run `rtk gateway hosts`. Both machines should show `live`.

#### E. Failure cases

| # | Do this | Expect |
| --- | --- | --- |
| 1 | Run `rtk daemon stop --yes` on the recipient, then send to it | `destination_unreachable`, with the text in the gateway's log |
| 2 | Run `rtk daemon` and `rtk attach --agents ...` there | The earlier message doesn't arrive |
| 3 | Run `rtk daemon stop --session codex` on the recipient, then send to `codex@<host>` | `session_gone` |
| 4 | Send to a name that's never existed, such as `nobody@<host>` | `unknown_recipient`, and a list of known agents |
| 5 | Close the recipient's laptop or cut its network, then send straight away | `delivery_uncertain` for up to about 20 seconds, as the gateway hasn't noticed yet |
| 6 | Keep it away, wait 20 seconds and send again | `destination_unreachable` |
| 7 | Bring it back | The daemon reconnects. Nothing from step 6 is delivered. See whether a message from step 5 turned up |
| 8 | Send to an agent while it's busy, or while you're typing in its pane | `host_stored`, then `held` on its machine, then pasted when safe. This isn't a failure |
| 9 | Run `rtk gateway unpair <host>` with a window open on that machine | Within seconds its title says `Gateway refused — retrying`, and its sends fail with the reason |
| 10 | Stop the daemon and the gateway. Run `rtk daemon`, then `rtk attach --agents claude` | The daemon starts. Attach prints `could not start claude`, with the reason, and no agent starts until the gateway is back |
| 11 | Run `rtk gateway --listen 0.0.0.0:7777`, then `--listen 100.x.y.z:0` | Both refused with a clear error |
| 12 | Run `rtk` while the daemon is running under the same state directory | `another ratatoskr is already running` |

Also worth watching in part D: whether agents actually use `ratatoskr_send` with `name@host`, and whether the timeouts feel right. All of them are guesses.

#### F. Automated tests

None of these have ever run, so expect some to fail on the tests themselves. From a checkout:

```sh
go test -race ./...
go test -race -tags rtktest ./internal/router ./internal/store
```

Use `-race`, because some router tests read state that's owned by another goroutine, and a report there may be a bug in the test.

#### G. Direct LAN

Run this after D works, or instead of it if you aren't using Tailscale. Use your gateway's address in place of `192.168.50.217`.

| # | Do this | Expect |
| --- | --- | --- |
| 1 | Run `rtk gateway --listen 192.168.50.217:7777` | Refused, naming `--allow-lan` |
| 2 | Add only `--insecure-http` | Refused, saying it also needs `--allow-lan` |
| 3 | Run `rtk gateway --listen 127.0.0.1:7777 --allow-lan` | Refused, because the flag applies only to a LAN address |
| 4 | Run `rtk gateway --listen 192.168.50.217:7777 --allow-lan`, stop it, then start it again | Prints `transport: HTTPS...`, with the same fingerprint both times |
| 5 | Run `rtk gateway pair mac`, then run its pairing command without `--fingerprint` | Refused, showing both forms |
| 6 | Run it with a wrong fingerprint, changing one digit | Refused with `does not match the pinned fingerprint` |
| 7 | Run it exactly as printed, with the same code | Paired, showing `certificate pinned`. Step 6 shouldn't have used up the code, because nothing is sent before the check |
| 8 | Run `rtk daemon` and `rtk attach --agents ...`, and have agents message each other across machines | Title says `Gateway connected`, and messages appear in `rtk gateway log` |
| 9 | Leave it idle for several minutes, then send again | Still connected, and the message arrives. This tests the long-lived connection over TLS |
| 10 | Stop the gateway, run `mv` on its `gateway/tls` directory to set it aside, and start it again | A new fingerprint, and the running daemon doesn't reconnect. Its window's title stays `Gateway reconnecting — sends unavailable`, with the mismatch reason, and a send returns `gateway_unreachable` |
| 11 | Stop the gateway, delete the new `gateway/tls`, move the original back, and start it again | The original fingerprint, and the daemon reconnects without pairing again |
| 12 | Run `rtk daemon stop --yes`, restart the gateway with `--allow-lan --insecure-http`, then run `rtk daemon` and `rtk attach --agents claude` | Attach prints `could not start claude`, with a reason about the gateway not answering with TLS |
| 13 | Run `rtk daemon stop --yes`, then `rtk gateway pair mac`, and run its pairing command | It has `--insecure-http`, and pairing prints the warning |
| 14 | Run `rtk daemon` and `rtk attach --agents ...`, and have agents message each other | Title says `Gateway connected (plain HTTP)`, and messages arrive |

Tell the team if a command or message differs from this, especially step 7, or any plain HTTP traffic in HTTPS mode.

### Cross-machine data and limits

- **Files:** each daemon keeps its socket and log in `daemon/` inside the state directory shown under [Data](#data). The gateway keeps its ledger in `gateway/gateway.db` there. With HTTPS, its certificate and private key are in `gateway/tls/`. A paired machine keeps its credential in `pairing.json` there, readable only by you.
- **The gateway never deletes messages automatically,** and full message text can include secrets. Its disk use grows until a retention policy exists. The 30-day limit applies only to each machine's local database. To erase the ledger, stop the gateway and delete its `gateway` directory.
- **A message can be on three machines:** the sender, the gateway and the recipient.
- **A compromised gateway** can read stored messages and inject prompts into connected agents. It can't answer permission prompts or bypass a machine's delivery gate.
- **The address check is only an address check.** Without `--allow-lan`, `--listen` accepts Tailscale's address ranges and loopback, which doesn't prove that Tailscale is what's listening. On those addresses `rtk` uses plain HTTP, so encryption depends on that being Tailscale.
- **Agents can read the credential.** It's never given to them, but they run as you, so one with file access could read `pairing.json`.
- **`--code` appears in your shell history and process list** until the code is used or expires.
- **A failure after a send returns** reaches you and the ledger, but not the sending model.
- **The ledger may miss a final state.** If a daemon stops before reporting `interrupted`, its log entry stays at the last reported state, marked `(session ended)`.
- **One daemon per state directory,** and plain `rtk` counts as one. The loop budget of 20 is per daemon, and resets when the window holding input presses Enter on that machine.

### The Network tab and SSH entry

> [!WARNING]
> **Written, never run.** This was implemented in the working tree on top of commit f411629 and checked only by `gofmt`, `go vet` and `go build` (darwin and linux). No test, `rtk`, gateway, daemon or `ssh` has been run for it, and nothing is committed or pushed. Expect defects. The tests that exist have never executed. Rebuild `rtk` before you try anything: any `./rtk` already in the repo predates the final code.

The Network tab is an extra view inside any attached window, not a dashboard. Every window shows a `[Terminals] [Network]` strip on its top row (`[T] [N]` when narrow). **Click Network** to open it and Terminals to go back; F5 is an optional shortcut and Esc also returns. You don't need `rtk daemon network` first: that command is only for inspecting or watching the data in a terminal. The window starts fetching as soon as it attaches (so a daemon with a window open keeps one long poll to the gateway running), and the tab first says `waiting for the first answer`. Clicks on the strip never reach an agent.

**How it looks (never drawn).** The top row is a dark bar with two touching tab blocks, ` Terminals ` and ` Network `, in a Monokai-style palette: the active tab is a filled green block with dark bold text, the other is muted. They are written to look and click like Herdr's tabs (read from Herdr's install files only; Herdr's own keys are prefix-based, so `rtk` adds none), and narrow terminals show ` T  N `. Click anywhere in a tab, padding included; F5 and Esc still work. With no colour the active tab is bold and reverse with a `>` (a non-terminal output strips the attributes, leaving the `>`), and the first frame may show that form before colour appears. Network rows show a symbol and a word for each state (`● idle`, `◐ busy`, `▲ needs you`, `? unknown`, `○ exited`, `… starting`), stale facts are orange with their age, and the selected row is highlighted. Colour is never the only cue, and harness panes are not restyled. Without a UTF-8 locale the symbols become `o * ! ? x .`. The bars are dark on a light terminal theme too, so expect a seam against the panes there, and a 256- or 16-colour terminal may map the greens and greys differently. Differences from the HTML mockups: no rounded corners, pills, hover or badge. There are two fixed tabs, not one per agent.

A plain `rtk attach` with no agents running opens a window on the Network tab instead of exiting (the Terminals tab says `rtk attach --agents claude,codex`). It still never starts a daemon, and starts agents only with `--agents`. If you pass `--agents` and every start fails, it exits with the reasons and opens no window. A window's sessions are fixed when it attaches, so agents started later need a reattach. With no daemon at all, `rtk attach` still exits with `no daemon`.

**Where the data comes from.** Each daemon reports its own sessions to the gateway (`PUT /v1/report`). The gateway joins the reports with its directory of paired hosts. Your daemon reads that through `GET /v1/network` and hands it to your window and to `rtk daemon network`. Nothing polls a host over SSH, and the gateway never carries a terminal, keystrokes, prompts, screens, environments or credentials. Reports are kept in the gateway's memory only, so a gateway restart empties them until the daemons report again.

**What a row means.** Per session: name, harness, start time, a state (`starting`, `idle`, `busy`, `needs_you`, `unknown`, `exited`), when that state was seen, and whether input is held, free or unknown.

- `unknown` is shown as unknown. It is never turned into idle.
- A missing field means it isn't available. The model is never reported.
- A report older than 30 seconds, or from a host that isn't live, is stale: it is history, not proof that anything is still running.
- A host with no report reads "no report", which is not the same as having no sessions.
- A gateway older than this build gives only bare peer addresses with no live flag.

**Keys in the tab (clicking works for the strip; the rest are keys):** arrows and PgUp/PgDn select, **Enter** enters the session, **R** refreshes, **F6** takes input, **F10** detaches.

**Commands**

```
rtk daemon network            # a table: this machine, the gateway link, every host's sessions
rtk daemon network --json     # the same as JSON (schema 1)
rtk daemon network --watch    # reprint when it changes
```

The JSON has `local` (always available, even with no gateway), `gateway.state` (`connected`, `unreachable`, `not_configured` or `unsupported`) and `snapshot` (the last one fetched, or `null` before the first). Field-by-field schemas and the two gateway routes are in [`docs/implementation/network-api-contract.md`](docs/implementation/network-api-contract.md).

**SSH entry.** Enter on a session in another machine's row opens it over `ssh` from your own terminal, with your own keys, agent and `~/.ssh/config`. `rtk` adds no host-key or auth settings. The window closes, `ssh -t -o ConnectTimeout=5 -- <ssh> '<rtk> attach --expect-session <id> --entered-via-ssh'` runs in the same terminal, and when it ends a fresh window opens on the Network tab at the same row. If nothing is running locally any more, `rtk` prints the usual guidance and exits.

Enter needs a host map on the machine you enter from, `<state dir>/hosts.json` (the state directory is shown under [Data](#data)). You edit it by hand and it is read at each Enter:

```json
{
  "version": 1,
  "hosts": {
    "vmb": { "ssh": "me@192.168.1.23", "rtk": "/home/me/.local/bin/rtk" },
    "vmc": { "ssh": "build-vmc", "rtk": "/usr/local/bin/rtk", "state_home": "/home/me/.local/state" }
  }
}
```

- Keys are the host names exactly as the gateway lists them. `ssh` can be a LAN or tailnet address, `user@host`, or an alias from your SSH config. `rtk` must be an absolute path on the remote machine. `state_home` is optional.
- The file is refused if it is a symlink, not a regular file, writable by group or others, owned by someone else or over 64 KiB. Without an entry, the row shows the command to type on that machine instead (a `manual` plan). Nothing is run.
- On the remote machine `rtk attach --expect-session` attaches only that exact session. If it ended or was replaced, it refuses before taking input.

| `ssh` ended with | The tab says |
| --- | --- |
| 0 | Nothing: back on the tab |
| 3 | The session ended or was replaced; choose again |
| 4 | No `rtk` daemon on that host |
| 2 | That host's `rtk` is too old for `--expect-session` |
| 255 | `ssh` couldn't connect or the connection ended. `rtk` can't tell unreachable, timeout, host key, auth or a dropped link apart, so read `ssh`'s own message |
| other | The remote command exited with that code |

**Limits**

- Same-user agents can read `pairing.json`, so this API doesn't isolate them from the network data. It grants no control: no starting, stopping, typing or entering without your own SSH keys. Any paired host can read every host's snapshot.
- Entering a session on another machine from inside an SSH-entered window is blocked. Press F10 to go back first.
- The gateway doesn't cap parked long-polls (paired hosts only), and a poll parked when its host is unpaired can answer for up to 20 seconds.
- `state_at` for a session that was already busy is when the daemon first saw it.
- The terminal handover (a 600 ms wait that drops typeahead) rests on reading Bubble Tea's source, not on observation. Whether a terminal is left clean after each way `ssh` can end is exactly what still needs your testing.
- SSH entry works on macOS and Linux only.

**Upgrading.** Either machine can be upgraded first. The full tab needs the gateway and each daemon on this build, and a window must match its own daemon's build, as before. There is no database change from the Network tab itself (the host-card feature below does change the gateway database); the Network tab needs no database change and the wire protocol stays at 2. Restarting a daemon with `rtk daemon stop --yes` ends its sessions, as before. Nothing is added for HTTPS over a tailnet.

The gateway now routes with [chi](https://github.com/go-chi/chi) v5.5.0. Two behaviours differ from before: `HEAD` on a `GET` route returns 405, and a request with an unclean path gets 404 instead of a redirect.

**First things worth testing, once you say so:** the terminal after each way `ssh` can end (clean exit, 255, `kill` of `rtk`, `kill -9` of `ssh`); typeahead and late terminal replies at the handover; Ctrl+C during the connection; F10 returning to the same row; exit 3 and 4 through `ssh`; ages moving on a quiet network; and live and stale flips while a poll is parked. The tab strip checks (clicks, drag over the strip, the empty window, `--agents` all failing) are there too. The step-by-step version is scenario 4 in [`docs/testing/`](docs/testing/).

## Host cards and the Graph tab

**Never run.** The first version is committed (744f86a). The 2026-11-08 corrections described here (the default listener, `card.yaml` and model drafting) are only in the working tree. The code has passed only `gofmt`, `go vet` and `go build`. See [ADR-0005](docs/adr/0005-host-agent-cards-and-graph/index.html) and [the contract](docs/implementation/host-cards-contract.md).

Each daemon serves a **read-only, unauthenticated "host card"** describing its host: a name, a description you write, and the skills you choose to declare. The card document follows the v1.0.0 AgentCard field set; **the host is not an A2A service**, and its one interface is the card's own URL, binding `RATATOSKR-CARD`. The gateway polls each registered host's card on a schedule, keeps the cards and poll history in its database, and a third **Graph** tab draws the hosts.

**The card is a file you edit:** `daemon/card.yaml` in the state directory shown under [Data](#data), mode 0600. The Graph tab shows its path. On first start the daemon writes a commented template. It is not served until you fill in `description` (the status says "needs description"). Edit it in your own editor: the daemon notices the change within about 2 seconds. If a save is invalid or half-written, the daemon keeps serving the **last good card** and the Graph tab shows the error and its line. A card needs a description, and each skill an id, name, description and at least one tag. Unknown keys, and values that look like keys or tokens, are refused with their line number (the value is not repeated). Skills found on disk are shown but **never added to the card** unless you write them in. There are no card commands. History is yours to keep (git, or your own backup).

| Command | What it does |
| --- | --- |
| `rtk daemon` | Serves the card at `/.well-known/agent-card.json` (and the alias `/.well-known/agent.json`) on **`0.0.0.0:7778` by default**, plain HTTP, GET and HEAD only. `--card-listen ADDR` picks another address, `--card-listen off` turns it off. Changing it on a running daemon means `rtk daemon stop`, which ends every session. If the port can't be bound the daemon still starts and says `NOT BOUND` with the reason. |
| `rtk daemon status` | Includes `card listener:` followed by `listening on ADDR, card at URL (readable by anyone who can reach this port)`, `NOT BOUND: REASON (the daemon is running; the card is not served)`, or `off`. The same line is in the daemon log at start and in the Graph tab. |
| `rtk gateway card-target set HOST URL [--fingerprint sha256:...]`, `show`, `clear` | You tell the gateway where to poll each host. IP literals only (no hostnames). Plain http is accepted on loopback, Tailscale and private LAN (RFC 1918) addresses. https with a fingerprint is still an option on a LAN. Public addresses are refused. `--insecure-http` is no longer needed and is refused. |
| `rtk gateway --listen ... --card-poll-interval 60s` | The poll interval, 10 s to 1 h. 60 s is a default I chose, not one you selected. |

**Generating a draft with a model.** In the Graph tab, on the local host, **g** prepares a draft using your installed harness, set under the file's private `drafting:` section (`preset: claude`, `codex` or `custom` with your own `argv`, and `pass_env` with variable **names** only). Nothing runs yet: a confirmation screen shows the exact command, the exact payload and its size, and what the harness may load. Press **y** to run it (up to 120 s). You then see a diff against `card.yaml`. **a** accepts: the old file is copied to `card.yaml.bak` and `card.yaml` is **rewritten**, so your comments survive only in the `.bak`. Nothing is written without **a**. **r** reloads the file now. The payload carries names, descriptions, harness names and versions only: no skill bodies, paths, usernames, environment or session text. The `drafting:` section never appears in the served card.
The preset flags come from the vendors' docs, read 2026-11-08, and **have never been run**:
- **claude:** `claude -p --safe-mode --tools "" --no-session-persistence --output-format text --permission-prompts none`. `--safe-mode` skips your CLAUDE.md, skills, plugins, hooks and MCP servers, but managed-policy hooks still apply. `--permission-prompts` needs Claude Code 2.1.259 or later.
- **codex:** `codex exec --sandbox read-only --skip-git-repo-check --ephemeral --ignore-user-config --ignore-rules`. Codex documents no per-run switch for skills, hooks or MCP servers, so it **may load yours**. The confirmation screen says so.

Closing the window cancels its draft. An unaccepted draft expires after 15 minutes.

**What it means and does not mean.**

- **Public, and on by default.** The listener binds every interface, so anyone who can reach port 7778 can read the card, without a password. Put nothing sensitive in `card.yaml`. The card is built by copying named fields from the file, so `drafting:` never reaches it. It holds no tokens, credentials, skill bodies, paths, usernames, environment or session text. The check is a block-list of shapes, not a guarantee. If you don't want it reachable, use `--card-listen off` or a loopback address.
- **Local editing only.** There is no way to change a card from the gateway or another host. To change a remote host's card, edit that host's `card.yaml` (for example in a session entered over SSH). Any process of yours on that host can write the file or call the daemon's socket, so this is not protection against same-user agents.
- **A poll proves little.** A good poll shows the card endpoint answered. A 304 shows it answered and nothing changed. A timeout is a failed poll, not proof the process died, and none of it says anything about sessions. The existing heartbeat and stream remain what say a host is connected; the Graph tab shows the two signals separately.
- **A stored card is not live proof.** After a gateway restart the Graph tab shows each host as restored from the database until it is polled again.
- **Edges** are `messages_seen`: sends the gateway decided in the last 24 hours, computed from the message log, failed ones included. They never mean delivered, trust or capability.
- **Drafting** calls a model only after your **y** and writes only after your **a**, as described above. Generation quality, the presets and the logins they need have never been tried.
- **Skills.** Found on disk, declared in the card, allowed and verified are four different things; only the first two exist. Only user-scope directories are scanned, by the daemon, at start, even with the listener off. `$CODEX_HOME/skills` is not documented as a skill directory by Codex.

**Upgrading needs care: the gateway database changes.** The first `rtk gateway --listen` on this build migrates an existing gateway database to schema version 2 after taking a `VACUUM INTO` backup next to it (`...bak-v1-<time>`, mode 0600), and it does so before binding. Admin commands such as `rtk gateway hosts` refuse an old database and tell you to start the gateway. An older gateway binary then refuses the migrated database; roll back by stopping the gateway, restoring the backup and running the older binary. **The migration has never run.** Copy the whole gateway state directory first and try a copy. Wire protocol 2 is unchanged; an older gateway answers 404 on `/v1/graph` and the window says so.

**Known limits.** `100.100.100.200` is on the deny list but unverified. Accept does not keep comments in `card.yaml`. The 2-second file check may lag an edit briefly. Not built: an `rtk daemon graph` command, a cap on parked long polls, retention settings. Light themes, mouse on real terminals and narrow widths are undrawn. [Test scenario 5](docs/testing/scenario-5-host-cards/README.md) lists the checks, every one marked written, not verified.

## Architecture decisions

Key decisions are kept in [`docs/adr/`](docs/adr/) as Architecture Decision Records (ADRs). Each one says what was chosen, why, what was rejected and what it costs.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](docs/adr/0001-mcp-agent-messaging/index.html) | Agents send through an MCP tool and receive through a gated paste | Accepted |
| [0002](docs/adr/0002-cross-machine-messaging/index.html) | Agents on different machines message through a central gateway on your tailnet or, by opt-in, your LAN, with pairing, full message storage and immediate failure when a machine is unreachable. The owner reports that it worked in their testing on 2026-11-06, before the daemon replaced `rtk runtime` | Accepted |
| [0003](docs/adr/0003-per-host-daemon/index.html) | Move from `rtk runtime` to a long-lived daemon on each machine, with attachable windows, as the basis for sessions that spawn workers. Plain `rtk` stays local. The daemon is built, and the owner reports that it works in their testing on 2026-11-06, with one open bug. Workers aren't built, and some questions about them remain open | Accepted |
| [0004](docs/adr/0004-network-tab/index.html) | A Network tab inside the attached window, following agent-net: a gateway API lists every host's sessions with how fresh each fact is, and Enter on a remote session opens a direct SSH connection from your own terminal. Not a dashboard. The API, tab and SSH entry are written but have never been run; the open choices in the record are proposals, not decisions | Proposed |
| [0005](docs/adr/0005-host-agent-cards-and-graph/index.html) | A public, unauthenticated A2A-style Agent Card per host daemon, served by default on port 7778 from a `card.yaml` its owner edits (optionally from a model draft), polled by the gateway on a preset schedule, stored with the graph in the gateway SQLite database and drawn as a graph of hosts in a third Graph tab. First version committed, the 2026-11-08 corrections are in the working tree, none of it run; the polling interval is a default I chose, the gateway database needs a migration, and the host is not an A2A service | Proposed |
| [0006](docs/adr/0006-a2a-routing-and-identity/index.html) | Full A2A through the gateway, as a proposal with animated diagrams: one endpoint per exact session, how the sender is proven, and tasks with questions and results. Proposal only: nothing is built or run, and ADR-0005's card is not this | Proposed |

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
