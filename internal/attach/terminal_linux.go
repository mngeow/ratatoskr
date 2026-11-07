//go:build linux

package attach

// The Linux terminal ioctls: TCGETS and TCSETS for the termios, and TCFLSH with TCIFLUSH to throw away
// pending input.

import "golang.org/x/sys/unix"

// terminalGet reads the terminal's current settings.
func terminalGet(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TCGETS)
}

// terminalSet writes the terminal's settings.
func terminalSet(fd int, termios *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TCSETS, termios)
}

// flushInput discards input that has arrived but not been read.
func flushInput(fd int) error {
	return unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH)
}
