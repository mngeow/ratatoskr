package windowclient

// Authored but UNRUN under GR-3. Request mux only; no socket, daemon or SSH.
import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
)

func TestRefusalPreservesMessageAndCodeThroughWrap(t *testing.T) {
	err := fmt.Errorf("windowclient: the daemon refused the attach: %w", &RefusalError{Code: wire.CodeNotRunning, Message: "ended"})
	var coded interface{ RefusalCode() wire.Code }
	if err.Error() != "windowclient: the daemon refused the attach: ended" || !errors.As(err, &coded) || coded.RefusalCode() != wire.CodeNotRunning {
		t.Fatal(err)
	}
}

func TestEnterQueryCopiesPlanAndRefusesMissingBody(t *testing.T) {
	for _, missing := range []bool{false, true} {
		c, _ := fixtureClient()
		plan := &wire.EnterPlan{Kind: wire.EnterSSH, Argv: []string{"ssh"}}
		if missing {
			plan = nil
		}
		done := make(chan error, 1)
		go func() {
			got, err := c.Enter(context.Background(), wire.EnterRequest{LocalID: "local"})
			if missing {
				if err == nil {
					done <- errors.New("missing plan accepted")
					return
				}
			} else {
				if err != nil {
					done <- err
					return
				}
				got.Argv[0] = "caller"
				if plan.Argv[0] != "ssh" {
					done <- errors.New("aliased plan")
					return
				}
			}
			done <- nil
		}()
		frame := <-c.out
		q := frame.body.(wire.Query)
		if q.Kind != wire.QueryEnter || q.Enter.LocalID != "local" {
			t.Fatal(q)
		}
		c.deliver(frame.id, wire.TypeResult, wire.Result{Plan: plan}, nil)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
