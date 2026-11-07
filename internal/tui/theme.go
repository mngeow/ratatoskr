package tui

// The daemon window's palette, and the one place that decides whether colour is drawn.
//
// Colour is never the only signal: every coloured cue also has a word or a symbol, so a terminal with
// colour off, a pipe, a monochrome theme or a non-UTF-8 locale still reads correctly. The ASCII path is
// what NO_COLOR and a non-UTF-8 locale force, and it is the reference the coloured path is checked
// against: with the same glyph policy, stripping SGR preserves the plain cells.
//
// The palette is classic Monokai chrome (round 2; the first build used the ADR-0004 blue mock). Every
// styled cell sets foreground AND background, so a light terminal cannot turn the bar into unreadable
// dark-on-dark; that does mean the Network view and the tab bar are the palette's dark surfaces on any
// terminal, which is the one place this palette overrides the user's own.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. Nothing has been drawn.

import (
	"image/color"
	"os"
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// uiSpan carries only presentation text and a role chosen from structured facts.
type uiSpan struct {
	text string
	fg   color.Color
	bold bool
}

func cleanUISpan(text string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, ansi.Strip(text))
}

// renderSpans cleans and clips plain cells BEFORE styling, including padding.
// Every segment uses the same background, so selected rows remain full width.
func (m *Model) renderSpans(spans []uiSpan, width int, bg color.Color) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	remaining := width
	for _, span := range spans {
		text := cleanUISpan(span.text)
		cells := ansi.StringWidth(text)
		cut := cells > remaining
		if cut {
			text = ansi.Truncate(text, remaining, "")
			cells = ansi.StringWidth(text)
		}
		fg := span.fg
		if fg == nil {
			fg = colText
		}
		b.WriteString(m.theme.style(text, fg, bg, span.bold))
		remaining -= cells
		if remaining == 0 || cut {
			break
		}
	}
	if remaining > 0 {
		b.WriteString(m.theme.style(strings.Repeat(" ", remaining), colText, bg, false))
	}
	return b.String()
}

func (m *Model) uiLine(text string, width int, fg, bg color.Color, bold bool) string {
	if m.daemon == nil {
		return fitPlain(text, width)
	}
	return m.renderSpans([]uiSpan{{text: text, fg: fg, bold: bold}}, width, bg)
}

func (m *Model) harnessTint(harness, name string) color.Color {
	if harness == "" {
		harness = name
	}
	switch harness {
	case "claude", "claude-code":
		return colClaude
	case "codex":
		return colCodex
	}
	return colText
}

// stateTint is the colour of one state cell.
//
// An old row is warn regardless of its state: the fact that it is history outranks what it said. An
// unknown - or unrecognised - state is purple and never green, and an exited session is muted, because
// neither is a healthy claim about now.
//
// UNTESTED.
func stateTint(state string, old bool) color.Color {
	if old {
		return colWarn
	}
	switch state {
	case protocol.SessionStateIdle:
		return colOK
	case protocol.SessionStateBusy:
		return colFocus
	case protocol.SessionStateNeedsYou:
		return colWarn
	case protocol.SessionStateStarting:
		return colAccent
	case protocol.SessionStateUnknown:
		return colUnknown
	case protocol.SessionStateExited:
		return colDim
	}
	// An unrecognised non-empty value keeps its literal and stays purple, never idle-looking.
	if state != "" {
		return colUnknown
	}
	return colDim
}

// inputTint is the colour of one input cell: held is green, unknown is purple, and an old row is warn
// for the same reason a state is.
//
// UNTESTED.
func inputTint(input string, old bool) color.Color {
	if old {
		return colWarn
	}
	switch input {
	case protocol.InputHeld:
		return colOK
	case protocol.InputUnknown:
		return colUnknown
	}
	if input != "" && input != protocol.InputFree {
		return colUnknown
	}
	return colDim
}

func (m *Model) styleLogLines(lines []string, width int) []string {
	if m.daemon == nil {
		return lines
	}
	out := make([]string, len(lines))
	for i, line := range lines {
		fg := color.Color(colText)
		if i < 2 {
			fg = colAccent
		}
		out[i] = m.uiLine(line, width, fg, colTermBG, i < 2)
	}
	return out
}

func (m *Model) hintRow(width int) string {
	if m.daemon == nil {
		return fitPlain(m.hintsLine(), width)
	}
	text := m.hintsLine()
	spans := []uiSpan{}
	for text != "" {
		end := strings.IndexByte(text, ' ')
		if end < 0 {
			end = len(text)
		}
		if end == 0 {
			end = 1
			for end < len(text) && text[end] == ' ' {
				end++
			}
		}
		word := text[:end]
		text = text[end:]
		fg := colDim
		if strings.HasPrefix(word, "F") || strings.HasPrefix(word, "Ctrl+") || word == "Esc" || word == "R" || word == "Enter" || word == "PgUp/PgDn" {
			fg = colText
		}
		spans = append(spans, uiSpan{text: word, fg: fg})
	}
	return m.renderSpans(spans, width, colTermBG)
}

// Palette roles, as classic Monokai chrome.
//
// Round 2 replaced the first build's ADR-blue palette with these values and added purple for
// "unknown", so an unknown state or input can never be mistaken for a healthy one. Two pairs share a
// hex deliberately - warn with the Claude tint, focus with the Codex tint - because the mock uses one
// orange and one cyan for both jobs; they keep separate names so a later change stays local.
var (
	colTermBG   = mustRGBA("#272822")
	colBarBG    = mustRGBA("#1e1f1c")
	colLine     = mustRGBA("#3e3d32")
	colText     = mustRGBA("#f8f8f2")
	colDim      = mustRGBA("#a6a28c")
	colTabIdle  = mustRGBA("#a6a28c")
	colTabSel   = mustRGBA("#f8f8f2")
	colAccent   = mustRGBA("#e6db74")
	colSelBG    = mustRGBA("#49483e")
	colFocus    = mustRGBA("#66d9ef")
	colOK       = mustRGBA("#a6e22e")
	colWarn     = mustRGBA("#fd971f")
	colBad      = mustRGBA("#f92672")
	colDetailBG = mustRGBA("#3e3d32")
	colClaude   = mustRGBA("#fd971f")
	colCodex    = mustRGBA("#66d9ef")
	// colUnknown is the purple role: unknown state and unknown input, never green.
	colUnknown = mustRGBA("#ae81ff")
)

// uiTheme is the window's colour state.
//
// It is deliberately inert until the terminal reports a colour profile: no probing, no querying, no
// environment sniffing beyond NO_COLOR and the locale, which are the user's own declarations.
type uiTheme struct {
	// noColor is true when NO_COLOR is set to a non-empty value.
	noColor bool
	// utf8 is true when the locale names UTF-8.
	utf8 bool
	// colours is true only when the profile can represent a palette colour and NO_COLOR is unset.
	colours bool
	// convert downsamples a palette colour to the terminal's profile. It is the message's own method,
	// so nothing here imports colorprofile.
	convert func(color.Color) color.Color
}

// newUITheme builds the theme from the environment alone. Colour stays off until a profile arrives.
//
// UNTESTED.
func newUITheme() uiTheme {
	return uiTheme{
		noColor: os.Getenv("NO_COLOR") != "",
		utf8:    utf8Locale(),
	}
}

// utf8Locale reports whether the locale names UTF-8, LC_ALL first, then LC_CTYPE, then LANG.
func utf8Locale() bool {
	for _, name := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		value := strings.ToUpper(os.Getenv(name))
		if value == "" {
			continue
		}
		return strings.Contains(value, "UTF-8") || strings.Contains(value, "UTF8")
	}
	return false
}

// applyProfile records the terminal's colour profile.
//
// Colour is enabled only when the profile can represent a palette colour - Convert returns nil for
// ASCII and below, which is the terminal saying it has none - and only when NO_COLOR is unset.
//
// UNTESTED.
func (t *uiTheme) applyProfile(message tea.ColorProfileMsg) {
	t.convert = message.Profile.Convert
	t.colours = !t.noColor && t.convert != nil && t.convert(colAccent) != nil
}

// glyphs reports whether the UTF-8 symbol set is used. Colour off forces ASCII.
// Plain/coloured equality uses the same glyph policy; Unicode substitution is deliberate.
//
// UNTESTED.
func (t uiTheme) glyphs() bool {
	return t.colours && !t.noColor && t.utf8
}

// style renders one segment with paired foreground and background, or returns it untouched.
//
// A segment that is not styled is returned as it was, so the plain path never depends on a reset.
//
// UNTESTED.
func (t uiTheme) style(text string, fg, bg color.Color, bold bool) string {
	if !t.colours || text == "" {
		return text
	}
	style := lipgloss.NewStyle()
	if fg != nil {
		style = style.Foreground(t.convertColour(fg))
	}
	if bg != nil {
		style = style.Background(t.convertColour(bg))
	}
	if bold {
		style = style.Bold(true)
	}
	return style.Render(text)
}

// convertColour downsamples one palette colour, falling back to it unchanged when that cannot fail in
// a way we can see.
func (t uiTheme) convertColour(c color.Color) color.Color {
	if t.convert == nil {
		return c
	}
	if converted := t.convert(c); converted != nil {
		return converted
	}
	return c
}

// mustRGBA parses "#rrggbb" into an opaque colour. A malformed constant is a programming error, so the
// zero colour is better than a panic in a render path.
func mustRGBA(hex string) color.RGBA {
	trimmed := strings.TrimPrefix(hex, "#")
	if len(trimmed) != 6 {
		return color.RGBA{}
	}
	value, err := strconv.ParseUint(trimmed, 16, 32)
	if err != nil {
		return color.RGBA{}
	}
	return color.RGBA{R: uint8(value >> 16), G: uint8(value >> 8), B: uint8(value), A: 0xff}
}

// Symbols, per state, in the two sets. Each ASCII form is one cell so a row's width does not change
// with the glyph policy - except that the ASCII ellipsis is "." rather than "..." for the same reason.
var (
	stateGlyphsUTF8 = map[string]string{
		"idle":     "●",
		"busy":     "◐",
		"needs":    "▲",
		"unknown":  "?",
		"exited":   "○",
		"starting": "…",
	}
	stateGlyphsASCII = map[string]string{
		"idle":     "o",
		"busy":     "*",
		"needs":    "!",
		"unknown":  "?",
		"exited":   "x",
		"starting": ".",
	}
)
