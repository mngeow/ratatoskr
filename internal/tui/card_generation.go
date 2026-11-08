package tui

// UNTESTED under GR-3. Only explicit Y runs a prepared model; only A accepts.
import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"errors"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/windowclient"
	"strings"
	"time"
	"unicode/utf8"
)

type cardState struct {
	view                         protocol.CardFileView
	have, pending, open, blocked bool
	draft                        *protocol.CardDraft
	generation                   uint64
	problem                      string
	last                         time.Time
	scroll                       int
}
type cardMsg struct {
	generation uint64
	op, id     string
	result     wire.Result
	err        error
}

func (m *Model) localCardSummary() string {
	c := &m.graph.card
	if !c.have {
		return "? listener unavailable; file status unavailable"
	}
	l := c.view.Listener
	text := "? listener " + cleanUISpan(l.State)
	switch l.State {
	case protocol.CardListenerListening:
		text = "+ listening " + cleanUISpan(l.Addr)
	case protocol.CardListenerOff:
		text = "- off"
	case protocol.CardListenerNotBound:
		text = "! not bound: " + cleanUISpan(l.Reason)
	}
	return text + "; file " + cardFileStateText(c.view.File.State)
}

func (m *Model) loadCard() tea.Cmd { return m.cardOperation(wire.CardOpGet) }
func (m *Model) cardOperation(op string) tea.Cmd {
	c := &m.graph.card
	if c.pending || m.daemon == nil || m.daemon.cardOp == nil {
		return nil
	}
	req := wire.CardOp{Op: op}
	if c.draft != nil {
		req.DraftID = c.draft.ID
	}
	switch op {
	case wire.CardOpDraftRun:
		if c.blocked || c.draft == nil || c.draft.State != protocol.CardDraftPrepared || c.draft.Stale {
			return nil
		}
		if len(c.draft.Argv) == 0 || c.draft.Binary == "" || !utf8.ValidString(c.draft.Payload) || c.draft.PayloadBytes != len(c.draft.Payload) {
			c.problem = "Prepared command/payload is incomplete or inconsistent; prepare again. Nothing ran."
			return nil
		}
		for _, arg := range append([]string{c.draft.Binary}, c.draft.Argv...) {
			if !utf8.ValidString(arg) {
				c.problem = "Prepared command has invalid text; prepare again. Nothing ran."
				return nil
			}
		}
	case wire.CardOpDraftAccept:
		if c.blocked || c.draft == nil || c.draft.State != protocol.CardDraftReady || c.draft.Stale {
			return nil
		}
		req.BaseToken = c.draft.BaseToken
	case wire.CardOpDraftStatus, wire.CardOpDraftCancel:
		if req.DraftID == "" {
			return nil
		}
	}
	c.pending = true
	c.generation++
	gen := c.generation
	call := m.daemon.cardOp
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), networkRequestBudget)
		defer cancel()
		r, err := call(ctx, req)
		return cardMsg{gen, op, req.DraftID, r, err}
	}
}
func (m *Model) applyCard(msg cardMsg) {
	c := &m.graph.card
	if msg.generation != c.generation {
		return
	}
	c.pending = false
	c.last = m.now()
	if msg.id != "" && (c.draft == nil || c.draft.ID != msg.id) {
		return
	}
	if msg.result.Card != nil {
		if msg.result.Card.Schema != protocol.CardFileViewSchema {
			c.problem = "local card schema unsupported"
			return
		}
		data, _ := json.Marshal(msg.result.Card)
		var v protocol.CardFileView
		if err := json.Unmarshal(data, &v); err != nil {
			c.problem = "invalid card status"
			return
		}
		c.view = v
		c.have = true
		if c.draft != nil && c.draft.BaseToken != "" && c.draft.BaseToken != v.Token {
			c.draft.Stale = true
		}
	}
	if msg.err != nil {
		c.problem = cleanUISpan(msg.err.Error())
		var e *windowclient.CardOpError
		if errors.As(msg.err, &e) && (e.Uncertain || e.Code == wire.CodeCardConflict) {
			c.blocked = true
		}
		if msg.op == wire.CardOpDraftPrepare {
			c.blocked = true
		}
		return
	}
	c.problem = ""
	if msg.result.Draft != nil {
		d := msg.result.Draft
		if d.ID == "" || (msg.id != "" && d.ID != msg.id) {
			c.problem = "draft answer identity mismatch"
			c.blocked = true
			return
		}
		data, _ := json.Marshal(d)
		var copy protocol.CardDraft
		if err := json.Unmarshal(data, &copy); err != nil {
			c.problem = "invalid draft answer"
			return
		}
		c.draft = &copy
		if msg.op == wire.CardOpDraftPrepare {
			c.blocked = false
			c.scroll = 0
		}
	}
	if msg.op == wire.CardOpDraftAccept {
		c.draft = nil
		c.open = false
		c.blocked = false
		m.feedback = "card.yaml accepted; backup written"
	}
}
func (m *Model) cardTick() tea.Cmd {
	c := &m.graph.card
	if c.pending {
		return nil
	}
	if c.draft != nil && c.draft.State == protocol.CardDraftRunning {
		if c.blocked || m.now().Sub(c.last) < time.Second {
			return nil
		}
		return m.cardOperation(wire.CardOpDraftStatus)
	}
	if m.now().Sub(c.last) >= 2*time.Second {
		return m.loadCard()
	}
	return nil
}
func (m *Model) cardGenerationKey(k tea.KeyPressMsg) tea.Cmd {
	c := &m.graph.card
	if isKey(k, tea.KeyEsc) {
		c.open = false
		return nil
	}
	if isKey(k, tea.KeyPgDown) || isKey(k, tea.KeyDown) {
		c.scroll += max(1, m.screenRows()-3)
		return nil
	}
	if isKey(k, tea.KeyPgUp) || isKey(k, tea.KeyUp) {
		c.scroll = max(0, c.scroll-max(1, m.screenRows()-3))
		return nil
	}
	if k.Mod&^(tea.ModShift|tea.ModCapsLock|tea.ModNumLock|tea.ModScrollLock) != 0 {
		return nil
	}
	switch strings.ToLower(string(k.Code)) {
	case "y":
		return m.cardOperation(wire.CardOpDraftRun)
	case "a":
		return m.cardOperation(wire.CardOpDraftAccept)
	case "d", "x":
		return m.cardOperation(wire.CardOpDraftCancel)
	case "g":
		return m.cardOperation(wire.CardOpDraftPrepare)
	case "r":
		return m.cardOperation(wire.CardOpReload)
	}
	return nil
}
func (m *Model) cardGenerationLines(width, rows int) []string {
	c := &m.graph.card
	lines := []string{"Generate card draft"}
	if c.pending {
		lines = append(lines, "Request in progress")
	}
	if c.problem != "" {
		lines = append(lines, "! "+c.problem)
	}
	if c.blocked {
		lines = append(lines, "Outcome unknown or file changed. Do not retry Accept. Reload and prepare a new draft; closing this window cancels/discards its draft.")
	}
	lines = append(lines, cardDraftReviewLines(c.draft)...)
	return m.graphTextAt(lines, width, rows, c.scroll)
}
