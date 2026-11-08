package gateway

// The participant listener: its own address, flag table, transport, endpoint file and http.Server. The
// participant API (docs/implementation/participant-api-contract.md, section 2) cannot share the host
// listener, whose transport is fixed by the address class (plain HTTP on a tailnet), because the participant
// API is HTTPS there by default.
//
// This file has no participant route yet; ParticipantHandler answers 404 to everything until P2.
//
// UNTESTED. Under GR-3 nothing here has been run: no address parsed, no socket bound.

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Flag spellings of the participant listener, shared so a message and the table cannot drift apart.
const (
	FlagParticipantListen       = "--participant-listen"
	FlagParticipantAllowLAN     = "--participant-allow-lan"
	FlagParticipantInsecureHTTP = "--participant-insecure-http"

	participantSubject      = "participant listener"
	participantEndpointName = "participant-endpoint.json"

	// ParticipantPlainHTTPWarning is printed once, at start, for a participant listener that serves plain
	// HTTP on a tailnet or LAN address. The docs say the same.
	ParticipantPlainHTTPWarning = "WARNING: the participant API is served over plain HTTP. The bearer credential, the pairing code and message bodies are readable by anything on the network path, and messages can be injected."
)

// ParticipantOptions are the three participant flags of `rtk gateway --listen`. The zero value means the
// participant API is off, and then the gateway's startup path, output and files are exactly as before.
type ParticipantOptions struct {
	Listen       string // --participant-listen
	AllowLAN     bool   // --participant-allow-lan
	InsecureHTTP bool   // --participant-insecure-http
}

// PlanParticipantListen applies the participant flag table of contract section 2. It returns nil when the
// participant API is off. It touches no file, lock or socket.
//
// The table is its own and is not planListenAs: that table's non-LAN branch forbids both flags and returns
// plain HTTP, the opposite of the participant default on a tailnet.
//
//	no --participant-listen          both other flags refused
//	loopback                         both flags refused; plain HTTP
//	Tailscale                        --participant-allow-lan refused; HTTPS, or plain with --participant-insecure-http
//	LAN                              --participant-allow-lan required whatever else is set;
//	                                 HTTPS, or plain with --participant-insecure-http
//
// UNTESTED.
func PlanParticipantListen(options ParticipantOptions) (*ListenPlan, error) {
	if options.Listen == "" {
		if options.AllowLAN || options.InsecureHTTP {
			return nil, fmt.Errorf("gateway: %s and %s apply only with %s ADDR",
				FlagParticipantAllowLAN, FlagParticipantInsecureHTTP, FlagParticipantListen)
		}
		return nil, nil
	}

	addrPort, class, err := parseAddressAs(participantSubject, options.Listen)
	if err != nil {
		return nil, err
	}

	switch class {
	case ClassLoopback:
		if options.AllowLAN || options.InsecureHTTP {
			return nil, fmt.Errorf(
				"%s: %s and %s do not apply to a loopback address, which is plain HTTP because the traffic never leaves this machine: drop the flag",
				participantSubject, FlagParticipantAllowLAN, FlagParticipantInsecureHTTP)
		}
		return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportPlain}, nil

	case ClassTailscale:
		if options.AllowLAN {
			return nil, fmt.Errorf(
				"%s: %s applies only to a LAN address (10.x, 172.16-31.x, 192.168.x or fc00::/7), and %s is %s: drop the flag, or listen on a LAN address",
				participantSubject, FlagParticipantAllowLAN, options.Listen, class)
		}
		if options.InsecureHTTP {
			return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportPlain}, nil
		}
		return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportHTTPS}, nil
	}

	// LAN.
	switch {
	case !options.AllowLAN:
		return nil, fmt.Errorf(
			"%s: %s is a LAN address: add %s to listen on it (HTTPS with a pinned certificate; add %s for plain HTTP), or use a Tailscale or loopback address",
			participantSubject, options.Listen, FlagParticipantAllowLAN, FlagParticipantInsecureHTTP)
	case options.InsecureHTTP:
		return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportLANHTTP}, nil
	}
	return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportHTTPS}, nil
}

// planBoth validates the host plan and the participant plan together, before any file, lock or socket: both
// must be valid, and they must not name the same bind endpoint. The participant plan is nil when the
// participant API is off.
func planBoth(listen string, allowLAN, insecureHTTP bool, participant ParticipantOptions) (host ListenPlan, part *ListenPlan, err error) {
	host, err = PlanListen(listen, allowLAN, insecureHTTP)
	if err != nil {
		return ListenPlan{}, nil, err
	}
	part, err = PlanParticipantListen(participant)
	if err != nil {
		return ListenPlan{}, nil, err
	}
	if part != nil && part.Addr == host.Addr {
		return ListenPlan{}, nil, fmt.Errorf(
			"gateway: %s and %s name the same address %s: the host protocol and the participant API need different ports",
			"--listen", FlagParticipantListen, host.Addr)
	}
	return host, part, nil
}

// SPKIPin is the SubjectPublicKeyInfo pin of a certificate in the form `sha256//BASE64`, which is what
// clients that pin a public key (such as `curl --pinnedpubkey`) check. It is computed from the existing
// identity: no new key. It is a different trust rule from the certificate's SHA-256 (Fingerprint), which
// stays the normative pin.
//
// UNTESTED.
func SPKIPin(der []byte) (string, error) {
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", fmt.Errorf("gateway: parse certificate for the key pin: %w", err)
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return "sha256//" + base64.StdEncoding.EncodeToString(sum[:]), nil
}

// ParticipantEndpoint is what a gateway wrote down about its participant listener, in its own file so the
// host endpoint file stays byte for byte as it was. Like that file it is advice for `rtk gateway
// participant pair`, never a requirement.
type ParticipantEndpoint struct {
	Listen      string `json:"listen"`
	Scheme      string `json:"scheme"`
	Class       string `json:"class"` // loopback, tailscale or lan
	Fingerprint string `json:"fingerprint,omitempty"`
	SPKIPin     string `json:"spki_pin,omitempty"`
}

func classWord(class AddrClass) string {
	switch class {
	case ClassLoopback:
		return "loopback"
	case ClassTailscale:
		return "tailscale"
	}
	return "lan"
}

// WriteParticipantEndpoint records a participant listener that is already bound. fingerprint and spkiPin
// are required exactly when the transport is HTTPS.
//
// UNTESTED.
func WriteParticipantEndpoint(dir string, plan ListenPlan, fingerprint, spkiPin string) error {
	endpoint := ParticipantEndpoint{
		Listen: plan.Addr.String(), Scheme: schemeHTTP, Class: classWord(plan.Class),
	}
	if plan.Transport == TransportHTTPS {
		endpoint.Scheme, endpoint.Fingerprint, endpoint.SPKIPin = schemeHTTPS, fingerprint, spkiPin
	}
	if err := validateParticipantEndpoint(endpoint); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(endpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("gateway: encode participant endpoint: %w", err)
	}
	return writePrivateFile(filepath.Join(dir, participantEndpointName), append(payload, '\n'))
}

// RemoveStaleParticipantEndpoint is the API-off housekeeping: a gateway started without the participant API
// deletes a participant-endpoint.json that an earlier run left, so the owner commands do not print an
// address nothing serves. It removes the entry ONLY if Lstat says it is a regular file. A directory, a
// symlink or anything else is left alone, and so is a regular file that cannot be removed; each is reported
// in the returned warning, which is empty when there is nothing to say. It never fails startup, and it
// never touches the host endpoint file.
//
// UNTESTED.
func RemoveStaleParticipantEndpoint(dir string) (warning string) {
	path := filepath.Join(dir, participantEndpointName)
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return ""
	case err != nil:
		return fmt.Sprintf("warning: could not inspect %s, so it was left alone: %v", path, err)
	case !info.Mode().IsRegular():
		return fmt.Sprintf("warning: %s is not a regular file (it is a %s), so it was left alone: remove it yourself if it is stale", path, describeMode(info.Mode()))
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Sprintf("warning: could not remove the stale %s: %v", path, err)
	}
	return ""
}

// describeMode names a non-regular file kind for a warning.
func describeMode(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return "symlink"
	case mode.IsDir():
		return "directory"
	}
	return "special file"
}

// LoadParticipantEndpoint reads the file under the same safety rules as LoadEndpoint: false for every
// problem.
//
// UNTESTED.
func LoadParticipantEndpoint(dir string) (ParticipantEndpoint, bool) {
	path := filepath.Join(dir, participantEndpointName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > identityFileLimit {
		return ParticipantEndpoint{}, false
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return ParticipantEndpoint{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var endpoint ParticipantEndpoint
	if err := decoder.Decode(&endpoint); err != nil {
		return ParticipantEndpoint{}, false
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ParticipantEndpoint{}, false
	}
	if validateParticipantEndpoint(endpoint) != nil {
		return ParticipantEndpoint{}, false
	}
	return endpoint, true
}

// validateParticipantEndpoint applies one set of rules to a file being written and one being read.
func validateParticipantEndpoint(endpoint ParticipantEndpoint) error {
	addrPort, class, err := parseAddressAs(participantSubject, endpoint.Listen)
	if err != nil {
		return err
	}
	if endpoint.Listen != addrPort.String() {
		return fmt.Errorf("gateway: %q is not the canonical form %q", endpoint.Listen, addrPort.String())
	}
	if endpoint.Class != classWord(class) {
		return fmt.Errorf("gateway: the class field says %q but %s is a %s address", endpoint.Class, endpoint.Listen, class)
	}
	switch endpoint.Scheme {
	case schemeHTTP:
		if endpoint.Fingerprint != "" || endpoint.SPKIPin != "" {
			return errors.New("gateway: a plain HTTP participant endpoint must not carry a pin")
		}
	case schemeHTTPS:
		if class == ClassLoopback {
			return errors.New("gateway: a loopback participant endpoint is plain HTTP")
		}
		if _, err := ParseFingerprint(endpoint.Fingerprint); err != nil {
			return err
		}
		if raw, ok := strings.CutPrefix(endpoint.SPKIPin, "sha256//"); !ok {
			return errors.New("gateway: a key pin looks like sha256//BASE64")
		} else if decoded, err := base64.StdEncoding.DecodeString(raw); err != nil || len(decoded) != sha256.Size {
			return errors.New("gateway: a key pin looks like sha256//BASE64 of a SHA-256")
		}
	default:
		return fmt.Errorf("gateway: scheme %q is neither %s nor %s", endpoint.Scheme, schemeHTTP, schemeHTTPS)
	}
	return nil
}

// participantStatusLines is what the gateway prints about the participant listener, after the host lines:
// the listening line, one transport line (for plain HTTP, the warning), and the firewall hint on a LAN.
// The fingerprint is public; nothing secret appears.
func participantStatusLines(plan ListenPlan, addr, fingerprint, spkiPin string) []string {
	lines := []string{fmt.Sprintf("participant API listening on %s", addr)}
	switch {
	case plan.Transport == TransportHTTPS:
		lines = append(lines,
			"participant transport: HTTPS, self-signed certificate, fingerprint "+fingerprint,
			"participant transport: key pin (for clients that can pin a public key) "+spkiPin)
	case plan.Class == ClassLoopback:
		lines = append(lines, "participant transport: plain HTTP; loopback only")
	default:
		lines = append(lines, ParticipantPlainHTTPWarning)
	}
	if plan.Class == ClassLAN {
		lines = append(lines, fmt.Sprintf(
			"participant API is reachable by every device on this LAN; if an adapter cannot reach this address, check that this machine's firewall allows incoming TCP on port %d",
			plan.Addr.Port()))
	}
	return lines
}

// withContractHeader names the contract minor on every participant response.
func withContractHeader(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(protocol.ParticipantContractHeader, protocol.ParticipantContract)
		next.ServeHTTP(w, r)
	})
}
