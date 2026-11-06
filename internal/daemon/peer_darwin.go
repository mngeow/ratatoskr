//go:build darwin

package daemon

// macOS peer credentials: LOCAL_PEERCRED on SOL_LOCAL for the UID, LOCAL_PEERPID for the
// PID.

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// xucredVersion is XUCRED_VERSION from <sys/ucred.h>, which is 0. x/sys does not
// export it, so the value is written here with the header it comes from.
const xucredVersion = 0

// peerCreds reads the credential of the peer of fd.
//
// A UID failure refuses the connection. A PID failure does not: the ancestry check is
// best effort, so the peer is simply recorded as having no known PID.
func peerCreds(fd uintptr) (peerInfo, error) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return peerInfo{}, fmt.Errorf("daemon: peer credentials: %w", err)
	}
	if cred.Version != xucredVersion {
		return peerInfo{}, errors.New("daemon: peer credentials: unexpected version")
	}

	info := peerInfo{UID: cred.Uid}
	if pid, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID); err == nil {
		info.PID = pid
		info.pidKnown = true
	}
	return info, nil
}
