# Spike 1 initial prototype brief

**CANCELLED 2026-11-04 under GR-3. Do not execute this brief.**
Lead cancelled Spikes 1/2/3 following the user's instruction: no testing or pushing until the user specifies when/how. Read docs/design/ground-rules.md before any revised work. No prototype code exists and all behavior is untested. The original brief below is retained as historical context only.

Execution record: frontend read docs/source/help and inspected identity/queue/Git state; ran go version, go env GOPATH GOMODCACHE, command -v claude codex tmux, claude --version, codex --version, tmux -V. Frontend made a public documentation search. No application build/vet/test, module fetch, PTY, private tmux session, real harness session or model task was started. No test was in flight or cancelled; nothing is left running. The commands' output is in the frontend OpenRig transcript at 2026-11-04 09:48–09:49Z. Monkey reports only read-only orientation/version discovery; zero implementation, no build/vet/module fetch or child harness/model runs, nothing in flight or left running. It closed qitem-20261104094855-20e6fba4ba0d730e as canceled before explicit go was given.

Supervisor: engineering.frontend. Parent: qitem-20261104094747-99ef4c82cf5e3d51.

Question: can two real full-screen harnesses remain usable inside Bubble Tea through x/vt? Your task is the bounded prototype only; frontend owns real harness runs and the verdict.

Allowed edits: spikes/1-panes/** only. Root go.mod/go.sum, production code, docs, rig files and owner settings are off limits. No commits. Application code is Go (GR-1). TUI uses charm.land/bubbletea/v2; no v1 Bubble Tea or other direct TUI framework (GR-2). Approved dependencies in this spike's own go.mod: Bubble Tea v2, github.com/charmbracelet/x/vt, github.com/creack/pty; Charm companion libraries as needed. Resolve current stable versions, pin them, and report versions.

Create a small package main with main.go (flags and Bubble Tea model) and pane.go (PTY/emulator lifecycle). CLI: -dir absolute throwaway child directory, -left executable, -right executable. Each executable has no additional arguments for this first draft. Refuse missing -dir and repository cwd. Children use that directory and inherited login environment after removing TMUX, TMUX_PANE, every OPENRIG_* and RIG_* variable; set TERM=xterm-256color and COLORTERM=truecolor. Do not modify permission modes or owner configs.

Pane holds PTY, process, x/vt emulator and mutex. Start each child with pty.StartWithSize. Feed PTY output into emulator; drain emulator.Read back into PTY so terminal query responses and encoded input reach the child. Render emulator.Render. Resize PTY and emulator together. Forward keys using emulator.SendKey with the library's key event type (inspect its actual API); preserve modifiers, text and special keys. Forward paste using the emulator paste API if present. Do not implement a hand-maintained subset of key strings. Serialize emulator access as its API requires. Ensure the Read response loop is not holding the emulator lock while blocked.

Model displays two panes side by side and one short status line. F1 focuses left; F2 focuses right; F10 exits. All other keys including Ctrl+C and Esc go to focused child. Resize divides available width with each pane's own dimensions. Use a bounded 30Hz tick for redraw; avoid unbounded message queues. Distinguish start/read/exit errors in the affected pane; do not kill the surviving child. Cleanup closes PTYs/emulators and terminates only processes this program started, waits for them, and restores outer terminal. No tmux use in your task.

Expose small methods Start, Resize, Render, SendKey, Paste, Close (exact signatures your choice); keep code straightforward and report any library API mismatch instead of inventing an API.

Acceptance for your draft: run go build ./... and go vet ./... in spikes/1-panes. No real claude/codex launches; no manual stub needed yet. No tests required for your initial version. Report commands/results, resolved versions, known gaps and files changed. Stop after buildable draft and return to frontend. Do not write verdict or expand scope. First acknowledge this brief and wait for my explicit go.
