package tui

// Masked input for the two secrets the setup wizard takes: the pasted VM-A block and the pairing code.
//
// Contract 4.1 rule 2 and 3, and the constants in internal/setup/paste.go: the code must never touch a
// file, argv, the environment or a log; it is held in a byte slice, handed to the engine, and zeroed.
// Nothing here renders or exposes the bytes: the view shows only "received". Update never copies a secret
// into a message, a tea.Msg or a command's error.

import (
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
	// lines counts newlines so the view can say how much arrived without showing any of it.
	lines int
	// truncation is set when more was pasted than the buffer accepts; the wizard then refuses to submit
	// rather than send a silently shortened secret.
	truncated bool
}

// take transfers the bytes and drops the wizard's reference. The engine owns and zeroes the returned slice.
func (s *secretInput) take() []byte {
	if s == nil || len(s.bytes) == 0 {
		return nil
	}
	bytes := s.bytes
	s.bytes, s.lines, s.truncated = nil, 0, false
	return bytes
}

// wipe zeroes whatever is still buffered. Safe on nil and safe to call twice.
func (s *secretInput) wipe() {
	if s == nil {
		return
	}
	wipeBytes(s.bytes)
	s.bytes, s.lines, s.truncated = nil, 0, false
}

// empty reports whether nothing was entered.
func (s *secretInput) empty() bool { return s == nil || len(s.bytes) == 0 }

// appendText adds pasted or typed content, up to limit bytes, and never splits a UTF-8 rune.
func (s *secretInput) appendText(text string, limit int) {
	if s == nil || text == "" {
		return
	}
	remaining := limit - len(s.bytes)
	if remaining <= 0 {
		s.truncated = true
		return
	}
	if len(text) > remaining {
		text = text[:remaining]
		for len(text) > 0 && !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
		s.truncated = true
	}
	for _, r := range text {
		if r == '\n' {
			s.lines++
		}
	}
	s.bytes = append(s.bytes, text...)
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
