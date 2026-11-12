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
		// ADR-0009 D16: the wildcard and public addresses are accepted, and only classified.
		{"0.0.0.0:7777", ClassWildcard},
		{"[::]:7777", ClassWildcard},
		{"8.8.8.8:7777", ClassPublic},
		{"[2001:4860:4860::8888]:7777", ClassPublic},
		{"100.128.0.1:7777", ClassPublic},
		{"172.32.0.1:7777", ClassPublic},
		{"169.254.1.1:7777", ClassPublic},
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

// TestParseAddressRefuses: these are never usable (the IP-literal parsing stays; the class refusals are gone).
func TestParseAddressRefuses(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "hostname", value: "hostname:7777"},
		{name: "localhost", value: "localhost:7777"},
		{name: "no port", value: "100.64.0.1"},
		{name: "port zero", value: "100.64.0.1:0"},
		{name: "mapped v4", value: "[::ffff:100.64.0.1]:7777"},
		{name: "mapped loopback", value: "[::ffff:127.0.0.1]:7777"},
		{name: "mapped lan", value: "[::ffff:192.168.1.5]:7777"},
		{name: "zone", value: "[fe80::1%en0]:7777"},
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

// TestPlanListenTable: the transport is plain unless --https, whatever the address (D17, D19).
func TestPlanListenTable(t *testing.T) {
	tests := []struct {
		name   string
		listen string
		https  bool
		want   Transport
		class  AddrClass
	}{
		{name: "default address plain", listen: DefaultListen, want: TransportPlain, class: ClassWildcard},
		{name: "wildcard https", listen: DefaultListen, https: true, want: TransportHTTPS, class: ClassWildcard},
		{name: "Tailscale plain", listen: "100.64.0.1:7777", want: TransportPlain, class: ClassTailscale},
		{name: "Tailscale https", listen: "100.64.0.1:7777", https: true, want: TransportHTTPS, class: ClassTailscale},
		{name: "loopback plain", listen: "127.0.0.1:7777", want: TransportPlain, class: ClassLoopback},
		{name: "LAN is plain now", listen: "192.168.1.5:7777", want: TransportPlain, class: ClassLAN},
		{name: "LAN https", listen: "192.168.1.5:7777", https: true, want: TransportHTTPS, class: ClassLAN},
		{name: "explicit public plain", listen: "8.8.8.8:7777", want: TransportPlain, class: ClassPublic},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := PlanListen(tc.listen, tc.https)
			if err != nil {
				t.Fatalf("PlanListen: %v", err)
			}
			if plan.Transport != tc.want || plan.Class != tc.class {
				t.Fatalf("plan = transport %d class %s, want %d %s", plan.Transport, plan.Class, tc.want, tc.class)
			}
		})
	}
	if _, err := PlanListen("hostname:7777", false); err == nil || !strings.Contains(err.Error(), "IP literal") {
		t.Fatalf("a hostname must still be refused: %v", err)
	}
}

// Nothing here calls Listen: under GR-3 no socket may be bound, and port 0 is
// refused anyway, so the bind path is verified only by reading it.
