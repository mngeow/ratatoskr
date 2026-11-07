# SSH entry review refinements

Source qitem-20261107003953-c5bcb1b77dc1c58d; return to delivery.lead.
Review fixes applied directly as requested; no new feature scope or monkey task.

- SSH Cancel now sends SIGTERM. WaitDelay is 2 seconds, marked an UNTESTED guess,
  so a stuck child still gets the os/exec kill fallback.
- Read termios immediately before Start. After any unsuccessful started SSH,
  restore that saved state before display reset or opening another window.
  Restoration failure returns an error wrapping errTerminalRestore and is fatal.
  Clean exits leave termios alone; a failed Start never restores settings that
  SSH did not change. No return-path input flush.
- Manual line uses the daemon's existing 'on <host>:' prefix unchanged.
- Corrected stale refusalCode and refused-argv comments.

Paths: internal/attach/entry.go, attach.go, entry_test.go;
internal/tui/network_entry.go, network_entry_test.go.

Added fake-only test cases for Cancel/WaitDelay configuration, restore on failed
wait, no restore on successful wait, fatal restore failure, and manual-prefix text.
All tests remain UNRUN under GR-3. No SSH, ioctl, application, daemon or probe ran.

Checks actually finished, no native prompt:
- gofmt on the five files.
- GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly
  ./internal/attach ./internal/tui ./internal/windowclient: exit0.
- Same environment/packages with go vet: exit0.
No Linux check requested or run in this review pass; lead owns integration checks.

Signals/process groups, termios restoration, graceful timeout/forced kill and the
600ms late-reply mitigation remain untested. I agree with the accepted SIGINT
gap between windows: terminal cooked, no SSH running, default SIGINT exits rtk
and leaves daemon sessions up. No change to that accepted limit.
