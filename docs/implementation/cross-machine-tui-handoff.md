# Cross-machine TUI candidate

Parent qitem-20261105154159-6a8ca6e9c34028d3. Frontend senior takeover complete; monkey released to backend 5c. Working-tree candidate, no commit/push. All behavior UNTESTED under GR-3.

## Scope and evidence

Selection/layout: internal/tui/runtime.go, layout.go, model.go, mouse.go, history.go, minimal interrupt.go loop change; unrun runtime_test.go. Status/history: new connected.go, connected_history.go; model.go/history.go integration, model_test.go fake read additions, unrun connected_test.go. Briefs: cross-machine-tui-brief.md and connected-status-tui-brief.md.

Public seams: ParseAgents(string) ([]string,error); NewRuntime(Router, events, []Slot) (*Model,error). New still requires two slots. NewRuntime accepts one/two, preserves order. Empty/whitespace agent value defaults claude,codex; names trimmed; duplicates/unknown/empty entries rejected. Backend owns flag/app wiring.

One pane fills width. Two panes retain existing split/separator. Shared geometry drives render/header/mouse/resize/cursor. One pane F2 is consumed in panes/log/detail, hints advertise F1 only. Existing input/draft/Ctrl+C/F4 contracts retained. Local mode bypasses all connected labels/merges/Tab behavior.

Status/history first version by monkey, then senior refinement: fixed outbound generation reset across reopening; added discoverable source hints; retained readable list/body on refresh failures; removed outbound render-time mutation; cleared superseded pending flags; added R/Shift+R refresh parity; Resync dirties outbound view. Tests authored for required wording, connection cache copy/local display, reopen stale responses, reload preservation/render purity, same-ID source distinction and send updates/Resync. Tests NOT run.

Actual senior commands: gofmt -w internal/tui/connected.go internal/tui/connected_history.go internal/tui/connected_test.go internal/tui/model.go internal/tui/history.go; go build -mod=readonly ./internal/tui; go vet -mod=readonly ./internal/tui. All exit 0. Earlier selection slice build/vet also exit 0. No app/harness/PTY/network probes or tests; no owner binary produced/removed, dependency changes or Git writes.

## Actual connection wording

Runtime title leads with the label, then existing title after two spaces. Agent headers prefer AgentView.Address. Snapshot.Runtime is mode marker; no connected label in plain local mode.

| State | Label |
| --- | --- |
| connecting | Gateway connecting |
| connected | Gateway connected |
| reconnecting | Gateway reconnecting — sends unavailable |
| refused | Gateway refused — retrying |
| nil/unknown data | Gateway state unavailable |

Sanitized ConnectionView.Err appears for nonconnected states in feedback after input-unavailable/quit/active feedback, before remaining banner/hold/error priorities. Refused is not labeled network failure. Router-owned banner preserved. Configured gateway URL never rendered. Peers cached, not presented as live readiness.

## Actual result wording

| Result | Text |
| --- | --- |
| sending | sending |
| gateway_unreachable | gateway unreachable — not sent |
| gateway_uncertain | gateway uncertain — may still arrive |
| unknown_recipient | unknown recipient — not delivered |
| destination_unreachable | destination unreachable — not delivered |
| session_gone | session ended — not delivered |
| delivery_uncertain | delivery uncertain — may still arrive |
| host_stored | saved by recipient host — delivery pending |
| empty | result unavailable |

Unknown future results show their sanitized value. Recipient reports append ` — recipient reports <state> (<reason>)`, preserving the immediate result. Accepted never says model acted. Later failures remain reports visible in recent rows/detail; no sending-model callback. Recent rows combine inbound/outbound by creation time, newest first, timestamp ties inbound then outbound then ID. Separate source records with same ID remain distinct.

## F3 interaction

Runtime F3 starts existing received/run history. Its hints add `Tab outbound`; Tab switches to `outbound sends — current run`; outbound hints say `Tab received`. At bound header says `outbound sends — current run — newest 200 sends only`. Router Sends(200) is current-run only; left/right consumed there and cannot imply older-run outbound access. Up/down selects; PgUp/PgDn pages; Home/End selects edges; Enter opens detail; R/Shift+R refreshes active list/detail; Esc detail→list, Esc list/F3 exits; F1/F2 switch to active panes. Child input never receives log keys. Pane-mode Tab forwards as before.

Detail scrolls through IDs/addresses/time/reply-to, immediate result and reported state/reason, full body and stored-order result/report events. Event reports say `recipient reports`. Source switching/closing invalidates pending reads. All store reads are tea.Cmd calls; no disk/network read in Update/View. Normal log hints: received shows existing keys plus Tab outbound; outbound list starts `Esc/F3 exit   Tab received   ↑/↓ select`; detail starts `Esc list   Tab received   F3 exit`.

## Residual limits

No rendered/manual/runtime verification or executed tests; build/vet prove compilation/static checks only. Local visual compatibility and one-pane interactions remain unobserved. Connection reasons can truncate to feedback width and are not expanded elsewhere. A report arriving while its detail is open updates recent cache; press R to reload that detail. Outbound older-run browsing is unavailable through the current router seam and is labeled current run. Directory peers are cached only. Network timing, real reconnect/refusal and remote late-report delivery belong to backend/user-authorized verification. No additional hardening scope taken.
