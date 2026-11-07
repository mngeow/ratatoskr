# SSH entry attach loop — bounded first draft

Owner frontend, parent qitem-20261106161140-ff6b29396d719685.
Read network-api-contract.md SSH entry in full, live attach.go, tui/network_entry.go,
entry/outcome.go. UI/client draft is now frontend-owned. Your allowed paths: only
internal/attach/attach.go and new internal/attach/entry.go, terminal.go,
terminal_darwin.go, terminal_linux.go. No tests in draft. No TUI/client/backend/docs edits.

## Shape

Run(Options) still maps errors through mapExit. Refactor one window into a helper
returning (*tui.EntryIntent,error); ordinary detach preserves finish messages,
SSH-intent return suppresses detach message. Each window fresh ctx/client/model.
Configure model.SetEnteredViaSSH(options.EnteredViaSSH) before Run. On resumed
windows call model.ResumeNetwork(intent.Selection) before Run, and surface the
previous outcome as feedback through a tiny Model setter IF already available;
otherwise print outcome before reopening and report that UI setter is senior work.
You may NOT edit tui for it.

Outer loop: first window uses initial options, every return after intent uses
Agents="" and ExpectSession="", preserving Version and EnteredViaSSH; no restart.
Existing zero-slot and reconnect errors exit unchanged. Ensure client closed
before running SSH. Program.Run must have actually returned; if forced cleanup
timed out, never SSH. Existing runUI forced-cancel path may return without Run
finishing, but cannot reach SSH because ctx cancelled: enforce that explicitly.

## SSH execution

Use exact plan.Argv via exec.CommandContext(rootCtx, Argv[0],Argv[1:]...), no shell,
no building commands from display/gateway strings. Refuse malformed ssh plans
(length !=7, executable !=ssh) rather than panic. stdin/out/err os.Stdin/Stdout/Stderr.
Print cleaned Note and opening line with intent.Address, never raw escape text.
Invoke entry.ClassifySSHExit or ClassifyStart and print nonempty Message. Exit255
also prints cleaned plan.Manual. New window always refreshes Network even when
Outcome.RefreshTab false. Do not retry SSH automatically or drop expected ID.

Signals: a root context watches SIGHUP/SIGTERM across the entire loop. On either,
stop only owned ssh and exit, reset terminal, never reopen. Per-window SIGINT
still cancels that window as today; while handover/ssh, SIGINT is caught through
signal.Notify on a continuously drained buffered channel, never signal.Ignore
(SIG_IGN survives exec). Register this catch before handover and unregister when
attempt ends. Do not create another process group; ssh must receive terminal
SIGINT normally. Root CommandContext kills only owned ssh on HUP/TERM and Wait
reaps it. No goroutine leak after successful exit.

## Handover terminal mitigation (lead approved 16:22)

After Bubble Tea Run returns and client closes, before ssh starts: turn off echo
on stdin's tty (ECHO and ECHONL), preserving exact original termios. Wait 600ms
with select on rootCtx.Done so HUP/TERM stays active. Flush INPUT ONLY, then
restore exact termios before executing SSH; on cancellation restore as well.
Any get/set/flush/restore failure means no SSH, return to Network tab with reason.
No input drain/flush on return from ssh. Do not read terminal bytes or change
fd blocking mode. Darwin get/set TIOCGETA/TIOCSETA; flush TIOCFLUSH with pointer
int 1 (FREAD in SDK sys/fcntl.h). Linux get/set TCGETS/TCSETS; flush IoctlSetInt
TCFLSH,TCIFLUSH. Existing x/sys/unix only. Functions split by GOOS in allowed files.
Comment source evidence: Bubble Tea v2.1.1 shutdown cancels reader before cleanup,
waitForReadLoop is capped500ms; executeQuery documents late-reply leak. This is
an UNTESTED mitigation, loses handover typeahead and cannot guarantee against
arbitrarily delayed replies. No claims of proven drain/quiescence.

After nonzero ssh exit/start failure or signal: write exactly targeted reset:
ESC[?1049l, ESC[?1000l, ESC[?1002l, ESC[?1003l, ESC[?1006l,
ESC[?2004l, ESC[<u, ESC[?25h, ESC[0m. No RIS, no input flush on return.
Expose reset as pure constant/helper for senior's unrun tests. Failed reset is
an error, not a silently ignored successful handover.

## Checks / stop

GR-1 Go; GR-2 existing Bubble Tea v2; GR-3 no tests, binary/app/SSH/harness/daemon/
socket/probes; no Git writes, dependency changes, user files or owner settings.
Only gofmt on allowed files, go build -mod=readonly ./internal/attach and go vet
same package. No executable output in repo. All behavior UNTESTED.
Confirm brief and ambiguities, await explicit go, return first version and exact
checks/gaps through durable queue; senior takes over and owns unrun tests.
