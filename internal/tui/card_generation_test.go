package tui

// Authored UNRUN under GR-3. Commands below call fakes, never a model or daemon.
import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/windowclient"
)

func cardFixture() (*Model, *[]wire.CardOp) {
	ops := []wire.CardOp{}
	m := &Model{now: time.Now}
	m.graph.selected = graphLocalKey
	m.daemon = &daemonWindow{cardOp: func(_ context.Context, op wire.CardOp) (wire.Result, error) {
		ops = append(ops, op)
		return wire.Result{Draft: &protocol.CardDraft{ID: "draft", State: protocol.CardDraftRunning}}, nil
	}}
	return m, &ops
}

func TestCardExplicitRunAndAcceptOnly(t *testing.T) {
	m, ops := cardFixture()
	c := &m.graph.card
	c.open = true
	c.draft = &protocol.CardDraft{ID: "draft", State: protocol.CardDraftPrepared, Binary: "/bin/model", Argv: []string{"model"}, Payload: "payload", PayloadBytes: 7}
	if m.cardGenerationKey(key(tea.KeyEnter)) != nil || len(*ops) != 0 {
		t.Fatal("Enter ran the model")
	}
	cmd := m.cardGenerationKey(tea.KeyPressMsg{Code: 'Y', Mod: tea.ModCapsLock | tea.ModShift})
	if cmd == nil {
		t.Fatal("explicit Yes ignored")
	}
	m.applyCard(cmd().(cardMsg))
	if len(*ops) != 1 || (*ops)[0].Op != wire.CardOpDraftRun || (*ops)[0].DraftID != "draft" {
		t.Fatal(*ops)
	}
	c.draft.State, c.draft.BaseToken = protocol.CardDraftReady, "original-file-token"
	if m.cardGenerationKey(key(tea.KeyEnter)) != nil {
		t.Fatal("Enter accepted")
	}
	cmd = m.cardGenerationKey(key('a'))
	if cmd == nil {
		t.Fatal("explicit Accept ignored")
	}
	cmd()
	if len(*ops) != 2 || (*ops)[1].Op != wire.CardOpDraftAccept || (*ops)[1].BaseToken != "original-file-token" {
		t.Fatal(*ops)
	}
}

func TestCardConflictPreservesReviewAndBlocksAccept(t *testing.T) {
	m, ops := cardFixture()
	c := &m.graph.card
	c.generation = 3
	c.draft = &protocol.CardDraft{ID: "draft", State: protocol.CardDraftReady, BaseToken: "old"}
	v := protocol.CardFileView{Schema: protocol.CardFileViewSchema, Token: "new"}
	m.applyCard(cardMsg{generation: 3, op: wire.CardOpDraftAccept, id: "draft", result: wire.Result{Card: &v}, err: &windowclient.CardOpError{Code: wire.CodeCardConflict, Message: "file changed"}})
	if !c.blocked || c.draft == nil || !c.draft.Stale || c.view.Token != "new" {
		t.Fatal("conflict lost current view/review or enabled retry")
	}
	if m.cardOperation(wire.CardOpDraftAccept) != nil || len(*ops) != 0 {
		t.Fatal("accept retried")
	}
	m.applyCard(cardMsg{generation: 2, result: wire.Result{Card: &protocol.CardFileView{Schema: protocol.CardFileViewSchema, Token: "stale"}}})
	if c.view.Token != "new" {
		t.Fatal("stale response installed")
	}
}

func TestCardListenerAndFileValidityStaySeparate(t *testing.T) {
	v := protocol.CardFileView{Path: "/owner/card.yaml", File: protocol.CardFileStatus{State: protocol.CardFileInvalid, Line: 4, Message: "invalid field"}, Served: &protocol.CardServed{}, Listener: protocol.CardListener{State: protocol.CardListenerNotBound, Reason: "port in use", URL: "must not show"}}
	text := strings.Join(cardFileStatusLines(&v), "\n")
	for _, want := range []string{"! not bound: port in use", "file: invalid", "line 4: invalid field", "last good card retained", "/owner/card.yaml"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	if strings.Contains(text, "must not show") || strings.Contains(text, protocol.CardReadableNotice) {
		t.Fatal("non-listening endpoint advertised")
	}
	v.Listener = protocol.CardListener{State: protocol.CardListenerListening, Addr: "0.0.0.0:7778", URL: "http://localhost:7778/card"}
	text = strings.Join(cardFileStatusLines(&v), "\n")
	if !strings.Contains(text, protocol.CardReadableNotice) || !strings.Contains(text, "file: invalid") {
		t.Fatal("listener hid invalid file")
	}
}

func TestCardPreparedBytesEscapesAndRewriteReview(t *testing.T) {
	d := &protocol.CardDraft{State: protocol.CardDraftPrepared, Argv: []string{"model", "a b", "\u202e"}, Payload: "one\t\x1b\xff\n", PayloadBytes: 8, Caveats: []string{"may load owner hooks and MCP"}}
	text := strings.Join(cardDraftReviewLines(d), "\n")
	for _, want := range []string{`["model","a b","\u202e"]`, `\t`, `\x1b`, `\xff`, "| ", "may load owner hooks and MCP"} {
		if !strings.Contains(text, want) {
			t.Fatal(want, text)
		}
	}
	if strings.ContainsRune(text, '\x1b') || strings.ContainsRune(text, '\u202e') {
		t.Fatal("control reached terminal")
	}
	d.State = protocol.CardDraftReady
	d.Diff = []protocol.CardDiffEntry{{Path: "description", Old: "old", New: "new", Generated: true}}
	d.RewriteNotice = "Accept rewrites the file; comments are not kept"
	text = strings.Join(cardDraftReviewLines(d), "\n")
	if !strings.Contains(text, "(generated)") || !strings.Contains(text, "card.yaml.bak") || !strings.Contains(text, "comments are not kept") {
		t.Fatal(text)
	}
}
