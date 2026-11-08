package card

// Validation, the ETag and the public-content check.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var (
	// skillIDPattern is a declared skill's id.
	skillIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// versionPattern is the published revision number as a string.
	versionPattern = regexp.MustCompile(`^[0-9]{1,9}$`)
	// sha256Pattern is a lowercase hex SHA-256.
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Evidence field bounds.
const (
	maxEvidenceHarness = 16
	maxEvidenceVersion = 64
	maxEvidenceScope   = 16
)

// Validate checks a card against every rule and returns all the problems, in field order.
//
// served says the card must carry its own URL (a card as served or as polled). A card as stored and
// edited may leave the URL empty, because it depends on the listener, which may be off.
//
// UNTESTED.
func Validate(card protocol.HostCard, served bool) []protocol.CardProblem {
	var problems []protocol.CardProblem
	add := func(field, message string) {
		problems = append(problems, protocol.CardProblem{Field: field, Message: message})
	}

	text := func(field, value string, limit int, required bool) {
		switch {
		case value == "" && required:
			add(field, "this field is required")
		case !validText(value):
			add(field, "this field has a control character or invalid text")
		case utf8.RuneCountInString(value) > limit:
			add(field, fmt.Sprintf("this field is longer than %d characters", limit))
		}
	}

	text("name", card.Name, protocol.MaxCardName, true)
	// The description is required (A2A's proto marks it so). A working copy may be saved without one,
	// with this problem listed; publishing is what refuses it.
	if card.Description == "" {
		add("description", "a description is required: say in a sentence what this host is for")
	} else {
		text("description", card.Description, protocol.MaxCardDescription, false)
	}

	switch {
	case len(card.SupportedInterfaces) != 1:
		add("supportedInterfaces", "exactly one interface is required")
	default:
		iface := card.SupportedInterfaces[0]
		if iface.ProtocolBinding != protocol.CardProtocolBinding {
			add("supportedInterfaces[].protocolBinding", "this is not the binding this build understands")
		}
		if iface.ProtocolVersion != protocol.CardProtocolVersion {
			add("supportedInterfaces[].protocolVersion", "this is not the protocol version this build understands")
		}
		switch {
		case iface.URL == "" && served:
			add("supportedInterfaces[].url", "a served card needs its own URL")
		case iface.URL != "" && !ValidURL(iface.URL):
			add("supportedInterfaces[].url", "the URL must be http or https, an IP address with a port, and the well-known card path")
		}
	}

	if !versionPattern.MatchString(card.Version) {
		add("version", "the version must be a revision number")
	}

	if card.Capabilities.Streaming || card.Capabilities.PushNotifications || card.Capabilities.ExtendedAgentCard {
		add("capabilities", "this build does not support streaming, push notifications or an extended card")
	}

	if !sameModes(card.DefaultInputModes) {
		add("defaultInputModes", "only text/plain is supported")
	}
	if !sameModes(card.DefaultOutputModes) {
		add("defaultOutputModes", "only text/plain is supported")
	}

	declared := make(map[string]struct{}, len(card.Skills))
	if len(card.Skills) > protocol.MaxCardSkills {
		add("skills", fmt.Sprintf("a card declares at most %d skills", protocol.MaxCardSkills))
	}
	for index, skill := range card.Skills {
		if index >= protocol.MaxCardSkills {
			break
		}
		field := fmt.Sprintf("skills[%d]", index)
		switch {
		case !skillIDPattern.MatchString(skill.ID):
			add(field+".id", "the id must be lowercase letters, digits, dot, dash or underscore, up to 64 characters")
		default:
			if _, duplicate := declared[skill.ID]; duplicate {
				add(field+".id", "this id is declared twice")
			}
			declared[skill.ID] = struct{}{}
		}
		text(field+".name", skill.Name, protocol.MaxCardSkillName, true)
		// A declared skill's description and at least one tag are required, like the card's own.
		if skill.Description == "" {
			add(field+".description", "a description is required for each declared skill")
		} else {
			text(field+".description", skill.Description, protocol.MaxCardSkillDescription, false)
		}
		if len(skill.Tags) == 0 {
			add(field+".tags", "at least one tag is required for each declared skill")
		}
		if len(skill.Tags) > protocol.MaxCardSkillTags {
			add(field+".tags", fmt.Sprintf("a skill has at most %d tags", protocol.MaxCardSkillTags))
		}
		for tagIndex, tag := range skill.Tags {
			if tagIndex >= protocol.MaxCardSkillTags {
				break
			}
			text(fmt.Sprintf("%s.tags[%d]", field, tagIndex), tag, protocol.MaxCardSkillTag, true)
		}
	}

	problems = append(problems, validateExtension(card.Capabilities.Extensions, declared)...)
	return problems
}

// validateExtension checks the one extension and its evidence.
func validateExtension(extensions []protocol.CardExtension, declared map[string]struct{}) []protocol.CardProblem {
	var problems []protocol.CardProblem
	add := func(field, message string) {
		problems = append(problems, protocol.CardProblem{Field: field, Message: message})
	}

	if len(extensions) != 1 {
		add("capabilities.extensions", "exactly one extension is required")
		return problems
	}
	extension := extensions[0]
	if extension.URI != protocol.CardExtensionURI {
		add("capabilities.extensions[].uri", "this is not the extension this build understands")
	}
	if extension.Required {
		add("capabilities.extensions[].required", "the extension cannot be required")
	}
	if extension.Description != "" && (!validText(extension.Description) || utf8.RuneCountInString(extension.Description) > protocol.MaxCardDescription) {
		add("capabilities.extensions[].description", "this field has a control character or is too long")
	}

	for id, evidence := range extension.Params.Skills {
		field := "capabilities.extensions[].params.skills{}"
		if _, ok := declared[id]; !ok {
			add(field, "evidence names a skill the card does not declare")
			continue
		}
		bounded := []struct {
			value string
			limit int
			name  string
		}{
			{evidence.Harness, maxEvidenceHarness, "harness"},
			{evidence.HarnessVersion, maxEvidenceVersion, "harnessVersion"},
			{evidence.Scope, maxEvidenceScope, "scope"},
		}
		for _, item := range bounded {
			if !validText(item.value) || utf8.RuneCountInString(item.value) > item.limit {
				add(field+"."+item.name, "this field has a control character or is too long")
			}
		}
		if evidence.SHA256 != "" && !sha256Pattern.MatchString(evidence.SHA256) {
			add(field+".sha256", "this is not a lowercase hex SHA-256")
		}
		if evidence.ScannedAt != "" {
			if _, ok := protocol.ParseTime(evidence.ScannedAt); !ok {
				add(field+".scannedAt", "this is not a time")
			}
		}
	}
	return problems
}

// sameModes reports whether a mode list is exactly ["text/plain"].
func sameModes(modes []string) bool {
	return len(modes) == 1 && modes[0] == "text/plain"
}

// validText reports whether a string is valid UTF-8 with no control characters.
func validText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if isControl(r) {
			return false
		}
	}
	return true
}

// isControl reports whether a rune would disturb a terminal line.
func isControl(r rune) bool {
	if r == 0x2028 || r == 0x2029 {
		return true
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// ValidURL reports whether a string is a card URL of exactly the accepted shape: http or https, an IP
// literal with a port, no userinfo, query or fragment, and the well-known card path.
//
// The URL is text from the card's author that a window offers to copy, so the shape is narrow on
// purpose. This is a shape check only: it does not check that the URL is reachable, that it belongs to
// the host that served the card, or that it equals the address the gateway polled (a port forward
// would legitimately differ). Nothing treats it as a destination; it is text.
//
// UNTESTED.
func ValidURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return false
	}
	if parsed.Path != protocol.CardPath {
		return false
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || address.Zone() != "" || address.Is4In6() {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	// Reject any spelling that is not the plain form (odd escapes, mixed case scheme, a trailing dot).
	return value == parsed.Scheme+"://"+parsed.Host+parsed.Path
}

// ETag is the quoted validator for canonical card bytes: a strong tag that changes exactly when the
// bytes do. It carries no timestamp.
//
// UNTESTED.
func ETag(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

// Hash is the full lowercase hex SHA-256 of canonical card bytes.
//
// UNTESTED.
func Hash(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// WithURL returns a copy of the card with its own URL filled in, for serving. The caller's card and
// its slices are not modified.
//
// UNTESTED.
func WithURL(card protocol.HostCard, cardURL string) protocol.HostCard {
	interfaces := make([]protocol.CardInterface, len(card.SupportedInterfaces))
	copy(interfaces, card.SupportedInterfaces)
	if len(interfaces) == 1 {
		interfaces[0].URL = cardURL
	}
	card.SupportedInterfaces = interfaces
	return card
}

// Public-content check.
//
// A card is public, and a card is edited by hand and filled partly from a scan. This check refuses the
// two things most likely to leak by accident: a secret-looking string and an absolute path. It is a
// block-list of obvious shapes, not a guarantee: the owner is still the one who decides what to
// publish, and the docs say so. It looks at text the owner or a scan can influence (name,
// description, skill text and tags) and not at the SHA-256 evidence, which is 64 hex characters by
// design.

// tokenPrefixes are the starts of well-known credential formats.
var tokenPrefixes = []string{
	"sk-", "sk_", "ghp_", "gho_", "ghu_", "ghs_", "github_pat_", "xoxb-", "xoxp-", "xoxa-", "xapp-",
	"AKIA", "ASIA", "AIza", "glpat-", "npm_", "pypi-", "eyJ",
}

// tokenShape is a long unbroken run of base64-ish characters.
var tokenShape = regexp.MustCompile(`^[A-Za-z0-9+/_=-]{32,}$`)

// CheckPublic returns the public-content problems in a card.
//
// UNTESTED.
func CheckPublic(card protocol.HostCard) []protocol.CardProblem {
	var problems []protocol.CardProblem
	check := func(field, value string) {
		if reason := publicProblem(value); reason != "" {
			problems = append(problems, protocol.CardProblem{Field: field, Message: reason})
		}
	}

	check("name", card.Name)
	check("description", card.Description)
	for index, skill := range card.Skills {
		field := fmt.Sprintf("skills[%d]", index)
		check(field+".id", skill.ID)
		check(field+".name", skill.Name)
		check(field+".description", skill.Description)
		for tagIndex, tag := range skill.Tags {
			check(fmt.Sprintf("%s.tags[%d]", field, tagIndex), tag)
		}
	}
	return problems
}

// publicProblem says what is wrong with one string, or "".
func publicProblem(value string) string {
	if strings.Contains(value, "-----BEGIN") {
		return "this looks like a key or certificate and cannot be published"
	}
	for _, word := range strings.Fields(value) {
		word = strings.Trim(word, `.,;:()[]{}"'<>`)
		if word == "" {
			continue
		}
		if strings.EqualFold(word, "bearer") {
			return "this looks like an authorization header and cannot be published"
		}
		for _, prefix := range tokenPrefixes {
			if strings.HasPrefix(word, prefix) && len(word) >= len(prefix)+8 {
				return "this looks like a credential and cannot be published"
			}
		}
		if tokenShape.MatchString(word) && hasLetterAndDigit(word) {
			return "this looks like a credential and cannot be published"
		}
		if looksLikePath(word) {
			return "this looks like a file path and cannot be published"
		}
	}
	return ""
}

// hasLetterAndDigit reports whether a word mixes letters and digits, which separates a token from a
// long plain word.
func hasLetterAndDigit(word string) bool {
	letter, digit := false, false
	for _, r := range word {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letter = true
		}
	}
	return letter && digit
}

// looksLikePath reports whether a word is an absolute or home-relative path, or names a user's home.
func looksLikePath(word string) bool {
	switch {
	case strings.HasPrefix(word, "/") && strings.Count(word, "/") >= 2:
		return true
	case strings.HasPrefix(word, "~/") || word == "~":
		return true
	case strings.Contains(word, "/Users/") || strings.Contains(word, "/home/"):
		return true
	case len(word) >= 3 && word[1] == ':' && (word[2] == '\\' || word[2] == '/') &&
		((word[0] >= 'a' && word[0] <= 'z') || (word[0] >= 'A' && word[0] <= 'Z')):
		return true
	}
	return false
}
