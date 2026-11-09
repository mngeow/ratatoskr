// Command ratatoskr-openrig is the bridge between the Ratatoskr participant API and one OpenRig operator seat
// (docs/implementation/openrig-bridge-contract.md). It runs as a long-lived sidecar (serve) and as a thin local
// client (send, directory, status), and pairs itself once (pair).
//
// UNTESTED. Under GR-3 nothing here has been built into a binary or run.
package main

import (
	_ "embed"
	"os"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/cli"
)

// The setup wizard installs these two files; go:embed cannot reach them from inside internal/cli.
//
//go:embed launchd/dev.ratatoskr.openrig.plist.template
var plistTemplate string

//go:embed skill/ratatoskr-openrig/SKILL.md
var skillText string

func main() {
	cli.SetAssets(plistTemplate, skillText)
	os.Exit(cli.Main(os.Args[1:], cli.Process()))
}
