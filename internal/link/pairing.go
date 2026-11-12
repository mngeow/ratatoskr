// Package link is the runtime's side of the gateway connection: pairing, the saved
// credential, and the request client that talks to the gateway.
//
// It imports internal/gateway only for the address rule, and internal/router only
// for the Link seam it implements. It never sees hook payloads, MCP tokens or
// terminal contents, and it never logs.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No pairing has been saved and
// no request has been made.
package link

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/gateway"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// pairingFileName is the file one runtime's pairing lives in, inside the state
	// directory.
	pairingFileName = "pairing.json"

	// pairTimeout bounds the whole pairing exchange, on top of the caller's
	// context.
	pairTimeout = 10 * time.Second // UNTESTED guess

	stateDirMode    = 0o700
	pairingFileMode = 0o600

	// maxCredentialBytes is a sanity bound, not a format: the gateway mints 43
	// base64url characters.
	maxCredentialBytes = 128

	// pairingTempPrefix names the temp file the credential is written through. It
	// is created 0600 in the same directory, so the secret is never in a wider
	// file and never crosses a filesystem boundary.
	pairingTempPrefix = "pairing.json.tmp-"
)

// ErrNotPaired reports that this machine has no saved pairing.
var ErrNotPaired = errors.New("link: this machine is not paired with a gateway (run: rtk runtime pair --gateway ADDR --code CODE)")

var (
	// hostNamePattern is the host half of a network name.
	hostNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)

	// credentialPattern is the base64url alphabet the gateway mints.
	credentialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Pairing is what one runtime saves about its gateway.
//
// Fingerprint and InsecureHTTP record the transport the user chose. Both are omitted
// when unset, so a pairing written before Direct LAN existed still loads: no flags
// means the Tailscale or loopback plain-HTTP mode, exactly as then.
type Pairing struct {
	Gateway      string `json:"gateway"`
	Host         string `json:"host"`
	Credential   string `json:"credential"`
	Fingerprint  string `json:"fingerprint,omitempty"`
	InsecureHTTP bool   `json:"insecure_http,omitempty"`
}

// scheme is how this pairing talks to its gateway.
//
// A fingerprint always means https and insecure_http always means http: there is no
// fallback between them, and nowhere else decides this.
func (p Pairing) scheme() string {
	if p.Fingerprint != "" {
		return "https"
	}
	return "http"
}

// Target is what the user typed for a gateway: the address and, for a LAN address, the
// transport choice. It is carried by `rtk runtime pair` and saved as a Pairing.
type Target struct {
	Gateway      string
	Fingerprint  string
	InsecureHTTP bool
}

// planTransport decides the runtime's transport (ADR-0009 D17, D19, N3). It no longer depends on the
// address class: a --fingerprint means HTTPS with that pin, and everything else is plain HTTP.
//
// The address is checked first with the shared rule (an IP literal with a port; hostnames are refused).
// --insecure-http is retired: it is accepted and changes nothing, except that together with --fingerprint
// it is a contradiction, which is refused. A pairing saved by an earlier build with the flag stays valid.
//
// UNTESTED.
func planTransport(target Target) (gateway.Transport, error) {
	if _, _, err := gateway.ParseAddress(target.Gateway); err != nil {
		return 0, fmt.Errorf("link: the gateway address is not usable: %w", err)
	}

	choseFingerprint := target.Fingerprint != ""
	switch {
	case choseFingerprint && target.InsecureHTTP:
		return gateway.Transport(0), errors.New(chooseTransportText)
	case choseFingerprint:
		if _, err := gateway.ParseFingerprint(target.Fingerprint); err != nil {
			return gateway.Transport(0), fmt.Errorf("link: the fingerprint is not usable: %w", err)
		}
		return gateway.TransportHTTPS, nil
	}
	return gateway.TransportPlain, nil
}

// plainOnOpenNetwork is true for a plain-HTTP pairing to an address that is not loopback and not Tailscale:
// the case the "plain HTTP" label and the warning are for. It is recorded in Pairing.InsecureHTTP, which no
// longer records a typed flag.
func plainOnOpenNetwork(target Target) bool {
	_, class, err := gateway.ParseAddress(target.Gateway)
	return err == nil && target.Fingerprint == "" && class != gateway.ClassLoopback && class != gateway.ClassTailscale
}

// chooseTransportText is the one wording for a contradictory transport choice.
const chooseTransportText = "link: --fingerprint means HTTPS and --insecure-http (retired) means plain HTTP: give only --fingerprint SHA256, or neither for plain HTTP"

// validatePairing checks a pairing before it is saved, loaded or used.
//
// The gateway address and the transport are validated with the same rules the gateway
// uses for its own listen address and the runtime flag table applies to a typed one. A
// saved endpoint is checked, not only a typed one.
//
// No error text ever contains the credential.
//
// UNTESTED.
func validatePairing(p Pairing) error {
	// The address and the transport are checked by the same table the user's flags
	// go through, so a saved file cannot be a combination the commands would refuse.
	if _, err := planTransport(Target{Gateway: p.Gateway, Fingerprint: p.Fingerprint, InsecureHTTP: p.InsecureHTTP}); err != nil {
		return err
	}
	if !hostNamePattern.MatchString(p.Host) {
		return fmt.Errorf("link: pairing host name %q is not valid", p.Host)
	}
	switch {
	case p.Credential == "":
		return errors.New("link: the pairing credential is empty")
	case len(p.Credential) > maxCredentialBytes:
		return errors.New("link: the pairing credential is implausibly long")
	case !credentialPattern.MatchString(p.Credential):
		return errors.New("link: the pairing credential has characters it should not")
	}
	return nil
}

// prepareStateDir creates or repairs the state directory at 0700.
//
// The state directory may be a symlink to a directory - that is a user setup - so it
// is checked with os.Stat; the pairing file itself is checked with os.Lstat.
//
// UNTESTED.
func prepareStateDir(stateDir string) error {
	info, err := os.Stat(stateDir)
	switch {
	case err == nil && !info.IsDir():
		return fmt.Errorf("link: state directory %s is not a directory", stateDir)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("link: stat state directory: %w", err)
	}

	if err := os.MkdirAll(stateDir, stateDirMode); err != nil {
		return fmt.Errorf("link: create state directory: %w", err)
	}
	if err := os.Chmod(stateDir, stateDirMode); err != nil {
		return fmt.Errorf("link: chmod state directory: %w", err)
	}
	return nil
}

// pairingPath is where one runtime's pairing lives.
func pairingPath(stateDir string) string {
	return filepath.Join(stateDir, pairingFileName)
}

// SavePairing validates and writes one pairing, atomically and at 0600.
//
// The credential only ever exists in a file created 0600 before anything is
// written: the temp file is created with that mode, written, synced, closed,
// chmodded and renamed. Any failure removes the temp file and leaves the previous
// pairing alone.
//
// UNTESTED.
func SavePairing(stateDir string, p Pairing) error {
	if err := validatePairing(p); err != nil {
		return err
	}
	if err := prepareStateDir(stateDir); err != nil {
		return err
	}

	target := pairingPath(stateDir)
	switch info, err := os.Lstat(target); {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("link: %s is a symlink", target)
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("link: %s is not a regular file", target)
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("link: stat pairing file: %w", err)
	}

	payload, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("link: encode pairing: %w", err)
	}

	tempName, err := newPairingTempName()
	if err != nil {
		return err
	}
	tempPath := filepath.Join(stateDir, tempName)

	// O_EXCL plus 0600 from the first moment: the secret is never in a wider file.
	file, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, pairingFileMode)
	if err != nil {
		return fmt.Errorf("link: create pairing temp file: %w", err)
	}

	cleanup := func() {
		_ = os.Remove(tempPath)
	}

	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("link: write pairing: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("link: sync pairing: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return fmt.Errorf("link: close pairing: %w", err)
	}
	if err := os.Chmod(tempPath, pairingFileMode); err != nil {
		cleanup()
		return fmt.Errorf("link: chmod pairing: %w", err)
	}
	if err := os.Rename(tempPath, target); err != nil {
		cleanup()
		return fmt.Errorf("link: install pairing: %w", err)
	}
	return nil
}

// newPairingTempName builds a unique temp name beside the pairing file.
func newPairingTempName() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("link: random temp name: %w", err)
	}
	return pairingTempPrefix + hex.EncodeToString(suffix[:]), nil
}

// LoadPairing reads one saved pairing.
//
// A missing file is ErrNotPaired. A symlink or non-regular file is refused, and so
// is any group or other permission bit: a credential readable by another user is not
// a credential, and the message says how to fix it without printing anything.
//
// UNTESTED.
func LoadPairing(stateDir string) (Pairing, error) {
	path := pairingPath(stateDir)

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Pairing{}, ErrNotPaired
	case err != nil:
		return Pairing{}, fmt.Errorf("link: stat pairing file: %w", err)
	case info.Mode()&os.ModeSymlink != 0:
		return Pairing{}, fmt.Errorf("link: %s is a symlink", path)
	case !info.Mode().IsRegular():
		return Pairing{}, fmt.Errorf("link: %s is not a regular file", path)
	case info.Mode().Perm()&0o077 != 0:
		return Pairing{}, fmt.Errorf("link: pairing file is readable by other users: fix with chmod 600 %s", path)
	}

	payload, err := os.ReadFile(path) //nolint:gosec // the path is the state directory's own file.
	if err != nil {
		return Pairing{}, fmt.Errorf("link: read pairing file: %w", err)
	}

	var pairing Pairing
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pairing); err != nil {
		return Pairing{}, errors.New("link: the pairing file could not be understood")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Pairing{}, errors.New("link: the pairing file has data after the pairing")
	}

	if err := validatePairing(pairing); err != nil {
		return Pairing{}, err
	}
	return pairing, nil
}

// Pair exchanges a one-time code for a credential and does not save it.
//
// RunPair saves; this only talks to the gateway, so a caller can decide what to do
// with a credential without touching the disk.
//
// The transport comes from the Target the user typed, so POST /v1/pair is pinned
// exactly like every later call: the handshake, and with it the pin check, completes
// before a single request byte - including the code - is written.
//
// UNTESTED.
func Pair(ctx context.Context, target Target, code string) (Pairing, error) {
	return pair(ctx, target, code, nil)
}

// pair is Pair with an injectable transport, so tests never touch the network.
func pair(ctx context.Context, target Target, code string, transport http.RoundTripper) (Pairing, error) {
	// planTransport's texts already start with "link:", so they are not wrapped again.
	if _, err := planTransport(target); err != nil {
		return Pairing{}, err
	}

	// Built from the same Target, so the pin cannot be skipped on this one request.
	var pinned *tls.Config
	if target.Fingerprint != "" {
		var err error
		pinned, err = pinnedTLSConfig(target.Fingerprint)
		if err != nil {
			return Pairing{}, fmt.Errorf("link: pair: %w", err)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, pairTimeout)
	defer cancel()

	// The code names its host, so no host is sent - and the struct has no Host field
	// at all, so no empty key appears either.
	payload, err := json.Marshal(struct {
		Code string `json:"code"`
	}{Code: code})
	if err != nil {
		return Pairing{}, fmt.Errorf("link: pair: encode request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, pairingScheme(target.Fingerprint)+"://"+target.Gateway+"/v1/pair", bytes.NewReader(payload))
	if err != nil {
		return Pairing{}, errors.New("link: pair: build request failed")
	}
	request.Header.Set(protocol.WireVersionHeader, strconv.Itoa(protocol.WireVersion))
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Transport: pairTransport(transport, Options{}, pinned), CheckRedirect: noRedirect}
	response, err := client.Do(request)
	if err != nil {
		return Pairing{}, fmt.Errorf("link: could not pair (%s)", causeKind(err))
	}
	defer func() {
		_ = response.Body.Close()
	}()

	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return Pairing{}, errors.New("link: pair: the gateway's answer could not be read")
	}

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		var paired protocol.PairResponse
		if err := json.Unmarshal(body, &paired); err != nil {
			return Pairing{}, errors.New("link: pair: the gateway's answer could not be understood")
		}

		result := Pairing{
			Gateway:      target.Gateway,
			Host:         paired.Host,
			Credential:   paired.Credential,
			Fingerprint:  target.Fingerprint,
			InsecureHTTP: plainOnOpenNetwork(target),
		}
		// A hostile or broken gateway must not be able to make us save junk.
		if err := validatePairing(result); err != nil {
			return Pairing{}, fmt.Errorf("link: pair: the gateway's answer was not usable: %w", err)
		}
		return result, nil

	case response.StatusCode == http.StatusForbidden:
		// Never echo the code itself.
		return Pairing{}, errors.New("link: that pairing code was not accepted")

	case response.StatusCode == http.StatusBadRequest:
		var wire protocol.WireError
		if err := json.Unmarshal(body, &wire); err == nil && wire.Code == protocol.WireErrVersion {
			return Pairing{}, fmt.Errorf("link: the gateway refused this runtime: %s", sanitizeText(wire.Message))
		}
		return Pairing{}, fmt.Errorf("link: could not pair (the gateway answered with status %d)", response.StatusCode)

	default:
		return Pairing{}, fmt.Errorf("link: could not pair (the gateway answered with status %d)", response.StatusCode)
	}
}

// RunPair pairs, saves, and prints what it did without ever printing the secret.
//
// The plain-HTTP warning is printed before the exchange, so a user who is about to send
// a credential over an unencrypted LAN sees it even if the pairing then fails. For
// HTTPS the pinned fingerprint is printed afterwards, which is what a user compares
// with the gateway machine.
//
// UNTESTED.
func RunPair(ctx context.Context, stateDir string, target Target, code string, out io.Writer) error {
	return runPair(ctx, stateDir, target, code, out, nil)
}

// runPair is RunPair with an injectable transport, so the printing can be tested
// without a network.
func runPair(ctx context.Context, stateDir string, target Target, code string, out io.Writer, transport http.RoundTripper) error {
	// Same writer, before the exchange: the warning must not depend on success.
	if plainOnOpenNetwork(target) {
		if _, err := fmt.Fprintln(out, gateway.PlainHTTPWarning); err != nil {
			return fmt.Errorf("link: write pairing warning: %w", err)
		}
	}

	pairing, err := pair(ctx, target, code, transport)
	if err != nil {
		return err
	}
	if err := SavePairing(stateDir, pairing); err != nil {
		return err
	}

	if _, err := fmt.Fprintf(out, "paired as %s with gateway %s\n", pairing.Host, target.Gateway); err != nil {
		return fmt.Errorf("link: write pairing result: %w", err)
	}
	if _, err := fmt.Fprintf(out, "credential saved in %s (mode 600)\n", pairingPath(stateDir)); err != nil {
		return fmt.Errorf("link: write pairing result: %w", err)
	}
	if target.Fingerprint != "" {
		if _, err := fmt.Fprintf(out, "transport: HTTPS, certificate pinned to %s\n", target.Fingerprint); err != nil {
			return fmt.Errorf("link: write pairing result: %w", err)
		}
	}
	return nil
}

// pairingScheme is the URL scheme for a fingerprint: https when there is one, http
// otherwise. It is the same rule Pairing.scheme applies to a saved pairing, kept here
// so a Target and a Pairing cannot disagree before the pairing exists.
func pairingScheme(fingerprint string) string {
	if fingerprint != "" {
		return "https"
	}
	return "http"
}

// sanitizeText makes a refusal readable: control characters become spaces,
// whitespace collapses, and the result is cut short.
func sanitizeText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))

	space := false
	count := 0
	for _, r := range value {
		if r == 0x7f || r < 0x20 || (r >= 0x80 && r <= 0x9f) {
			r = ' '
		}
		if r == ' ' {
			if builder.Len() == 0 || space {
				continue
			}
			space = true
		} else {
			space = false
		}
		if count == 200 {
			break
		}
		builder.WriteRune(r)
		count++
	}
	return strings.TrimSpace(builder.String())
}
