# Frontend structure and initial pane brief

Owner/supervisor: engineering.frontend. Candidate: qitem-20261104095414-220ec49432d2f84d. All behavior is untested under GR-3; cancelled spikes are not evidence.

## Structure

- internal/pane owns one PTY, process lifetime, x/vt emulator and input serialization. Start consumes protocol.LaunchSpec with a complete Env; no implicit inheritance. Snapshot returns rendered cells plus cursor position/visibility. Resize updates emulator and PTY. Close is idempotent and waits for its own child. App owns failed-start presentation and exit forwarding.
- internal/tui owns Bubble Tea model, two pane slots, layout, focus, draft flags, event consumption, history and quit confirmation. It consumes a narrow Pane interface, Router interface from interfaces.md, and a channel of protocol.Event. App supplies panes (or their start errors) and owns shutdown.
- Tests are authored without execution; only gofmt, go vet and go build compile checks may run. No application, harness, test binary or executable probe runs.

## Seam issues

PasteSubmit must write directly to the PTY, not x/vt.Paste: that method returns no error and merely queues pipe bytes, which cannot satisfy the real-write-error contract. It will reject ESC before any write, serialize bracketed paste and the separate Enter with user/query writes, and return any partial-write error. Paste-to-Enter delay remains an untested isolated constant. Query replies are terminal protocol plumbing, an additional legitimate PTY write source that the prose seam must name. x/vt's replies must be drained even when unfocused.

Source reading found x/vt v0.1.0 key.go explicitly lacks Kitty/CSI-u/modifyOtherKeys, and Shift+Enter with a modifier has no encoding case. This is a concrete compatibility gap, not evidence that both real harnesses are unusable. Per lead, map Shift+Enter to Ctrl+J in a single fallback function for claude/codex, marked UNTESTED. Claude documents Ctrl+J at https://code.claude.com/docs/en/terminal-config#enter-multiline-prompts. Codex official CLI docs did not establish Ctrl+J; upstream textarea.rs source is the supporting source (https://github.com/openai/codex/blob/main/codex-rs/tui/src/bottom_pane/textarea.rs). Other unsupported keys return an error for the status line. Never switch to tmux.

## Monkey initial task

Implement ONLY internal/pane/pane.go and internal/pane/doc.go. Allowed writes internal/pane/** (no tests yet); do not edit this note, root go.mod/go.sum, internal/protocol, tui, docs, spikes or rig files. GR-1: application Go only. GR-2: any TUI uses charm.land/bubbletea/v2, no old Bubble Tea or other TUI framework. GR-3: no tests, spikes, probes, harness launches, application runs, SSH testing, commits or pushes. gofmt/go vet/go build compile-only are allowed; nothing built may run. Do not run go mod tidy or change dependencies. Use already pinned pty, x/vt and ultraviolet.

Public API for integration:

    type Frame struct { Content string; X, Y int; Hidden bool }
    func Start(spec protocol.LaunchSpec, width, height int, onExit func(int)) (*Pane, error)
    func (p *Pane) Resize(width, height int) error
    func (p *Pane) Snapshot() Frame
    func (p *Pane) SendKey(key uv.KeyPressEvent) error
    func (p *Pane) Paste(text string) error
    func (p *Pane) PasteSubmit(ctx context.Context, text string) error
    func (p *Pane) Err() error
    func (p *Pane) Close() error

Start requires nonempty Argv, explicit non-nil Env, absolute Dir, positive bounded dimensions <=65535; use exec.Command and pty.StartWithSize. It must not start any process during your task. Start goroutines in code for PTY→emulator and emulator.Read→PTY. Callback exactly once after Wait. Catch PTY read/write errors in Err, suppress expected shutdown errors. Preserve output frame after exit. Bound scrollback (or disable it).

Use emulator mutex around all screen/state access (or SafeEmulator). Its Read blocks on an io.Pipe: drain it without holding a screen or PTY write mutex; lock PTY writes only after Read returns. Do not hold the PTY write mutex while calling emulator.SendKey/Paste: those block until the reply reader drains. Never call emulator.Close concurrently with Read: source's closed bool is not synchronized. Close the InputPipe's io.Closer to unblock Read, then shut down under your lifecycle locks. Avoid waiting on locks that a blocked io.Pipe writer needs while shutting down. Report any uncertainty rather than claiming this is settled.

Frame.Content is exactly Render(); X/Y/Hidden are CursorPosition/CursorHidden, no damage tracking. Disable scrollback with SetScrollbackSize(0). Err stores first non-shutdown transport error. Use SendKey for supported keys; apply fallback above for Shift+Enter, return named errors for other unsupported modified special keys. For printable text normalize comparison fields only as required by source. Keep PasteSubmit separate from user Paste: refuse ESC before writing, write one bracketed paste fully, then Enter as a separate write, under one input transaction mutex. Use isolated 50ms provisional UNTESTED delay. Honor context cancellation while waiting for transaction, during writes and delay; never send Enter after ctx ends. Error must report bytes written (define WriteError with BytesWritten and Unwrap). Use finite PTY write deadlines; cancel blocked write when context ends without modifying shared settings. No gate/retry logic. Full-write helper handles partial writes and zero progress. User Paste follows child bracketed mode via emulator.Paste; asynchronous transport errors stored in Err.

Stopping point: return first draft to frontend with files, static checks and known gaps; no expansion. Run only gofmt on your files, go build -mod=readonly ./internal/pane and go vet -mod=readonly ./internal/pane. If cache permissions fail, report to frontend; do not change owner configuration. No tests or harness launches. First acknowledge scope and await explicit go before any edit.
