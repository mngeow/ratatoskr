# ratatoskr

![Ratatoskr, drawn as a fierce gold beast in the style of Viking Age carving, with a drill-tooth tusk and a rune-stick message in its claws, encircled by the serpent Níðhöggr carved with its name in runes. Around the ring, a network links Claude Code, Codex, pi, OpenCode and other agents, along with app development, devops and infrastructure work.](assets/ratatoskr-banner.svg)

**Documentation home:** open [`docs/index.html`](docs/index.html) for every decision record, testing guide, contract and working paper, with search and a text preview of the Markdown files. On macOS, run `open docs/index.html` from the repository root, then bookmark it. GitHub shows `.html` files as source, so use a local checkout.

Ratatoskr is an **agent network written in Go**. One binary, `rtk`, runs Claude Code and Codex in terminal panes, lets them message each other, and extends that across machines through a central gateway. You stay in control: each harness keeps its own permission prompts, and you answer them yourself.

> [!NOTE]
> **Status: early, hand-tested, not a finished product.** The owner has run the plain `rtk` window, cross-machine messaging and the daemon by hand on macOS and reported that they work, with one open bug (text appears in the prompt box after an answer; the cause is unknown). The team has run **none** of the automated tests, and much of the newest code (the Network and Graph tabs, host cards, polling, model-drafted cards, SSH entry) has been written and statically checked but not exercised by the team. Every claim below says which it is. Details are in [outstanding issues](docs/implementation/outstanding-issues.md).

## What it does today

| Area | What exists |
| --- | --- |
| **Local window** | `rtk` opens Claude Code and Codex side by side in one terminal, with mouse focus, a message log and Ctrl+C that still reaches the agent. |
| **Messaging** | Agents send with an MCP tool, `ratatoskr_send`, and receive by a gated paste that waits until the other agent is idle. Every delivery state is recorded. |
| **Gateway** | `rtk gateway` pairs machines, routes every message and keeps the full ledger in SQLite. It listens on Tailscale or loopback, or on your LAN with an explicit opt-in. |
| **Daemons** | `rtk daemon` runs on each machine with agents, in the background. It owns the agents and their link to the gateway, so closing a window or dropping SSH does not stop them. |
| **Windows** | `rtk attach` opens a window on the daemon's sessions. One window holds input per agent; others are viewers. Plain `rtk attach` with no agents still opens a window. |
| **Tabs** | Every window has clickable **Terminals**, **Network** and **Graph** tabs in a restrained Monokai style. |
| **Network and SSH entry** | The Network tab lists every host's sessions with how fresh each fact is. **Enter** on a remote session opens it over your own `ssh`. `rtk daemon network` prints the same data. |
| **Host cards** | Each daemon serves a public, read-only A2A-style card from a file you edit, `card.yaml`. The gateway polls the cards every 5 seconds, stores them in SQLite, and the Graph tab draws the hosts. |
| **Model-drafted cards** | In the Graph tab you can ask your installed Claude or Codex to draft the card. You see the exact command and payload first, and nothing is written without your approval. |
| **Documentation** | `docs/index.html` links to everything, including Testing scenarios 1 to 5. |

**Limits worth knowing before you start**

- Only the harnesses **Claude Code and Codex** are supported, with session names fixed as `claude` and `codex`: **one of each per daemon, and a daemon is one per state directory.** In normal use that is one of each per machine; the [one-Mac test guide](docs/testing/scenario-1-local/README.md) runs several state directories on one Mac to simulate more. Pi, OpenCode and other harnesses are not implemented.
- The host card is **A2A-style discovery metadata, not an A2A service.** There is no task API, no A2A routing and no discovery tool today.
- There is no worker spawning, no teams, and no remote dispatch.
- OpenRig is a separate system. There is no bridge to it.
- HTTPS over a tailnet is not supported (Tailscale already encrypts the link, so `rtk` uses plain HTTP there). On a LAN you choose pinned HTTPS or plain HTTP explicitly.
- The daemon is not a system service: a reboot ends it.

## How it fits together

```text
 Mac                                   Gateway machine                    Ubuntu
 ┌──────────────────────┐              ┌───────────────────┐              ┌──────────────────────┐
 │ window  window       │              │ rtk gateway       │              │ window               │
 │   └─ rtk daemon ─────┼── dials out ─▶ pairing, routing,  ◀─ dials out ─┼─ rtk daemon          │
 │       claude  codex  │              │ ledger (SQLite),  │              │       codex          │
 │ card on :7778        │◀── polls ────┤ card polling      │──── polls ──▶│ card on :7778        │
 └──────────────────────┘              └───────────────────┘              └──────────────────────┘
```

- **Messaging links dial out.** Daemons connect to the gateway, so only the gateway needs a listening port for messaging, and every message goes through it, even between two agents on one machine. **Cards are the one inbound path:** each daemon listens on port 7778 by default so the gateway can poll its card (see [Host cards](#host-cards-polling-and-the-graph-tab)).
- **Permission prompts stay on the machine running the agent.** The gateway cannot answer them.
- **The gateway polls each registered host's card** on a schedule. That tells it the card endpoint answered; the daemon's heartbeat and stream remain what say a host is connected.

## Quickstart

### Prerequisites

- macOS or Ubuntu. Windows is not supported.
- Go 1.27.3 or newer, to build from source.
- [Claude Code](https://code.claude.com/docs) and [Codex](https://learn.chatgpt.com/docs) on your `PATH` on every machine that runs agents. The design was written against Claude Code 2.1.311 and Codex 0.166.0, and `rtk` does not check harness versions.
- For several machines: [Tailscale](https://tailscale.com/) on each (a normal network interface; userspace networking is not supported), or machines on one private LAN.

### Build and install

```sh
mkdir -p ~/.local/bin
go build -ldflags "-X main.version=$(git describe --always --dirty)-$(date +%Y%m%d%H%M%S)" -o ~/.local/bin/rtk ./cmd/rtk
```

Put `~/.local/bin` on your `PATH`. **Give every build its own version string**, as above. A window refuses to attach to a daemon from a different build, and builds without a version are all called `dev`, so `rtk` cannot tell them apart. Machines only need to agree on gateway protocol 2; the exact build must match only between a window and its own machine's daemon.

### Log in to each harness

`rtk` uses your existing harness logins and stores no Claude, Codex or provider credentials. (It does keep its own: the gateway pairing credential in `pairing.json` and local per-session tokens for its MCP tool.) Sign in on each machine first:

Run `claude` and sign in when prompted, then exit it. Then:

```sh
codex login
```

### Try the plain window (no gateway)

From the project you want both agents to work in:

```sh
rtk
```

Claude Code and Codex start side by side. Ask one to message the other:

```text
Ask Codex what files are in this directory, using ratatoskr_send.
```

Codex's first turn is automatic: `rtk` sends it a short startup prompt (one extra model turn per launch). Codex asks you to review its hooks the first time, and until you trust them `rtk` cannot see its state and holds messages to it.

| Key | Action |
| --- | --- |
| F1 / F2, or click a pane | Focus Claude Code / Codex |
| F3 | Message log. Left/Right step through runs, Up/Down select, Enter opens a message, R reloads, Esc goes back |
| F4 | Deliver held messages to the focused agent when its state is `unknown` or it looks mid-draft (never at a permission prompt or while busy) |
| F10 | Quit, warning if messages are waiting or a draft is open |
| Ctrl+C | Goes to the focused pane. A second press within a second quits |

Because `rtk` captures the mouse, select text with your terminal's bypass modifier (Shift, or Option in some macOS terminals). Plain `rtk` never uses the gateway, and it excludes a daemon using the same state directory.

### Run on one machine with the gateway

Everything below uses a loopback gateway, which is the quickest way to see the full system on one Mac.

In terminal 1, start the gateway and leave it running:

```sh
rtk gateway --listen 127.0.0.1:7777
```

In terminal 2, create a one-time code, pair with it, start the daemon (in the background), check that its link says connected, then open a window that starts both agents:

```sh
rtk gateway pair local
rtk runtime pair --gateway 127.0.0.1:7777 --code XXXX-XXXX-XXXX
rtk daemon
rtk daemon status
rtk attach --agents claude,codex
```

The agents appear as `claude@local` and `codex@local`. The scenario guides ([one Mac](docs/testing/scenario-1-local/README.md)) walk through the same steps with checks.

### Run on two machines (Mac and Ubuntu)

1. **Gateway machine.** Use its Tailscale address (`tailscale ip -4`) and a port you choose, and leave it running:
   ```sh
   rtk gateway --listen 100.x.y.z:7777
   ```
2. **Pair each machine with agents** (in another terminal on the gateway machine). The name becomes the machine's address, using lowercase letters, digits and hyphens. A code works once and expires after 10 minutes:
   ```sh
   rtk gateway pair mac
   rtk gateway pair ubuntu-b
   ```
3. **On each machine with agents**, pair, start the daemon, then attach:
   ```sh
   rtk runtime pair --gateway 100.x.y.z:7777 --code XXXX-XXXX-XXXX
   rtk daemon
   rtk daemon status
   ```
   Then open a window on each: `rtk attach --agents claude` on the Mac and `rtk attach --agents codex` on the server.
4. **Message across machines.** Other machines' agents need the full address; a bare name means an agent on the same machine:
   ```text
   Ask codex@ubuntu-b what files are in its directory, using ratatoskr_send.
   ```

**Without Tailscale (Direct LAN).** A gateway can listen on a private LAN address (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `fc00::/7`; never a public address or `0.0.0.0`). That takes an explicit opt-in, and plain HTTP takes a second one:

| You want | Gateway machine | Each machine with agents |
| --- | --- | --- |
| **HTTPS, pinned** (recommended) | `rtk gateway --listen 192.168.50.217:7777 --allow-lan` | the full `rtk runtime pair ... --fingerprint sha256:...` line that `rtk gateway pair` prints |
| **Plain HTTP** | `rtk gateway --listen 192.168.50.217:7777 --allow-lan --insecure-http` | `rtk runtime pair ... --insecure-http` |

Plain HTTP means anyone who can see or redirect the traffic can read every message and the credential, or pose as the gateway. Each machine makes the HTTPS-or-HTTP choice itself when it pairs; nothing falls back from one to the other. Hostnames are not accepted, so give the gateway a fixed address (a DHCP reservation). A laptop on plain HTTP keeps trying to reconnect on other networks and sends its credential to whatever answers, so stop its daemon before you leave. Every device on the LAN can reach the port, and `rtk` configures no firewall. The guides [LAN with plain HTTP](docs/testing/scenario-2-lan-http/README.md) and [tailnet](docs/testing/scenario-3-tailnet/README.md) cover the steps.

## Using it

### Daemons and windows

| Command | What it does |
| --- | --- |
| `rtk daemon` | Starts this machine's daemon in the background and prints its PID and log path. `--foreground` stays in the terminal |
| `rtk daemon status` | PID, build, uptime, gateway link, card listener, windows and agents |
| `rtk attach [--agents claude,codex]` | Opens a window. With `--agents`, first starts any listed agent that is not running, in the current directory |
| `rtk daemon stop --session NAME` | Ends one agent |
| `rtk daemon stop [--yes]` | **Stops the daemon and every agent.** With agents running, it lists them and needs `--yes` |

`rtk daemon` and `rtk attach` are always separate steps, and a daemon needs a paired gateway. A closed window or a dropped SSH connection leaves the agents running; the daemon crashing, a reboot, or `daemon stop` ends them, and a restarted agent is a **new** session that gets none of the old messages.

**Who types.** One window at a time holds input for each agent (`INPUT HOLDER`); the rest are `VIEWER`s. **F6** takes input for the focused agent. In a daemon window **F10** detaches, and Ctrl+C goes to the agent if the window holds input (a second press within a second detaches). Nothing answers a permission prompt except a key press from the holder.

### The tabs

Every window shows `Terminals`, `Network` and `Graph` as touching, clickable blocks on its top row (` T  N  G ` when narrow). **Click a tab to switch.** F5 (Network) and F7 (Graph) are optional shortcuts, and Esc returns to Terminals. Clicks on the strip never reach an agent.

- **Network** shows each host's sessions, their state (`idle`, `busy`, `needs you`, `unknown`, `exited`, `starting`), when that state was seen, and who holds input. An `unknown` stays unknown, a report older than 30 seconds is marked stale, and a host with no report reads "no report", not "no sessions". The same data is `rtk daemon network [--json] [--watch]` and the gateway's `GET /v1/network`; see the [network API contract](docs/implementation/network-api-contract.md).
- **Graph** draws the hosts, shows each host's card, and is where you generate card drafts (below).

### SSH entry

**Enter** on a remote session in the Network tab runs `ssh` from your own terminal, with your own keys and `~/.ssh/config`, and attaches that exact session on the other machine (`rtk attach --expect-session ID`, refused if the session ended or was replaced). **F10** brings you back to the same row. `rtk` adds no SSH options of its own.

It needs a host map you write by hand, `hosts.json` in the state directory, read at each Enter:

```json
{
  "version": 1,
  "hosts": {
    "ubuntu-b": { "ssh": "me@192.168.1.23", "rtk": "/home/me/.local/bin/rtk" }
  }
}
```

Keys are the host names the gateway lists. `rtk` must be an absolute path on the remote machine; `state_home` is optional. The file is refused if it is a symlink, group- or world-writable, owned by someone else or over 64 KiB. Without an entry the row shows the command to type there instead. Exit codes (3 session ended, 4 no daemon, 2 remote `rtk` too old, 255 `ssh` failed) are explained in the [Network API contract](docs/implementation/network-api-contract.md).

### Host cards, polling and the Graph tab

Each daemon serves a **public, read-only, unauthenticated** card at `/.well-known/agent-card.json` (alias `/.well-known/agent.json`) on **`0.0.0.0:7778` by default**, plain HTTP, GET and HEAD only. `rtk daemon --card-listen ADDR` picks another address and `--card-listen off` turns it off. Anyone who can reach that port can read the card, so **put nothing sensitive in it.** If the port cannot be bound the daemon still starts and says `NOT BOUND` with the reason in `rtk daemon status`, the log and the Graph tab. A running daemon needs `rtk daemon stop` (which ends sessions) to change this.

**The card is a file you edit:** `daemon/card.yaml` in the state directory (mode 0600; the Graph tab shows its path). On first start the daemon writes a commented template and serves the card at once. While `description` is empty it serves a default text and reports `defaulted`; the file itself is never rewritten. The daemon notices an edit within about 2 seconds, and if a save is invalid it keeps serving the last good card and shows the error and line. Skills found on disk are shown but are **never added to the card** unless you write them in. There are no card commands.

With no card to serve (an invalid file and no good card yet), the endpoint answers **503** with `Retry-After: 5` and a short JSON body (`error`, `reason`, `action`) that never contains the file's path or contents.

**The gateway polls** each host you register. You tell it where, by IP address (no hostnames):

```sh
rtk gateway card-target set HOST http://100.x.y.z:7778
rtk gateway card-target show
```

The poll interval is the gateway's `--card-poll-interval` flag (for example `--card-poll-interval 5s`): the default is 5 s and the allowed range is 2 s to 1 h.

Plain http is accepted for loopback, Tailscale and private LAN addresses; public addresses are refused. Every poll is a plain GET that returns the full card, with no conditional requests, and the gateway validates and stores the latest card each time. A host is **fresh** while its last good poll is within the larger of two intervals and 15 s, **stale** after that, and **offline** after the larger of 15 minutes and four intervals. A good poll shows only that the card endpoint answered. A timeout is a failed poll, not proof the process died, and none of it says anything about sessions. After a gateway restart the Graph tab shows each host as restored from the database until it is polled again. Graph **edges** count sends the gateway decided in the last 24 hours, failed ones included: they never mean delivered, trusted or capable.

**Model-drafted cards.** In the Graph tab, on the local host, **g** prepares a draft using your installed `claude` or `codex` (set under the private `drafting:` section of `card.yaml`; never put keys there). A confirmation screen shows the exact command, the exact payload and what the harness may load; **y** runs it, you review a diff, and **a** accepts. Accepting copies the old file to `card.yaml.bak` and **rewrites** `card.yaml`, so your comments survive only in the backup. `drafting:` never reaches the served card. **These presets have never been run**, and Codex documents no way to stop it loading your own skills and hooks.

[ADR-0005](docs/adr/0005-host-agent-cards-and-graph/index.html) and the [host card contract](docs/implementation/host-cards-contract.md) have the details.

### What each send result means

The agent sees this when it sends, and it is in the TUI and the ledger.

| Result | Meaning | Could it still arrive? |
| --- | --- | --- |
| `host_stored` | The recipient's machine has it. It may wait there until the agent can take it | Normal path |
| `gateway_unreachable` | No connection to the gateway, or it refused the request | No |
| `gateway_uncertain` | Sent, but the gateway did not answer in time | **Yes** |
| `unknown_recipient` | No agent by that name has been seen. The result lists who is known | No |
| `destination_unreachable` | That machine is not connected. It will not be delivered when the machine returns | No |
| `session_gone` | That session ended, or its machine refused the message | No |
| `delivery_uncertain` | Offered, with no confirmation in time | **Yes** |

Nothing is resent automatically, and `accepted` means only that the harness took the input, not that the model read or acted on it. Read the ledger on the gateway machine with `rtk gateway log [--follow] [--limit N]`, `rtk gateway show ID`, `rtk gateway hosts` and `rtk gateway unpair HOST` (these print message text, which may include secrets).

### Command summary

| Command | Where |
| --- | --- |
| `rtk` | Plain local window, no gateway |
| `rtk gateway --listen ADDR [--allow-lan [--insecure-http]] [--card-poll-interval 5s] [--participant-listen ADDR\|off [--participant-allow-lan] [--participant-insecure-http]]` | Gateway machine. The participant API is on by default at the `--listen` IP, port 7790 |
| `rtk gateway pair HOST`, `unpair HOST`, `hosts`, `log`, `show ID`, `card-target set\|show\|clear` | Gateway machine |
| `rtk runtime pair --gateway ADDR --code CODE [--fingerprint SHA256 \| --insecure-http]` | Each machine with agents |
| `rtk daemon [--foreground] [--card-listen ADDR\|off]`, `status`, `stop`, `network` | Each machine with agents |
| `rtk attach [--agents claude,codex] [--expect-session ID]` (alias `rtk runtime`) | Each machine with agents |

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| A pane shows a start error | Is that harness installed and on `PATH`? The other agent keeps running |
| `no hook events` banner, messages held | Codex hooks may need review. Answer its prompt in the Codex pane, or press F4 if it shows `unknown` |
| An agent says it replied but nothing arrived | It may have used a built-in messaging tool. Ask it to use `ratatoskr_send` and check the log (F3) |
| Refuses to start: another ratatoskr is already running | Only one of plain `rtk` or a daemon can run per state directory |
| `rtk daemon` exits: not paired | Pair with `rtk runtime pair` first. `rtk attach --agents` also says why when the gateway cannot be reached |
| A window refuses to open against the daemon | A different build. Run `rtk daemon stop`, which ends sessions, then start the daemon again |
| Refuses to start: database newer than this binary | Update `rtk`; the database is left untouched |
| Card endpoint answers 503 | See `reason` in the JSON, then `rtk daemon status` on that host |
| A host reads `redirect` in `card-target show` | An old gateway build misread the daemon's 304. Rebuild and restart the gateway |
| Daemon says `NOT BOUND` for the card | The port is in use or the address is wrong. The daemon still runs; use `--card-listen` |

## Data and security

- **Local data** is in `$XDG_STATE_HOME/ratatoskr/`, or `~/.local/state/ratatoskr/`. Message bodies are plain text and may contain secrets. The local database deletes runs older than 30 days at startup. To erase everything, delete that directory while nothing is running.
- **The gateway's ledger** (`gateway/gateway.db`) keeps every message with its full text and **never deletes it automatically.** A message can sit on three machines: the sender, the gateway and the recipient. A compromised gateway could read stored messages and inject prompts into connected agents, but not answer permission prompts.
- **A paired machine's credential** is in `pairing.json`, readable only by you. Agents run as you, so one with file access could read it. `--code` appears in your shell history and process list until used or expired.
- **Not a security boundary.** Agents run as you, and the daemon's socket refusal of its own agents is protection against accidents, not a guarantee.

## Upgrading

- **Move every machine together** when the gateway protocol changes. It is now version 2 and refuses version 1. Quit old `rtk runtime` windows, stop daemons, install the new build everywhere, and restart `rtk gateway` with the same flags. Pairings and the database carry over. Restarting a daemon ends its sessions.
- **The gateway database is migrated on first start.** The first `rtk gateway --listen` on a build that adds the host-card tables migrates an existing database to schema version 2 after taking a `VACUUM INTO` backup beside it (`...bak-v1-<time>`, mode 0600), before binding. Admin commands refuse an old database and tell you to start the gateway. An older gateway binary then refuses the migrated database; roll back by stopping the gateway, restoring the backup and running the older binary. **This migration has never been run.** Copy the whole gateway state directory first, and try the copy.
- **Either side can be upgraded first for card polling.** An old daemon answers the new gateway's plain GET with a full 200, and a new daemon ignores an old gateway's `If-None-Match`. A new window needs a matching daemon build.
- **Fixed since your last build, and not yet re-run by the team:** a fresh daemon answered the card endpoint with an empty 503 (the template card was invalid), and the gateway showed healthy hosts as `redirect` because it read the daemon's 304 as a redirect. After rebuilding and restarting, the next poll should read fresh. The owner reported both symptoms; the fixes have not been observed.

## Known limitations

- **One open bug:** text can appear pasted into the prompt box after an answer. Its cause is unknown ([outstanding issues](docs/implementation/outstanding-issues.md)).
- **Embedded panes are the biggest risk.** They use a young terminal emulator (`x/vt` v0.1.0) without Kitty or CSI-u keyboard support, so colours, wide characters, redraw speed and modified keys may misbehave. There is no scrollback beyond what the harness redraws.
- **`rtk` cannot see a harness's input box,** so any key except a plain Enter counts as a draft and can hold messages while nothing is typed. F4 delivers them.
- **Pastes have limits.** Over 1 MiB is refused, and a slow paste can stop after 3 seconds. Messages arrive as pasted input, so they look like user turns.
- **No resume or resend.** Unfinished messages become `interrupted` and are never resent.
- **Agents get the environment of the terminal that started the daemon.** Started over SSH, they keep that session's `SSH_AUTH_SOCK`, which can stop working after you log out.
- **Linux and SSH use are untested** at runtime. Whether the daemon survives logging out of macOS is unknown.
- **The newest features are unproven by the team:** the Network and Graph tabs, SSH entry, polling, the fresh-start and 304 fixes, the YAML reload and model drafting have been written and checked by `gofmt`, `go vet` and `go build` only. Colour, mouse and narrow-width rendering on real terminals are undrawn.

## Participant API (on the branch `feat/adapter-integration`, never run)

An **optional** HTTP+JSON API on the gateway for **external adapters**, written in any language, that is separate from the host protocol (which is unchanged). It is built against [ADR-0007](docs/adr/0007-adapter-integration-contract/index.html) and described in [`docs/spec/participant-api/`](docs/spec/participant-api/README.md) with JSON Schemas and 20 conformance examples. **Nothing has been run, and no external harness (OpenRig, Pi or any other) is claimed to work with it.** It is not on `main`.

How it fits, in outline:

- The participant API has its **own listener**, **on by default** at the IP of `--listen` on **port 7790**, so every `rtk gateway --listen` command above also serves it. `--participant-listen ADDR` picks another address and `--participant-listen off` turns it off. It is LAN and tailnet (and loopback) only, **HTTPS by default** on a LAN or tailnet address with the gateway's own pinned certificate, and plain HTTP only with `--participant-insecure-http`: the host's `--insecure-http` is never inherited. On a defaulted LAN address the host's `--allow-lan` is the opt-in; an explicit LAN address needs `--participant-allow-lan`. If the host listener already uses port 7790, startup is refused until you choose another address or `off`.
- The owner enrols an adapter with `rtk gateway participant pair ID`, which prints a single-use code and the certificate pins. `participant list` and `participant revoke ID` manage them.
- The adapter redeems the code, registers named sessions (`coder@pi-lab`), sends with `POST /participant/v1/messages` and receives by **long-polling** `GET /participant/v1/inbox`. A send to an offline participant fails at once; nothing is queued.
- Managed Claude and Codex sessions message a participant session with the existing `ratatoskr_send`; no change was made to them.

A single-file, standard-library-only **Python reference adapter** ([`integrations/participant-reference-py/`](integrations/participant-reference-py/README.md), also never run) shows the contract is not Go-specific. An adapter must pin the gateway certificate, which a stock `curl` cannot do against it; the spec explains why and shows a Python recipe, also untested. Test steps: [scenario 7](docs/testing/scenario-7-participant-api/README.md), also in the [HTML guide](docs/testing/index.html#s7).

## Roadmap (proposals, nothing built)

These are decision records, not features. None is implemented, and none authorises code.

| Proposal | What it would add |
| --- | --- |
| [ADR-0007](docs/adr/0007-adapter-integration-contract/index.html) | A language-neutral contract so outside harnesses and orchestrators (OpenRig, Pi, custom) join through adapters in any language; the user approved a separate HTTP+JSON participant API on the gateway with a long-poll inbox; being built on branch `feat/adapter-integration`, never run |
| [ADR-0008](docs/adr/0008-openrig-operator-integration/index.html) | Only the main OpenRig operator on the network, through a Go bridge (sidecar plus thin client under the OS service manager) that is a client of the ADR-0007 participant API; inbound by `rig send`, no allow-list in v1 (user choices 2026-11-09); the outbound mechanism is still open. Source-grounded against OpenRig 0.7.7, nothing built or run |
| [ADR-0009](docs/adr/0009-agent-discovery-tool/index.html) | A `ratatoskr_discover` tool returning a compact list of host cards and contactable sessions, for the model to choose from; the gateway does no search |

**Deferred:** [A2A](docs/adr/deferred/0006-a2a-deferral/index.html) (ADR-0006) is set aside for now because its task semantics do not fit the current need for direct peer messaging. The proposal and a never-run implementation branch, `feat/a2a-participants`, are kept; see the [deferred ADRs](docs/adr/deferred/index.html).

Not on the list yet: worker spawning, teams, Pi and OpenCode adapters, remote dispatch.

## Testing

The step-by-step guides are in [`docs/testing/`](docs/testing/) (full HTML guide: [`index.html`](docs/testing/index.html)): [one Mac](docs/testing/scenario-1-local/README.md), [LAN, plain HTTP](docs/testing/scenario-2-lan-http/README.md), [tailnet](docs/testing/scenario-3-tailnet/README.md), [Network tab and SSH entry](docs/testing/scenario-4-network-tab/README.md), [host cards and the Graph tab](docs/testing/scenario-5-host-cards/README.md) and, on the participant-API branch, [the participant API](docs/testing/scenario-7-participant-api/README.md). Their expected results come from reading the code, so tell the team where reality differs. **No automated test has ever been run**, and the only run-time evidence is the owner's hand-testing noted above, at the scopes they reported. Running `go test -race ./...` is not part of the quickstart.

## Architecture decisions

Decisions are kept in [`docs/adr/`](docs/adr/), one folder and one self-contained `index.html` each, with interactive diagrams. Each says what was chosen, why, what was rejected and what it costs. Numbers stay fixed; a changed decision gets a new record.

| ADR | Decision | Status |
| --- | --- | --- |
| [0001](docs/adr/0001-mcp-agent-messaging/index.html) | Agents send through an MCP tool and receive through a gated paste | Accepted |
| [0002](docs/adr/0002-cross-machine-messaging/index.html) | Agents on different machines message through a central gateway, with pairing, full message storage and immediate failure when a machine is unreachable | Accepted; the owner reports it worked in their 2026-11-06 testing, before the daemon replaced `rtk runtime` |
| [0003](docs/adr/0003-per-host-daemon/index.html) | A long-lived daemon on each machine with attachable windows. Plain `rtk` stays local | Accepted; built, owner-tested with one open bug; workers not built |
| [0004](docs/adr/0004-network-tab/index.html) | A Network tab and SSH entry, following agent-net | Proposed; implemented, not run by the team |
| [0005](docs/adr/0005-host-agent-cards-and-graph/index.html) | Public A2A-style host cards from `card.yaml`, polled by the gateway and drawn in a Graph tab | Proposed; implemented, not run by the team; the gateway database needs a migration |
| [0006](docs/adr/0006-a2a-routing-and-identity/index.html) | A2A routing and identity, with managed and external participants ([deferral record](docs/adr/deferred/0006-a2a-deferral/index.html)) | **Deferred** 2026-11-08; not on `main` |
| [0007](docs/adr/0007-adapter-integration-contract/index.html) | Adapter integration contract (language-neutral) | Approved; implemented on `feat/adapter-integration` (not on `main`), never run |
| [0008](docs/adr/0008-openrig-operator-integration/index.html) | OpenRig operator integration | Proposal only; the participant API it needs is merged, the bridge is not built |
| [0009](docs/adr/0009-agent-discovery-tool/index.html) | Agent discovery tool | Proposal only |

## The name

In Norse mythology, [Ratatoskr](https://en.wikipedia.org/wiki/Ratatoskr) is the squirrel that runs up and down the world tree Yggdrasil, carrying messages between the eagle perched at its top and the dragon Níðhöggr beneath its roots. The name is usually glossed as "drill-tooth".

This project does the same job for your agents, minus one detail: the Prose Edda says the original spread slanderous gossip to provoke the eagle and the dragon. Ours is meant to keep agents working together.
