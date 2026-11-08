package tui

// The card status and draft review, as plain lines.
//
// Two functions turn what the daemon says about card.yaml into text a caller can put in a viewport. They
// format and decide nothing: no Model receiver, no IO, no clock, no wrapping and no styling. The caller
// owns the viewport, the keys and the selection.
//
// Two rules shape the text. The listener is its own prominent signal with three real states, and only
// "listening" may mention the readable-by-anyone notice or a URL - off and not-bound must never read like
// an endpoint that is ready. And a draft's confirmation shows exactly what would run and exactly what
// would be on stdin: every suspicious byte is displayed as a visible escape rather than hidden, so the
// owner inspects the real values instead of a cleaned-up version of them.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No view has been rendered.

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// The keys the controller binds, named here so a hint cannot disagree with the binding.
const (
	cardKeyRun      = "y"
	cardKeyAccept   = "a"
	cardKeyDiscard  = "d"
	cardKeyCancel   = "x"
	cardKeyBack     = "Esc"
	cardKeyReload   = "r"
	cardKeyGenerate = "g"
)

// cardDraftExpiryMinutes is how long a prepared or ready draft lives after the owner's last operation.
const cardDraftExpiryMinutes = 15

// cardFileStatusLines renders the card.yaml status block.
//
// The listener comes first and loudest, the file's own validity is a separate line from the card that is
// being served, and the guidance the contract requires is always present.
//
// UNTESTED.
func cardFileStatusLines(view *protocol.CardFileView) []string {
	if view == nil {
		return []string{"card status is unavailable"}
	}

	lines := make([]string, 0, 24)

	// The listener first: whether anything is being served is the fact that changes what the rest means.
	lines = append(lines, cardListenerStatusLines(view.Listener)...)

	lines = append(lines, "card.yaml: "+escapeCardText(view.Path))
	lines = append(lines, "file: "+cardFileStateText(view.File.State))
	if view.File.Line > 0 || view.File.Message != "" {
		line := "file problem"
		if view.File.Line > 0 {
			line = fmt.Sprintf("line %d: ", view.File.Line)
		}
		lines = append(lines, line+escapeCardText(view.File.Message))
	}
	if view.File.ModTime != "" {
		lines = append(lines, "last modified: "+escapeCardText(view.File.ModTime))
	}

	switch {
	case view.Served == nil:
		lines = append(lines, "served: no card is being served")
	case view.Listener.State != protocol.CardListenerListening:
		lines = append(lines, "last good card retained; not being served while the listener is not listening")
	case view.Served.Current:
		lines = append(lines, "served: the current card, since "+escapeCardText(view.Served.Since))
	default:
		lines = append(lines, "served: the last good card from "+escapeCardText(view.Served.Since)+
			"; card.yaml has changed and is not valid")
	}

	lines = append(lines, cardDraftingLine(view.Drafting))
	lines = append(lines, cardSkillsLines(view.Skills)...)

	lines = append(lines,
		"edit card.yaml in your own editor; the daemon re-reads it when it changes",
		"reload now with "+cardKeyReload+"; generate a draft with "+cardKeyGenerate,
		"a running daemon needs a restart to change the card listener; rtk daemon stop ends its sessions",
	)

	return lines
}

// cardListenerLines is the listener block: a mark, a state and, only when listening, where and what it
// means.
//
// UNTESTED.
func cardListenerStatusLines(listener protocol.CardListener) []string {
	lines := make([]string, 0, 4)

	switch listener.State {
	case protocol.CardListenerListening:
		lines = append(lines, "+ listening: "+escapeCardText(listener.Addr))
		if listener.URL != "" {
			lines = append(lines, "url: "+escapeCardText(listener.URL))
		}
		// The one sentence that says what an unauthenticated endpoint means.
		lines = append(lines, escapeCardText(protocol.CardReadableNotice))
	case protocol.CardListenerOff:
		lines = append(lines, "- off: the card endpoint is not serving")
	case protocol.CardListenerNotBound:
		lines = append(lines, "! not bound: "+escapeCardText(listener.Reason))
	default:
		state := escapeCardText(listener.State)
		if state == "" {
			state = "unknown"
		}
		lines = append(lines, "? listener state "+state+" (unknown to this build)")
	}

	return lines
}

// cardFileStateText renders the file's state in words rather than wire spellings.
func cardFileStateText(state string) string {
	switch state {
	case protocol.CardFileOK:
		return "valid"
	case protocol.CardFileNeedsDescription:
		return "needs description"
	case protocol.CardFileInvalid:
		return "invalid"
	case protocol.CardFileMissing:
		return "missing"
	case "":
		return "not reported"
	}
	return escapeCardText(state) + " (unknown to this build)"
}

// cardDraftingLine says whether the daemon can prepare a draft, and which preset it would use.
func cardDraftingLine(info protocol.CardDraftingInfo) string {
	line := "draft: "
	switch {
	case info.Available && info.Preset != "":
		line += "available (preset " + escapeCardText(info.Preset) + ")"
	case info.Available:
		line += "available"
	default:
		line += "not available"
		if info.Message != "" {
			line += ": " + escapeCardText(info.Message)
		}
		return line
	}
	if info.Message != "" {
		line += ": " + escapeCardText(info.Message)
	}
	return line
}

// cardSkillsLines is the evidence-only inventory, with the two limits always stated.
//
// UNTESTED.
func cardSkillsLines(inventory protocol.SkillInventory) []string {
	if len(inventory.Found) == 0 && len(inventory.Notes) == 0 && len(inventory.NotScanned) == 0 {
		return nil
	}

	lines := []string{"skills found on this host (evidence only; not added to the card):"}
	for _, found := range inventory.Found {
		line := escapeCardText(found.Harness) + " " + escapeCardText(found.Scope) + " " + escapeCardText(found.Label)
		if !found.LocationVerified {
			// The location is the scanner's business, not the skill's; say which is which.
			line += " - undocumented location"
		}
		lines = append(lines, line)
	}
	if len(inventory.NotScanned) > 0 {
		lines = append(lines, "not scanned: "+escapeCardText(strings.Join(inventory.NotScanned, ", ")))
	}
	if inventory.Truncated {
		lines = append(lines, "inventory truncated at the scan limit")
	}
	for _, note := range inventory.Notes {
		lines = append(lines, "could not read "+escapeCardText(note.Label)+": "+escapeCardText(note.Reason))
	}

	// Both limitations are stated whenever the inventory is shown: the list can be shorter than the truth,
	// and the second one is the reason.
	lines = append(lines,
		"both harnesses follow symlinked skill folders; this scanner does not, so this list can be shorter",
		"project, plugin and managed scopes are not scanned",
	)
	return lines
}

// cardDraftReviewLines renders one draft: what would run, and what to do about it.
//
// UNTESTED.
func cardDraftReviewLines(draft *protocol.CardDraft) []string {
	if draft == nil {
		return []string{"no draft to review"}
	}

	lines := make([]string, 0, 40)

	switch draft.State {
	case protocol.CardDraftPrepared:
		lines = append(lines, cardPreparedLines(draft)...)
	case protocol.CardDraftRunning:
		lines = append(lines,
			"running: the model is running; cancel with "+cardKeyCancel,
			fmt.Sprintf("the run is bounded at %d s", draft.TimeoutSec),
		)
	case protocol.CardDraftReady:
		lines = append(lines, cardReadyLines(draft)...)
	case protocol.CardDraftFailed:
		lines = append(lines, "failed: "+escapeCardText(draft.Error))
	case protocol.CardDraftCancelled:
		if draft.Error == "expired" {
			lines = append(lines, "cancelled: expired before it was reviewed")
		} else {
			lines = append(lines, "cancelled: "+escapeCardText(draft.Error))
		}
	default:
		state := escapeCardText(draft.State)
		if state == "" {
			state = "no state reported"
		}
		lines = append(lines, "draft: "+state+" (unknown to this build)")
	}

	// Every state carries the two things a person can be surprised by.
	lines = append(lines,
		"closing this window cancels a running draft and discards a prepared or ready one",
		fmt.Sprintf("a draft expires %d minutes after your last operation on it", cardDraftExpiryMinutes),
	)
	return lines
}

// cardPreparedLines is the confirmation screen: exactly what would run, and exactly what it would read.
//
// UNTESTED.
func cardPreparedLines(draft *protocol.CardDraft) []string {
	lines := make([]string, 0, 32)

	lines = append(lines,
		"prepared: nothing has run yet",
		"preset: "+escapeCardText(draft.Preset),
		"binary: "+escapeCardText(draft.Binary),
		"argv (exact, no shell): "+cardArgvText(draft.Argv),
		fmt.Sprintf("payload (%d bytes reported, %d counted):", draft.PayloadBytes, len(draft.Payload)),
	)

	// The payload line by line, so its shape is visible; a trailing newline shows as a final empty line.
	for _, line := range strings.Split(draft.Payload, "\n") {
		lines = append(lines, "| "+escapeCardPayloadLine(line))
	}

	if len(draft.EnvNames) == 0 {
		lines = append(lines, "environment names: none")
	} else {
		names := make([]string, 0, len(draft.EnvNames))
		for _, name := range draft.EnvNames {
			names = append(names, escapeCardText(name))
		}
		lines = append(lines, "environment names: "+strings.Join(names, ", "))
	}

	lines = append(lines,
		fmt.Sprintf("timeout: %d s", draft.TimeoutSec),
		fmt.Sprintf("output cap: %d bytes", draft.OutputCapBytes),
	)
	for _, caveat := range draft.Caveats {
		lines = append(lines, "caveat: "+escapeCardText(caveat))
	}

	lines = append(lines,
		"press "+cardKeyRun+" for Yes, run the model; Enter does nothing, d discards, Esc returns",
		"nothing is written to card.yaml until you Accept a result",
	)
	return lines
}

// cardReadyLines is the review screen: the diff, the candidate size, and what Accept would do.
//
// UNTESTED.
func cardReadyLines(draft *protocol.CardDraft) []string {
	lines := make([]string, 0, 24)

	lines = append(lines, "ready: nothing has been written")
	lines = append(lines, fmt.Sprintf("candidate: %d bytes", draft.CandidateBytes))

	if len(draft.Diff) == 0 {
		lines = append(lines, "diff: no fields changed")
	}
	for _, entry := range draft.Diff {
		lines = append(lines, "diff: "+cardDiffText(entry))
	}

	if draft.BackupNotice != "" {
		lines = append(lines, "backup: "+escapeCardText(draft.BackupNotice))
	}
	if draft.RewriteNotice != "" {
		lines = append(lines, "rewrite: "+escapeCardText(draft.RewriteNotice))
	}

	if draft.Stale {
		lines = append(lines, "stale: card.yaml changed after this draft, so Accept is disabled")
	} else {
		lines = append(lines, "press "+cardKeyAccept+" to write card.yaml, or "+cardKeyDiscard+" to discard it")
	}
	lines = append(lines, "Accept writes card.yaml after copying it to card.yaml.bak; nothing is written without it")
	return lines
}

// cardDiffText renders one changed field with its direction.
func cardDiffText(entry protocol.CardDiffEntry) string {
	path := escapeCardText(entry.Path)
	oldText := escapeCardText(entry.Old)
	newText := escapeCardText(entry.New)

	switch {
	case entry.Old == "" && entry.New != "":
		newText += " (added)"
	case entry.Old != "" && entry.New == "":
		oldText += " (removed)"
	}
	if entry.Generated {
		newText += " (generated)"
	}
	return path + ": " + oldText + " -> " + newText
}

// cardArgvText renders the argv as a JSON array, which keeps argument boundaries unambiguous.
//
// The JSON is produced by the encoder and then escaped only for control characters that would disturb a
// terminal, so its own backslash escapes are not doubled.
//
// UNTESTED.
func cardArgvText(argv []string) string {
	if len(argv) == 0 {
		return "[]"
	}
	encoded, err := json.Marshal(argv)
	if err != nil {
		return "(the argv could not be shown)"
	}
	var builder strings.Builder
	for _, r := range string(encoded) {
		if unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 {
			builder.WriteString(fmt.Sprintf("\\u%04x", r))
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			// A JSON string cannot contain a raw control byte, so this is defence for a future caller
			// rather than a normal path.
			builder.WriteString(fmt.Sprintf("\\u%04x", r))
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

// escapeCardPayloadLine escapes one payload line for display.
//
// A backslash is doubled so a payload that already contains an escape spelling cannot look like one this
// renderer produced; nothing is dropped.
//
// UNTESTED.
func escapeCardPayloadLine(line string) string {
	return escapeCardText(line)
}

// escapeCardText makes one externally provided string safe to display without changing what it says.
//
// Nothing is stripped: control characters, format characters (which include the bidi overrides), the line
// and paragraph separators and invalid UTF-8 all become visible escapes, so the owner sees the real value.
//
// UNTESTED.
func escapeCardText(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))

	data := []byte(text)
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			// An invalid byte is shown rather than dropped.
			builder.WriteString(fmt.Sprintf("\\x%02x", data[0]))
			data = data[1:]
			continue
		}
		builder.WriteString(escapeCardRune(r))
		data = data[size:]
	}
	return builder.String()
}

// escapeCardRune escapes one rune when it cannot be drawn safely, and keeps it otherwise.
func escapeCardRune(r rune) string {
	switch {
	case r == '\\':
		return `\\`
	case r == '\n':
		return `\n`
	case r == '\r':
		return `\r`
	case r == '\t':
		return `\t`
	case r < 0x20 || r == 0x7f:
		return fmt.Sprintf("\\x%02x", r)
	case r >= 0x80 && r <= 0x9f:
		return fmt.Sprintf("\\u%04x", r)
	case r == 0x2028 || r == 0x2029:
		return fmt.Sprintf("\\u%04x", r)
	case unicode.Is(unicode.Cf, r):
		return fmt.Sprintf("\\u%04x", r)
	}
	return string(r)
}
