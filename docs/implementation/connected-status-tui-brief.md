# Connected TUI status and outbound history — next initial version

Owner engineering.frontend; parent qitem-20261105154159-6a8ca6e9c34028d3.
PREPARED ONLY. Backend holds monkey. Dispatch requires lead's next slot release. Router read signatures and ordering were confirmed against source at 16:31Z.

## Outcome

Runtime users see truthful gateway state and outbound results, including later recipient reports. Uncertain sends never read as definite failures; host_stored means saved, not delivered. Plain local rtk looks and behaves exactly as before. No gateway terminal/central-view feature.

## Boundaries and structure

Read AGENTS.md, ground-rules.md, cross-machine-contract.md, protocol/connected.go and protocol.go, current model.go/history.go/runtime.go/layout.go. First acknowledge scope and ambiguities, then wait for frontend's explicit go.

Allowed new internal/tui/connected.go and connected_history.go; minimal integration in model.go/history.go; fakeRouter interface additions only in model_test.go. No pane/mouse/interrupt/layout/transport policy changes, no app/cmd/router/protocol/dependency/docs/owner-file changes. Existing layout constructors and ParseAgents remain unchanged. Senior owns meaningful unrun tests and refinement afterward.

Use Snapshot.Runtime as the sole connected-mode display marker. Deep-copy cached Connection/Peers and Sends on snapshots; bound sends to newest 200, replace by ID on Event.Send without duplicate rows, copy Event.Connection. Resync replaces cached state. Preserve existing local message cache and event wait cadence. Never poll gateway or read router/disk in Update/View. Send events mark only the appropriate active history source dirty; preserve coalescing and generation checks.

## Gateway and address display

Connection states exactly connecting/connected/reconnecting/refused. Runtime title starts with the connection label so it remains visible when truncated; minimal view also shows it. Labels: 'Gateway connecting'; 'Gateway connected'; 'Gateway reconnecting — sends unavailable'; 'Gateway refused — retrying'. Show sanitized Err for connecting/reconnecting/refused in an available feedback/detail area, respecting input-unavailable/quit priorities. Refused must not say network failure. Missing connection data in a Runtime snapshot says 'Gateway state unavailable', not connected. This is missing data wording, not a new ConnState. Preserve router-owned banner; do not synthesize/clear it on a connection event. Do not show credential-bearing configured URLs. Pane header uses AgentView.Address in runtime mode when present, otherwise the existing name. Peers remain sanitized cached directory data, not proof of readiness.

## Result wording

One pure formatter serves recent outbound rows and full history. sending: 'sending'; gateway_unreachable: 'gateway unreachable — not sent'; gateway_uncertain: 'gateway uncertain — may still arrive'; unknown_recipient: 'unknown recipient — not delivered'; destination_unreachable: 'destination unreachable — not delivered'; session_gone: 'session ended — not delivered'; delivery_uncertain: 'delivery uncertain — may still arrive'; host_stored: 'saved by recipient host — delivery pending'. Unknown future values remain explicit sanitized values, never inferred success/failure. Use Uncertain/Failed helpers only consistently with these facts.

Keep Result and Report distinct: append 'recipient reports <state>' and ReportReason, rather than replacing the immediate tool result. Reported accepted never says acted/processed. Later failures visible on recent row and F3; no callback/tool response to the sending model. Runtime recent rows combine inbound MessageView and outbound SendView by creation time with deterministic ID/type ties, newest first. Local rows untouched. Deduplicate each source by ID; never conflate an inbound record with an outbound record just because IDs match.

## F3 outbound source

Proposed interaction (lead to review before go): runtime F3 defaults to existing received-message/run history, with Tab switching to an explicitly labeled 'Outbound sends — current run' source. Tab in local mode keeps its current behavior. Outbound list uses Up/Down, PgUp/PgDn, Home/End, Enter detail, R refresh, Esc from detail to list, F3 exit; F1/F2 use active-pane semantics. Left/Right are consumed in outbound source; Sends(limit) is bound to the router's current run, regardless of which received-history run was selected. Header says 'current run — newest 200 outbound sends' when at the boundary, otherwise 'outbound sends — current run'. Toggle invalidates old requests and does not mix list/detail selections. Full body hard-wraps, preserves the body shape returned by backend, sanitizes other controls; all metadata and ordered SendEventView entries reachable by scrolling.

Landed seam: Sends(limit int) ([]protocol.SendView, error) returns newest limit sends of the router's current run, oldest first; limit<=0 means 200; success returns nonnil slice. SendDetail(id string) (protocol.SendDetail, error) returns full body and stored-order events; unknown ID wraps router.ErrSendNotFound. Names/previews/bodies/reasons are sanitized by backend. Result events set Result; report events set Report and Reported=true. Both read store directly, not actor; with no gateway they return empty list/ErrSendNotFound. Use an optional outbound-reader interface so legacy fake/local routers need no production stub. All reads tea.Cmd, with generation/source/ID guards. On read error retain prior readable content and show sanitized error; stale response after toggle/close/switch must be ignored. No renderer state mutations. Bounds/scroll helpers may reuse existing pure history helpers; avoid rewriting received/run history.

## Acceptance and stop

GR-1 Go, GR-2 existing Bubble Tea v2/Charm dependencies, GR-3 compile only. gofmt owned files, go build -mod=readonly ./internal/tui, go vet -mod=readonly ./internal/tui. No tests/app/harness/probe/SSH, no Git writes or module changes. Return exact files/checks and known gaps through queue; stop after compiling draft. Senior reviews, refines, authors unrun state/wording/stale-response/local-regression checks, then hands candidate and actual user-facing wording to lead. UNTESTED throughout.
