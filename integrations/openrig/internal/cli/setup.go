package cli

// setup: the guided Bubble Tea TUI (contract section 4.1). This file is only the command: it refuses unless
// standard input and output are terminals, builds the engine (internal/setup) and hands it to the TUI, which
// lives in internal/setup/tui and is the only package that imports Charm. The steps themselves (internal/setup)
// do no terminal I/O.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"os"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
	setuptui "github.com/mngeow/ratatoskr/integrations/openrig/internal/setup/tui"
)

const setupUsage = "usage: setup [--config PATH] [--insecure-http]"

// Embedded files, set once by package main (go:embed cannot reach outside its own directory tree).
var embedded setup.Assets

// SetAssets gives the wizard the launchd template and the skill text that main embeds.
func SetAssets(plistTemplate, skill string) {
	embedded = setup.Assets{PlistTemplate: plistTemplate, Skill: skill}
}

func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func runSetup(args []string, io IO) int {
	fs := newFlags("setup")
	configPath := fs.String("config", "", "")
	insecure := fs.Bool("insecure-http", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fail(io, ExitUsage, setupUsage)
	}
	if !isTerminal(io.Stdin) || !isTerminal(io.Stdout) {
		return fail(io, ExitUsage, "setup needs a terminal on standard input and output; for a scripted install use `pair --pair-code-file` and write the configuration by hand")
	}
	if embedded.PlistTemplate == "" || embedded.Skill == "" {
		return fail(io, ExitFailed, "this build has no embedded launchd template or skill (internal error)")
	}
	env, err := setup.DefaultEnv(*configPath, *insecure, embedded)
	if err != nil {
		return fail(io, ExitUsage, "%v", err)
	}
	engine := setup.New(env)
	return runSetupUI(engine, io)
}

// runSetupUI launches the Bubble Tea front, which owns and closes the engine.
func runSetupUI(engine *setup.Engine, io IO) int {
	code, err := setuptui.Run(engine, io.Stdin, io.Stdout)
	if err != nil {
		return fail(io, ExitFailed, "setup TUI failed: %s", terminalText(err.Error()))
	}
	return code
}
