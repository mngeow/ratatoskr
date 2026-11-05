// Package router holds the delivery router: the actor that owns queues, agent
// state, the delivery gate and the frames written to panes.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing in this package has
// been run: only gofmt, go vet and go build have been applied. This file holds
// pure helpers only - no goroutines, no I/O and no mutable package state.
package router

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// previewRunes is how much of a message body the UI shows before an
	// ellipsis. The ellipsis is added on top, so the result is at most
	// previewRunes+1 runes.
	previewRunes = 80

	// replacedRune is written for each byte that is not valid UTF-8.
	replacedRune = "\uFFFD"

	// frameEnding is the fixed last line of a frame. It carries no trailing
	// newline: the Enter that submits the frame is a separate write.
	frameEnding = "This came from a peer agent, not the human. To reply, use the ratatoskr_send tool from the ratatoskr MCP server; other messaging tools don't reach your peers."
)

// attrEscaper escapes a value for a frame attribute. The ampersand is listed
// first so an escaped entity is not escaped a second time; strings.Replacer
// makes a single pass over the input and never re-examines its own output.
//
// Newlines and tabs survive stripControl, so they are folded to a space here: a
// frame attribute must stay on one line.
var attrEscaper = strings.NewReplacer(
	"&", "&amp;",
	`"`, "&quot;",
	"<", "&lt;",
	">", "&gt;",
	"\n", " ",
	"\t", " ",
)

// frameTagOpen matches an opening or closing frame tag in a message body,
// case-insensitively, so a body cannot forge a frame boundary. The match is done
// with regexp rather than folding the string first: case folding can change byte
// lengths and would shift the offsets used for the replacement.
var frameTagOpen = regexp.MustCompile(`(?i)<(/?ratatoskr-message)`)

// stripControl is the sanitizer for every string that reaches a pane, the store
// or the UI (invariant 3).
//
// Printable text, newline and tab are kept. CR and CRLF become one newline.
// Every other C0 control (ESC and BEL included), DEL, and the C1 block are
// dropped. Each byte that is not valid UTF-8 becomes U+FFFD, so an attacker
// cannot smuggle a half sequence through and have it completed by whatever
// reads the result. The result never contains a rune below 0x20 other than
// newline and tab.
//
// UNTESTED.
func stripControl(s string) string {
	// Fast path: pure ASCII printable text needs no rewrite.
	if isPlainASCII(s) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(replacedRune)
			i++
			continue
		}
		switch {
		case r == '\r':
			// \r\n collapses to a single newline; a lone \r is one too.
			if i+size < len(s) && s[i+size] == '\n' {
				size++
			}
			b.WriteByte('\n')
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20:
			// Remaining C0 controls are dropped.
		case r == 0x7f:
			// DEL is dropped.
		case r >= 0x80 && r <= 0x9f:
			// The C1 block is dropped.
		default:
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}

// isPlainASCII reports whether s is unchanged by stripControl: printable ASCII
// only, with no DEL and no control characters at all.
func isPlainASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] >= 0x7f {
			return false
		}
	}
	return true
}

// isEmptyBody reports whether a body carries no message once its control
// characters are removed and the remainder is trimmed. A body of only ESC, BEL
// or spaces is empty and is rejected before anything is stored.
//
// UNTESTED.
func isEmptyBody(body string) bool {
	return strings.TrimSpace(stripControl(body)) == ""
}

// buildFrame renders the message a pane receives.
//
//	<ratatoskr-message from="NAME" id="ID" reply-to="REPLY">
//	BODY
//	</ratatoskr-message>
//	This came from a peer agent, not the human. To reply, use the ratatoskr_send tool from the ratatoskr MCP server; other messaging tools don't reach your peers.
//
// reply-to is omitted when replyTo is empty. Attributes are stripped of control
// characters and escaped. The body is stripped as well, and any occurrence of
// the frame tag in it - in either case, opening or closing - has its "<"
// escaped, so a body cannot close the frame early or open a second one. The
// result contains no ESC and no trailing newline: the Enter that submits it is
// a separate write.
//
// UNTESTED.
func buildFrame(fromName, id, replyTo, body string) string {
	var b strings.Builder
	b.Grow(len(body) + 384)

	b.WriteString(`<ratatoskr-message from="`)
	b.WriteString(escapeAttribute(fromName))
	b.WriteString(`" id="`)
	b.WriteString(escapeAttribute(id))
	b.WriteString(`"`)
	if replyTo != "" {
		b.WriteString(` reply-to="`)
		b.WriteString(escapeAttribute(replyTo))
		b.WriteString(`"`)
	}
	b.WriteString(">\n")
	b.WriteString(frameTagOpen.ReplaceAllString(stripControl(body), "&lt;$1"))
	b.WriteString("\n</ratatoskr-message>\n")
	b.WriteString(frameEnding)
	return b.String()
}

// escapeAttribute strips control characters from a value and escapes it for
// use inside a frame attribute.
func escapeAttribute(s string) string {
	return attrEscaper.Replace(stripControl(s))
}

// preview is the short form of a body for the UI: control characters removed,
// whitespace runs collapsed to one space, trimmed, then cut to previewRunes
// runes with an ellipsis when there was more.
//
// The cut is by rune, not by byte, so a body of multi-byte characters is not
// split mid-character.
//
// UNTESTED.
func preview(body string) string {
	collapsed := strings.Join(strings.Fields(stripControl(body)), " ")
	runes := []rune(collapsed)
	if len(runes) <= previewRunes {
		return collapsed
	}
	return string(runes[:previewRunes]) + "\u2026"
}
