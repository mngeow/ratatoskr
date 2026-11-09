//go:build !linux

package ipc

import "net"

// checkPeer: macOS has no stdlib peer-credential call, so the filesystem checks (0700 directory, 0600 socket,
// owner) are the gate in v1. See contract section 11 and gap 2.
func checkPeer(net.Conn) error { return nil }
