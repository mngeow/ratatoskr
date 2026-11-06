// Command rtk opens Ratatoskr: Claude Code and Codex side by side in one window,
// able to message each other, locally or through a gateway. `rtk hook` is what the
// harnesses call back with.
//
// Untested: under GR-3 (docs/design/ground-rules.md) nothing here has been run.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mngeow/ratatoskr/internal/app"
	"github.com/mngeow/ratatoskr/internal/gateway"
	"github.com/mngeow/ratatoskr/internal/link"
	"github.com/mngeow/ratatoskr/internal/server"
	"github.com/mngeow/ratatoskr/internal/store"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: rtk                                    open Claude Code and Codex side by side in this directory
       rtk --version                          print the version
       rtk gateway --listen ADDR [--allow-lan [--insecure-http]]
                                              run the gateway (Tailscale, loopback or, with --allow-lan, a LAN address)
       rtk gateway pair HOST                  print a one-time code for HOST
       rtk gateway unpair HOST                remove HOST
       rtk gateway hosts                      list paired hosts and their state
       rtk gateway log [--follow] [--limit N] read the gateway's ledger
       rtk gateway show ID                    show one message in full
       rtk runtime pair --gateway ADDR --code CODE [--fingerprint SHA256 | --insecure-http]
                                              pair this machine with a gateway
       rtk runtime [--agents claude,codex]    open the agents, connected to the gateway
`

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "hook" {
		// A hook must never influence the harness: always exit 0, and write
		// nothing to stdout (invariant 2).
		_ = server.RunHook(args[1:], os.Stdin)
		os.Exit(0)
	}

	cmd, err := parseCommand(args)
	if err != nil {
		// A usage error names the problem and then shows the forms.
		fmt.Fprintf(os.Stderr, "%v\n%s", err, usage)
		os.Exit(2)
	}

	switch cmd.kind {
	case kindVersion:
		fmt.Println("rtk", version)
		return
	case kindHelp:
		fmt.Print(usage)
		return
	case kindLocal:
		if err := app.Run(version); err != nil {
			fmt.Fprintln(os.Stderr, "rtk:", err)
			os.Exit(1)
		}
		return
	case kindRuntimeRun:
		// RunRuntime installs its own signal handling, like Run, so a run's panes
		// and link shut down in order rather than being killed here.
		if err := app.RunRuntime(version, cmd.agents); err != nil {
			fmt.Fprintln(os.Stderr, "rtk:", err)
			os.Exit(1)
		}
		return
	}

	// The remaining kinds talk to a gateway and honour a signal while doing so.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	stateDir, err := store.DefaultDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rtk: state directory: %v\n", err)
		os.Exit(1)
	}

	switch cmd.kind {
	case kindGatewayServe:
		err = gateway.Run(ctx, stateDir, cmd.listen, cmd.allowLAN, cmd.insecureHTTP, os.Stderr)
	case kindGatewayAdmin:
		err = gateway.RunAdmin(ctx, stateDir, cmd.admin, os.Stdout, os.Stderr, nil)
	case kindRuntimePair:
		err = link.RunPair(ctx, stateDir, link.Target{
			Gateway:      cmd.gatewayAddr,
			Fingerprint:  cmd.fingerprint,
			InsecureHTTP: cmd.insecureHTTP,
		}, cmd.code, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "rtk:", err)
		os.Exit(1)
	}
}
