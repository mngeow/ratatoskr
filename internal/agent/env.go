package agent

import "strings"

// The pane tells the child what terminal it is on. These two values are the only
// terminal identity this package states, and they live in exactly one function
// so that a change of pane technology changes one place (design v0.11).
const (
	paneTerm      = "xterm-256color"
	paneColorTerm = "truecolor"
)

// childTerminalEnv is the terminal identity handed to every child.
//
// UNTESTED.
func childTerminalEnv() []string {
	return []string{
		"TERM=" + paneTerm,
		"COLORTERM=" + paneColorTerm,
	}
}

// exactEnvDeny lists names that are dropped only when they match exactly.
//
// TERM is exact, not a prefix: a prefix would also drop TERMINFO, which a
// terminal program may need. The parent's terminal identity is removed because
// the child is not on that terminal; ours is added back by childTerminalEnv.
var exactEnvDeny = map[string]struct{}{
	"TERM":            {},
	"COLORTERM":       {},
	"COLUMNS":         {},
	"LINES":           {},
	"VTE_VERSION":     {},
	"WINDOWID":        {},
	"TERM_SESSION_ID": {},
}

// prefixEnvDeny lists prefixes that are dropped wholesale: a rig's own
// variables, the harness's token, and every terminal program's marker.
// WT_ is a prefix rather than WT_SESSION so WT_PROFILE_ID is covered too.
var prefixEnvDeny = []string{
	"TMUX",
	"OPENRIG_",
	"RIG_",
	"RATATOSKR_",
	"TERM_PROGRAM",
	"KITTY_",
	"WEZTERM_",
	"ITERM_",
	"KONSOLE_",
	"ALACRITTY_",
	"WT_",
}

// buildEnv assembles the child's complete environment from BaseEnv.
//
// Everything that could describe the terminal this process runs on, or the rig
// that started it, is dropped; everything else is kept, including PATH, HOME,
// locale and login variables. No entry is deduplicated: whatever could collide
// has already been removed, and a duplicate PATH is the caller's business.
//
// UNTESTED.
func buildEnv(p Params) []string {
	env := make([]string, 0, len(p.BaseEnv)+len(childTerminalEnv())+1)

	for _, entry := range p.BaseEnv {
		name, _, _ := strings.Cut(entry, "=")
		if deniedEnvName(name) {
			continue
		}
		env = append(env, entry)
	}

	env = append(env, childTerminalEnv()...)

	// Codex reads the token from the environment, because it has no
	// per-server header option. Claude gets it in its MCP config file instead,
	// so its environment carries no secret.
	if p.Harness == harnessCodex {
		env = append(env, "RATATOSKR_TOKEN="+p.Token)
	}
	return env
}

// deniedEnvName reports whether a variable must not reach a child.
func deniedEnvName(name string) bool {
	if name == "" {
		return true
	}
	if _, denied := exactEnvDeny[name]; denied {
		return true
	}
	for _, prefix := range prefixEnvDeny {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
