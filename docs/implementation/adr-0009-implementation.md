# ADR-0009 implementation record

**UNTESTED: PENDING USER EXECUTION.**
- The implementation is on branch `feat/adr-0009-discovery-dashboard`, from `main` at faa1a62.
- The team ran only static checks:
  - `gofmt`;
  - `go vet` with and without `-tags rtktest`;
  - `go build` of `./cmd/rtk` for darwin/arm64 and linux/amd64;
  - the bridge's vet and build;
  - `node --check` of `app.js`;
  - sha256 of the vendored files.
- Tests are written beside the code and have **not** been run (GR-3).
- No gateway, daemon, browser, model or network has been exercised. Nothing here is evidence of behaviour.

The decisions are in [ADR-0009](../adr/0009-agent-discovery-tool/index.html), D1 to D20. This page records what was
built and the choices made where the ADR left an item open.

## What was built

| Area | Where | What |
| --- | --- | --- |
| Discovery tool | `internal/discover/`, `internal/server/mcp.go`, `internal/agent/{claude,codex}.go`, `internal/daemon/daemon.go` | `ratatoskr_discover`, no arguments. It returns hosts, card descriptions and declared capabilities, and the exact contactable sessions with live, stale, offline and unknown states. The text is **TOON** (D1), from an in-house encoder pinned to spec 4.4 (2026-10-08, Working Draft). A compact-JSON fallback sits behind the same function, `discover.Render`. It is registered for Claude and Codex. Their instructions say to call it before `ratatoskr_send`. |
| Dashboard server | `internal/gateway/dashboard/{view,build,handler}.go`, `internal/gateway/server.go` | GET-only routes under `/ui/`: `/ui/`, `/ui/network`, `/ui/network/graph.json`, `/ui/network/status`, `/ui/hosts/{name}/card` and `/ui/static/*`. They are on the 7777 listener, with **no authentication** (D7) and **read-only** (D13). They read the same snapshots as `/v1/network` and `/v1/graph`. `GET /` redirects to `/ui/`. Every `/v1` route keeps `withAuth`. |
| Dashboard pages | `internal/gateway/dashboard/templates/`, `static/` | Overview and Network pages, with a navigation bar in the cream palette (D10). The Network page has a **Cytoscape.js** hub-and-spoke graph (D9): spokes from `connection.live`, and no host-to-host edges. Its controls are zoom, pan and Fit. Click or keyboard-select a host to load its Agent Card through htmx, with full card and session detail (D12). An accessible host table is the keyboard and screen-reader path. The page refreshes every 5 s. An open-access strip states plain HTTP, no login and read-only. |
| Listeners | `internal/gateway/{listen,participant_listen,run,endpoint,advertise}.go`, `cmd/rtk` | `--listen` defaults to `0.0.0.0:7777`, and any IP, including a public one, is allowed (D14, D16). Port 7777 (host protocol and dashboard) is **plain HTTP unless `--https`** (D17, D19). The participant API is on the `--listen` IP at port 7790, **HTTPS unless `--participant-insecure-http`** (D18). The startup output states, per listener: every interface, cleartext unless `--https`, no dashboard login, and that firewalling is the boundary. |

## Choices on the items the ADR left open

| Item | Choice | Why |
| --- | --- | --- |
| N1: the name of the HTTPS opt-in on 7777 | `rtk gateway --https` | It names what it does. The dashboard is HTTPS too when it is set, because the listener is shared. |
| N2: the retired `rtk gateway --allow-lan` and `--insecure-http` | Accepted for one release, with no effect and one stderr warning | Existing scripts keep starting. The warning names the **breaking case**: a LAN gateway that relied on the old HTTPS default is now plain HTTP, and `--https` keeps HTTPS. |
| N3: `rtk runtime pair --insecure-http` | Accepted with a warning, no effect | Plain is the default without `--fingerprint`. `--fingerprint` together with `--insecure-http` is refused as a contradiction. |
| N4: `--participant-allow-lan` | Accepted with a warning, no effect | The address refusals are lifted (D16). |
| The tool name | `ratatoskr_discover` | The ADR's proposal. |
| Tabler (PROPOSAL only) | **Not adopted.** Hand-written CSS in the cream palette instead. | It avoids a Bootstrap dependency for two pages. htmx 2.0.11 (0BSD) and Cytoscape.js 3.35.0 (MIT) are vendored and pinned, with their sha256s in `static/VENDOR.md`. Nothing loads from a CDN. |
| The address printed for pairing on a wildcard bind | One dial address from the machine's interfaces, preferring Tailscale, then private LAN, then other. Further addresses are printed as "suggested local address, not proven reachable". If none is found, a `<THIS_MACHINE_IP>:PORT` placeholder is printed. `0.0.0.0` is never printed as the address to dial. | DevOps advised this. The participant pairing block keeps the format the OpenRig bridge parses; new advisory lines sit before its final line. |
| Addresses the gateway itself dials (card targets) | They keep the old strict refusals | D16 covers the gateway's listeners, not addresses it dials out to. |
| The discovery scope | Connected agents only | A daemon with no gateway has no directory. |
| Browser hardening | `Content-Security-Policy: default-src 'self'`, with `frame-ancestors`, `base-uri`, `form-action` and `object-src` locked; `nosniff`; `X-Frame-Options DENY`; `Referrer-Policy no-referrer`; `no-store`. `html/template` escapes card text. There is no inline script or style; the htmx config disables eval and indicator styles and allows self requests only. | S5, and part of S6. |

## Not done, or still open

- **S6, DNS rebinding: not mitigated.** There is no Host-header check, because the gateway is legitimately reached by
  IP, by LAN name and by MagicDNS name. A malicious web page opened in a browser on the same network could read the
  open dashboard through a rebound name. It is OPEN for the owner.
- **R1, TOON measurement: not done.** Token counts against minified JSON, and whether models read the status words
  correctly, need models to be run, which GR-3 forbids.
- **IPv6.** `--listen [::]:7777` binds IPv6 only, and the startup text does not say so. No extra IPv4 listener is
  opened.
- **Not yet updated:** older design records still describe the retired flags as current: ADR-0005, ADR-0007,
  ADR-0008, and the host-cards, network-API, bridge and cross-machine design documents.
- **Every runtime behaviour is unverified**, including:
  - the htmx and CSP interplay;
  - the graph refresh and the card swap;
  - the narrow layout;
  - interface enumeration on each OS;
  - the 0.0.0.0 bind;
  - how the Go MCP SDK handles the tool's result.

## Trying it, when the owner chooses

On the gateway machine, `rtk gateway` listens on every interface, port 7777. Open `http://<GATEWAY_IP>:7777/ui/` in a
browser.

**Anything that can reach that port can read the dashboard and the host traffic in cleartext.** Restrict the port
with a firewall first, or add `--https`. In a connected agent session, ask the agent to call `ratatoskr_discover`.
