package gateway

// The listen-address check. The gateway has no public mode: it binds a Tailscale
// address, loopback or - with --allow-lan - a private LAN address, and nothing else,
// so a mistake here cannot put a Ratatoskr gateway on the open internet.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No address has been parsed
// and no socket has been bound.

import (
	"fmt"
	"net"
	"net/netip"
)

var (
	// tailscaleV4 and tailscaleV6 are Tailscale's address ranges. Membership in a
	// range does not prove this machine owns the address; the bind succeeding
	// does, which is why Listen checks the result.
	tailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// AddrClass says what kind of address a gateway is on. It is decided by the
// address alone and is what the transport rules key on.
type AddrClass int

const (
	// ClassLoopback is 127.0.0.0/8 or ::1: a gateway and runtime on one machine.
	ClassLoopback AddrClass = iota + 1
	// ClassTailscale is 100.64.0.0/10 or fd7a:115c:a1e0::/48.
	ClassTailscale
	// ClassLAN is a private address that is not Tailscale: 10.0.0.0/8, 172.16.0.0/12,
	// 192.168.0.0/16 or fc00::/7.
	ClassLAN
)

// String names the class for messages.
func (c AddrClass) String() string {
	switch c {
	case ClassLoopback:
		return "loopback"
	case ClassTailscale:
		return "Tailscale"
	case ClassLAN:
		return "LAN"
	}
	return "unknown"
}

// Transport is how a gateway and its runtimes talk. It is fixed by the address class
// and the flags the user typed, on each side separately; it is never learned from the
// network, because a network attacker could downgrade that.
type Transport int

const (
	// TransportPlain is plain HTTP on a Tailscale or loopback address.
	TransportPlain Transport = iota + 1
	// TransportHTTPS is HTTPS with a self-signed certificate that runtimes pin.
	TransportHTTPS
	// TransportLANHTTP is plain HTTP on a LAN address, chosen with --insecure-http.
	TransportLANHTTP
)

// classOf reports the class of an address that already passed the shared checks.
//
// The Tailscale ranges are tested before IsPrivate: fd7a:115c:a1e0::/48 lies inside
// fc00::/7, and it must stay Tailscale.
func classOf(addr netip.Addr) (AddrClass, bool) {
	switch {
	case addr.IsLoopback():
		return ClassLoopback, true
	case tailscaleV4.Contains(addr) || tailscaleV6.Contains(addr):
		return ClassTailscale, true
	case addr.IsPrivate():
		return ClassLAN, true
	}
	return 0, false
}

// ParseAddress validates a gateway address and says which class it is in.
//
// It is the one address rule, shared by the gateway's --listen and by the runtime's
// pairing. The string is parsed exactly as typed: no trimming, no name resolution, no
// repair. A hostname fails at the parse, which is the point - resolving a name would
// make the accepted address depend on DNS.
//
// Loopback, Tailscale and LAN addresses are accepted here. Whether a LAN address may
// be used is the flag tables' decision (PlanListen, and the runtime's), not this one's.
// Public, link-local and unspecified addresses are refused whatever the flags say.
//
// UNTESTED.
func ParseAddress(value string) (netip.AddrPort, AddrClass, error) {
	addrPort, err := netip.ParseAddrPort(value)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf(
			"gateway: %q is not an IP address with an explicit port: give an IP literal such as 100.64.0.1:7777 or [fd7a:115c:a1e0::1]:7777 (hostnames are not accepted): %w",
			value, err,
		)
	}

	if addrPort.Port() == 0 {
		return netip.AddrPort{}, 0, fmt.Errorf("gateway: %q has port 0: the gateway endpoint must stay stable", value)
	}
	if addrPort.Addr().Zone() != "" {
		return netip.AddrPort{}, 0, fmt.Errorf("gateway: %q carries an IPv6 zone; give the address without it", value)
	}
	// Checked before anything unmaps: an IPv4-mapped IPv6 address is a different
	// literal and would bind differently from what was typed.
	if addrPort.Addr().Is4In6() {
		return netip.AddrPort{}, 0, fmt.Errorf("gateway: %q is an IPv4-mapped IPv6 address; give the IPv4 address itself", value)
	}
	if addrPort.Addr().IsUnspecified() {
		return netip.AddrPort{}, 0, fmt.Errorf("gateway: %q is the unspecified address; name the interface address explicitly", value)
	}

	class, ok := classOf(addrPort.Addr())
	if !ok {
		return netip.AddrPort{}, 0, fmt.Errorf(
			"gateway: %s is not a loopback, Tailscale or private LAN address: only 127.0.0.0/8 or ::1, 100.64.0.0/10 or fd7a:115c:a1e0::/48, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 or fc00::/7 are allowed, and there is no public gateway mode",
			value,
		)
	}
	return addrPort, class, nil
}

// ListenPlan is a validated --listen value with the transport its flags select.
type ListenPlan struct {
	Addr      netip.AddrPort
	Class     AddrClass
	Transport Transport
}

// Flag spellings, shared so a message and a table cannot drift apart.
const (
	FlagAllowLAN     = "--allow-lan"
	FlagInsecureHTTP = "--insecure-http"
)

// PlanListen applies the gateway flag table of the contract's "Direct LAN" section.
//
// Every refusal names the flag or the address to use instead, and none touches a file,
// a lock or a socket: this is the first thing Run does.
//
// UNTESTED.
func PlanListen(value string, allowLAN, insecureHTTP bool) (ListenPlan, error) {
	addrPort, class, err := ParseAddress(value)
	if err != nil {
		return ListenPlan{}, err
	}

	if class != ClassLAN {
		if allowLAN || insecureHTTP {
			return ListenPlan{}, fmt.Errorf(
				"gateway: %s and %s apply only to a LAN address (10.x, 172.16-31.x, 192.168.x or fc00::/7), and %s is %s: drop the flag, or listen on a LAN address",
				FlagAllowLAN, FlagInsecureHTTP, value, class,
			)
		}
		return ListenPlan{Addr: addrPort, Class: class, Transport: TransportPlain}, nil
	}

	switch {
	case !allowLAN && insecureHTTP:
		return ListenPlan{}, fmt.Errorf(
			"gateway: %s also needs %s: plain HTTP on a LAN is allowed only after the LAN listener itself is (use %s for HTTPS, or both for plain HTTP)",
			FlagInsecureHTTP, FlagAllowLAN, FlagAllowLAN,
		)
	case !allowLAN:
		return ListenPlan{}, fmt.Errorf(
			"gateway: %s is a LAN address: add %s to listen on it (HTTPS with a pinned certificate), or use a Tailscale or loopback address",
			value, FlagAllowLAN,
		)
	case insecureHTTP:
		return ListenPlan{Addr: addrPort, Class: class, Transport: TransportLANHTTP}, nil
	}
	return ListenPlan{Addr: addrPort, Class: class, Transport: TransportHTTPS}, nil
}

// Listen binds one parsed address, and only that one.
//
// The family comes from the parsed address, never from the original string, so a
// re-resolved or reformatted value cannot change what is bound. After binding,
// the listener's actual address is compared with what was asked for; a mismatch
// closes the listener and fails. There is no fallback to another address and no
// second attempt: a wrong address is a configuration error the user must see.
//
// A successful bind is also the only proof that this machine owns the address -
// range membership alone proves nothing.
//
// UNTESTED.
func Listen(addrPort netip.AddrPort) (net.Listener, error) {
	network := "tcp6"
	if addrPort.Addr().Is4() {
		network = "tcp4"
	}

	listener, err := net.Listen(network, addrPort.String())
	if err != nil {
		// The Tailscale advice belongs to a Tailscale address only.
		if class, _ := classOf(addrPort.Addr()); class == ClassTailscale {
			return nil, fmt.Errorf(
				"gateway: cannot bind %s: this machine needs an OS-level Tailscale interface with that address; userspace-networking mode is not supported: %w",
				addrPort, err,
			)
		}
		return nil, fmt.Errorf("gateway: cannot bind %s: this machine must own that address: %w", addrPort, err)
	}

	actual, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("gateway: listener reported %q, which is not a TCP address", listener.Addr())
	}
	// The comparison unmaps only the actual side: a tcp4 listener may report the
	// address in its 4-in-6 form, while the requested value stays as typed.
	if !sameAddrPort(actual.AddrPort(), addrPort) {
		_ = listener.Close()
		return nil, fmt.Errorf("gateway: asked for %s but the listener bound %s", addrPort, actual.AddrPort())
	}
	return listener, nil
}

// sameAddrPort compares a bound address with the requested one.
func sameAddrPort(actual, requested netip.AddrPort) bool {
	if actual.Port() != requested.Port() {
		return false
	}
	actualAddr := actual.Addr()
	if actualAddr.Is4In6() {
		actualAddr = actualAddr.Unmap()
	}
	return actualAddr == requested.Addr()
}
