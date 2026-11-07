//go:build !unix

package entry

// The host map is refused on a platform this build does not support: the ownership and link checks
// this file exists for cannot be made, and an unchecked hosts.json would become an ssh destination.

import "fmt"

// readHostsFile always fails, so every host is "manual" with the reason.
func readHostsFile(path string) ([]byte, error) {
	return nil, fmt.Errorf("%s cannot be checked on this platform", fileBase(path))
}
