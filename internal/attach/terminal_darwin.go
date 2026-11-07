//go:build darwin

package attach

// The macOS terminal ioctls: TIOCGETA and TIOCSETA for the termios, and TIOCFLUSH with FREAD to throw
// away pending input. FREAD is 1 in the SDK's sys/fcntl.h, which is what a *int argument to
// TIOCFLUSH carries.

import "golang.org/x/sys/unix"

// terminalGet reads the terminal's current settings.
func terminalGet(fd int) (*unix.Termios, error) {
	return unix.IoctlGetTermios(fd, unix.TIOCGETA)
}

// terminalSet writes the terminal's settings.
func terminalSet(fd int, termios *unix.Termios) error {
	return unix.IoctlSetTermios(fd, unix.TIOCSETA, termios)
}

// flushInput discards input that has arrived but not been read.
func flushInput(fd int) error {
	return unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, 1)
}
