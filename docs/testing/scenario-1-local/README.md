# Scenario 1: everything on one Mac

A gateway and two daemons on one Mac, with two copies of each harness. Each daemon pretends to be a machine, so this exercises real cross-machine messaging over loopback, with no network to go wrong.

**This is the short form, for reading on a VM or on GitHub.** The [full guide](../index.html#s1) has the diagrams, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source.

> **Nobody has run the daemon.** Expected results come from reading the code, so tell the team when something differs. Replace each `<PLACEHOLDER>`.

## 1. Open five terminals

Use ordinary terminal windows or tabs, not a pane that belongs to OpenRig or another tool. Name them so you can follow along. **Each one needs its own lines below, because the state directory is what makes it a separate machine.** A command run without them talks to your normal state directory.

**T1 gateway**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s1/gateway"
```

**T2 gateway admin**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s1/gateway"
```

**T3 alpha**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s1/alpha"
```

**T4 beta**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s1/beta"
```

**T5 spare window on alpha**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s1/alpha"
```

Your usual `~/.local/state/ratatoskr` and `~/.local/bin/rtk` stay untouched, because everything lives under `~/rtk-test`.

## 2. Build, and make the working directories

Each agent gets its own directory, so that no two can overwrite each other's files. Replace `<BUILD_ID>` with one identifier you choose.

**T1 gateway**

```sh
cd "<REPO>"
mkdir -p "$HOME/rtk-test/bin"
go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk" ./cmd/rtk
"$HOME/rtk-test/bin/rtk" --version
```

**T1 gateway**

```sh
mkdir -p "$HOME/rtk-test/s1/gateway" "$HOME/rtk-test/s1/alpha" "$HOME/rtk-test/s1/beta"
mkdir -p "$HOME/rtk-test/s1/work/alpha-claude" "$HOME/rtk-test/s1/work/alpha-codex" "$HOME/rtk-test/s1/work/beta-claude" "$HOME/rtk-test/s1/work/beta-codex"
```

Expect:

- `rtk --version` prints `rtk` and your build identifier, not `dev`.

**Choose a new identifier every time you rebuild,** such as `daemon-test-2`. It's how a window tells that a daemon came from a different build, and two builds with one identifier look identical.

## 3. Start the gateway

Leave it running in T1.

**T1 gateway**

```sh
rtk gateway --listen 127.0.0.1:7791
```

Expect:

- `gateway listening on 127.0.0.1:7791 (state in ...)`
- `transport: plain HTTP; loopback only`

Port 7791 is only for this guide. If something else is using it, pick another and use it throughout.

## 4. Pair the two identities

Each `pair` prints a one-time code that expires in 10 minutes. **Its last line starts with `on that machine: `. Copy only what follows that**, starting at `rtk runtime pair`, and run it in the terminal for that identity.

**T2 gateway admin**

```sh
rtk gateway pair alpha
```

**T3 alpha**

```sh
rtk runtime pair --gateway 127.0.0.1:7791 --code <ALPHA_CODE>
```

**T2 gateway admin**

```sh
rtk gateway pair beta
```

**T4 beta**

```sh
rtk runtime pair --gateway 127.0.0.1:7791 --code <BETA_CODE>
```

Expect:

- `paired as alpha with gateway 127.0.0.1:7791`, and the same for beta.
- `credential saved in ...` names a path under `~/rtk-test`, not your usual state directory.

## 5. Start both daemons

**T3 alpha**

```sh
rtk daemon
rtk daemon status
```

**T4 beta**

```sh
rtk daemon
rtk daemon status
```

**T2 gateway admin**

```sh
rtk gateway hosts
```

Expect:

- `daemon started (pid N)`, the log path, and `harnesses will inherit the environment of this terminal`.
- `rtk daemon status` shows `link: connected`, `windows: 0` and `sessions: none`. If the link isn't connected yet, run it again.
- `rtk gateway hosts` shows `alpha` and `beta` as `live`.

## 6. Start four agents

Start each one from its own directory. F10 detaches and leaves the agent running, so the last `rtk attach` shows both. Answer any trust or hook-review questions in the pane. **Each Codex launch costs one model turn, and these are real agents using your logins.**

**T3 alpha**

```sh
cd "$HOME/rtk-test/s1/work/alpha-claude"
rtk attach --agents claude
```

**T3 alpha**

```sh
cd "$HOME/rtk-test/s1/work/alpha-codex"
rtk attach --agents codex
```

**T3 alpha**

```sh
rtk attach
```

**T4 beta**

```sh
cd "$HOME/rtk-test/s1/work/beta-claude"
rtk attach --agents claude
```

**T4 beta**

```sh
cd "$HOME/rtk-test/s1/work/beta-codex"
rtk attach --agents codex
```

**T4 beta**

```sh
rtk attach
```

Expect:

- Press F10 after each of the first two commands in each terminal. It prints `detached; the agents keep running (rtk attach returns to them, rtk daemon stop ends them)`.
- Both panes appear, with headers starting `INPUT HOLDER`. The title says `Gateway connected`.
- A window only shows sessions that existed when it attached, which is why the last `rtk attach` comes after both have started.

**You can't run two of one harness in a single daemon.** Names are fixed as `claude` and `codex`, and `--agents claude,claude` is refused as a duplicate. A second identity is the only way to get a second copy.

## 7. Send messages between them

Type these into the panes. They're prompts for the agents, not shell commands.

**claude pane in T3**

```text
Ask claude@beta which directory it is working in, using ratatoskr_send, and ask it to reply to you.
```

**codex pane in T3**

```text
Ask codex@beta which directory it is working in, using ratatoskr_send, and ask it to reply to you.
```

**claude pane in T4**

```text
Ask codex@alpha for a one-word greeting, using ratatoskr_send, and ask it to reply to you.
```

**T2 gateway admin**

```sh
rtk gateway log --limit 10
```

Expect:

- The message appears in the recipient's pane, and the reply comes back. `accepted` means the harness took it, not that the model read it.
- F3 in a window shows received messages, and Tab shows what this identity sent.
- Each message appears in `rtk gateway log`. To see one in full, run `rtk gateway show ID`.
- A bare `codex` only means the agent on the sender's own identity. Other identities need the full `name@host`.

## 8. Try several windows

**T5 spare window on alpha**

```sh
rtk attach
```

Expect:

- Both headers in T5 say `VIEWER`. Typing doesn't reach the agent.
- F6 in T5 takes input for the focused pane. It becomes `INPUT HOLDER`, and the same pane in T3 becomes `VIEWER`.
- Ctrl+C in a `VIEWER` pane detaches that window at once, and the agents keep running.
- Press F10 in T5, then run `rtk daemon status` there. The session T5 held shows `no holder`, and T3 stays a viewer of it until you press F6 in T3.

## 9. Close the window: the point of this work

Close the T3 terminal window while attached. Open a new one, and give it the T3 lines again.

**new T3 alpha**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s1/alpha"
rtk daemon status
rtk attach
```

Expect:

- `rtk daemon status` still lists `claude` and `codex`.
- `rtk attach` shows the same agents on their current screens.
- Detach, ask an agent in T4 to message `claude@alpha` while no alpha window is open, then attach again. The message arrived or is held, and F3 shows it.

## 10. Check the failure cases

Keep T3 attached, so that you can type into its Claude pane. Detach T4 with F10. T5 should be at a shell prompt from the last step. Run these one at a time.

**T4 beta**

```sh
rtk daemon stop --session codex
```

**claude pane in T3**

```text
Send codex@beta a short test message using ratatoskr_send, and tell me the exact result.
```

**T4 beta**

```sh
cd "$HOME/rtk-test/s1/work/beta-codex"
rtk attach --agents codex
```

**claude pane in T3**

```text
Send nobody@beta a short test message using ratatoskr_send, and tell me the exact result.
```

**T5 spare window on alpha**

```sh
rtk attach --agents claude,claude
```

**T5 spare window on alpha**

```sh
rtk
```

Expect:

- Stopping codex on beta prints `session ended: codex`, and `claude@beta` keeps running.
- The send to `codex@beta` gets `session_gone`.
- The new `codex@beta` is a new session and never gets the earlier message. **Note whether Codex asks to review its hooks again.** It may ask once for each state directory, and shouldn't ask on later starts.
- The send to `nobody@beta` gets `unknown_recipient`, with a list of known agents.
- `--agents claude,claude` is refused with `duplicate agent`.
- Plain `rtk` under alpha's state is refused: `another ratatoskr is already running`.

## 11. Stop and restart the gateway

Press Ctrl+C in T1, watch a window, then start it again.

**T1 gateway**

```sh
rtk gateway --listen 127.0.0.1:7791
```

Expect:

- With the gateway down, the title says `Gateway reconnecting — sends unavailable`, the agents keep running, and a send returns `gateway_unreachable`.
- After the restart the title returns to `Gateway connected`. The failed message doesn't arrive, because nothing is resent.

## 12. Stop, restart, clean up

Detach T3 and T4 first. Stop T1 with Ctrl+C before the last command. The `rm` removes only this scenario's folder, including its messages and test credentials, so leave it out if you want to look through them. The test binary stays in `~/rtk-test/bin`.

**T3 alpha**

```sh
rtk daemon stop
```

**T3 alpha**

```sh
rtk daemon stop --yes
rtk daemon
cd "$HOME/rtk-test/s1/work/alpha-claude"
rtk attach --agents claude
```

**T3 alpha**

```sh
rtk daemon stop --yes
```

**T4 beta**

```sh
rtk daemon stop --yes
```

**T2 gateway admin**

```sh
rm -rf "$HOME/rtk-test/s1"
```

Expect:

- `rtk daemon stop` lists the agents and refuses without `--yes`.
- `--yes` prints `daemon stopped` once the process has gone, with no agent left running. If it says `still finishing its shutdown`, tell the team.
- The restarted Claude is a new session, with no memory of the last one and no old messages. If `rtk attach` says the gateway has not been reached yet, the daemon has only just started: run it again.
- Your Claude and Codex logins stay as they were. Codex may keep its records of trusted hooks for these paths.
