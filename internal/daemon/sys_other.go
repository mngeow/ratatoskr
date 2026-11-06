//go:build !linux && !darwin

package daemon

// Numbered errno values are platform-specific. This build does not support a daemon, so
// nothing can be refused with ECONNREFUSED.
import "errors"

// syscallECONNREFUSED stands in for an errno this platform does not name here.
var syscallECONNREFUSED = errors.New("daemon: connection refused (unsupported platform)")
