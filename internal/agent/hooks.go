package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// HookEvents is every harness hook this build configures, in the order both
// specs write them. No matcher is set: unimplemented events are irrelevant here,
// and a matcher would make the definition depend on which tool ran.
var HookEvents = []string{
	"SessionStart",
	"UserPromptSubmit",
	"Stop",
	"PermissionRequest",
	"PostToolUse",
}

// hookTimeoutSeconds is the hook's own timeout, as each harness reads it. The
// unit is the harness's; for Codex it is UNTESTED.
const hookTimeoutSeconds = 10

// bareArgPattern matches an argument that a POSIX shell passes through
// unchanged, so quoting it is unnecessary.
var bareArgPattern = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]+$`)

// HookCommand builds the command line a harness runs on a hook event.
//
// The command is deterministic for identical inputs, because Codex asks the user
// to re-trust a hook whose definition changed: a command that varied per run
// (an embedded token, port or nonce) would re-prompt on every start. Nothing
// secret appears in it - the token is read from the session file at call time.
//
// UNTESTED.
func HookCommand(rtkPath, agentName, event, sessionDir string) string {
	raw := []string{
		rtkPath,
		"hook",
		"--agent", agentName,
		"--event", event,
		"--session-dir", sessionDir,
	}

	quoted := make([]string, 0, len(raw))
	for _, arg := range raw {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

// shellQuote leaves a safe argument bare and single-quotes anything else,
// writing an embedded single quote the portable way.
func shellQuote(arg string) string {
	if bareArgPattern.MatchString(arg) {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// tomlString renders a TOML basic string: backslash and quote escaped, and every
// control rune written as a \uXXXX escape so no raw control character ever
// reaches a harness's config parser.
//
// UNTESTED.
func tomlString(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')

	for _, r := range value {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case r == '"':
			b.WriteString(`\"`)
		case r < 0x20 || r == 0x7f:
			b.WriteString(`\u`)
			hex := strconv.FormatInt(int64(r), 16)
			for i := len(hex); i < 4; i++ {
				b.WriteByte('0')
			}
			b.WriteString(hex)
		default:
			b.WriteRune(r)
		}
	}

	b.WriteByte('"')
	return b.String()
}
