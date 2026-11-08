# ADR-0005 frontend candidate

Candidate: working tree based on 2f65bf0 plus peer ADR-0005 changes. UNTESTED under GR-3. No tests, TUI, daemon, socket, harness, SSH, clipboard or model endpoint executed.

## Result for integration review

Daemon windows gain a third clickable Graph block on the existing title row. Full tab widths are 11/9/7 cells (27 total); compact widths 3/3/3. The monochrome marker replaces padding. No chrome row, pane rectangle, screenTop or child mouse offset changes. F7 is an isolated daemon-only Graph toggle; plain local F7 retains forwarding.

Graph queries run independently of Network, one pending request per source, on every tab. Every successful answer installs its body, even at the same version. Stable host slots use a 3 by 4 grid at 120x26 or larger with at most twelve slots, otherwise a list. Card marks and connection facts are separate; restored, rejected, omitted, truncated, partial-edge and ignored-field facts are visible. Count means sends the gateway decided including failures; host_stored is the subset that reached a host. No delivery claim is made.

Graph: Tab selects host/detail/control focus; arrows select or scroll; Enter opens detail or activates a control; PgUp/PgDn scroll; R refreshes; E opens the local editor only; C opens a card URL panel; Esc returns. The URL panel disables mouse capture for native selection. Enter explicitly requests OSC52 clipboard support, never reports copying as proven; Esc and tab/detach paths restore the ordinary capture policy.

Local card queries and operations are independent of gateway availability. Editor fields use Tab/Shift-Tab and normal text/paste, with bounds and cleaning; action rows provide save, validate, diff, explicit publish confirmation, history, full rollback preview and confirmation, rescan, declaration add/remove, inventory selection, reload and close. Draft remains not configured and makes no model call. Leaving Graph preserves the editor fields. Conflicts and unknown outcomes retain fields and block writes until explicit review/reload. Omitted FoundLabel preserves same-ID evidence; explicit found selection attaches fresh evidence without overwriting declaration text. Rollback requires CardView.Preview from history with the chosen revision.

## Files

New windowclient: graph.go, card.go. New tui: graph.go, graph_layout.go, graph_render.go, card_editor.go, card_detail.go, graph_nodes.go, graph_editor_test.go. Integrations: tui/daemon.go, runtime.go, model.go, tabs.go, network.go, interrupt.go; existing theme_test.go expectations updated for three tabs. No pane transport changes or dependencies.

Monkey authored only card_detail.go and graph_nodes.go, then senior reviewed/refined them. Senior owns requests, integration, editor, layout and tests as explicitly accepted by lead. Monkey source and delivery rows are closed; slot was released to backend.

## Checks actually completed

- Native go build and go vet -mod=readonly ./internal/tui ./internal/windowclient: exit 0.
- Darwin arm64 and Linux amd64, CGO_ENABLED=0: build and vet -mod=readonly ./internal/attach ./internal/tui ./internal/windowclient: exit 0.
- Darwin and Linux vet -mod=readonly -tags=rtktest ./internal/pane ./internal/tui: exit 0; tests compile but were not executed.
- Changed Go files formatted.

Authored unrun coverage includes evidence omission and independent tag storage, conflict preservation, dirty full-revision preview, rollback confirmation gating, same-version body refresh and stable host slots, raw card tail/control escaping, and Graph tab hit cells. Existing tab tests were adjusted for 27/9 geometry. This is not exhaustive behavioral proof.

## Residual untested risks and deliberate limits

- No visual or interaction proof. Grid, wrapping, scrolling, selection, mouse capture restoration, clipboard request, and actual daemon operation outcomes need integration/user evidence when GR-3 is lifted.
- Graph uses ASCII poll branches and an edge-summary list rather than HTML curves. Controls and detail sections use keyboard focus; host nodes and top tabs support clicks. Editor fields use keyboard focus rather than mouse placement.
- Long history/inventory/modal content uses PgUp/PgDn; selection is not automatically scrolled in every modal. Long field text horizontally follows its caret.
- Stable vacant slots remain holes; more than twelve accumulated slots falls back to list even if some are now vacant.
- A card operation timeout is an unknown outcome. There is no automatic retry; the user must reload/review before another write.
- Native selection of a wrapped URL may include line breaks. OSC52 support is terminal-dependent.
- No runtime performance measurements; graph body deep-copy and text layout are bounded by the protocol payload cap, but remain unmeasured.

Next owner: delivery lead for static integration review and any authorized proof. Backend handlers now exist in daemon/queries.go and card service sources; this frontend has not exercised them.

## Lead review refinements, 15:27Z

Local-card C uses CardView.Listener.URL when on, and says listener off otherwise; remote-card C continues to use only the parsed card URL. Local detail and editor show listener on with its URL, or off with startup-only configuration and the warning that stopping the daemon ends every session. Graph R/E/C accept uppercase, Shift and lock modifiers using Network's existing mask. Added unrun tests for these paths. Native package build/vet completed successfully after these changes; no tests or runtime paths executed.
