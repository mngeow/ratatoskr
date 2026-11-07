# Round 2 block tabs / Monokai candidate

Source qitem-20261107020547-76f312c67f42d7cc; return delivery.lead.
Confirmed monkey first version, senior source review/comment refinement/test
updates. Slot released. Only production tabs.go/theme.go changed in this round;
tests theme_test.go/tabs_test.go/empty_test.go updated. No dependencies, Git,
authoritative docs, transport or attach changes.

## Exact tabs and geometry

No gap. Full blocks11+9=20 cells, compact3+3=6. Full at width>=20, compact below20,
clipped below6. Whole padding included in hits. First tab [0,11), second[11,20),
or compact[0,3),[3,6), intersected with visible width. No non-clickable gap cell.
One bar cell separates the completed strip from the title when space remains.

Colour on, either active tab (active is shown by filled bold block):

    " Terminals  Network "
    " T  N "

Colour off, Terminals active / Network active:

    ">Terminals  Network "
    " Terminals >Network "
    ">T  N "
    " T >N "

The > REPLACES the active block's left padding, never adds a cell. Profile arrival
changes one glyph and attributes, not widths or targets. Colour-off active stays
bold/reverse without any colour setters; NoTTY can strip those attributes as
reported earlier, leaving the marker. Both categories stay fixed; no new keys.

## Palette actually used

Active tab bold #272822 on #a6e22e, inactive #a6a28c on bar#1e1f1c. No coloured
reverse/marker. All surrounding daemon UI keeps paired foreground/background.
Background#272822, foreground#f8f8f2, muted# a6a28c (without the space), line/detail
#3e3d32, selected#49483e, green#a6e22e, accent yellow#e6db74, warn/Claude orange
#fd971f, bad pink#f92672, focus/Codex cyan#66d9ef, unknown purple#ae81ff. Unknown
state/input and unrecognized nonempty values stay purple, never green; old facts
still warn, exited and missing/no-report stay muted. Existing structured spans,
log/status/empty/header rendering use the new constants. Local original header,
harness cells, geometry7/2, mouse/draft/ownership, F5/Esc, fetch, zero-window and
SSH behavior preserved.

## Downsampling reasoning, not execution

Read colorprofile v0.4.3 Profile.Convert and x/ansi v0.11.9 color.go. Convert256
chooses the six-level cube or grayscale by HSLuv distance; Convert16 maps the
chosen indexed value using ansi256To16. Manual candidate arithmetic:

| Role | 256 candidates | corresponding ANSI16 indices |
| --- | --- | --- |
| bar#1e1f1c | 16 /234 | 0 /0 |
| active foreground#272822 | 16 /235 | 0 /0 |
| active background#a6e22e | 148 /246 | 10 /7 |
| muted#a6a28c | 144 /247 | 3 /7 |

Whichever candidate branch wins, active background and muted are numerically
distinct from the bar, and active foreground differs from its background. I did
not execute Convert or determine which HSLuv branch wins. This is source-based
reasoning, not rendered proof. Customized ANSI palettes can change the visual
contrast; dark surfaces deliberately override a light terminal's own background.
Other close palette roles can merge in16/256; state words/glyphs stay.

## Limits / checks

Matches only the spec's Herdr description: fixed category blocks and click
switching, not Herdr prefix/new/close/rename/move/overflow/per-session behavior.
No badge/hint/hover/new border or row. No visual match to Herdr or HTML claimed;
nobody has drawn this candidate. Narrow widths abbreviate then clip. Existing
UTF8 state glyph/font ambiguity and one-line detail cropping remain. Source
comments now state20/6 and replacement marker instead of old brackets/28/9.

Final checks actually finished without native prompt: gofmt; Darwin arm64,
CGO_ENABLED=0 go build and go vet -mod=readonly ./internal/tui, both exit0.
No Linux check by senior this round. Tests updated/authored but UNRUN: exact
full/narrow strings both actives/colour modes, all padding/hit cells, profile
transition widths, marker normalization, no-colour bold/reverse, unknown purple,
old warning, downsampled active/bar distinction, unchanged local/harness content.
No tests, rtk/TUI/browser/harness/daemon/SSH or probes run (GR-3), no commit/push.
