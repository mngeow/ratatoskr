package attach

// Untested: under GR-3 nothing here has been run. Only pure functions are tested: no socket, no
// daemon, no program.

import (
	"strings"
	"testing"
)

// TestEstimatePaneSize: one pane is full width, two are halved, and nothing goes below one cell.
func TestEstimatePaneSize(t *testing.T) {
	tests := []struct {
		name         string
		cols, rows   int
		panes        int
		wantW, wantH int
	}{
		{name: "one pane", cols: 120, rows: 40, panes: 1, wantW: 120, wantH: 33},
		{name: "two panes", cols: 120, rows: 40, panes: 2, wantW: 60, wantH: 33},
		{name: "odd width halves down", cols: 121, rows: 40, panes: 2, wantW: 60, wantH: 33},
		{name: "exactly the chrome", cols: 80, rows: 7, panes: 1, wantW: 80, wantH: 1},
		{name: "smaller than the chrome", cols: 80, rows: 4, panes: 1, wantW: 80, wantH: 1},
		{name: "one column", cols: 1, rows: 20, panes: 2, wantW: 1, wantH: 13},
		{name: "no size at all", cols: 0, rows: 0, panes: 2, wantW: 1, wantH: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			width, height := estimatePaneSize(tc.cols, tc.rows, tc.panes)
			if width != tc.wantW || height != tc.wantH {
				t.Fatalf("estimatePaneSize(%d, %d, %d) = %dx%d, want %dx%d",
					tc.cols, tc.rows, tc.panes, width, height, tc.wantW, tc.wantH)
			}
		})
	}
}

// TestStartNames: an empty flag starts nothing; a list is validated by the tui parser.
func TestStartNames(t *testing.T) {
	t.Run("an empty flag starts nothing", func(t *testing.T) {
		names, err := startNames("")
		if err != nil {
			t.Fatalf("startNames: %v", err)
		}
		if names != nil {
			t.Fatalf("names = %v, want nil", names)
		}
	})

	t.Run("whitespace only starts nothing", func(t *testing.T) {
		names, err := startNames("   ")
		if err != nil {
			t.Fatalf("startNames: %v", err)
		}
		if names != nil {
			t.Fatalf("names = %v, want nil", names)
		}
	})

	t.Run("both agents", func(t *testing.T) {
		names, err := startNames("claude,codex")
		if err != nil {
			t.Fatalf("startNames: %v", err)
		}
		if len(names) != 2 || names[0] != "claude" || names[1] != "codex" {
			t.Fatalf("names = %v, want claude then codex", names)
		}
	})

	t.Run("one agent", func(t *testing.T) {
		names, err := startNames("codex")
		if err != nil {
			t.Fatalf("startNames: %v", err)
		}
		if len(names) != 1 || names[0] != "codex" {
			t.Fatalf("names = %v", names)
		}
	})

	t.Run("an unknown agent is refused", func(t *testing.T) {
		if _, err := startNames("gemini"); err == nil {
			t.Fatal("startNames accepted an agent this build cannot start")
		}
	})

	t.Run("a duplicate is refused", func(t *testing.T) {
		if _, err := startNames("claude,claude"); err == nil {
			t.Fatal("startNames accepted a duplicate")
		}
	})
}

// TestDetachMessage: the three sentences, and which of them is an error.
func TestDetachMessage(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		reason   string
		wantText string
		wantErr  bool
	}{
		{
			name:     "a signal",
			kind:     endingSignal,
			wantText: "detached; the agents keep running (rtk attach returns to them)",
		},
		{
			name:     "an intentional detach",
			kind:     endingDetach,
			wantText: "detached; the agents keep running (rtk attach returns to them, rtk daemon stop ends them)",
		},
		{
			name:     "a lost daemon",
			kind:     endingLost,
			reason:   "the daemon connection closed",
			wantText: "the daemon connection closed; sessions may still be running; run rtk attach",
			wantErr:  true,
		},
		{
			name:     "a lost daemon with no reason",
			kind:     endingLost,
			wantText: "window disconnected: the daemon connection closed; sessions may still be running; run rtk attach",
			wantErr:  true,
		},
		{
			// The window's own error already carries the guidance: it is not said twice.
			name:     "a reason that already carries the guidance",
			kind:     endingLost,
			reason:   "window disconnected: the daemon closed the connection; sessions may still be running; run rtk attach",
			wantText: "window disconnected: the daemon closed the connection; sessions may still be running; run rtk attach",
			wantErr:  true,
		},
		{
			name:     "an unknown kind says nothing",
			kind:     "nonsense",
			wantText: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text, isError := detachMessage(tc.kind, tc.reason)
			if text != tc.wantText {
				t.Fatalf("text = %q, want %q", text, tc.wantText)
			}
			if isError != tc.wantErr {
				t.Fatalf("isError = %t, want %t", isError, tc.wantErr)
			}
		})
	}
}

// TestZeroSlotText: the guidance and one start-error line, exactly as a user reads them.
func TestZeroSlotText(t *testing.T) {
	want := "no sessions are running: start some with: rtk attach --agents claude,codex"
	if noSessionsGuidance != want {
		t.Fatalf("guidance = %q, want %q", noSessionsGuidance, want)
	}
	if strings.HasPrefix(noSessionsGuidance, "rtk: ") {
		t.Fatal("the guidance carries its own rtk: prefix, which the caller adds")
	}

	line := startErrorLine("claude", "the gateway did not start the session")
	if line != "rtk: could not start claude: the gateway did not start the session" {
		t.Fatalf("startErrorLine = %q", line)
	}

	// A control sequence from the daemon cannot reach the terminal.
	dirty := startErrorLine("claude", "bad\x1b[31mthing")
	if strings.ContainsRune(dirty, 0x1b) {
		t.Fatalf("startErrorLine kept a control character: %q", dirty)
	}
}

// TestSanitize: control characters are stripped and the text is trimmed.
func TestSanitize(t *testing.T) {
	if got := sanitize("  hello\x00world\x07  "); got != "helloworld" {
		t.Fatalf("sanitize = %q", got)
	}
	if got := sanitize(""); got != "" {
		t.Fatalf("sanitize of empty = %q", got)
	}
}
