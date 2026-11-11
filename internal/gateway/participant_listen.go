package gateway

// The participant listener: its own address, flag table, transport, endpoint file and http.Server. The
// participant API (docs/implementation/participant-api-contract.md, section 2) cannot share the host
// listener: its transport is HTTPS by default (D18) while the host listener's is plain by default (D17).
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
	"net/netip"
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

// DefaultParticipantPort is the port of the participant listener when --participant-listen is not given.
const DefaultParticipantPort = 7790

// ParticipantListenOff is the --participant-listen value that turns the participant API off, like the
// daemon's --card-listen off.
const ParticipantListenOff = "off"

// ParticipantOptions are the participant flags of `rtk gateway`. Listen "" means the default address (the
// host --listen IP, port DefaultParticipantPort: 0.0.0.0:7790 with the default host address, so narrowing
// --listen narrows the participant API with it); ParticipantListenOff means the participant API is off, and
// then the gateway's startup path, output and files are exactly as before it existed. The zero value is
// therefore the DEFAULT, not off: a caller that wants the old behaviour says ParticipantListenOff.
type ParticipantOptions struct {
	Listen       string // --participant-listen: "", "off" or ADDR
	AllowLAN     bool   // --participant-allow-lan: RETIRED (N4), accepted and ignored; cmd/rtk warns
	InsecureHTTP bool   // --participant-insecure-http: the plain-HTTP opt-in (D18), kept
}

// RetiredParticipantAllowLANWarning is printed once on stderr when --participant-allow-lan is given (N4).
const RetiredParticipantAllowLANWarning = "warning: rtk gateway --participant-allow-lan is retired and has no effect (accepted for one release): any participant address may be named now."

// ResolveParticipantListen turns the host plan and the participant flags into the participant plan. It
// returns nil when the API is off. defaulted is true when the address was not typed. It touches no file,
// lock or socket.
//
// UNTESTED.
func ResolveParticipantListen(host ListenPlan, options ParticipantOptions) (plan *ListenPlan, defaulted bool, err error) {
	if options.Listen != "" {
		plan, err = PlanParticipantListen(options)
		return plan, false, err
	}
	options.Listen = netip.AddrPortFrom(host.Addr.Addr(), DefaultParticipantPort).String()
	plan, err = PlanParticipantListen(options)
	if err != nil {
		return nil, true, fmt.Errorf("%w (the participant listener defaulted to %s; use %s IP:PORT to choose another address, or %s %s to turn it off)",
			err, options.Listen, FlagParticipantListen, FlagParticipantListen, ParticipantListenOff)
	}
	return plan, true, nil
}

// PlanParticipantListen applies the participant flag table to an explicit address, or to
// ParticipantListenOff. It returns nil when the participant API is off. It touches no file, lock or socket.
//
// The transport no longer follows the address class (D18):
//
//	off                              --participant-insecure-http refused
//	loopback                         plain HTTP (the traffic never leaves this machine); --participant-insecure-http refused
//	any other address                HTTPS, or plain with --participant-insecure-http
//
// --participant-allow-lan is retired (N4) and ignored here.
//
// UNTESTED.
func PlanParticipantListen(options ParticipantOptions) (*ListenPlan, error) {
	if options.Listen == ParticipantListenOff {
		if options.InsecureHTTP {
			return nil, fmt.Errorf("gateway: %s does not apply with %s %s",
				FlagParticipantInsecureHTTP, FlagParticipantListen, ParticipantListenOff)
		}
		return nil, nil
	}

	addrPort, class, err := parseAddressAs(participantSubject, options.Listen)
	if err != nil {
		return nil, err
	}
	if class == ClassLoopback {
		if options.InsecureHTTP {
			return nil, fmt.Errorf(
				"%s: %s does not apply to a loopback address, which is plain HTTP because the traffic never leaves this machine: drop the flag",
				participantSubject, FlagParticipantInsecureHTTP)
		}
		return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportPlain}, nil
	}
	if options.InsecureHTTP {
		return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportPlain}, nil
	}
	return &ListenPlan{Addr: addrPort, Class: class, Transport: TransportHTTPS}, nil
}

// planBoth validates the host plan and the participant plan together, before any file, lock or socket: both
// must be valid, and they must not name the same bind endpoint. The participant plan is nil when the
// participant API is off; defaulted says its address was not typed.
func planBoth(listen string, https bool, participant ParticipantOptions) (host ListenPlan, part *ListenPlan, defaulted bool, err error) {
	host, err = PlanListen(listen, https)
	if err != nil {
		return ListenPlan{}, nil, false, err
	}
	part, defaulted, err = ResolveParticipantListen(host, participant)
	if err != nil {
		return ListenPlan{}, nil, false, err
	}
	if part != nil && part.Addr == host.Addr {
		if defaulted {
			return ListenPlan{}, nil, false, fmt.Errorf(
				"gateway: --listen uses %s, which is the default participant address: use %s IP:PORT to put the participant API elsewhere, or %s %s to turn it off",
				host.Addr, FlagParticipantListen, FlagParticipantListen, ParticipantListenOff)
		}
		return ListenPlan{}, nil, false, fmt.Errorf(
			"gateway: %s and %s name the same address %s: the host protocol and the participant API need different ports",
			"--listen", FlagParticipantListen, host.Addr)
	}
	return host, part, defaulted, nil
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
	Class       string `json:"class"` // loopback, tailscale, lan, wildcard or public
	Fingerprint string `json:"fingerprint,omitempty"`
	SPKIPin     string `json:"spki_pin,omitempty"`
}

func classWord(class AddrClass) string {
	switch class {
	case ClassLoopback:
		return "loopback"
	case ClassTailscale:
		return "tailscale"
	case ClassWildcard:
		return "wildcard"
	case ClassPublic:
		return "public"
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

// participantStatusLines is what the gateway prints about the participant listener, after the host lines: the
// listening line, one transport line (for plain HTTP, the warning) and the exposure note with the actual
// port. The fingerprint is public; nothing secret appears.
func participantStatusLines(plan ListenPlan, addr string, defaulted bool, fingerprint, spkiPin string) []string {
	lines := []string{fmt.Sprintf("participant API listening on %s", addr)}
	if defaulted {
		lines[0] += fmt.Sprintf(" (default; %s to change, %s %s to disable)", FlagParticipantListen, FlagParticipantListen, ParticipantListenOff)
	}
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
	if plan.Class != ClassLoopback {
		lines = append(lines, fmt.Sprintf(
			"participant API: reachable by whoever can route to %s; your firewall is the boundary (restrict inbound TCP on port %d to the machines that run adapters)",
			addr, plan.Addr.Port()))
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
