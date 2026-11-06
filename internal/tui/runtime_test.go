package tui

// Authored but UNRUN under GR-3. Stubs only; no harness or subprocess.
import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/mngeow/ratatoskr/internal/pane"
)

func TestParseRuntimeAgents(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{"", []string{"claude", "codex"}},
		{"  ", []string{"claude", "codex"}},
		{"codex", []string{"codex"}},
		{" codex , claude ", []string{"codex", "claude"}},
	} {
		got, err := ParseAgents(tc.input)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%q: %v, %v", tc.input, got, err)
		}
	}
	for _, input := range []string{"claude,", ",codex", "claude,claude", "Claude", "other"} {
		if _, err := ParseAgents(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestSinglePaneGeometryAndInactiveInput(t *testing.T) {
	p := &fakePane{}
	slots := []Slot{{ID: "codex", Name: "Codex", Pane: p}}
	if _, err := New(&fakeRouter{}, nil, slots); err == nil {
		t.Fatal("local accepted one slot")
	}
	m, err := NewRuntime(&fakeRouter{}, nil, slots)
	if err != nil {
		t.Fatal(err)
	}
	m.resize(81, 24)
	if got := p.resizes; len(got) != 1 || got[0] != [2]int{81, 17} {
		t.Fatalf("resize: %v", got)
	}
	for _, x := range []int{0, 40, 80} {
		got := m.mouseTargetAt(x, screenTop())
		if got.pane != 0 || !got.body || got.x != x {
			t.Fatalf("cell %d: %+v", x, got)
		}
	}
	m.frames[0] = pane.Frame{X: 80, Y: 16}
	if c := m.cursor(); c == nil || c.X != 80 || c.Y != screenTop()+16 {
		t.Fatalf("cursor: %+v", c)
	}
	// Even an accidentally populated tail must never accept work.
	tail := &fakePane{}
	m.slots[1] = Slot{ID: "inactive", Pane: tail}
	if cmd := m.enqueueInputFor(1, inputAction{}, false); cmd != nil {
		t.Fatal("inactive input accepted")
	}
	m.View()
	if tail.snapshots != 0 {
		t.Fatal("inactive pane sampled")
	}
	m.logMode = true
	m.key(key(tea.KeyF2))
	if !m.logMode || m.focus != 0 {
		t.Fatal("F2 changed single-pane log")
	}
	if strings.Contains(m.historyHints(), "F2") {
		t.Fatal("inactive key advertised")
	}
}

func TestRuntimeTwoPaneMatchesLocalGeometry(t *testing.T) {
	local, r, a, b := newModel(t)
	runtime, err := NewRuntime(r, nil, []Slot{{ID: "a", Name: "Claude", Pane: a}, {ID: "b", Name: "Codex", Pane: b}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.resize(local.width, local.height)
	if !reflect.DeepEqual(local.boxes(), runtime.boxes()) || local.headerLine() != runtime.headerLine() || local.hintsLine() != runtime.hintsLine() {
		t.Fatal("two-pane local/runtime layouts differ")
	}
}
