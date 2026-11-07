# SSH entry frontend — first draft, UI and client

Owner: engineering.frontend. Parent qitem-20261106161140-ff6b29396d719685.
Authority: network-api-contract.md, SSH entry in full; live wire types.
The user wants Enter on a Network session to open it over SSH, then F10 to return.
This first bounded draft records the plan; the attach loop is a separate supervised draft.

## Allowed paths

New internal/windowclient/entry.go; client.go refusal branches only.
New internal/tui/network_entry.go; minimal integration in network.go, network_render.go,
daemon.go and model.go. No tests in monkey draft. No attach, backend, deps, docs or Git edits.

## Interfaces and structure

- windowclient.RefusalError{Code wire.Code; Message string}, Error() returns Message,
  RefusalCode() returns Code. Wrap with the existing refusal prefix and %w in both
  Dial paths so printed text is unchanged and errors.As finds the code.
- Client.Enter(ctx context.Context, request wire.EnterRequest) (wire.EnterPlan,error):
  use queryContext with QueryEnter; missing Plan is an error; copy Argv before return.
- daemonWindow gets optional enter func(context.Context,wire.EnterRequest)(wire.EnterPlan,error),
  wired in NewDaemon from client.Enter; do not broaden the existing fake client interface.
- Export NetworkBookmark{Kind,Host,ID string}, EntryIntent{Plan wire.EnterPlan;
  Selection NetworkBookmark; Address string}. Model.EntryIntent() *EntryIntent returns a
  deep copy or nil. SetEnteredViaSSH(bool) stores the marker. ResumeNetwork(bookmark)
  is a pure pre-Init setter opening Network and preserving desired raw selection;
  tick starts its first fresh query. No IO in constructor, Update or View.

## Behavior

Network bare Enter: look up selected local or remote session from current cache by raw key.
Local request uses LocalID (even without gateway SessionID); remote uses Host+SessionID.
Host, summary and bare-directory rows show 'select a session to enter'. No draft/input actions.
Only one entry query pending; bounded 12s Cmd, no goroutine per key. Response carries
generation and selected identity; ignore if selection/view changed or window is detaching.
Do not kill the ordinary Network long poll or overlap another entry query.
EnteredViaSSH blocks other-host rows before query with:
'press F10 to return to the machine you came from, then choose from there'.
Same-host snapshot rows may still ask the daemon for a local plan.

Plan local with InWindow true: find LocalID in active slots, focus and close Network.
Absent slot or InWindow false: 'not in this window; reattach to see it'.
Plan manual: show cleaned Reason/Note/Manual; no execution.
Plan refused: show cleaned Reason. Unknown/missing plan kind: visible error.
Plan ssh: copy plan, raw bookmark and cleaned selected address into EntryIntent, call
detachWindow and return tea.Quit. No SSH/process/tea.ExecProcess in tui. Attach loop
will close client and execute exact Argv; no command built by window.
All display strings use cleanNetworkText, raw identities stay raw.
Hint adds 'Enter open session'. Remove 'Enter is not available here yet'.

Fold lead's explicit F5 corrections into network_render.go:
1. Drop remote snapshot row only if its host equals Local.Host and its nonempty
   session_id matches a local session's session_id. Keep unmatched disagreements.
2. Separate ansi import from stdlib group.
3. Reported nil displays 'no report' rather than repeated 'not available'.

## Checks and stopping point

GR-1 Go; GR-2 charm.land/bubbletea/v2 and existing companions only. GR-3: NO tests,
rtk, app, harness, SSH, daemon, socket/probes or binary runs; no commits/push, no owner
settings, no root rtk changes. gofmt on allowed files; go build -mod=readonly
./internal/windowclient ./internal/tui and go vet same packages only. All behavior UNTESTED.
First confirm scope and ambiguities with frontend and await explicit go. Return draft
through durable queue with exact files/checks/gaps; frontend owns refinement/tests unrun.
