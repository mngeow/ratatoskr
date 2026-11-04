package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// claudeHelpSource records where these shapes came from. It is not a claim that
// they are right: nothing here has run.
const claudeHelpSource = "UNTESTED: shapes from the Claude Code mcp and hooks docs and claude --help 2.1.311"

const (
	claudeMCPFileName      = "claude-mcp.json"
	claudeSettingsFileName = "claude-settings.json"

	claudeServerName = "ratatoskr"
	claudeToolName   = "mcp__ratatoskr__send_message"
)

// claudeMCPFile is the --mcp-config document: one HTTP MCP server, authenticated
// with a bearer header. The token is written literally, because whether ${VAR}
// expands in this file is not stated in the docs.
type claudeMCPFile struct {
	MCPServers map[string]claudeMCPServer `json:"mcpServers"`
}

type claudeMCPServer struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// claudeSettingsFile is the --settings document: one command hook per event and
// no token, because the hook command reads the session file itself.
type claudeSettingsFile struct {
	Hooks map[string][]claudeHookMatcher `json:"hooks"`
}

type claudeHookMatcher struct {
	Hooks []claudeHookCommand `json:"hooks"`
}

type claudeHookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// claudeSpec builds Claude Code's launch spec.
//
// The session id is generated here and returned in LaunchSpec.SessionID, so the
// router binds the agent at registration instead of waiting for a hook event
// that an orphan process could have produced.
//
// UNTESTED.
func claudeSpec(p Params, binary string, spec protocol.LaunchSpec) (protocol.LaunchSpec, error) {
	sessionID := uuid.NewString()

	if err := os.MkdirAll(p.SessionDir, 0o700); err != nil {
		return protocol.LaunchSpec{}, fmt.Errorf("agent: session directory %q: %w", p.SessionDir, err)
	}

	mcpPath := filepath.Join(p.SessionDir, claudeMCPFileName)
	settingsPath := filepath.Join(p.SessionDir, claudeSettingsFileName)

	mcpConfig, err := json.Marshal(claudeMCPFor(p))
	if err != nil {
		return protocol.LaunchSpec{}, fmt.Errorf("agent: marshal %s: %w", claudeMCPFileName, err)
	}
	if err := writeFileAtomic(mcpPath, mcpConfig, 0o600); err != nil {
		return protocol.LaunchSpec{}, err
	}

	settings, err := json.Marshal(claudeSettingsFor(p))
	if err != nil {
		return protocol.LaunchSpec{}, fmt.Errorf("agent: marshal %s: %w", claudeSettingsFileName, err)
	}
	if err := writeFileAtomic(settingsPath, settings, 0o600); err != nil {
		return protocol.LaunchSpec{}, err
	}

	spec.SessionID = sessionID
	// --allowedTools comes last because it is variadic: anything after it would
	// be read as another tool name.
	spec.Argv = []string{
		binary,
		"--session-id", sessionID,
		"--mcp-config", mcpPath,
		"--settings", settingsPath,
		"--allowedTools", claudeToolName,
	}
	return spec, nil
}

func claudeMCPFor(p Params) claudeMCPFile {
	return claudeMCPFile{
		MCPServers: map[string]claudeMCPServer{
			claudeServerName: {
				Type: "http",
				URL:  "http://" + p.ServerAddr + "/mcp",
				Headers: map[string]string{
					"Authorization": "Bearer " + p.Token,
				},
			},
		},
	}
}

func claudeSettingsFor(p Params) claudeSettingsFile {
	hooks := make(map[string][]claudeHookMatcher, len(HookEvents))
	for _, event := range HookEvents {
		hooks[event] = []claudeHookMatcher{{
			Hooks: []claudeHookCommand{{
				Type:    "command",
				Command: HookCommand(p.RtkPath, p.DisplayName, event, p.SessionDir),
				Timeout: hookTimeoutSeconds,
			}},
		}}
	}
	return claudeSettingsFile{Hooks: hooks}
}
