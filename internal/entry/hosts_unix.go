//go:build unix

package entry

// Reading hosts.json safely on a Unix system.
//
// Two checks matter and both are cheap: the file must be a regular file that this user owns and that
// nobody else can write, and it must not be a link. The link check happens twice - once with
// os.Lstat and again with O_NOFOLLOW on the actual open - so a file swapped in between is still
// refused.

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// readHostsFile opens, checks and reads the file.
func readHostsFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, ErrNoHostMap
	case err != nil:
		return nil, fmt.Errorf("%s could not be inspected", fileBase(path))
	case info.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("%s is a symbolic link; it must be a regular file", fileBase(path))
	case !info.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", fileBase(path))
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, unix.ENOENT):
		return nil, ErrNoHostMap
	case errors.Is(err, unix.ELOOP):
		return nil, fmt.Errorf("%s is a symbolic link; it must be a regular file", fileBase(path))
	case err != nil:
		return nil, fmt.Errorf("%s could not be opened", fileBase(path))
	}

	file := os.NewFile(uintptr(fd), path)
	defer func() {
		_ = file.Close()
	}()

	// The checks run on the OPENED descriptor, so they describe the file that will be read.
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s could not be inspected", fileBase(path))
	}
	if !opened.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", fileBase(path))
	}
	if opened.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by group or others; run chmod 600 %s", fileBase(path), path)
	}
	if owner, ok := ownerUID(opened); ok && owner != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("%s is owned by another user; it must belong to you", fileBase(path))
	}
	if opened.Size() > MaxHostsBytes {
		return nil, fmt.Errorf("%s is larger than %d KiB", fileBase(path), MaxHostsBytes>>10)
	}

	return readAllBounded(file, path)
}

// ownerUID reads the owner from the stat result the platform returned.
func ownerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return stat.Uid, true
}
