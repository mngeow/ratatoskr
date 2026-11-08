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
	"time"

	"github.com/mngeow/ratatoskr/internal/app"
	"github.com/mngeow/ratatoskr/internal/attach"
	"github.com/mngeow/ratatoskr/internal/daemon"
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
       rtk gateway card-target set|show|clear where the gateway polls a host's card (see: rtk gateway card-target)
       rtk gateway --listen ADDR [--card-poll-interval 60s]
                                              how often the gateway polls registered card targets (10s to 1h)
       rtk gateway show ID                    show one message in full
       rtk runtime pair --gateway ADDR --code CODE [--fingerprint SHA256 | --insecure-http]
                                              pair this machine with a gateway
       rtk attach [--agents claude,codex] [--expect-session ID] [--entered-via-ssh]     open a window on the daemon's sessions, starting any listed session that is not running
       rtk runtime [--agents claude,codex]    an alias for rtk attach
       rtk daemon                             start this machine's daemon in the background
       rtk daemon --foreground                the same, staying in this terminal
       rtk daemon --card-listen ADDR|off      where the host card is served: 0.0.0.0:7778 by default, plain HTTP, readable by
                                              anyone who can reach the port; set at start only (the card is the file card.yaml in
                                              the daemon directory; edit it in your editor)
       rtk daemon status                      running or not, its sessions, windows and log path
       rtk daemon stop [--yes]                stop the daemon and every session
       rtk daemon stop --session NAME         end one session
       rtk daemon network [--json] [--watch]  the network answer: local sessions and the gateway snapshot
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
	case kindAttach:
		// A window owns its own signal handling: a signal closes its socket and leaves the
		// daemon and its sessions running.
		// The exit code travels with the error: 4 for no daemon, 3 for an expectation that is not
		// running, 1 for everything else.
		if err := attach.Run(attach.Options{
			Version:       version,
			Agents:        cmd.agents,
			ExpectSession: cmd.expectSession,
			EnteredViaSSH: cmd.enteredViaSSH,
		}); err != nil {
			exitWith(err)
		}
		return
	case kindDaemonRun:
		daemon.SetBuild(version)
		if err := runDaemon(cmd); err != nil {
			fmt.Fprintln(os.Stderr, "rtk:", err)
			os.Exit(1)
		}
		return
	case kindDaemonStatus:
		daemon.SetBuild(version)
		if err := daemonStatus(); err != nil {
			fmt.Fprintln(os.Stderr, "rtk:", err)
			os.Exit(1)
		}
		return
	case kindDaemonNetwork:
		daemon.SetBuild(version)
		// A signal is how a --watch ends, so the context carries the terminal's signals.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		err := daemonNetwork(ctx, cmd)
		stop()
		if err != nil {
			exitWith(err)
		}
		return
	case kindDaemonStop:
		daemon.SetBuild(version)
		if err := daemonStop(cmd); err != nil {
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
		err = gateway.Run(ctx, stateDir, cmd.listen, cmd.allowLAN, cmd.insecureHTTP, cmd.cardPollInterval, os.Stderr)
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

// daemonNetwork runs `rtk daemon network` against this machine's state directory.
func daemonNetwork(ctx context.Context, cmd command) error {
	cfg, err := daemonConfig(false)
	if err != nil {
		return err
	}
	return daemon.RunNetwork(ctx, cfg.StateDir, cmd.networkJSON, cmd.networkWatch, os.Stdout)
}

// exitWith prints an error and exits with the status it asks for, or 1.
//
// Only errors that carry an exit code change the status; everything else keeps exiting 1, which is
// what every command did before this existed.
func exitWith(err error) {
	fmt.Fprintln(os.Stderr, "rtk:", err)
	if coded, ok := err.(interface{ ExitCode() int }); ok {
		os.Exit(coded.ExitCode())
	}
	os.Exit(1)
}

// daemonConfig is the daemon's configuration for this binary and this state directory.
func daemonConfig(foreground bool) (daemon.Config, error) {
	stateDir, err := store.DefaultDir()
	if err != nil {
		return daemon.Config{}, fmt.Errorf("state directory: %w", err)
	}
	return daemon.Config{Version: version, StateDir: stateDir, Foreground: foreground}, nil
}

// runDaemon starts the daemon: in the background, or in this terminal with --foreground.
func runDaemon(cmd command) error {
	foreground := cmd.foreground
	cfg, err := daemonConfig(foreground)
	if err != nil {
		return err
	}
	cfg.CardListen = cmd.cardListen
	if !foreground {
		return daemon.Start(cfg, os.Stdout)
	}
	// The daemon installs its own signal handling and ignores SIGHUP.
	return daemon.Run(context.Background(), cfg)
}

// daemonStatus prints the running daemon's status.
func daemonStatus() error {
	cfg, err := daemonConfig(false)
	if err != nil {
		return err
	}
	status, err := daemon.Status(context.Background(), cfg.StateDir)
	if err != nil {
		return err
	}
	daemon.PrintStatus(os.Stdout, status, version, time.Now())
	return nil
}

// daemonStop stops the daemon, or one of its sessions.
func daemonStop(cmd command) error {
	cfg, err := daemonConfig(false)
	if err != nil {
		return err
	}
	if cmd.session != "" {
		return daemon.StopSession(context.Background(), cfg.StateDir, cmd.session, os.Stdout)
	}
	return daemon.Stop(context.Background(), cfg.StateDir, cmd.yes, os.Stdout)
}
