// Package card is the host card's model: parsing, validation, canonical bytes, the ETag and the
// public-content check. It is pure (standard library and internal/protocol only), so the daemon, which
// publishes a card, and the gateway, which fetches other hosts' cards, apply exactly the same rules.
//
// A card is public and untrusted at the same time: the daemon's own cards are public the moment they
// are served, and every card the gateway fetches is text from another machine that a window will draw.
// So the rules are an allow-list in both directions. Only known fields exist; every string is bounded
// and free of control characters; the URL has one shape; and a rejection names a field, never a value.
//
// Authority: docs/implementation/host-cards-contract.md (ADR-0005). This is an "A2A-style card", not a
// conformance claim.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.
package card

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Mode says what Parse does with a field this build does not know.
type Mode int

const (
	// Strict refuses an unknown field, naming it. The daemon uses it for a card the owner is
	// publishing: a typo must not be silently dropped.
	Strict Mode = iota + 1
	// Polled ignores unknown fields, drops them from what is kept, and counts them. The gateway uses
	// it for a card fetched from another host, so a host on a newer build does not turn rejected
	// during a rolling upgrade. Known fields still get full validation.
	Polled
)

// Parsed is a card that passed Parse.
type Parsed struct {
	Card protocol.HostCard
	// UnknownFields counts the unknown fields Polled mode dropped; it is always 0 in Strict mode.
	UnknownFields int
}

// Parse reads and validates one card from JSON bytes.
//
// served says whether the card must carry its own URL: a card as served or as polled must, a card as
// stored and edited on its owner's host need not.
//
// A non-empty problem list means the card is rejected and the Parsed value is empty. Problems name a
// field and say what is wrong in our own words; no problem repeats a value from the input.
//
// UNTESTED.
func Parse(data []byte, mode Mode, served bool) (Parsed, []protocol.CardProblem) {
	if len(data) > protocol.MaxCardBytes {
		return Parsed{}, one("", "the card is larger than 64 KiB")
	}
	if !utf8.Valid(data) {
		return Parsed{}, one("", "the card is not valid UTF-8")
	}

	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		return Parsed{}, one("", "the card is not valid JSON")
	}
	if _, isObject := generic.(map[string]any); !isObject {
		return Parsed{}, one("", "the card must be a JSON object")
	}

	// Only exact-case known keys survive, so a case-variant key can never fill a known field through
	// the decoder's case-insensitive matching.
	var pruning prune
	pruned := pruning.walk(generic, reflect.TypeOf(protocol.HostCard{}), "")

	if mode == Strict && pruning.count > 0 {
		return Parsed{}, one(pruning.first, "this field is not part of the card")
	}

	clean, err := json.Marshal(pruned)
	if err != nil {
		return Parsed{}, one("", "the card could not be read")
	}

	var card protocol.HostCard
	if err := json.Unmarshal(clean, &card); err != nil {
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) {
			return Parsed{}, one(cleanPath(typeError.Field), "this field has the wrong type")
		}
		return Parsed{}, one("", "the card could not be read")
	}

	if problems := Validate(card, served); len(problems) > 0 {
		return Parsed{}, problems
	}
	return Parsed{Card: card, UnknownFields: pruning.count}, nil
}

// one builds a single-problem list.
func one(field, message string) []protocol.CardProblem {
	return []protocol.CardProblem{{Field: field, Message: message}}
}

// prune copies a decoded JSON value keeping only the keys a Go type declares, and counts the rest.
type prune struct {
	count int
	// first is the path of the first unknown field in sorted order, so a refusal is deterministic.
	first string
}

// walk returns value without the keys typ does not declare.
func (p *prune) walk(value any, typ reflect.Type, path string) any {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}

	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		fields := jsonFields(typ)
		out := make(map[string]any, len(object))
		for _, key := range sortedKeys(object) {
			fieldType, known := fields[key]
			if !known {
				p.count++
				if p.first == "" {
					p.first = cleanPath(join(path, key))
				}
				continue
			}
			out[key] = p.walk(object[key], fieldType, join(path, key))
		}
		return out

	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return value
		}
		out := make([]any, len(items))
		for index, item := range items {
			out[index] = p.walk(item, typ.Elem(), path+"[]")
		}
		return out

	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return value
		}
		out := make(map[string]any, len(object))
		for _, key := range sortedKeys(object) {
			out[key] = p.walk(object[key], typ.Elem(), path+"{}")
		}
		return out
	}
	return value
}

// jsonFields maps a struct's JSON key names to their field types.
func jsonFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, typ.NumField())
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "-" || !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

// sortedKeys returns a map's keys in order.
func sortedKeys(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// join appends a key to a dotted path.
func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// cleanPath makes a field path safe to show: no control characters, bounded. A path can come from a
// remote card's key names, which are text from another machine.
func cleanPath(path string) string {
	var builder strings.Builder
	count := 0
	for _, r := range path {
		if r == utf8.RuneError || isControl(r) {
			continue
		}
		builder.WriteRune(r)
		count++
		if count >= 64 {
			break
		}
	}
	return builder.String()
}

// Canonical renders a card as its canonical bytes: struct field order, map keys sorted, no HTML
// escaping, no trailing newline. The same card always yields the same bytes, which is what the ETag
// and the published file rest on.
//
// UNTESTED.
func Canonical(card protocol.HostCard) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(card); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}
