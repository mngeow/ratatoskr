//go:build !unix

package daemon

import "os"

// logFlags is how the log file is opened. O_NOCTTY does not exist here; the daemon's
// supported platforms are Linux and macOS, so this file exists only to keep the package
// buildable elsewhere.
const logFlags = os.O_APPEND | os.O_CREATE | os.O_WRONLY
