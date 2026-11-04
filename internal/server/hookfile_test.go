package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests are written but UNRUN: GR-3 forbids running them.

func TestWriteHookFileContentAndMode(t *testing.T) {
	dir := t.TempDir()

	if err := WriteHookFile(dir, "alpha", "127.0.0.1:4100", "deadbeef"); err != nil {
		t.Fatalf("WriteHookFile: %v", err)
	}

	path := filepath.Join(dir, "hook-alpha.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600", mode)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var file hookFile
	if err := json.Unmarshal(content, &file); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if file.URL != "http://127.0.0.1:4100" {
		t.Fatalf("url = %q, want http://127.0.0.1:4100", file.URL)
	}
	if file.Token != "deadbeef" {
		t.Fatalf("token = %q, want the token that was written", file.Token)
	}

	// A rewrite replaces the file rather than appending to it.
	if err := WriteHookFile(dir, "alpha", "127.0.0.1:4200", "cafebabe"); err != nil {
		t.Fatalf("second WriteHookFile: %v", err)
	}
	url, token, err := readHookFile(dir, "alpha")
	if err != nil {
		t.Fatalf("readHookFile: %v", err)
	}
	if url != "http://127.0.0.1:4200" || token != "cafebabe" {
		t.Fatalf("rewrite left %q / %q", url, token)
	}

	// No temporary files are left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("session directory holds %v, want only the hook file", names)
	}
}

func TestWriteHookFileRejectsBadInput(t *testing.T) {
	tests := []struct {
		name       string
		sessionDir string
		agentName  string
		addr       string
		token      string
	}{
		{name: "empty dir", sessionDir: "", agentName: "alpha", addr: "127.0.0.1:1", token: "t"},
		{name: "empty name", sessionDir: "dir", agentName: "", addr: "127.0.0.1:1", token: "t"},
		{name: "empty address", sessionDir: "dir", agentName: "alpha", addr: "", token: "t"},
		{name: "empty token", sessionDir: "dir", agentName: "alpha", addr: "127.0.0.1:1", token: ""},
		{name: "traversal", sessionDir: "dir", agentName: "../alpha", addr: "127.0.0.1:1", token: "t"},
		{name: "slash", sessionDir: "dir", agentName: "a/b", addr: "127.0.0.1:1", token: "t"},
		{name: "uppercase", sessionDir: "dir", agentName: "Alpha", addr: "127.0.0.1:1", token: "t"},
		{name: "too long", sessionDir: "dir", agentName: strings.Repeat("a", 33), addr: "127.0.0.1:1", token: "t"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := WriteHookFile(tc.sessionDir, tc.agentName, tc.addr, tc.token); err == nil {
				t.Fatal("WriteHookFile accepted invalid input")
			}
		})
	}
}

func TestAgentNamePattern(t *testing.T) {
	valid := []string{"a", "alpha", "alpha-beta", "alpha_beta", "a1", strings.Repeat("a", 32)}
	invalid := []string{"", strings.Repeat("a", 33), "Alpha", "alpha/beta", "alpha.json", " alpha", "alpha "}

	for _, name := range valid {
		if !agentNamePattern.MatchString(name) {
			t.Fatalf("%q should be a valid agent name", name)
		}
	}
	for _, name := range invalid {
		if agentNamePattern.MatchString(name) {
			t.Fatalf("%q should not be a valid agent name", name)
		}
	}
}

func TestReadHookFileErrors(t *testing.T) {
	dir := t.TempDir()

	if _, _, err := readHookFile(dir, "alpha"); err == nil {
		t.Fatal("readHookFile accepted a missing file")
	}
	if _, _, err := readHookFile(dir, "../alpha"); err == nil {
		t.Fatal("readHookFile accepted a traversal name")
	}

	path := filepath.Join(dir, "hook-broken.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := readHookFile(dir, "broken"); err == nil {
		t.Fatal("readHookFile accepted unparseable JSON")
	}
}
