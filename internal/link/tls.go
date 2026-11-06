package link

// The runtime's pinned TLS: one self-signed certificate per gateway, compared by the
// SHA-256 of its DER bytes on every connection. There is no CA, no system roots, no
// hostname check, and no way to turn the pin off.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No handshake has been made.

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/mngeow/ratatoskr/internal/gateway"
)

// errPinMismatch is returned when the gateway's leaf certificate is not the pinned one.
//
// It never carries certificate, address or fingerprint text: the message a user reads is
// built by causeKind, and it says what to do rather than what was seen.
var errPinMismatch = errors.New("link: the gateway's certificate does not match the pinned fingerprint")

// pinnedTLSConfig builds the client configuration for a pinned fingerprint.
//
// An empty or malformed fingerprint returns an error and no config, so a missing pin
// fails closed rather than falling back to a default that would trust anything.
//
// UNTESTED.
func pinnedTLSConfig(fingerprint string) (*tls.Config, error) {
	pinned, err := gateway.ParseFingerprint(fingerprint)
	if err != nil {
		return nil, fmt.Errorf("link: the pinned fingerprint is not usable: %w", err)
	}

	var none [sha256.Size]byte
	if pinned == none {
		// ParseFingerprint accepts 64 zero digits; a zero pin must still never match,
		// and refusing it here keeps that rule in one place.
		return nil, errors.New("link: the pinned fingerprint is all zeroes")
	}

	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		// HTTP/1.1 only, so the stream behaves exactly as it does on the tailnet.
		NextProtos: []string{"http/1.1"},
		// InsecureSkipVerify turns off only the system-roots check, which this design
		// replaces: the pin checked in VerifyConnection below is the trust, and no
		// option, flag or environment variable can turn it off.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errPinMismatch
			}
			sum := sha256.Sum256(state.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], pinned[:]) != 1 {
				return errPinMismatch
			}
			return nil
		},
	}, nil
}
