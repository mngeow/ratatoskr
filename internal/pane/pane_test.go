package pane

// Tests are authored but UNRUN under GR-3. They use in-memory encoders and
// pipes, not real harnesses. No result is claimed.

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

type partialWriter struct {
	limit int
	fail  error
	data  []byte
}

func (w *partialWriter) Write(b []byte) (int, error) {
	n := min(w.limit, len(b))
	w.data = append(w.data, b[:n]...)
	return n, w.fail
}

func TestWriteFullCountsPartialAndStopsOnFailure(t *testing.T) {
	w := &partialWriter{limit: 2}
	n, err := writeFull(w, []byte("abcdef"))
	if err != nil || n != 6 || string(w.data) != "abcdef" {
		t.Fatalf("write: %d %v %q", n, err, w.data)
	}
	failure := errors.New("full")
	w = &partialWriter{limit: 2, fail: failure}
	n, err = writeFull(w, []byte("abcdef"))
	if n != 2 || !errors.Is(err, failure) {
		t.Fatalf("partial failure: %d %v", n, err)
	}
	n, err = writeFull(&partialWriter{}, []byte("a"))
	if n != 0 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero progress: %d %v", n, err)
	}
}

func TestFallbackEncodesControlAndNewline(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  uv.KeyPressEvent
		want string
	}{
		{"newline", uv.KeyPressEvent{Code: uv.KeyEnter, Mod: uv.ModShift}, "\n"},
		{"interrupt", uv.KeyPressEvent{Code: 'c', Text: "c", Mod: uv.ModCtrl | uv.ModCapsLock, IsRepeat: true}, "\x03"},
		{"escape", uv.KeyPressEvent{Code: uv.KeyEscape}, "\x1b"},
		{"enter", uv.KeyPressEvent{Code: uv.KeyEnter, Text: "\r"}, "\r"},
		{"shifted printable", uv.KeyPressEvent{Code: '1', Text: "!", Mod: uv.ModShift}, "!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := fallbackKey("codex", tc.key)
			if err != nil {
				t.Fatal(err)
			}
			emu := vt.NewEmulator(20, 5)
			defer emu.InputPipe().(io.Closer).Close()
			got := make(chan string, 1)
			go func() {
				b := make([]byte, len(tc.want))
				_, err := io.ReadFull(emu, b)
				if err != nil {
					got <- "read error"
					return
				}
				got <- string(b)
			}()
			emu.SendKey(key)
			if s := <-got; s != tc.want {
				t.Fatalf("got %q want %q", s, tc.want)
			}
		})
	}
	_, err := fallbackKey("codex", uv.KeyPressEvent{Code: uv.KeyUp, Mod: uv.ModCtrl})
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Fatalf("unsupported key: %v", err)
	}
	_, err = fallbackKey("other", uv.KeyPressEvent{Code: uv.KeyEnter, Mod: uv.ModShift})
	if !errors.Is(err, ErrUnsupportedKey) {
		t.Fatalf("unknown harness newline: %v", err)
	}
}

// pipePane deliberately has no process, emulator or background reader. It
// isolates the message write contract from OS PTY/harness behavior.
func pipePane(t *testing.T) (*Pane, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	if err := r.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := &Pane{master: w, inputMu: newCtxMutex(), writeMu: newCtxMutex(), lifeCtx: ctx, lifeCancel: cancel, emu: vt.NewEmulator(20, 5), emulatorJobs: make(chan emulatorJob, 64)}
	p.initTransport()
	p.emu.SetCallbacks(vt.Callbacks{EnableMode: func(mode ansi.Mode) {
		p.trackMode(mode, true)
		if mode == ansi.ModeBracketedPaste {
			p.bracketed.Store(true)
		}
	}, DisableMode: func(mode ansi.Mode) {
		p.trackMode(mode, false)
		if mode == ansi.ModeBracketedPaste {
			p.bracketed.Store(false)
		}
	}})
	p.wg.Add(3)
	go p.writerLoop()
	go p.replyLoop()
	go p.emulatorLoop()
	t.Cleanup(func() { cancel(); p.closeEmulatorInput(); w.Close(); p.wg.Wait() })
	return p, r
}

func TestPasteSubmitRefusesEscapeWithoutWriting(t *testing.T) {
	p, _ := pipePane(t)
	err := p.PasteSubmit(context.Background(), "unsafe\x1b[201~")
	var we *WriteError
	if !errors.As(err, &we) || we.BytesWritten() || we.ByteCount() != 0 {
		t.Fatalf("refusal: %v", err)
	}
}

func TestPasteSubmitCancellationAfterPasteReportsBytesAndNoEnter(t *testing.T) {
	p, r := pipePane(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	frame := bracketedPasteStart + "hello" + bracketedPasteEnd
	read := make(chan error, 1)
	go func() {
		b := make([]byte, len(frame))
		_, err := io.ReadFull(r, b)
		if err == nil && string(b) != frame {
			err = errors.New("incorrect frame")
		}
		cancel()
		read <- err
	}()
	err := p.PasteSubmit(ctx, "hello")
	if e := <-read; e != nil {
		t.Fatal(e)
	}
	var we *WriteError
	if !errors.As(err, &we) || !we.BytesWritten() || we.ByteCount() != len(frame) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := r.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	n, _ := r.Read(b)
	if n != 0 {
		t.Fatalf("late Enter/input %q", b[:n])
	}
}

func TestPasteSubmitAlreadyCanceledHasNoBytes(t *testing.T) {
	p, _ := pipePane(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := p.PasteSubmit(ctx, "hello")
	var we *WriteError
	if !errors.As(err, &we) || we.BytesWritten() || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel: %v", err)
	}
}

func TestKeyWaitsForTransportBeforeReturning(t *testing.T) {
	p, r := pipePane(t)
	done := make(chan error, 1)
	go func() { done <- p.SendKey(uv.KeyPressEvent{Code: uv.KeyEnter}) }()
	b := make([]byte, 1)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	if b[0] != '\r' {
		t.Fatalf("Enter %q", b)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("transport acknowledgement stalled")
	}
}

func TestInvalidLaunchDoesNotStartAProcess(t *testing.T) {
	for _, spec := range []protocol.LaunchSpec{
		{}, {Argv: []string{"unused"}, Dir: "/", Env: nil},
		{Argv: []string{"unused"}, Dir: "relative", Env: []string{}},
	} {
		if p, err := Start(spec, 80, 24, nil); err == nil || p != nil {
			t.Fatalf("accepted invalid spec: %+v", spec)
		}
	}
	if err := validateSize(65535, 65535); err == nil {
		t.Fatal("unbounded screen allocation")
	}
	if err := validateSize(80, 24); err != nil {
		t.Fatal(err)
	}
	we := &WriteError{Op: "test", Written: 7, Err: io.ErrShortWrite}
	if !strings.Contains(we.Error(), "7 bytes") || !errors.Is(we, io.ErrShortWrite) {
		t.Fatal(we)
	}
}

func TestShutdownSignalsBeforeForceKill(t *testing.T) {
	exited := make(chan struct{})
	var calls []string
	err := stopOwnedChild(context.Background(), exited, time.Second, func() error { calls = append(calls, "term"); close(exited); return nil }, func() error { calls = append(calls, "kill"); return nil })
	if err != nil || strings.Join(calls, ",") != "term" {
		t.Fatalf("graceful order: %v %v", calls, err)
	}
}

func TestCanceledShutdownForceKillsOnlyThroughOwnedCallback(t *testing.T) {
	exited := make(chan struct{})
	var calls []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := stopOwnedChild(ctx, exited, time.Second, func() error { calls = append(calls, "term"); return nil }, func() error { calls = append(calls, "kill"); close(exited); return nil })
	if err != nil || strings.Join(calls, ",") != "term,kill" {
		t.Fatalf("cancel order: %v %v", calls, err)
	}
}

func TestCloseIsIdempotentWithAlreadyExitedStub(t *testing.T) {
	p, _ := pipePane(t)
	p.cmd = &exec.Cmd{}
	p.exitedCh = make(chan struct{})
	close(p.exitedCh)
	p.exited.Store(true)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
}
