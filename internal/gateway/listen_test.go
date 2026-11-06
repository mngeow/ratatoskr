package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseAddressAccepts(t *testing.T) {
	tests := []struct {
		value string
		class AddrClass
	}{
		{"127.0.0.1:7777", ClassLoopback},
		{"[::1]:7777", ClassLoopback},
		{"100.64.0.1:7777", ClassTailscale},
		{"100.127.255.254:7777", ClassTailscale},
		{"[fd7a:115c:a1e0::1]:7777", ClassTailscale},
		{"192.168.1.5:7777", ClassLAN},
		{"10.0.0.1:7777", ClassLAN},
		{"172.16.0.1:7777", ClassLAN},
		{"[fc00::1]:7777", ClassLAN},
	}

	for _, tc := range tests {
		t.Run(tc.value, func(t *testing.T) {
			addrPort, class, err := ParseAddress(tc.value)
			if err != nil {
				t.Fatalf("ParseAddress(%q): %v", tc.value, err)
			}
			if addrPort.String() != tc.value {
				t.Fatalf("ParseAddress(%q) = %s", tc.value, addrPort)
			}
			if class != tc.class {
				t.Fatalf("ParseAddress(%q) class = %s, want %s", tc.value, class, tc.class)
			}
		})
	}
}

// TestParseAddressRefuses: whatever the flags say, these are never usable.
func TestParseAddressRefuses(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "hostname", value: "hostname:7777"},
		{name: "localhost", value: "localhost:7777"},
		{name: "no port", value: "100.64.0.1"},
		{name: "port zero", value: "100.64.0.1:0"},
		{name: "unspecified v4", value: "0.0.0.0:7777"},
		{name: "unspecified v6", value: "[::]:7777"},
		{name: "mapped v4", value: "[::ffff:100.64.0.1]:7777"},
		{name: "mapped loopback", value: "[::ffff:127.0.0.1]:7777"},
		{name: "mapped lan", value: "[::ffff:192.168.1.5]:7777"},
		{name: "zone", value: "[fe80::1%en0]:7777"},
		{name: "link-local v4", value: "169.254.1.1:7777"},
		{name: "link-local v6", value: "[fe80::1]:7777"},
		{name: "outside the tailscale range", value: "100.128.0.1:7777"},
		{name: "just outside 172.16/12", value: "172.32.0.1:7777"},
		{name: "public", value: "8.8.8.8:7777"},
		{name: "public v6", value: "[2001:4860:4860::8888]:7777"},
		{name: "empty port", value: ":7777"},
		{name: "empty", value: ""},
		{name: "leading space", value: " 127.0.0.1:7777"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := ParseAddress(tc.value); err == nil {
				t.Fatalf("ParseAddress accepted %q", tc.value)
			}
		})
	}
}

// TestParseAddressIsExact: the accepted value is the parsed value, never a
// reformatted one, so nothing downstream can bind something else.
func TestParseAddressIsExact(t *testing.T) {
	addrPort, _, err := ParseAddress("100.64.0.1:7777")
	if err != nil {
		t.Fatalf("ParseAddress: %v", err)
	}
	if want := netip.MustParseAddrPort("100.64.0.1:7777"); addrPort != want {
		t.Fatalf("addr = %s, want %s", addrPort, want)
	}
}

// TestTailscaleIsCheckedBeforePrivate: fd7a:115c:a1e0::/48 lies inside fc00::/7, and
// it must stay Tailscale.
func TestTailscaleIsCheckedBeforePrivate(t *testing.T) {
	addr := netip.MustParseAddr("fd7a:115c:a1e0::1")
	if !addr.IsPrivate() {
		t.Fatalf("the premise changed: %s is no longer IsPrivate", addr)
	}
	class, ok := classOf(addr)
	if !ok || class != ClassTailscale {
		t.Fatalf("classOf(%s) = %s, %v; want Tailscale", addr, class, ok)
	}
}

// TestPlanListenTable is the gateway flag table of the contract's "Direct LAN" section.
func TestPlanListenTable(t *testing.T) {
	tests := []struct {
		name         string
		listen       string
		allowLAN     bool
		insecureHTTP bool
		want         Transport
		refusal      string // a phrase the refusal must contain; empty means accepted
	}{
		{name: "Tailscale plain", listen: "100.64.0.1:7777", want: TransportPlain},
		{name: "loopback plain", listen: "127.0.0.1:7777", want: TransportPlain},
		{name: "Tailscale v6 plain", listen: "[fd7a:115c:a1e0::1]:7777", want: TransportPlain},
		{name: "Tailscale with allow-lan", listen: "100.64.0.1:7777", allowLAN: true, refusal: "apply only to a LAN address"},
		{name: "loopback with insecure-http", listen: "127.0.0.1:7777", insecureHTTP: true, refusal: "apply only to a LAN address"},
		{name: "Tailscale with both", listen: "100.64.0.1:7777", allowLAN: true, insecureHTTP: true, refusal: "apply only to a LAN address"},
		{name: "LAN with no flag", listen: "192.168.1.5:7777", refusal: FlagAllowLAN},
		{name: "LAN with insecure-http alone", listen: "192.168.1.5:7777", insecureHTTP: true, refusal: "also needs " + FlagAllowLAN},
		{name: "LAN with allow-lan is HTTPS", listen: "192.168.1.5:7777", allowLAN: true, want: TransportHTTPS},
		{name: "LAN with both is plain HTTP", listen: "192.168.1.5:7777", allowLAN: true, insecureHTTP: true, want: TransportLANHTTP},
		{name: "LAN v6 with allow-lan", listen: "[fc00::1]:7777", allowLAN: true, want: TransportHTTPS},
		{name: "public with every flag", listen: "8.8.8.8:7777", allowLAN: true, insecureHTTP: true, refusal: "not a loopback"},
		{name: "unspecified with every flag", listen: "0.0.0.0:7777", allowLAN: true, insecureHTTP: true, refusal: "unspecified"},
		{name: "link-local with every flag", listen: "169.254.1.1:7777", allowLAN: true, insecureHTTP: true, refusal: "not a loopback"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanListen(tc.listen, tc.allowLAN, tc.insecureHTTP)
			if tc.refusal != "" {
				if err == nil {
					t.Fatalf("PlanListen accepted %q (%v, %v)", tc.listen, tc.allowLAN, tc.insecureHTTP)
				}
				if !strings.Contains(err.Error(), tc.refusal) {
					t.Fatalf("refusal %q does not contain %q", err, tc.refusal)
				}
				return
			}
			if err != nil {
				t.Fatalf("PlanListen: %v", err)
			}
			if plan.Transport != tc.want {
				t.Fatalf("transport = %d, want %d", plan.Transport, tc.want)
			}
		})
	}
}

// Nothing here calls Listen: under GR-3 no socket may be bound, and port 0 is
// refused anyway, so the bind path is verified only by reading it.
