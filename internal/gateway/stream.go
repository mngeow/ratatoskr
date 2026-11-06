package gateway

// The stream route: one long-lived GET /v1/stream per host, newline-delimited
// JSON written by the handler goroutine alone.
//
// The shape is fixed by the DevOps network and file rules: no whole-response write
// timeout, a per-frame write deadline, a flush after every frame, one serialized
// writer, a bounded queue, and no database transaction or lock held across a
// network write.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No stream has been opened.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// streamContentType is NDJSON: one JSON object per line.
	streamContentType = "application/x-ndjson"

	// maxFrameBytes caps one encoded frame plus its newline. A larger frame ends
	// the stream: half a line would corrupt the framing for everything after it.
	maxFrameBytes = 1 << 20 // UNTESTED guess

	// directoryReadTimeout bounds the directory query used while building a
	// frame, so a slow database cannot pin a stream's writer.
	directoryReadTimeout = 5 * time.Second // UNTESTED guess
)

// handleStream serves one host's stream.
//
// It re-reads the credential from the request so the periodic recheck can ask the
// database whether the host is still paired, attaches itself to the hub (replacing
// any earlier stream for the same host), writes one directory frame, and then
// writes frames and pings until the context ends or a write fails.
//
// The handler goroutine is the only writer to the connection; other goroutines only
// push frames into its queue.
//
// UNTESTED.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, host string) {
	// Needed again for the recheck: withAuth has already proved it is valid once,
	// but a stream outlives that check.
	credential, ok := bearerCredential(r.Header.Get("Authorization"))
	if !ok {
		writeWireError(w, http.StatusUnauthorized, protocol.WireErrUnauthorized, "a bearer credential is required")
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	st := &stream{
		host:   host,
		frames: make(chan protocol.StreamFrame, s.opts.QueueSize),
		cancel: cancel,
		done:   make(chan struct{}),
	}

	// A new stream replaces the old one for this host.
	if previous := s.hub.attach(host, st); previous != nil {
		previous.cancel()
	}
	// This host has just become attached, so its liveness changed for everyone
	// else: their `Live` flags would stay stale until the next registration if
	// nothing pushed a new directory. Never blocks.
	s.directoryChanged()

	defer func() {
		close(st.done)
		if s.hub.detach(host, st) {
			// This host has just stopped being attached, so its liveness changed:
			// pushDirectory only queues a goroutine and cannot deadlock here.
			s.directoryChanged()
		}
	}()

	header := w.Header()
	header.Set("Content-Type", streamContentType)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	// http.ResponseController finds SetWriteDeadline and Flush through the 404/405
	// wrapper's Unwrap; that dependency is why the wrapper has to keep both.
	controller := http.NewResponseController(w)

	// The first frame is the directory as it stands now. A failed read ends the
	// stream with nothing written: an empty directory would claim there are no
	// peers, which is worse than no stream.
	initialCtx, initialCancel := context.WithTimeout(ctx, directoryReadTimeout)
	entries, err := s.directoryEntries(initialCtx)
	initialCancel()
	if err != nil {
		return
	}
	if !s.writeFrame(controller, w, protocol.StreamFrame{
		Type:      protocol.FrameDirectory,
		Directory: entries,
		At:        s.opts.Now(),
	}) {
		return
	}

	ticker := time.NewTicker(s.opts.PingEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case frame := <-st.frames:
			if !s.writeFrame(controller, w, frame) {
				return
			}

		case <-ticker.C:
			// Recheck before writing, so an unpaired host receives no further
			// frames. A transient database error keeps the stream: only a real
			// unpaired answer ends it.
			if _, err := s.store.Authenticate(ctx, credential); err != nil {
				if errors.Is(err, ErrUnauthorized) {
					return
				}
			}
			if !s.writeFrame(controller, w, protocol.StreamFrame{
				Type: protocol.FramePing,
				At:   s.opts.Now(),
			}) {
				return
			}
		}
	}
}

// writeFrame writes one NDJSON frame and flushes it.
//
// Every step can end the stream: an encode failure, an over-size frame, a deadline
// that cannot be set (continuing without deadlines is exactly what the DevOps
// rules forbid), a short write, or a failed flush.
//
// UNTESTED.
func (s *Server) writeFrame(controller *http.ResponseController, w http.ResponseWriter, frame protocol.StreamFrame) bool {
	payload, err := json.Marshal(frame)
	if err != nil {
		return false
	}
	if len(payload)+1 > maxFrameBytes {
		return false
	}

	if err := controller.SetWriteDeadline(time.Now().Add(s.opts.WriteDeadline)); err != nil {
		return false
	}
	// One Write for the line and its newline: two writes could leave half a line
	// on the wire if the second failed.
	line := append(payload, '\n')
	if _, err := w.Write(line); err != nil {
		return false
	}
	if err := controller.Flush(); err != nil {
		return false
	}
	return true
}

// directoryChanged tells the server's directory hook, if it has one, that a
// host's liveness or sessions changed.
func (s *Server) directoryChanged() {
	if s.onDirectoryChanged != nil {
		s.onDirectoryChanged()
	}
}
