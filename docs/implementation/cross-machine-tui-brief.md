# Cross-machine TUI — bounded initial version

Owner: engineering.frontend. Parent: qitem-20261105154159-6a8ca6e9c34028d3.
Status: prepared; NOT dispatched. Lead releases the monkey slot first.

## Outcome and first slice

Runtime can select Claude, Codex, or both. One selected agent gets one full-width pane, with correct mouse, cursor and resize coordinates. Plain local rtk keeps its existing two-pane appearance and behavior. This initial slice implements selection and layout only; senior takes over before subsequent connection/result work. Everything is UNTESTED.

Read AGENTS.md, docs/design/ground-rules.md, cross-machine-contract.md, internal/tui/model.go, mouse.go, interrupt.go, history.go, and protocol/connected.go before confirming scope. Read current files again when the slot opens; peers may have landed changes.

## Allowed files and API

Allowed: internal/tui/model.go, mouse.go; new internal/tui/runtime.go and layout.go. Existing tests may receive only fake/interface compilation adjustments if necessary; report them. No edits to protocol, router, app, cmd, pane, dependencies, docs, ADRs, README, rigs, owner files or the root rtk binary. No Git writes.

Keep New(router, events, slots) requiring exactly two slots. Add NewRuntime(router, events, slots) permitting one or two; use a shared private constructor. Keep existing per-pane arrays of capacity two, add explicit active count, and ensure loops/focus/input/watchdog/quit/render paths visit only active slots. Do not infer runtime from slot count. Snapshot.Runtime is the connected-run marker; constructor choice permits layout before Init receives that snapshot.

Expose ParseAgents(value string) ([]string, error): empty whole value defaults to claude,codex; comma-separated names trimmed, exact lowercase claude/codex only; preserve order; reject empty entries, unknown names and duplicates. Backend owns flags/app and consumes this helper. No harness discovery or launch here.

Centralize active pane geometry: one pane full width; two panes preserve existing split and one-column separator exactly. Use the same rectangle calculation for pane headers, screen composition, RequestResize, cursor offsets, mouse hit testing and drag clamping. Never create an inactive pane's worker or invoke its methods. Single pane F1 focuses the sole slot; F2 is consumed without forwarding or changing focus. Single-pane hints advertise F1, not F2. Other key/draft/queue/Ctrl+C/F4/history policies remain intact. Keep minimum usable dimensions unchanged.

## Shared seams for subsequent work

Final connection states: connecting, connected, reconnecting, refused. Snapshot.Runtime marks runtime; AgentView.Address supplies network name. Snapshot.Connection/Sends and Event.Connection/Send carry updates; Resync replaces cached state. Refused is gateway rejection, not network loss; show sanitized ConnectionView.Err and keep the retry meaning. Preserve router-owned outage banner. No UI network calls or credentials.

Connection labels planned: Gateway connecting; Gateway connected; Gateway reconnecting — sends unavailable; Gateway refused — retrying, followed by sanitized Err. Local mode shows none of these. Router.Sends(limit) and SendDetail(id) are PENDING, so do not invent or call their signatures in this first slice.

Future result labels: sending; gateway unreachable — not sent; gateway uncertain — may still arrive; unknown recipient — not delivered; destination unreachable — not delivered; session ended — not delivered; delivery uncertain — may still arrive; saved by recipient host — delivery pending. Recipient delivery states are marked reported using Report/ReportReason; no implication the model acted. Full explanation remains reachable in detail when compact labels truncate.

## Checks and stopping point

GR-1 Go, GR-2 charm.land/bubbletea/v2 with existing dependencies, GR-3 compile/static only. Run gofmt on allowed Go files, go build -mod=readonly ./internal/tui and go vet -mod=readonly ./internal/tui. Never run tests, apps, harnesses, PTYs, probes or SSH; no module/dependency changes, commits or pushes. Package build must not emit a repo-root executable.

First acknowledge exact scope, interfaces, ambiguities and stopping point; wait for explicit go. Return draft through a durable queue with changed files, exact checks and known gaps. Stop after formatting/build/vet; no acceptance or runtime claims. Frontend then owns review, refinement, meaningful unrun tests and integration.
