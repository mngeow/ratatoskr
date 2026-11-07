# SSH entry frontend candidate

Parent qitem-20261106161140-ff6b29396d719685. Owner frontend; next owner delivery.lead.
Both first versions were written by build.monkey under separate confirmed briefs;
frontend took over and refined them. Monkey released at 16:26Z. No commits or pushes.

## Result

Daemon Network Enter queries the existing enter plan. Local rows use LocalID even
when registered with a different gateway SessionID. Local/in-window focuses that
slot; not-in-window says exactly 'not in this window; reattach to see it'. Manual
and refused plans remain visible; no command is inferred. Entry requests are single
flight and late replies after leaving/reopening Network are ignored without wedging
the next Enter. Enter never forwards child input or marks drafting.

An SSH plan produces a copied EntryIntent and intentional Tea quit. The attach loop
waits for Run to return, closes the client (releasing holds), then runs only the
daemon's exact seven-element Argv, with fixed prefix validated and no shell.
Returning makes a fresh client/model with empty Agents and ExpectSession, preserving
the raw selection and opening/refetching Network. No sessions are restarted. Zero
slots and reconnect failure exit through the existing errors. ClassifySSHExit /
ClassifyStart provide outcomes; SSH failure also prints Manual. Outcome remains
visible in the reopened tab via SetWindowFeedback.

SIGINT is caught/drained by the parent throughout handover and SSH, never SIG_IGN;
SSH remains in the terminal process group with normal signal disposition. SIGINT
during handover aborts that attempt. HUP/TERM cancels only the owned SSH process and
exits without reopening. Window contexts remain per window. Ctrl+C/F10 daemon
detach policy otherwise unchanged.

Before SSH: preserve termios, disable ECHO/ECHONL, wait 600ms cancellably, flush
input only, restore exact termios. A failed flush does not launch SSH and reopens
Network with the reason; failed termios restoration exits rather than opening
another UI with unknown terminal settings. After nonzero SSH/start failure/signal,
write the contract's targeted reset (never RIS); reset failure exits with an error.
Clean exit0 gets no reset. No return-path input flush.

Lead's F5 follow-ups included: drop matching local-host snapshot duplicates only;
retain disagreements; split ansi import group; nil report says 'no report'; remove
the obsolete 'Enter is not available here yet' wording.

## Paths

- internal/windowclient/entry.go, client.go refusal branches; entry_test.go (UNRUN).
- internal/tui/network_entry.go; network.go, network_render.go, daemon.go, model.go;
  network_entry_test.go (UNRUN).
- internal/attach/attach.go; entry.go, terminal.go, terminal_darwin.go,
  terminal_linux.go; entry_test.go (UNRUN).
- Briefs: ssh-entry-frontend-brief.md and ssh-entry-loop-brief.md beside this file.
Other worktree changes belong to existing/peer work and were preserved.

RefusalError preserves existing Dial error text via wrapping and exposes RefusalCode
for attach's existing exit3 mapping. Enter uses queryContext and copies Argv.
NewDaemon signature unchanged; new model seams: SetEnteredViaSSH, ResumeNetwork,
EntryIntent (deep copy), SetWindowFeedback. No new dependency.

## Evidence and limits

Final checks actually run, all exit0, for ./internal/attach ./internal/tui
./internal/windowclient with -mod=readonly and CGO_ENABLED=0:

- GOOS=darwin GOARCH=arm64 go build and go vet.
- GOOS=linux GOARCH=amd64 go build and go vet.
- gofmt applied to touched Go files.

Sandbox cache access initially failed; the same compile-only checks passed after
authorized cache access. Nothing built was run and no executable was written into
the repo. Tests were authored and type-checked by vet, NEVER executed (GR-3).
They cover LocalID vs registration identity, stale view responses, second-hop block,
intent copy isolation, duplicate/no-report rendering, refusal codes, enter query
body, fake loop return/no-restart/signal decisions, exact reset/argv, and fake
termios echo/flush/restore/cancellation paths.

Residual risks, all UNTESTED:

- SSH, auth prompts, signals/process-group behavior, ioctls, terminal display,
  reconnect and session ownership have never run in this candidate.
- The 600ms mitigation is based on reading Bubble Tea v2.1.1 source, not proof:
  tea.go shutdown cancels/closes the input reader before renderer cleanup;
  tty.go waitForReadLoop caps waiting at500ms; executeQuery documents reply leakage.
  The mitigation loses handover typeahead and cannot catch arbitrarily late replies.
  Lead approved this trade-off plus echo suppression; no return flush is applied.
- A failed exact termios restore or targeted reset is fatal. No attempt to repair
  user terminal policy or other terminals is made.
- Manual guidance and return outcomes occupy existing feedback space, which crops
  at narrow widths; the outcome/manual fallback is also printed between windows.
- This does not establish a cause or fix for the separately parked prompt-box text
  defect. No code was changed under that defect's row.

Window keys: Network Enter opens selected session; Enter on non-session asks to
select a session. '--entered-via-ssh' blocks other-host Enter with 'press F10 to
return to the machine you came from, then choose from there'. Existing daemon F10
detaches; viewer's first Ctrl+C detaches; holder Ctrl+C interrupts then detaches.
Network hints add 'Enter open session'; F5/Esc terminals, arrows/page select,
R refresh, F6 focused terminal input, F10 detach remain.
