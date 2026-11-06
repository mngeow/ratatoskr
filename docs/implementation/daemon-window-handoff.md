# Daemon window slice 4 candidate

Parent qitem-20261106091222-57e3e16090943c00. Monkey draft qitem-20261106091856-f426a601; senior takeover complete. Monkey released backend slice5 at09:19Z. Working tree only; no commit/push. All behavior UNTESTED.

## Files and integration

New internal/tui/daemon.go and unrun daemon_test.go; integrations model.go, interrupt.go, history.go. No windowclient/daemon/wire/app/cmd production edits. Constructor tui.NewDaemon(client *windowclient.Client) (*Model,error) landed; accepts one/two stable slots, focuses first live else first, seeds cached drafting, surfaces start errors; zero sessions returns guidance error. Backend owns initial size/cwd/CLI/Tea program/client.Close.

Model.WindowError() error is nil intentional detach; disconnect stores `window disconnected: <reason>; sessions may still be running; run rtk attach`. Caller should read it before deferred client.Close and print it after Tea returns. F10/second Ctrl+C/viewer-first Ctrl+C return tea.Quit, never close sessions. Socket closure/tick-cached error also exits Tea. One WindowEvents wait plus existing protocol wait/tick; ownership/ended/operation errors consumed with no socket reads in Update/View.

Concrete client import exists only daemon.go. Internal narrow client/session seams permit fake-only checks. daemon marker independent of Snapshot.Runtime. Local New and all nil-daemon control/text paths retained. Runtime connection labels (including LAN HTTP) remain at title start; daemon title adds `New sessions need re-attach`. A session started later is not added; reattach required. Ended slot retains frame and shows `SESSION ENDED` plus `exit=N` when known.

## Review/refinement

Monkey first version wrote key/ownership/acknowledgement design. Senior found and fixed missing daemon Ctrl+C dispatch and missing daemon watchdog branch, preserved gateway title, fixed outbound-source detach hints, added ownership rechecks around acknowledged controls/bytes, exact revision admission including rev0, conservative failed-control drafting, fresh same-revision Drafting reconciliation, and warning detail priority. Rapid ownership loss drops queued input but keeps an old Cmd's slot occupied until its stale completion returns; a quick regain cannot start a second Cmd. Daemon mode relies on bounded client operations rather than the local3s watchdog; local watchdog unchanged.

Viewer focus/history work, but key/paste/mouse/F4/draft/resize enqueue nothing. F6 itself marks no draft; confirmed ownership seeds Drafting and current geometry; ownership alone never clears router flag. Enter sends draft(true), key, submitted, then schedules draft(false) only when quiet; local flag clears only after clear ack. Refusal remains I/O WARNING and conservative draft. F4 retains draft until acknowledged cached router state indicates otherwise. Router stays the sole drafting and delivery authority. No approval request exists.

## Window keys and literal text

| Key/action | Daemon window behavior |
| --- | --- |
| F10 | Detach immediately, no waiting-message/draft warning |
| Ctrl+C, holder ready in panes | First forwarded to child, feedback `Ctrl+C again to detach`; second within existing1s detaches |
| Ctrl+C, viewer / ended / unavailable / log / too-small | First detaches immediately |
| F6 | Take focused session input; one pending per slot; no draft mutation |
| F1 / F2 | Focus first/second pane, leave history; one pane F2 consumed |
| F3 | Open/close existing history, no child input |
| F4 | Holder only, existing idle/unknown gate; request held delivery after existing text and submit both; no optimistic draft clear |
| Enter | Holder only; actual key acknowledgement precedes submitted, quiet draft-clear acknowledgement |
| Shift+Enter / Ctrl+J | Existing harness newline behavior; no submission shortcut |
| Paste / other keys, including Esc outside log | Holder only, existing forwarding rules and bounded queue |
| Mouse | Existing focus/drag/wheel rules; viewers can focus, cannot send mouse input |
| History Tab | Existing runtime received/outbound toggle retained |
| History arrows/PgUp/PgDn/Home/End/R/Enter/Esc | Existing source selection/scroll/detail/reload/back semantics; no child input |

Normal hint, two slots: `F10 detach   Ctrl+C interrupt; again detach   F1/F2 focus   F3 log   F4 deliver held   F6 take input`.

Normal hint, one slot: `F10 detach   Ctrl+C interrupt; again detach   F1 focus   F3 log   F4 deliver held   F6 take input`.

Small hint: `F10 detach   Ctrl+C detach   F6 take input (child input paused)`.

Received list help: `←/→ runs  ↑/↓ select  Enter detail  R refresh  PgUp/PgDn page  Esc/F3 back  Tab outbound  F1/F2 panes  F10 detach  F6 take input` (one slot substitutes F1 pane).

Received detail help: `↑/↓ scroll  PgUp/PgDn page  Home/End  R reload  ←/→ runs  Esc list  F3 exit  Tab outbound  F1/F2 panes  F10 detach  F6 take input` (one slot substitutes F1 pane).

Outbound list help: `Esc/F3 exit   Tab received   ↑/↓ select   PgUp/PgDn page   Enter detail   R refresh   F10 detach  F6 take input`.

Outbound detail help: `Esc list   Tab received   F3 exit   ↑/↓ scroll   PgUp/PgDn page   Home/End   R reload   F10 detach  F6 take input`.

Headers lead with `INPUT HOLDER ` / `VIEWER ` / `SESSION ENDED ` (optional exit=N) before name. Viewer forwarded-key feedback: `VIEWER: F6 takes input; Ctrl+C detaches`. Operation failure: `I/O WARNING: <reason>`. Real holder transport failure: `INPUT UNAVAILABLE: <name> cannot receive input`. F6 ended: `F6 unavailable: <name> has ended`; refused: `F6 failed: <reason>`. Attach failures: `start errors: <name>: <message>; ...`, construction feedback plus minimal-view line. Strings sanitized/cell-fitted; long hints crop/wrap using existing chrome. Caller may print `Detached; sessions keep running` after intentional detach.

## Actual checks / limits

gofmt on owned files, native go build/vet -mod=readonly ./internal/tui all exit0. With CGO_ENABLED=0, GOOS=darwin GOARCH=arm64 and GOOS=linux GOARCH=amd64, go build/vet -mod=readonly ./internal/pane ./internal/tui all exit0. Package builds write no binary. No tests, daemon, socket, app, PTY/harness/probe/SSH run, no dependencies or Git writes.

Unrun fake-only checks cover viewer no-input/no-resize, F6 no-new-draft and takeover seeding, immediate detach despite pending/draft, holder first/second Ctrl+C, daemon-vs-local watchdog, refused draft/submit/clear/F4 visibility, quiet Enter clear after ack, rapid regain command serialization, gateway/reattach title and disconnect guidance. Compilation/vet are not behavior proof.

Residual risks: all interaction, rendering, ownership races and socket timing unrun. In-flight operations remain finally checked by daemon; UI revision checks cannot cancel already-written bytes. Cached Drafting can lag ack until next pane state; conservative flags may persist briefly, no delivery authority changes. Narrow headers/hints/start-error lists crop under existing fitting. Daemon frame/ownership messages can arrive between UI checks; server gate remains authoritative. No new sessions enter an existing window; attach must be repeated. Ended/viewer states are distinct from transport failure. No worker/session-stop UI added.
