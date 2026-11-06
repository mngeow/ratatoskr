package daemon

// Who is on the other end of a Unix socket connection.
//
// The UID check is the security boundary: a different user is refused before anything is
// read, and a failure to read the credentials refuses too (fail closed). The PID is only
// used for the best-effort ancestry check, so failing to read it is not a refusal.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No connection has been accepted.

import (
	"fmt"
	"net"
)

// peerInfo is what the kernel told us about a connection's peer.
type peerInfo struct {
	UID uint32
	PID int
	// pidKnown is false when the platform could not supply a PID. The ancestry check is
	// then skipped, and logged as skipped.
	pidKnown bool
}

// peerOf reads the peer credentials of one connection.
//
// Both the raw-connection control call and the platform's getsockopt are checked: either
// failing refuses the connection. The borrowed descriptor is never closed and never kept
// beyond the control call.
//
// UNTESTED.
func peerOf(conn *net.UnixConn) (peerInfo, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return peerInfo{}, fmt.Errorf("daemon: peer credentials: %w", err)
	}

	var info peerInfo
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		info, credErr = peerCreds(fd)
	}); err != nil {
		return peerInfo{}, fmt.Errorf("daemon: peer credentials: %w", err)
	}
	if credErr != nil {
		return peerInfo{}, credErr
	}
	return info, nil
}
