package pane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Untested: under GR-3 (docs/design/ground-rules.md) nothing here has been run.
// Only gofmt, go vet and go build have been applied to it.

const (
	// maxDimension bounds a pane size in cells. Larger values are rejected
	// rather than truncated, because a zero or absurd size would make the
	// emulator and the child disagree about the screen.
	maxDimension = 65535

	// pasteEnterDelay separates the bracketed paste from the Enter that submits
	// it. The value is provisional and UNTESTED (GR-3): it exists so the delay
	// can be changed in one place once panes can be exercised.
	pasteEnterDelay = 50 * time.Millisecond

	// bracketedPasteStart and bracketedPasteEnd are the xterm bracketed paste
	// markers. They duplicate the values in charmbracelet/x/ansi, which is not
	// imported here so the package's dependency set stays as briefed.
	bracketedPasteStart = "\x1b[200~"
	bracketedPasteEnd   = "\x1b[201~"

	readBufferSize     = 32 * 1024
	emulatorBufferSize = 4096
	shutdownGrace      = 2 * time.Second // UNTESTED: allow harness session/tool cleanup before force-kill.

	// boundScrollback is the scrollback bound actually applied to the emulator.
	// See Start for why the value is one and not zero.
	boundScrollback = 1

	escRune = '\x1b'

	// harnessClaude, harnessClaudeCode and harnessCodex are the harness names
	// the Shift+Enter fallback applies to. Any other harness reports the key as
	// unsupported instead of guessing.
	harnessClaude     = "claude"
	harnessClaudeCode = "claude-code"
	harnessCodex      = "codex"
)

// ErrUnsupportedKey reports a key that x/vt v0.1.0 cannot encode. It is
// exported so the TUI can surface it on the status line instead of losing the
// keystroke; errors.Is and errors.As both work against it.
//
// This exists because (*vt.Emulator).SendKey returns nothing and silently emits
// an empty string for combinations its switch does not know (Shift+Enter above
// all), and emits a bare special rune for unmodified special keys it does not
// cover. Dropping input or writing junk to a child are both worse than an
// error the status line can show.
var ErrUnsupportedKey = errors.New("pane: key not supported by x/vt encoder")

// ErrPaneClosed reports use of a pane after Close.
var ErrPaneClosed = errors.New("pane: pane is closed")

// errInputDrainStopped reports input that cannot reach the child because the
// terminal reply drain has stopped. User input and terminal replies share one
// pipe, so a dead drain would otherwise block or silently drop a keystroke.
var errInputDrainStopped = errors.New("pane: input cannot reach the child; the reply drain has stopped")

// WriteError reports a PTY write that did not complete. BytesWritten is the
// number of bytes the PTY actually accepted for that write, never zero when
// some of it landed. Err is the underlying cause; Unwrap exposes it.
type WriteError struct {
	Op      string
	Written int
	Err     error
}

func (e *WriteError) Error() string {
	return fmt.Sprintf("pane: %s: wrote %d bytes: %v", e.Op, e.Written, e.Err)
}

// BytesWritten lets the router report ambiguous partial delivery without importing pane.
func (e *WriteError) BytesWritten() bool { return e.Written > 0 }

// ByteCount is the cumulative number of PTY bytes accepted in the transaction.
func (e *WriteError) ByteCount() int { return e.Written }

// Unwrap returns the underlying cause.
func (e *WriteError) Unwrap() error { return e.Err }

// Frame is one rendered pane. Content is exactly the emulator's Render output;
// X, Y and Hidden are the cursor position and visibility. There is no damage
// tracking, so App decides what part of Content to draw.
type Frame struct {
	Content string
	X, Y    int
	Hidden  bool
}

// Pane owns one PTY, one child process, one x/vt emulator and the
// serialization between them.
//
// The emulator has one owning worker, and Snapshot/Err/status use atomics.
// inputMu orders user/message transactions; writeMu orders actual PTY writes.
// Neither is entered by View or Update. lifeMu serializes owned signals/reaps.
type Pane struct {
	id      protocol.AgentID
	harness string

	master *os.File
	cmd    *exec.Cmd
	emu    *vt.Emulator

	writeMu ctxMutex
	inputMu ctxMutex
	lifeMu  sync.Mutex

	lifeCtx    context.Context
	lifeCancel context.CancelFunc

	wg             sync.WaitGroup
	exitedCh       chan struct{}
	exited         atomic.Bool
	closed         atomic.Bool
	drainDead      atomic.Bool
	mouseModes     atomic.Uint32
	desired        atomic.Uint64
	resizeWake     chan struct{}
	transport      chan transportItem
	queuedBytes    atomic.Int64
	encoding       atomic.Pointer[inputBatch]
	lastFrame      atomic.Pointer[Frame]
	emulatorJobs   chan emulatorJob
	emulatorDead   atomic.Bool
	emulatorBusyAt atomic.Int64
	bracketed      atomic.Bool
	errValue       atomic.Pointer[paneError]
	ownedPGID      int
	exitOnce       sync.Once
	onExit         func(int)
	closeOnce      sync.Once
	closeErr       error
	closeDone      chan struct{}
}

// Start launches one child on a new PTY and returns a Pane rendering it.
//
// spec must name a program (Argv), a complete environment (Env, never nil) and
// an absolute working directory. width and height are cells, at least 1 and at
// most 65535.
//
// onExit is called exactly once, after the child has been waited for, with its
// exit code, or -1 when the process was killed by a signal.
//
// Start fails if the PTY master cannot be given to the Go poller with working
// write deadlines. Without that, a write blocked on a full input buffer could
// not be cancelled and there would be no way to honour a context, so a failed
// probe aborts the start rather than degrading to writes that outlive their
// caller.
func Start(spec protocol.LaunchSpec, width, height int, onExit func(int)) (*Pane, error) {
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return nil, fmt.Errorf("pane: start: Argv must name a program")
	}
	if spec.Env == nil {
		return nil, fmt.Errorf("pane: start: Env must be explicit, not nil")
	}
	if !filepath.IsAbs(spec.Dir) {
		return nil, fmt.Errorf("pane: start: Dir %q must be absolute", spec.Dir)
	}
	if err := validateSize(width, height); err != nil {
		return nil, err
	}

	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...) //nolint:gosec // Argv is the harness to run, chosen by agent launch spec.
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env

	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}) //nolint:gosec // bounded by validateSize.
	if err != nil {
		return nil, fmt.Errorf("pane: start %s: %w", spec.DisplayName, err)
	}

	polled, err := pollerMaster(master)
	if err != nil {
		abortStart(master, cmd)
		return nil, fmt.Errorf("pane: start %s: %w", spec.DisplayName, err)
	}
	// The probe is what makes context cancellation of a blocked write possible.
	// A zero deadline means "no deadline" and must always be accepted on a
	// poller-owned descriptor.
	if err := polled.SetWriteDeadline(time.Time{}); err != nil {
		abortStart(polled, cmd)
		return nil, fmt.Errorf("pane: start %s: PTY writes cannot carry deadlines: %w", spec.DisplayName, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	p := &Pane{
		id:         spec.ID,
		harness:    strings.ToLower(strings.TrimSpace(spec.Harness)),
		master:     polled,
		cmd:        cmd,
		emu:        vt.NewEmulator(width, height),
		writeMu:    newCtxMutex(),
		inputMu:    newCtxMutex(),
		lifeCtx:    ctx,
		lifeCancel: cancel,
		exitedCh:   make(chan struct{}),
		resizeWake: make(chan struct{}, 1),
		onExit:     onExit,
	}
	p.initTransport()
	p.emulatorJobs = make(chan emulatorJob, 64)
	p.lastFrame.Store(&Frame{Hidden: true})
	if pgid, err := unix.Getpgid(cmd.Process.Pid); err == nil && pgid == cmd.Process.Pid {
		p.ownedPGID = pgid
	}
	p.desired.Store(packSize(width, height))
	// Bound scrollback to a single line. The obvious SetScrollbackSize(0) does
	// not disable it: NewScreen already creates a default-sized buffer, and
	// Scrollback.SetMaxLines ignores values <= 0, so zero is a no-op and the
	// default would stay. Emulator exposes no way to drop the buffer itself
	// (Screen.SetScrollback(nil) is not reachable from it), so one line is the
	// smallest bound this version allows.
	p.emu.SetScrollbackSize(boundScrollback)

	// The child tells us which mouse modes it wants through these callbacks, which
	// only record an atomic bitset. They are registered before any reader starts,
	// so no mode change can arrive before the tracker exists. Nothing else in this
	// package has registered callbacks; if that changes, this must merge rather
	// than replace.
	p.emu.SetCallbacks(vt.Callbacks{
		EnableMode: func(mode ansi.Mode) {
			p.trackMode(mode, true)
			if mode == ansi.ModeBracketedPaste {
				p.bracketed.Store(true)
			}
		},
		DisableMode: func(mode ansi.Mode) {
			p.trackMode(mode, false)
			if mode == ansi.ModeBracketedPaste {
				p.bracketed.Store(false)
			}
		},
	})

	p.wg.Add(5)
	go p.emulatorLoop()
	go p.writerLoop()
	go p.readLoop()
	go p.replyLoop()
	go p.resizeLoop()
	go p.waitLoop()
	return p, nil
}

// pollerMaster replaces the PTY master with a non-blocking duplicate so the Go
// runtime poller owns it.
//
// creack/pty v1.1.24 opens the master with os.NewFile on a blocking descriptor,
// and os.newFile only registers descriptors with the poller when O_NONBLOCK is
// already set. Handing it an O_NONBLOCK duplicate is what gives reads and, more
// importantly, writes a runtimeCtx, without which SetWriteDeadline fails with
// "file type does not support deadline". The duplicate shares the open file
// description, so the pty is the same one the child holds; the original is
// closed here and must not be used again.
func pollerMaster(master *os.File) (*os.File, error) {
	raw, err := unix.Dup(int(master.Fd()))
	if err != nil {
		return nil, fmt.Errorf("dup PTY master: %w", err)
	}
	unix.CloseOnExec(raw)
	if err := unix.SetNonblock(raw, true); err != nil {
		_ = unix.Close(raw)
		return nil, fmt.Errorf("set PTY master non-blocking: %w", err)
	}
	polled := os.NewFile(uintptr(raw), master.Name())
	if polled == nil {
		_ = unix.Close(raw)
		return nil, fmt.Errorf("reopen PTY master as a polled file")
	}
	if err := master.Close(); err != nil {
		_ = polled.Close()
		return nil, fmt.Errorf("close blocking PTY master: %w", err)
	}
	return polled, nil
}

// abortStart tears down a start that failed after the child had been launched.
// It only ever touches the child this call started.
func abortStart(master *os.File, cmd *exec.Cmd) {
	_ = master.Close()
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
}

// readLoop moves child output into the emulator.
func (p *Pane) readLoop() {
	defer p.wg.Done()
	buf := make([]byte, readBufferSize)
	for {
		n, err := p.master.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			ctx, cancel := context.WithTimeout(p.lifeCtx, paneOperationLimit)
			emuErr := p.emulatorCall(ctx, func() error { _, e := p.emu.Write(data); return e })
			cancel()
			if emuErr != nil {
				if p.lifeCtx.Err() == nil {
					p.setError(emuErr)
				}
				return
			}
		}
		if err != nil {
			// A child that exits makes the master read fail with EIO; that is
			// expected and must not be reported as a pane error.
			p.recordError("read pty", err)
			return
		}
	}
}

// waitLoop reaps the child and reports its exit exactly once.
// Resize sets the emulator and the PTY to the given size. After the child has
// exited only the emulator changes; the stale frame keeps its own dimensions
// and there is no error, because there is no surviving process to inform.
func (p *Pane) Resize(width, height int) error {
	if err := validateSize(width, height); err != nil {
		return err
	}
	if err := p.checkOpen(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(p.lifeCtx, paneOperationLimit)
	defer cancel()
	if err := p.emulatorCall(ctx, func() error { p.emu.Resize(width, height); return nil }); err != nil {
		return err
	}

	if p.exited.Load() {
		return nil
	}

	p.lifeMu.Lock()
	defer p.lifeMu.Unlock()
	if p.exited.Load() || p.closed.Load() {
		return nil
	}
	if err := pty.Setsize(p.master, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)}); err != nil { //nolint:gosec // bounded by validateSize.
		if p.closed.Load() || p.exited.Load() || p.isShutdownErr(err) {
			return nil
		}
		return fmt.Errorf("pane: resize %s: %w", p.id, err)
	}
	return nil
}

// Snapshot returns the current frame. It is safe to call after the child exits:
// the last output stays in the emulator.
func (p *Pane) Snapshot() Frame {
	if cached := p.lastFrame.Load(); cached != nil {
		return *cached
	}
	return Frame{Hidden: true}
}

// SendKey forwards one key to the focused child.
//
// Shift+Enter goes through fallbackKey. Any other combination the pinned
// encoder cannot express returns an error wrapping ErrUnsupportedKey instead of
// being dropped or written as junk.
func (p *Pane) SendKey(key uv.KeyPressEvent) error {
	if len(key.Text) > userPasteLimit {
		return fmt.Errorf("pane: key text exceeds %d-byte input limit", userPasteLimit)
	}
	mapped, err := fallbackKey(p.harness, key)
	if err != nil {
		return err
	}
	return p.sendEncoded(func() int32 { return 1 }, func() error { p.emu.SendKey(mapped); return nil })
}

// Paste retains the child's bracketed-paste mode and returns only after its
// bytes complete, without holding the screen lock during PTY progress. UNTESTED.
func (p *Pane) Paste(text string) error {
	if text == "" {
		return p.checkInput()
	}
	if len(text) > userPasteLimit {
		return fmt.Errorf("pane: paste exceeds %d-byte input limit", userPasteLimit)
	}
	return p.sendEncoded(func() int32 {
		if p.bracketed.Load() {
			return 3
		}
		return 1
	}, func() error { p.emu.Paste(text); return nil })
}

// PasteSubmit delivers one message into the child: a single bracketed paste,
// then Enter as a separate write.
//
// It is deliberately separate from Paste. Text containing ESC is refused before
// anything is written, because ESC is how a message could smuggle terminal
// control into the child. Cancellation is honoured while waiting for the input
// transaction, during the paste write and during the delay, and Enter is never
// written after ctx has ended.
func (p *Pane) PasteSubmit(ctx context.Context, text string) error {
	written := 0
	fail := func(err error) error { return &WriteError{Op: "paste submit", Written: written, Err: err} }
	if ctx == nil {
		return fail(errors.New("nil context"))
	}
	if err := p.checkInput(); err != nil {
		return fail(err)
	}
	if strings.ContainsRune(text, escRune) {
		return fail(errors.New("refusing text containing ESC"))
	}

	if err := p.inputMu.lock(ctx); err != nil {
		return fail(err)
	}
	defer p.inputMu.unlock()
	if err := p.checkInput(); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := p.fenceTransport(ctx); err != nil {
		return fail(err)
	}
	// Keep queries and user input out of the entire paste/delay/Enter sequence.
	if err := p.writeMu.lock(ctx); err != nil {
		return fail(err)
	}
	defer p.writeMu.unlock()

	frame := bracketedPasteStart + text + bracketedPasteEnd
	n, err := p.writeWithDeadline(ctx, []byte(frame))
	written += n
	if err != nil {
		p.noteTransportError(err)
		return fail(err)
	}

	if err := sleepContext(ctx, pasteEnterDelay); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	n, err = p.writeWithDeadline(ctx, []byte("\r"))
	written += n
	if err != nil {
		p.noteTransportError(err)
		return fail(err)
	}
	return nil
}

// Err returns the first transport error the pane saw, or nil. Shutdown and
// expected post-exit errors are not recorded.
func (p *Pane) Err() error {
	if v := p.errValue.Load(); v != nil {
		return v.err
	}
	return nil
}

// Close stops the child this pane started, waits for it and for the pane's
// goroutines, and releases the PTY. It is idempotent: later calls return the
// first call's error.
func (p *Pane) Close() error {
	return p.CloseContext(context.Background())
}

// CloseContext waits for the shared shutdown. Cancellation shortens the grace
// period; cleanup still closes the PTY and reaps only this pane's owned child.
// A canceled caller may return before that shared cleanup completes. UNTESTED.
func (p *Pane) CloseContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("pane: close: nil context")
	}
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		p.closeDone = make(chan struct{})
		go func() { p.closeErr = p.close(ctx); close(p.closeDone) }()
	})
	select {
	case <-p.closeDone:
		return p.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Pane) close(ctx context.Context) error {
	p.closed.Store(true)
	p.lifeCancel()

	// Unblock the emulator's Read. Emulator.Close is not called: it flips a
	// bool that Read reads without synchronization, so closing the input pipe's
	// closer is the documented way out of Read.
	var closeErr error
	if err := p.closeEmulatorInput(); err != nil && !p.isShutdownErr(err) {
		closeErr = fmt.Errorf("pane: close %s: emulator input pipe: %w", p.id, err)
	}

	// SIGTERM first, while output is still drained, lets the owned harness
	// flush session history and stop tools in the verified child-owned group.
	if p.cmd.Process != nil && !p.exited.Load() {
		closeErr = errors.Join(closeErr, stopOwnedChild(ctx, p.exitedCh, shutdownGrace,
			func() error { return p.signalOwned(unix.SIGTERM) }, func() error { return p.signalOwned(unix.SIGKILL) }))
	}
	p.lifeMu.Lock()
	// Closing the master releases a reader or writer blocked in the poller and
	// lets waitLoop and the two loops finish.
	if err := p.master.Close(); err != nil && !p.isShutdownErr(err) && closeErr == nil {
		closeErr = fmt.Errorf("pane: close %s: pty master: %w", p.id, err)
	}
	p.lifeMu.Unlock()

	// Master close also releases a still-running foreground reader with HUP.
	// Wait remains owned by waitLoop even if a pathological child outlives us.
	joined := make(chan struct{})
	go func() { p.wg.Wait(); close(joined) }()
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	select {
	case <-joined:
	case <-timer.C:
		closeErr = errors.Join(closeErr, fmt.Errorf("pane: shutdown I/O did not finish within grace period: %w", context.DeadlineExceeded))
	}
	return closeErr
}

// stopOwnedChild isolates timing/signal policy so unrun tests can use stubs.
// After force-kill, the existing waitLoop is still responsible for reaping.
func stopOwnedChild(ctx context.Context, exited <-chan struct{}, grace time.Duration, terminate, kill func() error) error {
	select {
	case <-exited:
		return nil
	default:
	}
	termErr := terminate()
	if errors.Is(termErr, os.ErrProcessDone) {
		termErr = nil
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-exited:
		return termErr
	case <-timer.C:
	case <-ctx.Done():
	}
	killErr := kill()
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	reaped := time.NewTimer(grace)
	defer reaped.Stop()
	select {
	case <-exited:
		return errors.Join(termErr, killErr)
	case <-reaped.C:
		return errors.Join(termErr, killErr, fmt.Errorf("pane: owned child did not exit within force-kill grace period: %w", context.DeadlineExceeded))
	}
}

// writePTY serializes one PTY write and applies the caller's context to it.
func (p *Pane) writePTY(ctx context.Context, op string, b []byte) error {
	if err := p.writeMu.lock(ctx); err != nil {
		return err
	}
	defer p.writeMu.unlock()

	n, err := p.writeWithDeadline(ctx, b)
	if err != nil {
		return &WriteError{Op: op, Written: n, Err: err}
	}
	return nil
}

// writeWithDeadline writes b, using the context to interrupt a write that is
// blocked because the child is not reading.
//
// The deadline is process-wide state on the file, so the caller must hold
// writeMu for the whole call, including the reset. The AfterFunc is awaited
// before the reset, otherwise a late callback could expire the next write.
func (p *Pane) writeWithDeadline(ctx context.Context, b []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	settled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(settled)
		// Expiring now interrupts a write blocked on a full input buffer.
		_ = p.master.SetWriteDeadline(time.Now())
	})

	n, err := writeFull(p.master, b)
	if !stop() {
		<-settled
	}
	if resetErr := p.master.SetWriteDeadline(time.Time{}); resetErr != nil && err == nil {
		err = fmt.Errorf("reset PTY write deadline: %w", resetErr)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}
		return n, err
	}
	if err := ctx.Err(); err != nil {
		return n, err
	}
	return n, nil
}

// writeFull writes all of b, tolerating partial writes and refusing to spin on
// a write that makes no progress.
func writeFull(w io.Writer, b []byte) (int, error) {
	written := 0
	for written < len(b) {
		n, err := w.Write(b[written:])
		written += n
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

// fallbackKey maps a key onto something the pinned encoder can express, or
// reports it as unsupported.
//
// Shift+Enter is a known gap in x/vt v0.1.0: the switch has no modified Enter
// case and the printable fallback needs an empty modifier, so it writes nothing.
// Claude documents Ctrl+J as its newline binding and Codex's upstream textarea
// keymap binds Ctrl+J to insert_newline; both are UNTESTED here (GR-3).
// Sources: https://code.claude.com/docs/en/terminal-config#enter-multiline-prompts
// and https://github.com/openai/codex/blob/main/codex-rs/tui/src/keymap.rs
func fallbackKey(harness string, key uv.KeyPressEvent) (uv.KeyPressEvent, error) {
	// Lock/repeat/text fields participate in x/vt's struct switch. Normalize
	// fields that must not turn a Ctrl+C or Enter into an unmatched key.
	k := uv.Key(key)
	k.Mod &^= uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock
	k.IsRepeat = false
	k.BaseCode, k.ShiftedCode = 0, 0
	if k.Mod&uv.ModCtrl != 0 || isSpecialCode(k.Code) || k.Code < 32 || k.Code == 127 {
		k.Text = ""
	}
	key = uv.KeyPressEvent(k)
	if isShiftEnter(key) {
		switch harness {
		case harnessClaude, harnessClaudeCode, harnessCodex:
			return uv.KeyPressEvent{Code: 'j', Mod: uv.ModCtrl}, nil
		default:
			return uv.KeyPressEvent{}, fmt.Errorf("%w: shift+enter for harness %q", ErrUnsupportedKey, harness)
		}
	}
	if !encodable(key) {
		return uv.KeyPressEvent{}, fmt.Errorf("%w: %s", ErrUnsupportedKey, key.String())
	}
	return key, nil
}

func isShiftEnter(key uv.KeyPressEvent) bool {
	if key.Mod != uv.ModShift {
		return false
	}
	return key.Code == uv.KeyEnter // KeyReturn is the same code; the alias is not repeated here.
}

// encodable mirrors the cases in x/vt v0.1.0 key.go. That function returns no
// error, so an unknown combination is silent loss and an unknown unmodified
// special key is a bare rune written to the child; both are rejected here.
//
// Lock modifiers do not change encoding and are ignored. Alt is stripped the
// way SendKey strips it before matching.
func encodable(key uv.KeyPressEvent) bool {
	mod := key.Mod &^ uv.ModAlt
	mod &^= uv.ModCapsLock | uv.ModNumLock | uv.ModScrollLock

	switch mod {
	case 0:
		return !isSpecialCode(key.Code) || specialEncodable(key.Code)
	case uv.ModShift:
		// The one shifted special the encoder handles.
		if key.Code == uv.KeyTab {
			return true
		}
		return key.Text != "" && !isSpecialCode(key.Code)
	case uv.ModCtrl:
		return ctrlEncodable(key.Code)
	default:
		return false
	}
}

func isSpecialCode(code rune) bool { return code > unicode.MaxRune }

func ctrlEncodable(code rune) bool {
	switch code {
	case uv.KeySpace, '[', '\\', ']', '^', '_':
		return true
	}
	return code >= 'a' && code <= 'z'
}

// specialEncodable lists the special keys the pinned encoder has a case for.
// Unlisted special keys would reach its printable fallback and be written as a
// single out-of-range rune.
func specialEncodable(code rune) bool {
	switch code {
	case uv.KeyUp, uv.KeyDown, uv.KeyRight, uv.KeyLeft,
		uv.KeyInsert, uv.KeyDelete, uv.KeyHome, uv.KeyEnd,
		uv.KeyPgUp, uv.KeyPgDown,
		uv.KeyKpEnter, uv.KeyKpEqual, uv.KeyKpMultiply, uv.KeyKpPlus,
		uv.KeyKpComma, uv.KeyKpMinus, uv.KeyKpDecimal,
		uv.KeyKp0, uv.KeyKp1, uv.KeyKp2, uv.KeyKp3, uv.KeyKp4,
		uv.KeyKp5, uv.KeyKp6, uv.KeyKp7, uv.KeyKp8, uv.KeyKp9,
		uv.KeyF1, uv.KeyF2, uv.KeyF3, uv.KeyF4, uv.KeyF5, uv.KeyF6,
		uv.KeyF7, uv.KeyF8, uv.KeyF9, uv.KeyF10, uv.KeyF11, uv.KeyF12,
		uv.KeyEnter, uv.KeyTab, uv.KeyBackspace, uv.KeyEscape:
		return true
	default:
		return false
	}
}

// recordError keeps the first non-shutdown transport error.
func (p *Pane) recordError(op string, err error) {
	if p.isShutdownErr(err) {
		return
	}
	p.setError(fmt.Errorf("pane: %s %s: %w", op, p.id, err))
}

type paneError struct{ err error }

func (p *Pane) setError(err error) {
	if err != nil {
		p.errValue.CompareAndSwap(nil, &paneError{err: err})
	}
}

// isShutdownErr reports whether err is expected while closing or after the
// child exited, so it is not surfaced as a pane failure.
func (p *Pane) isShutdownErr(err error) bool {
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, io.EOF),
		errors.Is(err, os.ErrClosed),
		errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, os.ErrDeadlineExceeded),
		errors.Is(err, unix.EIO),
		errors.Is(err, unix.EBADF):
		return true
	}
	return p.closed.Load() || p.exited.Load() || p.lifeCtx.Err() != nil
}

func (p *Pane) checkOpen() error {
	if p.closed.Load() {
		return fmt.Errorf("%w: %s", ErrPaneClosed, p.id)
	}
	return nil
}

// closeEmulatorInput closes the emulator's input pipe writer, which is what
// unblocks Read on the reader side without touching Emulator.Close.
func (p *Pane) closeEmulatorInput() error {
	closer, ok := p.emu.InputPipe().(io.Closer)
	if !ok {
		return nil
	}
	return closer.Close() //nolint:wrapcheck // the caller wraps with pane context.
}

// drainErr reports the pane as unusable for input once the reply drain died.
func (p *Pane) drainErr() error {
	if !p.drainDead.Load() {
		return nil
	}
	if err := p.Err(); err != nil {
		return fmt.Errorf("%w: %v", errInputDrainStopped, err)
	}
	return errInputDrainStopped
}

func validateSize(width, height int) error {
	if width < 1 || height < 1 {
		return fmt.Errorf("pane: size %dx%d must be positive", width, height)
	}
	if width > maxDimension || height > maxDimension {
		return fmt.Errorf("pane: size %dx%d exceeds %d cells", width, height, maxDimension)
	}
	if width > (1<<20)/height {
		return fmt.Errorf("pane: size %dx%d exceeds screen cell limit", width, height)
	}
	return nil
}

// exitCode reports the child's exit code, or -1 when a signal ended it or Wait
// failed.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ctxMutex is a mutex that can be acquired with a context, so a pane shutting
// down cannot leave a caller parked on a lock forever.
type ctxMutex chan struct{}

func newCtxMutex() ctxMutex {
	m := make(ctxMutex, 1)
	m <- struct{}{}
	return m
}

func (m ctxMutex) lock(ctx context.Context) error {
	select {
	case <-m:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m ctxMutex) unlock() { m <- struct{}{} }
