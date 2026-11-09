package tui

// Small components in this package, in the shape the Bubbles libraries use: each one owns a little state,
// has an Update that takes a key press and returns the component plus an optional command, a View that
// renders it, Focus/Blur, and a keyMap with a short and a full help.
//
// They are written here rather than imported because charm.land/bubbles/v2 would need five module
// downloads that nobody has approved (atotto/clipboard, harmonica, fuzzy, heredoc, go-humanize). The
// shape is kept so that swapping in the real components later is mechanical.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// keyMap is one control's help: the short legend the footer shows and the full panel ? opens.
type keyMap struct {
	Short string
	Full  []string
}

// textInput is a single-line field with a visible cursor, optional masking and a validation state.
type textInput struct {
	ID          string
	Label       string
	Help        string
	Placeholder string
	Optional    bool
	Secret      bool // metadata only; secret bytes are held by model.secret, never value

	value  string
	cursor int
	width  int
	focus  bool

	// message is the engine's Validate answer for the current value; "" means acceptable.
	message string
	// valid is true when the field has been validated and has no message.
	valid bool
}

// textInputKeys is the field's own help.
var textInputKeys = keyMap{
	Short: "enter next · ←/→ move · esc back",
	Full: []string{
		"enter: confirm and move to the next control",
		"left/right, home/end: move the cursor",
		"backspace/delete: edit",
		"tab/shift+tab, up/down: leave the field",
		"esc: back",
	},
}

// Focus marks the field as the one accepting keys.
func (t *textInput) Focus() { t.focus = true }

// Blur drops focus.
func (t *textInput) Blur() { t.focus = false }

// Value is the field's text.
func (t textInput) Value() string { return t.value }

// SetValue replaces the text and puts the cursor at the end.
func (t *textInput) SetValue(value string) {
	if t.Secret {
		value = ""
	}
	t.value = value
	t.cursor = len([]rune(value))
	t.message, t.valid = "", false
}

// SetMessage records the engine's validation answer.
func (t *textInput) SetMessage(message string) {
	t.message = message
	t.valid = message == "" && t.value != ""
}

// Update applies one key press. It returns a command only for operations that act on the program; typing
// is pure state.
func (t textInput) Update(msg tea.KeyPressMsg) (textInput, tea.Cmd) {
	if t.Secret {
		// Secret input belongs to model.secret, a wipeable byte buffer. Fail closed if a caller
		// accidentally routes a key through the ordinary string field component.
		return t, nil
	}
	switch msg.Code {
	case tea.KeyLeft:
		if t.cursor > 0 {
			t.cursor--
		}
		return t, nil
	case tea.KeyRight:
		if t.cursor < len([]rune(t.value)) {
			t.cursor++
		}
		return t, nil
	case tea.KeyHome:
		t.cursor = 0
		return t, nil
	case tea.KeyEnd:
		t.cursor = len([]rune(t.value))
		return t, nil
	case tea.KeyBackspace:
		runes := []rune(t.value)
		if t.cursor > 0 && t.cursor <= len(runes) {
			t.value = string(append(runes[:t.cursor-1], runes[t.cursor:]...))
			t.cursor--
			t.valid = false
		}
		return t, nil
	case tea.KeyDelete:
		runes := []rune(t.value)
		if t.cursor < len(runes) {
			t.value = string(append(runes[:t.cursor], runes[t.cursor+1:]...))
			t.valid = false
		}
		return t, nil
	}

	if msg.Text != "" && msg.Mod&(tea.ModAlt|tea.ModCtrl) == 0 {
		runes := []rune(t.value)
		if t.cursor > len(runes) {
			t.cursor = len(runes)
		}
		insert := []rune(msg.Text)
		merged := make([]rune, 0, len(runes)+len(insert))
		merged = append(merged, runes[:t.cursor]...)
		merged = append(merged, insert...)
		merged = append(merged, runes[t.cursor:]...)
		t.value = string(merged)
		t.cursor += len(insert)
		t.valid = false
	}
	return t, nil
}

// View renders one field: marker, label, value or placeholder, then help, then the validation line.
func (t textInput) View(theme theme) string {
	marker := "  "
	if t.focus {
		marker = "> "
	}

	var out strings.Builder
	out.WriteString(marker)
	out.WriteString(terminalText(t.Label))
	out.WriteString(": ")

	switch {
	case t.Secret:
		out.WriteString(theme.dim("use the masked input"))
	case t.focus:
		runes := []rune(t.value)
		cursor := t.cursor
		if cursor > len(runes) {
			cursor = len(runes)
		}
		out.WriteString(terminalText(string(runes[:cursor])))
		out.WriteString(theme.accent("|"))
		out.WriteString(terminalText(string(runes[cursor:])))
	case t.value != "":
		out.WriteString(terminalText(t.value))
	default:
		out.WriteString(theme.dim(terminalText(t.Placeholder)))
	}

	if t.Optional && t.value == "" {
		out.WriteString(theme.dim(" (optional)"))
	}
	out.WriteString("\n")

	if t.Help != "" {
		out.WriteString("    " + theme.dim(terminalText(t.Help)) + "\n")
	}
	switch {
	case t.message != "":
		out.WriteString("    " + theme.err("! "+terminalText(t.message)) + "\n")
	case t.valid:
		out.WriteString("    " + theme.ok("✓ valid") + "\n")
	}
	return out.String()
}

// maskPresence is how a masked value is described when the field is not being typed into.
func maskPresence(bytes int) string {
	return "received, hidden (" + itoa(bytes) + " bytes)"
}

// toggle is a Yes/No consent control. Its initial value is exactly Question.DefaultYes.
type toggle struct {
	ID      string
	Prompt  string
	Detail  []string
	Yes     bool
	focus   bool
	changed bool
}

// toggleKeys is the toggle's help.
var toggleKeys = keyMap{
	Short: "space/←/→ toggle · y=yes n=no · enter next",
	Full: []string{
		"space, left/right: switch between Yes and No",
		"y: yes, n: no",
		"enter: accept this answer and move to the next control",
		"tab/shift+tab, up/down: move focus",
	},
}

// Focus marks the toggle as focused.
func (t *toggle) Focus() { t.focus = true }

// Blur drops focus.
func (t *toggle) Blur() { t.focus = false }

// Update handles the toggle's keys.
func (t toggle) Update(msg tea.KeyPressMsg) (toggle, tea.Cmd) {
	switch {
	case msg.Code == tea.KeySpace || msg.Text == " " || msg.Code == tea.KeyLeft || msg.Code == tea.KeyRight:
		t.Yes = !t.Yes
		t.changed = true
	case msg.Code == 'y' || msg.Code == 'Y':
		t.Yes = true
		t.changed = true
	case msg.Code == 'n' || msg.Code == 'N':
		t.Yes = false
		t.changed = true
	}
	return t, nil
}

// View renders "( ) Yes  (•) No" with the current answer marked.
func (t toggle) View(theme theme, width int) string {
	marker := "  "
	if t.focus {
		marker = "> "
	}
	yes, no := "( ) Yes", "( ) No"
	if t.Yes {
		yes = "(•) Yes"
	} else {
		no = "(•) No"
	}
	line := marker + yes + "  " + no + "  " + terminalText(t.Prompt)
	var out strings.Builder
	for _, piece := range wrapText(line, width) {
		out.WriteString(piece)
		out.WriteString("\n")
	}
	for _, detail := range t.Detail {
		for _, piece := range wrapText(terminalText(detail), width-6) {
			out.WriteString("      " + theme.dim(piece) + "\n")
		}
	}
	return out.String()
}

// choiceList is a vertical list the owner picks from with the arrow keys.
type choiceList struct {
	Options  []string
	Notes    []string
	Selected int
	focus    bool
}

// choiceKeys is the choice list's help.
var choiceKeys = keyMap{
	Short: "↑/↓ choose · enter select",
	Full: []string{
		"up/down: move the selection",
		"enter: open the selected method",
		"tab/shift+tab: move focus",
		"esc: back",
	},
}

// Focus marks the list as focused.
func (c *choiceList) Focus() { c.focus = true }

// Blur drops focus.
func (c *choiceList) Blur() { c.focus = false }

// Update handles the list's keys.
func (c choiceList) Update(msg tea.KeyPressMsg) (choiceList, tea.Cmd) {
	switch msg.Code {
	case tea.KeyUp:
		if c.Selected > 0 {
			c.Selected--
		}
	case tea.KeyDown:
		if c.Selected < len(c.Options)-1 {
			c.Selected++
		}
	}
	return c, nil
}

// View renders the options with a cursor and the selected one marked.
func (c choiceList) View(theme theme, width int) string {
	var out strings.Builder
	for index, option := range c.Options {
		marker := "  "
		bullet := "( ) "
		if index == c.Selected {
			bullet = "(•) "
			if c.focus {
				marker = "> "
			}
		}
		line := marker + bullet + option
		for _, piece := range wrapText(terminalText(line), width) {
			out.WriteString(piece)
			out.WriteString("\n")
		}
		if index < len(c.Notes) && c.Notes[index] != "" {
			for _, piece := range wrapText(terminalText(c.Notes[index]), width-6) {
				out.WriteString("      " + theme.dim(piece) + "\n")
			}
		}
	}
	return out.String()
}

// buttonRow is the visible [ Back ] [ Continue ] (or [ Stay ] [ Quit ]) row.
type buttonRow struct {
	Labels   []string
	Selected int
	focus    bool
	blocked  []string // per button: a reason that disables it, or ""
}

// buttonKeys is the row's help.
var buttonKeys = keyMap{
	Short: "←/→ choose · enter press",
	Full: []string{
		"left/right: move between the buttons",
		"enter: press the highlighted button",
		"tab/shift+tab: move focus",
	},
}

// Focus marks the row as focused.
func (b *buttonRow) Focus() { b.focus = true }

// Blur drops focus.
func (b *buttonRow) Blur() { b.focus = false }

// Update handles the row's keys.
func (b buttonRow) Update(msg tea.KeyPressMsg) (buttonRow, tea.Cmd) {
	switch msg.Code {
	case tea.KeyLeft:
		if b.Selected > 0 {
			b.Selected--
		}
	case tea.KeyRight:
		if b.Selected < len(b.Labels)-1 {
			b.Selected++
		}
	}
	return b, nil
}

// Disabled reports whether the highlighted button is blocked, and why.
func (b buttonRow) Disabled() (string, bool) {
	if b.Selected < 0 || b.Selected >= len(b.blocked) {
		return "", false
	}
	if b.blocked[b.Selected] == "" {
		return "", false
	}
	return b.blocked[b.Selected], true
}

// View renders the row, dimming blocked buttons and showing the reason beside the row.
func (b buttonRow) View(theme theme, width int) string {
	var out strings.Builder
	out.WriteString("  ")
	for index, label := range b.Labels {
		text := "[ " + label + " ]"
		highlighted := index == b.Selected
		switch {
		case index < len(b.blocked) && b.blocked[index] != "":
			text = theme.dim(text)
		case highlighted && b.focus:
			text = theme.accent(text)
		}
		if index > 0 {
			out.WriteString("  ")
		}
		out.WriteString(text)
	}
	out.WriteString("\n")
	// Keep Continue's disabled reason visible even when Back is focused.
	reasons := make([]string, 0, len(b.blocked))
	for index, reason := range b.blocked {
		if reason == "" || index >= len(b.Labels) {
			continue
		}
		reasons = append(reasons, b.Labels[index]+": "+terminalText(reason))
	}
	if len(reasons) > 0 {
		for _, line := range wrapText("  "+strings.Join(reasons, " · "), width) {
			out.WriteString(theme.dim(line) + "\n")
		}
	}
	return out.String()
}

// pane is the scrolling main area: it holds rendered lines and an offset.
type pane struct {
	lines  []string
	offset int
	height int
}

// SetLines replaces the content and keeps the offset inside it.
func (p *pane) SetLines(lines []string) { p.lines = lines; p.clamp() }

// ScrollBy moves the view by delta lines.
func (p *pane) ScrollBy(delta int) { p.offset += delta; p.clamp() }

// Top jumps to the first line.
func (p *pane) Top() { p.offset = 0 }

// Bottom jumps to the last screenful.
func (p *pane) Bottom() { p.offset = len(p.lines) - p.height; p.clamp() }

// EnsureRow scrolls the minimum amount that puts row in view.
func (p *pane) EnsureRow(row int) {
	if p.height <= 0 {
		return
	}
	if row < p.offset {
		p.offset = row
	}
	if row >= p.offset+p.height {
		p.offset = row - p.height + 1
	}
	p.clamp()
}

// View returns the visible window of lines.
func (p pane) View() []string {
	if p.height <= 0 || p.height >= len(p.lines) {
		return p.lines
	}
	end := p.offset + p.height
	if end > len(p.lines) {
		end = len(p.lines)
	}
	if p.offset < 0 {
		return p.lines[:end]
	}
	return p.lines[p.offset:end]
}

// More reports whether content is hidden above or below.
func (p pane) More() (above, below bool) {
	return p.offset > 0, p.offset+p.height < len(p.lines)
}

// clamp keeps the offset inside the content.
func (p *pane) clamp() {
	if p.offset < 0 {
		p.offset = 0
	}
	maxOffset := len(p.lines) - p.height
	if maxOffset < 0 {
		maxOffset = 0
	}
	if p.offset > maxOffset {
		p.offset = maxOffset
	}
}

// dialog is the quit confirmation.
type dialog struct {
	Title   string
	Body    []string
	Details []string
	Buttons buttonRow
	// Hint is the dim line under the buttons. The quit confirmation and an error notice say different
	// things, so it is data rather than a fixed string.
	Hint string
	open bool
}

// dialogKeys is the dialog's help.
var dialogKeys = keyMap{
	Short: "←/→ choose · enter press · esc stay",
	Full: []string{
		"left/right, tab: move between Stay and Quit",
		"enter: press the highlighted button",
		"esc: stay (the default)",
	},
}

// Open shows the dialog with Stay focused.
func (d *dialog) Open() {
	d.open = true
	d.Buttons.Selected = 0
	d.Buttons.Focus()
}

// Close hides it.
func (d *dialog) Close() {
	d.open = false
	d.Buttons.Blur()
}

// Yes reports whether Quit is highlighted.
func (d dialog) QuitSelected() bool { return d.Buttons.Selected == 1 }

// Update handles the dialog's keys.
func (d dialog) Update(msg tea.KeyPressMsg) (dialog, tea.Cmd) {
	if msg.Code == tea.KeyTab {
		if msg.Mod&tea.ModShift != 0 {
			if d.Buttons.Selected > 0 {
				d.Buttons.Selected--
			}
		} else if d.Buttons.Selected < len(d.Buttons.Labels)-1 {
			d.Buttons.Selected++
		}
		return d, nil
	}
	buttons, _ := d.Buttons.Update(msg)
	d.Buttons = buttons
	return d, nil
}

// View renders the dialog box.
func (d dialog) View(theme theme, width int) string {
	var out strings.Builder
	out.WriteString("\n")
	out.WriteString(theme.warn(terminalText(d.Title)))
	out.WriteString("\n\n")
	for _, line := range d.Body {
		for _, piece := range wrapText(terminalText(line), width) {
			out.WriteString(piece)
			out.WriteString("\n")
		}
	}
	if len(d.Details) > 0 {
		out.WriteString("\n" + theme.dim("Done so far:") + "\n")
		for _, detail := range d.Details {
			for _, piece := range wrapText("  - "+terminalText(detail), width) {
				out.WriteString(piece)
				out.WriteString("\n")
			}
		}
	}
	out.WriteString("\n")
	out.WriteString(d.Buttons.View(theme, width))
	out.WriteString("\n")
	hint := d.Hint
	if hint == "" {
		hint = "←/→ or tab: choose · enter: confirm · esc: close"
	}
	out.WriteString(theme.dim(hint))
	return out.String()
}

// renderProgress draws the step progress bar.
func renderProgress(done, total, width int, theme theme) string {
	if total <= 0 {
		return ""
	}
	if width < 1 {
		width = 1
	}
	filled := done * width / total
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return theme.accent(bar)
}

// renderSidebar draws the nine-step list with marks that never rely on colour.
func renderSidebar(steps []setupStepStatus, current int, width int, theme theme) []string {
	var lines []string
	for index, step := range steps {
		mark := "○"
		switch {
		case index == current:
			mark = "●"
		case step.Status == statusDone || step.Status == statusAlready:
			mark = "✓"
		case step.Status == statusSkipped:
			mark = "–"
		case step.Status == statusNotDone || step.Status == statusYourTurn:
			mark = "!"
		}
		marker := "  "
		if index == current {
			marker = "> "
		}
		line := marker + mark + " " + terminalText(step.Title)
		// The word is always shown beside the mark, so no state is carried by colour or by an icon alone.
		if word := statusWord(step.Status); word != "" && index != current {
			line += " (" + word + ")"
		}
		if index == current && statusWord(step.Status) != "" {
			line += " (" + statusWord(step.Status) + ")"
		}
		line = truncate(line, width)
		if index == current {
			line = theme.accent(line)
		}
		lines = append(lines, line)
	}
	return lines
}

// truncate shortens a line to width runes, adding an ellipsis when it cuts.
func truncate(line string, width int) string {
	if width <= 0 {
		return line
	}
	runes := []rune(line)
	if len(runes) <= width {
		return line
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

// terminalText makes terminal controls and directional formatting characters visible before they
// reach the renderer. It is applied only to display copies; raw values remain available to validation.
func terminalText(text string) string {
	var out strings.Builder
	for _, r := range text {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&out, "\\u{%04X}", r)
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}

// wrapText breaks text at word boundaries, hard-wrapping a word that is longer than the width.
func wrapText(text string, width int) []string {
	if width < 20 {
		width = 20
	}
	if text == "" {
		return []string{""}
	}

	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		remaining := paragraph
		for utf8.RuneCountInString(remaining) > width {
			cut := strings.LastIndex(remaining[:minInt(len(remaining), width*4)], " ")
			if cut <= 0 || utf8.RuneCountInString(remaining[:cut]) > width {
				cut = runeCut(remaining, width)
			}
			lines = append(lines, strings.TrimRight(remaining[:cut], " "))
			remaining = strings.TrimLeft(remaining[cut:], " ")
			if remaining == "" {
				break
			}
		}
		lines = append(lines, remaining)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

// runeCut returns the byte offset of the width-th rune, so a cut never splits a character.
func runeCut(text string, width int) int {
	count := 0
	for index := range text {
		if count == width {
			return index
		}
		count++
	}
	return len(text)
}

// minInt is the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt is the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// isWordRune reports whether a rune is part of a word, used only for cursor movement in tests.
func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// padding returns n spaces.
func padding(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat(" ", n)
}

// box draws a bordered area around content.
func box(theme theme, width int, content string) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(0, 1)
	if colour := theme.borderColour(); colour != nil {
		style = style.BorderForeground(colour)
	}
	if width > 4 {
		style = style.Width(width - 2)
	}
	return style.Render(content)
}
