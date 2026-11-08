package card

// Untested: under GR-3 nothing here has been run. Every case drives THIS TEST BINARY as the command - never
// claude, never codex, never a shell - through the helper-process pattern, and nothing touches a model or
// the network.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// helperEnvVar selects the helper's behaviour when the test binary is re-run as a command.
const helperEnvVar = "RTK_DRAFT_HELPER"

// TestHelperProcess is the command the tests run. It only acts when the env var is set.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnvVar)
	if mode == "" {
		// An ordinary test run: this is not the helper.
		return
	}

	switch mode {
	case "echo":
		payload, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.Write(payload)
	case "big":
		_, _ = os.Stdout.Write(bytes.Repeat([]byte("x"), 100<<10))
	case "sleep":
		time.Sleep(30 * time.Second)
	case "exit3":
		// The marker must never reach the caller's error text.
		_, _ = os.Stderr.WriteString("SECRET-STDERR")
		os.Exit(3)
	case "empty":
		os.Exit(0)
	case "envcount":
		entries := os.Environ()
		empty := "not-empty"
		if names, err := os.ReadDir("."); err == nil && len(names) == 0 {
			empty = "empty"
		}
		_, _ = os.Stdout.WriteString(itoaForTest(len(entries)) + " " + empty)
	default:
		os.Exit(4)
	}

	// The helper must not run the rest of the test binary.
	os.Exit(0)
}

// itoaForTest renders an int without importing strconv for one call.
func itoaForTest(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 6)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// helperSpec builds a spec that runs this test binary in the given mode.
func helperSpec(t *testing.T, mode string, timeout time.Duration, maxOutput int) CommandSpec {
	t.Helper()

	binary := testBinaryPath(t)
	return CommandSpec{
		Argv: []string{binary, "-test.run=TestHelperProcess"},
		// Exactly two entries: the helper's mode and a PATH that cannot find anything, so a missing env
		// would be visible as a different count.
		Env:       []string{helperEnvVar + "=" + mode, "PATH=/nonexistent"},
		Timeout:   timeout,
		MaxOutput: maxOutput,
		Label:     "test",
	}
}

// testBinaryPath is this test binary's absolute path, which is the command the drafter runs.
func testBinaryPath(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		if args := os.Args; len(args) > 0 && args[0] != "" {
			path = args[0]
		} else {
			t.Fatalf("cannot locate the test binary: %v", err)
		}
	}
	if !strings.ContainsRune(path, os.PathSeparator) {
		t.Fatalf("the test binary path is not a path: %q", path)
	}
	return path
}

// TestNewCommandDrafterRejects: the constructor's checks.
func TestNewCommandDrafterRejects(t *testing.T) {
	binary := testBinaryPath(t)

	cases := []struct {
		name string
		spec CommandSpec
	}{
		{name: "no argv", spec: CommandSpec{Timeout: time.Second, MaxOutput: 10}},
		{name: "empty program", spec: CommandSpec{Argv: []string{""}, Timeout: time.Second, MaxOutput: 10}},
		{name: "a NUL in the program", spec: CommandSpec{Argv: []string{"a\x00b"}, Timeout: time.Second, MaxOutput: 10}},
		{name: "no timeout", spec: CommandSpec{Argv: []string{binary}, MaxOutput: 10}},
		{name: "negative timeout", spec: CommandSpec{Argv: []string{binary}, Timeout: -time.Second, MaxOutput: 10}},
		{name: "no output limit", spec: CommandSpec{Argv: []string{binary}, Timeout: time.Second}},
		{name: "a missing program", spec: CommandSpec{Argv: []string{"/nonexistent/program"}, Timeout: time.Second, MaxOutput: 10}},
		{name: "a bare name with no PATH in the spec", spec: CommandSpec{
			Argv: []string{"definitely-not-a-program"}, Env: []string{"HOME=/tmp"}, Timeout: time.Second, MaxOutput: 10,
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if drafter, err := NewCommandDrafter(tc.spec); err == nil {
				t.Fatalf("NewCommandDrafter accepted %+v (got %v)", tc.spec, drafter)
			}
		})
	}

	t.Run("the resolved binary is absolute and the argv keeps argv[0]", func(t *testing.T) {
		spec := helperSpec(t, "echo", time.Second, 1024)
		drafter, err := NewCommandDrafter(spec)
		if err != nil {
			t.Fatalf("NewCommandDrafter: %v", err)
		}
		if !strings.HasPrefix(drafter.Binary(), "/") {
			t.Fatalf("binary = %q, want an absolute path", drafter.Binary())
		}
		argv := drafter.Argv()
		if len(argv) != len(spec.Argv) || argv[0] != spec.Argv[0] {
			t.Fatalf("argv = %v, want %v", argv, spec.Argv)
		}
		// The copy is its own slice.
		argv[0] = "changed"
		if drafter.Argv()[0] == "changed" {
			t.Fatal("Argv returned the internal slice")
		}
		if drafter.Name() != "test" {
			t.Fatalf("name = %q", drafter.Name())
		}
	})

	t.Run("a bare name is found through the spec's PATH", func(t *testing.T) {
		dir := t.TempDir()
		program := dir + "/a-program"
		if err := os.WriteFile(program, []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatalf("write: %v", err)
		}
		drafter, err := NewCommandDrafter(CommandSpec{
			Argv: []string{"a-program"}, Env: []string{"PATH=" + dir}, Timeout: time.Second, MaxOutput: 10,
		})
		if err != nil {
			t.Fatalf("NewCommandDrafter: %v", err)
		}
		if drafter.Binary() != program {
			t.Fatalf("binary = %q, want %q", drafter.Binary(), program)
		}
	})
}

// TestDraftSuccess: stdout comes back exactly, and the working directory is empty while it runs.
func TestDraftSuccess(t *testing.T) {
	drafter, err := NewCommandDrafter(helperSpec(t, "echo", 10*time.Second, 4096))
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	payload := []byte("hello draft\nsecond line\n")
	output, err := drafter.Draft(context.Background(), payload)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if !bytes.Equal(output, payload) {
		t.Fatalf("output = %q, want %q", output, payload)
	}
}

// TestDraftTooLarge: more than MaxOutput stops the command.
func TestDraftTooLarge(t *testing.T) {
	drafter, err := NewCommandDrafter(helperSpec(t, "big", 10*time.Second, 1024))
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	output, err := drafter.Draft(context.Background(), nil)
	if !errors.Is(err, ErrDraftTooLarge) {
		t.Fatalf("error = %v, want ErrDraftTooLarge", err)
	}
	if output != nil {
		t.Fatalf("a capped draft returned %d bytes", len(output))
	}
}

// TestDraftTimeout: a command that overruns is stopped and reported as a timeout.
func TestDraftTimeout(t *testing.T) {
	drafter, err := NewCommandDrafter(helperSpec(t, "sleep", 200*time.Millisecond, 1024))
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	started := time.Now()
	_, err = drafter.Draft(context.Background(), nil)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrDraftTimeout) {
		t.Fatalf("error = %v, want ErrDraftTimeout", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("the timeout took %v to return", elapsed)
	}
}

// TestDraftCancelled: a cancelled parent context is a cancellation, not a timeout.
func TestDraftCancelled(t *testing.T) {
	drafter, err := NewCommandDrafter(helperSpec(t, "sleep", 30*time.Second, 1024))
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, err = drafter.Draft(ctx, nil)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrDraftCancelled) {
		t.Fatalf("error = %v, want ErrDraftCancelled", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("cancellation took %v to return", elapsed)
	}
}

// TestDraftFailureLeaksNothing: a non-zero exit is a failure whose text carries nothing the command wrote.
func TestDraftFailureLeaksNothing(t *testing.T) {
	spec := helperSpec(t, "exit3", 10*time.Second, 1024)
	spec.Argv = append(spec.Argv, "ARGV-MARKER")
	drafter, err := NewCommandDrafter(spec)
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	_, err = drafter.Draft(context.Background(), []byte("PAYLOAD-MARKER"))
	if !errors.Is(err, ErrDraftFailed) {
		t.Fatalf("error = %v, want ErrDraftFailed", err)
	}
	for _, forbidden := range []string{"SECRET-STDERR", "ARGV-MARKER", "PAYLOAD-MARKER", "exit status", "signal"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("the error text %q contains %q", err.Error(), forbidden)
		}
	}
}

// TestDraftEmpty: no output is its own answer.
func TestDraftEmpty(t *testing.T) {
	drafter, err := NewCommandDrafter(helperSpec(t, "empty", 10*time.Second, 1024))
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}
	if _, err := drafter.Draft(context.Background(), nil); !errors.Is(err, ErrDraftEmpty) {
		t.Fatalf("error = %v, want ErrDraftEmpty", err)
	}
}

// TestDraftInheritsNoEnvironmentAndRunsInAnEmptyDirectory: the helper counts its own environment and looks
// at its working directory.
func TestDraftInheritsNoEnvironmentAndRunsInAnEmptyDirectory(t *testing.T) {
	drafter, err := NewCommandDrafter(helperSpec(t, "envcount", 10*time.Second, 1024))
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	output, err := drafter.Draft(context.Background(), nil)
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}

	// Exactly the two entries the spec set - neither the daemon's environment nor anything added.
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		t.Fatalf("helper output = %q", output)
	}
	if fields[0] != "2" {
		t.Fatalf("the command saw %s environment entries, want 2", fields[0])
	}
	if fields[1] != "empty" {
		t.Fatalf("the working directory was %s", fields[1])
	}
}

// TestDraftSentinelsAreStable: the sentences are part of the contract.
func TestDraftSentinelsAreStable(t *testing.T) {
	want := map[error]string{
		ErrDraftTimeout:   "the command ran past its time limit and was stopped",
		ErrDraftTooLarge:  "the command wrote more output than allowed and was stopped",
		ErrDraftCancelled: "the draft was cancelled",
		ErrDraftStart:     "the command could not be started",
		ErrDraftFailed:    "the command exited with an error",
		ErrDraftEmpty:     "the command wrote no output",
	}
	for sentinel, text := range want {
		if sentinel.Error() != text {
			t.Fatalf("sentinel = %q, want %q", sentinel.Error(), text)
		}
	}
}

// TestDraftDoesNotUseAShell: an argument with shell metacharacters is passed through as one argument.
func TestDraftDoesNotUseAShell(t *testing.T) {
	spec := helperSpec(t, "echo", 10*time.Second, 1024)
	spec.Argv = append(spec.Argv, "; echo SHOULD-NOT-APPEAR")
	drafter, err := NewCommandDrafter(spec)
	if err != nil {
		t.Fatalf("NewCommandDrafter: %v", err)
	}

	output, err := drafter.Draft(context.Background(), []byte("payload"))
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if strings.Contains(string(output), "SHOULD-NOT-APPEAR") {
		t.Fatalf("a shell ran: %q", output)
	}
	if string(output) != "payload" {
		t.Fatalf("output = %q", output)
	}
}
