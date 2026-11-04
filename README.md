# ratatoskr

![Ratatoskr, drawn as a fierce gold beast in the style of Viking Age carving, with a drill-tooth tusk and a rune-stick message in its claws, encircled by the serpent Níðhöggr carved with its name in runes. Around the ring, a network links Claude Code, Codex, pi, OpenCode and other agents, along with app development, devops and infrastructure work.](assets/ratatoskr-banner.svg)

**Ratatoskr is an agent network.** Define agents built on the harnesses you already use, such as Claude Code, Codex, pi and OpenCode, and put them on a common network where they can discover each other and talk to each other. From there, you can spawn whole teams of agents to work together on a shared task.

> [!WARNING]
> Ratatoskr is at a very early stage and **its first version is largely unproven.** The project's owner has run it by hand on macOS and reports that mouse click focus and Ctrl+C work. Nothing else has been checked: no automated test has been run, and message delivery, recovery and Linux haven't been verified, so don't assume the rest works. See [outstanding issues](docs/implementation/outstanding-issues.md). The product sections describe what the project is setting out to build; [Setup](#setup) covers the first version only.

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
> Apart from mouse click focus and Ctrl+C, which the project's owner checked by hand on macOS, every command and behaviour in this section is **unverified**: it is written from the design and from each harness's docs and `--help`. Expect corrections once testing starts.

The first version is one command, `rtk`. It is meant to open one terminal window with Claude Code and Codex side by side, so that they can message each other while you stay in control. It doesn't include discovery, teams, other harnesses or cross-machine messaging.

### Prerequisites

- macOS or Ubuntu. Windows is not supported.
- Go 1.27.3 or newer, to build from source.
- [Claude Code](https://code.claude.com/docs) and [Codex](https://learn.chatgpt.com/docs) on your `PATH`. The design was written against Claude Code 2.1.311 and Codex 0.166.0, but neither has been run with `rtk`, and `rtk` doesn't check harness versions.
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

Both harnesses start as new sessions in that directory. Each one keeps its own permission prompts, and you answer them in its pane. Only the `send_message` tool is pre-approved.

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
Ask Codex what files are in this directory, using send_message.
```

The message should appear in the Codex pane once Codex is idle, and the log shows its status. These statuses describe delivery only: `accepted` means the harness took the input, not that the model read or acted on it.

Agents can send at most 20 messages in a row. The budget resets when you press Enter in a pane.

### Troubleshooting

| Symptom | What to check |
| --- | --- |
| A pane shows a start error | Is that harness installed and on `PATH`? The other agent keeps running. |
| `no hook events` banner, messages held | Codex hooks may need review. F4 delivers in the meantime. |
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
- **Pastes have limits.** A paste over 1 MiB is refused with a message. A slow paste can stop after 3 seconds with only part of it delivered.
- **Pasting is how messages arrive,** so they look like user turns. The message frame says they came from a peer.
- **No resume or resend.** Agent sessions end with `rtk` and a new run knows nothing about the last one. Unfinished messages are marked `interrupted` and never resent; read them in the log (F3) and pass them on yourself.
- **If an SSH connection drops,** `rtk` and both agents may end with it. Running `rtk` inside `tmux` or `screen` on the server is the suggested stopgap.
- **Not a security boundary.** Both agents run as you and could read each other's files or tokens. A message can't approve anything for the recipient, but it can still persuade its model, so its permission prompts remain the real check.

## The name

In Norse mythology, [Ratatoskr](https://en.wikipedia.org/wiki/Ratatoskr) is the squirrel that runs up and down the world tree Yggdrasil, carrying messages between the eagle perched at its top and the dragon Níðhöggr beneath its roots. The name is usually glossed as "drill-tooth".

This project does the same job for your agents, minus one detail: the Prose Edda says the original spread slanderous gossip to provoke the eagle and the dragon. Ours is meant to keep agents working together.
