# Scenario 6: A2A tasks between sessions and an external service

The gateway serves a standard A2A v1.0 endpoint for each participant. A managed Claude on the Mac asks a managed Codex on another machine for a review, Codex asks one question, Claude answers, and Codex reports a result. Then a small external service, the reference adapter, takes part the same way. This builds on the machines of scenario 2 or 3: a gateway VM, the Mac and VM-B, each with a running agent.

**This is the short form, for reading on a VM or on GitHub.** The [full guide](../index.html#s6) has the diagrams, fill-in fields and copy buttons. Open it from a local checkout, because GitHub shows `.html` as source.

> **Expected results come from reading the code,** and the team hasn't run these steps, so tell the team when something differs. Replace each `<PLACEHOLDER>`.

> **None of this has ever run, and one step changes a real database.** The A2A code is written and was checked only by `gofmt`, `go vet` and `go build`; its tests were written and type-checked, never executed. **Migration 0003 has never run against any database.** Copy the gateway's whole state directory first, and try a copy before your real one. The SDK's actual result shapes, the daemon-to-gateway task flow and the external-service mirror have never run end to end, so expect first-run fixes. Everything below is "written", never "verified". **Also read this:** the gateway sends a per-registration secret to an external service, and the reference adapter checks it. That path has never run. The adapter's `--no-auth` mode is a demo with no authentication, not the authenticated path. Bind the adapter to loopback or your tailnet address either way.

## 1. Protect your data first

This is the only step that can damage anything, so do it before you build. You are on the branch `feat/a2a-participants`.

**G1 gateway VM**

```sh
rtk gateway hosts
```

**G2 gateway VM**

```sh
cp -a "<GW_STATE>" "<GW_STATE>.copy-before-a2a"
ls -l "<GW_STATE>.copy-before-a2a"
```

Expect:

- The first command shows your paired hosts. Stop the gateway (Ctrl+C in its terminal) **before** the copy.
- A full copy of the gateway state directory exists, with its database and pairing files.
- Try the migration step on a copy if you can (point `--state` at the copy).

- **Restarting a daemon ends its agents.** Rebuilding means `rtk daemon stop --yes` on each machine.
- Nothing here uses your real project: start the agents in a scratch directory.

## 2. Build and copy to all three machines

Use a **new** build identifier. The adapter is a separate module in `integrations/a2a-reference`.

**M1 Mac**

```sh
cd "<REPO>"
git branch --show-current
mkdir -p "$HOME/rtk-test/bin"
go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk" ./cmd/rtk
GOOS=linux GOARCH=<VM_ARCH> go build -ldflags "-X main.version=<BUILD_ID>" -o "$HOME/rtk-test/bin/rtk-linux" ./cmd/rtk
(cd integrations/a2a-reference && GOOS=linux GOARCH=<VM_ARCH> go build -o "$HOME/rtk-test/bin/a2a-reference" .)
scp "$HOME/rtk-test/bin/rtk-linux" <GW_SSH>:rtk-test/bin/rtk
scp "$HOME/rtk-test/bin/rtk-linux" <VMB_SSH>:rtk-test/bin/rtk
scp "$HOME/rtk-test/bin/a2a-reference" <VMB_SSH>:rtk-test/bin/a2a-reference
```

Expect:

- `git branch --show-current` prints `feat/a2a-participants`.
- `rtk --version` prints your identifier on all three machines.

## 3. Run the unit tests (the first time anything runs)

Many tests were written and only type-checked. Run them before trusting anything else.

**M1 Mac**

```sh
cd "<REPO>"
go test -race ./internal/a2a/ ./internal/gateway/ ./internal/link/ ./internal/server/ ./internal/daemon/ ./cmd/rtk/
(cd integrations/a2a-reference && go test ./...)
```

Expect:

- Everything passes. **Any failure is a defect to report**, with the output. These tests build temporary databases and never touch yours.

## 4. Start the gateway: migration 0003

Starting the gateway upgrades an existing database. **This has never run.** Use the copied state directory first if you can.

**G1 gateway VM**

```sh
rtk gateway --listen <GW_ADDR> --state "<GW_STATE>"
```

Expect:

- The gateway reports a migration and the path of a backup next to the database. Your hosts, pairings and message log match the copy (`rtk gateway hosts`, `rtk gateway log`).
- An older gateway binary now refuses the database. To go back: stop, restore the `.bak`, run the older binary.

The migration adds tables only (`a2a_participants`, `a2a_tasks`, `a2a_observations`, `a2a_dispatches`). It runs before the gateway binds its address.

## 5. Start the daemons and one agent on each machine

As in scenario 2 or 3: pair, start the daemon, attach. Claude on the Mac, Codex on VM-B.

**M1 Mac**

```sh
rtk daemon
rtk daemon status
rtk attach --agents claude
```

**B1 VM-B**

```sh
rtk daemon
rtk daemon status
rtk attach --agents codex
```

Expect:

- Both daemons say the gateway link is connected, and each pane shows `claude@mac` / `codex@vmb` (your names).
- Start the agents in a scratch directory.

## 6. Start the reference adapter and read its card

The adapter is a tiny model-free A2A server that counts lines, words and characters. It accepts only requests carrying the gateway's secret. For now it gets a placeholder token file; you replace the contents in step 7. Bind it to loopback or a tailnet address.

**B2 VM-B**

```sh
umask 077
printf "placeholder-token\n" > "$HOME/rtk-test/a2a-gateway-token"
"$HOME/rtk-test/bin/a2a-reference" --listen <VMB_IP>:<ADAPTER_PORT> --url http://<VMB_IP>:<ADAPTER_PORT>/ --gateway-token-file "$HOME/rtk-test/a2a-gateway-token"
```

**M2 Mac**

```sh
curl -i http://<VMB_IP>:<ADAPTER_PORT>/.well-known/agent-card.json
curl -i -X POST http://<VMB_IP>:<ADAPTER_PORT>/ -H "Content-Type: application/json" -H "A2A-Version: 1.0" -d '{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}'
```

Expect:

- The card (no credential needed) is JSON with a name, a description, one `JSONRPC` interface with protocol version `1.0`, `capabilities` all false, and a bearer scheme: `"securitySchemes":{"bearer":{"httpAuthSecurityScheme":{"scheme":"Bearer"}}}` with a matching `securityRequirements`.
- The second command, with no credential, gets **HTTP 401** with `WWW-Authenticate: Bearer` and the body `unauthorized`, before any JSON-RPC. A wrong bearer gets the same.
- Started with no token file, no environment variable and no `--no-auth`, the adapter exits with status 2 and listens on nothing. `--no-auth` is a demo: it prints a banner, its card declares no scheme, and the gateway registers it only with `--no-auth` too.
- This is the **adapter's** card. The gateway serves a different card for each participant it hosts.

## 7. Enrol and register the adapter

Enrolment is an owner action on the gateway host. Both secrets are printed **once**: the enrolment credential (what refbot would use to call the gateway) and, at registration, the outbound secret (what the gateway sends to the adapter).

**G2 gateway VM**

```sh
rtk gateway participant enrol refbot
```

**G3 gateway VM**

```sh
rtk gateway participant register refbot --url http://<VMB_IP>:<ADAPTER_PORT>/
rtk gateway participant list
```

**B4 VM-B**

```sh
umask 077
printf "%s\n" "PASTE-THE-OUTBOUND-SECRET-HERE" > "$HOME/rtk-test/a2a-gateway-token"
```

Expect:

- `enrol` prints the participant name and a credential, and says it cannot be recovered. Copy it into the **CRED** field above.
- `register` fetches the adapter's card, checks that it declares an HTTP Bearer scheme, mints the outbound secret, prints it once and prints `registered refbot at ...`. Paste that secret into the token file (the adapter re-reads it on every request, so **no restart is needed**). Registering again mints a new secret and the old one stops working.
- `list` shows `refbot` as active, its URL, and an AUTH column of `bearer`. A card that declares no security is refused unless you pass `--no-auth`, which warns loudly and shows `NONE`. A card that needs OAuth2, OpenID Connect, mutual TLS or an API key is refused. A plain-HTTP LAN address is refused unless you add `--insecure-http`, and then a warning says the secret and every task cross that network in the clear; https needs `--fingerprint`; loopback and tailnet addresses need neither.
- The secret never appears in `list`, in `--json` output, in any log, or in any error text. Check that.

`register` also refuses a public address, a hostname, a redirect and an oversize card. Try a wrong port once and read the refusal.

## 8. Find a session id and its endpoint

The endpoint of a managed session is its session id.

**M1 Mac**

```sh
rtk daemon network
rtk daemon network --json
```

**M2 Mac**

```sh
curl -i http://<GW_ADDR>/a2a/agents/<SESSION_ID>/.well-known/agent-card.json
```

Expect:

- Each session shows its id. Put the **Codex on VM-B** id in **SESSION_ID** above.
- The public card for that session names a `JSONRPC` interface at `/a2a/agents/<id>` and a bearer security scheme. Reading it needs no credential and gives no permission.
- A name that matches more than one live session is refused with the candidates, rather than guessed. (Optional: scenario 1's layout makes two sessions of one harness.)

## 9. The first round trip: ask, clarify, answer, result

You drive it from the two panes. The first prompt goes to Claude on the Mac.

**M3 Claude pane**

```text
Use ratatoskr_task_send to ask codex@vmb to review the retry loop in this directory. Tell me the task id.
```

**B3 Codex pane**

```text
When a task arrives, ask me one clarifying question first, using ratatoskr_task_report with state input_required. Then, when I answer, finish with state completed and a short result.
```

Expect:

- Claude calls `ratatoskr_task_send` and prints a task id at once; the call does not wait for Codex. Put it in **TASK_ID** above.
- Codex's pane receives a labelled prompt: **from claude@mac, verified by the gateway**, with the sender's text quoted between marker lines.
- Codex calls `ratatoskr_task_report` with `input_required`. Claude's pane is **woken** (through the normal readiness gates) with a short "needs input" note, not only a database row. `ratatoskr_task_get` shows `INPUT_REQUIRED` and the question.
- Claude answers with `ratatoskr_task_send` **using the task id**; Codex receives the answer, reports `completed` with a result, and Claude is woken again. `ratatoskr_task_get` shows `COMPLETED` and the result.
- The task became `WORKING` only after Codex's harness accepted the prompt. A turn ending without a report does not complete it. Write down anything that differs.

The three task tools are `ratatoskr_task_send`, `ratatoskr_task_get` and `ratatoskr_task_report`. `ratatoskr_send` is unchanged.

## 10. Authentication, forgery and scoping, with curl

These talk to the endpoint directly, as a standard client would. The JSON shapes follow the A2A v1.0 spec but **have not been checked against the running server**.

**M2 Mac**

```sh
curl -i -X POST http://<GW_ADDR>/a2a/agents/<SESSION_ID> -H "Content-Type: application/json" -H "A2A-Version: 1.0" -d '{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello"}]}}}'
```

**M3 Mac**

```sh
curl -i -X POST http://<GW_ADDR>/a2a/agents/<SESSION_ID> -H "Authorization: Bearer <CRED>" -H "Content-Type: application/json" -H "A2A-Version: 1.0" -d '{"jsonrpc":"2.0","id":2,"method":"SendMessage","params":{"message":{"messageId":"m2","role":"ROLE_USER","parts":[{"text":"hello from refbot"}],"metadata":{"from":"claude@mac"}}}}'
```

**M4 Mac**

```sh
curl -i -X POST http://<GW_ADDR>/a2a/agents/<SESSION_ID> -H "Authorization: Bearer <CRED>" -H "Content-Type: application/json" -H "A2A-Version: 1.0" -d '{"jsonrpc":"2.0","id":3,"method":"GetTask","params":{"id":"<TASK_ID>"}}'
```

Expect:

- No credential: **HTTP 401** with `WWW-Authenticate: Bearer realm="a2a"`. A wrong credential is also refused.
- With refbot's credential the message is accepted and Codex's pane shows it **from ext:refbot**, verified by the gateway. The `from` you put in the body is ignored: the origin is the authenticated principal.
- refbot has no permission to read the task from the first round trip: `GetTask` returns a not-found error (`-32001`), exactly as for a task that does not exist. Its `ListTasks` omits it too.
- Enrolment is not a grant: refbot can message any session, but it can read only tasks it is party to.
- `ListTasks` returns `totalSize` as the number of tasks refbot may see **before** pagination, newest first (by last update), with `nextPageToken` always present and empty on the last page.

## 11. Refusals the standard requires

Each should answer with the spec's own error, not silence.

**M2 Mac**

```sh
curl -s -X POST http://<GW_ADDR>/a2a/agents/<SESSION_ID> -H "Authorization: Bearer <CRED>" -H "Content-Type: application/json" -H "A2A-Version: 0.3" -d '{"jsonrpc":"2.0","id":4,"method":"SendMessage","params":{"message":{"messageId":"m4","role":"ROLE_USER","parts":[{"text":"x"}]}}}'
curl -s -X POST http://<GW_ADDR>/a2a/agents/<SESSION_ID> -H "Authorization: Bearer <CRED>" -H "Content-Type: application/json" -H "A2A-Version: 1.0" -d '{"jsonrpc":"2.0","id":5,"method":"SendStreamingMessage","params":{"message":{"messageId":"m5","role":"ROLE_USER","parts":[{"text":"x"}]}}}'
```

Expect:

- A wrong or empty `A2A-Version` gives `VersionNotSupportedError` (`-32009`).
- `SendStreamingMessage`, `SubscribeToTask` and the extended card give `UnsupportedOperationError` (`-32004`); the push-config methods give `PushNotificationNotSupportedError` (`-32003`).
- `CancelTask` on a task that is still working gives `TaskNotCancelableError` (`-32002`). No canceled state, no interrupt. An unknown task gives `-32001`.
- Try also a parse error, an unknown method and bad params, which give `-32700`, `-32601` and `-32602`.

## 12. End the target mid-task

Start a new task to Codex from Claude, then end Codex before it reports.

**B2 VM-B**

```sh
rtk daemon stop --session codex
```

**M1 Mac**

```sh
rtk daemon network
```

Expect:

- The open task becomes `FAILED` with the reason "session ended". `ratatoskr_task_get` on it shows that.
- A new send to the old session id is refused. It is **not** redirected to a replacement. Start Codex again with `rtk attach --agents codex`: it is a new session with a new id, and the old task stays failed.
- Sending to a target the gateway knows is offline fails at once and creates no task.

## 13. An external service takes part

Claude asks the reference adapter. It asks which format, then answers.

**M3 Claude pane**

```text
Use ratatoskr_task_send to ask refbot to count this text: "one two three". Show me the task id, then check it with ratatoskr_task_get.
```

**M5 Claude pane**

```text
The adapter asked for a format. Answer it with ratatoskr_task_send using the same task id: "format: json".
```

Expect:

- The gateway sends a standard A2A `SendMessage` to the registered URL. The task reaches `INPUT_REQUIRED` and Claude is woken. After the answer it reaches `COMPLETED` with a result counting lines, words and characters.
- The gateway mirrors the adapter's task by polling it (every 5 seconds at first). The task id you see is the gateway's, not the adapter's.
- Stop the adapter (Ctrl+C) in the middle of another task. The task stays **non-final**, with an observation that the participant was lost; it is not marked failed on a disconnect. A later send to `refbot` fails at once.
- Replace the token file contents with a wrong value and send again: the task fails at once as **FAILED** with a reason that the service refused the gateway's credential (HTTP 401), and an `auth=refused` observation. Put the right secret back and the next send works. With no credential the adapter cannot be reached from anything but the gateway holding that secret.

## 14. Retention, deletion and clean up

Tasks are kept until the owner deletes them. Nothing expires.

**G1 gateway VM**

```sh
rtk gateway participant list
rtk gateway participant delete-tasks --all-final
rtk gateway participant revoke refbot
```

**M1 Mac**

```sh
rtk daemon stop --yes
```

**B2 VM-B**

```sh
rtk daemon stop --yes
```

Expect:

- Restart the gateway first and check the tasks are still there. `delete-tasks` reports how many tasks it removed, and removes their dispatch and wake messages from the ledger. A running task is not deleted by `--all-final`.
- `revoke` makes the next call from refbot fail and ends its open tasks with a reason.
- Stop the gateway with Ctrl+C. To roll back the migration: stop, restore the `.bak` (or your copy), and run the older binary.
