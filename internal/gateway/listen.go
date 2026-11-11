package gateway

// The listen-address check (ADR-0009 D8, D14, D16). The gateway binds 0.0.0.0 by default and accepts
// any IP literal with an explicit port: the wildcard and public refusals of ADR-0002 are gone, and
// network firewalling is the exposure boundary, not this check. What stays is the IP-literal parsing:
// no hostnames, no zones, no IPv4-mapped IPv6, no port 0.
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
	// ClassWildcard is the unspecified address (0.0.0.0 or ::): every interface of this machine.
	ClassWildcard
	// ClassPublic is any other address, including a public one.
	ClassPublic
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
	case ClassWildcard:
		return "wildcard"
	case ClassPublic:
		return "public"
	}
	return "unknown"
}

// Transport is how a gateway and its runtimes talk. Since ADR-0009 it no longer depends on the address
// class: it is fixed by the flags the user typed, on each side separately (host and dashboard: plain unless
// --https; participant API: HTTPS unless --participant-insecure-http). It is never learned from the
// network, because a network attacker could downgrade that.
type Transport int

const (
	// TransportPlain is plain HTTP.
	TransportPlain Transport = iota + 1
	// TransportHTTPS is HTTPS with a self-signed certificate that runtimes pin.
	TransportHTTPS
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

// classifyAny is classOf extended to every address: the wildcard and everything else are classes too.
func classifyAny(addr netip.Addr) AddrClass {
	if class, ok := classOf(addr); ok {
		return class
	}
	if addr.IsUnspecified() {
		return ClassWildcard
	}
	return ClassPublic
}

// ParseAddress validates a gateway address and says which class it is in.
//
// It is the one address rule, shared by the gateway's --listen and by the runtime's
// pairing. The string is parsed exactly as typed: no trimming, no name resolution, no
// repair. A hostname fails at the parse, which is the point - resolving a name would
// make the accepted address depend on DNS.
//
// Every IP literal is accepted, the unspecified address and public addresses included (D16): the class
// says what kind it is, and decides nothing. Hostnames are not accepted.
//
// UNTESTED.
func ParseAddress(value string) (netip.AddrPort, AddrClass, error) {
	return parseAddressAs("gateway", value)
}

// ParseAddressFor is ParseAddress with the thing being addressed named in the messages, so another
// listener (the daemon's card listener) can reuse the one rule without saying "gateway". With subject
// "gateway" the messages are byte for byte ParseAddress's.
//
// UNTESTED.
func ParseAddressFor(subject, value string) (netip.AddrPort, AddrClass, error) {
	return parseAddressAs(subject, value)
}

// parseAddressAs is the one address rule; subject only changes the words.
func parseAddressAs(subject, value string) (netip.AddrPort, AddrClass, error) {
	addrPort, err := netip.ParseAddrPort(value)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf(
			"%s: %q is not an IP address with an explicit port: give an IP literal such as 100.64.0.1:7777 or [fd7a:115c:a1e0::1]:7777 (hostnames are not accepted): %w",
			subject, value, err,
		)
	}

	if addrPort.Port() == 0 {
		return netip.AddrPort{}, 0, fmt.Errorf("%s: %q has port 0: the %s endpoint must stay stable", subject, value, subject)
	}
	if addrPort.Addr().Zone() != "" {
		return netip.AddrPort{}, 0, fmt.Errorf("%s: %q carries an IPv6 zone; give the address without it", subject, value)
	}
	// Checked before anything unmaps: an IPv4-mapped IPv6 address is a different
	// literal and would bind differently from what was typed.
	if addrPort.Addr().Is4In6() {
		return netip.AddrPort{}, 0, fmt.Errorf("%s: %q is an IPv4-mapped IPv6 address; give the IPv4 address itself", subject, value)
	}
	return addrPort, classifyAny(addrPort.Addr()), nil
}

// parseAddressStrict is the address rule of ADR-0002 that ADR-0009 D16 lifted for the gateway's own listeners
// only: loopback, Tailscale and private LAN addresses, and nothing else. A card target is an address the gateway
// DIALS, so it keeps the refusals of the wildcard and of every public or link-local address.
func parseAddressStrict(subject, value string) (netip.AddrPort, AddrClass, error) {
	addrPort, class, err := parseAddressAs(subject, value)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	switch class {
	case ClassWildcard:
		return netip.AddrPort{}, 0, fmt.Errorf("%s: %q is the unspecified address; name the interface address explicitly", subject, value)
	case ClassPublic:
		return netip.AddrPort{}, 0, fmt.Errorf(
			"%s: %s is not a loopback, Tailscale or private LAN address: only 127.0.0.0/8 or ::1, 100.64.0.0/10 or fd7a:115c:a1e0::/48, 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 or fc00::/7 are allowed, and there is no public %s mode",
			subject, value, subject)
	}
	return addrPort, class, nil
}

// ListenPlan is a validated --listen value with the transport its flags select.
type ListenPlan struct {
	Addr      netip.AddrPort
	Class     AddrClass
	Transport Transport
}

// Flag spellings, shared so a message and a table cannot drift apart. FlagAllowLAN and FlagInsecureHTTP are
// RETIRED on `rtk gateway` (D20): accepted, ignored, and warned about for one release (N2).
const (
	FlagHTTPS        = "--https"
	FlagAllowLAN     = "--allow-lan"
	FlagInsecureHTTP = "--insecure-http"
)

// DefaultListen is the address of `rtk gateway` without --listen (D8): every interface, port 7777, shared
// by the host protocol and the dashboard.
const DefaultListen = "0.0.0.0:7777"

// RetiredGatewayFlagWarning is printed once on stderr when `rtk gateway` receives --allow-lan or
// --insecure-http (N2). It names the breaking case.
const RetiredGatewayFlagWarning = "warning: rtk gateway --allow-lan and --insecure-http are retired and have no effect (they stay accepted for one release). " +
	"Plain HTTP is now the default on the host port, and --allow-lan is not needed: any address may be named. " +
	"BREAKING: a LAN gateway that relied on the old HTTPS default is now plain HTTP, readable by anything on the network path; add --https to keep HTTPS."

// PlanListen turns a --listen value and the --https flag into the host plan: the transport is plain unless
// https is set, whatever the address (D17, D19). Nothing is checked beyond the address literal. It touches
// no file, lock or socket: this is the first thing Run does.
//
// UNTESTED.
func PlanListen(value string, https bool) (ListenPlan, error) {
	return planListenAs("gateway", value, https)
}

// PlanListenFor is PlanListen for another listener: the messages name subject instead of "gateway". With
// subject "gateway" the messages are byte for byte PlanListen's.
//
// UNTESTED.
func PlanListenFor(subject, value string, https bool) (ListenPlan, error) {
	return planListenAs(subject, value, https)
}

// planListenAs is the one plan; subject only changes the words.
func planListenAs(subject, value string, https bool) (ListenPlan, error) {
	addrPort, class, err := parseAddressAs(subject, value)
	if err != nil {
		return ListenPlan{}, err
	}
	transport := TransportPlain
	if https {
		transport = TransportHTTPS
	}
	return ListenPlan{Addr: addrPort, Class: class, Transport: transport}, nil
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
	return listenAs("gateway", addrPort)
}

// ListenFor is Listen for another listener: the bind rules are the same, the messages name subject.
// With subject "gateway" the messages are byte for byte Listen's.
//
// UNTESTED.
func ListenFor(subject string, addrPort netip.AddrPort) (net.Listener, error) {
	return listenAs(subject, addrPort)
}

// listenAs is the one bind; subject only changes the words.
func listenAs(subject string, addrPort netip.AddrPort) (net.Listener, error) {
	network := "tcp6"
	if addrPort.Addr().Is4() {
		network = "tcp4"
	}

	listener, err := net.Listen(network, addrPort.String())
	if err != nil {
		// The Tailscale advice belongs to a Tailscale address only.
		if class, _ := classOf(addrPort.Addr()); class == ClassTailscale {
			return nil, fmt.Errorf(
				"%s: cannot bind %s: this machine needs an OS-level Tailscale interface with that address; userspace-networking mode is not supported: %w",
				subject, addrPort, err,
			)
		}
		return nil, fmt.Errorf("%s: cannot bind %s: this machine must own that address: %w", subject, addrPort, err)
	}

	actual, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, fmt.Errorf("%s: listener reported %q, which is not a TCP address", subject, listener.Addr())
	}
	// The comparison unmaps only the actual side: a tcp4 listener may report the
	// address in its 4-in-6 form, while the requested value stays as typed.
	if !sameAddrPort(actual.AddrPort(), addrPort) {
		_ = listener.Close()
		return nil, fmt.Errorf("%s: asked for %s but the listener bound %s", subject, addrPort, actual.AddrPort())
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
