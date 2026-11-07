# Zero-session daemon window and attach-time Network cache — prepared

Parent qitem-20261107010550-21b6fe12d1385224. Frontend owns this pass after tabs.
NOT dispatched. Lead must release monkey slot again between passes.

Read network-api-contract.md 'Always-visible Network tab' and architect decisions
in parent row: --agents all failed remains a nonzero error, with every reason;
zero-session applies plain attach and SSH return only. Mixed success opens and
keeps current start-error presentation. No new session starts or daemon autostart.

Allowed: internal/tui/runtime.go, layout.go, daemon.go, model.go, network.go,
network_render.go, interrupt.go, new empty.go; internal/attach/attach.go.
No tests from monkey; no other paths/deps/docs/owner files.

## Bounded structure

Keep New exactly-two and NewRuntime one/two validations unchanged. Extract shared
allocation from buildModel into private allocateModel(router,events,slots) which
copies slots to [2] and initializes clock/drafts/mouse; call it only after existing
validation, or from NewDaemon's zero-slot branch after client validation. No dummy
slot, no fake pane and no worker. NewDaemon permits0..2, assigns daemon state then
sets empty model networkMode=true. activeCount returns0 only for daemon active0;
retain existing fallback1 for old/local zero-value fixtures. boxes empty.

One emptyDaemon() predicate and one empty Terminals renderer, not a refactor to
dynamic slot slices. activeSlots/reconcile/resize/watchdog/frame loops visit none.
Guard input centrally: paneInputReady invalid index=false; canReceiveInput=false;
forward, paste, markDraft and enqueueInput refuse; F4/F6 show no-session guidance;
F1/F2 no-op; F3 log remains usable, F5/tab switch normal, Ctrl+C/F10 detach once.
No control sends with empty AgentID; queued input completions cannot exist.
Cursor nil, no phantom headers or pane rectangles; Terminals body says:
'No sessions in this window.'
'Start agents: rtk attach --agents claude,codex'
'Sessions started later need a reattach.'
At small sizes keep persistent strip and meaningful empty guidance within bounds.

Attach removes zero-slot rejection ONLY when Start is empty. When nonempty Start
returns zero attached slots: print all StartErrors, close, return noSessionsGuidance
as error. Existing missing daemon4 and expected session3 unchanged. Zero return
after SSH now opens Network instead of exiting. Keep initial session-start rules,
exact SSH argv/loop/signal/reset behavior intact. Update now-false comments.

## Cache and state

Every daemon model Init seeds one Network query beside current waits/tick.
networkTick continues bounded single-flight long polls while either tab visible:
one pending, Since last version/WaitMS8000, first FirstWait8000, floor250ms; no
extra query when F5/click opens during a pending one. Same-version install stays.
Backend independently registers consumer at attach; no network calls from View/Update.
Explicit states: 'waiting for the first answer', local 'no local sessions', gateway
not_configured/unsupported/unreachable with retained snapshot age. Never synthesize
remote facts or hide kept snapshot age. Existing schema/sanitizer/old guards stay.

Checks: GR-1/2/3, no tests/apps/SSH/probes/Git/owner/dependency changes. gofmt,
Darwin go build/vet -mod=readonly ./internal/attach ./internal/tui only, no prompt:
skip/report if native approval needed. Confirm then explicit go, return bounded
draft with exact files/checks/gaps. Senior owns refinement and tests UNRUN.
