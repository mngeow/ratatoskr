//go:build linux

package ipc

import (
	"errors"
	"net"
	"os"
	"syscall"
)

// checkPeer requires the connecting process to run as the current user (SO_PEERCRED).
func checkPeer(conn net.Conn) error {
	unix, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("not a unix connection")
	}
	raw, err := unix.SyscallConn()
	if err != nil {
		return err
	}
	var cred *syscall.Ucred
	var sockErr error
	if err := raw.Control(func(fd uintptr) {
		cred, sockErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return err
	}
	if sockErr != nil {
		return sockErr
	}
	if int(cred.Uid) != os.Getuid() {
		return errors.New("peer uid differs")
	}
	return nil
}
