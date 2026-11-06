package gateway

// Untested: under GR-3 nothing here has been run. This file tests only the pure
// status-line builder; Run itself needs a real listener and a real database.

import (
	"strings"
	"testing"
)

// TestStatusLines: the listening line, exactly one transport line, and the firewall
// hint for both LAN modes only.
func TestStatusLines(t *testing.T) {
	tests := []struct {
		name        string
		listen      string
		allowLAN    bool
		insecure    bool
		fingerprint string
		want        []string
		absent      []string
	}{
		{
			name:   "loopback",
			listen: "127.0.0.1:7777",
			want: []string{
				"gateway listening on 127.0.0.1:7777 (state in /state/gateway)",
				"transport: plain HTTP; loopback only",
			},
			absent: []string{"Tailscale", "reachable by every device", "WARNING"},
		},
		{
			name:   "Tailscale",
			listen: "100.64.0.1:7777",
			want: []string{
				"transport: plain HTTP; encrypted only if this address really is Tailscale",
			},
			absent: []string{"loopback only", "reachable by every device", "WARNING"},
		},
		{
			name:        "LAN over HTTPS",
			listen:      "192.168.1.20:7777",
			allowLAN:    true,
			fingerprint: testFingerprint,
			want: []string{
				"transport: HTTPS, self-signed certificate, fingerprint " + testFingerprint,
				"reachable by every device on this LAN; if a runtime cannot reach this address, check that this machine's firewall allows incoming TCP on port 7777",
			},
			absent: []string{"WARNING", "loopback only", "Tailscale"},
		},
		{
			name:     "LAN over plain HTTP",
			listen:   "192.168.1.20:8080",
			allowLAN: true,
			insecure: true,
			want: []string{
				PlainHTTPWarning,
				"incoming TCP on port 8080",
			},
			absent: []string{"self-signed", "loopback only", "Tailscale"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlan(t, tc.listen, tc.allowLAN, tc.insecure)
			lines := statusLines(plan, plan.Addr.String(), "/state/gateway", tc.fingerprint)

			if len(lines) == 0 {
				t.Fatal("statusLines returned nothing")
			}
			if !strings.HasPrefix(lines[0], "gateway listening on "+plan.Addr.String()) {
				t.Fatalf("first line = %q", lines[0])
			}
			joined := strings.Join(lines, "\n")
			for _, part := range tc.want {
				if !strings.Contains(joined, part) {
					t.Fatalf("lines %q do not contain %q", joined, part)
				}
			}
			for _, part := range tc.absent {
				if strings.Contains(joined, part) {
					t.Fatalf("lines %q contain %q, which does not apply", joined, part)
				}
			}

			// Exactly one transport line.
			transports := 0
			for _, line := range lines {
				if strings.HasPrefix(line, "transport: ") || strings.HasPrefix(line, PlainHTTPWarning) {
					transports++
				}
			}
			if transports != 1 {
				t.Fatalf("%d transport lines, want exactly 1: %q", transports, lines)
			}
		})
	}
}

// TestStatusLinesNeverContainASecret: no credential or key text can appear.
func TestStatusLinesNeverContainASecret(t *testing.T) {
	plan := mustPlan(t, "192.168.1.20:7777", true, false)
	for _, line := range statusLines(plan, "192.168.1.20:7777", "/state/gateway", testFingerprint) {
		for _, secret := range []string{"PRIVATE KEY", "Bearer", "credential"} {
			if strings.Contains(line, secret) {
				t.Fatalf("status line %q contains %q", line, secret)
			}
		}
	}
}
