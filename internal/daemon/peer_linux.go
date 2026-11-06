//go:build linux

package daemon

// Linux peer credentials: SO_PEERCRED on SOL_SOCKET.

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// peerCreds reads the credential of the peer of fd.
func peerCreds(fd uintptr) (peerInfo, error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return peerInfo{}, fmt.Errorf("daemon: peer credentials: %w", err)
	}
	return peerInfo{UID: cred.Uid, PID: int(cred.Pid), pidKnown: true}, nil
}
