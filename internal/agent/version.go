package agent

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

const (
	// versionTimeout bounds the startup probe. A harness that does not answer in
	// this long is reported as unknown rather than delaying the interface.
	versionTimeout = 5 * time.Second // UNTESTED guess

	// versionMaxBytes bounds the stored version string.
	versionMaxBytes = 64
)

// Version reports what a harness says its version is, or "" when that cannot be
// learned.
//
// It runs `<harness> --version`, which is why only App calls it, at startup. Any
// failure - unknown harness, not on PATH, non-zero exit, timeout, empty output -
// is reported as unknown instead of an error: a missing version is a missing
// column, not a reason to refuse to start.
//
// UNTESTED.
func Version(ctx context.Context, harness string) string {
	if harness != harnessClaude && harness != harnessCodex {
		return ""
	}

	binary, err := exec.LookPath(harness)
	if err != nil {
		return ""
	}

	probeCtx, cancel := context.WithTimeout(ctx, versionTimeout)
	defer cancel()

	output, err := exec.CommandContext(probeCtx, binary, "--version").Output()
	if err != nil {
		return ""
	}

	return truncateVersion(firstNonEmptyLine(string(output)))
}

// firstNonEmptyLine returns the first line that carries something other than
// whitespace, trimmed.
func firstNonEmptyLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// truncateVersion cuts a version string to versionMaxBytes without splitting a
// character in half: the stored value is shown to a user, and half a rune is
// never what the harness said.
func truncateVersion(version string) string {
	if len(version) <= versionMaxBytes {
		return version
	}

	cut := versionMaxBytes
	for cut > 0 && !utf8Start(version[cut]) {
		cut--
	}
	return version[:cut]
}

// utf8Start reports whether b can begin a UTF-8 encoded rune.
func utf8Start(b byte) bool {
	return b&0xC0 != 0x80
}
