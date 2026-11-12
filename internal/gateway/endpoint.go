package gateway

// endpoint.json: what a running gateway bound, so `rtk gateway pair` can print a ready
// command instead of a placeholder. The file is advice and never a requirement: any
// problem reading it falls back to the placeholder line.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	endpointName = "endpoint.json"

	schemeHTTP  = "http"
	schemeHTTPS = "https"
)

// Endpoint is what a gateway wrote down about its own listener.
type Endpoint struct {
	Listen      string `json:"listen"`
	Scheme      string `json:"scheme"`
	Fingerprint string `json:"fingerprint,omitempty"`
	LAN         bool   `json:"lan,omitempty"`
}

// WriteEndpoint records the listener of a gateway that is already serving.
//
// dir is the gateway directory, so the file is <dir>/endpoint.json at 0600, written
// through a temp file and renamed.
//
// UNTESTED.
func WriteEndpoint(dir string, plan ListenPlan, fingerprint string) error {
	endpoint := Endpoint{
		Listen:      plan.Addr.String(),
		Scheme:      schemeHTTP,
		Fingerprint: fingerprint,
		LAN:         plan.Class == ClassLAN,
	}
	if plan.Transport == TransportHTTPS {
		endpoint.Scheme = schemeHTTPS
	}

	// Written only when it is valid: a reader must never see a half-true file.
	if err := validateEndpoint(endpoint); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(endpoint, "", "  ")
	if err != nil {
		return fmt.Errorf("gateway: encode endpoint: %w", err)
	}
	payload = append(payload, '\n')

	path := filepath.Join(dir, endpointName)
	if err := writePrivateFile(path, payload); err != nil {
		return err
	}
	return nil
}

// LoadEndpoint reads a gateway's endpoint.
//
// It reports false for every problem - missing, a symlink, a non-regular file, a mode
// wider than 0600, unknown fields, trailing data, or values that do not agree with each
// other - because a caller has a better answer than an error: the placeholder line.
//
// UNTESTED.
func LoadEndpoint(dir string) (Endpoint, bool) {
	endpoint, err := loadEndpoint(dir)
	if err != nil {
		return Endpoint{}, false
	}
	return endpoint, true
}

// loadEndpoint is LoadEndpoint with the reason kept, for tests and for a caller that
// wants to log it one day. Its errors never quote the file.
func loadEndpoint(dir string) (Endpoint, error) {
	path := filepath.Join(dir, endpointName)

	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return Endpoint{}, fmt.Errorf("gateway: %s does not exist", path)
	case err != nil:
		return Endpoint{}, fmt.Errorf("gateway: stat %s: %w", path, err)
	case info.Mode()&os.ModeSymlink != 0:
		return Endpoint{}, fmt.Errorf("gateway: %s must be a regular file, not a symlink", path)
	case !info.Mode().IsRegular():
		return Endpoint{}, fmt.Errorf("gateway: %s must be a regular file", path)
	case info.Mode().Perm()&0o077 != 0:
		return Endpoint{}, fmt.Errorf("gateway: %s is readable by group or other (mode %04o)", path, info.Mode().Perm())
	case info.Size() > identityFileLimit:
		return Endpoint{}, fmt.Errorf("gateway: %s is too large to be an endpoint", path)
	}

	payload, err := os.ReadFile(path)
	if err != nil {
		return Endpoint{}, fmt.Errorf("gateway: read %s: %w", path, err)
	}

	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var endpoint Endpoint
	if err := decoder.Decode(&endpoint); err != nil {
		// The decoder's message names a field or an offset, never the values.
		return Endpoint{}, fmt.Errorf("gateway: %s is not a valid endpoint: %w", path, err)
	}
	// Trailing data after the object is a defect, not something to ignore.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Endpoint{}, fmt.Errorf("gateway: %s has trailing data", path)
	}

	if err := validateEndpoint(endpoint); err != nil {
		return Endpoint{}, err
	}
	return endpoint, nil
}

// validateEndpoint applies the same rules to a file being written and one being read,
// so the writer cannot record something its reader would refuse.
//
// UNTESTED.
func validateEndpoint(endpoint Endpoint) error {
	addrPort, class, err := ParseAddress(endpoint.Listen)
	if err != nil {
		return err
	}
	if endpoint.Listen != addrPort.String() {
		// Refuse a spelling ParseAddress would normalise: the file records what was
		// bound, not a near miss.
		return fmt.Errorf("gateway: %q is not the canonical form %q", endpoint.Listen, addrPort.String())
	}

	switch endpoint.Scheme {
	case schemeHTTP:
		if endpoint.Fingerprint != "" {
			return errors.New("gateway: a plain HTTP endpoint must not carry a fingerprint")
		}
	case schemeHTTPS:
		if _, err := ParseFingerprint(endpoint.Fingerprint); err != nil {
			return err
		}
		// HTTPS (the --https opt-in) is allowed on any address since ADR-0009.
	default:
		return fmt.Errorf("gateway: scheme %q is neither %s nor %s", endpoint.Scheme, schemeHTTP, schemeHTTPS)
	}

	// The lan field records the address class, so it cannot disagree with it.
	if isLAN := class == ClassLAN; endpoint.LAN != isLAN {
		return fmt.Errorf("gateway: the lan field says %t but %s is a %s address", endpoint.LAN, endpoint.Listen, class)
	}
	return nil
}
