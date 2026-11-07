# Network query and F5 view candidate

Parent qitem-20261106155419-633da21a924a6fce. Monkey draft qitem-20261106160830-735db2ae; senior refinement/integration complete. Monkey released backend4a at16:08Z. Working-tree candidate, all behavior UNTESTED; no commit/push.

## Changed paths / API

New internal/windowclient/network.go and unrun network_test.go; requests.go gains queryContext, with previous query delegating using Background and existing cancellation retirement preserved. New internal/tui/network.go, network_render.go and unrun network_test.go; model.go/daemon.go/interrupt.go/history.go integrations. No protocol/wire/daemon/app/cmd/entry edits. Design network-tui-brief.md; the16:07 lead correction overrides its older same-version wording.

Client.Network(ctx context.Context, request wire.NetworkRequest) (protocol.NetworkView,uint64,error) landed. Validates each wait0..8000 and refuses both positive waits together; requires successful Result.Network body, returns daemon NetworkVersion. Uses existing mux/semaphore/cancellation/retired-ID semantics, never gateway directly. Existing query callers unchanged. UI uses optional daemon.network function supplied by NewDaemon.

## Implemented behavior by source review

Daemon-only F5 opens Network; plain local F5 still forwards to child. First F5 triggers first query; windows never opening F5 issue no network query. One pending tea.Cmd across visible/hidden view. First query Since0/FirstWaitMS8000; normal follow-on echoes envelope version with WaitMS8000. R/reopen asks immediate Since0 without wait; if a request is pending only refreshWanted is set, no cancellation/overlap. Tick schedules next visible query with250ms floor; hidden completion caches data, no follow-on. UI request ctx12s, client query12s, daemon wait8s.

**Every successful reply installs/copies its body and FetchedAt even when version matches.** Version is Since token only. Age is gateway AgeMS + max(0, window-now minus answer.FetchedAt), never host StateAt/ReportedAt or a local replacement timestamp. Unreachable body retains its old FetchedAt per daemon contract. Error retains old cache and visible error. Missing/bad time, negative/overflow age, missing/nonpositive threshold, stale report, nonlive host, unreachable gateway or prior query error cannot produce fresh/current report. Old marker precedes row text so it survives narrow crop.

Local daemon sessions shown independently, including gateway not_configured/unsupported. Null remote snapshot falls back to cached Connection.Peers as bare addresses with no state/live/age. Hosts without report say 'no report'. State unknown stays unknown, optional strings/pointers missing say 'not available', unknown enum says '(unknown to this build)'. Outer schema other than1 is unavailable, previous supported body retained only marked old; nested newer/unsupported snapshot hides remote data while known local remains. No model column.

Rows keep raw identities separate: remote host/sessionID; local host/sessionID or LocalID. Selection preserved after reorder. Drawn text strips ANSI plus C0/C1, format/bidi controls, zero-width marks, variation selectors and line/paragraph separators. Cached bodies deep-copy nested report/live/age pointers. Renderer is read-only; scroll state changes only during model updates/selection. Complete Network body uses current pane-body viewport; toggle does not request resize. Actual WindowSize retains normal holder-only sizing.

Senior corrected missing tick hookup, swallowed F6/Ctrl+C, paste/mouse/draft leak paths, child cursor in Network mode, schema guards, remote stale evaluation, invisible cropped old marker, refresh discarding readable cache/using wrong wait, incomplete pointer copies and render-time scroll mutation. These were draft gaps, not observed runtime defects.

## Keys and literal text

- F5: Network↔Terminals, daemon only. Esc from Network returns Terminals.
- F3 switches Network→existing log. F1/F2 return Terminals/focus; single-pane F2 no-op.
- Up/Down selection, PgUp/PgDn page, Home/End bounds, R/Shift+R immediate refresh request.
- F6 remains takeover of the focused LOCAL terminal, never the selected remote row.
- F10 detaches. Ctrl+C in Network follows log policy: first press detaches, no child key.
- Enter, Tab, other keys, paste and mouse consumed in Network; no draft/input/control writes. Existing queued input before switching may finish.

Hint: `F5/Esc terminals   ↑/↓ select   PgUp/PgDn page   R refresh   F6 focused terminal input   F10 detach`.

Small view: `network view needs 40x10 (Esc back)`; last hint `F10 detach  Esc terminals  F6 input`.

Pane and log daemon hints now include `F5 network`; plain local hints unchanged. Header begins `Network`, gateway state/reason and fetched age. Missing data says `loading Network data…`, `age not available`, `not available`, `no report`, `no snapshot yet; directory peers follow`, or `remote data unavailable: snapshot schema unsupported by this build`. Unsupported outer schema: `network data unavailable: schema unsupported by this build`. Retained-error header says `previous data old`.

Detail help: `Read-only; Enter is not available here yet`. Session details label historical report `old report; not current readiness`. Local facts identify own daemon; report facts never globally authoritative readiness. No Enter/SSH affordance or command was built here.

## Actual checks and remaining limits

gofmt owned paths; go build/vet -mod=readonly ./internal/windowclient ./internal/tui all exit0. With CGO_ENABLED=0, GOOS=darwin GOARCH=arm64 and GOOS=linux GOARCH=amd64, both commands also exit0. Nothing built run; package builds write no executable. No tests/socket/daemon/harness/probe/SSH, dependency changes, commits or pushes.

Authored unrun cases: Network wait validation/cancel/mux/version; same-version body/FetchedAt install, age growth/stale/unreachable; no pre-F5 fetching and one hidden/visible pending query; deferred R/reopen and recurring wait; Network input suppression/F6/Ctrl+C/local F5; schema/optional/no-report behavior; bidi/zero-width sanitization, raw identity selection, pure renderer and failure retaining local data. No behavior verified by team.

Residual limits: rendering and long waits unrun; clocks/whole-second timestamp precision bound age accuracy; query/time/queue limits guesses. Selected detail is cell-truncated to one body row; main row prefixes old/not-available information before identifiers to preserve key freshness meaning. Very narrow screen uses minimum-size notice. Host/session lists use finite response bounds from backend, not pagination. A first network query registers fetch consumption until this window closes, even after leaving F5 (contract). Enter/SSH and return-loop restoration belong solely to next row qitem-20261106161140-ff6b29396d719685. Prompt-box defect unaffected and separate.
