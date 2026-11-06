# Windowclient slice 3 candidate

Parent qitem-20261106084710-2bfd6b57d66c58c2. Monkey first version delivered via qitem-20261106090124-1438a39b. Senior takeover complete; working-tree candidate, UNTESTED. Monkey released to backend at09:02Z. No wire/daemon/tui/app/cmd production edit by frontend in this slice.

## Files and API

internal/windowclient/{doc,client,pane,state,requests}.go; unrun client_test.go and interfaces_test.go. Design windowclient-brief.md. Production imports only stdlib, wire, protocol, pane, uv. Tests compare actual tui.Pane/Router interfaces; copied production method sets alone do not detect future consumer changes.

Dial(ctx, socketPath, build, wire.Attach) (*Client,error); Attached() wire.Attached copy; Pane(AgentID) *Pane; Events() <-chan protocol.Event; WindowEvents() <-chan WindowEvent; Close() error; Err() error; TakeInput(ctx,AgentID) error; OperationError(AgentID) error. Client implements tui.Router/outboundReader; Pane implements tui.Pane and provides State() wire.PaneState, Ended/Agent/Name/Address/TakeInput. WindowEvent kinds ownership/session_ended/disconnected/operation_error; Kind/Agent/Error/Ownership fields. No new wire fields.

Backend integration must provide a window-lifetime ctx. Dial's internal40s bootstrap ctx includes connect/attach and5s hello; handshake clears socket deadlines before pumps. Lifetime caller cancellation closes only client. Zero-slot Attached returned for caller guidance. Version/window/session identity checked. No reconnect, replay or detach ack; Close closes socket and joins tracked publishers/workers, never signals daemon sessions.

## Senior changes

Fixed retained handshake deadline and missing caller lifetime cancellation; validated build/window/slot identities; added missing agreed Client.TakeInput/OperationError wrappers. Added publisher/closure synchronization, tracked resize/resync workers, serialized pane state merges and preserved ended/exit/error latch fields; copied exit pointers. Added pane dimension validation, immutable bootstrap copies, bounded message/send caches and Resync delta buffer. Resync install/merge/publication serialized and emits a single Resync after final cache install. Query retirement/removal atomic; wrong response type wakes its waiter; cancelled input with unknown outcome ends connection. Unknown-session void mutations emit an operation error instead of silently disappearing. TakeInput checks returned agent/holder and emits authoritative ownership notification; transport-failure latch cannot clear on F6. Resize retains newest size while viewer, writes only while holder, avoids bypassing error latch.

Getters use immutable/atomic caches or short state/error locks never held during IO. Void mutations wait bounded ack only inside input Cmd and latch failures; following input refuses before writing. Socket loss resolves pending requests and makes Err authoritative even if disconnect event cannot enqueue. Reader never waits for query or consumer; bounded queues overflow→disconnect. Key repeat/base/shift/mod fields retained; mouse kind and tracking mask match pinned rules. No window PasteSubmit or peer-message delivery path.

## Actual checks

gofmt -w internal/windowclient; go build -mod=readonly ./internal/windowclient; go vet -mod=readonly ./internal/windowclient all exit0.

For GOOS=linux GOARCH=amd64 and GOOS=darwin GOARCH=arm64, CGO_ENABLED=0: go build and go vet -mod=readonly ./internal/windowclient all exit0. First cross-check attempts failed sandbox Go-cache access; escalated compile-only retries passed. Nothing built was run; no root binary written. No tests/socket/daemon/harness/probe/SSH execution, no module/Git writes. Native approval prefixes were saved by the tool; frontend did not interact with a terminal prompt.

Unrun tests cover actual consumer interface assertions; in-memory handshake deadline clearing; stale ownership and permanent ended latch plus copied exit pointer; refused-draft suppression/partial-write evidence; out-of-order request correlation/late query reply; bounded independent caches; concurrent ownership/end merging. Compilation/vet do not prove these behaviors.

## Residual risks and next work

All wire IO/handshake/disconnect/close/ownership/Resync behavior and deadlines remain unrun. Native deadline and channel-race fixes are source-reviewed, not runtime-verified. Bounds/timers are guesses: input3s/request, query12s/max8, write5s, bootstrap40s/hello5s; pending/writer128, event channels256, retired query IDs64. A very late reply after its retired ID is evicted terminates the window. Full frame size exceeding wire8MiB ends the connection. Resync uses bounded idempotent deltas; no stream sequence field was added.

Slice4 still must consume WindowEvents and cached State for viewer/holder/ended labels, F6, draft seeding, operation-error completion checks and detach keys. In particular existing TUI input Cmd has3s watchdog while a socket action can contain multiple individually bounded acknowledged calls; daemon-only UI handling must avoid treating healthy multi-step latency as a permanent transport failure. Failed submitted/draftclear/F4 must remain visible via OperationError, not a successful local completion. Backend confirmed router is sole draft authority: seed local draft on attach/new holder from PaneState.Drafting; draft(true), bytes, submitted, then draft(false) only when no later drafting input remains. Ownership alone never clears drafting. App/command wiring belongs to backend; no running attach outcome is claimed by this adapter-only slice.
