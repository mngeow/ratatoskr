package gateway

// The HTTP client that polls one host's card endpoint, built for one target.
//
// Everything about it is a refusal: no proxy (ProxyFromEnvironment would send card polls through
// HTTP_PROXY), no redirects, no keep-alive, no HTTP/2, no compression, no cookies, no credentials of
// any kind (the host credential is never sent), bounded connect, header and total time, and a TLS
// configuration that exists only for this target's pin. The destination is an IP literal, so no name is
// resolved; the address actually connected is checked again, at dial, against the deny list, the class
// the target was registered as and the registered address itself.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No connection has been made.

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

const (
	cardConnectTimeout = 3 * time.Second // UNTESTED guess; contract default
	cardTotalTimeout   = 5 * time.Second // UNTESTED guess; contract default
	cardHeaderBytes    = 16 << 10        // UNTESTED guess
	cardUserAgent      = "ratatoskr-gateway"
)

// newCardClient builds the client for one target. It is built per poll round and holds nothing
// between polls: there is no shared TLS configuration for two targets to race on.
//
// UNTESTED.
func newCardClient(target CardTarget) (*http.Client, error) {
	dialer := &net.Dialer{
		Timeout: cardConnectTimeout,
		Control: cardDialControl(target),
	}

	transport := &http.Transport{
		// nil is the point: the environment's proxy settings are never consulted.
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		DisableKeepAlives:      true,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		TLSHandshakeTimeout:    cardConnectTimeout,
		ResponseHeaderTimeout:  cardTotalTimeout,
		MaxResponseHeaderBytes: cardHeaderBytes,
		// A non-nil empty map keeps HTTP/2 off.
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}

	if target.Scheme == "https" {
		config, err := cardPinnedTLS(target.Fingerprint)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = config
	}

	return &http.Client{
		Transport: transport,
		Timeout:   cardTotalTimeout,
		// A 3xx is a failed poll and is never followed: the response itself is returned.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// cardDialControl is called by the dialer with the address it is about to connect to, after any
// resolution. For an IP-literal target that address is the target's own, so the check is equality; it
// also re-applies the deny list and the class, so a bug elsewhere cannot widen what is dialled.
func cardDialControl(target CardTarget) func(network, address string, _ syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		dialled, err := netip.ParseAddrPort(address)
		if err != nil {
			return fmt.Errorf("%w", ErrAddressRefused)
		}
		addr := dialled.Addr().Unmap()
		if DenyReason(addr) != "" {
			return fmt.Errorf("%w", ErrAddressRefused)
		}
		if class, ok := classOf(addr); !ok || class != target.Class {
			return fmt.Errorf("%w", ErrAddressRefused)
		}
		if netip.AddrPortFrom(addr, dialled.Port()) != target.Addr {
			return fmt.Errorf("%w", ErrAddressRefused)
		}
		return nil
	}
}

// cardPinnedTLS is the client configuration for one pinned certificate: no CA, no system roots, no
// name check, TLS 1.3, and the leaf's SHA-256 compared in constant time. An empty, malformed or
// all-zero pin returns an error and no configuration, so a missing pin fails closed.
//
// UNTESTED.
func cardPinnedTLS(fingerprint string) (*tls.Config, error) {
	pinned, err := ParseFingerprint(fingerprint)
	if err != nil {
		return nil, fmt.Errorf("gateway: the card target's fingerprint is not usable: %w", err)
	}
	var none [sha256.Size]byte
	if pinned == none {
		return nil, errors.New("gateway: the card target's fingerprint is all zeroes")
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{"http/1.1"},
		// InsecureSkipVerify turns off only the system-roots check this design replaces; the pin in
		// VerifyConnection is the trust, and nothing can turn it off.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return ErrPinMismatch
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], pinned[:]) != 1 {
				return ErrPinMismatch
			}
			return nil
		},
	}, nil
}
