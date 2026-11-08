package main

// Argument parsing for rtk. main calls this after the hook branch, which keeps its
// old byte-for-byte behaviour and is not parsed here.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/entry"
)

type commandKind int

const (
	// kindLocal is plain `rtk`: open the local panes.
	kindLocal commandKind = iota
	kindVersion
	kindHelp
	kindGatewayServe
	kindGatewayAdmin
	kindRuntimePair
	// kindAttach is `rtk attach` and also `rtk runtime`, which is an alias for it.
	kindAttach
	kindDaemonRun
	kindDaemonStatus
	kindDaemonStop
	kindDaemonNetwork
)

// command is one parsed invocation. Only the fields its kind uses are set.
type command struct {
	kind          commandKind
	listen        string   // kindGatewayServe
	gatewayAddr   string   // kindRuntimePair
	code          string   // kindRuntimePair
	agents        string   // kindAttach, raw; empty means start nothing
	expectSession string   // kindAttach, --expect-session: an exact-session window
	enteredViaSSH bool     // kindAttach, --entered-via-ssh: this window runs inside an SSH entry
	admin         []string // kindGatewayAdmin, the command word first

	// allowLAN and insecureHTTP are the gateway's two opt-ins; fingerprint and
	// insecureHTTP are the runtime's transport choice. insecureHTTP is shared because a
	// command is only ever one of the two kinds, and the semantic refusals (which flag
	// goes with which address) stay in gateway.PlanListen and link.planTransport, so
	// there is one table per side.
	allowLAN     bool
	insecureHTTP bool
	fingerprint  string

	// foreground and yes belong to the daemon forms: --foreground keeps it in this
	// terminal, and --yes allows `daemon stop` to end running sessions.
	foreground   bool
	yes          bool
	session      string // kindDaemonStop, --session NAME
	networkJSON  bool   // kindDaemonNetwork, --json
	networkWatch bool   // kindDaemonNetwork, --watch

	// cardListen is `rtk daemon --card-listen ADDR|off` (kindDaemonRun): where the host card is
	// served. Empty means the default, 0.0.0.0:7778.
	cardListen string

	// cardPollInterval is `rtk gateway --card-poll-interval`; 0 means the default. The range is
	// checked by gateway.Run, before any state is touched.
	cardPollInterval time.Duration

	// The participant API's three flags of `rtk gateway --listen`. Empty and false mean it is off.
	// gateway.RunWith checks them against each other and against --listen before any state is touched.
	participantListen       string
	participantAllowLAN     bool
	participantInsecureHTTP bool
}

const (
	gatewayNeedsListen = "rtk: gateway needs --listen ADDR to run (or one of: pair HOST, unpair HOST, hosts, log, show ID)"
	runtimePairUsage   = "rtk: runtime pair needs --gateway ADDR and --code CODE"
)

// adminCommands are the gateway subcommands RunAdmin dispatches on. When one leads,
// the remaining arguments are passed through untouched so RunAdmin's own parsing and
// usage errors stay the single source of truth.
var adminCommands = map[string]bool{
	"pair":   true,
	"unpair": true,
	"hosts":  true,
	"log":    true,
	"show":   true,

	"card-target": true,
	"participant": true,
}

// parseCommand turns the command line into one command.
//
// A parse error's text is the message alone; main appends the usage, which is how an
// unknown argument reads today.
func parseCommand(args []string) (command, error) {
	if len(args) == 0 {
		return command{kind: kindLocal}, nil
	}

	switch args[0] {
	case "--version":
		if len(args) > 1 {
			return command{}, fmt.Errorf("rtk: %q takes no arguments", args[0])
		}
		return command{kind: kindVersion}, nil
	case "--help", "-h":
		if len(args) > 1 {
			return command{}, fmt.Errorf("rtk: %q takes no arguments", args[0])
		}
		return command{kind: kindHelp}, nil
	case "gateway":
		return parseGateway(args[1:])
	case "attach":
		return parseAttach(args[1:])
	case "runtime":
		return parseRuntime(args[1:])
	case "daemon":
		return parseDaemon(args[1:])
	}
	return command{}, fmt.Errorf("rtk: unknown argument %q", args[0])
}

// parseGateway handles `rtk gateway ...`.
func parseGateway(args []string) (command, error) {
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			return command{kind: kindHelp}, nil
		}
		if adminCommands[args[0]] {
			return command{kind: kindGatewayAdmin, admin: args}, nil
		}
	}

	fs := flag.NewFlagSet("gateway", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "", "")
	allowLAN := fs.Bool("allow-lan", false, "")
	insecureHTTP := fs.Bool("insecure-http", false, "")
	cardPoll := fs.Duration("card-poll-interval", 0, "")
	participantListen := fs.String("participant-listen", "", "")
	participantAllowLAN := fs.Bool("participant-allow-lan", false, "")
	participantInsecureHTTP := fs.Bool("participant-insecure-http", false, "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("%s (%v)", gatewayNeedsListen, err)
	}
	if *listen == "" {
		return command{}, fmt.Errorf("%s", gatewayNeedsListen)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: gateway: unexpected argument %q", rest[0])
	}
	return command{
		kind: kindGatewayServe, listen: *listen,
		allowLAN: *allowLAN, insecureHTTP: *insecureHTTP, cardPollInterval: *cardPoll,
		participantListen: *participantListen, participantAllowLAN: *participantAllowLAN,
		participantInsecureHTTP: *participantInsecureHTTP,
	}, nil
}

// parseDaemon handles `rtk daemon`, `rtk daemon status` and `rtk daemon stop ...`.
//
// The semantic rule that `--session` and `--yes` do not belong together is checked here,
// in one place, rather than in the command that acts on it.
func parseDaemon(args []string) (command, error) {
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			return command{kind: kindHelp}, nil
		case "status":
			if len(args) > 1 {
				return command{}, fmt.Errorf("rtk: daemon status takes no arguments")
			}
			return command{kind: kindDaemonStatus}, nil
		case "stop":
			return parseDaemonStop(args[1:])
		case "network":
			return parseDaemonNetwork(args[1:])
		}
	}

	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	foreground := fs.Bool("foreground", false, "")
	cardListen := fs.String("card-listen", "", "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("rtk: daemon: %v", err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: daemon: unexpected argument %q", rest[0])
	}
	return command{kind: kindDaemonRun, foreground: *foreground, cardListen: *cardListen}, nil
}

// parseDaemonNetwork handles `rtk daemon network [--json] [--watch]`. No flag has a meaning the
// parser has to enforce, so the two booleans are all it produces.
func parseDaemonNetwork(args []string) (command, error) {
	fs := flag.NewFlagSet("daemon network", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	watch := fs.Bool("watch", false, "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("rtk: daemon network: %v", err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: daemon network: unexpected argument %q", rest[0])
	}
	return command{kind: kindDaemonNetwork, networkJSON: *asJSON, networkWatch: *watch}, nil
}

// parseDaemonStop handles `rtk daemon stop [--yes] [--session NAME]`.
func parseDaemonStop(args []string) (command, error) {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "")
	session := fs.String("session", "", "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("rtk: daemon stop: %v", err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: daemon stop: unexpected argument %q", rest[0])
	}
	if *yes && *session != "" {
		return command{}, fmt.Errorf("rtk: daemon stop --session does not take --yes")
	}
	if *session != "" {
		return command{kind: kindDaemonStop, session: *session}, nil
	}
	return command{kind: kindDaemonStop, yes: *yes}, nil
}

// parseRuntime handles `rtk runtime ...`.
func parseRuntime(args []string) (command, error) {
	if len(args) > 0 {
		switch args[0] {
		case "--help", "-h":
			return command{kind: kindHelp}, nil
		case "pair":
			return parseRuntimePair(args[1:])
		}
	}

	fs := flag.NewFlagSet("runtime", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agents := fs.String("agents", "", "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("rtk: runtime: %v", err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: runtime: unexpected argument %q", rest[0])
	}
	return command{kind: kindAttach, agents: *agents}, nil
}

// parseAttach handles `rtk attach [--agents LIST]`.
//
// The flag is passed through raw: the list is validated where it is used, so the parser never
// duplicates a rule.
func parseAttach(args []string) (command, error) {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	agents := fs.String("agents", "", "")
	expectSession := fs.String("expect-session", "", "")
	enteredViaSSH := fs.Bool("entered-via-ssh", false, "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("rtk: attach: %v", err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: attach: unexpected argument %q", rest[0])
	}
	if *expectSession != "" {
		if strings.TrimSpace(*agents) != "" {
			return command{}, fmt.Errorf("rtk: --expect-session attaches one running session and cannot be combined with --agents")
		}
		if !entry.IsUUID(*expectSession) {
			return command{}, fmt.Errorf("rtk: --expect-session needs a session id")
		}
	}
	return command{
		kind:          kindAttach,
		agents:        *agents,
		expectSession: *expectSession,
		enteredViaSSH: *enteredViaSSH,
	}, nil
}

// parseRuntimePair handles `rtk runtime pair --gateway ADDR --code CODE`. Both flags
// are required, and the error names whichever is missing.
func parseRuntimePair(args []string) (command, error) {
	fs := flag.NewFlagSet("runtime pair", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	gateway := fs.String("gateway", "", "")
	code := fs.String("code", "", "")
	fingerprint := fs.String("fingerprint", "", "")
	insecureHTTP := fs.Bool("insecure-http", false, "")
	if err := fs.Parse(args); err != nil {
		return command{}, fmt.Errorf("%s (%v)", runtimePairUsage, err)
	}
	if rest := fs.Args(); len(rest) > 0 {
		return command{}, fmt.Errorf("rtk: runtime pair: unexpected argument %q", rest[0])
	}
	switch {
	case *gateway == "" && *code == "":
		return command{}, fmt.Errorf("%s", runtimePairUsage)
	case *gateway == "":
		return command{}, fmt.Errorf("rtk: runtime pair needs --gateway ADDR")
	case *code == "":
		return command{}, fmt.Errorf("rtk: runtime pair needs --code CODE")
	}
	return command{
		kind: kindRuntimePair, gatewayAddr: *gateway, code: *code,
		fingerprint: *fingerprint, insecureHTTP: *insecureHTTP,
	}, nil
}
