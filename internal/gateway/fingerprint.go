package gateway

// The certificate fingerprint format and the shared plain-HTTP warning. Both sides
// use them: the gateway prints a fingerprint, and a runtime pins it.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	fingerprintPrefix = "sha256:"

	// PlainHTTPWarning is printed by `rtk gateway` and `rtk runtime pair` when a LAN
	// run is plain HTTP. One constant, so the two cannot drift.
	PlainHTTPWarning = "WARNING: plain HTTP on the LAN. Anyone who can see or redirect this network traffic can read messages, the pairing code and the credential, and can inject messages into your agents."
)

// Fingerprint is "sha256:" plus the lowercase hex SHA-256 of a certificate's DER
// bytes.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// ParseFingerprint decodes a fingerprint exactly as Fingerprint writes it. Case,
// spacing and a missing prefix are all refused: a pin is typed or pasted once, and a
// near-match must not be quietly repaired.
func ParseFingerprint(value string) ([sha256.Size]byte, error) {
	var out [sha256.Size]byte
	if !strings.HasPrefix(value, fingerprintPrefix) {
		return out, errors.New("gateway: a fingerprint looks like sha256:<64 lowercase hex digits>")
	}
	digits := value[len(fingerprintPrefix):]
	if len(digits) != 2*sha256.Size || digits != strings.ToLower(digits) {
		return out, errors.New("gateway: a fingerprint looks like sha256:<64 lowercase hex digits>")
	}
	decoded, err := hex.DecodeString(digits)
	if err != nil || len(decoded) != sha256.Size {
		return out, errors.New("gateway: a fingerprint looks like sha256:<64 lowercase hex digits>")
	}
	copy(out[:], decoded)
	return out, nil
}
