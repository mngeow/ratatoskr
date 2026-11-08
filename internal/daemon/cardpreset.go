package daemon

// The drafting presets: the exact command a card draft runs for `preset: claude`, `codex` and `custom`.
//
// The flags below are taken from the vendors' own documentation as READ on 2026-11-08, not from memory
// and not from `--help` (running a binary to see its help is a probe GR-3 forbids). Nothing here has
// been run: the presets are UNTESTED, and the confirmation screen says so.
//
// Claude Code, https://code.claude.com/docs/en/cli-reference and https://code.claude.com/docs/en/headless:
//
//	-p, --print              "Print response without interactive mode"
//	--safe-mode              "Start with all customizations disabled ... CLAUDE.md, skills, plugins, hooks, MCP
//	                          servers, custom commands and agents, output styles, workflows ... and auto memory
//	                          do not load. Authentication, model selection, built-in tools, and permissions
//	                          work normally"
//	--tools ""               "Restrict which built-in tools Claude can use. Use "" to disable all"
//	--no-session-persistence "Disable session persistence so sessions are not saved to disk" (print mode only)
//	--output-format text     "text (default): plain text output"
//	--permission-prompts none "Pass none when nobody can answer, and Claude Code denies them instead."
//	stdin                    "Non-interactive mode reads stdin" (example: cat file | claude -p "query")
//
// --safe-mode does not switch off everything: the same page says "Managed settings policy still applies,
// including policy-configured hooks, status line, and file-suggestion commands." --permission-prompts is
// marked v2.1.259+ in the docs, so an older Claude Code refuses it.
//
// --bare is NOT used: the docs say it never reads OAuth credentials or the keychain, so it would not find
// the owner's login.
//
// Codex, https://developers.openai.com/codex/noninteractive and https://developers.openai.com/codex/cli/reference.
// Both answered HTTP 308 to learn.chatgpt.com/docs/non-interactive-mode and
// learn.chatgpt.com/docs/developer-commands?surface=cli on 2026-11-08; that redirect from the OpenAI
// domain is the only evidence the second domain is OpenAI's (not independently confirmed):
//
//	--sandbox read-only      "Sandbox policy for model-generated commands"
//	--skip-git-repo-check    "Allow running outside a Git repository"
//	--ephemeral              "Run without persisting session rollout files to disk"
//	--ignore-user-config     "Do not load $CODEX_HOME/config.toml. Authentication still uses CODEX_HOME."
//	--ignore-rules           "Do not load user or project execpolicy .rules files for this run"
//	prompt + stdin           "If stdin is piped and you also provide a prompt argument, Codex treats the
//	                          prompt as the instruction and the piped content as additional context."
//
// The Codex pages offer NO per-run switch for skills, hooks or MCP servers. --ignore-user-config skips the
// file the docs say holds MCP server entries (`codex mcp` manages "~/.codex/config.toml"); that this stops
// them loading is an inference the docs do not state.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"fmt"
	"sort"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	draftDocsRead = "2026-11-08"

	// draftPrompt is the one positional argument the presets pass. The real instruction travels in the
	// payload on stdin, where the owner reads it whole on the confirmation screen.
	draftPrompt = "Follow the instruction in the JSON on stdin. Reply with only the JSON object it asks for. Do not use tools."
)

// draftCommand is a built command for the drafter, with what the owner is shown about it.
type draftCommand struct {
	spec     card.CommandSpec
	envNames []string
	caveats  []string
}

// buildDraftCommand turns the private drafting: section into a command. getenv reads the daemon's
// environment for the names the command may see; a name that is unset is not passed and not listed.
//
// UNTESTED.
func buildDraftCommand(draft card.OwnerDraft, getenv func(string) string) (draftCommand, error) {
	var argv, baseEnv, caveats []string

	switch draft.Preset {
	case card.PresetClaude:
		argv = []string{
			"claude", "-p", "--safe-mode", "--tools", "", "--no-session-persistence",
			"--output-format", "text", "--permission-prompts", "none", draftPrompt,
		}
		baseEnv = []string{"PATH", "HOME", "CLAUDE_CONFIG_DIR"}
		caveats = []string{
			"Flags are from Anthropic's Claude Code documentation (https://code.claude.com/docs/en/cli-reference and /headless), read " + draftDocsRead + ". Ratatoskr has NOT run this command.",
			"--safe-mode starts Claude Code with CLAUDE.md, skills, plugins, hooks, MCP servers, custom commands, agents and auto memory not loaded; login, model and permissions work normally (per the docs). It does NOT turn off everything: managed settings policy still applies, including policy-configured hooks, status line and file-suggestion commands. --tools \"\" removes the built-in tools.",
			"--permission-prompts is marked v2.1.259+ in the docs: an older Claude Code refuses the flag, so a failure may be a version problem.",
			"HOME, PATH and CLAUDE_CONFIG_DIR (if set) are passed so Claude Code can find its own login. The documentation does not list every variable it may need, so a login can fail: add names under pass_env.",
		}
	case card.PresetCodex:
		argv = []string{
			"codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check", "--ephemeral",
			"--ignore-user-config", "--ignore-rules", draftPrompt,
		}
		baseEnv = []string{"PATH", "HOME", "CODEX_HOME"}
		caveats = []string{
			"Flags are from OpenAI's Codex documentation (https://developers.openai.com/codex/noninteractive and /codex/cli/reference, which answered HTTP 308 to https://learn.chatgpt.com/docs/non-interactive-mode and /docs/developer-commands?surface=cli), read " + draftDocsRead + ". That the second domain is OpenAI's is not independently confirmed. Ratatoskr has NOT run this command.",
			"The documentation offers no per-run switch for skills, hooks or MCP servers, so Codex may load yours. --ignore-user-config skips $CODEX_HOME/config.toml, where the docs say MCP servers are stored; they do not say this stops them loading.",
			"HOME, PATH and CODEX_HOME (if set) are passed so Codex can find its own login. The documentation does not list every variable it may need: add names under pass_env if the login fails.",
		}
	case card.PresetCustom:
		argv = append([]string(nil), draft.Argv...)
		baseEnv = []string{"PATH"}
		caveats = []string{
			"This is your own command. Ratatoskr cannot say what it loads, runs or sends. Only PATH and the names under pass_env are passed to it.",
		}
	default:
		return draftCommand{}, fmt.Errorf("daemon: no drafting preset is set in card.yaml")
	}
	caveats = append(caveats, "The payload above is sent to whatever model service your command talks to.")

	names := append([]string(nil), baseEnv...)
	names = append(names, draft.PassEnv...)
	sort.Strings(names)

	var env, passed []string
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if value := getenv(name); value != "" {
			env = append(env, name+"="+value)
			passed = append(passed, name)
		}
	}

	return draftCommand{
		spec: card.CommandSpec{
			Argv:      argv,
			Env:       env,
			Timeout:   protocol.CardDraftTimeoutSec * time.Second,
			MaxOutput: protocol.CardDraftOutputCapBytes,
			Label:     draft.Preset,
		},
		envNames: passed,
		caveats:  caveats,
	}, nil
}
