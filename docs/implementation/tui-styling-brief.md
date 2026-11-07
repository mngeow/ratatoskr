# Daemon TUI styling — bounded first version

Parent qitem-20261107012731-d413573f5276f17b, frontend owns refinement.
Authority: tui-styling-spec.md and ADR0004 dark mock styles. User: 'TUI looks very
ugly ... add some color ... not obvious ... tabs ... build according to [ADR4]'.
Read those and live sources before confirming. Lead released monkey slot now.

## Allowed files

New internal/tui/theme.go; tabs.go, network_render.go, model.go, runtime.go,
empty.go, history.go and connected_history.go ONLY as needed for UI rendering.
No tests from monkey; no attach/transport/logic/deps/module/docs/owner changes.

## Design

Style only daemon-owned UI. Plain local rtk stays byte-identical. Never alter
paneLines/fitStyled, emulator frame contents, mouse routing, geometry or SSH.
No new rows/borders, chromeRows7 and screenTop2. Colour existing separator/header
instead of inventing pane frames. No badge (stable hit boxes); no hover or rounded
corners/pills. ASCII [ ] boundaries/edge markers in tab hit cells avoid ambiguous
Unicode glyph widths. Visible > marker stays even without styles.

Theme palette constants from spec, paired foreground/background for EVERY styled
cell including spaces. Add uiTheme to Model, initialized in allocateModel with
explicit noColor = os.Getenv("NO_COLOR") != "" (empty NO_COLOR is NOT set), and
UTF8 locale policy from LC_ALL then LC_CTYPE then LANG (contains UTF-8 or UTF8,
otherwise ASCII). Initial colour disabled until ColorProfileMsg; Update stores
msg.Profile.Convert as func(image/color.Color) color.Color (stdlib type), sets
colours iff conversion of a palette colour is nonnil and !noColor. This uses
existing lipgloss plus the message's method, NO direct colorprofile import.
Source: pinned profile.go Convert nil for Unknown/NoTTY/ASCII, 16/256 conversion,
truecolour passthrough; Tea forwards ColorProfileMsg to Model.Update; lipgloss
Style.Render emits ANSI from chosen colours and does not detect NO_COLOR itself.
Upstream envNoColor parses bool only; explicit nonempty override matches lead.
No probes/profile queries. No-color render is separate plain marker/word path,
no coloured style with reverse piled on. ASCII symbols for noColor or nonUTF8.

## Tabs

tabLayout remains SINGLE measured plain cell/hit description. Full labels padded
inside boundaries: '> [ Terminals ]  [ Network ]' / '[ Terminals ]  > [ Network ]'
(choose exact fixed spaces consistently; marker part of active hit area). Full
form fits40; abbreviate only below measured full form, KEEP existing [T]/[N]
compact text, active marker. Geometry independent of profile and never host count.
Selected white bold on #0d0f12, accent #e0b35c marker/ASCII edges; inactive
#aab1b9 on bar #20242a. Entire row including gaps/title/right padding bar colour;
selected surface occupies only tab cells. Sanitize/cell-truncate title BEFORE
styling; do not fitStyled-pad with unstyled blanks after styles reset. Model's
daemonTitleRow uses same labels/hits and styles each padded segment. Colour-off
text identical to ansi.Strip(colour-on), stable active cue. No colour reverse.

## Network and other UI

Sanitize cleanNetworkText -> truncate/pad PLAIN by cell width -> style segments.
Do not emit untrusted escapes from reported names/state/errors at any stage.
Network rows may carry structured spans/role metadata from their actual fields;
never infer known state by parsing text from a host/name/reason.
Host rows accent; local daemon/gateway headings muted/accent. Session state
symbol+word (idle ● /busy ◐ /needs_you ▲ /unknown ? /exited ○ /starting …), ASCII
fallback o/*/!/ ?/x/...; state words retained, needs_you shows 'needs you'. Unknown
unrecognized state stays literal+'unknown to this build', never idle. Fresh/live
green, stale/not-live warning with existing old/age words; no report muted.
Claude/Codex name tint #e8926a/#6cc4d6. Held input green, free/unknown words remain.
Use structured spans if necessary for separately tinted state/name cells; all
selected-row spans share bg#2d3a4b to full width, ASCII > marker in focus#8ab4ff
(avoids width-ambiguous ▌). Ordinary rows bg#0d0f12. Detail row existing one line
bg#1a1e23, no new border row. Error text ! plus bad#ee8f87, gateway words+age stay.
Selection/plain widths identical across color profiles.

Daemon pane headers spell INPUT HOLDER green, VIEWER info/muted, SESSION ENDED
bad, unknown state never green. Existing focus header gets focus accent, rest
muted. Must not tint child output or the separator column between pane body rows.
Existing full separator row #343a41. Empty guidance muted/command accent; log
view text/headers in same palette via wrapper over OUR plain history output.
Feedback/status hints muted with keys in text colour if practical, errors bad.
No UI background/foreground may leak into a pane: each UI style closes with reset.
Normal and minimal render paths both retain styled tabs and sanitized UI lines.

## Checks / stop

GR-1 Go; GR-2 existing Bubble Tea v2/Charm only. GR-3 no tests/app/TUI/browser/
harness/SSH/daemon/probes, no Git writes/push, no new deps, no owner settings.
Only gofmt allowed files, Darwin build/vet -mod=readonly ./internal/tui. Skip a
check requiring native approval and report; no escalation. Nothing built run.
Confirm scope/questions and await explicit go. Return files/exact labels/styles/
checks/gaps through queue; all behavior UNTESTED, never claim visual match.
