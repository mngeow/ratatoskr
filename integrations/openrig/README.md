# ratatoskr-openrig

**Status: written, statically checked (gofmt, go vet, go build to /dev/null on darwin/arm64 and linux/amd64), never
built into a binary and never run (GR-3). Nothing here is evidence of behaviour.** The round trip operator → gateway →
Claude on another host → operator is **PENDING USER EXECUTION**.

The bridge between the Ratatoskr participant API (ADR-0007) and one existing OpenRig operator seat (ADR-0008). One
program, three entry points: a guided `setup` TUI, a long-running sidecar (`serve`) that holds the participant
credential, and a thin local client (`send`, `directory`, `status`) that holds none. The design contract is
[`docs/implementation/openrig-bridge-contract.md`](../../docs/implementation/openrig-bridge-contract.md).

## Known limitation, stated plainly

`rig send` from outside a seat shows `From: <unknown sender>`; `--from` is ignored. The Ratatoskr sender exists only in
the text this bridge writes, and any local process could imitate that text. Message text is untrusted correspondence.

## What it does not do

It never starts, stops or manages an OpenRig session, never writes MCP or hook configuration, never reads OpenRig's
database. The only `rig` commands it runs are `rig --version`, `rig ps --nodes --rig RIG --json --fields ...` and
`rig send SEAT TEXT`. The outbound mechanism (skill plus this client) is the architect's working assumption; the user has
not accepted it yet.

`setup` requires terminal stdin and stdout. It guides the owner through preflight, local OpenRig settings, VM-A
pairing material, config, pairing, service, readiness, skill activation and the final checklist. It never starts an
OpenRig seat. It uses a progress header, a responsive step sidebar, a scrolling form, visible Back/Continue controls,
and contextual key help. Pairing offers a masked whole-block paste or typed URL/pin/code fields; the paste is read
when its end-marker line arrives, and Enter inserts line breaks while typing it. The code is held in a wipeable byte
buffer and is never rendered. See the [scenario 8 guide](../../docs/testing/scenario-8-openrig-bridge/README.md) for
the current keys, consent defaults, service skip cases and manual fallback. The TUI and multi-line paste have not been
run; the round trip remains **PENDING USER EXECUTION**.

## Commands

```
ratatoskr-openrig setup [--config PATH] [--insecure-http]
ratatoskr-openrig pair --pair-code-file PATH [--config PATH] [--insecure-http]
ratatoskr-openrig serve [--config PATH] [--insecure-http] [--resume]
ratatoskr-openrig send --to NAME@HOST [--reply-to ID] [--id ID] [--config PATH]     (message on standard input)
ratatoskr-openrig directory [--config PATH]
ratatoskr-openrig status [--json] [--config PATH]
ratatoskr-openrig --version
```

Exit codes: 0 ok · 1 failed (definitely not done) · 2 usage/config/files · 3 uncertain (may have been stored) ·
4 replaced · 5 revoked · 6 the sidecar is not running (nothing sent).

## Configuration

One JSON file, owner-written, mode 0600 or tighter, no secret in it (contract section 5). Default path
`~/.local/state/ratatoskr-openrig/config.json`. Required: `config_version` (1), `participant_url`
(`https://HOST:PORT`), `certificate_sha256` (64 hex digits; the certificate pin, **not** the `sha256//` key pin; absent
for plain HTTP), `participant` (`openrig-main`), `session` (`operator`), `operator_seat` (`operator-agent@kernel`),
`rig_path`, `rig_env_path`, `openrig_home` (all absolute). Optional: `openrig_url`, `state_dir`, `credential_path`,
`limits.{inline_body_bytes,per_sender_per_minute,total_per_minute}`, `ready_check_timeout_ms`. Unknown fields are refused.

## Pairing

For an interactive install, the recommended route is `ratatoskr-openrig setup [--config PATH]` from an ordinary
terminal. Use the manual config-and-pair route below only as a fallback or when setup cannot run in a terminal. For
this scenario, keep the participant API on HTTPS and omit `--insecure-http`.

1. On the gateway host: `rtk gateway participant pair openrig-main` prints the code, the URL and the pins.
2. On this machine: put the code, alone on one line, in a file with mode 0600.
3. `ratatoskr-openrig pair --pair-code-file FILE --config CONFIG` creates `<state_dir>/credential` (mode 0600, `O_EXCL`)
   **before** redeeming the code, so a bad path cannot consume it. The credential is never printed.
4. Delete the code file.

## Transport

HTTPS is pinned by the certificate's DER SHA-256, checked on every new connection before any HTTP is written
(`InsecureSkipVerify` is set only together with that check). Plain HTTP needs `--insecure-http` on the command and prints
a warning. There is no proxy and no redirect.

## Layout

`main.go` dispatch · `internal/cli` commands · `internal/config` · `internal/gw` participant client · `internal/journal`
· `internal/render` · `internal/limits` · `internal/rigcli` · `internal/ipc` · `internal/sidecar`.
`launchd/` and `skill/` are DevOps's and frontend's. Tests are written beside the code and have never been run.
