// Package ipc is the local protocol between the thin client and the sidecar over a Unix socket
// (docs/implementation/openrig-bridge-contract.md section 11).
//
// One request per connection: one JSON object on one line ending in \n, then one JSON object on one line in
// reply, then the sidecar closes. The socket lives in the 0700 state directory with mode 0600. The client holds
// no gateway credential and makes no network call.
//
// UNTESTED. Under GR-3 nothing here has been run.
package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	// Version is the protocol version carried in every message.
	Version = 1

	// MaxLine bounds one request or reply line.
	MaxLine = 256 * 1024
	// ReadRequestTimeout is how long the sidecar waits for the request after accepting.
	ReadRequestTimeout = 5 * time.Second
	// ReplyTimeout is how long the client waits for the reply (the gateway's send wait is 8 s).
	ReplyTimeout = 15 * time.Second
	// MaxConcurrent is how many requests are served at once; the next gets OutcomeBusy.
	MaxConcurrent = 4
)

// Ops.
const (
	OpSend      = "send"
	OpDirectory = "directory"
	OpStatus    = "status"
)

// Outcomes of the sidecar's reply.
const (
	OutcomeOK           = "ok"
	OutcomeStored       = "stored"
	OutcomeNotDelivered = "not_delivered"
	OutcomeUncertain    = "uncertain"
	OutcomeNotSent      = "not_sent"
	OutcomeRejected     = "rejected"
	OutcomeRevoked      = "revoked"
	OutcomeBusy         = "busy"
)

// ErrBridgeDown means the socket could not be reached: nothing was sent.
var ErrBridgeDown = errors.New("the bridge is not running")

// ErrAlreadyRunning means a live sidecar owns the socket.
var ErrAlreadyRunning = errors.New("a sidecar is already running")

// Request is what the client sends.
type Request struct {
	V       int    `json:"v"`
	Op      string `json:"op"`
	ID      string `json:"id,omitempty"`
	To      string `json:"to,omitempty"`
	Body    string `json:"body,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// DirectoryEntry is one address.
type DirectoryEntry struct {
	Address string `json:"address"`
	Live    bool   `json:"live"`
}

// Response is what the sidecar answers.
type Response struct {
	V         int              `json:"v"`
	Outcome   string           `json:"outcome"`
	Result    string           `json:"result,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	ID        string           `json:"id,omitempty"`
	Directory []DirectoryEntry `json:"directory,omitempty"`
	Status    json.RawMessage  `json:"status,omitempty"`
}

// CheckPrivate applies the filesystem gate: the state directory is a plain 0700 directory owned by the current
// user, and the socket, if it exists, is a socket with mode 0600 owned by the current user.
func CheckPrivate(stateDir, socket string, needSocket bool) error {
	info, err := os.Lstat(stateDir)
	switch {
	case err != nil:
		return ErrBridgeDown
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || info.Mode().Perm() != 0o700 || !owned(info):
		return errors.New("the state directory is not a private directory owned by you")
	}
	info, err = os.Lstat(socket)
	if err != nil {
		if needSocket {
			return ErrBridgeDown
		}
		return nil
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 || !owned(info) {
		return errors.New("the bridge socket is not a private socket owned by you")
	}
	return nil
}

func owned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// Call sends one request and reads one reply.
func Call(ctx context.Context, stateDir, socket string, req Request) (Response, error) {
	if err := CheckPrivate(stateDir, socket, true); err != nil {
		return Response{}, err
	}
	req.V = Version
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("cannot encode the request: %w", err)
	}
	raw = append(raw, '\n')
	if len(raw) > MaxLine {
		return Response{}, errors.New("the request is too large")
	}

	dialer := net.Dialer{Timeout: 2 * time.Second}
	conn, err := dialer.DialContext(ctx, "unix", socket)
	if err != nil {
		return Response{}, ErrBridgeDown
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(ReplyTimeout))
	if _, err := conn.Write(raw); err != nil {
		return Response{}, fmt.Errorf("%w: the request could not be written", ErrBridgeDown)
	}
	line, err := readLine(conn)
	if err != nil {
		// The request was written: the sidecar may have acted on it.
		return Response{}, &LostReply{Err: err}
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil || resp.V != Version || resp.Outcome == "" {
		return Response{}, &LostReply{Err: errors.New("the bridge answered something unexpected")}
	}
	return resp, nil
}

// LostReply means the request reached the socket but no usable reply came back.
type LostReply struct{ Err error }

func (e *LostReply) Error() string { return "the bridge did not answer: " + e.Err.Error() }
func (e *LostReply) Unwrap() error { return e.Err }

func readLine(r io.Reader) ([]byte, error) {
	reader := bufio.NewReaderSize(io.LimitReader(r, MaxLine+1), 4096)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > MaxLine {
		return nil, errors.New("line too long")
	}
	return line, nil
}

// Listen binds the socket at path with mode 0600. A stale socket (nothing listening) is removed; a live one
// returns ErrAlreadyRunning.
func Listen(path string) (net.Listener, error) {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("something that is not a socket is at the bridge socket path")
		}
		if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
			_ = conn.Close()
			return nil, ErrAlreadyRunning
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("cannot remove the stale bridge socket: %w", err)
		}
	}
	old := syscall.Umask(0o177)
	listener, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("cannot listen on the bridge socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("cannot set the bridge socket mode: %w", err)
	}
	return listener, nil
}

// Handler answers one request.
type Handler func(ctx context.Context, req Request) Response

// Serve accepts connections until the listener is closed or ctx ends. At most MaxConcurrent run at once.
func Serve(ctx context.Context, listener net.Listener, handler Handler) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, MaxConcurrent)
	for {
		conn, err := listener.Accept()
		if err != nil {
			break
		}
		select {
		case slots <- struct{}{}:
		default:
			reply(conn, Response{V: Version, Outcome: OutcomeBusy})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			serveOne(ctx, conn, handler)
		}()
	}
	wg.Wait()
}

func reply(conn net.Conn, resp Response) {
	defer func() { _ = conn.Close() }()
	raw, err := json.Marshal(resp)
	if err != nil {
		return
	}
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write(append(raw, '\n'))
}

func serveOne(ctx context.Context, conn net.Conn, handler Handler) {
	if err := checkPeer(conn); err != nil {
		reply(conn, Response{V: Version, Outcome: OutcomeRejected, Detail: "peer not allowed"})
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(ReadRequestTimeout))
	line, err := readLine(conn)
	if err != nil {
		reply(conn, Response{V: Version, Outcome: OutcomeRejected, Detail: "no valid request line"})
		return
	}
	var req Request
	if json.Unmarshal(line, &req) != nil || req.V != Version {
		reply(conn, Response{V: Version, Outcome: OutcomeRejected, Detail: "bad request"})
		return
	}
	resp := handler(ctx, req)
	resp.V = Version
	reply(conn, resp)
}

// SocketDir returns the directory of a socket path, for callers that need the state directory.
func SocketDir(socket string) string { return filepath.Dir(socket) }
