# Scenario 5: host cards, scheduled polling and the Graph tab

Each daemon can publish a read-only card about its host, the gateway polls it on a schedule and keeps it in its database, and a third Graph tab draws the hosts. This builds on the machines of scenario 2 or 3: a gateway VM, the Mac and VM-B, each with a running agent.

**This is the short form, for reading on a VM or on GitHub.** The [full guide](../index.html#s5) has the diagrams, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source.

> **Expected results come from reading the code,** and the team hasn't run these steps, so tell the team when something differs. Replace each `<PLACEHOLDER>`.

> **None of this has ever run, and one step changes a real database.** The code is written and was checked only by `gofmt`, `go vet` and `go build`. **The gateway migration has never run against any database.** Copy the gateway's whole state directory first, by hand, and try a copy before your real one. The built-in backup is also unproven. Everything below is "written", never "verified". Whether it looks right is your call.

## 1. Protect your data first

This is the only scenario step that can damage anything, so do it before you build.

**G2 gateway admin**

```sh
rtk gateway hosts
# then stop the gateway with Ctrl+C in its terminal
cp -a "<GW_STATE>" "<GW_STATE>.copy-before-cards"
ls -l "<GW_STATE>.copy-before-cards"
```

Expect:

- A full copy of the gateway state directory exists, with its database file and pairing files, and the gateway is stopped.
- Do the migration steps below on a **copy** of the directory first if you can (point `--state` at the copy). Do not rely on the automatic backup alone.

- **Restarting a daemon ends its agents.** Rebuilding means stopping each daemon with `rtk daemon stop --yes`.
- **Skill scan:** a new daemon scans your user-scope Claude and Codex skill folders by itself at start, in the background, whether or not the card listener is on. It reads files and runs `<harness> --version`, and publishes nothing.

## 2. Build and copy to all three machines

Use a **new** build identifier, because the window check cannot tell two builds with one name apart.

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

## 3. Run the unit tests (the first time anything runs)

About 90 test functions were written and only type-checked. Run them before trusting anything else.

**M1 Mac**

```sh
cd "<REPO>"
go test ./internal/gateway/ ./internal/card/ ./internal/skillscan/ ./internal/daemon/ ./internal/link/ ./internal/tui/ ./cmd/rtk/
go test -tags rtktest ./internal/gateway/ ./internal/daemon/ ./internal/tui/
go test -race ./internal/gateway/ ./internal/daemon/
```

Expect:

- Everything passes. **Any failure is a defect to report**, with the output. These tests build temporary databases and never touch yours.

## 4. An old database is refused by an admin command

Run an admin command with the new binary against the **old, still version 1** database.

**G2 gateway admin**

```sh
rtk gateway hosts
```

Expect:

- A refusal that says the database needs upgrading by the gateway and to start it once. The database is unchanged (compare it with your copy). Admin commands never migrate an existing database.

## 5. Start the gateway: the migration

Starting the gateway upgrades an existing database. **This is the step that has never run.** Use the copied state directory first if you can.

**G1 gateway**

```sh
rtk gateway --listen <the address you used before> --state "<GW_STATE>"
```

Expect:

- The gateway prints that it migrated, from version 1 to 2, and the path of a backup next to the database, named `...bak-v1-<time>`.
- The backup is **not empty**, has mode 0600, and opens as version 1. **This tests the one assumption only a run can settle:** that `VACUUM INTO` accepts the empty file created first. If it fails, the gateway stops with a message naming the path, and the database is unchanged.
- Your hosts, pairings and message log match the copy: `rtk gateway hosts`, `rtk gateway log`. An **older** gateway binary now refuses the database as newer than itself. Rollback is: stop, put the `.bak` file back, run the older binary.

The migration runs before the gateway binds its address. A gateway that migrates and then cannot bind stays migrated, so an older binary then needs the backup.

## 6. Start the daemons, with the card listener on the Mac

The card listener is **off by default**. Turning it on later means stopping the daemon, which ends its agents. Use a loopback or Tailscale address; a LAN address needs `--allow-lan`, and plain HTTP on a LAN needs `--insecure-http`. No public address is accepted.

**M1 Mac**

```sh
rtk daemon --card-listen <MAC_IP>:<CARD_PORT>
```

**B1 VM-B**

```sh
rtk daemon --card-listen <VMB_IP>:<CARD_PORT>
```

Expect:

- Each daemon starts as before and says its card listener address. Start one agent on each as in scenario 2 or 3.
- Bad choices are refused with a reason and nothing bound: a public address, a LAN address without `--allow-lan`, and `--card-listen` while a daemon is already running.

## 7. Write and publish the Mac's card

Editing exists only on the host that owns the card. Use the Graph tab's editor, or the command line.

**M1 Mac**

```sh
printf "Ratatoskr host daemon on the Mac." > "$HOME/rtk-test/card-description.txt"
rtk daemon card validate
rtk daemon card publish --name mac --description-file "$HOME/rtk-test/card-description.txt"
rtk daemon card show
rtk daemon card history
```

Expect:

- `validate` and `publish` succeed and `show` prints revision 1. Publishing a token-like string or a path in the description is **refused** with a readable reason (try it). The word "bearer" alone also blocks, which is a known false positive.
- The command line cannot declare skills. Only the window's editor can. A card **cannot be published without a description**: `rtk daemon card publish` stops and points to `--description-file PATH` (or `-`). A declared skill needs a description you wrote and at least one tag.

## 8. Read the card as anyone could

The card is public: no login. Check what a stranger would see.

**M1 Mac**

```sh
curl -i http://<MAC_IP>:<CARD_PORT>/.well-known/agent-card.json
curl -i http://<MAC_IP>:<CARD_PORT>/.well-known/agent.json
curl -i -X POST http://<MAC_IP>:<CARD_PORT>/.well-known/agent-card.json
curl -i http://<MAC_IP>:<CARD_PORT>/other
```

Expect:

- GET returns the card with an `ETag`; repeating it with `If-None-Match` gives **304**. HEAD works. **POST gives 405**, any other path **404**, and before anything is published **503**. `agent.json` returns the same bytes as a compatibility alias.
- Read the JSON for anything that should not be public: no tokens, credentials, skill bodies, absolute paths, usernames or environment values. Its one interface points at the card's own URL with binding `RATATOSKR-CARD`. It is "A2A-style", **not** a claim of A2A conformance, because the official proto source has not been read.

## 9. Tell the gateway where to poll

Only you, on the gateway host, decide where it polls. The target is never read from a card. Targets are IP literals only: a hostname, including a Tailscale MagicDNS name, is refused.

**G2 gateway admin**

```sh
rtk gateway card-target set mac http://<MAC_IP>:<CARD_PORT>
rtk gateway card-target set vmb http://<VMB_IP>:<CARD_PORT>
rtk gateway card-target show
```

Expect:

- Both targets are listed. http is accepted on loopback and Tailscale addresses only. These are refused with a reason: a hostname, `100.100.100.100`, a LAN http address without `--insecure-http`, and a LAN https address without `--fingerprint`. (`100.100.100.200` is also on the deny list but that entry is unverified.)
- Changing a target clears that host's stored card, because the old card came from another endpoint.

## 10. Polling with nothing watching

The gateway polls on a schedule (default 60 seconds, `--card-poll-interval` from 10 seconds to 1 hour), whether or not any window is open.

**G2 gateway admin**

```sh
rtk gateway card-target show
```

Expect:

- After a minute or two, without opening any tab, each host shows a recent good poll and a stored card.
- Stop VM-B's card listener (stop that daemon): the card goes **stale**, then **offline** after the larger of 15 minutes and four intervals. **The connection signal (stream and heartbeat) must not change because of it.** And stopping VM-B's heartbeat must not change the card signal. A timeout means the poll failed. It does not prove the process died.

## 11. Restart the gateway

A stored card is a record of what the gateway last saw. It is not proof that anything is up.

**G1 gateway**

```sh
rtk gateway --listen <the same address> --state "<GW_STATE>"
```

Expect:

- No second migration. In the Graph tab each host reads as restored from the database, not yet polled, until its first poll. A host becomes fresh only from a new poll.
- Change a LAN certificate and polling for that host pauses until the identical `card-target set` is run again. Unpair a host: its card, revisions and attempts go and the message log stays.

## 12. The Graph tab

Inside any attached window.

**M1 Mac**

```sh
rtk attach
```

Expect:

- The strip shows ` Terminals `, ` Network ` and ` Graph ` as touching blocks (27 cells wide, ` T  N  G ` when narrow). Click Graph. **F7** is an optional shortcut and takes a key a pane received before. A window with no sessions still opens on Network; Graph is one click away.
- At 120 by 26 or more, up to 12 hosts show as a grid; smaller, or more hosts, show a list. Each host is marked `● fresh`, `… stale`, `○ offline`, `? no card` or `✗ rejected` (ASCII `X`). The mark is about the **card**, never about busy or idle. `rejected` means the newest poll was refused (invalid card, oversize body or changed certificate); the last good card is kept and the reason says which.
- Message lines are **sends the gateway decided**, including failed ones, never "delivered". Poll lines show the last poll result and link no two hosts together.
- Select a host: Card, Skills, Security, Capabilities and Raw JSON. A remote host says it is read-only and to edit on that host. The local host shows the editor: change the description, declare a skill you choose, validate, see the diff, publish, and look at history and rollback. A conflict keeps your edits. The draft key says drafting is **not configured**: no model is ever called.
- Typing letters or pasting while in Graph reaches no pane. The URL panel lets you select the URL natively; the copy button says "request sent", never "copied".
- With colour off (`NO_COLOR=1`) the marks and the active tab keep their widths. An older gateway shows "gateway too old". If the graph is too large, card bodies are omitted and the view says so.

## 13. Stop and clean up

Removes only this guide's folders. Keep the copy of the gateway state until you are happy with the migration.

**M1 Mac**

```sh
rtk daemon stop --yes
```

**B1 VM-B**

```sh
rtk daemon stop --yes
```

Expect:

- `daemon stopped` on each. Stop the gateway with Ctrl+C. To roll back the migration: stop the gateway, restore the `.bak` file (or your copy), and run the older binary.
