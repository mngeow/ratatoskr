// Command rtk opens Ratatoskr: Claude Code and Codex side by side in one window,
// able to message each other. `rtk hook` is what the harnesses call back with.
//
// Untested: under GR-3 (docs/design/ground-rules.md) nothing here has been run.
package main

import (
	"fmt"
	"os"

	"github.com/mngeow/ratatoskr/internal/app"
	"github.com/mngeow/ratatoskr/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: rtk            open Claude Code and Codex side by side in this directory
       rtk --version  print the version
`

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "hook" {
		// A hook must never influence the harness: always exit 0, and write
		// nothing to stdout (invariant 2).
		_ = server.RunHook(args[1:], os.Stdin)
		os.Exit(0)
	}
	if len(args) > 0 {
		switch args[0] {
		case "--version":
			fmt.Println("rtk", version)
			return
		case "--help", "-h":
			fmt.Print(usage)
			return
		default:
			fmt.Fprintf(os.Stderr, "rtk: unknown argument %q\n%s", args[0], usage)
			os.Exit(2)
		}
	}
	if err := app.Run(version); err != nil {
		fmt.Fprintln(os.Stderr, "rtk:", err)
		os.Exit(1)
	}
}
