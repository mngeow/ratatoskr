package pane

// UNTESTED. Only this worker calls x/vt. UI methods read atomics or submit
// coalesced requests; a parser/renderer stall cannot enter View or Update.

import (
	"context"
	"fmt"
	"time"
)

const paneOperationLimit = 3 * time.Second // UNTESTED bound for one operation, not per chunk.
type emulatorJob struct {
	ctx  context.Context
	run  func() error
	done chan error
}

func (p *Pane) emulatorLoop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.lifeCtx.Done():
			return
		case job := <-p.emulatorJobs:
			if p.emulatorDead.Load() {
				job.done <- fmt.Errorf("pane: emulator unavailable: %w", context.DeadlineExceeded)
				continue
			}
			if err := job.ctx.Err(); err != nil {
				job.done <- err
				continue
			}
			p.emulatorBusyAt.Store(time.Now().UnixNano())
			err := job.run()
			if err == nil {
				pos := p.emu.CursorPosition()
				frame := Frame{Content: p.emu.Render(), X: pos.X, Y: pos.Y, Hidden: p.emu.CursorHidden()}
				p.lastFrame.Store(&frame)
			}
			p.emulatorBusyAt.Store(0)
			job.done <- err
		}
	}
}

func (p *Pane) emulatorCall(ctx context.Context, run func() error) error {
	job := emulatorJob{ctx: ctx, run: run, done: make(chan error, 1)}
	select {
	case p.emulatorJobs <- job:
	case <-ctx.Done():
		p.noteEmulatorTimeout(ctx.Err())
		return ctx.Err()
	case <-p.lifeCtx.Done():
		return p.lifeCtx.Err()
	}
	select {
	case err := <-job.done:
		return err
	case <-ctx.Done():
		p.noteEmulatorTimeout(ctx.Err())
		return ctx.Err()
	case <-p.lifeCtx.Done():
		return p.lifeCtx.Err()
	}
}

func (p *Pane) noteEmulatorTimeout(err error) {
	if p.lifeCtx.Err() != nil {
		return
	}
	p.emulatorDead.Store(true)
	p.setError(fmt.Errorf("pane: emulator operation did not finish: %w", err))
}
