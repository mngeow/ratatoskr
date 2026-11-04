package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// These tests are written but UNRUN: GR-3 forbids running them. Nothing here
// executes a harness: unknown names return before any lookup, and the rest is
// pure string handling.

func TestVersionRejectsUnknownHarness(t *testing.T) {
	for _, harness := range []string{"", "vim", "claude-coder", "Claude", "codex2"} {
		if got := Version(context.Background(), harness); got != "" {
			t.Fatalf("Version(%q) = %q, want empty", harness, got)
		}
	}
}

func TestFirstNonEmptyLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace only", in: " \n\t\n ", want: ""},
		{name: "single line", in: "2.1.311 (Claude Code)\n", want: "2.1.311 (Claude Code)"},
		{name: "leading blank lines", in: "\n\n  codex 0.166.0  \nsecond\n", want: "codex 0.166.0"},
		{name: "no trailing newline", in: "1.0.0", want: "1.0.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstNonEmptyLine(tc.in); got != tc.want {
				t.Fatalf("firstNonEmptyLine(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTruncateVersion(t *testing.T) {
	short := "1.2.3"
	if got := truncateVersion(short); got != short {
		t.Fatalf("truncateVersion(%q) = %q, want it unchanged", short, got)
	}

	exact := strings.Repeat("a", versionMaxBytes)
	if got := truncateVersion(exact); got != exact {
		t.Fatalf("truncateVersion changed a string of exactly the limit: %q", got)
	}

	long := strings.Repeat("a", versionMaxBytes+10)
	if got := truncateVersion(long); len(got) != versionMaxBytes {
		t.Fatalf("truncated length = %d, want %d", len(got), versionMaxBytes)
	}

	// A multi-byte rune straddling the limit is dropped whole, not halved.
	straddling := strings.Repeat("a", versionMaxBytes-1) + "é" + strings.Repeat("b", 5)
	got := truncateVersion(straddling)
	if len(got) > versionMaxBytes {
		t.Fatalf("truncated to %d bytes, want at most %d", len(got), versionMaxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncation split a character: %q", got)
	}
	want := strings.Repeat("a", versionMaxBytes-1)
	if got != want {
		t.Fatalf("truncated value = %q, want %d 'a's kept whole", got, versionMaxBytes-1)
	}
}
