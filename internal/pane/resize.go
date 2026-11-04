package pane

// Coalesced, non-blocking resize requests.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No resize has ever been
// applied by the worker.

// RequestResize records the size the pane should become and wakes the resize
// worker. It never waits for the PTY, so a resize storm (a dragged window edge)
// cannot stall the UI: intermediate sizes are simply replaced.
//
// The size is validated exactly as Resize validates it, so an impossible size is
// refused here rather than by the worker later.
//
// UNTESTED.
func (p *Pane) RequestResize(width, height int) error {
	if err := validateSize(width, height); err != nil {
		return err
	}
	if err := p.checkOpen(); err != nil {
		return err
	}

	// One atomic word, so the worker can never observe a width from one request
	// beside a height from another.
	p.desired.Store(packSize(width, height))
	select {
	case p.resizeWake <- struct{}{}:
	default:
		// The worker is already awake and will read the newest value.
	}
	return nil
}

// resizeLoop applies the newest requested size until the pane closes.
//
// It is one lifecycle goroutine, tracked by wg, and it calls the existing Resize
// - which takes the lifecycle lock and updates emulator and PTY together - so
// there is no second path into a resize.
//
// UNTESTED.
func (p *Pane) resizeLoop() {
	defer p.wg.Done()

	applied := packSize(0, 0)
	for {
		select {
		case <-p.lifeCtx.Done():
			return
		case <-p.resizeWake:
		}

		for {
			desired := p.desired.Load()
			if desired == applied {
				break
			}
			applied = desired

			width, height := unpackSize(desired)
			if err := p.Resize(width, height); err != nil {
				if p.isShutdownErr(err) {
					return
				}
				p.recordError("resize", err)
			}
		}
	}
}

// packSize puts a validated size into one atomic word.
func packSize(width, height int) uint64 {
	return uint64(uint32(width))<<32 | uint64(uint32(height)) //nolint:gosec // both are validated to fit in 16 bits.
}

// unpackSize reads a packed size back.
func unpackSize(packed uint64) (width, height int) {
	return int(packed >> 32), int(packed & 0xffffffff) //nolint:gosec // packed from two validated sizes.
}
