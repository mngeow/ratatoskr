package attach

// Authored but UNRUN under GR-3. Fake windows and SSH only; no terminal operations.
import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/entry"
	"github.com/mngeow/ratatoskr/internal/tui"
	"golang.org/x/sys/unix"
)

func TestSSHStopConfigurationAndTermiosRestoration(t *testing.T) {
	command := exec.CommandContext(context.Background(), "ssh")
	configureSSHStop(command)
	if command.Cancel == nil || command.WaitDelay != sshStopGrace {
		t.Fatal("missing graceful cancellation or bounded kill fallback")
	}
	for _, failed := range []bool{false, true} {
		for _, restoreFails := range []bool{false, true} {
			calls := 0
			waitErr := errors.New("ssh terminated")
			err, fatal := waitSSH(func() error {
				if failed {
					return waitErr
				}
				return nil
			}, func() error {
				calls++
				if restoreFails {
					return errors.New("restore failed")
				}
				return nil
			})
			if failed && (calls != 1 || !errors.Is(err, waitErr) || errors.Is(fatal, errTerminalRestore) != restoreFails) {
				t.Fatal(calls, err, fatal)
			}
			if !failed && (calls != 0 || err != nil || fatal != nil) {
				t.Fatal("clean SSH exit touched termios", calls, err, fatal)
			}
		}
	}
}

func TestEntryReturnStartsNothingAndRestoresBookmark(t *testing.T) {
	for _, kind := range []string{entry.OutcomeOK, entry.OutcomeSSHFailed, entry.OutcomeCannotStart} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			bookmark := tui.NetworkBookmark{Kind: "remote", Host: "raw-host", ID: "raw-session"}
			ops := entryOperations{
				open: func(_ context.Context, o Options, resume *tui.NetworkBookmark, feedback string) (*tui.EntryIntent, error) {
					calls++
					if calls == 1 {
						if o.Agents != "codex" || resume != nil {
							t.Fatal(o, resume)
						}
						return &tui.EntryIntent{Selection: bookmark, Plan: wire.EnterPlan{Manual: "manual fallback"}}, nil
					}
					if calls != 2 || o.Agents != "" || o.ExpectSession != "" || o.Version != "build" || resume == nil || *resume != bookmark || !strings.Contains(feedback, "outcome") {
						t.Fatal(o, resume, feedback)
					}
					return nil, nil
				},
				ssh: func(context.Context, tui.EntryIntent) (entry.Outcome, error) {
					return entry.Outcome{Kind: kind, Message: "outcome"}, nil
				},
				report: func(entry.Outcome, tui.EntryIntent) error { return nil },
			}
			if err := entryWindows(context.Background(), Options{Version: "build", Agents: "codex"}, ops); err != nil || calls != 2 {
				t.Fatal(err, calls)
			}
		})
	}
}

func TestEntrySignalDoesNotReopenAndResetFailureStopsLoop(t *testing.T) {
	for _, signal := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		opens := 0
		failure := errors.New("reset failed")
		ops := entryOperations{
			open: func(context.Context, Options, *tui.NetworkBookmark, string) (*tui.EntryIntent, error) {
				opens++
				return &tui.EntryIntent{}, nil
			},
			ssh: func(context.Context, tui.EntryIntent) (entry.Outcome, error) {
				if signal {
					cancel()
				}
				return entry.Outcome{Kind: entry.OutcomeSSHFailed}, nil
			},
			report: func(entry.Outcome, tui.EntryIntent) error {
				if signal {
					return nil
				}
				return failure
			},
		}
		err := entryWindows(ctx, Options{}, ops)
		cancel()
		if opens != 1 || (!signal && !errors.Is(err, failure)) || (signal && err != nil) {
			t.Fatal(opens, err)
		}
	}
}

type shortResetWriter struct{}

func (shortResetWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestTargetedTerminalResetAndExactArgv(t *testing.T) {
	var buffer bytes.Buffer
	if err := writeReset(&buffer); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[<u\x1b[?25h\x1b[0m"
	if buffer.String() != want || strings.Contains(buffer.String(), "\x1bc") {
		t.Fatal("incorrect or full terminal reset")
	}
	if writeReset(shortResetWriter{}) == nil {
		t.Fatal("short reset accepted")
	}
	argv := []string{"ssh", "-t", "-o", "ConnectTimeout=5", "--", "alias", "rtk attach --expect-session id"}
	if !validSSHArgv(argv) {
		t.Fatal(argv)
	}
	for i := 0; i < 5; i++ {
		bad := append([]string(nil), argv...)
		bad[i] = "changed"
		if validSSHArgv(bad) {
			t.Fatal("bad fixed prefix accepted", bad)
		}
	}
	if validSSHArgv(argv[:6]) {
		t.Fatal("missing remote argument accepted")
	}
}

func TestHandoverEchoFlushRestoreAndFailures(t *testing.T) {
	for _, failure := range []string{"", "flush", "restore", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			original := unix.Termios{Lflag: unix.ECHO | unix.ECHONL | unix.ICANON}
			sets, flushed := 0, false
			tty := handoverTerminal{
				get: func(int) (*unix.Termios, error) { copied := original; return &copied, nil },
				set: func(_ int, term *unix.Termios) error {
					sets++
					if sets == 1 {
						if term.Lflag&(unix.ECHO|unix.ECHONL) != 0 || term.Lflag&unix.ICANON == 0 {
							t.Fatal("echo suppression altered other flags")
						}
						if failure == "cancel" {
							cancel()
						}
					} else {
						if *term != original {
							t.Fatal("original settings lost")
						}
						if failure == "restore" {
							return errors.New("restore")
						}
					}
					return nil
				},
				flush: func(int) error {
					flushed = true
					if failure == "flush" {
						return errors.New("flush")
					}
					return nil
				},
			}
			err := prepareTerminal(ctx, ctx, 0, tty, 0*time.Second)
			if sets != 2 || (failure == "" && err != nil) || (failure != "" && err == nil) || (failure == "cancel" && flushed) || (failure == "restore" && !errors.Is(err, errTerminalRestore)) {
				t.Fatal(sets, flushed, err)
			}
		})
	}
}
