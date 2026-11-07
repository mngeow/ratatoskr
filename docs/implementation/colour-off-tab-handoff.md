# Colour-off active tab review fix

Source qitem-20261107014026-8c371650e30c9629; return delivery.lead.
tabs.go tabSegment now uses a separate lipgloss Bold+Reverse path for the active
tab when colours are off. No foreground/background set. Inactive tabs and
theme.style remain plain; same labels and measured hit boxes. No hint or badge.

Pinned source finding: Bubble Tea v2.1.1 cursedRenderer.setColorProfile forwards
to ultraviolet TerminalScreen. ultraviolet cell.go ConvertStyle preserves
attributes while clearing colours for ASCII, but returns Style{} for NoTTY.
Therefore a real ASCII terminal can show bold/reverse; NoTTY strips attributes,
leaving the > marker/labels. colorprofile v0.4.3 writer.go similarly strips all
escapes at NoTTY and retains non-colour SGR parameters through its ASCII path.
This is read-source evidence, not a drawn result.

theme_test.go adds active bold/reverse, inactive plain, stripped-text/width
equality and unchanged theme.style assertions. Existing strip-SGR comparisons
remain valid. Tests UNRUN under GR-3.

gofmt complete; Darwin arm64 CGO_ENABLED=0 build/vet -mod=readonly ./internal/tui
both exit0 without native prompt. No TUI/test/browser/harness/probe run, no
commits/push. Actual display remains unverified.
