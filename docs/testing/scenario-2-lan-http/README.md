# Scenario 2: across machines on a LAN, plain HTTP

A gateway on one Ubuntu VM, a daemon on a second, and a daemon on your Mac, all talking in the clear over your LAN.

**This is the short form, for reading on a VM or on GitHub.** The [full guide](../index.html#s2) has the diagrams, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source.

> **Expected results come from reading the code,** and the team hasn't run these steps, so tell the team when something differs. Replace each `<PLACEHOLDER>`.

> **Plain HTTP is readable on this network.** Anyone who can see the traffic can read messages, the pairing code and the credential, and anyone who can redirect it can inject messages into your agents. Use it only on a LAN you trust. If you take the Mac elsewhere, stop its daemon first, because it keeps trying to reach this address and would send its credential to whatever answers.

## 1. Before you start

Check these, on all three machines.

- **Same network:** the Mac and VM-B can reach the gateway VM, and you can SSH to both VMs with a key or password.
- **Harnesses:** Claude Code on the Mac, and Codex on VM-B, each signed in by running it once. The gateway VM needs neither.
- **Anything already running:** quit any old `rtk runtime` windows, and stop any old `rtk gateway` with Ctrl+C in its own terminal. A gateway still using this address and port makes the bind fail. The new protocol also refuses old runtimes.
- **Existing state:** this guide doesn't touch your old pairings or databases, because it uses its own folders under `~/rtk-test`.
- **Find the gateway's LAN IP** on that VM with `ip -4 addr`. Give it a fixed address with a router DHCP reservation, because hostnames aren't accepted, and a new address means pairing again.

## 2. Build with one identifier on all three machines

**One identifier you choose, on all three machines,** set explicitly rather than generated on each. Your checkout should be up to date with `main`. Then use one of the two ways below. **The first is simpler, because the VMs don't need Go.** Run `uname -m` on a VM if you're unsure of its architecture: `x86_64` is `amd64`, and `aarch64` is `arm64`.

**M1 Mac**

```sh
cd "<REPO>"
mkdir -p "$HOME/rtk-test/bin"
go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk" ./cmd/rtk
GOOS=linux GOARCH=<VM_ARCH> go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk-linux" ./cmd/rtk
ssh <GW_SSH> 'mkdir -p "$HOME/rtk-test/bin"'
scp "$HOME/rtk-test/bin/rtk-linux" <GW_SSH>:rtk-test/bin/rtk
ssh <VMB_SSH> 'mkdir -p "$HOME/rtk-test/bin"'
scp "$HOME/rtk-test/bin/rtk-linux" <VMB_SSH>:rtk-test/bin/rtk
```

**Or, on each VM with Go 1.27.3: clone and build**

```sh
git clone https://github.com/mngeow/ratatoskr.git "$HOME/rtk-test/src"
cd "$HOME/rtk-test/src"
mkdir -p "$HOME/rtk-test/bin"
go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk" ./cmd/rtk
```

**Why the identifier matters, and what it doesn't do.** A window must match the daemon on its own machine exactly, so a rebuild needs a new identifier and a daemon restart there. The gateway doesn't compare builds: between machines, the only check is gateway protocol 2. Using one identifier everywhere is how you can tell that every machine has the same code. If you clone, build the Mac's copy from the same commit, using the first block's `go build` line.

## 3. Open your terminals

Each one needs these lines, which keep this test apart from anything already on that machine. Run the gateway inside `tmux` on its VM, because a gateway stops when its terminal goes away. It isn't a daemon.

**G1 gateway VM**

```sh
ssh <GW_SSH>
```

**G1 gateway VM**

```sh
tmux new -s rtk-gateway
```

**G1 and G2 on the gateway VM**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s2/gateway"
rtk --version
```

**G2 gateway admin**

```sh
ssh <GW_SSH>
```

**B1 VM-B**

```sh
ssh <VMB_SSH>
```

**B1 VM-B**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s2/vmb"
mkdir -p "$HOME/rtk-test/s2/vmb" "$HOME/rtk-test/s2/work/codex"
rtk --version
```

**M1 Mac**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s2/mac"
mkdir -p "$HOME/rtk-test/s2/mac" "$HOME/rtk-test/s2/work/claude"
rtk --version
```

Expect:

- `rtk --version` prints your build identifier on all three machines.

In G2, run the `ssh` line first and then the `export` lines. On the Mac, use an ordinary terminal rather than a pane belonging to OpenRig or another tool.

## 4. Start the gateway

Leave it running. To leave `tmux` without stopping it, press Ctrl-b then d. To get back, run `tmux attach -t rtk-gateway`.

**G1 gateway VM**

```sh
mkdir -p "$HOME/rtk-test/s2/gateway"
rtk gateway --listen <GW_LAN_IP>:<GW_PORT> --allow-lan --insecure-http
```

Expect:

- `gateway listening on ...`
- The `WARNING: plain HTTP on the LAN` line.
- A reminder to check the firewall if a machine can't reach this port.

**If a machine can't connect,** check that port is open. Only if `sudo ufw status` says active on the gateway VM, allow your LAN with `sudo ufw allow from <your LAN CIDR> to any port <port> proto tcp`. That changes the VM's firewall, so you may want to undo it afterwards. `rtk` never does this itself. `nc -vz <gateway IP> <port>` from the Mac checks that the port is reachable.

## 5. Pair the Mac and VM-B

Each `pair` prints a one-time code that expires in 10 minutes. **Its last line starts with `on that machine: `. Copy only what follows that**, starting at `rtk runtime pair`, and run it on the machine named.

**G2 gateway admin**

```sh
rtk gateway pair mac
```

**M1 Mac**

```sh
rtk runtime pair --gateway <GW_LAN_IP>:<GW_PORT> --code <MAC_CODE> --insecure-http
```

**G2 gateway admin**

```sh
rtk gateway pair vmb
```

**B1 VM-B**

```sh
rtk runtime pair --gateway <GW_LAN_IP>:<GW_PORT> --code <VMB_CODE> --insecure-http
```

Expect:

- `paired as mac with gateway ...`, and the same for `vmb`.
- `credential saved in ...` names a path under `~/rtk-test`.
- Pairing prints the plain HTTP warning, on both machines.

## 6. Start both daemons

**M1 Mac**

```sh
rtk daemon
rtk daemon status
```

**B1 VM-B**

```sh
rtk daemon
rtk daemon status
```

**G2 gateway admin**

```sh
rtk gateway hosts
```

Expect:

- `daemon started (pid N)`, the log path, and `harnesses will inherit the environment of this terminal`.
- `rtk daemon status` shows `link: connected` on both. If not, it shows `link error:` with the reason.
- `rtk gateway hosts` shows `mac` and `vmb` as `live`.

**VM-B's agents keep the environment of this SSH session,** including `SSH_AUTH_SOCK`, which stops working when the session ends. So `git` over SSH from an agent can start failing later.

## 7. Start one agent on each

Sign in to each harness on its machine first, if you haven't. Answer any trust or hook-review questions in the pane. Each Codex launch costs one model turn.

**M1 Mac**

```sh
cd "$HOME/rtk-test/s2/work/claude"
rtk attach --agents claude
```

**B1 VM-B**

```sh
cd "$HOME/rtk-test/s2/work/codex"
rtk attach --agents codex
```

Expect:

- The pane's header starts with `INPUT HOLDER`.
- The title says `Gateway connected (plain HTTP)`.

Want a second copy of a harness across machines? Log in on VM-B, then run `rtk attach --agents claude` from another directory there. The Mac's is `claude@mac`, and that one is `claude@vmb`.

## 8. Send a message each way

Type these into the panes.

**claude pane on the Mac**

```text
Ask codex@vmb what machine it is running on, using ratatoskr_send, and ask it to reply to you.
```

**codex pane on VM-B**

```text
Ask claude@mac what machine it is running on, using ratatoskr_send, and ask it to reply to you.
```

**G2 gateway admin**

```sh
rtk gateway log --limit 10
```

Expect:

- The message appears in the recipient's pane, and the reply comes back.
- A permission prompt appears only on that agent's own machine.
- Each message is in `rtk gateway log`. To see one in full, run `rtk gateway show ID`, which prints its text.

## 9. Try a second window, and drop SSH

This is the point of the work on a server.

**B2 second SSH to VM-B**

```sh
ssh <VMB_SSH>
```

**B2 second SSH to VM-B**

```sh
export PATH="$HOME/rtk-test/bin:$PATH"
export XDG_STATE_HOME="$HOME/rtk-test/s2/vmb"
rtk attach
```

Expect:

- B2's header says `VIEWER`, and typing doesn't reach Codex. F6 takes input, and B1 becomes `VIEWER`.
- Close the B1 terminal without detaching, then close B2 as well. Reconnect, repeat the lines above, and `rtk daemon status` still lists `codex`. `rtk attach` returns to it.
- Ask `claude@mac` to message `codex@vmb` while no window is open on VM-B. The message arrives or is held, and shows when you attach.

Whether the daemon survives a macOS logout or a VM reboot isn't promised. A reboot ends it.

## 10. Check the failure cases

Detach on VM-B first, then run these one at a time. Ask `claude@mac` to send `codex@vmb` a short test message after each, and to tell you the exact result.

**B1 VM-B**

```sh
rtk daemon stop --session codex
```

**B1 VM-B**

```sh
cd "$HOME/rtk-test/s2/work/codex"
rtk attach --agents codex
```

**B1 VM-B**

```sh
rtk daemon stop --yes
```

**B1 VM-B**

```sh
rtk daemon
rtk attach --agents codex
```

**G2 gateway admin**

```sh
rtk gateway unpair vmb
```

Expect:

- After `--session codex`: `session ended: codex`, and the send gets `session_gone`.
- After `--agents codex`: a new session, which never gets the earlier message. **Note whether Codex asks to review its hooks again.**
- After `stop --yes`: `daemon stopped`, and the send gets `destination_unreachable`, with the text in the gateway's log.
- After a restart, the earlier message doesn't arrive. If `rtk attach` says the gateway has not been reached yet, run it again.
- Stop the gateway with Ctrl+C in G1: the title says `Gateway reconnecting — sends unavailable`, the agents keep running, and a send gets `gateway_unreachable`. Start it again with the same command, and nothing is resent.
- Cut VM-B's network and send at once: `delivery_uncertain` for up to about 20 seconds, then `destination_unreachable`.
- After `unpair vmb`: within seconds VM-B's title says `Gateway refused — retrying`, and its sends fail with the reason. Do this last, because VM-B then needs pairing again.

All these time limits are guesses, so tell the team if one feels wrong.

## 11. Stop and clean up

Each `rm` removes only this scenario's folder on that machine, including its messages and test credentials. Leave it out to keep them. Stop the gateway with Ctrl+C in G1, then type `exit` to close `tmux`.

**M1 Mac**

```sh
rtk daemon stop --yes
rm -rf "$HOME/rtk-test/s2"
```

**B1 VM-B**

```sh
rtk daemon stop --yes
rm -rf "$HOME/rtk-test/s2"
```

**G2 gateway admin**

```sh
rm -rf "$HOME/rtk-test/s2"
```

Expect:

- `daemon stopped` on each, or `no daemon is running` if you'd already stopped it. Plain `rtk` on the Mac then opens as usual.
