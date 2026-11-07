# Round 2 tabs / Monokai — bounded first version

Parent qitem-20261107020547-76f312c67f42d7cc, frontend owns refinement.
Read tui-styling-spec.md Round2 in full and current theme/tabs/renderers. User:
'tabs ... function like herdr tabs ... one dark ... monokai', fixed two categories.
Lead released monkey, nobody waiting. Scope new presentation only, no new keys.

Allowed: internal/tui/tabs.go, theme.go, and model.go/network_render.go/empty.go
ONLY if palette-role wiring requires it. No test edits in monkey draft, senior
owns all stale assertion updates. No other paths, docs/deps/transport/owner/Git.

## Concrete design

NO gap (tabGap0). Full blocks ' Terminals ' (11) and ' Network ' (9), total20.
Narrow ' T ' and ' N ', total6. Symmetric one-cell padding; no brackets/edges.
Full from width20 upward, narrow below20, cell clip below6. Whole block including
padding clickable. No badge/hint. tabLayout single source for labels/width/hits.

Colour active: bold foreground#272822 on background#a6e22e; no > marker/reverse.
Inactive muted#a6a28c on bar#1e1f1c. Entire bar (title/right padding) still filled.
Colour OFF active: keep bold/reverse with NO colour setters, replace its FIRST
padding space with '>' rather than adding a cell. Thus full off:
'>Terminals  Network ' or ' Terminals >Network ', narrow '>T  N ' or ' T >N '.
Colored full ' Terminals  Network ', colored narrow ' T  N '. Raw label text
may differ in the marker cell, but widths/hits MUST be identical across profile
and active category. Measure plain actual drawn labels, no stripping styled text
for geometry. Profile transition must never move a click target.

Palette replace constants globally only in daemon theme:
termBG#272822, barBG#1e1f1c, text/tabSel#f8f8f2, dim/tabIdle#a6a28c,
line/detailBG#3e3d32, selectedBG#49483e, OK#a6e22e, accent#e6db74,
warn/Claude#fd971f, bad#f92672, focus/Codex#66d9ef,
NEW unknown role#ae81ff. Unknown state and unknown input use purple, never OK;
unrecognized nonempty state still literal+'unknown to this build' and purple;
empty/no-report and exited stay muted. Old facts still warn, regardless of state.
Active tab uses colTermBG/colOK explicitly; do not change colTabSel to dark (it
also supplies focused header foreground). Preserve paired fg/bg on all UI cells.

Keep paneLines/fitStyled, harness byte contents, local styledHeader branch,
chromeRows7/screenTop2, mouse routing/drafts/ownership, F5/Esc, fetch, empty-window
and SSH untouched. Theme profile/noColor/locale/ASCII policy stays. No prefix mode,
new/close/rename/move/per-session tabs or Herdr keyboard claims. No visual claims.

Correct Round1 comments in touched source (dark ADR-blue palette, brackets,
always-on marker, 28/9-cell widths) to final geometry/palette. Old docs stay owned
by architect; do not edit them. Senior updates all stale tests after takeover.

## Stop/checks

GR-1 Go; GR-2 existing Charm/Bubble Tea v2 only. GR-3 no tests, TUI/rtk/browser/
harness/daemon/SSH/probes, no binary/Git/push/deps/owner settings. gofmt allowed
files, Darwin go build/vet -mod=readonly ./internal/tui only. If native permission
needed, skip/report, do not escalate. Confirm scope/questions, await explicit go,
then return exact labels/palette/files/checks/gaps via durable queue.
