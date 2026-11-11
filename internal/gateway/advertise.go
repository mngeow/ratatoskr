package gateway

// The address printed for pairing when the gateway is bound to the wildcard (ADR-0009, DevOps consult).
//
// An endpoint file keeps what was BOUND (0.0.0.0:7777). 0.0.0.0 (or ::) is never a dial target, so the pair
// commands re-derive suggestions at pair time from this machine's interfaces: up, unicast, not loopback, not
// link-local, in the listener's address family. One is chosen (Tailscale first, then private LAN, then any
// other global unicast), the rest are listed. The choice is a SUGGESTION of a local address, not proof that
// it is reachable from the machine being paired; --listen with a concrete IP:PORT is the deterministic
// override. Interface names are not used: utun and bridge interfaces can be real routes.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// localAddresses lists the addresses of this machine's up interfaces. It is a variable so a test can supply
// a fixed list.
var localAddresses = func() []netip.Addr {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if addr, ok := netip.AddrFromSlice(ip); ok {
				out = append(out, addr.Unmap())
			}
		}
	}
	return out
}

// dialRank orders candidates: Tailscale, then private LAN, then everything else.
func dialRank(addr netip.Addr) int {
	switch classifyAny(addr) {
	case ClassTailscale:
		return 0
	case ClassLAN:
		return 1
	}
	return 2
}

// DialAddresses turns a recorded listen address into addresses a peer can use. For a concrete bind address
// it is that address, and nothing else. For the wildcard it is the suggested candidate and the others of the
// same family, each as host:port (IPv6 bracketed). When nothing qualifies, chosen is empty and the caller
// prints a placeholder and the manual-entry hint. Never returns 0.0.0.0 or ::.
func DialAddresses(listen string) (chosen string, others []string) {
	bound, err := netip.ParseAddrPort(listen)
	if err != nil {
		return "", nil
	}
	if !bound.Addr().IsUnspecified() {
		return bound.String(), nil
	}
	want4 := bound.Addr().Is4()
	seen := map[netip.Addr]bool{}
	var candidates []netip.Addr
	for _, addr := range localAddresses() {
		if addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() || addr.IsMulticast() || !addr.IsValid() {
			continue
		}
		if addr.Is4() != want4 || seen[addr] {
			continue
		}
		seen[addr] = true
		candidates = append(candidates, addr)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if ri, rj := dialRank(candidates[i]), dialRank(candidates[j]); ri != rj {
			return ri < rj
		}
		return candidates[i].Less(candidates[j])
	})
	if len(candidates) == 0 {
		return "", nil
	}
	chosen = netip.AddrPortFrom(candidates[0], bound.Port()).String()
	for _, addr := range candidates[1:] {
		others = append(others, netip.AddrPortFrom(addr, bound.Port()).String())
	}
	return chosen, others
}

// dialPlaceholder is what a command prints when the gateway is on the wildcard and no interface address
// qualifies: an explicit manual-entry marker, never a runnable-looking endpoint.
func dialPlaceholder(listen string) string {
	if bound, err := netip.ParseAddrPort(listen); err == nil {
		return "<THIS_MACHINE_IP>:" + strconv.Itoa(int(bound.Port()))
	}
	return "<THIS_MACHINE_IP>:PORT"
}

// dialHintLines are the advisory lines printed AFTER the critical pairing lines, never between them:
// which address was chosen and what else exists.
func dialHintLines(listen, chosen string, others []string) []string {
	bound, err := netip.ParseAddrPort(listen)
	if err != nil || !bound.Addr().IsUnspecified() {
		return nil
	}
	if chosen == "" {
		return []string{"the gateway listens on every interface but no usable address of this machine was found: replace " + dialPlaceholder(listen) + " with the address the other machine can reach (or start the gateway with --listen IP:PORT)"}
	}
	lines := []string{"the address above is a suggested local address of this machine, not proven reachable from the other machine"}
	if len(others) > 0 {
		lines = append(lines, "other addresses of this machine: "+strings.Join(others, ", "))
	}
	return lines
}
