package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// These tests are written but UNRUN: GR-3 forbids running them.

// readFile is a tiny indirection so tests read what Build wrote without pulling
// os into every file's imports.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func TestClaudeSpecArgv(t *testing.T) {
	params := baseParams(t, harnessClaude)
	spec, err := Build(params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if _, err := uuid.Parse(spec.SessionID); err != nil {
		t.Fatalf("SessionID %q is not a UUID: %v", spec.SessionID, err)
	}

	mcpPath := filepath.Join(params.SessionDir, claudeMCPFileName)
	settingsPath := filepath.Join(params.SessionDir, claudeSettingsFileName)
	want := []string{
		params.Binary,
		"--session-id", spec.SessionID,
		"--mcp-config", mcpPath,
		"--settings", settingsPath,
		"--allowedTools", "mcp__ratatoskr__send_message",
	}
	if len(spec.Argv) != len(want) {
		t.Fatalf("argv has %d elements, want %d: %v", len(spec.Argv), len(want), spec.Argv)
	}
	for i := range want {
		if spec.Argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q", i, spec.Argv[i], want[i])
		}
	}

	// The session id is what the router binds, so it must be the one on the
	// command line, not merely a UUID.
	if spec.Argv[2] != spec.SessionID {
		t.Fatalf("argv session id %q differs from SessionID %q", spec.Argv[2], spec.SessionID)
	}
}

func TestClaudeSpecSessionIDDiffersPerBuild(t *testing.T) {
	first, err := Build(baseParams(t, harnessClaude))
	if err != nil {
		t.Fatalf("first Build: %v", err)
	}
	second, err := Build(baseParams(t, harnessClaude))
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}

	if first.SessionID == second.SessionID {
		t.Fatal("two builds produced the same session id")
	}
	for _, id := range []string{first.SessionID, second.SessionID} {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("session id %q is not a UUID: %v", id, err)
		}
	}
}

func TestClaudeMCPFileShape(t *testing.T) {
	params := baseParams(t, harnessClaude)
	if _, err := Build(params); err != nil {
		t.Fatalf("Build: %v", err)
	}

	path := filepath.Join(params.SessionDir, claudeMCPFileName)
	assertMode(t, path, 0o600)

	content, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var document struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse: %v", err)
	}

	server, ok := document.MCPServers[claudeServerName]
	if !ok {
		t.Fatalf("mcp config has no %q server: %s", claudeServerName, content)
	}
	if server.Type != "http" {
		t.Fatalf("type = %q, want http", server.Type)
	}
	if want := "http://" + params.ServerAddr + "/mcp"; server.URL != want {
		t.Fatalf("url = %q, want %q", server.URL, want)
	}
	if got := server.Headers["Authorization"]; got != "Bearer "+params.Token {
		t.Fatalf("Authorization = %q, want the bearer token", got)
	}
}

func TestClaudeSettingsFileShape(t *testing.T) {
	params := baseParams(t, harnessClaude)
	if _, err := Build(params); err != nil {
		t.Fatalf("Build: %v", err)
	}

	path := filepath.Join(params.SessionDir, claudeSettingsFileName)
	assertMode(t, path, 0o600)

	content, err := readFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// The token must not be in a file the harness shows in a settings diff.
	if strings.Contains(string(content), params.Token) {
		t.Fatalf("settings file contains the token: %s", content)
	}

	commands := commandsFromClaudeArgs(t, []string{"--settings", path})
	if len(commands) != len(HookEvents) {
		t.Fatalf("settings configure %d events, want %d", len(commands), len(HookEvents))
	}
	for _, event := range HookEvents {
		want := HookCommand(params.RtkPath, params.DisplayName, event, params.SessionDir)
		if commands[event] != want {
			t.Fatalf("event %s command =\n%q\nwant\n%q", event, commands[event], want)
		}
	}

	// And the timeout is the literal 10 for every event.
	var document struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for event, matchers := range document.Hooks {
		command := matchers[0].Hooks[0]
		if command.Type != "command" {
			t.Fatalf("event %s hook type = %q, want command", event, command.Type)
		}
		if command.Timeout != hookTimeoutSeconds {
			t.Fatalf("event %s timeout = %d, want %d", event, command.Timeout, hookTimeoutSeconds)
		}
	}
}

func TestCodexSpecArgv(t *testing.T) {
	params := baseParams(t, harnessCodex)
	params.RtkPath = "/opt/rtk"
	params.SessionDir = "/run/session"
	params.ServerAddr = "127.0.0.1:4100"

	spec, err := Build(params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if spec.SessionID != "" {
		t.Fatalf("Codex SessionID = %q, want empty: no session flag was found", spec.SessionID)
	}

	want := []string{
		params.Binary,
		"-c", `mcp_servers.ratatoskr.url="http://127.0.0.1:4100/mcp"`,
		"-c", `mcp_servers.ratatoskr.bearer_token_env_var="RATATOSKR_TOKEN"`,
		"-c", `mcp_servers.ratatoskr.tools.send_message.approval_mode="approve"`,
	}
	for _, event := range HookEvents {
		want = append(want, "-c", `hooks.`+event+
			`=[{hooks=[{type="command",command="/opt/rtk hook --agent alpha --event `+event+
			` --session-dir /run/session",timeout=10}]}]`)
	}

	if len(spec.Argv) != len(want) {
		t.Fatalf("argv has %d elements, want %d:\n%v", len(spec.Argv), len(want), spec.Argv)
	}
	for i := range want {
		if spec.Argv[i] != want[i] {
			t.Fatalf("argv[%d] =\n%q\nwant\n%q", i, spec.Argv[i], want[i])
		}
	}
}

// TestCodexSpecAddsNoDangerousFlags: a harness is launched as the user
// configured it. Nothing here widens a sandbox or approval.
func TestCodexSpecAddsNoDangerousFlags(t *testing.T) {
	params := baseParams(t, harnessCodex)
	spec, err := Build(params)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	forbidden := []string{"--dangerously", "--yolo", "--full-auto", "--ask-for-approval", "sandbox_mode", "approval_policy"}
	for _, arg := range spec.Argv {
		for _, banned := range forbidden {
			if strings.Contains(arg, banned) {
				t.Fatalf("argv carries %q: %v", arg, spec.Argv)
			}
		}
	}
}

// TestCodexSpecWritesNothing: Codex's configuration is entirely on the command
// line, so nothing may appear in the session directory.
func TestCodexSpecWritesNothing(t *testing.T) {
	params := baseParams(t, harnessCodex)
	if err := os.MkdirAll(params.SessionDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := Build(params); err != nil {
		t.Fatalf("Build: %v", err)
	}

	entries, err := os.ReadDir(params.SessionDir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("Codex build wrote %v into the session directory", names)
	}
}

// TestBuildWritesOnlyInsideSessionDir is invariant 5: never ~/.claude, never
// ~/.codex, never the working directory.
func TestBuildWritesOnlyInsideSessionDir(t *testing.T) {
	params := baseParams(t, harnessClaude)
	workDir := params.Dir
	// Keep the session directory outside the working directory, so the test can
	// tell "Build wrote next to the harness" from "Build wrote in its own place".
	sessionDir := filepath.Join(t.TempDir(), "session")
	params.SessionDir = sessionDir

	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	before := directoryNames(t, workDir)

	if _, err := Build(params); err != nil {
		t.Fatalf("Build: %v", err)
	}

	after := directoryNames(t, workDir)
	if strings.Join(before, ",") != strings.Join(after, ",") {
		t.Fatalf("the working directory changed: %v -> %v", before, after)
	}

	written := directoryNames(t, sessionDir)
	want := []string{claudeMCPFileName, claudeSettingsFileName}
	if strings.Join(written, ",") != strings.Join(want, ",") {
		t.Fatalf("session directory holds %v, want %v", written, want)
	}
}

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
