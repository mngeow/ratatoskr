package main

// Untested: under GR-3 nothing here has been run.

import (
	"strings"
	"testing"
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
			want: command{kind: kindRuntimeRun, agents: "claude,codex"},
		},
		{
			// The raw string is passed through; tui.ParseAgents validates it later.
			name: "runtime run passes an odd list through", args: []string{"runtime", "--agents", "claude,claude"},
			want: command{kind: kindRuntimeRun, agents: "claude,claude"},
		},
		{
			name: "runtime run with no flag", args: []string{"runtime"},
			want: command{kind: kindRuntimeRun, agents: ""},
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
