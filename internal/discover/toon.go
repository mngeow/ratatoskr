package discover

// The TOON encoder, in-house (ADR-0009 D1, section 8). TOON appears ONLY in the discovery tool's reply to a
// model; the gateway's JSON and the stored cards are untouched.
//
// Pinned to the TOON specification, Version 4.4, Date 2026-10-08, Status: Working Draft
// (github.com/toon-format/spec SPEC.md, read 2026-11-11). The spec "may be updated, replaced, or obsoleted",
// so this file is the ONLY place that knows the format: Render is the one function that chooses it, and
// FormatJSON is the fallback behind the same function.
//
// What is implemented, by spec section:
//
//	9.3   root keyed tabular arrays:  key[N]{f1,f2}:  then one row per line, two spaces deep, comma delimiter
//	9.1   an empty array is `key: []` (never the legacy key[0]: form)
//	7.2   a string VALUE is quoted when it is empty; has leading or trailing space or tab; equals true, false
//	      or null; is numeric-like; contains a colon, double quote, backslash, bracket, brace, a control
//	      character or the delimiter; or starts with "-" or "#". Quoting is always allowed, so this encoder also
//	      quotes the other line-break characters (U+0085, U+2028, U+2029), which the spec does not require.
//	7.3   keys and field names are fixed identifiers here, all matching ^[A-Za-z_][A-Za-z0-9_.]*$
//	7.1   escapes: \\ \" \n \r \t, and \uXXXX (lowercase hex) for the other controls U+0000..U+001F; nothing else
//	2,3   null is `null`; numbers are canonical integers (ages in whole seconds); booleans true and false
//	12    LF only, no blank lines, no trailing spaces, no trailing newline, one space after a colon
//
// Hostile text: every string that came from a card or a host is a cell or a value that passes through
// quote(), so card text cannot add or alter a row: a newline inside it becomes the two characters \n inside a
// quoted string, and a comma or colon forces quotes.
//
// UNTESTED. Under GR-3 nothing here has been run; the golden tests are written, not run.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SpecVersion is the TOON specification version this encoder is written against.
const SpecVersion = "4.4 (2026-10-08, Working Draft)"

const delimiter = ","

// numericLike is the spec's rule for a string that must be quoted so it is not read as a number.
var numericLike = regexp.MustCompile(`(?i)^[+-]?[0-9]+(?:\.[0-9]+)?(?:e[+-]?[0-9]+)?$`)

// quote returns a string value as TOON text: bare when the spec allows it, else quoted and escaped.
func quote(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if !needsQuote(s) {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func needsQuote(s string) bool {
	if s == "" || s == "true" || s == "false" || s == "null" || numericLike.MatchString(s) {
		return true
	}
	if s[0] == ' ' || s[0] == '\t' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[0] == '-' || s[0] == '#' {
		return true
	}
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f, r == 0x85, r == 0x2028, r == 0x2029:
			return true
		case strings.ContainsRune(delimiter+`:"\[]{}`, r):
			return true
		case r == utf8.RuneError:
			return true
		}
	}
	return false
}

// cell renders one value of a row or of a key line. nil is null; string, bool, int and int64 are the
// primitives this package uses.
func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return quote(x)
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	panic(fmt.Sprintf("discover: unsupported TOON cell type %T", v))
}

// table is one root keyed tabular array.
type table struct {
	key    string
	fields []string
	rows   [][]any
}

// encodeTOON writes the scalar lines, then each table. An empty table is `key: []`.
func encodeTOON(scalars [][2]any, tables []table) string {
	var lines []string
	for _, kv := range scalars {
		lines = append(lines, kv[0].(string)+": "+cell(kv[1]))
	}
	for _, t := range tables {
		if len(t.rows) == 0 {
			lines = append(lines, t.key+": []")
			continue
		}
		lines = append(lines, fmt.Sprintf("%s[%d]{%s}:", t.key, len(t.rows), strings.Join(t.fields, delimiter)))
		for _, row := range t.rows {
			cells := make([]string, len(row))
			for i, v := range row {
				cells[i] = cell(v)
			}
			lines = append(lines, "  "+strings.Join(cells, delimiter))
		}
	}
	return strings.Join(lines, "\n")
}
