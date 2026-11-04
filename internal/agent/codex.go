package agent

import (
	"fmt"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// codexHelpSource records where these shapes came from. The nested -c shapes are
// the least certain part of this package: nothing here has run.
const codexHelpSource = "UNTESTED: nested -c shapes are not shown in the docs"

const (
	codexServerURLKey      = "mcp_servers.ratatoskr.url"
	codexTokenEnvKey       = "mcp_servers.ratatoskr.bearer_token_env_var"
	codexApprovalModeKey   = "mcp_servers.ratatoskr.tools.send_message.approval_mode"
	codexTokenEnvName      = "RATATOSKR_TOKEN"
	codexApprovalModeValue = "approve"
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

	for _, event := range HookEvents {
		command := HookCommand(p.RtkPath, p.DisplayName, event, p.SessionDir)
		// One inline table per event, holding one command hook.
		value := fmt.Sprintf(
			`[{hooks=[{type="command",command=%s,timeout=%d}]}]`,
			tomlString(command), hookTimeoutSeconds,
		)
		addConfig("hooks."+event, value)
	}

	spec.Argv = argv
	return spec, nil
}
