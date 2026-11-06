package gateway

// The routing rules as pure functions: address parsing, report ordering and reason
// cleaning. Keeping them here means the handlers can be read as steps rather than
// as string handling.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// messageIDPattern is the message id a runtime may send. It is deliberately
	// narrow: an id is a key, not prose.
	messageIDPattern = `^[A-Za-z0-9-]{1,64}$`

	// reasonMaxRunes bounds every reason string that arrives from a runtime. It is
	// the only field bound needed: a reason is stored and shown, never replayed.
	reasonMaxRunes = 200
)

// messageIDPatternMatcher is the compiled id rule.
var messageIDPatternMatcher = regexp.MustCompile(messageIDPattern)

// parseAddress splits a network address into its local name and host.
//
// Exactly one '@' is required, both halves must match their own pattern, and a
// bare name is not an address: the runtime is what composes name@host, so the
// gateway never has to guess which host a bare name means.
//
// UNTESTED.
func parseAddress(address string) (name, host string, ok bool) {
	at := strings.IndexByte(address, '@')
	if at < 0 || at != strings.LastIndexByte(address, '@') {
		return "", "", false
	}

	name, host = address[:at], address[at+1:]
	if !agentNamePattern.MatchString(name) || !hostNamePattern.MatchString(host) {
		return "", "", false
	}
	return name, host, true
}

// reportRank orders the recipient's reports so an out-of-order one cannot undo a
// later state. Zero means "not a report at all".
//
// The three final states share rank 6: nothing in the contract orders failed above
// interrupted or accepted, and a report that reaches the end has stopped moving.
//
// UNTESTED.
func reportRank(state protocol.MessageState) int {
	switch state {
	case protocol.MessageQueued:
		return 1
	case protocol.MessageHeld:
		return 2
	case protocol.MessageDelivering:
		return 3
	case protocol.MessagePasted:
		return 4
	case protocol.MessageUnconfirmed:
		return 5
	case protocol.MessageAccepted, protocol.MessageFailed, protocol.MessageInterrupted:
		return 6
	default:
		return 0
	}
}

// cleanReason makes a runtime's reason safe to store and show: control characters
// (newlines and tabs included) become spaces, whitespace runs collapse, and the
// result is cut to reasonMaxRunes runes without splitting a character.
//
// UNTESTED.
func cleanReason(reason string) string {
	var builder strings.Builder
	builder.Grow(len(reason))

	space := false
	for _, r := range reason {
		if r == 0x7f || r < 0x20 || (r >= 0x80 && r <= 0x9f) {
			r = ' '
		}
		if r == ' ' {
			if builder.Len() == 0 || space {
				continue
			}
			space = true
			builder.WriteRune(r)
			continue
		}
		space = false
		builder.WriteRune(r)
	}

	trimmed := strings.TrimSpace(builder.String())
	if utf8.RuneCountInString(trimmed) <= reasonMaxRunes {
		return trimmed
	}

	count := 0
	for index := range trimmed {
		if count == reasonMaxRunes {
			return strings.TrimSpace(trimmed[:index])
		}
		count++
	}
	return trimmed
}
