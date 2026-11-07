# Persistent daemon tab strip — first bounded draft

Parent qitem-20261107010550-21b6fe12d1385224. Owner frontend.
PREPARED, not dispatched until lead releases monkey after backend.
Read network-api-contract.md 'Always-visible Network tab', current model/mouse/network.
The user wants 'a clickable tab that is always on the TUI', not a dashboard.

## Scope and shape

Only new internal/tui/tabs.go and minimal model.go/mouse.go integration. No empty
constructor or attach work in this pass. No tests from monkey; senior owns unrun tests.
Terminals/Network strip FIRST on existing title row0 for daemon windows, visibly
bracketed tabs with active styling. No new row: chromeRows7, screenTop2, all pane
geometry/header/body/mouse offsets unchanged. Local/plain rendering unchanged.
Use one helper for tab labels, styled drawing and cell-width hit rectangles. At
width>=23 use full names; narrower use clearly active T/N abbreviations when full
strip cannot fit. At minimum40 the full names fit. Truncate leftover title only.
Do not route styled tabs through fitPlain (it would strip styles); fitStyled keeps
the application's SGR. Existing titleLine remains plain; a new title-row renderer
combines strip+title for normal and minimal daemon rendering.

Daemon minimal renderer MUST keep strip on row0 even input-unavailable or quit
warning: next rows hold warning/error/size guidance, rows clipped to available
height. No hidden interactive row or moved hit target. Local minimal path unchanged.
Log is a Terminals subview (Terminals tab active). Clicking Terminals leaves log
or Network and shows panes; clicking Network leaves log through existing openNetwork.
Strip click works while too-small. While quitArmed consume strip click without
changing tabs (keep warning). Left click switches; other buttons/wheel over strip
are consumed. No draft/input queue/ownership mutation from strip clicks.

## Mouse, authoritative lead clarification

A gesture already owned by a child STAYS with the child: mouse.owner>=0 must
continue through dragMouse/releaseMouse, clamped to that pane even over row0, so
the press has its matching release. Never turn an owned release into a tab click.
Only a fresh press ON the strip with no owner changes tab; its own release is
swallowed wherever it lands (focusConsumed state or dedicated UI gesture flag).
An existing focus-consumed press also retains its release swallowing.
Route mouse messages from Update to UI tab handling BEFORE networkMode drops
them. Then network/log/too-small still consume non-tab input normally. No child
mouse change for plain local windows or for an already-owned gesture.

## Checks and stopping point

GR-1 Go, GR-2 existing charm.land/bubbletea/v2/companions only, no deps.
GR-3 no tests/rtk/app/harness/SSH/daemon/probes/binary runs, no Git/owner changes.
Only gofmt allowed files and Darwin go build/vet -mod=readonly ./internal/tui;
skip a check that needs native permission, report it instead of escalation.
Confirm scope and ambiguities and await explicit go. Return files/checks/gaps
via queue. Everything UNTESTED. Stop before empty-session or fetch-state work.
