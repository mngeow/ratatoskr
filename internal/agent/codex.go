package agent

import (
	"fmt"
	"strings"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// codexHelpSource records where these shapes came from. The nested -c shapes are
// the least certain part of this package: nothing here has run.
const codexHelpSource = "UNTESTED: nested -c shapes are not shown in the docs"

const (
	// peerToolName is the tool the prompt tells Codex to use. It duplicates the
	// server's tool name by necessity: internal/agent must not import
	// internal/server, and the two are visible together only to a reader of both.
	peerToolName = "ratatoskr_send"
	// discoverToolName is the discovery tool (ADR-0009), duplicated for the same reason.
	discoverToolName = "ratatoskr_discover"

	codexServerURLKey            = "mcp_servers.ratatoskr.url"
	codexTokenEnvKey             = "mcp_servers.ratatoskr.bearer_token_env_var"
	codexApprovalModeKey         = "mcp_servers.ratatoskr.tools.ratatoskr_send.approval_mode"
	codexDiscoverApprovalModeKey = "mcp_servers.ratatoskr.tools.ratatoskr_discover.approval_mode"
	codexTokenEnvName            = "RATATOSKR_TOKEN"
	codexApprovalModeValue       = "approve"
)

// codexSpec builds Codex's launch spec.
//
// Codex gets no session id: no flag for one was found in its help output, so the
// router binds from the first SessionStart whose source is startup instead.
//
// Nothing here adds or loosens a sandbox or approval flag, and no --dangerously-*
// option is used: a harness is launched as the user configured it.
//
// UNTESTED.
func codexSpec(p Params, binary string, spec protocol.LaunchSpec) (protocol.LaunchSpec, error) {
	argv := []string{binary}

	addConfig := func(key, value string) {
		argv = append(argv, "-c", key+"="+value)
	}

	addConfig(codexServerURLKey, tomlString("http://"+p.ServerAddr+"/mcp"))
	addConfig(codexTokenEnvKey, tomlString(codexTokenEnvName))
	addConfig(codexApprovalModeKey, tomlString(codexApprovalModeValue))
	addConfig(codexDiscoverApprovalModeKey, tomlString(codexApprovalModeValue))

	for _, event := range HookEvents {
		command := HookCommand(p.RtkPath, p.DisplayName, event, p.SessionDir)
		// One inline table per event, holding one command hook.
		value := fmt.Sprintf(
			`[{hooks=[{type="command",command=%s,timeout=%d}]}]`,
			tomlString(command), hookTimeoutSeconds,
		)
		addConfig("hooks."+event, value)
	}

	// The startup prompt is the trailing [PROMPT] argument: Codex sends its first
	// hook only when a turn starts, so without this a held message waits for the
	// user to type something.
	argv = append(argv, codexStartupPrompt(p.DisplayName, p.Peers))

	spec.Argv = argv
	return spec, nil
}

// codexStartupPromptText is the fixed part of the prompt.
const codexStartupPromptText = "Message a peer only with the " + peerToolName +
	" tool from the ratatoskr MCP server; a built-in send_message does not reach peers." +
	" To see who is on the network, call " + discoverToolName + " first." +
	" Do not start any work or message anyone now. Reply with one short line saying you are ready, then wait."

// codexStartupPrompt builds the first turn Codex is launched with.
//
// Every word here is visible in the process list, so nothing secret may ever be
// added: no token, no URL, no session directory, no message content.
//
// It must begin with "You are": a leading dash or a subcommand word (exec,
// review, login, resume) could be read by Codex's argument parser as an option
// or a command instead of the [PROMPT]. The text is fixed, so no `--` separator
// is used; whether the parser accepts one here was not checked.
//
// UNTESTED.
func codexStartupPrompt(name string, peers []string) string {
	base := "You are " + name + " in a Ratatoskr session"
	switch len(peers) {
	case 0:
		base += "."
	case 1:
		base += "; your peer is " + peers[0] + "."
	default:
		base += "; your peers are " + strings.Join(peers, ", ") + "."
	}
	return base + " " + codexStartupPromptText
}
