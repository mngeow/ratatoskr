package daemon

// The socket: the accept loop, the per-connection checks, the handshake and the few
// requests this slice serves.
//
// Order per connection, and the order is the contract: the peer's UID is checked before a
// single byte is read, because that check is the security boundary; then the best-effort
// ancestry check; then the hello, with a deadline so a silent peer cannot hold a
// goroutine.
//
// Ancestry is NOT a security boundary: a double-forked process gets past it, and
// credentials reflect connect time, so PID reuse, reparenting and passing a descriptor all
// defeat it. It stops the accidental case and an agent running `rtk attach` from inside a
// pane.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No connection has been accepted.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// serve accepts connections until the listener is closed.
func (s *daemonServer) serve(listener net.Listener) error {
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				// A closed listener is how shutdown stops the loop.
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

// requestStop asks the daemon to end, exactly once.
func (s *daemonServer) requestStop() {
	s.endOnce.Do(func() { close(s.requestEnd) })
}

// handleConn runs one connection.
func (s *daemonServer) handleConn(conn net.Conn) {
	defer func() {
		_ = conn.Close()
	}()

	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		// Not a Unix socket connection: impossible through this listener, and refused
		// rather than guessed at.
		s.log.logf("refused a connection that is not on a Unix socket")
		return
	}

	// The UID check comes before anything is read.
	peer, err := peerOf(unixConn)
	if err != nil {
		s.log.logf("refused a connection: peer credentials could not be read: %v", err)
		s.refuse(conn, wire.CodeNotAuthorized, "the daemon could not read this connection's credentials")
		return
	}
	// The peer credential is the peer's effective UID, so it is compared with ours.
	if peer.UID != uint32(os.Geteuid()) {
		s.log.logf("refused a connection: peer UID %d is not this user (pid %d)", peer.UID, peer.PID)
		s.refuse(conn, wire.CodeNotAuthorized, "this daemon answers only its own user")
		return
	}

	// Best effort, and never a boundary: see the file comment.
	refused, note := ancestryCheck(peer, s.harnesses)
	if note != "" {
		s.log.logf("%s", note)
	}
	if refused {
		s.refuse(conn, wire.CodeAgentRefused, "this connection comes from an agent this daemon started, which may not use the daemon")
		return
	}

	hello, err := s.readHello(conn)
	if err != nil {
		s.log.logf("refused a connection: %v", err)
		return
	}

	decision := helloDecision(hello, s.build)
	if !decision.ok {
		s.log.logf("refused a %s connection: %s", hello.Role, decision.message)
		s.refuse(conn, decision.code, decision.message)
		return
	}

	s.log.logf("accepted a %s connection (pid %d)", hello.Role, hello.PID)

	windowID := ""
	if hello.Role == wire.RoleWindow {
		// A window gets a random identifier so the gate can name a holder, and so two
		// connections can never be confused for each other.
		windowID = newWindowID()
		if windowID == "" {
			s.log.logf("refused a window connection: no window identifier could be generated")
			s.refuse(conn, wire.CodeInternal, "the daemon could not identify this window")
			return
		}
	}

	if err := s.writeMessage(conn, wire.TypeHelloAck, 0, wire.HelloAck{
		Build:    s.build,
		PID:      os.Getpid(),
		WindowID: windowID,
	}); err != nil {
		s.log.logf("closing a connection after the hello: %v", err)
		return
	}

	if hello.Role == wire.RoleControl {
		s.serveControl(conn)
		return
	}
	s.serveWindow(conn, windowID)
}

// readHello reads the first message, which must be a hello.
func (s *daemonServer) readHello(conn net.Conn) (wire.Hello, error) {
	if err := conn.SetReadDeadline(time.Now().Add(firstMessageDeadline)); err != nil {
		return wire.Hello{}, err
	}
	envelope, err := wire.Read(conn)
	if err != nil {
		return wire.Hello{}, err
	}
	if envelope.Type != wire.TypeHello {
		return wire.Hello{}, errors.New("the first message was not a hello")
	}

	var hello wire.Hello
	if err := wire.Decode(envelope, &hello); err != nil {
		return wire.Hello{}, err
	}
	if hello.Role != wire.RoleWindow && hello.Role != wire.RoleControl {
		return wire.Hello{}, errors.New("the hello named an unknown role")
	}
	// The hello's deadline must not outlive the hello: what follows sets its own.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return wire.Hello{}, err
	}
	return hello, nil
}

// serveControl answers the control requests of this slice.
func (s *daemonServer) serveControl(conn net.Conn) {
	for {
		// A control client asks and leaves; one that idles is dropped, so it cannot hold a
		// goroutine for ever.
		if err := conn.SetReadDeadline(time.Now().Add(controlIdle)); err != nil {
			return
		}
		envelope, err := wire.Read(conn)
		if err != nil {
			// A control client hanging up is normal.
			return
		}

		switch envelope.Type {
		case wire.TypeStatus:
			status := s.status()
			if err := s.writeMessage(conn, wire.TypeResult, envelope.ID, wire.Result{Status: &status}); err != nil {
				return
			}

		case wire.TypeStop:
			if err := s.writeMessage(conn, wire.TypeAck, envelope.ID, wire.Ack{OK: true}); err != nil {
				return
			}
			s.log.logf("stop requested over the socket")
			s.requestStop()
			return

		case wire.TypeStopSession:
			var request wire.StopSession
			if err := wire.Decode(envelope, &request); err != nil {
				s.writeAck(conn, envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeBadRequest, Message: "the request could not be read"}})
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
			endErr := s.sessions.End(ctx, request.Name)
			cancel()
			switch {
			case endErr == nil:
				s.writeAck(conn, envelope.ID, wire.Ack{OK: true})
			case errors.Is(endErr, errNoSession):
				// An unknown name and an already-ended one are the same answer.
				s.writeAck(conn, envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeNoSuchSession, Message: "no session by that name is running"}})
			case errors.Is(endErr, errBusy):
				s.writeAck(conn, envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeBusy, Message: "that session is being started; try again in a moment"}})
			default:
				// Anything else is the daemon's problem, and the text says what it was.
				s.writeAck(conn, envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeInternal, Message: endErr.Error()}})
			}

		default:
			s.writeAck(conn, envelope.ID, wire.Ack{OK: false, Error: &wire.Error{Code: wire.CodeBadRequest, Message: "that request is not supported by this build"}})
		}
	}
}

// serveWindow runs one window connection.
//
// The first message must be an attach, within the same deadline the hello had. A refusal ends
// the connection; everything after that is reported inside the attached answer or as ack/result
// messages.
//
// UNTESTED.
func (s *daemonServer) serveWindow(conn net.Conn, windowID string) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		// Not a Unix connection through this listener: it cannot be a window.
		return
	}

	if err := conn.SetReadDeadline(time.Now().Add(firstMessageDeadline)); err != nil {
		return
	}
	envelope, err := wire.Read(conn)
	if err != nil {
		s.log.logf("window %s: no attach arrived: %v", windowID, err)
		return
	}
	if envelope.Type != wire.TypeAttach {
		s.refuseOpen(conn, wire.CodeBadRequest, "a window must attach first")
		return
	}

	var request wire.Attach
	if err := wire.Decode(envelope, &request); err != nil {
		s.refuseOpen(conn, wire.CodeBadRequest, "that attach could not be read")
		return
	}

	// Attach is the end of the reading deadline: an attached window may sit idle for hours.
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return
	}

	w := newWindow(windowID, unixConn, s.hub, s.log)
	if !s.handleAttach(w, request) {
		return
	}
	w.serve()
}

// status builds the answer to a status request.
//
// The link state and its reason come from the router's own snapshot, so the daemon does
// not keep a second copy of what the UI would show.
func (s *daemonServer) status() wire.Status {
	status := wire.Status{
		PID:       os.Getpid(),
		Build:     s.build,
		StartedAt: s.startedAt,
		LogPath:   s.logPath,
		Windows:   s.windowCount(),
		Sessions:  s.sessionStatusWithHolder(),
	}

	if s.router != nil {
		if snapshot := s.router.Snapshot(); snapshot.Connection != nil {
			status.Link = string(snapshot.Connection.State)
			status.LinkErr = snapshot.Connection.Err
		}
	}
	return status
}

// sessionStatus asks the session control for its sessions, tolerating none being set.
//
// In the daemon the holder comes from the gate, which is the only thing that knows who may type.
func sessionStatus(control sessionControl) []wire.SessionStatus {
	if control == nil {
		return nil
	}
	return control.Status()
}

// sessionStatusWithHolder reports the sessions, with the holder added from the gate: the
// manager knows which sessions run, and only the gate knows who may type into them.
func (s *daemonServer) sessionStatusWithHolder() []wire.SessionStatus {
	sessions := sessionStatus(s.sessions)
	if s.hub == nil {
		return sessions
	}
	for i := range sessions {
		holder, _, drafting, _, _ := s.hub.gate.snapshot(protocol.AgentID(sessions[i].Agent))
		sessions[i].Holder = holder
		sessions[i].Drafting = drafting
	}
	return sessions
}

// windowCount is how many windows are attached.
func (s *daemonServer) windowCount() int {
	if s.hub == nil {
		return 0
	}
	return s.hub.count()
}

// writeAck writes one acknowledgement.
func (s *daemonServer) writeAck(conn net.Conn, id uint64, ack wire.Ack) {
	_ = s.writeMessage(conn, wire.TypeAck, id, ack)
}

// writeMessage frames and writes one message under a deadline.
func (s *daemonServer) writeMessage(conn net.Conn, typ wire.Type, id uint64, body any) error {
	if err := conn.SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
		return err
	}
	return wire.Write(conn, typ, id, body)
}

// refuse sends a refusal and closes the connection.
func (s *daemonServer) refuse(conn net.Conn, code wire.Code, message string) {
	_ = s.writeMessage(conn, wire.TypeRefused, 0, wire.Refusal{Code: code, Message: message})
}

// refuseOpen is refuse, for a connection that is already past its hello.
func (s *daemonServer) refuseOpen(conn net.Conn, code wire.Code, message string) {
	_ = s.writeMessage(conn, wire.TypeRefused, 0, wire.Refusal{Code: code, Message: message})
}

// helloVerdict is the handshake decision, kept pure so it can be tested without a socket.
type helloVerdict struct {
	ok      bool
	code    wire.Code
	message string
}

// helloDecision applies the contract's handshake rule: a window must be this exact build,
// a control connection may differ.
//
// Control must work across builds, because the window refusal's own advice is to run
// `rtk daemon stop`, and a stop from a newer binary has to be able to do it.
//
// UNTESTED.
func helloDecision(hello wire.Hello, daemonBuild string) helloVerdict {
	if hello.Role == wire.RoleControl {
		return helloVerdict{ok: true}
	}
	if hello.Build == daemonBuild {
		return helloVerdict{ok: true}
	}
	return helloVerdict{
		ok:   false,
		code: wire.CodeVersionMismatch,
		message: "this window is build " + hello.Build + " and the daemon is build " + daemonBuild +
			": run `rtk daemon stop`, then start the daemon again with the new build",
	}
}

// newWindowID is 8 random hex characters.
//
// A collision only means two windows share an identifier, which matters once windows
// exist. If the random source fails there is no identifier: an empty WindowID is what a
// control connection carries, and this slice refuses the attach that would use it.
func newWindowID() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(raw[:])
}
