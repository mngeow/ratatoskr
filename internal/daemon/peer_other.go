//go:build !linux && !darwin

package daemon

// Peer credentials on a platform this build does not support: refuse, because the UID
// check is the security boundary and this build cannot perform it.

import "errors"

// peerCreds always fails, so every connection is refused (fail closed).
func peerCreds(fd uintptr) (peerInfo, error) {
	return peerInfo{}, errors.New("daemon: peer credentials are not supported on this platform")
}
