package pane

// UNTESTED under GR-3. Reaping and group signalling share lifeMu so a child
// cannot be reaped (and its PID reused) between an ownership check and signal.

import (
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const reapInterval = 100 * time.Millisecond // UNTESTED: up to this exit-notification latency.

func (p *Pane) signalOwned(signal unix.Signal) error {
	p.lifeMu.Lock()
	defer p.lifeMu.Unlock()
	if p.exited.Load() {
		return os.ErrProcessDone
	}
	pid := p.cmd.Process.Pid
	if pid <= 1 {
		return errors.New("pane: invalid owned child PID")
	}
	pgid, err := unix.Getpgid(pid)
	if target := ownedGroupTarget(pid, p.ownedPGID, pgid, err == nil); target < 0 {
		err := unix.Kill(target, signal) // only this still-unreaped child's verified group
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// Ownership was not established or changed: no group assumption is safe.
	return p.cmd.Process.Signal(signal)
}

func ownedGroupTarget(pid, recorded, current int, verified bool) int {
	if pid > 1 && verified && recorded == pid && current == pid {
		return -pid
	}
	return pid
}

func (p *Pane) waitLoop() {
	code := -1
	for {
		p.lifeMu.Lock()
		var status unix.WaitStatus
		pid, err := unix.Wait4(p.cmd.Process.Pid, &status, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			p.lifeMu.Unlock()
			continue
		}
		if pid == p.cmd.Process.Pid || err != nil {
			if err != nil {
				p.setError(fmt.Errorf("pane: wait for child: %w", err))
			} else {
				code = ownedExitCode(status)
			}
			// All exec streams are PTY *os.File values: exec started no copy
			// goroutines to join. Wait4 owns the one reap; Release closes Go's
			// process handle/pidfd without attempting a second wait.
			p.exited.Store(true)
			_ = p.cmd.Process.Release()
			p.lifeMu.Unlock()
			break
		}
		p.lifeMu.Unlock()
		time.Sleep(reapInterval)
	}
	close(p.exitedCh)
	p.exitOnce.Do(func() {
		if p.onExit != nil {
			p.onExit(code)
		}
	})
}

func ownedExitCode(status unix.WaitStatus) int {
	if status.Exited() {
		return status.ExitStatus()
	}
	return -1
}
