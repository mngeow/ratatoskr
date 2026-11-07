# TUI styling spec: Herdr-style tabs and Monokai chrome

Status: **design for implementation, 2026-11-07. Not built, never drawn.** Source of truth: the dark `.mock` styles in [ADR-0004](../adr/0004-network-tab/index.html) (palette values below are read from its CSS). The owner tested the tab correction and said: "ok much better. However, the TUI looks very ugly, can you add some color to it? and also, it is not obvious that the Network and Terminals are tabs. Please refer to ADR 4 mockups and build it according to that." (The "much better" is reported only for the tab correction, nothing wider.)

## Rules
1. **Style only the surrounding UI.** Never restyle, recolour or rewrite a harness pane's contents. Pane frames, headers, the title/tab row, the Network view, the status line and the log view are ours.
2. **Colour is never the only signal.** Every colour cue also has a word or symbol. ASCII fallback for symbols when the terminal is not UTF-8 or `NO_COLOR` is set.
3. **No new rows, no offset drift.** `chromeRows` 7 and `screenTop` 2 stay. If any change needs a row or shifts a cell, update the size maths, hit boxes, drag handling, mouse forwarding and tests together. Prefer styling that costs no rows.
4. **Terminal capabilities.** Use lipgloss colour profile downsampling (truecolor, 256, 16, none). With `NO_COLOR` or no colour, fall back to today's reverse-video and marker cues, which must stay readable. Keep contrast readable on both light and dark terminal backgrounds by always setting foreground and background together on a styled cell.
5. **Keep what exists** (the bracketed `[T] [N]` form and the always-on `>` marker are replaced by round 2 below): F5, eager fetch, the zero-session Network window, SSH entry semantics.
6. **Deliberate differences from HTML** (record in code comments and the README): no rounded corners or pills (use block cells), no hover colour unless the terminal reports motion (optional), one-cell-high underline replaced by a coloured bar or marker.

## Round 2 (2026-11-07): Herdr-style tabs and a Monokai palette

Owner feedback on the first styling build: "This is not bad. However: 1. The Network and Terminal tabs do not look or function like tabs, I want it to function like herdr tabs 2. Color theme wise, I want more of a one dark color theme, like monokai. Otherwise it looks good." Recorded at that scope: the owner saw the first build and found it "not bad" and otherwise good. Everything not named below is kept.

### What Herdr's tabs are (read-only: Herdr 0.9.5 install README, CHANGELOG, the default-config comments and option names inside its binary; **never run and never seen drawn**)
- A **tab row** of contiguous tabs, at the top by default (`ui.tab_bar_position` top or bottom), with right-aligned status entries and an option to hide it when there is a single tab.
- **Labels are centred in their tab, with symmetric padding**, and the **active tab is a highlighted block**. Muted labels were fixed to stay readable on dark themes. Overflow keeps the last tab fully visible. A tab with a zoomed pane is marked in the bar. Clicking a tab switches to it.
- Keyboard is **prefix based**: `prefix+c` new tab, `prefix+n` / `prefix+p` next and previous, `prefix+shift+t` rename, `prefix+shift+x` close, `keys.switch_tab` and `keys.indexed.tabs` for numbers, optional `keys.move_tab_previous` / `move_tab_next`. Those are Herdr's configurable defaults as written in its config comments. `rtk` has no prefix mode and its tabs are fixed categories, so **no new keys are invented**: the keyboard path stays F5 (toggle) and Esc (back), and Herdr's other bindings are not claimed.

### Scope (confirmed by the owner via the operator, 2026-11-07: "Terminals and Network should remain 2 tabs for now")
Keep the two categories, **Terminals** and **Network**. Give them Herdr's tab look and click behaviour: a contiguous bar, no brackets, centred labels with symmetric padding, a filled block for the active tab, muted inactive tabs, click anywhere in a tab block to switch. **Per-session tabs are not built**; the owner confirmed two tabs for now. Herdr's new/close/rename/move are not applicable to fixed categories.

### Palette: classic Monokai chrome (replaces the ADR-blue palette above for new work)
| Role | Hex | Use |
| --- | --- | --- |
| Background | `#272822` | Network view, empty-window text, dark surfaces |
| Tab bar | `#1e1f1c` | the whole row-0 bar |
| Foreground | `#f8f8f2` | text |
| Muted | `#a6a28c` | inactive tab labels, hints, column headings (a lighter comment tone for contrast; Monokai's own comment `#75715e` is for decoration only) |
| Line | `#3e3d32` | separator rows, detail surface |
| Selection | `#49483e` | selected row background |
| Active tab | `#272822` bold on `#a6e22e` | the active tab block |
| Green | `#a6e22e` | live, connected, input holder |
| Yellow | `#e6db74` | accent, host titles, headers |
| Orange | `#fd971f` | stale, warn, needs you, Claude tint |
| Pink/red | `#f92672` | error, unreachable, session ended |
| Cyan | `#66d9ef` | focus, selection bar, viewer, Codex tint |
| Purple | `#ae81ff` | unknown (still never styled like idle), badges |

### The tab row (row 0, no new row, same hit-box rules)
- The row is `#1e1f1c`. Each tab is a block of cells with **one padding cell on each side of its centred label**: ` Terminals ` and ` Network `, touching the next tab with at most a one-cell gap. No brackets.
- **Active:** `#272822` bold on `#a6e22e`. **Inactive:** `#a6a28c` on the bar. The whole block, padding included, is clickable, and `tabLayout` stays the single source for drawing and hit boxes.
- **Colour off or low colour:** the active tab is bold and reverse; the `>` marker is **kept only when colour is off** (it was the non-colour cue). With colour on the block alone says which tab is active.
- Narrow form: ` T ` and ` N ` blocks, same rules.
- The title and any hint sit after the tabs on the same bar. No host-count badge or hint is added unless the owner asks.

## Superseded first palette (kept for the record)

The first build used the ADR-0004 dark theme (bluish `#0d0f12` / selection `#2d3a4b`). It is replaced by the Monokai table above.
| Role | Hex | Mockup use |
| --- | --- | --- |
| Terminal background | `#0d0f12` | `.mock` surface |
| Tab bar background | `#20242a` | `.tabstrip` |
| Line / frame | `#343a41` | borders, status divider |
| Text | `#d9dde2` | `--term-ink` |
| Dim / muted | `#8d949c` | `.dim`, labels, status text |
| Tab unselected | `#aab1b9` on bar | unselected tab |
| Tab selected | `#ffffff` bold on `#0d0f12`, accent underline/bar `#e0b35c` | selected tab |
| Accent / header | `#e0b35c` | `.hd`, selected tab bar |
| Selection row | bg `#2d3a4b`, left bar `#8ab4ff` | selected session row |
| Focus / info | `#8ab4ff` | focus ring, selection bar |
| OK / live / holder | `#74c98f` | `.ph.hold`, live |
| Warn / stale | `#e0ac5c` | `.row.stale` |
| Bad / error | `#ee8f87` | `.screen.bad` border |
| Badge | `#dbe8f7` on `#34506e` | host-count badge |
| Claude / Codex tint | `#e8926a` / `#6cc4d6` | harness names |

## Network view (colours now from the Monokai table)
- Column header row dim (`#8d949c`), small caps feel via spacing, not a new row beyond what exists.
- Host group titles in accent `#e0b35c`. Session rows: name in text colour, harness tinted, **state with a symbol and word**: `● idle`, `◐ busy`, `▲ needs you`, `? unknown`, `○ exited`, `… starting`. `needs_you` uses warn, `unknown` dim and never styled like idle, `exited` dim.
- **Freshness:** live host `● live` OK green; stale or not-live `◌ stale 42s` warn with the age as text; no report `– no report` dim. Input: `held`/`free`/`unknown` as words, holder in OK green.
- **Selected row:** bg `#2d3a4b` with a `▌` left bar in `#8ab4ff`, plus the existing marker character so it survives no-colour.
- **Detail pane** (if there is room): bordered `#343a41`, bg slightly lighter than the terminal; gateway errors with a `!` and bad colour in a left bar, the message in text.
- Gateway line: `connected` OK, `unreachable`/`unsupported` bad or warn, each with the word, and the kept snapshot's age.
- Status line: dim, with keys in text colour (`b` equivalent), as `.status`.

## Terminals view (surrounding UI only)
- Pane header: dim, with `INPUT HOLDER` in OK green, `VIEWER` in dim/info, `SESSION ENDED` in bad colour, each still spelled out.
- Pane frame lines `#343a41`; the focused pane's frame uses focus `#8ab4ff`. Harness output cells are untouched.
- Empty window text uses dim and the accent for the command.

## Out of scope
Prompt-box defect, transport, SSH behaviour, new rows, any harness-content styling, themes beyond downsampling and NO_COLOR.

## Checks (written, not run)
Style functions are pure and table-testable (colour off: same text and widths as colour on, with no colour codes; bold and reverse video may remain on the active tab, because NO_COLOR forbids colour, not attributes; colour on: SGR present, widths identical). Hit boxes and widths are identical with and without colour.
