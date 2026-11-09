// Package cli implements the ratatoskr-openrig commands: setup, pair, serve, send, directory and status
// (docs/implementation/openrig-bridge-contract.md section 4). Every command writes one-line, terminal-safe
// messages to stderr; stdout is for results.
//
// UNTESTED. Under GR-3 nothing here has been run.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

// Exit codes (the Python adapter's, extended by 6).
const (
	ExitOK         = 0
	ExitFailed     = 1
	ExitUsage      = 2
	ExitUncertain  = 3
	ExitReplaced   = 4
	ExitRevoked    = 5
	ExitBridgeDown = 6
)

// Version is the program version printed by --version.
const Version = "0.1.0-untested"

// IO is where a command reads and writes.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

const usage = `usage: ratatoskr-openrig <command> [flags]

  setup [--config PATH] [--insecure-http]                           (guided; needs a terminal)
  pair --pair-code-file PATH [--config PATH] [--insecure-http]
  serve [--config PATH] [--insecure-http] [--resume]
  send --to NAME@HOST [--reply-to ID] [--id ID] [--config PATH]    (message on standard input)
  directory [--config PATH]
  status [--json] [--config PATH]
  --version`

// Main runs one command and returns its exit code.
func Main(args []string, io IO) int {
	if len(args) == 0 {
		fmt.Fprintln(io.Stderr, usage)
		return ExitUsage
	}
	switch args[0] {
	case "--version", "version":
		fmt.Fprintln(io.Stdout, "ratatoskr-openrig", Version)
		return ExitOK
	case "--help", "-h", "help":
		fmt.Fprintln(io.Stdout, usage)
		return ExitOK
	case "setup":
		return runSetup(args[1:], io)
	case "pair":
		return runPair(args[1:], io)
	case "serve":
		return runServe(args[1:], io)
	case "send":
		return runSend(args[1:], io)
	case "directory":
		return runDirectory(args[1:], io)
	case "status":
		return runStatus(args[1:], io)
	}
	fmt.Fprintf(io.Stderr, "ratatoskr-openrig: unknown command %q\n%s\n", terminalText(args[0]), usage)
	return ExitUsage
}

// newFlags makes a flag set that reports its own errors as one line.
func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func fail(io IO, code int, format string, args ...any) int {
	fmt.Fprintf(io.Stderr, "ratatoskr-openrig: "+format+"\n", args...)
	return code
}

// loadConfig resolves --config and loads it.
func loadConfig(path string, insecureHTTP bool, io IO) (*config.Config, int) {
	if path == "" {
		def, err := config.DefaultPath()
		if err != nil {
			return nil, fail(io, ExitUsage, "%v", err)
		}
		path = def
	}
	cfg, err := config.Load(path, insecureHTTP)
	if err != nil {
		return nil, fail(io, ExitUsage, "%v", err)
	}
	return cfg, 0
}

// terminalText keeps text printable on one line.
func terminalText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			r = ' '
		}
		b.WriteRune(r)
	}
	if b.Len() > 200 {
		return b.String()[:200]
	}
	return b.String()
}

// stateIO is a convenience for tests and callers: the real process streams.
func Process() IO { return IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr} }
