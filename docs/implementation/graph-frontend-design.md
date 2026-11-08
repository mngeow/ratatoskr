# ADR0005 frontend design — for review, no implementation

Parent qitem-20261107145139-eef772cd06b59771; next delivery.lead/architect.
Read host-cards-contract.md §§2,5,6,8a,10 including latest size/edge rulings;
ADR0005 is context. No code before design acceptance and backend type landing.

## Structure and layout

Extend touching tabs to Terminals/Network/Graph, measured by tabLayout: full
11+9+7=27, compact3+3+3=9, clips below9. Off-colour > replaces active left pad;
profile changes never move hits. No new chrome row/offset; zero slots still open
Network. F7 opens Graph as an optional shortcut; clicking is sufficient.

New graph.go (cache/Cmds/mode/key routing), graph_layout.go (pure rectangles and
raw-identity selection), graph_render.go (nodes/edges/legend), card_detail.go
(formatted sections), card_editor.go (working-copy form/diff/history), and
windowclient/graph.go + card.go (existing query/ack mux only). Model/tabs use small
daemon-only integration; no Network key/SSH changes or pane-content restyling.
Graph mode owns keyboard, paste and mouse before child-input paths; letters r/e/c
never mark drafts, forward, or take ownership. F6 retains its focused-terminal
meaning, F10/Ctrl+C existing daemon detach policy; Graph cannot receive Ctrl+C.

At width>=120 and height>=26: fixed three-column/four-row host grid on left
(18-cell nodes with2-cell gutters), formatted detail on right. Up to12 host slots:
stable raw-host order on first load, existing hosts retain positions on updates,
new hosts fill holes; filters leave holes, not animate/repack. Node click/arrows
select, and selected host's detail updates immediately. At narrower/shorter sizes
or >12 hosts use a scrollable host list; Enter opens full-width detail, Esc returns.
Selection survives refresh by raw host identity, otherwise nearest visible index.

Node marks describe CARD freshness only: fresh/stale/offline/no card (and rejected
explicitly). Unicode ●/…/○/? with ASCII alternatives and words. Restored always
adds 'from the database, not yet polled', never becomes a live mark. Detail shows
connection source+age and card source+age independently; elapsed age uses supplied
snapshot basis, no merged alive/idle/activity computation. Unreachable keeps the
old snapshot/age labelled; gateway_too_old has its own message. Truncated and
card_omitted flags remain visible in summary/detail/Raw JSON, not silently empty.

Layers are labelled controls (Tab/arrow/Enter): poll spokes, messages_seen, freshness
filter. Poll lines come ONLY from each host.card; their 'gateway poll' anchor is
a source legend, not another host node. Avoid crossing ASCII spaghetti: draw poll
branches in the grid and show selected-host messages_seen arrows/counts in an edge
area beneath it (scrollable full list on Enter if needed). Legend says
'sends the gateway decided; host_stored is the subset that reached a host', source
gateway-ledger + window/age; NEVER 'delivered'. No inferred skill/name/text edges.
This is a deliberate simplification of the HTML's connecting SVG curves.

## Details and local editing

Tab cycles hosts / detail / layer-filter controls. Detail section selector:
Card, Skills, Security, Capabilities, Raw JSON. Arrows move/scroll, Enter activates,
r refetches via Cmd, Esc backs out then returns Terminals. Each section uses
cleanUISpan on EVERY field, including formatted Raw JSON, before wrapping/clipping
and Monokai styling. Raw JSON is a selectable detail section, never default view.
Missing fields say unavailable; no claims of unverified A2A conformance.

Local identity/editor come from QueryCard, independently of QueryGraph. Backend's
proposed CardView carries Host{Name,Paired}, Token, Published, Working, Revisions,
Skills, Listener and Drafting. Show a persistent 'Local card / edit' action even
when unpaired/no graph; do not infer local host identity from a remote name. e is
advertised only for the owning host/local action; elsewhere it only explains
'read-only here; edit on that host'. CardOp is LOCAL socket only, never a remote
host parameter. API names pending backend/lead acceptance.

Editor is a Graph subview: Tab/Shift-Tab fields/buttons, arrows selection, Enter
activate. Fields: name, description, declared skill list; skill form id/name/
description/tags, plus explicit found-skill selection. Found is evidence, not usable
or allowed/verified; only owner text becomes declared description. Project/plugin/
managed sources say 'not scanned'. Actions: save working copy, validate, diff,
publish (review confirmation), history, rollback (review confirmation), draft.
Local queries/mutations run in bounded Cmds, one mutation pending; BaseToken is
captured from CardView. Conflict preserves hand edits and offers reload/review,
never overwrites. Refresh cannot replace dirty fields. Validation/read/write error
keeps working copy and last-good published card. History/rollback uses backend
revision IDs, shows sources/times/caller; never fabricates a revision locally.

Draft screen says 'not configured: no provider/model selected', explains future
setup, returns to manual edit; it calls NO provider, model or drafting endpoint.
Esc from dirty editor offers save/discard/cancel. F10 remains global detach per
existing contract; editor clearly marks unsaved fields, and saved working copies
remain on the daemon. No autosave or remote write is introduced.

c opens a URL panel showing the COMPLETE sanitized card URL, wrapped without
truncation, with application mouse capture disabled for native text selection.
An explicit 'Copy with terminal clipboard' button may issue Tea.SetClipboard
(OSC52, pinned source says support varies); report request sent, never 'copied'.
Selectable URL stays available regardless of clipboard support. No clipboard read,
shell command, clipboard-tool dependency or automatic clipboard assumption.

## Boundaries, handoff and checks

Graph fetch uses daemon GraphVersion and one bounded query at a time, with late
generation guards and cached last data; local CardView fetched independently so
editor works for not_configured/unreachable/gateway_too_old. Existing Network poll
is untouched. Backend owns all protocol/wire/card validation/provenance/graph fetch.
Requested seam confirmation directly from backend and copied lead: tokens/conflict,
local identity, wrappers and listener/draft availability. No type names assumed
final until lead announces landing.

Proposed ONE bounded monkey draft after backend's turn: pure card-detail section
renderers in card_detail.go against landed types, plus structured graph node/legend
render helpers if small enough. Brief names exact structs/inputs, no IO/mutations,
no shared model/tabs/editor edits; senior takes over, then implements integration,
layout/key state and editor as the scoped senior-owned remainder under this brief.
If lead requires a separate first version for integration/editor, sequence another
bounded monkey pass rather than bypassing that rule. No dispatch now.

Acceptance checks to author UNRUN:27/9 tab labels and hits across profiles; all
Graph input isolated from panes; stable host placement/list fallback; freshness vs
connection/restored; messages count vs host_stored; omitted/truncated/old-gateway;
hostile text/Raw JSON; local editor with no graph; remote e refused; token conflict
and dirty refresh; publish/rollback confirmations; draft calls nothing; copy fallback.
GR-3: no tests/UI/browser/rtk/daemon/SSH/harness/model/probes. Only permitted no-prompt
gofmt/build/vet incl rtktest for Darwin/Linux; report exact completions/skips.
No commits/push, dependency additions or authoritative doc edits. Stop at reviewed
candidate with residual untested/visual limits and hand off to lead.

## Accepted changes — lead/architect 2026-11-07 14:58Z

This section overrides the proposals above where they differ. Design accepted;
bounded monkey renderers accepted, remaining integration/editor senior-owned, no
second monkey pass needed. Wait for types and explicit slot after backend scanner.

- Graph consumer from attach: Init seeds one Graph query; its own floor and
  single-flight long poll continue on EVERY tab, independently of Network.
  Graph responses cannot move pane focus, resize, draft or ownership.
- Clicking another tab/F5 hides, never destroys, the editor/dirty working copy;
  no prompt and no loss on tab switches. Preserve detail section and scroll too.
  Editor field letters r/e/c are text, not actions; paste is cleaned/bounded into
  that field only. Explicit action focus is required for operations.
- Raw JSON section pretty-prints PARSED card data, then splits lines BEFORE
  cleanUISpan; scroll line-by-line within the64KiB bound. Escaped control text
  remains visibly e.g. \\u001b rather than being executed or collapsing all lines.
  Label 'Raw JSON — what the gateway kept'; when ignored-field count>0 display
  it. Backend supplies the actual field name. This is not the original response.
- URL panel explicitly says Esc returns (mouse capture off means tab clicks
  cannot work). Mouse capture follows modal state and restores on all ways out,
  including detach. Use only parsed card supportedInterfaces URL; unparsable
  card offers nothing to copy. Still clean it independently of backend validation.
- Security says only 'unauthenticated, read-only card' and the URL's transport
  scheme; no secure/verified claims or statements about agent network reach.
- F7 remains a small isolated daemon-only shortcut branch; click primary.
  Record that daemon F7 used to forward to harness, now opens Graph; plain local
  F7 unchanged. It must be easy to remove if the user objects.
- Fifth mark: '✗ rejected', error colour, ASCII 'X rejected': newest card failed
  validation and the last good one is kept. It says nothing about session state.
- Listener bind failure will not be projected as Listener.State=='failed'; use
  final landed types. Do not invent that state in UI.
- Record120x26 layout threshold and poll branches/edge area instead of SVG curves
  as deliberate HTML differences. Graph body may be omitted/truncated; show both
  flags, and gateway_too_old stays distinct. Count and host_stored retain the
  accepted 'gateway-decided sends' / 'subset reached a host' legend, never delivered.

Additional UNRUN cases: tab switching preserves dirty fields/section/scroll,
focused r/e/c and paste edit only their field, eager graph while hidden cannot
affect panes, multiline64KiB Raw JSON and ignored fields, unsupported/unparsable
URL handling, mouse capture restored on every modal exit, rejected keeps last-good.
