package gateway

// Written under GR-3 and never run. The interface list is replaced by a fixed one: no interface is read.

import (
	"net/netip"
	"strings"
	"testing"
)

func withLocalAddresses(t *testing.T, addrs ...string) {
	t.Helper()
	previous := localAddresses
	t.Cleanup(func() { localAddresses = previous })
	localAddresses = func() []netip.Addr {
		var out []netip.Addr
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out
	}
}

func TestDialAddressesConcreteBindIsUsedAsIs(t *testing.T) {
	withLocalAddresses(t, "192.168.1.5")
	chosen, others := DialAddresses("100.64.0.1:7777")
	if chosen != "100.64.0.1:7777" || others != nil {
		t.Fatalf("got %q %v", chosen, others)
	}
}

func TestDialAddressesWildcardPrefersTailscaleThenLANThenOther(t *testing.T) {
	withLocalAddresses(t, "127.0.0.1", "169.254.3.3", "203.0.113.9", "192.168.1.5", "100.64.0.7", "192.168.1.5", "10.0.0.2", "fd7a:115c:a1e0::5")
	chosen, others := DialAddresses("0.0.0.0:7777")
	if chosen != "100.64.0.7:7777" {
		t.Fatalf("chosen = %q", chosen)
	}
	if got := strings.Join(others, ","); got != "10.0.0.2:7777,192.168.1.5:7777,203.0.113.9:7777" {
		t.Fatalf("others = %q (loopback, link-local, v6 and duplicates must be gone)", got)
	}
	for _, a := range append([]string{chosen}, others...) {
		if strings.HasPrefix(a, "0.0.0.0") || strings.HasPrefix(a, "[::]") {
			t.Fatalf("a wildcard was offered as a dial target: %q", a)
		}
	}
}

func TestDialAddressesIPv6WildcardUsesOnlyV6WithBrackets(t *testing.T) {
	withLocalAddresses(t, "192.168.1.5", "fc00::9", "fd7a:115c:a1e0::5")
	chosen, others := DialAddresses("[::]:7777")
	if chosen != "[fd7a:115c:a1e0::5]:7777" || len(others) != 1 || others[0] != "[fc00::9]:7777" {
		t.Fatalf("got %q %v (Tailscale v6 must outrank generic fc00::/7)", chosen, others)
	}
}

func TestDialAddressesNothingUsable(t *testing.T) {
	withLocalAddresses(t, "127.0.0.1", "169.254.1.1")
	chosen, others := DialAddresses("0.0.0.0:7777")
	if chosen != "" || others != nil {
		t.Fatalf("got %q %v", chosen, others)
	}
	if got := dialPlaceholder("0.0.0.0:7777"); got != "<THIS_MACHINE_IP>:7777" {
		t.Fatalf("placeholder = %q", got)
	}
	hints := strings.Join(dialHintLines("0.0.0.0:7777", "", nil), "\n")
	if !strings.Contains(hints, "<THIS_MACHINE_IP>:7777") || !strings.Contains(hints, "--listen IP:PORT") {
		t.Fatalf("hint = %q", hints)
	}
}

func TestParticipantPairPrintsASuggestedAddressForAWildcardBind(t *testing.T) {
	withLocalAddresses(t, "100.64.0.7", "192.168.1.5")
	store, _ := openStore(t, false)
	recordEndpoint(t, store, ParticipantOptions{Listen: "0.0.0.0:7790"})
	out, _, err := participantCmd(t, store, "pair", "pi-lab")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if strings.Contains(out, "0.0.0.0") || !strings.Contains(out, "participant API: https://100.64.0.7:7790") {
		t.Fatalf("output:\n%s", out)
	}
	// The advice comes after the pins and before the final line; the critical lines stay adjacent.
	api, cert, key, hint, other, last := -1, -1, -1, -1, -1, len(lines)-1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "participant API:"):
			api = i
		case strings.HasPrefix(l, "certificate SHA-256"):
			cert = i
		case strings.HasPrefix(l, "key pin"):
			key = i
		case strings.Contains(l, "suggested local address"):
			hint = i
		case strings.HasPrefix(l, "other addresses of this machine: 192.168.1.5:7790"):
			other = i
		}
	}
	if !(api >= 0 && cert == api+1 && key == cert+1 && hint > key && other > hint && other < last) ||
		!strings.HasPrefix(lines[last], "the address above was recorded") {
		t.Fatalf("order wrong:\n%s", out)
	}
}
