# Persistent Network tabs and zero-session window

Parent qitem-20261107010550-21b6fe12d1385224; return to delivery.lead.
Two senior-designed, confirmed monkey first versions, then senior refinement.
Slot released between passes and after the second pass. No Git writes or pushes.

## Requirements

1. Every daemon window draws visibly bracketed Terminals and Network tabs FIRST
   on the existing title row0. Active tab has a plain > marker and bold/inverse.
   Full strip22 cells; below that, [T]/[N] abbreviations use8 cells, clipped only
   to the actual terminal width. Title takes remaining cells. Drawing and hit
   rectangles share measured cell widths. Minimal/error/quit views keep striprow0.
   Plain local rtk is unchanged. No new row: chromeRows7, screenTop2, pane headers,
   body rectangles, cursor offsets and RequestResize geometry remain unchanged.

2. Fresh left strip press switches tabs, never drafts/queues child input or takes
   ownership; its matching or anonymous legacy release is consumed anywhere.
   Other buttons/wheel over strip do nothing. Log is a Terminals subview; clicking
   Terminals leaves log/Network, Network leaves log. Too-small still clickable;
   quitArmed consumes without switching. An already child-owned gesture continues
   clamped drag/release over the strip. Focus-only consumed gestures retain their
   original release path. The UI tab route runs before Network drops mouse input.

3. Backend owns attach-to-close fetch consumer registration. Window now seeds one
   Network query in Init, then single-flight long polls on either tab. First8s,
   subsequent Since+Wait8s, floor250ms; no overlap when clicking/F5 during a wait.
   Same-version bodies/fetched_at still install. No IO in Update/View.

4. Network shows 'waiting for the first answer' before bootstrap, 'no local
   sessions' for an empty local section, and existing gateway not_configured,
   unsupported/unreachable reasons and kept snapshot age. Minimal Network shows
   these states rather than a warning about a nonexistent pane's size. No remote
   facts synthesized. Schema/sanitizer/old markers preserved.

5. NewDaemon allows0..2. Zero uses shared bare model allocation, activeCount0,
   no dummy slot, pane rectangle, worker, cursor or child input. New exactly-two
   and NewRuntime one/two rules unchanged. Empty opens Network. Terminals says:
   'No sessions in this window.'
   'Start agents: rtk attach --agents claude,codex'
   'Sessions started later need a reattach.'
   F1/F2 cannot focus a pane; F4/F6 send nothing; F3 log remains available;
   Ctrl+C/F10 detach once. Local rows added after attach query the normal plan and
   say 'not in this window; reattach to see it'; remote SSH entry remains available.
   Hints omit nonexistent focus/input actions on the empty Terminals view.

Attach permits zero-slot windows for empty Start, including SSH return. All
requested starts failed stays nonzero with every StartError printed and existing
guidance; partial success keeps current error presentation. No daemon autostart,
no implicit agent start. Missing daemon4/missing expected session3 unchanged.
SSH plan/hosts/terminal handover/signals/reset/reconnect loop not changed.

## Paths and senior refinements

New: internal/tui/tabs.go, empty.go, tabs_test.go, empty_test.go.
Modified: tui/model.go, runtime.go, layout.go, daemon.go, network.go,
network_render.go, interrupt.go, network_entry.go, network_test.go;
internal/attach/attach.go. Own briefs network-tabs-brief.md and
network-empty-window-brief.md; peer/backend/docs changes preserved.

Senior fixes: title separator, anonymous tab release, focus-consumed guard;
invalid-index paneInputReady and empty central input guards; removed draft's
overbroad empty enterNetwork guard (it would block the very remote entry an empty
window needs); ignored non-session rows; completed waiting-state text and removed
unreachable waiting branch; put empty F4 explanation before pane checks; adjusted
empty hints. No speculative hardening added.

## Checks actually finished

- gofmt on touched files.
- GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly
  ./internal/attach ./internal/tui ./internal/windowclient: exit0.
- Same environment/packages go vet: exit0 (includes test type-checking).
No native approval prompt. No Linux check by senior in this pass; lead integrates.
Tests authored/updated but NEVER RUN (GR-3): tab/no-input ownership, minimal/quit
strip placement, owned mouse release, unchanged geometry/local constructors,
zero-slot key/paste/mouse/cursor safety, empty states, attach-time single-flight
cache while hidden, remote entry/local not-in-window from zero-slot models.

All behavior UNTESTED: no rtk, daemon, SSH, browser, terminal operation, socket or
probe executed. Mouse offsets are supported by unchanged constants and static
source/fake tests only, not rendered evidence. Zero-session NewDaemon/socket/SSH
return paths have not run; sessions remain fixed at attach. At extremely narrow
widths tab names abbreviate and may clip; no hidden cells are clickable. Existing
SSH terminal-reply timing/typeahead and between-window SIGINT limits remain.
