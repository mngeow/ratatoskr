package daemon

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSameFileTellsTheLogFromAnythingElse: the daemon reports its log path only when stderr is
// that very file, so a detached child reports it and a foreground run does not.
func TestSameFileTellsTheLogFromAnythingElse(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "daemon.log")
	other := filepath.Join(dir, "other.log")

	opened, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = opened.Close() }()
	if err := os.WriteFile(other, nil, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !sameFile(opened, logFile) {
		t.Fatal("an open file was not recognised as its own path")
	}
	if sameFile(opened, other) {
		t.Fatal("an open file matched a different file")
	}
	if sameFile(opened, filepath.Join(dir, "missing.log")) {
		t.Fatal("an open file matched a path that does not exist")
	}
	if sameFile(nil, logFile) {
		t.Fatal("a nil file matched")
	}
}

// TestAnOldLogDoesNotCountAsTheLog: a leftover daemon.log from an earlier run must not make a
// foreground daemon claim it is logging there.
func TestAnOldLogDoesNotCountAsTheLog(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(old, []byte("earlier run\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	stand, err := os.CreateTemp(dir, "stderr-")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	defer func() { _ = stand.Close() }()

	if sameFile(stand, old) {
		t.Fatal("a different open file matched an old log")
	}
}

// TestProcessExists: signal 0 reports this process, and no pid at all is not a process.
func TestProcessExists(t *testing.T) {
	if !processExists(os.Getpid()) {
		t.Fatal("this process was not found")
	}
	if processExists(0) || processExists(-1) {
		t.Fatal("a non-positive pid was reported as a process")
	}
}

// TestTheClientWaitsOutTheDaemonsShutdown pins the sizing the stop and stop --session waits
// rest on: each must outlast the worst case of what the daemon does before it answers or exits.
func TestTheClientWaitsOutTheDaemonsShutdown(t *testing.T) {
	if stopWait <= shutdownWorstCase {
		t.Fatalf("stopWait %v does not outlast the daemon's worst shutdown %v", stopWait, shutdownWorstCase)
	}
	if want := byeGrace + 8*shutdownBudget + closeGrace; shutdownWorstCase != want {
		t.Fatalf("shutdownWorstCase = %v, want %v: update it when a shutdown step changes", shutdownWorstCase, want)
	}
	if stopSessionTimeout <= shutdownBudget+closeGrace {
		t.Fatalf("stopSessionTimeout %v does not outlast one End (%v)", stopSessionTimeout, shutdownBudget+closeGrace)
	}
	if stopSessionTimeout <= controlTimeout {
		t.Fatalf("stopSessionTimeout %v is not longer than a status request (%v)", stopSessionTimeout, controlTimeout)
	}
	if stopWait < 20*time.Second {
		t.Fatalf("stopWait %v is implausibly short", stopWait)
	}
}
