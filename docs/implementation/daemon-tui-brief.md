# Daemon window: frontend design, slice 0

Parent qitem-20261106082154-903f81b4eb18e54e. Authoritative daemon-contract.md; no worker scope. DESIGN ONLY, UNTESTED. Wire agreed with backend at 08:28Z via qitem-20261106082310-5882cd4d55701526 and direct clarification. No client/window application implementation in slice 0; later slices require their own rows and monkey handoffs.

## Agreed wire surface

Backend owns internal/daemon/wire/wire.go and types.go; rationale daemon-design.md, Wire surface (slice 0). Four-byte big-endian length plus JSON Envelope{type,id,body}; 8MiB maximum frame, 1MiB maximum paste. wire.Write performs one write and now returns io.ErrShortWrite on short count with nil error. Hello carries exact build/role/PID; window mismatch refused; hello_ack precedes attach/attached. Request IDs correlate ack/result. No compatibility/reconnect/replay.

Attached carries WindowID, Slots in claude/codex order including ended sessions, Snapshot and cached Pending. StartErrors are separate and in requested order; failed registration never creates a slot. Window keeps existing slots, initially focuses first non-ended slot (otherwise first ended slot), and prominently displays all sanitized start errors without synthesizing phantom sessions. Zero slots cannot enter NewRuntime; attach entrypoint must print start errors/no-session guidance and exit. This is backend command integration, not a new zero-pane UI.

PaneState is complete: Agent, Rev, Frame, Cols/Rows, Mouse mask, InputReady, Err, Ended/ExitCode/StartError, Holder/YouHold/Drafting. TypePane coalesces latest per pane; event/ownership/session_ended/pending/ack/result are bounded reliable messages. Pending changes pushed on 500ms ticker plus available query; synchronous PendingOnQuit uses cache. InputReady is transport only, independent of YouHold.

Revision rule confirmed: one counter per stable session. Holder/YouHold apply only when Rev >= highest applied holder revision. Ended with exit/start-error is a permanent latch regardless of its revision; never clear it from a pane message. After ended ignore ownership, clear holder locally. Complete PaneState merges both domains by these rules. This prevents reliable partial changes being undone by a coalesced frame arriving later.

Wire KeyEvent has Code/ShiftedCode/BaseCode int32, Text, Mod int and IsRepeat. Mouse has kind click/release/wheel/motion plus pane-relative X/Y and uv integer Button/Mod. Mouse mode bits are X10/Normal/Highlight/ButtonEvent/AnyEvent/SGR, corresponding to 9/1000/1001/1002/1003/1006. Requests key/paste/mouse/resize/draft/submitted/deliver_held/take_input all receive per-session FIFO Ack; key/paste/mouse success means bytes reached transport and Written reports count. Take-input ack includes authoritative Ownership. Codes include not_holder/session_ended/no_such_session/input_failed/too_large/bad_request/shutting_down/busy/internal. No approve operation.

Query kinds history/runs/message_detail/sends/send_detail/pending/snapshot use wire.Query and tagged Result payload/errors. Store reads remain daemon-owned. Client translates protocol.Event unchanged and surfaces ownership/ended through its own window-state channel/cache. No fabricated Agent events.

## Boundary

Proposed internal/windowclient implements tui.Pane, tui.Router, optional outbound reader, and events with protocol/pane/uv/stdlib imports. No app/router/store/server/link import. Backend owns framing, handshake, message types, authentication and daemon. Client owns one bounded writer and a read pump, correlated requests, atomically published caches. Snapshot, PendingOnQuit, frame, mouse-mode/readiness/error/ownership getters never wait on socket or disk. Queries run in tea.Cmd with deadlines and resolve on disconnect. No reconnect/replay; attach again.

Exact build handshake precedes initial ordered session slots, full snapshot/pending count, frames/dimensions/mouse modes/input errors/holder/ended state. Window ID identifies holder; every request names stable session ID. Session set fixed at attach; later start/replacement needs reattach. Start errors render alongside surviving sessions. Never route an old pane by name to a new session.

Explicit key fields preserve pinned uv.Key Code/Text/Mod/ShiftedCode/BaseCode/IsRepeat. Mouse uses kind click/release/wheel/motion plus X/Y/Button/Mod in pane-relative cell coordinates. Daemon rechecks ownership/mouse eligibility. Paste uses existing 1MiB limit. Requests cover key/paste/mouse/resize/draft/submitted/F4/take-input and queries History/Runs/MessageDetail/Sends/SendDetail/PendingOnQuit. Keys/pastes/mouse acknowledge actual completion/errors; typed not-holder/ended failures. FIFO ensures draft precedes bytes and submitted follows successful Enter. Void router mutators use the same request stream and must not drop silently; cached errors/events communicate failure. Backend must publish cached pending count updates; synchronous TUI getter cannot query socket.

## Later slice 3: socket adapter

Allowed implementation paths proposed internal/windowclient/**, with meaningful unrun client tests; shared wire files backend-owned. Senior design and exact type check, bounded monkey first version, senior takeover. Stop at adapter compiling against backend slices1/2, ownership/ended/transport error paths authored as unrun checks, gofmt/build/vet. No socket/daemon/PTY execution under GR-3.

## Later slice 4: daemon window behavior

Explicit daemon-only constructor/options plus optional ownership interface/event channel; Snapshot.Runtime remains gateway mode and must not double as daemon marker. Existing New and plain rtk key/text paths unchanged. Candidate paths internal/tui/daemon.go, model.go, interrupt.go, mouse.go and unrun tests; exact API follows backend agreement.

Per-session header: INPUT HOLDER, VIEWER, or SESSION ENDED. Viewer is a healthy view, not INPUT UNAVAILABLE. Daemon transport failure remains distinct. F6 requests authoritative takeover for focused session, marks no draft, and resends that window's current pane dimensions only after confirmed grant. Viewer keys/paste/mouse/F4/draft/resize enqueue nothing. Ownership loss cancels queued local input; server is final authority on in-flight races. Viewers crop actual frames without requesting resize; holder sets PTY size; no holder leaves existing size.

Daemon-only F10 and second Ctrl+C detach immediately, no pending-message/draft warning; hint says detach. First Ctrl+C forwards only when focused holder can receive input; otherwise requests detach. Window close/SSH loss closes only socket; it never signals sessions. Session-ended header/state remains on stable slot, disables input/takeover, renders last frame plus exit information. F3 history/result/source behavior stays; session starts after attach require reattach, documented by accepted limit.

No approval request, peer-message paste operation, session-stop action or credentials in window. Input hold/draft state survives window loss; daemon snapshot shows drafting reason, and new holder Enter/F4 uses existing semantics.

## Checks and open seams

GR-1 Go, GR-2 Bubble Tea v2 existing companions, GR-3 format/static/build only, no tests/app/harness/daemon/SSH/probes. No commits/push or new dependency. All tests authored unrun; build/vet evidence never runtime proof.

No unresolved wire decision. Implementation review point: synchronous void Router mutators must preserve FIFO and record acknowledged refusal in cached client/window error state so a subsequent input command cannot silently proceed after a failed draft mutation; no socket read added to Snapshot/PendingOnQuit. Read pump must remain independent of request waiting. One/two fixed slots match current allowed claude/codex sessions; zero-session attach errors remain backend-owned. Long-lived ended slots stay ended; replacements require reattach. No implementation started, no runtime/compile checks performed by frontend in this design slice.
