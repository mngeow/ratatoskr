# Scenario 4: the Network tab and SSH entry

The Network tab shows every host's sessions inside an attached window, and Enter on a remote session opens it over your own SSH. This builds on a working scenario 2 or 3: the same gateway, the Mac and VM-B, each with a running agent.

**This is the short form, for reading on a VM or on GitHub.** The [full guide](../index.html#s4) has the diagrams, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source.

> **Expected results come from reading the code,** and the team hasn't run these steps, so tell the team when something differs. Replace each `<PLACEHOLDER>`.

> **None of this has ever run.** The API, the tab and SSH entry were written and checked only by `gofmt`, `go vet` and `go build`. Expect defects, and tell the team what you see. The first runs are the point of this scenario. It uses your real SSH keys and config, and `rtk` adds no SSH policy of its own.

## 1. Before you start

Do scenario 2 or 3 up to its agents step first, then stop everything and rebuild. Every machine needs the new build, because the gateway, each daemon and each window must match.

- **Stop the old daemons and the gateway.** `rtk daemon stop --yes` on the Mac and on VM-B ends their agents. Ctrl+C the gateway.
- **SSH works by hand first.** From the Mac, `ssh <VMB_ADDR>` must log in with the key or agent you normally use, from that terminal, with any host-key or passphrase question answered. If it doesn't, SSH entry can't either.
- **The remote `rtk` is found by absolute path,** because a non-interactive SSH command often has no `~/.local/bin` on its path. Find it on VM-B with `command -v rtk` after the `export` lines.
- **Old `./rtk` in the repo:** any `./rtk` there predates the final code. Build fresh with a new identifier.

## 2. Build and copy to all three machines

Use a **new** build identifier, because the window check can't tell two builds with one name apart.

**M1 Mac**

```sh
cd "<REPO>"
mkdir -p "$HOME/rtk-test/bin"
go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk" ./cmd/rtk
GOOS=linux GOARCH=<VM_ARCH> go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk-linux" ./cmd/rtk
scp "$HOME/rtk-test/bin/rtk-linux" <GW_SSH>:rtk-test/bin/rtk
scp "$HOME/rtk-test/bin/rtk-linux" <VMB_SSH>:rtk-test/bin/rtk
```

Expect:

- `rtk --version` prints your new identifier on all three machines.

## 3. Start the gateway and both daemons, with agents

Repeat scenario 2 or 3 from "Start the gateway" through "Start one agent on each". Your earlier pairing is kept if you reuse the same state directories. Detach each window with F10 so both agents keep running.

**M1 Mac**

```sh
rtk daemon network
```

**B1 VM-B**

```sh
rtk daemon network --json
```

Expect:

- A table (Mac) and JSON (VM-B) with `schema: 1`, `local.sessions` listing that machine's agent with a `local_id`, `gateway.state` `connected`, and a `snapshot` listing both hosts.
- A session's `state` is one of `starting`, `idle`, `busy`, `needs_you`, `unknown` or `exited`. **An `unknown` must be shown as unknown, never as idle.**
- No `model` field appears anywhere.
- A host whose daemon has not reported yet reads `no report`, which is not the same as having no sessions.

## 4. Watch it change

This checks the long poll and the time-driven flips.

**M1 Mac**

```sh
rtk daemon network --watch
```

**B1 VM-B**

```sh
rtk attach
```

Expect:

- `--watch` reprints when something changes. Typing into an agent on VM-B (a state going `busy` and back) shows up on the Mac within seconds.
- On a quiet network the ages keep moving and do not freeze.
- Stop VM-B's daemon (`rtk daemon stop --yes` on VM-B): within about 15 to 30 seconds its row goes not live, and its report goes stale. **A stale report is history, not proof anything is running.** Start it again and it recovers.
- Ctrl+C the gateway: `gateway.state` becomes `unreachable`, the last snapshot is kept with its age, and `local` is unaffected. Start the gateway again.

`--json` and the table are the same data. The README's "Network tab and SSH entry" section lists the fields.

## 5. Open the tab

Every attached window shows a `[Terminals] [Network]` strip on its top row. Click Network with the mouse (F5 also works). You do **not** need `rtk daemon network` first: the window fetches from the moment it attaches.

**M1 Mac**

```sh
rtk attach
```

Expect:

- Clicking Network shows the tab at once or within a moment (`waiting for the first answer` first, then data): your Mac's sessions and VM-B's, each with state, how old the fact is, and whether input is held. F5 or Esc returns to the terminals, and the agents never noticed.
- Arrows and PgUp/PgDn move the selection. R refreshes. Your own host's session appears once, not twice.
- Stop the gateway: the tab says so, keeps what it had with its age, and never shows it as live.

## 6. Check the strip, the colours, the clicks and the empty window

These are the checks for the always-visible tab and its styling. **Never run before, never drawn.** Rebuild with a new identifier and restart the daemon first, because a window must match its daemon. Tell the team what differs.

**M1 Mac**

```sh
rtk daemon stop --session claude
rtk attach
```

Expect:

- **Do they look like tabs?** The top row is a dark bar with two touching blocks, ` Terminals ` and ` Network `: the active one a filled green block with dark bold text, the other muted. Say whether they look and click like tabs (you asked for Herdr-style), and whether the Monokai colours are what you meant. Also try 40 columns, a narrow window, a light terminal theme (expect a seam against the panes), `NO_COLOR=1 rtk attach` (no colours; the active tab is bold and reverse with a `>`), a 256-colour terminal and a window over ssh (colours or symbols may differ there).
- On the Network tab each state has a symbol and a word (`● idle`, `◐ busy`, `▲ needs you`, `? unknown`, `○ exited`), stale facts are amber with an age, and the selected row is highlighted. Harness panes look exactly as before. Click the left and right padding of each tab, not just its name.
- The top row of every window reads `[Terminals] [Network]`, the active one marked with `>`. In a narrow terminal it shortens to `[T] [N]`.
- Click Terminals, then Network, then Terminals. Each click switches the tab and **types nothing into the agent**: the agent's prompt shows no stray text and the pane header does not change to drafting.
- Press the mouse on the strip and drag down into a pane: the click is not forwarded, and a drag that began in a pane still ends normally.
- With no agent running, plain `rtk attach` opens a window on the Network tab (it says `No sessions in this window`, and Terminals says `rtk attach --agents claude,codex`). It does not start a daemon or an agent. With no daemon at all it still exits with `no daemon`.
- `rtk attach --agents claude` when the start fails exits non-zero with the reason and opens no window.
- A session you start while a window is open appears only after you detach and attach again.

Detaching an empty window, or ending it with a signal, prints `detached; no agents were running`. Windows with agents are unchanged.

## 7. Write the host map on the Mac

Enter needs `hosts.json` in the Mac's state directory. Use the names exactly as `rtk gateway hosts` lists them.

**M1 Mac**

```sh
cat > "$XDG_STATE_HOME/ratatoskr/hosts.json" <<'EOF'
{
  "version": 1,
  "hosts": {
    "vmb": { "ssh": "<VMB_ADDR>", "rtk": "<VMB_RTK>", "state_home": "<VMB_STATE>" }
  }
}
EOF
chmod 600 "$XDG_STATE_HOME/ratatoskr/hosts.json"
ls -l "$XDG_STATE_HOME/ratatoskr/hosts.json"
```

Expect:

- The file isn't group- or world-writable. `rtk` refuses a symlink, a non-regular file, a file owned by someone else, one writable by others, or one over 64 KiB, and says why.
- It is read at each Enter, so you can edit it without a restart.

If your state directory is laid out differently, `rtk daemon status` shows where the daemon keeps its files, and `hosts.json` sits beside `pairing.json`. Check that path first if Enter says there is no mapping.

## 8. Enter a remote session over SSH

In the Mac's window click Network, select the `codex` row on `vmb`, and press Enter.

**M1 Mac**

```sh
rtk attach
```

Expect:

- The window closes and prints `opening codex@vmb over ssh. F10 inside returns here.`
- Your SSH config, host-key question, passphrase or Tailscale check shows in this same terminal, as with any `ssh`. `rtk` sets none of those.
- You land in a window on VM-B attached to **that exact session**. Its Network tab disables Enter for other hosts, saying to press F10 to return first.
- F10 returns to a fresh Mac window on the Network tab, on the same row, with the data refetched. The agents on both machines kept running.

The command run is `ssh -t -o ConnectTimeout=5 -- <ssh> '<rtk> attach --expect-session <id> --entered-via-ssh'`. Nothing from the gateway becomes a destination, a path or an option.

## 9. Try the endings, and check the terminal each time

After each one, check the Mac terminal is normal: typed characters echo, the cursor is visible, the mouse doesn't print escape codes, and Enter and Ctrl+C behave. If it isn't normal, type `reset` and **tell the team which ending did it.**

**B1 VM-B**

```sh
rtk daemon stop --session codex
```

**B1 VM-B**

```sh
rtk daemon stop --yes
```

Expect:

- **Ended or replaced session (exit 3):** stop codex on VM-B, then press Enter on its stale row on the Mac. It says the session ended or was replaced and opens nothing.
- **No daemon (exit 4):** stop VM-B's daemon, then Enter. It says there is no rtk daemon there.
- **Unreachable or wrong address (exit 255):** break the `ssh` value in `hosts.json`. `rtk` says ssh couldn't connect or the connection ended, and prints the command to type by hand. It can't tell unreachable, timeout, host key, auth and a dropped link apart, so read `ssh`'s own text above it.
- **Missing mapping:** remove the `vmb` entry. Enter shows the command to run on that machine, and runs nothing.
- **Too old remote (exit 2):** only if you put an older build on VM-B. It says to upgrade.

Each ending that exits non-zero should restore the terminal exactly and write a targeted reset, never a full clear. A clean exit needs neither.

## 10. The harder cases

These are the ones most likely to expose the terminal handover. Do them last.

**M1 Mac**

```sh
rtk attach
```

Expect:

- **`kill` of `rtk`:** Enter a session, then from another terminal `kill` the Mac's `rtk` process (not `ssh`). `ssh` should stop, no new window should open, and the terminal should be normal.
- **`kill -9` of `ssh`:** Enter a session, then `kill -9` the `ssh` process. The terminal should be restored and you should land back on the tab with a message.
- **Ctrl+C while connecting:** Enter a session and press Ctrl+C before it connects. `ssh` should be interrupted, not ignored.
- **Typeahead at the handover:** type quickly while pressing Enter on a row. Anything typed during the 600 ms wait is dropped. A late terminal reply may still leak. **Tell the team if stray characters appear.**
- **Nothing left running:** detach and end every local session, then Enter on a remote row and F10 back. If no local session runs, `rtk` prints the usual no-sessions guidance and exits.

The 600 ms wait is based on reading Bubble Tea's source, not on any test, so these checks are real unknowns.

## 11. Stop and clean up

Removes only this guide's folders, like the other scenarios.

**M1 Mac**

```sh
rtk daemon stop --yes
```

**B1 VM-B**

```sh
rtk daemon stop --yes
```

Expect:

- `daemon stopped` on each. Stop the gateway with Ctrl+C, and remove `hosts.json` if you no longer want it.
