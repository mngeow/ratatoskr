package pane

// UNTESTED under GR-3. Pipe draining never waits for a PTY write. Saturation
// reports backpressure and keeps the drain alive; real transport errors disable
// input visibly. Completion fences preserve actual-Enter-before-submit ordering.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

const transportSlots = 512
const transportByteLimit = 2 * 1024 * 1024
const userPasteLimit = 1024 * 1024

var ErrEmptyEncoding = errors.New("pane: emulator produced an empty input encoding")

var ErrInputBackpressure = errors.New("pane: input buffer full; wait for the child and retry")

type inputBatch struct {
	ctx          context.Context
	remaining    atomic.Int32
	captured     chan struct{}
	capturedOnce sync.Once

	mu   sync.Mutex
	err  error
	done chan error
	once sync.Once
}

func newInputBatch() *inputBatch {
	return &inputBatch{done: make(chan error, 1), captured: make(chan struct{})}
}
func (b *inputBatch) fail(err error) {
	if b == nil || err == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err == nil {
		b.err = err
	}
}
func (b *inputBatch) result() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}
func (b *inputBatch) finish() { b.once.Do(func() { b.done <- b.result() }) }

type transportItem struct {
	data  []byte
	batch *inputBatch
	fence bool
}

func (p *Pane) initTransport() { p.transport = make(chan transportItem, transportSlots) }

// enqueueFromPipe must not block: its caller may be the reader that releases
// the emulator owner. Bytes already accepted remain in FIFO order.
func (p *Pane) enqueueFromPipe(item transportItem) {
	n := int64(len(item.data))
	if p.queuedBytes.Add(n) > transportByteLimit {
		p.queuedBytes.Add(-n)
		p.rejectTransport(item)
		return
	}
	select {
	case p.transport <- item:
	default:
		p.queuedBytes.Add(-n)
		p.rejectTransport(item)
	}
}

func (p *Pane) rejectTransport(item transportItem) {
	if item.batch != nil {
		item.batch.fail(ErrInputBackpressure)
		if item.fence {
			item.batch.finish()
		}
	} else {
		p.setError(fmt.Errorf("terminal reply dropped: %w", ErrInputBackpressure))
	}
}

// replyLoop only drains the emulator pipe. It never waits for writeMu or PTY
// progress. Each encoder operation declares its real pipe-write count. An unexpected
// empty encoding is an error, not a synthetic completion marker.
func (p *Pane) replyLoop() {
	defer p.wg.Done()
	// io.Pipe never combines writes. Every supported input Write fits here:
	// keys/mouse write once; nonempty Paste writes once or three times in mode 2004.
	buf := make([]byte, userPasteLimit+32)
	for {
		n, err := p.emu.Read(buf)
		batch := p.encoding.Load()
		if n > 0 {
			p.enqueueFromPipe(transportItem{data: append([]byte(nil), buf[:n]...), batch: batch})
		}
		if err == nil && batch != nil {
			if n == 0 {
				batch.fail(ErrEmptyEncoding)
			}
			if batch.remaining.Add(-1) == 0 {
				p.enqueueFromPipe(transportItem{batch: batch, fence: true})
				batch.capturedOnce.Do(func() { close(batch.captured) })
				p.encoding.CompareAndSwap(batch, nil)
			}
		}
		// Unexpected empty query output has no input batch and is simply ignored.
		if err != nil {
			return
		}
	}
}

// writerLoop is independent of screen locking. Slow input has no arbitrary
// per-chunk timeout: shutdown cancels it. The queue is bounded, and its producer
// reports overflow rather than blocking the output parser or killing the drain.
func (p *Pane) writerLoop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.lifeCtx.Done():
			return
		case item := <-p.transport:
			if item.fence {
				if p.drainDead.Load() {
					item.batch.fail(p.drainErr())
				}
				item.batch.finish()
				continue
			}
			if p.drainDead.Load() {
				item.batch.fail(p.drainErr())
				p.queuedBytes.Add(-int64(len(item.data)))
				continue
			}
			// Once a batch overflowed, do not continue its remaining bytes.
			if item.batch != nil && item.batch.result() != nil {
				p.queuedBytes.Add(-int64(len(item.data)))
				continue
			}
			ctx := item.batchContext(p.lifeCtx)
			cancel := func() {}
			if item.batch == nil {
				ctx, cancel = context.WithTimeout(p.lifeCtx, paneOperationLimit)
			}
			err := p.writePTY(ctx, "terminal input", item.data)
			cancel()
			p.queuedBytes.Add(-int64(len(item.data)))
			if err != nil {
				item.batch.fail(err)
				p.noteTransportError(err)
			}
		}
	}
}

// sendEncoded serializes user input, captures its pipe bytes, then waits for
// actual transport completion outside the emulator owner. Query parsing can make progress
// while a child is reading slowly or echoing a large paste.
func (p *Pane) sendEncoded(writes func() int32, encode func() error) error {
	if err := p.checkInput(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(p.lifeCtx, paneOperationLimit)
	defer cancel()
	if err := p.inputMu.lock(ctx); err != nil {
		return err
	}
	defer p.inputMu.unlock()
	if err := p.checkInput(); err != nil {
		return err
	}
	batch := newInputBatch()
	batch.ctx = ctx
	err := p.emulatorCall(ctx, func() error {
		batch.remaining.Store(writes())
		p.encoding.Store(batch)
		if err := encode(); err != nil {
			p.encoding.CompareAndSwap(batch, nil)
			return err
		}
		// No synthetic empty write/barrier: count the pinned encoder's real pipe
		// writes, including an unexpected empty encoding, and fence those bytes.
		select {
		case <-batch.captured:
			return nil
		case <-ctx.Done():
			// Leave the batch identity for a late pipe read; its canceled
			// context prevents those bytes being reclassified as a query.
			p.noteEmulatorTimeout(ctx.Err())
			return ctx.Err()
		case <-p.lifeCtx.Done():
			return p.lifeCtx.Err()
		}
	})
	if err != nil {
		return err
	}
	return waitInputBatch(ctx, batch)
}

func (item transportItem) batchContext(parent context.Context) context.Context {
	if item.batch != nil && item.batch.ctx != nil {
		return item.batch.ctx
	}
	return parent
}

func waitInputBatch(ctx context.Context, batch *inputBatch) error {
	select {
	case err := <-batch.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// fenceTransport orders the direct router paste after previously buffered user
// input, even if a previous input batch was refused. It sends no child bytes.
func (p *Pane) fenceTransport(ctx context.Context) error {
	batch := newInputBatch()
	select {
	case p.transport <- transportItem{batch: batch, fence: true}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.lifeCtx.Done():
		return p.lifeCtx.Err()
	}
	return waitInputBatch(ctx, batch)
}

func (p *Pane) checkInput() error {
	if err := p.checkOpen(); err != nil {
		return err
	}
	if p.exited.Load() {
		return errors.New("pane: child has exited")
	}
	if p.emulatorDead.Load() {
		return errors.New("pane: emulator unavailable after operation timeout")
	}
	return p.drainErr()
}

func (p *Pane) noteTransportError(err error) {
	if err == nil || p.lifeCtx.Err() != nil || p.closed.Load() {
		return
	}
	p.setError(err)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrInputBackpressure) {
		p.drainDead.Store(true)
	}
}
