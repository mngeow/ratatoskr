package setup

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Label is the launchd job label of the template.
const Label = "dev.ratatoskr.openrig"

// PlistValues are the five placeholders of launchd/dev.ratatoskr.openrig.plist.template.
type PlistValues struct {
	Binary, Config, StateDir, LogDir, Home string
}

var leftover = regexp.MustCompile(`__[A-Z_]+__`)

// RenderPlist fills the template. Every value must be a clean absolute path with no control character; each is
// XML-escaped. A placeholder left over after the fill is an error (the template and this code disagree).
func RenderPlist(template string, v PlistValues) (string, error) {
	pairs := []struct{ name, value string }{
		{"__BINARY__", v.Binary}, {"__CONFIG__", v.Config}, {"__STATE_DIR__", v.StateDir},
		{"__LOG_DIR__", v.LogDir}, {"__HOME__", v.Home},
	}
	var args []string
	for _, p := range pairs {
		if p.value == "" || !filepath.IsAbs(p.value) || filepath.Clean(p.value) != p.value || hasControl(p.value) {
			return "", fmt.Errorf("the value for %s must be a clean absolute path", p.name)
		}
		escaped, err := xmlEscape(p.value)
		if err != nil {
			return "", fmt.Errorf("the value for %s cannot be written to XML", p.name)
		}
		args = append(args, p.name, escaped)
	}
	// One pass: a value that itself contains a placeholder name is not substituted again.
	out := strings.NewReplacer(args...).Replace(template)
	if leftover.MatchString(out) {
		return "", errors.New("the plist template has a placeholder this program does not know")
	}
	return out, nil
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func xmlEscape(s string) (string, error) {
	var b bytes.Buffer
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return "", err
	}
	return b.String(), nil
}

// LineDiff lists the lines that differ between two texts, "- old" then "+ new", at most max lines. It is a
// plain positional comparison, enough to show an owner what a replacement would change.
func LineDiff(old, new string, max int) []string {
	a, b := strings.Split(old, "\n"), strings.Split(new, "\n")
	var out []string
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y string
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x == y {
			continue
		}
		if len(out) >= max {
			return append(out, "... (more differences)")
		}
		if i < len(a) {
			out = append(out, fmt.Sprintf("- %s", Printable(x)))
		}
		if i < len(b) {
			out = append(out, fmt.Sprintf("+ %s", Printable(y)))
		}
	}
	return out
}
