package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// These tests are written but UNRUN: GR-3 forbids running them. The fake harness
// binary they point at is never executed: Build only records its path.

// fakeBinary writes an executable file and returns its absolute path.
func fakeBinary(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

// baseParams is a complete, valid set of parameters for one harness.
func baseParams(t *testing.T, harness string) Params {
	t.Helper()

	dir := t.TempDir()
	return Params{
		ID:          protocol.AgentID("alpha"),
		DisplayName: "alpha",
		Harness:     harness,
		Binary:      fakeBinary(t),
		Dir:         dir,
		RtkPath:     "/opt/rtk/bin/rtk",
		SessionDir:  filepath.Join(dir, "session"),
		ServerAddr:  "127.0.0.1:4100",
		Token:       "token-abc",
		BaseEnv: []string{
			"PATH=/usr/bin:/bin",
			"HOME=/home/user",
			"TERMINFO=/usr/share/terminfo",
			"LANG=en_GB.UTF-8",
			"TERM=xterm-kitty",
			"COLORTERM=truecolor-x",
			"COLUMNS=120",
			"LINES=40",
			"TMUX=/tmp/tmux-501/default,123,0",
			"TMUX_PANE=%3",
			"OPENRIG_SESSION=build-monkey",
			"RIG_NAME=team-ratatoskr",
			"RATATOSKR_TOKEN=leaked",
			"TERM_PROGRAM=iTerm.app",
			"TERM_PROGRAM_VERSION=3.5.0",
			"TERM_SESSION_ID=w0t0p0",
			"KITTY_WINDOW_ID=1",
			"WEZTERM_PANE=2",
			"ITERM_SESSION_ID=x",
			"VTE_VERSION=7000",
			"WINDOWID=42",
			"ALACRITTY_LOG=/tmp/a",
			"WT_SESSION=abc",
			"WT_PROFILE_ID={123}",
		},
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Params)
	}{
		{name: "empty ID", mutate: func(p *Params) { p.ID = "" }},
		{name: "empty name", mutate: func(p *Params) { p.DisplayName = "" }},
		{name: "empty dir", mutate: func(p *Params) { p.Dir = "" }},
		{name: "empty rtk path", mutate: func(p *Params) { p.RtkPath = "" }},
		{name: "empty session dir", mutate: func(p *Params) { p.SessionDir = "" }},
		{name: "empty server addr", mutate: func(p *Params) { p.ServerAddr = "" }},
		{name: "empty token", mutate: func(p *Params) { p.Token = "" }},
		{name: "uppercase name", mutate: func(p *Params) { p.DisplayName = "Alpha" }},
		{name: "name with slash", mutate: func(p *Params) { p.DisplayName = "a/b" }},
		{name: "name too long", mutate: func(p *Params) { p.DisplayName = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }},
		{name: "relative rtk path", mutate: func(p *Params) { p.RtkPath = "bin/rtk" }},
		{name: "relative session dir", mutate: func(p *Params) { p.SessionDir = "session" }},
		{name: "unknown harness", mutate: func(p *Params) { p.Harness = "cursor" }},
		{name: "empty harness", mutate: func(p *Params) { p.Harness = "" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := baseParams(t, harnessClaude)
			tc.mutate(&params)

			if _, err := Build(params); err == nil {
				t.Fatal("Build accepted invalid parameters")
			}
		})
	}
}

func TestBuildCopiesIdentityAndLeavesVersionEmpty(t *testing.T) {
	for _, harness := range []string{harnessClaude, harnessCodex} {
		t.Run(harness, func(t *testing.T) {
			params := baseParams(t, harness)
			spec, err := Build(params)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			if spec.ID != params.ID || spec.DisplayName != params.DisplayName {
				t.Fatalf("identity not copied: %+v", spec)
			}
			if spec.Harness != harness {
				t.Fatalf("harness = %q, want %q", spec.Harness, harness)
			}
			if spec.Dir != params.Dir {
				t.Fatalf("dir = %q, want %q", spec.Dir, params.Dir)
			}
			// App fills this from Version at startup; Build must never run a
			// harness to find out.
			if spec.HarnessVersion != "" {
				t.Fatalf("HarnessVersion = %q, want empty", spec.HarnessVersion)
			}
		})
	}
}

func TestResolveBinary(t *testing.T) {
	dir := t.TempDir()

	notExecutable := filepath.Join(dir, "claude")
	if err := os.WriteFile(notExecutable, []byte("data"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	tests := []struct {
		name    string
		binary  string
		harness string
		ok      bool
	}{
		{name: "explicit absolute executable", binary: fakeBinary(t), harness: harnessClaude, ok: true},
		{name: "explicit relative path", binary: "claude", harness: harnessClaude},
		{name: "explicit missing file", binary: filepath.Join(dir, "nope"), harness: harnessClaude},
		{name: "explicit not executable", binary: notExecutable, harness: harnessClaude},
		{name: "explicit directory", binary: dir, harness: harnessClaude},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveBinary(Params{Harness: tc.harness, Binary: tc.binary})
			if tc.ok {
				if err != nil {
					t.Fatalf("resolveBinary: %v", err)
				}
				if got != tc.binary {
					t.Fatalf("resolved %q, want %q", got, tc.binary)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveBinary accepted %q", tc.binary)
			}
		})
	}
}

// TestBuildEnvIsCompleteAndStripped is the environment contract: nothing from the
// outer terminal or the rig survives, PATH and locale do, and TERM/COLORTERM are
// ours.
func TestBuildEnvIsCompleteAndStripped(t *testing.T) {
	denied := []string{
		"TERM", "COLORTERM", "COLUMNS", "LINES", "VTE_VERSION", "WINDOWID", "TERM_SESSION_ID",
		"TMUX", "TMUX_PANE", "OPENRIG_SESSION", "RIG_NAME", "RATATOSKR_TOKEN",
		"TERM_PROGRAM", "TERM_PROGRAM_VERSION", "KITTY_WINDOW_ID", "WEZTERM_PANE",
		"ITERM_SESSION_ID", "ALACRITTY_LOG", "WT_SESSION", "WT_PROFILE_ID",
	}
	kept := []string{"PATH", "HOME", "TERMINFO", "LANG"}

	for _, harness := range []string{harnessClaude, harnessCodex} {
		t.Run(harness, func(t *testing.T) {
			params := baseParams(t, harness)
			spec, err := Build(params)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}

			for _, name := range denied {
				if value, ok := envValue(spec.Env, name); ok {
					t.Fatalf("%s survived into the child environment as %q", name, value)
				}
			}

			for _, name := range kept {
				if _, ok := envValue(spec.Env, name); !ok {
					t.Fatalf("%s was stripped; it must be kept", name)
				}
			}

			// TERM and COLORTERM come from exactly one place.
			want := childTerminalEnv()
			term, _ := envValue(spec.Env, "TERM")
			colorTerm, _ := envValue(spec.Env, "COLORTERM")
			if term+"|"+colorTerm != want[0]+"|"+want[1] {
				t.Fatalf("terminal identity = %q / %q, want %v", term, colorTerm, want)
			}

			token, hasToken := envValue(spec.Env, "RATATOSKR_TOKEN")
			if harness == harnessCodex {
				if !hasToken || token != params.Token {
					t.Fatalf("Codex token env = (%q, %v), want the agent's token", token, hasToken)
				}
			} else if hasToken {
				// Claude carries the token in its MCP config file instead.
				t.Fatalf("Claude environment carries RATATOSKR_TOKEN=%q", token)
			}
		})
	}
}

// envValue finds one variable in a complete environment.
func envValue(env []string, name string) (string, bool) {
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if found && key == name {
			return value, true
		}
	}
	return "", false
}
