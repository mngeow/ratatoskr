package windowclient

// Authored but UNRUN under GR-3. Request mux only, no socket/daemon.
import (
	"context"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func TestNetworkBoundsAndCancellationBeforeRequest(t *testing.T) {
	c, _ := fixtureClient()
	for _, req := range []wire.NetworkRequest{{WaitMS: -1}, {WaitMS: 8001}, {FirstWaitMS: 8001}, {WaitMS: 1, FirstWaitMS: 1}} {
		if _, _, err := c.Network(context.Background(), req); err == nil {
			t.Fatal("invalid wait accepted", req)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Network(ctx, wire.NetworkRequest{}); err == nil {
		t.Fatal("cancel ignored")
	}
	if len(c.out) != 0 || c.Err() != nil {
		t.Fatal("cancel/validation wrote or disconnected")
	}
}

func TestNetworkReturnsBodyAndDaemonVersion(t *testing.T) {
	c, _ := fixtureClient()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		view, version, err := c.Network(ctx, wire.NetworkRequest{Since: 4, WaitMS: 8000})
		if err == nil && (view.Schema != 1 || version != 9) {
			err = context.Canceled
		}
		done <- err
	}()
	frame := <-c.out
	q := frame.body.(wire.Query)
	if q.Kind != wire.QueryNetwork || q.Network == nil || q.Network.Since != 4 {
		t.Fatal("wrong query", q)
	}
	c.deliver(frame.id, wire.TypeResult, wire.Result{Network: &protocol.NetworkView{Schema: 1}, NetworkVersion: 9}, nil)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
