package tui

// The formatted card sections: plain lines a caller can scroll and style.
//
// Nothing here reads a file, a clock, the environment or the network, and nothing decides what a skill
// or an agent can do. The sections describe what the gateway kept, what the owner declared, and what the
// scanner found - three different claims that the wording keeps apart on purpose.
//
// Every value is cleaned BEFORE it is wrapped, and nothing is styled: the caller owns colour, cropping
// and scrolling.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No card has been rendered.

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Card section indices.
const (
	cardSectionCard = iota
	cardSectionSkills
	cardSectionSecurity
	cardSectionCapabilities
	cardSectionRaw
)

// notScannedList is what a card's inventory says about the scopes this build does not look at.
//
// It is printed whether or not the daemon listed them, because "not scanned" is a fact about this
// feature, not about one scan's notes.
var notScannedList = []string{"project", "plugin", "managed"}

// wrapClean cleans one value and hard-wraps it to the width.
//
// Cleaning first means a control sequence can never become visible text, and hard-wrapping may split an
// unbroken token - a URI, a hash - rather than letting it overflow the width.
//
// UNTESTED.
func wrapClean(text string, width int) []string {
	cleaned := cleanUISpan(text)
	if cleaned == "" {
		return []string{""}
	}
	wrapped := ansi.Hardwrap(cleaned, width, true)
	return strings.Split(wrapped, "\n")
}

// appendWrapped appends one value's wrapped lines, with an optional label.
func appendWrapped(lines []string, label, value string, width int) []string {
	if label != "" {
		value = label + " " + value
	}
	return append(lines, wrapClean(value, width)...)
}

// cardSectionLines renders one section of a host card as plain lines.
//
// card and poll describe the card; local adds this host's inventory. A nil card is normal - a poll can
// have facts without a body, and a host can have no card at all - and always reads as an explicit
// absence rather than a blank. The caller owns scrolling, so every line is returned.
//
// UNTESTED.
func cardSectionLines(card *protocol.HostCard, poll *protocol.GraphCard, local *protocol.CardView, section int, width int) []string {
	if width <= 0 {
		return nil
	}

	switch section {
	case cardSectionCard:
		return cardSectionCardLines(card, poll, width)
	case cardSectionSkills:
		return cardSectionSkillsLines(card, poll, local, width)
	case cardSectionSecurity:
		return cardSectionSecurityLines(card, width)
	case cardSectionCapabilities:
		return cardSectionCapabilitiesLines(card, width)
	case cardSectionRaw:
		return cardSectionRawLines(card, poll, width)
	}

	return wrapClean("section "+itoa(section)+" does not exist", width)
}

// cardSectionCardLines is the card itself: identity, the interface and the poll facts.
func cardSectionCardLines(card *protocol.HostCard, poll *protocol.GraphCard, width int) []string {
	lines := make([]string, 0, 12)

	if card == nil {
		lines = appendWrapped(lines, "", "no card", width)
		if poll == nil {
			lines = appendWrapped(lines, "", "no card has been polled for this host", width)
		}
	} else {
		lines = appendWrapped(lines, "name:", notAvailableForCard(card.Name), width)
		if card.Description != "" {
			lines = appendWrapped(lines, "description:", card.Description, width)
		}
		lines = appendWrapped(lines, "version:", notAvailableForCard(card.Version), width)

		if len(card.SupportedInterfaces) > 0 {
			iface := card.SupportedInterfaces[0]
			// The URL is the card's own, from the card. An empty one means the card was published while
			// the listener was off, which is a fact worth stating rather than an empty field.
			if iface.URL != "" {
				lines = appendWrapped(lines, "url:", iface.URL, width)
			} else {
				lines = appendWrapped(lines, "", "url: not published", width)
			}
			lines = appendWrapped(lines, "binding:", notAvailableForCard(iface.ProtocolBinding), width)
			lines = appendWrapped(lines, "protocol:", notAvailableForCard(iface.ProtocolVersion), width)
		} else {
			lines = appendWrapped(lines, "", "no supported interface is published", width)
		}
	}

	if poll != nil {
		state := poll.State
		if poll.Restored {
			state += " (restored from the database, not yet polled)"
		}
		lines = appendWrapped(lines, "card poll:", state, width)
		if poll.LastResult != "" {
			lines = appendWrapped(lines, "last result:", poll.LastResult, width)
		}
		if poll.Source != "" {
			lines = appendWrapped(lines, "source:", poll.Source, width)
		}
		if poll.Revision != "" {
			lines = appendWrapped(lines, "revision:", poll.Revision, width)
		}
		if poll.LastOKAt != "" {
			lines = appendWrapped(lines, "last good poll:", poll.LastOKAt, width)
		}
		if poll.LastAttemptAt != "" {
			lines = appendWrapped(lines, "last attempt:", poll.LastAttemptAt, width)
		}
		if poll.CardOmitted {
			lines = appendWrapped(lines, "", "the card body was omitted by the gateway (too large)", width)
		}
	}

	return lines
}

// cardSectionSkillsLines is the declared skills, the evidence for them, and this host's inventory.
func cardSectionSkillsLines(card *protocol.HostCard, poll *protocol.GraphCard, local *protocol.CardView, width int) []string {
	lines := make([]string, 0, 24)

	evidence := evidenceBySkill(poll)

	if card == nil || len(card.Skills) == 0 {
		lines = appendWrapped(lines, "", "no skills are declared in this card", width)
	} else {
		lines = appendWrapped(lines, "", "declared by the owner (found is not declared, and neither is allowed):", width)
		for _, skill := range card.Skills {
			lines = appendWrapped(lines, "declared:", skill.Name+" ("+skill.ID+")", width)
			if len(skill.Tags) > 0 {
				lines = appendWrapped(lines, "tags:", strings.Join(skill.Tags, ", "), width)
			}
			if skill.Description != "" {
				lines = appendWrapped(lines, "", skill.Description, width)
			}
			if found, ok := evidence[skill.ID]; ok {
				lines = appendWrapped(lines, "found for", skill.ID+": "+evidenceLine(found), width)
			}
		}
	}

	if local != nil {
		lines = append(lines, sectionDivider(width)...)
		lines = appendWrapped(lines, "", "found on this host (inventory evidence, not a declaration of use):", width)
		lines = appendWrapped(lines, "", "This list can be shorter than what a harness loads: both harnesses follow symlinked skill folders; this scan does not.", width)
		if len(local.Skills.Found) == 0 {
			lines = appendWrapped(lines, "", "no skill files were found", width)
		}
		for _, found := range local.Skills.Found {
			location := "undocumented location"
			if found.LocationVerified {
				location = "location confirmed by documentation"
			}
			declared := "not declared"
			if found.Declared {
				declared = "declared in the working copy"
			}
			lines = appendWrapped(lines, "found:",
				found.Harness+" "+found.Scope+" "+found.Label+" ("+declared+"; "+location+")", width)
		}

		// The scopes this build never looks at are said plainly, whether or not the daemon listed them.
		notScanned := append([]string(nil), local.Skills.NotScanned...)
		for _, known := range notScannedList {
			if !containsFold(notScanned, known) {
				notScanned = append(notScanned, known)
			}
		}
		lines = appendWrapped(lines, "not scanned:", strings.Join(notScanned, ", "), width)
		if local.Skills.Truncated {
			lines = appendWrapped(lines, "", "the inventory was truncated at the scan limit", width)
		}
		for _, note := range local.Skills.Notes {
			lines = appendWrapped(lines, "could not read", note.Label+": "+note.Reason, width)
		}
	}

	return lines
}

// evidenceBySkill is the skill evidence a polled card carries.
//
// Only the one extension this build knows is read, by its exact URI: a card cannot smuggle evidence
// through an extension nobody agreed on.
func evidenceBySkill(poll *protocol.GraphCard) map[string]protocol.SkillEvidence {
	out := make(map[string]protocol.SkillEvidence)
	if poll == nil || poll.Card == nil {
		return out
	}
	for _, extension := range poll.Card.Capabilities.Extensions {
		if extension.URI != protocol.CardExtensionURI {
			continue
		}
		for id, found := range extension.Params.Skills {
			out[id] = found
		}
	}
	return out
}

// evidenceLine renders one piece of skill evidence.
func evidenceLine(found protocol.SkillEvidence) string {
	parts := make([]string, 0, 5)
	if found.Harness != "" {
		parts = append(parts, found.Harness)
	}
	if found.HarnessVersion != "" {
		parts = append(parts, found.HarnessVersion)
	}
	if found.Scope != "" {
		parts = append(parts, found.Scope)
	}
	if found.SHA256 != "" {
		parts = append(parts, found.SHA256)
	}
	if found.ScannedAt != "" {
		parts = append(parts, "scanned "+found.ScannedAt)
	}
	if len(parts) == 0 {
		return "evidence recorded, no details"
	}
	return strings.Join(parts, " ")
}

// cardSectionSecurityLines states what the card publishes about access, and nothing more.
func cardSectionSecurityLines(card *protocol.HostCard, width int) []string {
	if card == nil {
		return wrapClean("no card; no published security metadata to show", width)
	}
	lines := make([]string, 0, 4)
	lines = appendWrapped(lines, "", "read-only: the card is published for reading", width)
	lines = appendWrapped(lines, "", "unauthenticated: reading it needs no credential", width)

	// The transport comes from the card's own URL and from nothing else.
	if card != nil && len(card.SupportedInterfaces) > 0 && card.SupportedInterfaces[0].URL != "" {
		if scheme := cardURLScheme(card.SupportedInterfaces[0].URL); scheme != "" {
			lines = appendWrapped(lines, "transport:", scheme, width)
		}
	}
	lines = appendWrapped(lines, "", "no securitySchemes are published", width)
	return lines
}

// cardURLScheme reads a URL's scheme, or "" when it has none this reader trusts.
func cardURLScheme(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch parsed.Scheme {
	case "http", "https":
		return parsed.Scheme
	}
	return ""
}

// cardSectionCapabilitiesLines describes what the card publishes, never what an agent can do.
func cardSectionCapabilitiesLines(card *protocol.HostCard, width int) []string {
	lines := make([]string, 0, 10)
	if card == nil {
		return appendWrapped(lines, "", "no card", width)
	}

	caps := card.Capabilities
	lines = appendWrapped(lines, "streaming:", boolText(caps.Streaming), width)
	lines = appendWrapped(lines, "pushNotifications:", boolText(caps.PushNotifications), width)
	lines = appendWrapped(lines, "extendedAgentCard:", boolText(caps.ExtendedAgentCard), width)

	for _, extension := range caps.Extensions {
		lines = append(lines, sectionDivider(width)...)
		lines = appendWrapped(lines, "extension:", extension.URI, width)
		lines = appendWrapped(lines, "required:", boolText(extension.Required), width)
		if extension.Description != "" {
			lines = appendWrapped(lines, "", extension.Description, width)
		}
		ids := make([]string, 0, len(extension.Params.Skills))
		for id := range extension.Params.Skills {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			lines = appendWrapped(lines, "evidence for skills:", strings.Join(ids, ", "), width)
		}
	}

	lines = append(lines, sectionDivider(width)...)
	lines = appendWrapped(lines, "", "this describes what the card publishes and what the scanner found; it says nothing about what an agent can do", width)
	return lines
}

// cardSectionRawLines shows the card as the gateway kept it.
//
// The marshalled text is the source of truth: lines are split FIRST, then cleaned, so a JSON escape keeps
// its spelling and the caller sees what was stored, including the last lines of a large card.
func cardSectionRawLines(card *protocol.HostCard, poll *protocol.GraphCard, width int) []string {
	lines := make([]string, 0, 32)

	title := "Raw JSON - local card"
	if poll != nil {
		title = "Raw JSON - what the gateway kept"
	}
	lines = appendWrapped(lines, "", title, width)

	if poll != nil {
		if poll.UnknownFields > 0 {
			lines = appendWrapped(lines, "unknown fields ignored:", itoa(poll.UnknownFields), width)
		}
		if poll.CardOmitted {
			lines = appendWrapped(lines, "", "the card body was omitted by the gateway (too large)", width)
		}
	}

	if card == nil {
		return appendWrapped(lines, "", "no card body", width)
	}

	encoded, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		// Marshalling a card cannot fail in practice; saying so is better than an empty section.
		return appendWrapped(lines, "", "the card could not be shown as JSON", width)
	}

	for _, line := range strings.Split(string(encoded), "\n") {
		lines = appendWrapped(lines, "", line, width)
	}
	return lines
}

// sectionDivider is one blank line, so sections stay separate without any styling.
func sectionDivider(width int) []string {
	return []string{strings.Repeat(" ", minInt(width, 1))}
}

// notAvailableForCard renders a field the card left empty.
func notAvailableForCard(value string) string {
	if strings.TrimSpace(value) == "" {
		return "not available"
	}
	return value
}

// boolText renders a boolean as the word a person reads.
func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

// itoa renders a small integer without pulling strconv in for one call.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	digits := make([]byte, 0, 12)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

// minInt is the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// containsFold reports whether the list holds the value, case folded.
func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}
