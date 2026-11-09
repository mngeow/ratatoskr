// Package setup holds the pure parts of `ratatoskr-openrig setup`: the parser for the block that
// `rtk gateway participant pair` prints, the rig_env_path builder, the launchd plist renderer, the atomic
// configuration write and the small terminal helpers. The interactive flow is in internal/cli.
//
// UNTESTED. Under GR-3 nothing here has been run.
package setup

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

const (
	// MaxPasteLines and MaxPasteBytes bound one pasted block.
	MaxPasteLines = 40
	MaxPasteBytes = 8 * 1024
	// MaxCodeBytes bounds the pairing code.
	MaxCodeBytes = 4096

	// EndLinePrefix starts the last line `rtk gateway participant pair` prints.
	EndLinePrefix = "the address above was recorded"
)

var certLine = regexp.MustCompile(`^certificate SHA-256[^:]*:\s*(\S+)$`)

// Pasted is what the block contained. Code is a byte slice so the caller can zero it.
type Pasted struct {
	Code        []byte
	Participant string
	URL         string // participant_url: scheme://host:port
	Pin         string // sha256:HEX (certificate), empty for plain HTTP
	Plain       bool   // the URL is http://
	Rotation    bool   // the block carried the re-pair (rotation) line
	KeyPinSeen  bool   // a key pin (sha256//) line was present and ignored
	StaleNote   string // the "the address above was recorded ..." line, shown to the owner as printed
}

// Wipe zeroes the code.
func (p *Pasted) Wipe() { Wipe(p.Code) }

// Wipe zeroes a byte slice where practical.
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ParsePaste reads the block `rtk gateway participant pair NAME` prints (internal/gateway/participant_admin.go):
//
//	<CODE>
//	participant: NAME
//	expires in 10 minutes; single use
//	this is a rotation: ...                    only on a re-pair
//	participant API: https://HOST:PORT
//	certificate SHA-256 (...): sha256:HEX      the pin to use
//	key pin (...): sha256//BASE64              ignored: a different rule
//	the address above was recorded ...
//
// Unknown lines are ignored. The code is never part of an error message.
func ParsePaste(text []byte) (Pasted, error) {
	var p Pasted
	if !utf8.Valid(text) {
		return p, errors.New("the pasted text is not UTF-8")
	}
	haveCode := false
	var certValue string
	for _, raw := range bytes.Split(text, []byte("\n")) {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		if !haveCode {
			// The first non-empty line is the code. A block pasted without it starts with a label.
			if strings.HasPrefix(line, "participant:") || strings.HasPrefix(line, "participant API:") {
				return p, errors.New("the first line must be the pairing code alone: copy the whole output, starting at the code")
			}
			if strings.ContainsAny(line, " \t") || len(line) > MaxCodeBytes {
				return p, errors.New("the first line is not a pairing code (a code is one word): copy the whole output, starting at the code")
			}
			p.Code = []byte(line)
			haveCode = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "participant API:"):
			p.URL = strings.TrimSpace(strings.TrimPrefix(line, "participant API:"))
		case strings.HasPrefix(line, "participant:"):
			p.Participant = strings.TrimSpace(strings.TrimPrefix(line, "participant:"))
		case strings.HasPrefix(line, EndLinePrefix):
			p.StaleNote = Printable(line)
		case strings.HasPrefix(line, "key pin"):
			p.KeyPinSeen = true
		case strings.HasPrefix(line, "this is a rotation"):
			p.Rotation = true
		case strings.HasPrefix(line, "certificate SHA-256"):
			if m := certLine.FindStringSubmatch(line); m != nil {
				certValue = m[1]
			}
		}
	}
	if !haveCode {
		p.Wipe()
		return Pasted{}, errors.New("nothing was pasted")
	}
	fail := func(format string, args ...any) (Pasted, error) {
		p.Wipe()
		return Pasted{}, fmt.Errorf(format, args...)
	}
	if p.Participant == "" {
		return fail("the block has no \"participant:\" line: copy the whole output")
	}
	if p.URL == "" {
		return fail("the block has no \"participant API:\" line: copy the whole output")
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Host == "" || u.Port() == "" {
		return fail("the \"participant API:\" line is not scheme://host:port")
	}
	switch u.Scheme {
	case "https":
		if certValue == "" {
			if p.KeyPinSeen {
				return fail("the block has a key pin (sha256//...) but no \"certificate SHA-256\" line: the bridge needs the certificate pin; run the pair command again on VM-A and copy the whole output")
			}
			return fail("the block has no \"certificate SHA-256\" line: copy the whole output")
		}
		pin, err := config.ParsePin(certValue)
		if err != nil {
			return fail("the \"certificate SHA-256\" value is not a certificate pin (expected sha256: and 64 hex digits)")
		}
		p.Pin = fmt.Sprintf("sha256:%x", pin[:])
	case "http":
		p.Plain = true
	default:
		return fail("the \"participant API:\" line must start with https:// or http://")
	}
	p.URL = u.Scheme + "://" + u.Host
	return p, nil
}

// CheckAgainst validates a parsed block against the configured participant and the plain-HTTP flag.
func (p Pasted) CheckAgainst(participant string, insecureHTTP bool) error {
	if p.Participant != participant {
		return fmt.Errorf("the block is for participant %q, but this bridge is configured as %q: run the pair command for %s on VM-A",
			Printable(p.Participant), participant, participant)
	}
	if p.Plain && !insecureHTTP {
		return errors.New("the participant API is plain http://: re-run with --insecure-http to accept it, or ask for an https listener")
	}
	return nil
}

// Printable replaces anything that is not a printable character by a space and cuts to 120 runes.
func Printable(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n == 120 {
			break
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			r = ' '
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
