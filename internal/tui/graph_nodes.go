package tui

// The graph's small pieces of text: one host's card mark, its node lines, and the legend.
//
// They are pure and plain. The caller owns the graph's geometry, cropping and styling; these functions
// only say what the mark reads and which words the legend uses, so the vocabulary about cards,
// connections and message counts stays in one place and cannot drift between the drawing code and the
// legend that explains it.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No node or mark has been drawn.

import (
	"strings"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// graphCardMark is the card state as a symbol and a word.
//
// The mark is about the card endpoint only - never about the connection, the host or a session - and the
// word is always there, so colour and glyph are never the only signal. An unknown state keeps its literal
// and says this build does not know it; nothing here ever reads as idle, active or alive.
//
// UNTESTED.
func graphCardMark(card protocol.GraphCard, utf8 bool) string {
	var mark, word string

	switch card.State {
	case protocol.CardFresh:
		mark, word = "●", "fresh"
		if !utf8 {
			mark = "o"
		}
	case protocol.CardStale:
		mark, word = "…", "stale"
		if !utf8 {
			mark = "."
		}
	case protocol.CardOffline:
		mark, word = "○", "offline"
		if !utf8 {
			mark = "o"
		}
	case protocol.CardNone:
		mark, word = "?", "no card"
	case protocol.CardRejected:
		mark, word = "✗", "rejected"
		if !utf8 {
			mark = "X"
		}
	default:
		literal := cleanUISpan(card.State)
		if literal == "" {
			literal = "unknown"
		}
		word = literal + " (unknown to this build)"
	}

	text := strings.TrimSpace(mark + " " + word)

	if card.Restored {
		// The row is a record of what the gateway last saw, not proof of anything now.
		text += " from the database, not yet polled"
	}

	if card.State == protocol.CardRejected {
		// The newest answer was refused; the last good card is kept, which is not a claim that the last
		// card was accepted now.
		text += "; the newest answer was refused, the last good card is kept if available"
	}

	return text
}

// graphNodeText is one host node's three plain lines: who it is, its card mark, and where that came from.
//
// The card line carries the CARD's own source and the caller's age string; the connection's source,
// heartbeat and reports are a different signal and are shown separately by the caller. Nothing here is
// cropped or coloured, and nothing merges the two sources into one line.
//
// UNTESTED.
func graphNodeText(host protocol.GraphHost, age string, utf8 bool) []string {
	lines := make([]string, 0, 3)

	lines = append(lines, cleanUISpan(host.Name))
	lines = append(lines, graphCardMark(host.Card, utf8))

	source := "source: " + cleanUISpan(host.Card.Source)
	if trimmed := strings.TrimSpace(age); trimmed != "" {
		source += "  " + cleanUISpan(trimmed)
	}
	lines = append(lines, source)

	return lines
}

// graphLegendLines is the legend that explains a graph's marks and counts.
//
// It is deliberately short and says only what the data is: the card mark is one signal, the connection is
// another, and the message counts are what the gateway decided rather than what anyone read.
//
// UNTESTED.
func graphLegendLines() []string {
	return []string{
		"card mark: the card poll only; it says nothing about the connection",
		"connection: the gateway stream and heartbeat, shown separately",
		"messages_seen: sends the gateway decided (includes failures)",
		"host_stored: the subset that reached a host",
		"source: edges gateway-ledger, poll spokes gateway-poll",
	}
}
