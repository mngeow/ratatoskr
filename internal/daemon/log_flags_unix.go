//go:build unix

package daemon

import (
	"os"
	"syscall"
)

// logFlags is how the log file is opened.
//
// O_NOCTTY keeps a terminal device from becoming the daemon's controlling terminal, and
// O_APPEND puts every write at the end of the file, so a reader that truncates it and a
// writer that appends cannot corrupt each other's lines.
const logFlags = os.O_APPEND | os.O_CREATE | os.O_WRONLY | syscall.O_NOCTTY
