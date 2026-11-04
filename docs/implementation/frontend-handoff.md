# Pane and TUI handoff

Candidate: working tree, no commit. Owner engineering.frontend; integration owner delivery.lead. Selected row qitem-20261104095414-220ec49432d2f84d. **All behavior untested under GR-3.** Monkey supplied separate supervised initial drafts for pane and TUI; frontend reviewed, refined and authored the unrun tests. Monkey is released to backend.

Implemented internal/pane and internal/tui only, plus their tests and frontend implementation notes. Root module/protocol/README/peer code ownership preserved. No tests, application binaries, real harnesses, probes or SSH sessions ran; no commits or pushes.

Static checks on the final code: gofmt; go build -mod=readonly ./internal/pane ./internal/tui; go vet -mod=readonly ./internal/pane ./internal/tui. Build and vet exited 0. Cache permission failures on intermediate checks were rerun with native approval; these were compile checks, not behavior execution. Existing untracked files and peer changes remain intact. No generated executable artifact is produced by building these library packages.

## Integration API

pane.Start(spec,width,height,onExit) returns a pane or an error. App supplies complete environment and absolute child directory, owns process cleanup and calls Close after the Bubble Tea program returns. Callback follows process Wait, with 0 on clean exit and -1 for signal termination. A nil TUI slot pane with StartErr displays a failed launch; the other pane stays available. App should close router event delivery on shutdown to release an outstanding UI event wait; callbacks must not block indefinitely.

tui.New(router,events,[]tui.Slot) expects exactly two unique agent IDs, keeps supplied order for F1/F2, returns *Model implementing tea.Model. Pass model to tea.NewProgram. No harness starts inside tui. Pane inputs use the already pinned ultraviolet key type, a Charm companion library; no new dependencies or alternative TUI framework introduced.

## Assumptions and limits by source

- internal/pane/pane.go:37: 50ms paste/Enter delay is provisional. :47: two-second user/query write bound is provisional. :173 Start and :249 pollerMaster: nonblocking duplicate and Go runtime poller deadline support are source-derived, not observed on a real PTY. Failed deadline support aborts startup rather than leaving uncancellable writes.
- internal/pane/pane.go:310 replyLoop and :472 flushInput: an empty io.Pipe write acknowledges preceding actual PTY writes; this depends on supported x/vt encodings/query replies being nonempty. Empty user paste bypasses x/vt. :495 PasteSubmit: direct, serialized bracketed paste then Enter, cancellation and cumulative write counts; every failure is a WriteError with BytesWritten() bool/ByteCount() int/Unwrap. Never retries.
- internal/pane/pane.go:664 fallbackKey: Shift+Enter maps to documented/default Ctrl+J for claude/claude-code/codex; custom bindings may differ. Unsupported modified keys surface an error. x/vt lacks Kitty/CSI-u. Scrollback is bounded at one line because zero does not disable it; total screen allocation is bounded to 1,048,576 cells. Only the owned direct child PID is explicitly killed by Close; descendant process cleanup and outer terminal restoration need future user-directed checks.
- internal/tui/model.go:57: input queues hold at most 128 actions per pane. :389 enqueueInput and :421 inputCompleted: draft is held before bytes are scheduled, then cleared only on successful bare Enter completion with no newer queued or refused input. UserSubmitted resets budget only, per agreed updated seam. Conservative drafting can keep messages held after menus, Escape or deletion.
- internal/tui/model.go:255 View: frame captured once per pane, reused for cursor; emulator locking can still delay render during query/resize. :513 fetchHistory and :1009 waitEvent: bounded history refresh, request generations, one outstanding event wait. Snapshot copied before UI mutation. History uses empty RunID for current run. Log refresh is coalesced at 250ms after events.
- internal/tui/model.go:1062 sanitize and :1101 fitStyled: chrome strips terminal controls; child screens retain emulator styles and reset style/hyperlinks at pane boundaries. Wide-character visual fit, actual colours, redraw speed, alternate screen, focus, dialogs and permission interactions are unobserved.

Line references locate the delivered version; see function names if later integration edits shift them. Source evidence, terminal environment rationale and the first future checks are in pane-risks.md. No usability verdict exists. No tmux fallback, persistence of pane content or owner configuration writes are introduced.

Unrun tests cover partial writes, key normalization/newline fallback, canceled message byte counts/no late Enter, input transport acknowledgement, bounded draft/Enter sequencing, overflow, failed writes, global/log/dialog input interception, snapshot ownership/resync, stale history and cell-fit/sanitization with stub panes. These test files compile through vet; they supply no passing behavior results.

## Accepted integration refinements after initial handoff

Lead accepted graceful Close and F4 draft override in qitem-20261104102134-d88e2e6fd8962dea. Close sends SIGTERM to the owned child, gives a provisional two-second grace, then force-kills that PID only and gives a bounded reap wait. CloseContext lets callers cancel their wait/shorten graceful delay; the shared cleanup still runs and remains idempotent. PTY close unblocks I/O. Shutdown-order/idempotency stub tests are authored and unrun. The latest graceful-close/F4 candidate passed gofmt/build/vet compile checks; no behavior ran.

F4 is refused while local input is queued and for busy/needs-you/starting/exited views. Eligible idle/unknown requests clear only the UI's local draft mark; router owns the atomic gate override, so UI does not call UserDraft(false) separately. Drafting hold text warns that F4 pastes after any text in the box and submits both. The user briefly paused pane work, then chose embedded panes again (design v0.11); all files were preserved and no replacement was started.

## F3 older runs and full message detail

qitem-20261104103638-88fe503553762057 is implemented in the uncommitted working tree. Monkey supplied the supervised first version; frontend took over and refined it. Backend owns Runs/MessageDetail read seams; frontend changed no backend code, root module, protocol or README. All behavior remains UNTESTED under GR-3.

Files: internal/tui/history.go (run/message selection and full detail), model.go (Router methods, response guards, log rendering/help integration), model_test.go (fake interface methods), history_test.go (senior-authored unrun tests), this note and log-history-brief.md.

Keys for README: F3 opens/closes log; Left older run, Right newer run; Up/Down selects a message, PgUp/PgDn moves a selection page; Enter opens full detail. In detail Up/Down/PgUp/PgDn scroll, Home/End go to start/end, Esc returns to list. R refreshes list/run metadata or reloads current detail (upper/lower case accepted). Left/Right also changes run from detail. Esc from list and F1/F2 return to panes; F10 retains quit behavior. Log input is consumed and never forwarded to a harness. Read-only detail has no resend/paste action.

Source references for integration review:

- history.go:126–188: asynchronous run/history/detail requests carry generations and identities. model.go Update rejects stale results after navigation, refresh or log close. Run-read pending state fences current-run refresh; detail navigation releases invalidated history requests rather than leaving refresh permanently blocked.
- history.go:226–295: preserve readable lists on refresh errors, preserve same-message detail after failed reload, keep selection by ID, and never display another message's body under a newly selected ID. Read errors are sanitized in feedback, including when a router banner also exists.
- history.go:530: compact two-line run header includes date/time, end reason and interrupted count. Full ID, start/end timestamps, end reason and interrupted count remain in scrollable detail. Header ID/reason may be abbreviated at narrow widths; full metadata is available in detail. Empty runs have no message detail to open.
- history.go:635–791: detail includes full sanitized body, reply link, creation time and chronological timestamped state changes. Body newline boundaries remain, tab stops use display cells, and ANSI/C0/C1 controls are removed before grapheme-aware wrapping. Scroll range is clamped for rendering without rewriting the requested offset in View.
- history.go:813: log replaces the two live-preview footer rows with wrapped navigation help, avoiding mixed run identities and preserving key discovery at width 40. Reads list the newest 200 runs and newest 200 messages per selected run; boundary feedback states those limits. No pagination is introduced.

Final checks for this extension: gofmt -l internal/tui produced no output; go build -mod=readonly ./internal/tui and go vet -mod=readonly ./internal/tui exited 0; git diff --check produced no output. Unrun tests cover older-run navigation/detail, full body and history tail, stale responses after switch/close, read failures, failed reload preserving prior body, detail-close refresh recovery, wide-character tabs/sanitization and narrow help. No tests, UI, app binary, harness, probe or remote session ran. No commits or push. Key behavior, wrapping, read races and actual storage integration await user-directed testing.


## Input/freeze candidate (latest)

Mouse/Ctrl+C and freeze follow-ups qitem-20261104145908-5cf49d9699fb48f8 and qitem-20261104151413-2b18cba0d8af038a are incorporated. Current source proof, exact changed paths, final hint wording, checks and residual risks are in frontend-input-bugs.md, Final candidate; that section supersedes older pane locking/direct-child descriptions above. All behavior remains untested.

Final darwin/arm64 and linux/amd64 build+vet, default and rtktest, exited 0; native whole-tree build exited 0; gofmt/diff whitespace clean. No tests or binaries ran. Start/New/Slot signatures remain unchanged. tui.Pane additionally requires InputReady, MouseEnabled, SendMouse and RequestResize. UI reads cached atomic frame/status; router writes are in ordered Cmds. Ctrl+C policy is user-decided; one-second timing remains an untested guess.

Paste over 1MiB is refused before bytes, with draft conservatively held. Slow paste can time out partially after three seconds without permanently killing the drain; a stuck Cmd is visible, and emulator timeout/real OS failure latches unavailable. Worker/Cmd recovery and complete descendant cleanup remain limited. Actual cause of the user's freeze is unestablished. Handed to lead immediately after final compile checks; no further hardening in this candidate.
