package main

// Untested: under GR-3 nothing here has been run.

import (
	"strings"
	"testing"
	"time"
)

func TestParseCommandForms(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		want  command
		isErr bool
	}{
		{name: "no arguments is the local run", args: nil, want: command{kind: kindLocal}},
		{name: "version", args: []string{"--version"}, want: command{kind: kindVersion}},
		{name: "help", args: []string{"--help"}, want: command{kind: kindHelp}},
		{name: "short help", args: []string{"-h"}, want: command{kind: kindHelp}},
		{
			name: "gateway --help", args: []string{"gateway", "--help"},
			want: command{kind: kindHelp},
		},
		{
			name: "runtime --help", args: []string{"runtime", "--help"},
			want: command{kind: kindHelp},
		},
		{
			name: "gateway serve", args: []string{"gateway", "--listen", "100.64.0.1:7777"},
			want: command{kind: kindGatewayServe, listen: "100.64.0.1:7777"},
		},
		{
			name: "gateway serve equals form", args: []string{"gateway", "--listen=127.0.0.1:7777"},
			want: command{kind: kindGatewayServe, listen: "127.0.0.1:7777"},
		},
		{
			name: "gateway pair passes the command word through", args: []string{"gateway", "pair", "mac"},
			want: command{kind: kindGatewayAdmin, admin: []string{"pair", "mac"}},
		},
		{
			name: "gateway unpair", args: []string{"gateway", "unpair", "mac"},
			want: command{kind: kindGatewayAdmin, admin: []string{"unpair", "mac"}},
		},
		{
			name: "gateway hosts", args: []string{"gateway", "hosts"},
			want: command{kind: kindGatewayAdmin, admin: []string{"hosts"}},
		},
		{
			name: "gateway log keeps its own flags", args: []string{"gateway", "log", "--follow", "--limit", "10"},
			want: command{kind: kindGatewayAdmin, admin: []string{"log", "--follow", "--limit", "10"}},
		},
		{
			name: "gateway show", args: []string{"gateway", "show", "m1"},
			want: command{kind: kindGatewayAdmin, admin: []string{"show", "m1"}},
		},
		{
			// A missing HOST is RunAdmin's usage error, not ours.
			name: "gateway pair without a host is passed through", args: []string{"gateway", "pair"},
			want: command{kind: kindGatewayAdmin, admin: []string{"pair"}},
		},
		{
			name: "runtime pair", args: []string{"runtime", "pair", "--gateway", "100.64.0.1:7777", "--code", "abcd"},
			want: command{kind: kindRuntimePair, gatewayAddr: "100.64.0.1:7777", code: "abcd"},
		},
		{
			name: "runtime run with both agents", args: []string{"runtime", "--agents", "claude,codex"},
			want: command{kind: kindAttach, agents: "claude,codex"},
		},
		{
			// The raw string is passed through; tui.ParseAgents validates it later.
			name: "runtime run passes an odd list through", args: []string{"runtime", "--agents", "claude,claude"},
			want: command{kind: kindAttach, agents: "claude,claude"},
		},
		{
			name: "runtime run with no flag", args: []string{"runtime"},
			want: command{kind: kindAttach, agents: ""},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if tc.isErr {
				if err == nil {
					t.Fatalf("parseCommand(%q) accepted the arguments", tc.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCommand(%q): %v", tc.args, err)
			}
			if got.kind != tc.want.kind {
				t.Fatalf("kind = %v, want %v", got.kind, tc.want.kind)
			}
			if got.listen != tc.want.listen {
				t.Fatalf("listen = %q, want %q", got.listen, tc.want.listen)
			}
			if got.gatewayAddr != tc.want.gatewayAddr || got.code != tc.want.code {
				t.Fatalf("pair fields = %q %q, want %q %q", got.gatewayAddr, got.code, tc.want.gatewayAddr, tc.want.code)
			}
			if got.agents != tc.want.agents {
				t.Fatalf("agents = %q, want %q", got.agents, tc.want.agents)
			}
			if strings.Join(got.admin, "\x00") != strings.Join(tc.want.admin, "\x00") {
				t.Fatalf("admin = %q, want %q", got.admin, tc.want.admin)
			}
		})
	}
}

func TestParseCommandErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown argument", args: []string{"explode"}, want: `rtk: unknown argument "explode"`},
		{name: "hook is not parsed here", args: []string{"hook"}, want: `rtk: unknown argument "hook"`},
		{name: "gateway with nothing", args: []string{"gateway"}, want: gatewayNeedsListen},
		{name: "gateway listen without a value", args: []string{"gateway", "--listen"}, want: gatewayNeedsListen},
		{name: "gateway unknown flag", args: []string{"gateway", "--local"}, want: gatewayNeedsListen},
		{name: "gateway extra argument", args: []string{"gateway", "--listen", "127.0.0.1:7777", "extra"}, want: "rtk: gateway: unexpected argument"},
		{name: "runtime pair with nothing", args: []string{"runtime", "pair"}, want: runtimePairUsage},
		{name: "runtime pair without a code", args: []string{"runtime", "pair", "--gateway", "x:1"}, want: "rtk: runtime pair needs --code CODE"},
		{name: "runtime pair without a gateway", args: []string{"runtime", "pair", "--code", "abcd"}, want: "rtk: runtime pair needs --gateway ADDR"},
		{name: "runtime pair unknown flag", args: []string{"runtime", "pair", "--host", "x"}, want: runtimePairUsage},
		{name: "runtime pair extra argument", args: []string{"runtime", "pair", "--gateway", "x:1", "--code", "a", "extra"}, want: "rtk: runtime pair: unexpected argument"},
		{name: "runtime unknown flag", args: []string{"runtime", "--local"}, want: "rtk: runtime:"},
		{name: "runtime extra argument", args: []string{"runtime", "--agents", "claude", "extra"}, want: "rtk: runtime: unexpected argument"},
		{name: "version with an argument", args: []string{"--version", "extra"}, want: `rtk: "--version" takes no arguments`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCommand(tc.args)
			if err == nil {
				t.Fatalf("parseCommand(%q) accepted the arguments", tc.args)
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error = %q, want a prefix %q", err, tc.want)
			}
		})
	}
}

// TestUsageListsEveryForm pins the help text a user reads.
func TestUsageListsEveryForm(t *testing.T) {
	for _, form := range []string{
		"usage: rtk ",
		"rtk --version",
		"rtk gateway --listen ADDR",
		"rtk gateway pair HOST",
		"rtk gateway unpair HOST",
		"rtk gateway hosts",
		"rtk gateway log [--follow] [--limit N]",
		"rtk gateway show ID",
		"rtk runtime pair --gateway ADDR --code CODE",
		"rtk runtime [--agents claude,codex]",
	} {
		if !strings.Contains(usage, form) {
			t.Fatalf("usage does not mention %q:\n%s", form, usage)
		}
	}
	if strings.Contains(usage, "\x00") || strings.HasSuffix(usage, "\n\n") {
		t.Fatalf("usage has a spacing defect:\n%q", usage)
	}
}

// TestParseCommandLANFlags: the two opt-ins and the runtime transport choice are
// parsed here and refused semantically later, so the parser never duplicates a rule.
// UNTESTED: written, not run.
func TestParseCommandLANFlags(t *testing.T) {
	fingerprint := "sha256:" + strings.Repeat("ab", 32)

	tests := []struct {
		name string
		args []string
		want command
	}{
		{
			name: "gateway with --allow-lan",
			args: []string{"gateway", "--listen", "192.168.1.20:7777", "--allow-lan"},
			want: command{kind: kindGatewayServe, listen: "192.168.1.20:7777", allowLAN: true},
		},
		{
			name: "gateway with both opt-ins",
			args: []string{"gateway", "--listen", "192.168.1.20:7777", "--allow-lan", "--insecure-http"},
			want: command{kind: kindGatewayServe, listen: "192.168.1.20:7777", allowLAN: true, insecureHTTP: true},
		},
		{
			// The flags are accepted and then refused by PlanListen: no rule here.
			name: "gateway with a stray opt-in",
			args: []string{"gateway", "--listen", "100.64.0.1:7777", "--allow-lan"},
			want: command{kind: kindGatewayServe, listen: "100.64.0.1:7777", allowLAN: true},
		},
		{
			name: "runtime pair with a fingerprint",
			args: []string{"runtime", "pair", "--gateway", "192.168.1.20:7777", "--code", "abcd", "--fingerprint", fingerprint},
			want: command{kind: kindRuntimePair, gatewayAddr: "192.168.1.20:7777", code: "abcd", fingerprint: fingerprint},
		},
		{
			name: "runtime pair with --insecure-http",
			args: []string{"runtime", "pair", "--gateway", "192.168.1.20:7777", "--code", "abcd", "--insecure-http"},
			want: command{kind: kindRuntimePair, gatewayAddr: "192.168.1.20:7777", code: "abcd", insecureHTTP: true},
		},
		{
			// Both is the user's mistake to make; planTransport refuses it.
			name: "runtime pair with both",
			args: []string{"runtime", "pair", "--gateway", "192.168.1.20:7777", "--code", "abcd", "--fingerprint", fingerprint, "--insecure-http"},
			want: command{kind: kindRuntimePair, gatewayAddr: "192.168.1.20:7777", code: "abcd", fingerprint: fingerprint, insecureHTTP: true},
		},
		{
			// An admin subcommand keeps its own flags: nothing is parsed here.
			name: "an admin subcommand passes the opt-ins through",
			args: []string{"gateway", "log", "--allow-lan"},
			want: command{kind: kindGatewayAdmin, admin: []string{"log", "--allow-lan"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if err != nil {
				t.Fatalf("parseCommand(%q): %v", tc.args, err)
			}
			if got.kind != tc.want.kind || got.listen != tc.want.listen ||
				got.gatewayAddr != tc.want.gatewayAddr || got.code != tc.want.code ||
				got.fingerprint != tc.want.fingerprint ||
				got.allowLAN != tc.want.allowLAN || got.insecureHTTP != tc.want.insecureHTTP ||
				strings.Join(got.admin, "\x00") != strings.Join(tc.want.admin, "\x00") {
				t.Fatalf("parseCommand(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}

	// An opt-in without --listen is still the "needs --listen" usage error.
	for _, args := range [][]string{
		{"gateway", "--allow-lan"},
		{"gateway", "--insecure-http"},
		{"gateway", "--allow-lan", "--insecure-http"},
	} {
		if _, err := parseCommand(args); err == nil || !strings.HasPrefix(err.Error(), gatewayNeedsListen) {
			t.Fatalf("parseCommand(%q) = %v, want the needs-listen error", args, err)
		}
	}
}

// TestUsageListsTheTransportForms pins the two lines the new flags appear in.
func TestUsageListsTheTransportForms(t *testing.T) {
	for _, form := range []string{
		"rtk gateway --listen ADDR [--allow-lan [--insecure-http]]",
		"rtk runtime pair --gateway ADDR --code CODE [--fingerprint SHA256 | --insecure-http]",
		// The old forms must still be readable, since they still work.
		"rtk gateway pair HOST",
		"rtk runtime [--agents claude,codex]",
	} {
		if !strings.Contains(usage, form) {
			t.Fatalf("usage does not mention %q:\n%s", form, usage)
		}
	}
}

// TestParseDaemonForms: the daemon lines of the contract.
// UNTESTED: written, not run.
func TestParseDaemonForms(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want command
	}{
		{
			name: "start in the background",
			args: []string{"daemon"},
			want: command{kind: kindDaemonRun},
		},
		{
			name: "start in the foreground",
			args: []string{"daemon", "--foreground"},
			want: command{kind: kindDaemonRun, foreground: true},
		},
		{
			name: "status",
			args: []string{"daemon", "status"},
			want: command{kind: kindDaemonStatus},
		},
		{
			name: "stop",
			args: []string{"daemon", "stop"},
			want: command{kind: kindDaemonStop},
		},
		{
			name: "stop --yes",
			args: []string{"daemon", "stop", "--yes"},
			want: command{kind: kindDaemonStop, yes: true},
		},
		{
			name: "stop --session",
			args: []string{"daemon", "stop", "--session", "claude"},
			want: command{kind: kindDaemonStop, session: "claude"},
		},
		{
			name: "daemon --help",
			args: []string{"daemon", "--help"},
			want: command{kind: kindHelp},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if err != nil {
				t.Fatalf("parseCommand(%q): %v", tc.args, err)
			}
			if got.kind != tc.want.kind {
				t.Fatalf("kind = %v, want %v", got.kind, tc.want.kind)
			}
			if got.foreground != tc.want.foreground {
				t.Fatalf("foreground = %t, want %t", got.foreground, tc.want.foreground)
			}
			if got.yes != tc.want.yes {
				t.Fatalf("yes = %t, want %t", got.yes, tc.want.yes)
			}
			if got.session != tc.want.session {
				t.Fatalf("session = %q, want %q", got.session, tc.want.session)
			}
		})
	}
}

// TestParseDaemonErrors: each refusal, kept as a message-only parse error.
func TestParseDaemonErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "status takes no arguments", args: []string{"daemon", "status", "extra"}, want: "rtk: daemon status takes no arguments"},
		{name: "an unknown daemon flag", args: []string{"daemon", "--now"}, want: "rtk: daemon:"},
		{name: "an extra daemon argument", args: []string{"daemon", "extra"}, want: "rtk: daemon: unexpected argument"},
		{name: "an unknown stop flag", args: []string{"daemon", "stop", "--now"}, want: "rtk: daemon stop:"},
		{name: "an extra stop argument", args: []string{"daemon", "stop", "extra"}, want: "rtk: daemon stop: unexpected argument"},
		{
			// The one semantic rule, in the one place it is checked.
			name: "yes with a session", args: []string{"daemon", "stop", "--session", "claude", "--yes"},
			want: "rtk: daemon stop --session does not take --yes",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseCommand(tc.args)
			if err == nil {
				t.Fatalf("parseCommand(%q) accepted the arguments", tc.args)
			}
			if !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("error = %q, want a prefix %q", err, tc.want)
			}
		})
	}
}

// TestUsageListsTheDaemonForms pins the contract's daemon lines.
func TestUsageListsTheDaemonForms(t *testing.T) {
	for _, form := range []string{
		"rtk daemon                             start this machine's daemon in the background",
		"rtk daemon --foreground                the same, staying in this terminal",
		"rtk daemon status                      running or not, its sessions, windows and log path",
		"rtk daemon stop [--yes]                stop the daemon and every session",
		"rtk daemon stop --session NAME         end one session",
	} {
		if !strings.Contains(usage, form) {
			t.Fatalf("usage does not mention %q:\n%s", form, usage)
		}
	}
}

// TestParseAttachAndAlias: `rtk attach` and `rtk runtime` are the same kind, and the runtime
// pairing form is untouched.
// UNTESTED: written, not run.
func TestParseAttachAndAlias(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want command
	}{
		{
			name: "attach starts nothing by default",
			args: []string{"attach"},
			want: command{kind: kindAttach},
		},
		{
			name: "attach with a list",
			args: []string{"attach", "--agents", "claude,codex"},
			want: command{kind: kindAttach, agents: "claude,codex"},
		},
		{
			name: "attach passes the raw list through",
			args: []string{"attach", "--agents", "claude,claude"},
			want: command{kind: kindAttach, agents: "claude,claude"},
		},
		{
			name: "runtime is the same kind",
			args: []string{"runtime"},
			want: command{kind: kindAttach},
		},
		{
			name: "runtime with a list is the same kind",
			args: []string{"runtime", "--agents", "codex"},
			want: command{kind: kindAttach, agents: "codex"},
		},
		{
			name: "attach --help shows the shared usage",
			args: []string{"attach", "--help"},
			want: command{kind: kindHelp},
		},
		{
			name: "runtime pair is still the pairing form",
			args: []string{"runtime", "pair", "--gateway", "100.64.0.1:7777", "--code", "abcd"},
			want: command{kind: kindRuntimePair, gatewayAddr: "100.64.0.1:7777", code: "abcd"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if err != nil {
				t.Fatalf("parseCommand(%q): %v", tc.args, err)
			}
			if got.kind != tc.want.kind || got.agents != tc.want.agents ||
				got.gatewayAddr != tc.want.gatewayAddr || got.code != tc.want.code {
				t.Fatalf("parseCommand(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}

	for _, args := range [][]string{
		{"attach", "--now"},
		{"attach", "extra"},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("parseCommand(%q) accepted the arguments", args)
		}
	}
}

// TestUsageListsAttachAndAlias pins the two new lines.
func TestUsageListsAttachAndAlias(t *testing.T) {
	for _, form := range []string{
		"rtk attach [--agents claude,codex]",
		"an alias for rtk attach",
		"rtk runtime pair --gateway ADDR --code CODE",
	} {
		if !strings.Contains(usage, form) {
			t.Fatalf("usage does not mention %q:\n%s", form, usage)
		}
	}
}

// TestParseDaemonNetwork: the network subcommand and its two flags.
// UNTESTED: written, not run.
func TestParseDaemonNetwork(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want command
	}{
		{name: "one shot", args: []string{"daemon", "network"}, want: command{kind: kindDaemonNetwork}},
		{name: "json", args: []string{"daemon", "network", "--json"}, want: command{kind: kindDaemonNetwork, networkJSON: true}},
		{name: "watch", args: []string{"daemon", "network", "--watch"}, want: command{kind: kindDaemonNetwork, networkWatch: true}},
		{
			name: "both", args: []string{"daemon", "network", "--json", "--watch"},
			want: command{kind: kindDaemonNetwork, networkJSON: true, networkWatch: true},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if err != nil {
				t.Fatalf("parseCommand(%q): %v", tc.args, err)
			}
			if got.kind != tc.want.kind || got.networkJSON != tc.want.networkJSON || got.networkWatch != tc.want.networkWatch {
				t.Fatalf("parseCommand(%q) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}

	// An unknown flag is a parse error, which exits 2 like every other usage error.
	for _, args := range [][]string{
		{"daemon", "network", "--now"},
		{"daemon", "network", "extra"},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("parseCommand(%q) accepted the arguments", args)
		}
	}
}

// TestUsageListsDaemonNetwork pins the new usage line.
func TestUsageListsDaemonNetwork(t *testing.T) {
	if !strings.Contains(usage, "rtk daemon network [--json] [--watch]") {
		t.Fatalf("usage does not mention the network command:\n%s", usage)
	}
}

// TestParseAttachExpectation: the exact-session flags and their usage errors.
// UNTESTED: written, not run.
func TestParseAttachExpectation(t *testing.T) {
	const sessionID = "0192f2b4-1111-2222-3333-444455556666"

	t.Run("an expectation alone", func(t *testing.T) {
		got, err := parseCommand([]string{"attach", "--expect-session", sessionID})
		if err != nil {
			t.Fatalf("parseCommand: %v", err)
		}
		if got.kind != kindAttach || got.expectSession != sessionID || got.agents != "" {
			t.Fatalf("command = %+v", got)
		}
	})

	t.Run("the ssh marker alone is accepted", func(t *testing.T) {
		got, err := parseCommand([]string{"attach", "--entered-via-ssh"})
		if err != nil {
			t.Fatalf("parseCommand: %v", err)
		}
		if !got.enteredViaSSH || got.expectSession != "" {
			t.Fatalf("command = %+v", got)
		}
	})

	t.Run("both together", func(t *testing.T) {
		got, err := parseCommand([]string{"runtime", "--expect-session", sessionID, "--entered-via-ssh"})
		if err != nil {
			t.Fatalf("parseCommand: %v", err)
		}
		if !got.enteredViaSSH || got.expectSession != sessionID {
			t.Fatalf("command = %+v", got)
		}
	})

	t.Run("agents with an expectation is a usage error", func(t *testing.T) {
		_, err := parseCommand([]string{"attach", "--agents", "claude", "--expect-session", sessionID})
		if err == nil {
			t.Fatal("the combination was accepted")
		}
		if !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("error = %q", err)
		}
	})

	t.Run("a value that is not a session id is a usage error", func(t *testing.T) {
		for _, bad := range []string{"nope", "0192f2b4-1111-2222-3333", "0192f2b4111122223333444455556666"} {
			if _, err := parseCommand([]string{"attach", "--expect-session", bad}); err == nil {
				t.Fatalf("%q was accepted", bad)
			}
		}
	})

	t.Run("an unknown flag is still a usage error", func(t *testing.T) {
		if _, err := parseCommand([]string{"attach", "--entered-via-ssh=maybe"}); err == nil {
			t.Fatal("a bad boolean value was accepted")
		}
	})
}

// TestUsageListsTheEntryFlags pins the usage line.
func TestUsageListsTheEntryFlags(t *testing.T) {
	for _, form := range []string{"--expect-session ID", "--entered-via-ssh"} {
		if !strings.Contains(usage, form) {
			t.Fatalf("usage does not mention %q:\n%s", form, usage)
		}
	}
}

// TestParseCardForms: the host card flags and commands. UNTESTED: written, not run.
func TestParseCardForms(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want command
	}{
		{
			name: "daemon with a card listener",
			args: []string{"daemon", "--card-listen", "100.64.0.5:7778"},
			want: command{kind: kindDaemonRun, cardListen: "100.64.0.5:7778"},
		},
		{
			name: "daemon with the card listener off",
			args: []string{"daemon", "--card-listen=off"},
			want: command{kind: kindDaemonRun, cardListen: "off"},
		},
		{
			name: "daemon with no card flag uses the default address",
			args: []string{"daemon"},
			want: command{kind: kindDaemonRun},
		},
		{
			name: "gateway card poll interval",
			args: []string{"gateway", "--listen", "127.0.0.1:7777", "--card-poll-interval", "30s"},
			want: command{kind: kindGatewayServe, listen: "127.0.0.1:7777", cardPollInterval: 30 * time.Second},
		},
		{
			name: "gateway card-target passes through",
			args: []string{"gateway", "card-target", "set", "vmb", "http://100.64.0.5:7778"},
			want: command{kind: kindGatewayAdmin, admin: []string{"card-target", "set", "vmb", "http://100.64.0.5:7778"}},
		},
		{
			name: "gateway participant passes through",
			args: []string{"gateway", "participant", "register", "svc", "--url", "http://127.0.0.1:7790/"},
			want: command{kind: kindGatewayAdmin, admin: []string{"participant", "register", "svc", "--url", "http://127.0.0.1:7790/"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCommand(tc.args)
			if err != nil {
				t.Fatalf("parseCommand(%q): %v", tc.args, err)
			}
			if got.kind != tc.want.kind || got.cardListen != tc.want.cardListen ||
				got.cardPollInterval != tc.want.cardPollInterval ||
				strings.Join(got.admin, "\x00") != strings.Join(tc.want.admin, "\x00") {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}

	if _, err := parseCommand([]string{"gateway", "--listen", "127.0.0.1:7777", "--card-poll-interval", "soon"}); err == nil {
		t.Fatal("a non-duration poll interval was accepted")
	}
	// The card LAN flags and the card command group are gone: the listener has no opt-ins, and the card
	// is a file.
	for _, args := range [][]string{
		{"daemon", "--card-listen", "0.0.0.0:7778", "--allow-lan"},
		{"daemon", "--insecure-http"},
		{"daemon", "card", "publish"},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("parseCommand(%q) was accepted", args)
		}
	}
}
