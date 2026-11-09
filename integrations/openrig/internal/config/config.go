// Package config loads and validates the owner-written ratatoskr-openrig configuration file and names the
// files of the state directory (docs/implementation/openrig-bridge-contract.md sections 5 and 7).
//
// The file holds no secret. Every refusal names the FIELD, never a value beyond what is needed.
//
// UNTESTED. Under GR-3 nothing here has been run.
package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

const (
	// Version is the only config_version this build reads.
	Version = 1

	// MaxFileBytes bounds the configuration file.
	MaxFileBytes = 64 * 1024

	// maxSocketPath is the longest Unix socket path macOS accepts (103 bytes plus the terminating NUL).
	maxSocketPath = 103

	// File names inside the state directory.
	socketName     = "bridge.sock"
	lockName       = "serve.lock"
	journalName    = "journal.jsonl"
	credentialName = "credential"
	stoppedName    = "stopped.json"

	defaultInlineBodyBytes    = 4096
	defaultPerSenderPerMinute = 6  // UNTESTED guess
	defaultTotalPerMinute     = 20 // UNTESTED guess
	defaultReadyCheckMS       = 1500

	// MaxInlineBodyBytes is the participant API's own body limit.
	MaxInlineBodyBytes = 65536
)

var (
	participantPattern = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)
	sessionPattern     = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
	seatPattern        = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9._-]+$`)
)

// Limits are the bridge's own inbound limits. Zero means the default. All numbers are UNTESTED guesses.
type Limits struct {
	InlineBodyBytes    int `json:"inline_body_bytes"`
	PerSenderPerMinute int `json:"per_sender_per_minute"`
	TotalPerMinute     int `json:"total_per_minute"`
}

// Config is the configuration file.
type Config struct {
	ConfigVersion       int    `json:"config_version"`
	ParticipantURL      string `json:"participant_url"`
	CertificateSHA256   string `json:"certificate_sha256"`
	Participant         string `json:"participant"`
	Session             string `json:"session"`
	OperatorSeat        string `json:"operator_seat"`
	RigPath             string `json:"rig_path"`
	RigEnvPath          string `json:"rig_env_path"`
	OpenRigHome         string `json:"openrig_home"`
	OpenRigURL          string `json:"openrig_url,omitempty"`
	StateDir            string `json:"state_dir,omitempty"`
	CredentialPath      string `json:"credential_path,omitempty"`
	Limits              Limits `json:"limits"`
	ReadyCheckTimeoutMS int    `json:"ready_check_timeout_ms,omitempty"`

	// Derived by Load; never read from the file.
	Home string `json:"-"`
	// Scheme is "http" or "https"; Host is "host:port".
	Scheme string `json:"-"`
	Host   string `json:"-"`
	// Pin is the certificate DER SHA-256, set for https only.
	Pin [32]byte `json:"-"`
}

// DefaultStateDir is <home>/.local/state/ratatoskr-openrig.
func DefaultStateDir(home string) string {
	return filepath.Join(home, ".local", "state", "ratatoskr-openrig")
}

// DefaultPath is the configuration file used when --config is not given.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return "", errors.New("cannot find the home directory: pass --config PATH")
	}
	return filepath.Join(DefaultStateDir(home), "config.json"), nil
}

// ownedBy reports whether the file is owned by the current user.
func ownedBy(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

// Load reads and validates the file at path. insecureHTTP is the command-line flag that allows a plain http
// URL; without it an http URL is refused.
func Load(path string, insecureHTTP bool) (*Config, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("--config must be an absolute path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the configuration file: %s", describe(err))
	}
	switch {
	case !info.Mode().IsRegular():
		return nil, errors.New("the configuration file is not a regular file")
	case !ownedBy(info):
		return nil, errors.New("the configuration file is not owned by the current user")
	case info.Mode().Perm()&0o022 != 0:
		return nil, errors.New("the configuration file is writable by group or others: run chmod 600 on it")
	case info.Size() > MaxFileBytes:
		return nil, fmt.Errorf("the configuration file is larger than %d bytes", MaxFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the configuration file: %s", describe(err))
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var c Config
	if err := decoder.Decode(&c); err != nil {
		return nil, fmt.Errorf("the configuration file is not valid: %s", describeJSON(err))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the configuration file has content after the JSON object")
	}
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil, errors.New("cannot find the home directory")
	}
	c.Home = home
	if err := c.validate(insecureHTTP); err != nil {
		return nil, err
	}
	return &c, nil
}

func describe(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "it does not exist"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	}
	return "the operating system refused"
}

// describeJSON names a field or a kind of problem without echoing the file's content.
func describeJSON(err error) string {
	var syntax *json.SyntaxError
	var typed *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return "it is not JSON"
	case errors.As(err, &typed):
		return fmt.Sprintf("the field %q has the wrong type", typed.Field)
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return err.Error()
	}
	return "it is not a JSON object"
}

func absolute(field, value string) error {
	if value == "" {
		return fmt.Errorf("the field %q is required", field)
	}
	if strings.HasPrefix(value, "~") || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("the field %q must be a clean absolute path (no ~, no relative path)", field)
	}
	return nil
}

func (c *Config) validate(insecureHTTP bool) error {
	if c.ConfigVersion != Version {
		return fmt.Errorf("the field %q must be %d", "config_version", Version)
	}
	if err := c.validateURL(insecureHTTP); err != nil {
		return err
	}
	if !participantPattern.MatchString(c.Participant) {
		return fmt.Errorf("the field %q must match %s", "participant", participantPattern)
	}
	if !sessionPattern.MatchString(c.Session) {
		return fmt.Errorf("the field %q must match %s", "session", sessionPattern)
	}
	if !seatPattern.MatchString(c.OperatorSeat) {
		return fmt.Errorf("the field %q must look like seat@rig", "operator_seat")
	}
	for _, f := range []struct{ name, value string }{
		{"rig_path", c.RigPath}, {"openrig_home", c.OpenRigHome},
	} {
		if err := absolute(f.name, f.value); err != nil {
			return err
		}
	}
	if c.RigEnvPath == "" || strings.ContainsAny(c.RigEnvPath, "\x00\n") {
		return fmt.Errorf("the field %q is required", "rig_env_path")
	}
	for _, dir := range strings.Split(c.RigEnvPath, ":") {
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("the field %q must list absolute directories only", "rig_env_path")
		}
	}
	if c.OpenRigURL != "" {
		u, err := url.Parse(c.OpenRigURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("the field %q must be an http or https URL", "openrig_url")
		}
	}
	if c.StateDir == "" {
		c.StateDir = DefaultStateDir(c.Home)
	}
	if err := absolute("state_dir", c.StateDir); err != nil {
		return err
	}
	if c.CredentialPath == "" {
		c.CredentialPath = filepath.Join(c.StateDir, credentialName)
	}
	if err := absolute("credential_path", c.CredentialPath); err != nil {
		return err
	}
	if len(c.SocketPath()) > maxSocketPath {
		return fmt.Errorf("the field %q is too long: the socket path would exceed %d bytes", "state_dir", maxSocketPath)
	}
	return c.validateLimits()
}

func (c *Config) validateURL(insecureHTTP bool) error {
	if c.ParticipantURL == "" {
		return fmt.Errorf("the field %q is required", "participant_url")
	}
	u, err := url.Parse(c.ParticipantURL)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.Port() == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("the field %q must be a base URL: scheme://host:port, nothing else", "participant_url")
	}
	c.Scheme, c.Host = u.Scheme, u.Host
	switch u.Scheme {
	case "https":
		pin, err := ParsePin(c.CertificateSHA256)
		if err != nil {
			return err
		}
		c.Pin = pin
	case "http":
		if c.CertificateSHA256 != "" {
			return fmt.Errorf("the field %q is forbidden with an http participant_url", "certificate_sha256")
		}
		if !insecureHTTP {
			return fmt.Errorf("the field %q is http: plain HTTP needs the --insecure-http flag on the command", "participant_url")
		}
	default:
		return fmt.Errorf("the field %q must start with https:// or http://", "participant_url")
	}
	return nil
}

// ParsePin reads a certificate DER SHA-256 in the form 64 hex digits or sha256:64 hex digits. The sha256//
// key pin is a different rule and is refused.
func ParsePin(value string) ([32]byte, error) {
	var pin [32]byte
	if strings.HasPrefix(value, "sha256//") {
		return pin, fmt.Errorf("the field %q is a key pin (sha256//...): use the certificate SHA-256 (sha256:HEX)", "certificate_sha256")
	}
	digits := strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(digits)
	if err != nil || len(decoded) != 32 {
		return pin, fmt.Errorf("the field %q must be 64 hex digits, optionally prefixed sha256:", "certificate_sha256")
	}
	copy(pin[:], decoded)
	return pin, nil
}

func (c *Config) validateLimits() error {
	l := &c.Limits
	if l.InlineBodyBytes == 0 {
		l.InlineBodyBytes = defaultInlineBodyBytes
	}
	if l.PerSenderPerMinute == 0 {
		l.PerSenderPerMinute = defaultPerSenderPerMinute
	}
	if l.TotalPerMinute == 0 {
		l.TotalPerMinute = defaultTotalPerMinute
	}
	if c.ReadyCheckTimeoutMS == 0 {
		c.ReadyCheckTimeoutMS = defaultReadyCheckMS
	}
	switch {
	case l.InlineBodyBytes < 1 || l.InlineBodyBytes > MaxInlineBodyBytes:
		return fmt.Errorf("the field %q must be between 1 and %d", "limits.inline_body_bytes", MaxInlineBodyBytes)
	case l.PerSenderPerMinute < 1:
		return fmt.Errorf("the field %q must be at least 1", "limits.per_sender_per_minute")
	case l.TotalPerMinute < 1:
		return fmt.Errorf("the field %q must be at least 1", "limits.total_per_minute")
	case c.ReadyCheckTimeoutMS < 500 || c.ReadyCheckTimeoutMS > 2000:
		return fmt.Errorf("the field %q must be between 500 and 2000", "ready_check_timeout_ms")
	}
	return nil
}

// File locations inside the state directory.
func (c *Config) SocketPath() string  { return filepath.Join(c.StateDir, socketName) }
func (c *Config) LockPath() string    { return filepath.Join(c.StateDir, lockName) }
func (c *Config) JournalPath() string { return filepath.Join(c.StateDir, journalName) }
func (c *Config) StoppedPath() string { return filepath.Join(c.StateDir, stoppedName) }

// CheckStateDir applies the state-directory rule: it exists (created 0700 when create is set), is a
// directory and not a symlink, is owned by the current user and has mode 0700.
func (c *Config) CheckStateDir(create bool) error {
	if create {
		if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
			return fmt.Errorf("cannot create the state directory: %s", describe(err))
		}
	}
	info, err := os.Lstat(c.StateDir)
	switch {
	case err != nil:
		return fmt.Errorf("the state directory cannot be read: %s", describe(err))
	case info.Mode()&os.ModeSymlink != 0 || !info.IsDir():
		return errors.New("the state directory is not a plain directory (a symlink or a file is refused)")
	case !ownedBy(info):
		return errors.New("the state directory is not owned by the current user")
	case info.Mode().Perm() != 0o700:
		return errors.New("the state directory must have mode 0700: run chmod 700 on it")
	}
	return nil
}

// ValidSeat reports whether s looks like seat@rig, the shape operator_seat must have.
func ValidSeat(s string) bool { return seatPattern.MatchString(s) }

// Check validates a configuration built in memory (the setup wizard) with the rules Load applies to a file.
// Home must be set. It fills the same defaults and derived fields as Load.
func (c *Config) Check(insecureHTTP bool) error { return c.validate(insecureHTTP) }
