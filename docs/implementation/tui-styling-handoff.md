# Daemon TUI styling candidate

Source qitem-20261107012731-d413573f5276f17b; next owner delivery.lead.
Monkey wrote confirmed first version; frontend took over, completed rendering
integration and per-field spans, and restored the plain local header path.
Monkey slot released. No commits/push, dependency or transport changes.

## Actual presentation

Tabs use padded ASCII block boundaries, active > marker, white bold on terminal
surface #0d0f12 and accent #e0b35c marker; inactive #aab1b9 on #20242a. The whole
bar (gap/title/right padding) is painted, not unstyled spaces after resets.

Full plain strips, 28 cells:

    > [ Terminals ]  [ Network ]
    [ Terminals ]  > [ Network ]

Below28, compact strips, 9 cells:

    >[T]  [N]
    [T]  >[N]

Tab marker/padding belong to their tab; two gap cells belong to neither. One
tabLayout measures plain strings for rendering/hits; colour and host counts never
affect geometry. At40 both full labels fit with remaining title cells. Below9 the
compact strip clips to visible cells, with no invisible hit targets. No badge.

Network: ordinary rows #d9dde2 on #0d0f12; selected background #2d3a4b across
ALL cells/padding, > marker #8ab4ff. Host titles #e0b35c; Claude/Codex address/name
tints #e8926a/#6cc4d6 from Harness/Name fields, never parsed text. State symbol+
word: idle green #74c98f, busy info #8ab4ff, needs you warn #e0ac5c, starting
accent, unknown/exited muted #8d949c. Old rows keep their old/age words and state/
input gets warning rather than fresh green. Held input green only when not old.
No-report text stays explicit, muted. Gateway connected green, unreachable bad
#ee8f87, other unavailable states warning; kept snapshot ages remain. Existing
detail row #1a1e23, no added border row.

Daemon headers keep words INPUT HOLDER (green), VIEWER (info), SESSION ENDED
(bad), with a separate focus > cue and known paired background. Existing full
separator is line #343a41 on terminal surface; message/log text and hints use
muted/text/accent roles. Hint key tokens are brighter text. Empty command line
accent, other guidance muted, including padded empty body cells. Feedback errors
use bad with their existing explanatory words.

Every reported span is stripped of ANSI/control/bidi formatting and cell-truncated
BEFORE colour is applied. Styled UI segments close/reset; selected padding carries
the selected background. Harness paneLines, fitStyled and frame bytes unchanged.
The shared plain-rtk header's original lipgloss treatment is restored exactly;
only daemon windows respond to profile messages for the new theme.

## Capabilities and deliberate differences

Tea v2.1.1 ColorProfileMsg reaches Update after the renderer. The model stores the
message's Profile.Convert as a stdlib func(color.Color)color.Color; no direct
colorprofile import/dependency change. Palette conversion supports truecolour,
256 and16 profiles; ASCII/NoTTY/Unknown and nonempty NO_COLOR disable colours.
Empty NO_COLOR does not disable them. This explicitly follows no-color.org rather
than the pinned detector's ParseBool-only rule. Before the profile arrives the
theme is inert. Locale LC_ALL > LC_CTYPE > LANG decides UTF8; unknown/nonUTF8 or
no-colour uses ASCII symbols. No probes or capability queries added.

Differences for architect's README/spec notes (docs remain architect-owned):
- ASCII padded brackets/marker replace rounded HTML corners, pills and underline
  row; no hover and no host-count badge.
- No new pane frame rows or columns. Colour existing header/separator only;
  pane-body separator column remains unchanged and child content untouched.
- Existing Network header is metadata rather than a new small-caps column row;
  existing single-line detail has a dark surface without an extra frame.
- State glyphs use ● ◐ ▲ ? ○ … in colour+UTF8; ASCII o * ! ? x . otherwise
  (single dot for starting keeps the glyph one measured cell). Host liveness
  retains its existing live/not-live words and report/age facts, with colour.
- No-colour is plain boundaries/marker/words, without reverse-plus-colour tricks.
- Existing log/hint arrows and the UTF8 Network state glyphs can be ambiguous
  width in some fonts. None enter tab hit ranges or pane-body geometry.
- Narrow feedback/manual/detail text still clips to existing rows. Minimal
  screens prioritise strip and readable muted guidance, not full colour detail.

No styling visually verified: no TUI/browser was opened or rendered. This does
not claim a visual match to the HTML or proven contrast on a real terminal.

## Files/checks

New internal/tui/theme.go and theme_test.go. Modified tabs.go, model.go,
runtime.go, network_render.go, empty.go, and empty_test.go's padded-tab assertion.
No attach/SSH/transport or authoritative docs edits. Own brief/handoff notes only.

Final checks actually completed without native prompts:
- gofmt touched Go files.
- GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -mod=readonly ./internal/tui:0.
- Same environment/package go vet:0, including test type-checking.
No Linux check in this pass; lead owns integration checks.

Written tests UNRUN: profile levels, nonempty/empty NO_COLOR, identical cells with
the same ASCII glyph policy, profile-independent hit cells, width clipping,
selected padding backgrounds, reset termination, unchanged harness frame output,
unchanged local render, unknown-state wording and sanitization of hostile reported
text. Existing mouse/zero-window tests remain UNRUN. No tests, rtk, application,
daemon, SSH, browser, harness, socket or probe executed (GR-3).
