package gateway

// Untested: under GR-3 nothing here has been run. This file tests only the pure
// status-line builder; Run itself needs a real listener and a real database.

import (
	"strings"
	"testing"
)

// TestStatusLines: the listening line, exactly one transport line, the dashboard exposure line with NO
// authentication, and the firewall line, each with the real port (ADR-0009 D16, D17, D19).
func TestStatusLines(t *testing.T) {
	tests := []struct {
		name        string
		listen      string
		https       bool
		fingerprint string
		want        []string
		absent      []string
	}{
		{
			name:   "default wildcard, plain",
			listen: DefaultListen,
			want: []string{
				"gateway listening on 0.0.0.0:7777 (state in /state/gateway)",
				"listening on every interface (0.0.0.0), including any public one",
				PlainHTTPWarning,
				"dashboard: http://<this machine's address>:7777/ui/ - NO authentication",
				"restrict inbound TCP on port 7777",
			},
			absent: []string{"self-signed", "loopback only"},
		},
		{
			name:        "wildcard over HTTPS",
			listen:      "0.0.0.0:8443",
			https:       true,
			fingerprint: testFingerprint,
			want: []string{
				"transport: HTTPS, self-signed certificate, fingerprint " + testFingerprint,
				"dashboard: https://<this machine's address>:8443/ui/ - NO authentication",
				"restrict inbound TCP on port 8443",
			},
			absent: []string{"WARNING", "loopback only"},
		},
		{
			name:   "loopback",
			listen: "127.0.0.1:7777",
			want: []string{
				"gateway listening on 127.0.0.1:7777 (state in /state/gateway)",
				"transport: plain HTTP; loopback only",
			},
			absent: []string{"every interface", "WARNING"},
		},
		{
			name:   "an explicit address is not called every interface",
			listen: "192.168.1.20:8080",
			want:   []string{PlainHTTPWarning, "restrict inbound TCP on port 8080"},
			absent: []string{"every interface", "self-signed"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan := mustPlan(t, tc.listen, tc.https)
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
	plan := mustPlan(t, "192.168.1.20:7777", true)
	for _, line := range statusLines(plan, "192.168.1.20:7777", "/state/gateway", testFingerprint) {
		for _, secret := range []string{"PRIVATE KEY", "Bearer", "credential"} {
			if strings.Contains(line, secret) {
				t.Fatalf("status line %q contains %q", line, secret)
			}
		}
	}
}
