package gateway

// Card targets: where the gateway polls a host's card, entered by the gateway's owner and never
// learned from the network.
//
// A target is an IP literal with a port, a scheme and, for HTTPS, a pinned certificate fingerprint.
// It is checked when it is entered and again, on the address actually dialled, at every poll
// (carddial.go). It is never read from a card, a message, a report or a skill, so a host cannot
// change where it is polled.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// subjectCardTarget names a card target in the shared address messages.
const subjectCardTarget = "card target"

// CardTarget is one host's registered card endpoint.
type CardTarget struct {
	Host        string
	Scheme      string // "http" or "https"
	Addr        netip.AddrPort
	Class       AddrClass
	Fingerprint string // "sha256:..." for https, "" for http
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// URL is the exact URL polled: the registered scheme and address and the fixed well-known path.
func (t CardTarget) URL() string {
	return t.Scheme + "://" + t.Addr.String() + protocol.CardPath
}

// classKey is the class as the database stores it.
func classKey(class AddrClass) string {
	switch class {
	case ClassLoopback:
		return "loopback"
	case ClassTailscale:
		return "tailscale"
	case ClassLAN:
		return "lan"
	}
	return ""
}

// classFromKey is classKey's inverse; a value it does not know is refused rather than guessed.
func classFromKey(key string) (AddrClass, bool) {
	switch key {
	case "loopback":
		return ClassLoopback, true
	case "tailscale":
		return ClassTailscale, true
	case "lan":
		return ClassLAN, true
	}
	return 0, false
}

// ParseCardTarget validates what the owner typed for `card-target set`.
//
// The rules, in the contract's words:
//
//   - the host part is an IP literal with an explicit port (no name resolution, so no rebinding);
//   - the path is empty, "/" or exactly the well-known card path, and nothing else: no userinfo, query
//     or fragment either. Only the scheme and the address are kept;
//   - plain http on loopback, Tailscale and private LAN (RFC 1918) addresses, with no flag (the card
//     is public by the owner's decision, and the daemon serves plain HTTP); https only on a LAN
//     address, and then --fingerprint is required;
//   - a fingerprint with http, or --insecure-http at all, is refused: a flag that does nothing hides a
//     mistake;
//   - the address must not be on the deny list.
//
// Host is left empty; the caller fills it from the paired host.
//
// UNTESTED.
func ParseCardTarget(raw, fingerprint string, insecureHTTP bool) (CardTarget, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return CardTarget{}, errors.New("gateway: the card target must look like http://100.64.0.5:7778 (an IP address and a port)")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return CardTarget{}, errors.New("gateway: the card target must start with http:// or https://")
	}
	if parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return CardTarget{}, errors.New("gateway: the card target takes no credentials, query or fragment: give only the scheme, the address and the port")
	}
	if parsed.Path != "" && parsed.Path != "/" && parsed.Path != protocol.CardPath {
		return CardTarget{}, fmt.Errorf("gateway: the card target path is fixed: leave it out, or give exactly %s", protocol.CardPath)
	}

	addr, class, err := parseAddressStrict(subjectCardTarget, parsed.Host)
	if err != nil {
		return CardTarget{}, err
	}
	if reason := DenyReason(addr.Addr()); reason != "" {
		return CardTarget{}, fmt.Errorf("gateway: the card target address is refused: %s", reason)
	}

	target := CardTarget{Scheme: parsed.Scheme, Addr: addr, Class: class}

	switch {
	case parsed.Scheme == "https" && class != ClassLAN:
		return CardTarget{}, fmt.Errorf("gateway: https is for a LAN card target; a %s address uses http", class)

	case parsed.Scheme == "https":
		if fingerprint == "" {
			return CardTarget{}, errors.New("gateway: an https card target needs --fingerprint sha256:<64 hex digits> (the host's card certificate)")
		}
		if _, err := ParseFingerprint(fingerprint); err != nil {
			return CardTarget{}, err
		}
		if insecureHTTP {
			return CardTarget{}, errors.New("gateway: --insecure-http applies only to a plain-HTTP LAN card target")
		}
		target.Fingerprint = fingerprint

	case fingerprint != "":
		return CardTarget{}, errors.New("gateway: --fingerprint applies only to an https card target")

	case insecureHTTP:
		return CardTarget{}, errors.New("gateway: --insecure-http is not needed: plain http is accepted for loopback, Tailscale and private LAN card targets")
	}

	return target, nil
}

// denied is one entry of the deny list.
type denied struct {
	prefix netip.Prefix
	reason string
}

// denyList is the explicit list of addresses a card target may never be, on top of the address
// classes. Allowed ranges still contain addresses that are not hosts:
//
//   - 169.254.0.0/16 is link-local and holds the cloud metadata address 169.254.169.254 (AWS
//     documents it, and fd00:ec2::254, as its instance metadata addresses);
//   - 100.100.100.200 is a cloud metadata address inside the Tailscale range. NOT VERIFIED: it was
//     named from memory and the one search made for it found no page;
//   - 100.100.100.100 and fd7a:115c:a1e0::53 are this machine's own Tailscale resolver and device
//     services (Tailscale documents them as local to the device), reachable from here and never a
//     host's card;
//   - fd00:ec2::254 is AWS's IPv6 metadata address, inside fc00::/7.
//
// Unspecified and multicast addresses are refused by predicate. The list only ever refuses more.
var denyList = []denied{
	{netip.MustParsePrefix("169.254.0.0/16"), "a link-local address, which includes cloud metadata"},
	{netip.MustParsePrefix("100.100.100.200/32"), "a cloud metadata address"},
	{netip.MustParsePrefix("100.100.100.100/32"), "this machine's own Tailscale service address"},
	{netip.MustParsePrefix("fd7a:115c:a1e0::53/128"), "this machine's own Tailscale service address"},
	{netip.MustParsePrefix("fd00:ec2::254/128"), "a cloud metadata address"},
}

// DenyReason says why an address may not be a card target, or returns "" when it may. It is checked
// at entry and again at dial, on the address actually connected.
//
// UNTESTED.
func DenyReason(addr netip.Addr) string {
	addr = addr.Unmap()
	switch {
	case !addr.IsValid():
		return "not an address"
	case addr.IsUnspecified():
		return "the unspecified address"
	case addr.IsMulticast():
		return "a multicast address"
	case addr.IsLinkLocalUnicast():
		return "a link-local address"
	}
	for _, entry := range denyList {
		if entry.prefix.Contains(addr) {
			return entry.reason
		}
	}
	return ""
}
