package windowclient

// Authored but UNRUN under GR-3. In-memory framing and cache/request stubs only;
// no Unix listener, daemon, terminal, harness or process is created.
import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

type memoryConn struct {
	in        bytes.Buffer
	out       bytes.Buffer
	deadlines []time.Time
	closed    bool
}

func (c *memoryConn) Read(b []byte) (int, error)     { return c.in.Read(b) }
func (c *memoryConn) Write(b []byte) (int, error)    { return c.out.Write(b) }
func (c *memoryConn) Close() error                   { c.closed = true; return nil }
func (*memoryConn) LocalAddr() net.Addr              { return nil }
func (*memoryConn) RemoteAddr() net.Addr             { return nil }
func (c *memoryConn) SetDeadline(d time.Time) error  { c.deadlines = append(c.deadlines, d); return nil }
func (*memoryConn) SetReadDeadline(time.Time) error  { return nil }
func (*memoryConn) SetWriteDeadline(time.Time) error { return nil }

func fixtureClient() (*Client, *Pane) {
	c := &Client{conn: &memoryConn{}, done: make(chan struct{}), channelsClosed: make(chan struct{}), out: make(chan outFrame, writeQueue),
		events: make(chan protocol.Event, eventQueue), windowEvents: make(chan WindowEvent, eventQueue),
		pending: make(map[uint64]*pendingCall), retired: make(map[uint64]struct{}), querySem: make(chan struct{}, queryLimit)}
	c.state = newStateCache(wire.Attached{})
	p := &Pane{client: c, agent: "a", resizeWake: make(chan struct{}, 1)}
	p.state.Store(&wire.PaneState{Agent: "a", Cols: 80, Rows: 24, InputReady: true, YouHold: true})
	c.panes = &paneStore{panes: map[protocol.AgentID]*Pane{"a": p}}
	return c, p
}

func TestHandshakeClearsBootstrapDeadline(t *testing.T) {
	conn := &memoryConn{}
	if err := wire.Write(&conn.in, wire.TypeHelloAck, 0, wire.HelloAck{Build: "build", WindowID: "w"}); err != nil {
		t.Fatal(err)
	}
	if err := wire.Write(&conn.in, wire.TypeAttached, 0, wire.Attached{WindowID: "w"}); err != nil {
		t.Fatal(err)
	}
	c := &Client{conn: conn}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.handshake(ctx, "unused", "build", wire.Attach{Width: 80, Height: 24}); err != nil {
		t.Fatal(err)
	}
	if len(conn.deadlines) < 3 || !conn.deadlines[len(conn.deadlines)-1].IsZero() {
		t.Fatal("bootstrap deadline leaked into lifetime")
	}
	if conn.deadlines[1].After(time.Now().Add(helloTimeout)) {
		t.Fatal("hello did not use shorter budget")
	}
}

func TestPaneRevisionEndedLatchAndCopies(t *testing.T) {
	_, p := fixtureClient()
	p.applyOwnership(wire.Ownership{Agent: "a", Rev: 10, Holder: "other"})
	p.apply(wire.PaneState{Agent: "a", Rev: 9, Holder: "old", YouHold: true})
	if p.State().YouHold || p.State().Holder != "other" {
		t.Fatal("stale frame stole ownership")
	}
	code := 7
	p.markEnded(2, &code)
	p.applyOwnership(wire.Ownership{Agent: "a", Rev: 20, YouHold: true})
	p.apply(wire.PaneState{Agent: "a", Rev: 21, YouHold: true})
	got := p.State()
	if !got.Ended || got.YouHold || got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("end lost: %+v", got)
	}
	*got.ExitCode = 99
	if *p.State().ExitCode != 7 {
		t.Fatal("State leaked mutable exit pointer")
	}
}

func TestRefusedDraftStopsFollowingKey(t *testing.T) {
	c, p := fixtureClient()
	cause := inputRefused{code: string(wire.CodeNotHolder), message: "lost holder"}
	p.latch(cause, true)
	if err := p.SendKey(uv.KeyPressEvent{Code: 'x', Text: "x"}); err == nil {
		t.Fatal("failed draft latch bypassed")
	}
	if len(c.out) != 0 {
		t.Fatal("bytes queued after refused draft")
	}
	if c.OperationError("a") == nil {
		t.Fatal("refusal not exposed")
	}
	p.clearRecoverableLatch()
	if p.OperationError() != nil {
		t.Fatal("takeover recovery latch stayed")
	}
	p.latch(inputRefused{code: string(wire.CodeInputFailed), written: 3}, true)
	p.clearRecoverableLatch()
	var written interface {
		BytesWritten() bool
		ByteCount() int
	}
	if !errors.As(p.OperationError(), &written) || !written.BytesWritten() || written.ByteCount() != 3 {
		t.Fatal("transport partial-write evidence lost")
	}
}

func TestRequestIDsAndAtomicRetirement(t *testing.T) {
	c, _ := fixtureClient()
	a, _ := c.register(1, wire.TypeAck)
	b, _ := c.register(2, wire.TypeResult)
	c.deliver(2, wire.TypeResult, wire.Result{}, nil)
	c.deliver(1, wire.TypeAck, wire.Ack{OK: true}, nil)
	if (<-a.reply).err != nil || (<-b.reply).err != nil {
		t.Fatal("out-of-order independent responses failed")
	}
	if _, err := c.register(3, wire.TypeResult); err != nil {
		t.Fatal(err)
	}
	c.retire(3)
	c.deliver(3, wire.TypeResult, wire.Result{}, nil)
	if c.Err() != nil || len(c.pending) != 0 {
		t.Fatal("late query reply failed window")
	}
}

func TestCachedStateBoundedAndIndependent(t *testing.T) {
	c, _ := fixtureClient()
	peers := []string{"claude@host"}
	c.state.install(protocol.Snapshot{Connection: &protocol.ConnectionView{Peers: peers}})
	peers[0] = "mutated"
	for i := 0; i < 240; i++ {
		c.state.merge(protocol.Event{Message: &protocol.MessageView{ID: string(rune(i + 1))}, Send: &protocol.SendView{ID: string(rune(i + 1))}})
	}
	snap := c.Snapshot()
	if len(snap.Messages) != 200 || len(snap.Sends) != 200 || snap.Connection.Peers[0] != "claude@host" {
		t.Fatal("cache bound/copy failed")
	}
	c.state.setPending(9)
	if c.PendingOnQuit() != 9 {
		t.Fatal("pending getter stale")
	}
}

func TestConcurrentOwnershipAndEndNeverRevives(t *testing.T) {
	_, p := fixtureClient()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(rev uint64) {
			defer wg.Done()
			p.applyOwnership(wire.Ownership{Agent: "a", Rev: rev, YouHold: true})
		}(uint64(i))
	}
	p.markEnded(1, nil)
	wg.Wait()
	if !p.State().Ended || p.State().YouHold {
		t.Fatal("concurrent ownership revived ended session")
	}
}
