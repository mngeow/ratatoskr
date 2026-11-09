package tui

// The Bubble Tea v2 front over internal/setup: `ratatoskr-openrig setup` (contract 4.1, ruling W5).
//
// Only this package imports Charm (ADR-0008 12a): the engine, the sidecar, the client and `pair` stay on
// the standard library. The engine does every step and returns values; this package collects input and
// renders those values, so it can be driven by a fake engine and the engine can be tested without a
// terminal.
//
// UNTESTED (GR-3): the program has not been run. Compile-only checks do not establish terminal behavior.

import (
	"context"
	"io"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
)

// Run shows the setup wizard and returns the command's exit code (setup.ExitOK, ExitFailed, ExitUsage or
// ExitUncertain).
//
// It refuses to start unless standard input and output are character devices, so the wizard is never
// piped or redirected: Run returns setup.ExitUsage in that case and does nothing else. The command keeps
// its own terminal gate as well.
//
// Run owns the engine: it calls Close on every path, so the engine's secrets are zeroed even when the
// owner quits early. The returned code is captured before Close, so closing cannot change it. The caller
// must not use the engine afterwards.
//
// A non-nil error means the program itself could not run (Bubble Tea returned an error); a step that
// stopped the run is not an error - it is a Result, and the exit code says how the command ends.
func Run(engine *setup.Engine, in io.Reader, out io.Writer) (int, error) {
	if engine == nil {
		return setup.ExitUsage, nil
	}
	if !isCharDevice(in) || !isCharDevice(out) {
		// Nothing has started, so nothing is left to clean up; the engine is still closed below.
		engine.Close()
		return setup.ExitUsage, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer engine.Close()
	defer cancel()

	wizard := newModel(engine, ctx)
	program := tea.NewProgram(wizard, tea.WithInput(in), tea.WithOutput(out))

	finished, err := program.Run()

	// The exit code is decided while the engine still holds its state, then the engine is closed.
	code := setup.ExitFailed
	switch result := finished.(type) {
	case *model:
		result.wipeSecrets()
		code = result.exitCode
	default:
		// Bubble Tea hands back the last model it rendered; anything else means the program never got
		// that far, so the run did not complete.
		wizard.wipeSecrets()
	}
	return code, err
}

// isCharDevice reports whether a stream is a terminal.
//
// The standard library has no isatty, and this package may not add golang.org/x/term; a character-device
// check on an *os.File is what is available. A caller that passes a wrapper (a buffer, a pipe) is not a
// terminal, and the wizard refuses rather than render escape sequences into it.
//
// The parameter is any, not io.Reader: Run checks one reader and one writer, and both are *os.File when
// they are terminals at all.
func isCharDevice(stream any) bool {
	file, ok := stream.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
