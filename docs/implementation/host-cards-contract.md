# Host Agent Cards, scheduled polling and the Graph tab: first implementation contract

Authoritative contract for ADR-0005
([`docs/adr/0005-host-agent-cards-and-graph/index.html`](../adr/0005-host-agent-cards-and-graph/index.html)).
Written 2026-11-07 against `main` at `2f65bf0` plus the uncommitted ADR-0005 docs.

**Status: written in the working tree on 2026-11-07, never run, not committed.** The user authorised
implementation on 2026-11-07 ("Ok please begin implementation"). Nothing below is verified; see "As built". Under [GR-3](../design/ground-rules.md) nothing may be
executed: tests are written, not run; `gofmt`, `go vet` and `go build` are allowed; no app, harness,
browser, network probe, SSH or model call; **no commit or push is authorised**.

Labels: **USER** = the user's decision. **DEFAULT** = an architect-chosen, reversible engineering value,
not selected by the user. **OPEN** = a product choice only the user can make.

## 1. What is being built

1. Each daemon can serve one **host card** at `GET /.well-known/agent-card.json` (**USER**: unauthenticated,
   read-only). Alias `/.well-known/agent.json` returns the same bytes (**DEFAULT**: on, documented as a
   compatibility alias only).
2. The owner writes or edits the card **only on the owning host**, through a local daemon operation
   (**USER**). Optional, explicit, reviewed model drafts from detected skills.
3. The gateway **polls** every paired host that has a registered card target, **on a preset schedule
   independent of viewers** (**USER**), and stores cards and poll attempts in its SQLite database (**USER**).
4. A gateway read route serves a **graph** (hosts, cards, poll state, edges). A third **Graph** tab in the
   window draws it (**USER**: persistent, clickable, node = host, formatted selected-node detail).

Not in scope: A2A task transport, any remote write, worker dispatch, remote control, HTTPS on a tailnet,
per-session cards, replacing the heartbeat or stream, changes to Network, Terminals, SSH entry or message
routing.

## 2. Card shape (what is published)

JSON object, UTF-8, at most 64 KiB (**DEFAULT**). Field names are from A2A v1.0.0 §4.4 as read on
2026-11-07 (see ADR-0005 section 3). **Only fields verified in that read are emitted:**

| Field | Value |
|---|---|
| `name` | host label (default: the paired host name) |
| `description` | owner text, at most 280 characters (**DEFAULT**) |
| `supportedInterfaces` | exactly one entry: `url` = **this card's own URL** (the one that exists), `protocolBinding` = `RATATOSKR-CARD`, `protocolVersion` = `1`. It means "discovery metadata only; no task operations". |
| `version` | the published revision number, as a string |
| `capabilities` | `streaming`, `pushNotifications`, `extendedAgentCard` all `false`; one extension `urn:ratatoskr:host-card:v1` with `params.skills` evidence per skill id |
| `defaultInputModes`, `defaultOutputModes` | `["text/plain"]` |
| `skills` | owner-declared entries: `id`, `name`, `description`, `tags` |

Not emitted: `securitySchemes`, `securityRequirements`, `signatures`, `provider`, `iconUrl`,
`documentationUrl`. **Why:** the shapes of `SecurityScheme` and `SecurityRequirement` were not read in
full, and the spec page itself is inconsistent about `security` versus `securityRequirements`. Optional
fields that are unverified are left out rather than guessed.

**Before any seat calls the JSON "A2A-conforming":** the normative source was read on 2026-11-07 (section 12). The card is described as an "A2A-style host card" and the host is never called an A2A service, because it implements none of A2A's operations.

> **Change from the ADR mockups:** the mockups show `RATATOSKR-GATEWAY` and a `/ratatoskr/v1` URL. That
> advertised an endpoint that does not exist on the daemon. This contract replaces it with the card's own
> URL. ADR-0005 is updated to match once the build is reviewed.

**Public means public.** The card is built from an allow-list; it never contains tokens, keys, pairing
data, credentials, skill bodies, absolute paths, usernames, environment, session text, prompts or working
directories. A publish-time check blocks token-like and path-like strings. Skill `description` is the owner's
declared text, never copied silently from a SKILL.md body.

## 3. Daemon side

### 3.1 The card listener (new network surface)

- **Off by default** (**DEFAULT**, and the first **OPEN** choice, see section 9). Enabled by a daemon flag,
  proposed `--card-listen ADDR`.
- **Reuse the gateway's address policy** (`internal/gateway/listen.go`, `ParseAddress`/`PlanListen`/`Listen`):
  loopback and Tailscale addresses are plain HTTP; a LAN address needs `--allow-lan` and serves HTTPS with
  a self-signed certificate (`LoadOrCreateIdentity` pattern, in the daemon state directory) whose
  fingerprint the owner registers on the gateway; plain HTTP on a LAN needs `--insecure-http`. No public
  address. One bind, no fallback.
- **Plain HTTP on Tailscale is the existing pattern, not a claim of HTTPS.** A2A §4.4.6 says production
  URLs must be HTTPS; this feature deviates exactly where the gateway already does, and the docs say so.
  No tailnet HTTPS support is added or claimed.
- Serves **GET and HEAD only** on the two well-known paths. Everything else is 404/405. Uses chi. Sends
  `ETag` (hash of the canonical card), `Cache-Control: max-age=30` (**DEFAULT**), honours `If-None-Match`
  with 304. 503 with no body if no card is published. No cookies, no CORS headers, no directory listing.
- Read/write timeouts of 5 s, header limit 8 KiB (**DEFAULT**). Binds to nothing else: it must not share the
  Unix socket's handlers.

### 3.2 Editing is a local-host operation

- New socket operations (names are proposals): `card.get`, `card.edit`, `card.validate`, `card.publish`,
  `card.history`, `card.rollback`, `skills.detect`, `card.draft.*`. They are on the **Unix socket only**.
  The listener in 3.1 can never reach them.
- They pass the existing caller checks (same user, best-effort refusal of a caller descended from a
  harness). **Document the same-user limit plainly:** any process of the owner can call the socket; the
  guarantee is "no remote write path", not tamper-proofing against same-user agents.
- Storage: a state-directory file or table owned by the daemon; revisions are append-only with per-field
  source (`owner`, `detection`, `model-draft`), time, and the caller kind (`window`, `cli`). Keep the
  newest 20 revisions (**DEFAULT**). Publishing validates first; an invalid draft never replaces the
  published card, and the last good card keeps being served.
- CLI wrappers (thin): `rtk daemon card show|validate|publish|history|rollback`. The TUI editor is in the
  Graph tab for the local host only (section 6).

### 3.3 Skill detection (evidence, not capability)

- Scans **on the owning host only**, reads files, **never executes** a skill or server.
- **Scopes supported in v1: user scope only**, because the card is host-wide. Claude Code
  `~/.claude/skills/<name>/SKILL.md`; Codex `~/.agents/skills` and `$CODEX_HOME/skills` (default
  `~/.codex/skills`). These locations come from agent-net's research, **checked there on 2026-10-30 against
  Claude Code 2.1.314 and Codex 0.163.0**. The implementer must re-check them against the official
  documentation for the versions actually installed and record the result; where unverifiable, show
  "unverified location". Project-scope, plugin and managed sources are **not** scanned in v1 and the
  editor says so ("not scanned"), rather than implying a complete inventory.
- Per skill, keep: harness, harness version (`internal/agent/version.go`, no new execution path), scope,
  a relative label (no absolute path, no username), SHA-256 of `SKILL.md`, scan time, and "could not
  read" notes. Limits (**DEFAULT**): `SKILL.md` read up to 64 KiB, at most 200 skills, do not follow
  symlinks out of the root.
- The UI and docs use four separate words: **found** (a file exists), **declared** (the owner put it in
  the card), **allowed** (nothing here grants permission), **verified** (nothing here checks ability).
  Only the first two exist. Never claim a found skill is usable in any or all sessions.

### 3.4 Optional model drafts

- **As built (2026-11-07) none of the following exists:** only a fixed `not_configured` status, a `model-draft` source label and an editor screen. The design below is the target, and section 12.4 is the minimal version waiting for the user's choice.
- Target plumbing: payload builder, a confirmation step that shows provider, model and the exact
  payload and size, response parsing, schema validation, diff against the published card, per-field
  attribution, and the working-copy rule (a draft never overwrites a hand edit without the owner choosing
  it).
- Payload: host name, skill names, owner-visible front-matter descriptions, harness names and versions,
  scope words. **Never** skill bodies, paths, usernames, environment, credentials, session text, or the
  current card (unless the owner ticks it).
- **No provider is selected** (**OPEN**). Ship with `drafting: not configured`: the drafting key explains
  that no provider is set and how to set one in the future, and **no model is ever called**. Manual editing
  is complete without it. Do not block any other phase on this. Do not invent a default provider, model or
  retention for prompts and replies (the prompt and reply are not kept).

## 4. Gateway side

### 4.1 Database migration (required, and different from today)

`internal/gateway/store.go` currently **re-applies the whole `0001_init.sql` when the version is below
`schemaVersion`** and has no backup. That works only for an empty database. The file's own comment says a
later change needs its own numbered file. A real gateway database now exists on the user's machines, so:

1. Replace it with an **ordered, versioned migrator** (the pattern in `internal/store/migrate.go` already
   exists: numbered embedded files, `PRAGMA user_version`, newer-version refusal).
2. Add `0002_host_cards.sql`. **Additive only**: no change to `hosts`, `pairing_codes`, `sessions`,
   `messages` or `message_events`. The ledger and pairings are preserved byte for byte.
3. **Before migrating an existing database, back it up** with `VACUUM INTO` to
   `<db>.bak-v<old>-<unix>`, mode 0600, as the runtime store does, and say so in the log and the docs.
4. Migrate inside one immediate transaction that re-reads the version inside it. **Ruled 2026-11-07:** only the `--listen` process, which holds the gateway lock, migrates an **existing** database. An admin command (`rtk gateway hosts`, `pair`, and so on) on an older-version database refuses and says to start the gateway; it never migrates while an older gateway may be running. Creating a fresh database is unchanged. This narrows the earlier "may race to create or migrate".
5. **Compatibility:** after migration to version 2 an older gateway binary refuses the database ("newer
   than this binary"). Rollback = stop, restore the `.bak` file, run the older binary. Document this.
6. **Never run it against a user's real database.** Tests build a temporary database; and under GR-3 they
   are written and not executed. The migration is verified only by reading and by the guide's user check.

### 4.2 Proposed tables (names are DEFAULT)

| Table | Holds |
|---|---|
| `host_card_targets` | per paired host: the card URL, optional pinned certificate fingerprint, `created_at`. **Owner-entered on the gateway side.** Foreign key to `hosts`, deleted with it. |
| `host_cards` | per host: the last good card JSON, revision, ETag, hash, `fetched_at`, `last_ok_at`, `last_attempt_at`, last result, short error |
| `card_revisions` | older good cards (newest 20 per host, **DEFAULT**) |
| `poll_attempts` | time, outcome, HTTP status, duration, short reason (last 7 days or 1,000 rows per host, whichever is smaller, **DEFAULT**) |

**No edges table.** Edges are a query over the existing `messages` table (cross-host pairs by the hosts in
`from_address`/`to_address`, one rolling 24-hour count per ordered pair, no bodies, no hourly buckets). **Ruled 2026-11-07:** a row counts when its `result` is not `stored` (the gateway decided it) and both hosts are currently paired. That includes failed sends, so an edge means "sends the gateway decided", never "delivered". The API also carries `host_stored`, the subset that reached a host, so the UI can say which is which.
It is durable because the ledger is. This is simpler than the ADR's `graph_edges` and gives the same
persistence; the user said the graph is stored in SQLite, so this is surfaced as a choice in section 9.
Unpair deletes the host's target, cards, revisions and attempts in one transaction.

### 4.3 Registering what to poll (the owner controls the target)

- Admin commands on the gateway host (the existing `pair`/`unpair`/`hosts` family):
  `rtk gateway card-target set HOST URL [--fingerprint sha256:…]`, `… show [HOST]`, `… clear HOST`.
- **Ruled 2026-11-07:** http only on loopback and Tailscale; https only on a LAN address, with `--fingerprint` required; http on a LAN needs an explicit `--insecure-http`, matching the listener.
- The URL is checked **when entered** against the same address classes as the listener. It is never read
  from a card, a message, a report or a skill. A host cannot change where it is polled.
- Pairing itself is unchanged and carries no card secret; none is needed (the card is public).

### 4.4 The poller

- Runs inside the gateway process for the life of the gateway, for every host with a target, **whether or
  not anything is watching**. Default interval 60 s (**DEFAULT**, not selected by the user), flag
  `--card-poll-interval`, bounded 10 s to 1 h. Start jitter up to 10 %. One poll in flight per host.
- Request: GET, no body, `If-None-Match` when an ETag is stored, `Accept: application/json`, no
  credentials of any kind (never the host credential). **No redirects** (a 3xx is a failed poll).
  Connect timeout 3 s, total 5 s, response body at most 256 KiB, `Content-Type` must be JSON (**DEFAULT**
  values).
- **Targets are IP literals only** (`ParseAddress` refuses hostnames by design), so there is no DNS resolution and no rebinding on this path, and a Tailscale MagicDNS name does not work as a target. Document it. The connect-time check is a check of the dialled address.
- **Metadata deny list (ruled 2026-11-07):** the address classes do not cover metadata inside allowed ranges, so add an explicit deny list checked at entry and at dial, including 169.254.0.0/16, 100.100.100.200 (inside 100.64.0.0/10), fd00:ec2::254 (inside fc00::/7), unspecified and multicast addresses. The gateway's own `--listen` is not changed.
- **SSRF/DNS-rebinding policy:** dial only the validated address class; check the resolved IP **at connect
  time** in a custom dialer, not only when the URL was entered. Loopback/LAN/Tailscale are allowed only for
  the class the target was registered as; link-local and cloud-metadata addresses are refused.
- LAN HTTPS: verify the leaf certificate against the registered fingerprint, as the link client does.
  A changed certificate stops polling for that host and is reported; it is never retried silently.
- Outcomes recorded: `200` (valid), `304`, `timeout`, `refused`, `redirect`, `http-status`,
  `invalid-card` (kept as a failed poll with the reason; the last good card stays), `oversize`,
  `certificate`. A failed poll keeps the schedule (no exponential backoff, **DEFAULT**); the interval
  floor and the no-overlap rule prevent hammering.
- Freshness (**DEFAULT**): fresh = a good poll within 2 intervals; stale = older, last good card kept and
  labelled with its age; offline = no good poll for max(15 minutes, 4 intervals) (**ruled 2026-11-07**: a fixed 15 minutes contradicted fresh at intervals above 7.5 minutes). These are card-endpoint states only.
- Deny list additions (ruled): 100.100.100.100 and fd7a:115c:a1e0::53 (Tailscale's own service addresses). **100.100.100.200 is unverified** (from memory, no page found); it stays on the list because refusing it costs nothing; never cite it as verified. 169.254.169.254 and fd00:ec2::254 are confirmed by AWS documentation, per backend.
- Graph response limit 768 KiB (below `link`'s 1 MiB), bodies omitted largest first. Additive JSON: `schema`, `card_omitted`, `truncated`, `host_stored`, and an edge-row-cap flag if needed.
- Card validation: **strict where the card is authored** (the daemon refuses unknown fields on publish) and **tolerant where it is polled** (the gateway ignores unknown fields, stores and serves only the known ones, and records "N unknown fields ignored"), so a newer host does not turn `rejected` just because it is ahead of the gateway. The card still has to be a valid object within the size cap.
- New packages `internal/card` and `internal/skillscan`; daemon card state is files under `<state>/daemon/card/` (no runtime-store migration); the targets table also has `updated_at` (certificate pause).

### 4.5 What a poll result means (must be enforced in code, API and UI wording)

| Result | It shows | It does not show |
|---|---|---|
| 200 + valid card | the endpoint answered at that time | that any session is active |
| 304 | the endpoint is reachable and the card is unchanged | anything about sessions |
| timeout, refusal, redirect, 4xx/5xx, invalid | that poll failed | that the process died |

The **existing heartbeat and stream stay the authority** on whether a host is connected
(`internal/gateway/liveness.go`, 15 s window). **Do not change, replace or feed it** from poll results.
Carry both signals in the API under separate fields with separate source labels and ages.

### 4.6 Restart

On gateway start, load cards from the database and mark every host `restored: true` ("from the database,
not yet polled") until its first poll since boot. A stored row is a record of what the gateway last saw,
never proof of anything live. Stream/heartbeat state is not persisted and is whatever it is now.

## 5. Graph API (language-neutral JSON, no terminal bytes)

`GET /v1/graph?since=<rev>&wait_ms=<n>` with a paired host's credential, chi, same long-poll behaviour as
`GET /v1/network` (up to 20 s, 204 if unchanged). **Any paired host can read every card summary and the
edge counts** (public cards plus message-pattern metadata); say so in the docs next to the existing network
exposure note. **Size (ruled 2026-11-07):** `link` caps an answer at 1 MiB and about 16 full cards would exceed it, so the graph has its own limit. When a response would be too large it drops card bodies, keeps each host's summary and state, and sets a visible flag (for example `card_omitted: true` per host and `truncated: true` overall). Backend proposes the exact limit; adding these fields is approved. `protocol.WireVersion` is not bumped: an older gateway answers 404 on `/v1/graph`, and the window shows "gateway too old" as its own state.

```text
{ "revision": 12, "generated_at": "...",
  "hosts": [ { "name": "vmb",
      "connection": { "live": true, "heartbeat_age_ms": 4100, "source": "gateway-stream" },
      "card": { "state": "fresh|stale|offline|none|rejected", "restored": false,
                "revision": "5", "etag": "...", "last_ok_at": "...", "last_attempt_at": "...",
                "last_result": "200|304|timeout|…", "age_ms": 12000,
                "source": "gateway-poll", "card": { …public card… } },
      "report_age_ms": 9000 } ],
  "edges": [ { "from": "mac", "to": "vmb", "kind": "messages_seen", "window_hours": 24,
               "count": 23, "host_stored": 21, "last_at": "...", "source": "gateway-ledger" } ] }
```

- `connection` (stream/heartbeat) and `card` (poll) are separate objects with their own source and age.
  The window computes no merged "alive".
- Edge kinds in v1: **`messages_seen`** only. The poll spokes are drawn client-side from each host's
  `card` object. **No inferred edges** (nothing from skills, names or card text), no capability links.
- The window gets the graph through its own daemon as a fetch consumer, exactly like the Network tab
  (the window never talks to the gateway itself). Unreachable gateway: the tab shows the last data with
  its age, labelled.

## 6. The Graph tab

- Third touching block ` Graph ` after ` Network `, same Monokai/Herdr-style, click first. Widths: wide
  strip 27 cells (` Terminals `11 + ` Network `9 + ` Graph `7); narrow form ` T  N  G ` 9 cells; clipped
  below 9. Still on the title row; `chromeRows` stays 7; `tabLayout` stays the single source for drawing and
  hit boxes. `tabLabels`/`tabKind` extend to three. Colour off keeps widths and the `>` marker.
- **F7** optional shortcut (**DEFAULT**; F5 stays Network, F6/F10 unchanged). Not required: it must work by
  click. A zero-session window still opens on Network; Graph is one click away.
- Layout: up to 12 hosts in fixed positions from a stable order (no animation); more than 12, or a narrow
  terminal, shows a list. Selecting a host shows a formatted card with sections **Card, Skills, Security,
  Capabilities, Raw JSON**, a copy-URL action (with a selectable fallback), and an `e` edit key shown
  **only on the host that owns the card**. Remote hosts show "read-only here; edit on that host".
- **Five marks (ruled 2026-11-07):** a fifth state `rejected` (the newest card failed validation, last good kept) is drawn `✗ rejected` in the error colour, ASCII fallback `X`. Grid layout: 3 by 4 hosts at width 120 and height 26 or more; anything smaller, or more than 12 hosts, shows the list. Poll branches are drawn in the grid; the selected host's `messages_seen` arrows and counts go in an edge area beneath it (the HTML mockup's curves are simplified). Copy URL: a panel shows the full URL with mouse capture off for native selection; an explicit button sends an OSC 52 request and the wording says "request sent", never "copied". F7 takes a key a pane receives today (as F5 does) and is listed as a behaviour change.
- Node mark = card freshness only (`● fresh`, `… stale`, `○ offline`, `? no card`); it never says idle or
  active. Layers: messages seen, poll spokes, both labelled with a legend and source/age. Filters:
  freshness. Keyboard: Tab, arrows, Enter, `r`, `e`, `c`, Esc.
- The local editor: description, name label, which found skills to declare, validate, diff, publish,
  history/rollback; readable errors; last good card kept. Draft key shows "not configured" until a
  provider exists.

## 7. Phases and ownership

Per the team model: each senior designs first, then assigns one bounded first version to the monkey (one
monkey task at a time, brief confirmed with the assigning engineer, senior takes over). Lead coordinates
order. **Suggested split (lead decides):**

| Phase | Owner | Content | Monkey-suitable first version |
|---|---|---|---|
| 0 | backend | Versioned gateway migrator + `0002` + backup + compatibility notes | no (data-safety critical) |
| 1 | backend | `internal/protocol` types (card, graph, poll outcomes), card model, validation, canonical JSON/ETag, public-content allow-list and the secret/path check | validation tests (written) |
| 2 | backend | Daemon card store/revisions, local ops, CLI wrappers, listener (address policy, chi, ETag), skill scanner | skill scanner and front-matter parser |
| 3 | backend (+ DevOps consult on listener/firewall/TLS) | Gateway: card-target admin commands, poller with custom dialer, storage, restart projection, `GET /v1/graph` | poll-outcome classifier |
| 4 | frontend | Graph tab, tab strip to three, detail sections, local editor, draft screen ("not configured"), daemon `window graph` request | detail-section renderers |
| 5 | lead + architect | Integration, static review, docs, guide | none |

Phases 2 and 3 do not depend on the draft provider. Phase 4 can start from the API shape in section 5.

## 8. Required docs and checks

- Architect updates after the reviewed build: ADR-0005, README, this contract's "As built" section,
  `docs/design/README.md`, and the HTML + Markdown test guide (new scenario 5) covering build, enabling
  the listener, registering a target, the editor, drafting (not configured), polling, restart, stale,
  and the Graph tab, with **written** versus **verified** marked on every step.
- Seats: `gofmt`, `go vet` (also `-tags rtktest`), `go build` for darwin and linux; tests written for the
  migrator, validation, allow-list, poll classifier, dialer policy, ETag/304, edge query, tab layout and
  hit boxes. **Not executed.**

## 8a. Rulings on the lead's questions (2026-11-07)

Edge counting, hourly buckets (none), who migrates (listen process only), `card-target` flags, IP-literal
targets, the metadata deny list, the graph size limit and the unchanged `WireVersion` are folded into
sections 4 and 5 above. `link` FetchGraph, the daemon's graph fetch loop and the wire handlers belong to
backend (as `netsvc.go` did); frontend has the window side.

## 9. Choices only the user can make (none block the build)

1. **Card listener default.** Off until the owner passes a flag (this contract), or on by default on a
   loopback/Tailscale address so the graph populates without setup? On-by-default publishes to the network
   without the owner asking. *Recommendation: off.* **Cost to tell the user:** the listener is a start flag on `rtk daemon`; turning it on later means `rtk daemon stop`, which **ends every session**. No persistent setting or runtime toggle is planned unless the user wants one.
   With it off, hosts show `? no card (listener off)`.
2. **Edges from the ledger instead of a stored edges table.** Same durability, one less table; the
   user's words were "graph … stored in SQLite". *Recommendation: derive from `messages`.*
3. **Model provider and model for drafts.** Not chosen; drafting ships "not configured".
4. **Retention numbers** (20 revisions, 7 days/1,000 attempts, 24 h edge window) and the **60 s default
   interval** are DEFAULTS, not choices the user made.
5. **Plain HTTP on Tailscale and loopback** (the gateway's existing pattern) versus requiring HTTPS for the
   card. *Recommendation: follow the existing pattern and document the deviation from A2A's HTTPS rule.*
6. **Legacy `agent.json` alias:** on by default (this contract).
7. **Per-host card** (this contract) versus per-session cards: still read as per host.

## 10. Hard limits for everyone

No remote write path. No credential, host credential or prompt in a card, a poll or a log. No model call
without an explicit confirmed action. No change to message routing, the heartbeat or stream, Network,
Terminals or SSH entry. No worker dispatch, no tailnet HTTPS claim. Do not test, run or probe anything; do
not touch a live database or instance; do not commit or push.


## 11. As built (2026-11-07, static review only)

Candidate: the uncommitted working tree on `main` at `2f65bf0`. The lead saw `gofmt -l cmd internal` empty,
`go vet ./...` and `go vet -tags rtktest ./...` clean on darwin and linux/amd64, and `go build ./...` for
darwin/arm64 and linux/amd64 (no binary written), all with CGO off and `-mod=readonly`. No `go.mod` or
`go.sum` change. `internal/gateway/liveness.go`, `route.go`, `send.go`, `internal/router`, `internal/entry`
and `internal/pane` are unchanged against `2f65bf0`. About 90 new test functions are type-checked only.
**Nothing was run: no test, `rtk`, gateway, daemon, TUI, browser, harness, network probe, SSH, migration or
model call.** The migration has never run against any database.

**Review depth (say this if asked).** The lead read in full: `migrate.go`, `0002_host_cards.sql`,
`carddial.go`, `cardtarget.go`, `cardpoll.go`, `graph.go`, `cardserver.go`, `card/validate.go`,
`tui/graph.go` and the diffs of `run`, `store`, `server`, `model` and `tabs`; for `cardstore`, `cardsvc`,
`card/parse`, `skillscan`, `graphsvc`, `link/graph`, `cardcmd`, `card_editor`, `card_detail` and
`graph_render` the lead did a targeted search and relied on the owning senior. The architect spot-read
`migrate.go`, `0002` and `carddial.go` and found no defect. Eight defects found in review were fixed.

**Built as the contract says:** the versioned migrator (only `--listen` migrates an existing database, after a
`VACUUM INTO` backup at 0600; admin commands refuse), four additive tables with cascade from `hosts`, the
daemon card service and state files, the read-only listener (`rtk daemon --card-listen`, off by default),
`rtk daemon card ...`, `rtk gateway card-target set|show|clear`, the poller and dialer, `GET /v1/graph`,
`--card-poll-interval`, and the third tab.

**Where the build adds to or narrows the contract:**

1. Names: `location_verified`, `CardView.Preview`, `unknown_fields`, `card_omitted`, `truncated`,
   `edges_partial`, `schema`, `CodeCardConflict` (returns the error and the current card), `Strict` and
   `Polled` parse modes, `--insecure-http` on `card-target set`.
2. `rejected` is wider than the ruling: the newest poll was refused for an invalid card, an oversize body
   or a changed certificate; a rejection outranks a recent good poll; the reason text says which.
3. The skill scan runs **by itself at daemon start**, in the background, with the listener on or off. It
   reads user-scope skill directories and runs the existing `<harness> --version` for a harness with
   something found. Nothing is published by it.
4. Skill locations: `~/.claude/skills` confirmed by Claude Code docs; `~/.agents/skills` from OpenAI's page
   through a translated snippet; `$CODEX_HOME/skills` (default `~/.codex/skills`) **not confirmed**, scanned
   with `location_verified` false. Installed harness versions were not checked. **Update 2026-11-07:** section 12.2 now records that `$CODEX_HOME/skills` is not documented as a skill directory.
5. `100.100.100.200` stays on the deny list but is **unverified**. Say so wherever it is listed.
6. Changing a target clears that host's stored card and revisions (a 304 could otherwise confirm a card from
   another endpoint). An identical re-`set` keeps them and is how a host paused on a certificate change
   resumes.
7. The CLI cannot declare skills; only the window editor can. `publish` takes `--name` and
   `--description-file`.
8. The public-content check is a block-list of shapes, not a guarantee. The word "bearer" alone blocks a
   publish; there is no username check beyond home-path shapes.
9. The migration runs **before** the gateway binds. A gateway that migrates and then fails to bind stays
   migrated, and an older binary then needs the backup.
10. Not built: `rtk daemon graph`, a cap on parked long polls, retention settings. Edge counts assume
    `created_at` follows `seq`, so a clock set backwards can undercount.
11. `spec/a2a.proto` was read on 2026-11-07 (section 12.1). Three required-field mismatches remain in the build; the host is never an A2A service, so **nothing may say "A2A-conforming".**

**Open choices still the user's** (none blocked the build): the card listener default (built off; turning
it on later ends sessions), edges from the ledger (built that way), the draft provider and model (none),
plain HTTP on Tailscale and loopback (built, with the deviation from A2A's HTTPS rule), and the retention
and interval defaults. The ADR, README and test guide scenario 5 describe all of this.


## 12. Source validation (2026-11-07, reading only)

### 12.1 The A2A card against the normative proto

Source: `specification/a2a.proto` at the `v1.0.0` tag of the official repository
(`https://raw.githubusercontent.com/a2aproject/A2A/v1.0.0/specification/a2a.proto`), package `lf.a2a.v1`,
read in full for the AgentCard messages. The tag was not resolved to a commit hash. Not read: the OAuth
flow messages.

| Check | Result |
|---|---|
| Fields REQUIRED on `AgentCard` | `name`, `description`, `supported_interfaces`, `version`, `capabilities`, `default_input_modes`, `default_output_modes`, `skills`. **All are emitted.** |
| JSON names | The proto has no `json_name` options, so protobuf's lowerCamel mapping applies. The built names (`supportedInterfaces`, `defaultInputModes`, `protocolBinding`, `protocolVersion`, `extendedAgentCard`, and so on) match. |
| `AgentInterface` REQUIRED | `url`, `protocol_binding`, `protocol_version`. All emitted. `protocol_binding` is an **open string** whose comment names `JSONRPC`, `GRPC` and `HTTP+JSON` as the core values, so `RATATOSKR-CARD` is allowed. `url` "must be a valid absolute HTTPS URL in production": our Tailscale and loopback listener is plain HTTP, the documented deviation. |
| `protocol_version` meaning | "The version of the A2A protocol this interface exposes", examples `"0.3"`, `"1.0"`. Our interface exposes **no** A2A protocol, so `"1"` is our own card-interface version. A strict client could misread it. Accepted and documented; it is not a claim of A2A 1.0. |
| `AgentCapabilities` | Fields are `streaming`, `push_notifications`, `extensions`, `extended_agent_card`. There is **no** `state_transition_history` (the spec page's sample was wrong). The built set matches. `AgentExtension` is `uri`, `description`, `required`, `params`: matches. |
| Security fields | There is **no** `security` field and no `preferred_transport`. The fields are `security_schemes` (map) and `security_requirements`. The page's inconsistency is resolved in favour of the proto. **Omitting both is correct:** the endpoint is unauthenticated, so there is nothing to declare. `SecurityScheme` is a oneof of API key, HTTP auth, OAuth2, OpenID Connect and mTLS, and `SecurityRequirement` is `map<string, StringList> schemes`, if they are ever needed. |
| `AgentSkill` REQUIRED | `id`, `name`, `description`, `tags`. |

**Mismatches found (evidence-grounded, to be fixed in the code; not yet fixed):**

1. The card-level `description` is **optional** in `internal/card/validate.go` but REQUIRED in the proto.
2. A skill's `description` is **optional** in the build but REQUIRED in the proto.
3. A skill's `tags` may be **empty** in the build but are REQUIRED in the proto.
4. `url` may be empty on an **unserved** working copy. **As built (backend, confirmed):** `Validate(card, true)` requires it; the published revision on disk has **no URL by design**; the served card gets its own URL when it is served; publish also validates a rendering with a placeholder URL, so a card that could not be served is never published, and serving validates the rendered bytes first.

Fix: publish requires a non-empty card `description`, and each declared skill requires an owner-written
`description` and at least one tag (the editor adds `detected` or `declared` and the harness name by default).
The editor must say why. Until these land the docs say the card is "A2A-style" and not schema-valid in
those three respects. After they land, say only: "the card document follows the v1.0.0 AgentCard field set;
the host is not an A2A service".

### 12.2 Skill locations against current official docs

Pages read 2026-11-07. Claude Code's page shows versions only; the Codex pages show no date. The old
`developers.openai.com` Codex addresses now redirect (HTTP 308) to `learn.chatgpt.com/docs/...`.

| Path | Result |
|---|---|
| `~/.claude/skills/<name>/SKILL.md` | **Documented** as Claude Code's personal (user) location: "All your projects on this machine". Verbatim. |
| `.claude/skills` (project), plugin and enterprise locations | Documented, **not user scope**, not scanned. Nested `<subdir>/.claude/skills` load only for sessions started in or below it. |
| `~/.agents/skills` (`$HOME/.agents/skills`) | **Documented** as Codex's user location, **from the part of the page that was summarised, not read verbatim** (lower confidence). |
| `$CODEX_HOME/skills`, `~/.codex/skills` | **Not documented as skill directories on any page read.** The environment-variable page says `CODEX_HOME` (default `~/.codex`) is "the root for Codex state, including config, auth, logs, sessions, skills, and standalone package metadata", which suggests but does not state the path. Keep `location_verified: false` and label it undocumented. Codex's bundled system skills probably live under it; the scanner should not present those as the owner's skills (unverified). |
| Codex repo scope, admin (`/etc/codex/skills`) | Documented, not user scope, not scanned. |
| Symlinks | Both harnesses follow symlinked skill folders (Claude: "can be a symlink to a directory elsewhere on disk"; Codex: "follows the symlink target"). The scanner does **not** follow them and lists them as "symlink not followed". That is a deliberate safety rule, and it means the found list can be shorter than what a harness actually loads. The UI must say so. |
| Front matter | Claude: `name` defaults to the directory name; the combined `description` and `when_to_use` is truncated at 1,536 characters in the skill listing (summarised, not verbatim). Codex: `SKILL.md` "must include `name` and `description`". |

No code change is needed for the paths: the build already marks Claude and `~/.agents/skills` verified and
`$CODEX_HOME/skills` unverified. The wording above replaces "not confirmed" with "not documented".

### 12.3 What drafting actually is

**Unimplemented.** Present: the status `not_configured` and its message in `internal/daemon/cardsvc.go`, the
`model-draft` source constant in `internal/protocol/card.go`, and the editor screen in
`internal/tui/card_editor.go`. Absent: any configuration, provider, payload builder, confirmation
operation, model or process call, response parser, draft validation or diff. The earlier reports and the
docs that said the "plumbing exists" overstated it and are corrected.

### 12.4 Minimal drafting design (not authorised to build; needs the user's choice)

An adapter `Drafter` (`Name`, `Draft(ctx, request) (result, error)`) with **one** built-in implementation,
the **command adapter**:

- Config: an owner-edited file in the daemon card directory, mode 0600, absent means "not configured":
  `{ "version": 1, "provider": "<display label>", "model": "<display label>", "argv": ["...", "..."],
  "pass_env": ["NAME", "..."] }`. The labels are for the confirmation screen only. No shell. **No key in the
  file.** The binary is resolved to an absolute path and shown.
- Contract: payload JSON on stdin; one JSON object on stdout; empty temporary working directory; environment
  limited to `pass_env`; 120 s limit; 64 KiB output cap; no network of its own beyond what the command does.
- Payload: host name, skill names, owner-visible descriptions, harness names and versions, scope words.
  Never skill bodies, paths, usernames, environment, session text, or the current card (unless ticked).
- Flow: key, a confirmation operation that returns the exact payload and its size, explicit yes, run,
  parse into a card patch, strict validation, per-field attribution `model-draft`, diff against the working
  copy, nothing published until the owner publishes. Any failure leaves the published card untouched.
- Not chosen here: a vendor, a model, a price, or a built-in harness invocation (`claude -p`,
  `codex exec`): the headless flags that stop a harness from loading the owner's own skills, hooks and MCP
  servers were not verified.

The user's choice: **A** command adapter only (recommended), **B** built-in installed-harness call (after
verifying the flags), **C** built-in direct API call (needs the provider, model and the environment variable
holding a key), or **D** none for now.

### 12.5 Status of the section 12.1 fixes (2026-11-07, written, never run)

Built by the seniors and statically checked (gofmt, vet incl. `-tags rtktest`, build for darwin and linux): the
card `description`, each skill's `description` and at least one tag are required **at publish** (a working copy
can still be saved without them, and the editor says why). The editor writes the skill description only from the
owner (front-matter text is not copied or offered), and defaults tags to the harness name plus `detected` or
`declared`. Polled cards follow the same rules: a polled card lacking them is recorded `invalid-card` with the last
good card kept; a stored card this build no longer accepts shows `rejected` with its body left out. **Rollback
changed:** a revision is judged by today's rules before the working copy is touched ("revision N cannot be
restored: it does not meet today's rules"). `$CODEX_HOME/skills` skips `.system` with a visible note and is
marked undocumented; both found-skill views say symlinks are not followed. **First publish from the CLI needs a
description:** `rtk daemon card publish` without one stops and points to `--description-file PATH` or `-`.
Drafting was not built.

## 13. User corrections (2026-11-08): these SUPERSEDE sections 3.1, 3.2, 3.4, 9 (items 1 and 3) and 12.4

The user's words: "The default for the daemon is to start and listen on 0.0.0.0 on a default port, this can be changed if CLI args are provided"; "Why is the LLM generated card not implemented? I asked for it"; "cards should be stored as yaml file, there is no need to have cli commands to publish, show, bla bla bla, just edit a static file". Everything below is written, not built, not run.

**13.1 Listener on by default.** `rtk daemon` serves the card on `0.0.0.0:7778` (**DEFAULT** port; the gateway is 7777) with no flag. `--card-listen ADDR` overrides it, `--card-listen off` disables it. Plain HTTP, GET/HEAD only, unauthenticated, as already decided. The `--allow-lan` / `--insecure-http` requirement for the card listener is dropped: the card is public by the user's decision, so **nothing in it may be sensitive** (the existing redaction and no-path/no-username rules stay mandatory, and the TUI states "this card is readable by anyone who can reach this port"). A bind failure (port in use) must NOT stop the daemon: log it, show "card listener: not bound (reason)" in the Graph tab, keep running. A running daemon needs a restart to pick this up (`rtk daemon stop` ends sessions); say so in the docs, do not restart anything.
The gateway side must follow: a card target may be plain `http://` on loopback, Tailscale **and private LAN (RFC 1918)** addresses without `--fingerprint`/`--insecure-http`. Keep the deny list, IP-literal rule, no redirects, no proxy, size/time caps. Public addresses stay refused.

**13.2 The card is a YAML file you edit.** One file in the daemon state directory, `card.yaml` (mode 0600; the path is shown in the Graph tab and the README). It holds the A2A card fields the owner controls: name, description, version, skills (id, name, description, tags), and a `drafting:` section (13.3). The daemon creates a commented template on first start if the file is absent (the template does not validate until `description` is filled, so no card is served and the status says "needs description"). The daemon re-reads the file when its mtime changes (stat on each request plus a 2 s check; no fsnotify dependency needed). Invalid or half-saved file: keep serving the **last good card**, show the error and line in the Graph tab. Go side needs a YAML dependency (none in go.mod: `gopkg.in/yaml.v3`, strict known-fields decode, size cap 64 KiB; `go mod tidy` needs the network, the lead decides how). The server fills `supportedInterfaces`, `capabilities`, modes and protocol fields; the owner file does not set them.
**Remove:** the `rtk daemon card show|validate|publish|history|rollback` commands, the card socket operations for publish/history/rollback, the revision store and its 20-revision limit (the file is the truth; git or the owner's own backup covers history). The gateway keeps storing polled cards and their revisions (ADR-0005 decision, unchanged). The gateway `card-target` admin commands stay (a different thing: the gateway is told where to poll); say so and ask nothing.
Skill detection stays (evidence only). Detected skills appear in the Graph tab and in the generated draft; they are **not** added to the served card unless the owner puts them in `card.yaml`.

**13.3 Model generation, built (user choice: build it).** Of the section 12.4 options this is **A with installed-harness presets**: one `Drafter` interface and the command adapter, configured in `card.yaml`:
```yaml
drafting:
  preset: claude          # claude | codex | custom
  argv: []                # only for custom; no shell
  pass_env: []            # names only; never a key in the file
```
Presets are plain argv lists for the installed `claude` (headless print mode) and `codex` (`codex exec`). **The implementer must take the exact flags from the official documentation, not from memory**, record the doc URL and date in the code comment and the docs, and choose the flags that do not load the owner's own skills, hooks and MCP servers if the docs support that; if they do not, say so on the confirmation screen. Reading `--help` of the installed binary is an executable probe: do NOT run it. Mark the preset untested. Payload, limits and rules are those of 12.4 (names, descriptions, harness names/versions, scope words only; never skill bodies, paths, usernames, environment, session text; 120 s, 64 KiB output cap, empty temp cwd, env limited to `pass_env` plus what the harness needs to find its own login, which the preset documents).
Trigger (no CLI command): in the Graph tab, local host, key **g** "Generate card draft". Confirmation screen shows the command, the exact payload and its size; explicit yes; run; parse; strict validate; show a diff against `card.yaml`. **Accept** writes `card.yaml` after copying the old file to `card.yaml.bak`; nothing is written without Accept, and a failure leaves `card.yaml` untouched. Per-field attribution is no longer stored; generated fields are marked in the diff only. If the YAML file has comments, the writer must preserve them (yaml.v3 node API) or say plainly that Accept rewrites the file.

**13.4 What stays.** ETag/304, strict-on-write/tolerant-on-poll, unauthenticated read-only endpoint, the SQLite graph storage, polling schedule, migrator, Graph tab layout. The TUI editor shrinks to: show path and status, open nothing, **g** generate, **r** reload now. Manual editing happens in the owner's own editor.

**13.5 Docs and tests to update:** ADR-0005 (a "Corrections 2026-11-08" block; do not rewrite history), this contract, README, scenario 5 (replace publish/read steps with "edit card.yaml", add the default listener, the generate step marked as needing the user's go-ahead because it calls a model), tests for YAML decode/validation, reload, last-good retention, default bind and bind failure. Writing tests is allowed, running them is not.

**13.6 Private configuration and visible bind state (operator requirement, 2026-11-08).**
- `drafting:` (preset, argv, pass_env) is **private local configuration**. The public card is built only by an explicit projection function that copies named fields (name, description, version, skills, plus the server-filled interface, capabilities and modes). It must never marshal the decoded `card.yaml` struct directly, and the owner file's type must not be the served type. A test must prove that a `card.yaml` containing `drafting:` and an unknown extra key produces served JSON with neither. Unknown top-level keys in `card.yaml` are rejected with a line number, not passed through.
- No keys, tokens or secret values in `card.yaml`: `pass_env` holds variable names only; a value that looks like a credential (reuse the existing redaction patterns) is a validation error naming the line, not echoed.
- Listener state is a first-class status with three values: `listening ADDR`, `off`, `not bound: REASON`. The Graph tab local-host row, the daemon log and `rtk daemon` status output show it prominently (a distinct mark, not a footnote). The card must never be described as "endpoint ready" or published to the Graph unless the state is `listening`. A not-bound daemon still reports its local card validity separately.

## 14. As built for the section 13 corrections (2026-11-08, static review only, never run)

Source: the lead's integration row qitem-20261108010443-b3da3fd7e28605d4 and the backend and frontend rows it names; `docs/implementation/card-yaml-frontend-handoff.md` has the window detail. Facts below are the lead's report of what was written, plus gofmt, vet (both build tags, darwin and linux/amd64) and build, which the lead saw finish. **No test, `rtk`, daemon, TUI, harness, model call or network probe has run.**

- **Listener:** default `0.0.0.0:7778`; `--card-listen ADDR|off`; `--allow-lan` and `--insecure-http` refused on `rtk daemon`. Three states (`listening ADDR`, `off`, `NOT BOUND: REASON`) in `rtk daemon status`, the log and the Graph tab; a server that dies after binding turns to not bound. A wildcard bind advertises an interface address in the card URL (Tailscale, then LAN, then loopback), so the URL a reader sees is a choice the daemon made, not the bind address.
- **Gateway targets:** plain http on loopback, Tailscale and RFC 1918; `--insecure-http` on `card-target set` refused; https with `--fingerprint` remains. Deny list, IP-literal rule and caps unchanged.
- **card.yaml:** `<state>/daemon/card.yaml`, 0600, template on first start and never over an existing file; reload on stat change, checked per request and every 2 s; last good served while broken; strict decode with the line number, values not echoed; credential-looking value is an error; 64 KiB cap. Dependency `gopkg.in/yaml.v3 v3.0.1` (one `go get`; an offline `go mod tidy` cannot finish, so a networked tidy may add go.sum lines).
- **Projection (13.6):** `card.Project` copies named fields; `Owner` is never the served type. Tests named: `TestProjectionNeverCarriesPrivateConfiguration`, `TestAValidFileIsServedWithoutPrivateConfiguration`.
- **Removed:** `cardcmd.go`, publish/history/rollback/edit/rescan operations, the revision store, `card_editor.go`.
- **Drafting:** `Drafter` and `CommandDrafter` (argv, no shell, empty temp cwd, listed env names only, 120 s, 64 KiB, process-group kill); presets in `internal/daemon/cardpreset.go`; one draft per window, two running daemon-wide, cancelled on window close, expiring 15 minutes after the last action. Window flow: `g` prepares (nothing runs), `y` runs, diff, `a` accepts with the draft's token, `d` discards, `x` cancels, `r` reloads, `c` copies the URL only while listening, `e` only says to edit externally. **Accept rewrites the file: comments survive only in `card.yaml.bak`**, and the screen says so (this settles the 13.3 choice in favour of "say plainly"). Generated skills merge by id and replace the description; name, version and drafting are untouched. Tests use a fake runner only.
- **Preset flags** (read from the vendors' pages twice, never run): claude `-p --safe-mode --tools "" --no-session-persistence --output-format text --permission-prompts none` with the payload on stdin (managed-policy hooks still apply; `--permission-prompts` needs 2.1.259+; `--bare` not used because it skips the login). codex `exec --sandbox read-only --skip-git-repo-check --ephemeral --ignore-user-config --ignore-rules` (no per-run switch for skills, hooks or MCP, so the confirmation says Codex may load them; the vendor pages are reached through a 308 redirect, which is the only evidence of ownership; `--color never` was dropped as unconfirmed).
- **Defaults the lead chose beyond this contract (accepted unless the user objects):** the file token is a SHA-256 prefix of the file bytes; a declared skill gets found-skill evidence when its id equals a found skill's directory name; the scan runs at start and on `r` only.
- **Riskiest unverified assumptions:** yaml.v3 node tags and line numbers in `Load`; both preset flag sets and whether the logins work with the env passed; the process-group kill; the wildcard advertise address; stat-based reload; a possible macOS firewall prompt on the wildcard bind.
- **Consequence to keep in view:** every daemon now opens a public read-only port on all interfaces by default. The card is the only thing served, and nothing sensitive may be in it.
