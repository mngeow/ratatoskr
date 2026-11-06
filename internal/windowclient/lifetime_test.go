package windowclient

// Authored but UNRUN under GR-3.
//
// This one needs a real Unix socket on purpose: the mismatch it guards against (a caller giving
// Dial a bootstrap-only context and cancelling it right after) was invisible to every test that
// used an in-memory connection, because Dial only keeps the context alive through a goroutine
// started after the handshake. The fake daemon answers the hello and the attach and then holds
// the connection open.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// startFakeDaemon listens on a short temp socket path (a Unix path must stay under about 100
// bytes, and t.TempDir can be longer) and serves one window: hello, attach, then silence.
func startFakeDaemon(t *testing.T, build string) string {
	t.Helper()

	dir, err := os.MkdirTemp("/tmp", "rtk-wc-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")

	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		if _, err := wire.Read(conn); err != nil { // hello
			return
		}
		_ = wire.Write(conn, wire.TypeHelloAck, 0, wire.HelloAck{Build: build, PID: 1, WindowID: "w1"})
		if _, err := wire.Read(conn); err != nil { // attach
			return
		}
		_ = wire.Write(conn, wire.TypeAttached, 0, wire.Attached{
			WindowID: "w1",
			Slots: []wire.Slot{{
				Agent: "a1", Name: "claude", Address: "claude@mac",
				Pane: wire.PaneState{Agent: "a1", Rev: 1, Cols: 80, Rows: 24, InputReady: true, YouHold: true, Holder: "w1"},
			}},
			Snapshot: protocol.Snapshot{},
		})
		// Hold the connection open until the test ends: a healthy daemon says nothing to an
		// idle window.
		_, _ = wire.Read(conn)
	}()
	return path
}

// TestTheClientLivesAsLongAsTheContextGivenToDial: a window that attached stays alive when
// nothing has ended its context, and ends when that context ends. The context given to Dial is
// the window's lifetime, so a caller must not give it one it cancels after Dial returns.
func TestTheClientLivesAsLongAsTheContextGivenToDial(t *testing.T) {
	path := startFakeDaemon(t, "build")

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	defer cancelLifetime()

	client, err := Dial(lifetime, path, "build", wire.Attach{Dir: "/", Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Longer than any bootstrap-only budget a caller might have used for the connect.
	time.Sleep(150 * time.Millisecond)
	if err := client.Err(); err != nil {
		t.Fatalf("the client failed with its lifetime context still alive: %v", err)
	}

	cancelLifetime()
	deadline := time.After(2 * time.Second)
	for client.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("the client did not end when its lifetime context ended")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestACancelledBootstrapContextEndsTheClient pins the behaviour attach must not trigger: a
// context cancelled right after Dial returns fails the client at once.
func TestACancelledBootstrapContextEndsTheClient(t *testing.T) {
	path := startFakeDaemon(t, "build")

	bootstrap, cancelBootstrap := context.WithTimeout(context.Background(), 40*time.Second)
	client, err := Dial(bootstrap, path, "build", wire.Attach{Dir: "/", Width: 80, Height: 24})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	cancelBootstrap()
	deadline := time.After(2 * time.Second)
	for client.Err() == nil {
		select {
		case <-deadline:
			t.Fatal("cancelling Dial's context did not end the client: the lifetime rule changed")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
