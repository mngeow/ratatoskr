//go:build linux

package daemon

// Untested: under GR-3 nothing here has been run. This parser is Linux-only, so its file
// is too.

import "testing"

// TestParseStatPPID: the comm field may contain spaces and parentheses, so the parse starts
// after the LAST ')'.
func TestParseStatPPID(t *testing.T) {
	tests := []struct {
		name string
		line string
		want int
	}{
		{
			name: "a plain entry",
			line: "1234 (claude) S 1200 1234 1234 0 -1 4194304",
			want: 1200,
		},
		{
			name: "a comm with a space",
			line: "1234 (go test) S 1200 1234 1234 0 -1 4194304",
			want: 1200,
		},
		{
			name: "a comm with parentheses",
			line: "1234 (a (weird) name) S 1200 1234 1234 0 -1 4194304",
			want: 1200,
		},
		{
			name: "a comm ending in a parenthesis",
			line: "1234 (name)) R 7 1234 1234 0 -1 4194304",
			want: 7,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseStatPPID(tc.line)
			if err != nil {
				t.Fatalf("parseStatPPID: %v", err)
			}
			if got != tc.want {
				t.Fatalf("ppid = %d, want %d", got, tc.want)
			}
		})
	}

	bad := []string{
		"",
		"1234 no comm field",
		"1234 (claude)",
		"1234 (claude) S",
		"1234 (claude) S notanumber 1234",
		"1234 (claude) S -5 1234",
	}
	for _, line := range bad {
		t.Run("bad "+line, func(t *testing.T) {
			if _, err := parseStatPPID(line); err == nil {
				t.Fatalf("parseStatPPID accepted %q", line)
			}
		})
	}
}
