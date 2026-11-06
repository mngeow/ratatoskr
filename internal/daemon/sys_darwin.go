//go:build darwin

package daemon

// Numbered errno values are platform-specific, so the ones the CLI distinguishes live with
// the platform that defines them.

import "syscall"

// syscallECONNREFUSED is ECONNREFUSED.
const syscallECONNREFUSED = syscall.ECONNREFUSED
