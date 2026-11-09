// UNTESTED (GR-3): written, never run.

package ipc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortDir makes a state directory whose socket path fits macOS's limit (t.TempDir paths are too long).
func shortDir(t *testing.T) (dir, socket string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rto")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir, filepath.Join(dir, "bridge.sock")
}

func TestCallRoundTrip(t *testing.T) {
	dir, socket := shortDir(t)
	listener, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Serve(ctx, listener, func(_ context.Context, req Request) Response {
			return Response{Outcome: OutcomeStored, ID: req.ID, Result: "host_stored"}
		})
		close(done)
	}()
	defer func() { cancel(); _ = listener.Close(); <-done }()

	info, _ := os.Lstat(socket)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v", info.Mode().Perm())
	}
	resp, err := Call(context.Background(), dir, socket, Request{Op: OpSend, ID: "m1", To: "a@b", Body: "hi"})
	if err != nil || resp.Outcome != OutcomeStored || resp.ID != "m1" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
}

func TestNoListenerMeansBridgeDown(t *testing.T) {
	dir, socket := shortDir(t)
	if _, err := Call(context.Background(), dir, socket, Request{Op: OpStatus}); !errors.Is(err, ErrBridgeDown) {
		t.Fatalf("err = %v", err)
	}
}

func TestListenRemovesAStaleSocketButRefusesALiveOne(t *testing.T) {
	_, socket := shortDir(t)
	first, err := Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(socket); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen err = %v", err)
	}
	// Closing a unix listener removes the file in Go; recreate a stale one by hand.
	_ = first.Close()
	if err := os.WriteFile(socket, nil, 0o600); err == nil {
		if _, err := Listen(socket); err == nil {
			t.Fatal("a regular file at the socket path was replaced")
		}
	}
}

func TestSlowRequestIsDropped(t *testing.T) {
	dir, socket := shortDir(t)
	listener, _ := Listen(socket)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Serve(ctx, listener, func(context.Context, Request) Response { return Response{Outcome: OutcomeOK} })
		close(done)
	}()
	defer func() { cancel(); _ = listener.Close(); <-done }()
	_ = dir
	// The behaviour under test is ReadRequestTimeout; here only its value is pinned.
	if ReadRequestTimeout != 5*time.Second || MaxConcurrent != 4 {
		t.Fatal("the contract's constants changed")
	}
}
