package agent

import (
	"encoding/json"
	"testing"
)

// These tests are written but UNRUN: GR-3 forbids running them.

func TestHookEventsOrder(t *testing.T) {
	want := []string{"SessionStart", "UserPromptSubmit", "Stop", "PermissionRequest", "PostToolUse"}
	if len(HookEvents) != len(want) {
		t.Fatalf("HookEvents has %d entries, want %d", len(HookEvents), len(want))
	}
	for i := range want {
		if HookEvents[i] != want[i] {
			t.Fatalf("HookEvents[%d] = %q, want %q", i, HookEvents[i], want[i])
		}
	}
}

func TestHookCommandQuoting(t *testing.T) {
	tests := []struct {
		name       string
		rtkPath    string
		agentName  string
		event      string
		sessionDir string
		want       string
	}{
		{
			name:       "everything bare",
			rtkPath:    "/opt/rtk/bin/rtk",
			agentName:  "alpha",
			event:      "SessionStart",
			sessionDir: "/run/session",
			want:       "/opt/rtk/bin/rtk hook --agent alpha --event SessionStart --session-dir /run/session",
		},
		{
			name:       "space in the rtk path",
			rtkPath:    "/Applications/My Tools/rtk",
			agentName:  "alpha",
			event:      "Stop",
			sessionDir: "/run/session",
			want:       "'/Applications/My Tools/rtk' hook --agent alpha --event Stop --session-dir /run/session",
		},
		{
			name:       "single quote in the path",
			rtkPath:    "/tmp/it's/rtk",
			agentName:  "alpha",
			event:      "Stop",
			sessionDir: "/run/session",
			want:       `'/tmp/it'\''s/rtk' hook --agent alpha --event Stop --session-dir /run/session`,
		},
		{
			name:       "empty rtk path still quotes",
			rtkPath:    "",
			agentName:  "alpha",
			event:      "Stop",
			sessionDir: "/run/session",
			want:       `'' hook --agent alpha --event Stop --session-dir /run/session`,
		},
		{
			name:       "space in the session dir",
			rtkPath:    "/opt/rtk",
			agentName:  "alpha",
			event:      "PostToolUse",
			sessionDir: "/Users/me/Library/App Support/ratatoskr/session",
			want:       "/opt/rtk hook --agent alpha --event PostToolUse --session-dir '/Users/me/Library/App Support/ratatoskr/session'",
		},
		{
			name:       "dollar sign is not a shell expansion here",
			rtkPath:    "/opt/rtk",
			agentName:  "alpha",
			event:      "Stop",
			sessionDir: "/tmp/$HOME/session",
			want:       "/opt/rtk hook --agent alpha --event Stop --session-dir '/tmp/$HOME/session'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := HookCommand(tc.rtkPath, tc.agentName, tc.event, tc.sessionDir)
			if got != tc.want {
				t.Fatalf("HookCommand =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestHookCommandIgnoresTokenAndPort is the determinism rule: Codex re-asks the
// user to trust a hook whose definition changed, so the command must not carry
// anything that varies per run.
func TestHookCommandIgnoresTokenAndPort(t *testing.T) {
	first, err := Build(baseParams(t, harnessClaude))
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	params := baseParams(t, harnessClaude)
	params.Token = "a-different-token"
	params.ServerAddr = "127.0.0.1:4999"
	second, err := Build(params)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}

	firstCommands := commandsFromClaudeArgs(t, first.Argv)
	secondCommands := commandsFromClaudeArgs(t, second.Argv)

	if len(firstCommands) != len(HookEvents) {
		t.Fatalf("first build configured %d events, want %d", len(firstCommands), len(HookEvents))
	}
	for event, command := range firstCommands {
		if secondCommands[event] != command {
			t.Fatalf("event %s: command changed between builds:\n%q\n%q", event, command, secondCommands[event])
		}
	}
}

// commandsFromClaudeArgs reads the settings file a Build wrote and returns the
// command for each event.
func commandsFromClaudeArgs(t *testing.T, argv []string) map[string]string {
	t.Helper()

	settingsPath := ""
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "--settings" {
			settingsPath = argv[i+1]
		}
	}
	if settingsPath == "" {
		t.Fatalf("argv carries no --settings: %v", argv)
	}

	content, err := readFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(content, &settings); err != nil {
		t.Fatalf("parse settings: %v", err)
	}

	commands := make(map[string]string, len(settings.Hooks))
	for event, matchers := range settings.Hooks {
		if len(matchers) != 1 || len(matchers[0].Hooks) != 1 {
			t.Fatalf("event %s has an unexpected hook shape", event)
		}
		commands[event] = matchers[0].Hooks[0].Command
	}
	return commands
}

func TestTomlStringEscaping(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "hello", want: `"hello"`},
		{name: "backslash", in: `a\b`, want: `"a\\b"`},
		{name: "quote", in: `a"b`, want: `"a\"b"`},
		{name: "newline", in: "a\nb", want: `"a\u000ab"`},
		{name: "tab", in: "a\tb", want: `"a\u0009b"`},
		{name: "del", in: "a\x7fb", want: `"a\u007fb"`},
		{name: "nul", in: "a\x00b", want: `"a\u0000b"`},
		{name: "non-ascii is literal", in: "héllo →", want: `"héllo →"`},
		{name: "single quote needs nothing", in: `it's`, want: `"it's"`},
		{name: "dollar needs nothing", in: "$HOME", want: `"$HOME"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tomlString(tc.in); got != tc.want {
				t.Fatalf("tomlString(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}
