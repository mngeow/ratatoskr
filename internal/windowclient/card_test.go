package windowclient

// Authored UNRUN under GR-3. Fake mux only; no socket, daemon or command runs.
import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func TestCardOpKeepsConflictViewAndRejectsWrongDraft(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		c, _ := fixtureClient()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		type answer struct {
			r wire.Result
			e error
		}
		done := make(chan answer, 1)
		go func() {
			r, e := c.CardOp(ctx, wire.CardOp{Op: wire.CardOpDraftAccept, DraftID: "draft", BaseToken: "before"})
			done <- answer{r, e}
		}()
		frame := <-c.out
		op := frame.body.(wire.CardOp)
		if op.DraftID != "draft" || op.BaseToken != "before" {
			t.Fatal(op)
		}
		r := wire.Result{Card: &protocol.CardFileView{Schema: protocol.CardFileViewSchema, Token: "after"}}
		if conflict {
			r.Error = &wire.Error{Code: wire.CodeCardConflict, Message: "file changed"}
		}
		c.deliver(frame.id, wire.TypeResult, r, nil)
		got := <-done
		cancel()
		if got.r.Card == nil || got.r.Card.Token != "after" {
			t.Fatal("current file view lost")
		}
		if conflict {
			var e *CardOpError
			if !errors.As(got.e, &e) || e.Code != wire.CodeCardConflict || e.Uncertain {
				t.Fatal(got.e)
			}
		} else if got.e != nil {
			t.Fatal(got.e)
		}
		if len(c.out) != 0 {
			t.Fatal("operation retried")
		}
	}
}

func TestCardRunAnswerMustMatchDraftID(t *testing.T) {
	c, _ := fixtureClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := c.CardOp(ctx, wire.CardOp{Op: wire.CardOpDraftRun, DraftID: "one"}); done <- e }()
	frame := <-c.out
	c.deliver(frame.id, wire.TypeResult, wire.Result{Draft: &protocol.CardDraft{ID: "other", State: protocol.CardDraftRunning}}, nil)
	var e *CardOpError
	if !errors.As(<-done, &e) || !e.Uncertain {
		t.Fatal("wrong draft accepted")
	}
}

func TestRemovedCardOpsNeverReachWire(t *testing.T) {
	c, _ := fixtureClient()
	for _, op := range []string{"edit", "publish", "history", "rollback", "rescan"} {
		if _, e := c.CardOp(context.Background(), wire.CardOp{Op: op}); e == nil {
			t.Fatal(op)
		}
	}
	if len(c.out) != 0 {
		t.Fatal("obsolete operation written")
	}
}
