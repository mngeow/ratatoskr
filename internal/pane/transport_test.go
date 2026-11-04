package pane

// Authored, UNRUN under GR-3. Pipe/stub-only regression cases; no harness or PTY.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"
)

func TestEmptyEncodingIsNotACompletionBarrier(t *testing.T) {
	p, r := pipePane(t)
	err := p.sendEncoded(func() int32 { return 1 }, func() error {
		p.emu.SendKey(uv.KeyPressEvent{Code: uv.KeyEnter, Mod: uv.ModCtrl | uv.ModShift})
		return nil
	})
	if !errors.Is(err, ErrEmptyEncoding) {
		t.Fatalf("empty encoding: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- p.SendKey(uv.KeyPressEvent{Code: uv.KeyEnter}) }()
	b := make([]byte, 1)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	if b[0] != '\r' {
		t.Fatalf("next input %q", b)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnexpectedEmptyQueryDoesNotWaitForAReceiver(t *testing.T) {
	p, _ := pipePane(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.emulatorCall(ctx, func() error { _, err := p.emu.InputPipe().Write(nil); return err }); err != nil {
		t.Fatal(err)
	}
	if p.drainDead.Load() {
		t.Fatal("empty output killed drain")
	}
}

func TestQueueBackpressureDoesNotDisableInput(t *testing.T) {
	p := &Pane{}
	p.initTransport()
	for range transportSlots {
		p.enqueueFromPipe(transportItem{data: []byte("q")})
	}
	batch := newInputBatch()
	p.enqueueFromPipe(transportItem{data: []byte("x"), batch: batch})
	if !errors.Is(batch.result(), ErrInputBackpressure) || !p.InputReady() {
		t.Fatal("overflow disabled input or was silent")
	}
}

func TestSnapshotDoesNotCallBlockedEmulator(t *testing.T) {
	p, _ := pipePane(t)
	want := Frame{Content: "last good", Hidden: true}
	p.lastFrame.Store(&want)
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(release) })
	p.emulatorJobs <- emulatorJob{ctx: context.Background(), done: make(chan error, 1), run: func() error { close(started); <-release; return nil }}
	<-started
	if got := p.Snapshot(); got.Content != want.Content {
		t.Fatalf("cache %q", got.Content)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := p.emulatorCall(ctx, func() error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked emulator %v", err)
	}
	if p.InputReady() {
		t.Fatal("timed-out emulator input still ready")
	}
}

func TestMouseTrackerResetAndModeFiltering(t *testing.T) {
	p, _ := pipePane(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	write := func(s string) {
		t.Helper()
		if err := p.emulatorCall(ctx, func() error { _, e := p.emu.Write([]byte(s)); return e }); err != nil {
			t.Fatal(err)
		}
	}
	write("\x1b[?1000h")
	press := uv.MouseClickEvent{Button: uv.MouseLeft}
	motion := uv.MouseMotionEvent{Button: uv.MouseLeft}
	if !p.MouseEnabled(press) || p.MouseEnabled(motion) {
		t.Fatal("normal tracking motion filter")
	}
	write("\x1b[?1002h")
	if !p.MouseEnabled(motion) {
		t.Fatal("button motion missing")
	}
	write("\x1bc")
	if p.MouseEnabled(press) || p.mouseModes.Load() != 0 {
		t.Fatal("RIS left stale mouse mode")
	}
	if e := clampMouse(uv.MouseClickEvent{X: 999, Y: 999}, false).Mouse(); e.X != 222 || e.Y != 222 {
		t.Fatal("legacy coordinate overflow")
	}
	p.trackMode(ansi.ModeMouseX10, true)
	if p.MouseEnabled(uv.MouseReleaseEvent{}) || !p.MouseEnabled(uv.MouseWheelEvent{Button: uv.MouseWheelUp}) {
		t.Fatal("X10 eligibility")
	}
}

func TestOwnedGroupAndExitCodeRules(t *testing.T) {
	if ownedGroupTarget(500, 500, 500, true) != -500 {
		t.Fatal("owned group")
	}
	for _, tc := range [][4]int{{500, 499, 500, 1}, {500, 500, 499, 1}, {500, 500, 500, 0}, {1, 1, 1, 1}} {
		if ownedGroupTarget(tc[0], tc[1], tc[2], tc[3] != 0) < 0 {
			t.Fatal("unowned group")
		}
	}
	if ownedExitCode(unix.WaitStatus(7<<8)) != 7 || ownedExitCode(unix.WaitStatus(unix.SIGTERM)) != -1 {
		t.Fatal("exit-code mapping")
	}
}

func TestBracketedPasteCaptureHasThreeWrites(t *testing.T) {
	p, r := pipePane(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := p.emulatorCall(ctx, func() error { _, e := p.emu.Write([]byte("\x1b[?2004h")); return e }); err != nil {
		t.Fatal(err)
	}
	want := bracketedPasteStart + strings.Repeat("x", 8192) + bracketedPasteEnd
	done := make(chan error, 1)
	go func() { done <- p.Paste(strings.Repeat("x", 8192)) }()
	b := make([]byte, len(want))
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatal("paste ordering")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
