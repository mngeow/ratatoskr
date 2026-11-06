package daemon

// Untested: under GR-3 nothing here has been run. The stale-socket cases create real
// files and one real socket file in a temporary directory; nothing is dialled.

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSocketPathLayout: the socket lives in daemon/ inside the state directory.
func TestSocketPathLayout(t *testing.T) {
	got := socketPath("/state")
	want := filepath.Join("/state", "daemon", "daemon.sock")
	if got != want {
		t.Fatalf("socketPath = %q, want %q", got, want)
	}
	if dir := daemonDir("/state"); dir != filepath.Join("/state", "daemon") {
		t.Fatalf("daemonDir = %q", dir)
	}
}

// TestCheckSocketPath: the three refusals, and one acceptance.
func TestCheckSocketPath(t *testing.T) {
	ok := "/tmp/ratatoskr/daemon/daemon.sock"
	if err := checkSocketPath(ok); err != nil {
		t.Fatalf("checkSocketPath(%q): %v", ok, err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "at the limit",
			path: "/" + strings.Repeat("a", maxSocketPath-1),
			want: "must be under",
		},
		{
			name: "over the limit",
			path: "/" + strings.Repeat("a", maxSocketPath+10),
			want: "must be under",
		},
		{
			name: "an embedded NUL",
			path: "/tmp/da\x00emon.sock",
			want: "NUL",
		},
		{
			name: "an abstract socket",
			path: "@ratatoskr",
			want: "abstract",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSocketPath(tc.path)
			if err == nil {
				t.Fatalf("checkSocketPath accepted %q", tc.path)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
			// The error names the path when it can be printed.
			if !strings.Contains(tc.path, "\x00") && !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("error = %q, want it to name the path", err)
			}
		})
	}
}

// TestRemoveStaleSocket: only a stale socket is removed, and every refusal names the path.
func TestRemoveStaleSocket(t *testing.T) {
	t.Run("absent is nothing to do", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "daemon.sock")
		if err := removeStaleSocket(path); err != nil {
			t.Fatalf("removeStaleSocket: %v", err)
		}
	})

	t.Run("a stale socket is removed", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "daemon.sock")

		// A real socket file, opened and closed: exactly what a daemon that died leaves.
		listener, err := net.Listen("unix", path)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		if err := listener.Close(); err != nil {
			t.Fatalf("close the listener: %v", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("the socket file is missing: %v", err)
		}

		if err := removeStaleSocket(path); err != nil {
			t.Fatalf("removeStaleSocket: %v", err)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("the socket still exists: %v", err)
		}
	})

	t.Run("a regular file is refused and kept", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "daemon.sock")
		if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		err := removeStaleSocket(path)
		if err == nil {
			t.Fatal("removeStaleSocket removed a regular file")
		}
		if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "not a socket") {
			t.Fatalf("error = %q, want it to name the path and the reason", err)
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("the file was removed: %v", err)
		}
	})

	t.Run("a directory is refused and kept", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "daemon.sock")
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}

		if err := removeStaleSocket(path); err == nil {
			t.Fatal("removeStaleSocket removed a directory")
		}
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("the directory was removed: %v", err)
		}
	})

	t.Run("a symlink to a socket is refused and kept", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "real.sock")
		listener, err := net.Listen("unix", target)
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		defer func() {
			_ = listener.Close()
		}()

		link := filepath.Join(dir, "daemon.sock")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		err = removeStaleSocket(link)
		if err == nil {
			t.Fatal("removeStaleSocket removed through a symlink")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error = %q, want it to name the symlink", err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatalf("the symlink was removed: %v", err)
		}
	})
}

// TestPrepareDir: daemon/ is 0700 and a symlinked daemon/ is refused.
func TestPrepareDir(t *testing.T) {
	t.Run("creates 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "daemon")
		if err := prepareDir(dir); err != nil {
			t.Fatalf("prepareDir: %v", err)
		}
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("mode = %04o, want 0700", perm)
		}
	})

	t.Run("repairs a widened mode", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "daemon")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		if err := prepareDir(dir); err != nil {
			t.Fatalf("prepareDir: %v", err)
		}
		info, err := os.Lstat(dir)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("mode = %04o, want 0700", perm)
		}
	})

	t.Run("a symlinked directory is refused", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "real")
		if err := os.Mkdir(real, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		link := filepath.Join(root, "daemon")
		if err := os.Symlink(real, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		if err := prepareDir(link); err == nil {
			t.Fatal("prepareDir accepted a symlinked directory")
		}
	})
}
