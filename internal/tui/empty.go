package tui

// A daemon window with no sessions.
//
// The daemon may attach a window with nothing running: every session may have ended, or the user
// opened a window to watch a machine with none. That window is still useful - it shows the Network
// tab, the tab strip, the log and the detach keys - so it is a normal model with zero panes rather
// than an error. What it must never do is pretend: no phantom pane, no focus, no cursor, no input and
// no control request, and one honest block of text in the body.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No empty window has been rendered.

import "strings"

// emptyMessages are the body of a session-less Terminals view, in order.
//
// They say the three things a user needs: that there is nothing here, how to start something, and that
// anything started later will not appear in this window.
var emptyMessages = [3]string{
	"No sessions in this window.",
	"Start agents: rtk attach --agents claude,codex",
	"Sessions started later need a reattach.",
}

// emptyWindowHint is what a key that needs a session says in an empty window.
const emptyWindowHint = "no sessions in this window"

// emptyDaemon reports whether this window is a daemon window with no sessions.
//
// It is the one predicate every guard uses - input, focus, the renderer, the cursor - so no two call
// sites can disagree about what "empty" means.
//
// UNTESTED.
func (m *Model) emptyDaemon() bool {
	return m.daemon != nil && m.activeCount() == 0
}

// emptyTerminalsLines renders the session-less Terminals body.
//
// The lines are left aligned and clipped to the width, and only the lines that fit are returned, so a
// small window still says something true.
//
// UNTESTED.
func (m *Model) emptyTerminalsLines(width, rows int) []string {
	if rows < 1 || width < 1 {
		return nil
	}

	lines := make([]string, 0, rows)
	for index, message := range emptyMessages {
		if len(lines) >= rows {
			break
		}
		plain := fitPlain(message, width)
		// The command is the one actionable line, so it gets the accent; the rest is muted. Colour is
		// decoration on top of the words, never the message itself.
		colour := colDim
		if index == 1 {
			colour = colAccent
		}
		lines = append(lines, m.theme.style(plain, colour, colTermBG, false))
	}
	for len(lines) < rows {
		lines = append(lines, m.theme.style(strings.Repeat(" ", width), colText, colTermBG, false))
	}
	return lines
}
