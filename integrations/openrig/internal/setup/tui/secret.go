package tui

// Masked input for the two secrets the setup wizard takes: the pasted VM-A block and the pairing code.
//
// Contract 4.1 rule 2 and 3, and the constants in internal/setup/paste.go: the code must never touch a
// file, argv, the environment or a log; it is held in a byte slice, handed to the engine, and zeroed.
// Nothing here renders or exposes the bytes: the view shows only "received". Update never copies a secret
// into a message, a tea.Msg or a command's error.

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// secretInput is a masked, append-only buffer.
//
// The wizard only needs to append, delete the last rune and take the bytes; a full line editor for a
// password is more machinery than the contract asks for, and every extra code path is another place a
// secret could be rendered by accident.
type secretInput struct {
	bytes []byte
	// lines counts line breaks so the view can say how much arrived without showing any of it.
	lines int
	// truncation is set when more was pasted than the buffer accepts; the wizard then refuses to submit
	// rather than send a silently shortened secret.
	truncated bool
	// pasted marks content that arrived in one bracketed paste rather than being typed. The paste box
	// uses it to decide what Enter means: a pasted block can be read at once even without its last line,
	// typed text should keep being typed. It says nothing about the content.
	pasted bool
}

// take transfers the bytes and drops the wizard's reference. The engine owns and zeroes the returned slice.
func (s *secretInput) take() []byte {
	if s == nil || len(s.bytes) == 0 {
		return nil
	}
	bytes := s.bytes
	s.bytes, s.lines, s.truncated, s.pasted = nil, 0, false, false
	return bytes
}

// copyBytes returns a working copy for an engine call while keeping the masked buffer available for a
// retry. The caller must ensure the receiving operation zeroes its copy.
func (s *secretInput) copyBytes() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.bytes...)
}

// wipe zeroes whatever is still buffered. Safe on nil and safe to call twice.
func (s *secretInput) wipe() {
	if s == nil {
		return
	}
	wipeBytes(s.bytes)
	s.bytes, s.lines, s.truncated, s.pasted = nil, 0, false, false
}

// lineCount is how many lines the buffer holds, for the feedback line; 0 when it is empty.
func (s *secretInput) lineCount() int {
	if s == nil || len(s.bytes) == 0 {
		return 0
	}
	return s.lines + 1
}

// empty reports whether nothing was entered.
func (s *secretInput) empty() bool { return s == nil || len(s.bytes) == 0 }

// appendText adds pasted or typed content, up to limit bytes, and never splits a UTF-8 rune.
//
// Line endings are normalised AS the bytes are appended: CRLF becomes one LF and a lone CR becomes one LF.
// A terminal sends line breaks inside a bracketed paste as CR (xterm, Terminal, iTerm2, and ultraviolet
// forwards them raw), so without this the buffer would hold one long line and the end marker could never
// start a line.
//
// The normalisation writes straight into the buffer - there is no second copy of the content, which matters
// for a secret - and the limit applies to the bytes actually stored.
func (s *secretInput) appendText(text string, limit int) {
	if s == nil || text == "" {
		return
	}
	if len(s.bytes) >= limit {
		s.truncated = true
		return
	}

	for index := 0; index < len(text); index++ {
		// One incoming unit: a CRLF pair collapses to one LF, a lone CR becomes an LF, everything else is
		// copied byte for byte, so multi-byte runes pass through untouched.
		b := text[index]
		if b == '\r' {
			if index+1 < len(text) && text[index+1] == '\n' {
				index++
			}
			b = '\n'
		}

		if len(s.bytes)+1 > limit {
			s.truncated = true
			break
		}
		if b == '\n' {
			s.lines++
		}
		s.bytes = append(s.bytes, b)
	}

	// A truncated append can end inside a multi-byte rune; drop the partial tail so the buffer stays valid
	// UTF-8 for the engine and for line counting.
	if s.truncated {
		for len(s.bytes) > 0 && !utf8.Valid(s.bytes) {
			s.bytes = s.bytes[:len(s.bytes)-1]
		}
	}
}

// backspace removes the last rune.
func (s *secretInput) backspace() {
	if s == nil || len(s.bytes) == 0 {
		return
	}
	last := len(s.bytes) - 1
	if s.bytes[last] == '\n' {
		s.lines--
	}
	_, size := utf8.DecodeLastRune(s.bytes)
	s.bytes = s.bytes[:len(s.bytes)-size]
	if len(s.bytes) == 0 {
		// Deleting the entire pasted block leaves subsequent typing as typed input, not as a stale
		// bracketed-paste arrival.
		s.pasted = false
		s.lines = 0
	}
}

// contains reports whether the buffered text carries a marker line. It exists so the wizard can stop
// collecting the moment the printed block's last line arrives, without ever rendering the content.
func (s *secretInput) contains(marker string) bool {
	if s == nil || len(s.bytes) == 0 || marker == "" {
		return false
	}
	// The marker is a line prefix, not an arbitrary substring. Keep the check on bytes so the secret
	// never gets copied into an unwipeable string, and treat CR, LF and CRLF as line breaks: content that
	// never passed through appendText (an older buffer, or a caller that appended raw bytes) is still
	// recognised.
	want := []byte(marker)
	start := 0
	for index := 0; index < len(s.bytes); index++ {
		if s.bytes[index] != '\n' && s.bytes[index] != '\r' {
			continue
		}
		if bytes.HasPrefix(s.bytes[start:index], want) {
			return true
		}
		// A CRLF is one break: skip its LF so the next line does not start with an empty one.
		if s.bytes[index] == '\r' && index+1 < len(s.bytes) && s.bytes[index+1] == '\n' {
			index++
		}
		start = index + 1
	}
	return bytes.HasPrefix(s.bytes[start:], want)
}

// size is the byte count, which is safe to show: it says how much arrived, not what it was.
func (s *secretInput) size() int {
	if s == nil {
		return 0
	}
	return len(s.bytes)
}

// describe renders the only thing the view may say about a secret.
func (s *secretInput) describe(what string) string {
	if s == nil || len(s.bytes) == 0 {
		return what + " not entered yet"
	}
	var builder strings.Builder
	builder.WriteString(what)
	builder.WriteString(": received, hidden (")
	builder.WriteString(itoa(len(s.bytes)))
	builder.WriteString(" bytes")
	if s.lines > 0 {
		builder.WriteString(", ")
		builder.WriteString(itoa(s.lines + 1))
		builder.WriteString(" lines")
	}
	builder.WriteString(")")
	if s.truncated {
		builder.WriteString(" - TOO LONG: nothing will be submitted until you clear it")
	}
	return builder.String()
}

// wipeBytes overwrites a slice in place.
//
// A Go string cannot be wiped, which is why every secret here stays a []byte until the engine takes it.
func wipeBytes(bytes []byte) {
	for index := range bytes {
		bytes[index] = 0
	}
}

// itoa is a small decimal conversion, so this file needs nothing beyond strings and utf8.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		position--
		digits[position] = '-'
	}
	return string(digits[position:])
}
