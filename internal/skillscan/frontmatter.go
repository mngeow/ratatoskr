package skillscan

// The front-matter reader: name and description from a SKILL.md, and nothing else.
//
// This is deliberately not a YAML parser. A skill's front matter is written by hand, and the two fields
// this package reports are two short scalars; everything else in the file - other keys, nested maps,
// lists, comments, blank lines - is ignored without complaint. Only a `name` or `description` value that
// this reader cannot understand is refused, with one sentence. It never panics and never loops: every
// loop below advances through the input.

import (
	"strings"
	"unicode/utf8"
)

const (
	// frontMatterWindow is how far into the file a line may START and still count: the closing "---"
	// must begin inside it. Beyond this the front matter is treated as unclosed rather than read wider:
	// a SKILL.md is a small file, and a front matter that long is a mistake, not something to
	// accommodate.
	frontMatterWindow = 8 << 10

	// frontMatterClose is the delimiter that opens and closes the front matter.
	frontMatterClose = "---"
)

// The problems this reader reports. Each is our own sentence, and none carries a path, a line number or
// a byte offset.
const (
	problemNoFrontMatter = "no front matter"
	problemUnclosed      = "front matter is not closed"
	problemUnsupported   = "the front matter uses syntax this reader does not support"
)

// FrontMatter is what a SKILL.md says about itself.
//
// Problem is empty when the front matter was read. For an unsupported value the fields found before it
// are kept; for no front matter or an unclosed one both fields are empty, because nothing is trusted
// from a block that never ended.
type FrontMatter struct {
	Name        string
	Description string
	Problem     string
}

// ParseFrontMatter reads the name and description from a SKILL.md's bytes.
//
// A UTF-8 BOM and CRLF line endings are accepted, because editors add both. Only `name` and
// `description` are read, and the first occurrence of each wins.
//
// UNTESTED.
func ParseFrontMatter(data []byte) FrontMatter {
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines, starts := splitLines(text)

	if len(lines) == 0 || trimCR(lines[0]) != frontMatterClose {
		return FrontMatter{Problem: problemNoFrontMatter}
	}

	var front FrontMatter
	haveName, haveDescription := false, false

	for index := 1; index < len(lines); index++ {
		if starts[index] >= frontMatterWindow {
			// The closing delimiter was not found in the window; nothing read so far is trusted.
			return FrontMatter{Problem: problemUnclosed}
		}

		line := trimCR(lines[index])
		if line == frontMatterClose {
			return front
		}

		key, value, ok := splitKey(line)
		if !ok {
			// A blank line, a comment, a list item, an indented line of a block we are not reading:
			// none of them is a key line, and none of them is our business.
			continue
		}

		switch {
		case key == "name" && !haveName:
			read, problem := readScalar(lines, &index, value)
			if problem != "" {
				front.Problem = problem
				return front
			}
			front.Name = collapseSpace(read)
			haveName = true

		case key == "description" && !haveDescription:
			read, problem := readScalar(lines, &index, value)
			if problem != "" {
				front.Problem = problem
				return front
			}
			front.Description = collapseSpace(read)
			haveDescription = true
		}
		// Every other key, and a repeated name or description, is ignored; the indented lines under it
		// are skipped by the not-a-key-line rule above.
	}

	return FrontMatter{Problem: problemUnclosed}
}

// splitLines splits on LF and returns each line with the byte offset where it starts. The CR of a CRLF
// stays on the line for the caller to trim, so both endings behave the same.
func splitLines(text string) ([]string, []int) {
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	starts := make([]int, len(lines))
	offset := 0
	for index, line := range lines {
		starts[index] = offset
		offset += len(line) + 1
	}
	return lines, starts
}

// trimCR removes the CR a CRLF leaves on a line.
func trimCR(line string) string {
	return strings.TrimRight(line, "\r")
}

// splitKey reads one `key: value` line at zero indentation.
//
// A line with no colon, or a colon with nothing that looks like a key before it, is not a key line.
func splitKey(line string) (key, value string, ok bool) {
	if line == "" || isIndented(line) {
		return "", "", false
	}
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:colon])
	if key == "" {
		return "", "", false
	}
	for _, r := range key {
		if !isKeyRune(r) {
			return "", "", false
		}
	}
	return key, strings.TrimLeft(line[colon+1:], " \t"), true
}

// isKeyRune allows the characters a plain YAML key uses here.
func isKeyRune(r rune) bool {
	return r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// isIndented reports whether a line starts with a space or a tab.
func isIndented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

// readScalar reads one value, advancing the line index past the lines the value owns.
//
// The shapes are a `>` or `|` block, a quoted scalar on one line (with an optional trailing comment),
// and a plain scalar whose indented following lines continue it.
func readScalar(lines []string, index *int, value string) (string, string) {
	trimmed := strings.TrimRight(value, " \t")

	switch {
	case strings.HasPrefix(trimmed, ">"):
		return readBlock(lines, index, ">")

	case strings.HasPrefix(trimmed, "|"):
		return readBlock(lines, index, "|")

	case strings.HasPrefix(trimmed, "\""):
		return readQuoted(trimmed, '"')

	case strings.HasPrefix(trimmed, "'"):
		return readQuoted(trimmed, '\'')
	}

	parts := make([]string, 0, 2)
	if first := stripComment(trimmed); first != "" {
		parts = append(parts, first)
	}
	parts = append(parts, continuation(lines, index)...)
	return strings.Join(parts, " "), ""
}

// continuation collects the indented lines that continue a plain scalar, advancing the index past
// them. A comment line or the closing delimiter ends it.
func continuation(lines []string, index *int) []string {
	var parts []string
	for next := *index + 1; next < len(lines); next++ {
		line := trimCR(lines[next])
		if line == frontMatterClose {
			break
		}
		if line != "" && !isIndented(line) {
			break
		}
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "#") {
			break
		}
		*index = next
		if text != "" {
			parts = append(parts, text)
		}
	}
	return parts
}

// stripComment removes a trailing ` #comment` from a plain scalar.
//
// Only a hash with leading whitespace starts a comment; a hash inside a word is part of the value.
func stripComment(value string) string {
	for index := 0; index < len(value); index++ {
		if value[index] != '#' {
			continue
		}
		if index > 0 && (value[index-1] == ' ' || value[index-1] == '\t') {
			return strings.TrimRight(value[:index], " \t")
		}
	}
	return value
}

// readQuoted reads a single-line quoted scalar, with an optional trailing ` #comment` after the closing
// quote.
//
// In a single-quoted value two apostrophes are one; in a double-quoted value `\"` is a quote, `\n` a
// newline and `\t` a tab, with every other backslash left as written. A value that does not close on its
// line, or has anything but a comment after the closing quote, is refused.
func readQuoted(value string, quote byte) (string, string) {
	var builder strings.Builder
	builder.Grow(len(value))

	for index := 1; index < len(value); index++ {
		char := value[index]

		if quote == '"' && char == '\\' && index+1 < len(value) {
			next := value[index+1]
			switch next {
			case '"':
				builder.WriteByte('"')
			case 'n':
				builder.WriteByte('\n')
			case 't':
				builder.WriteByte('\t')
			default:
				builder.WriteByte('\\')
				builder.WriteByte(next)
			}
			index++
			continue
		}

		if char != quote {
			builder.WriteByte(char)
			continue
		}

		if quote == '\'' && index+1 < len(value) && value[index+1] == '\'' {
			builder.WriteByte('\'')
			index++
			continue
		}

		if !onlyTrailingComment(value[index+1:]) {
			return "", problemUnsupported
		}
		return builder.String(), ""
	}

	// The line ended before the closing quote.
	return "", problemUnsupported
}

// onlyTrailingComment reports whether what follows a closing quote is nothing, or whitespace then a
// comment.
func onlyTrailingComment(rest string) bool {
	if strings.TrimSpace(rest) == "" {
		return true
	}
	return (strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, "\t")) &&
		strings.HasPrefix(strings.TrimLeft(rest, " \t"), "#")
}

// readBlock reads a `>` or `|` block: the following lines that are indented.
//
// `>` folds the lines into spaces and `|` keeps the newlines; either way the caller collapses whitespace
// runs afterwards, so the reported text is one line either way.
func readBlock(lines []string, index *int, style string) (string, string) {
	collected := make([]string, 0, 4)

	for next := *index + 1; next < len(lines); next++ {
		line := trimCR(lines[next])
		if line == frontMatterClose {
			break
		}
		if line != "" && !isIndented(line) {
			break
		}
		*index = next
		collected = append(collected, strings.TrimSpace(line))
	}

	if style == ">" {
		return strings.Join(collected, " "), ""
	}
	return strings.Join(collected, "\n"), ""
}

// collapseSpace turns every run of whitespace into one space, drops control characters and trims the
// ends.
//
// Both fields are shown on one line, so their own newlines are not meaningful to a reader.
func collapseSpace(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	space := false
	for _, r := range value {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if builder.Len() > 0 {
				space = true
			}
			continue
		}
		if isControl(r) || r == utf8.RuneError {
			continue
		}
		if space {
			builder.WriteByte(' ')
			space = false
		}
		builder.WriteRune(r)
	}
	return builder.String()
}
