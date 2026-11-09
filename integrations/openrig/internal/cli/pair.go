package cli

// pair: redeem the single-use code once and write the credential at mode 0600 (contract section 4). The file
// is created with O_EXCL BEFORE the code is redeemed, so a bad path cannot consume the code.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/setup"
)

const maxSecretBytes = 4096

func readPairCode(path string) (string, error) {
	info, err := os.Lstat(path)
	switch {
	case err != nil:
		return "", errors.New("cannot read the pairing-code file")
	case !info.Mode().IsRegular():
		return "", errors.New("the pairing-code file is not a regular file")
	case info.Mode().Perm()&0o077 != 0:
		return "", errors.New("the pairing-code file is readable by group or others: run chmod 600 on it")
	case info.Size() > maxSecretBytes:
		return "", fmt.Errorf("the pairing-code file is larger than %d bytes", maxSecretBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("cannot read the pairing-code file")
	}
	if !utf8.Valid(raw) {
		return "", errors.New("the pairing-code file is not UTF-8")
	}
	code := strings.TrimSpace(string(raw))
	if code == "" {
		return "", errors.New("the pairing-code file is empty")
	}
	return code, nil
}

func runPair(args []string, io IO) int {
	fs := newFlags("pair")
	configPath := fs.String("config", "", "")
	codeFile := fs.String("pair-code-file", "", "")
	insecure := fs.Bool("insecure-http", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || *codeFile == "" {
		return fail(io, ExitUsage, "usage: pair --pair-code-file PATH [--config PATH] [--insecure-http]")
	}
	cfg, code := loadConfig(*configPath, *insecure, io)
	if cfg == nil {
		return code
	}
	if err := cfg.CheckStateDir(true); err != nil {
		return fail(io, ExitUsage, "%v", err)
	}
	pairCode, err := readPairCode(*codeFile)
	if err != nil {
		return fail(io, ExitUsage, "%v", err)
	}
	return redeemPair(cfg, pairCode, io)
}

// redeemPair is the command's wrapper around setup.Redeem (the one redemption code, shared with the setup
// engine).
func redeemPair(cfg *config.Config, pairCode string, io IO) int {
	if cfg.Scheme == "http" {
		fmt.Fprintln(io.Stderr, "WARNING: plain HTTP: the pairing code and the credential are readable by anything on the network path.")
	}
	res := setup.Redeem(cfg, pairCode)
	switch res.Outcome {
	case setup.Paired:
		fmt.Fprintf(io.Stdout, "paired as %s\ncredential written to %s (mode 0600)\n", cfg.Participant, cfg.CredentialPath)
		return ExitOK
	case setup.RedeemUncertain:
		return fail(io, ExitUncertain, "%s", res.Message)
	case setup.RedeemUsage:
		return fail(io, ExitUsage, "%s", res.Message)
	}
	return fail(io, ExitFailed, "%s", res.Message)
}
