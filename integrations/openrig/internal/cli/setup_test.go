package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestSetupRefusesWithoutATerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	code := Main([]string{"setup"}, IO{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut})
	if code != ExitUsage {
		t.Fatalf("exit %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut.String(), "terminal") {
		t.Fatalf("message should name the terminal: %q", errOut.String())
	}
}
