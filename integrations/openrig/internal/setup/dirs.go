package setup

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func ownedByMe(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// CheckPrivateDir requires path to be a real directory (not a symlink) owned by the current user with mode
// 0700. With create set, a missing directory is made with mode 0700 (the parent must exist).
func CheckPrivateDir(path string, create bool) error {
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("cannot create %s", Printable(path))
		}
	}
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return fmt.Errorf("cannot read %s", Printable(path))
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return fmt.Errorf("%s is not a plain directory (a symlink or a file is refused)", Printable(path))
	case !ownedByMe(info):
		return fmt.Errorf("%s is not owned by the current user", Printable(path))
	case info.Mode().Perm() != 0o700:
		return fmt.Errorf("%s must have mode 0700: run chmod 700 on it", Printable(path))
	}
	return nil
}

// OwnedRegular lstat's path and requires a regular file (not a symlink) owned by the current user.
func OwnedRegular(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file (a symlink or another kind is refused)", Printable(path))
	}
	if !ownedByMe(info) {
		return nil, fmt.Errorf("%s is not owned by the current user", Printable(path))
	}
	return info, nil
}

// EnsureLogFile creates the log file with mode 0600 if it is missing and never truncates one that exists.
// It returns a note when an existing file has a wider mode (it is not changed).
func EnsureLogFile(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("cannot create %s", Printable(path))
	}
	_ = f.Close()
	info, err := OwnedRegular(path)
	if err != nil {
		return "", err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Sprintf("%s is readable by group or others (mode %o): run chmod 600 on it", Printable(path), info.Mode().Perm()), nil
	}
	return "", nil
}
