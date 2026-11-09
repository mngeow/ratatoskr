package setup

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/gw"
)

// RedeemOutcome is how a redemption ended.
type RedeemOutcome int

const (
	Paired          RedeemOutcome = iota
	RedeemFailed                  // exit 1: refused, or nothing was written
	RedeemUncertain               // exit 3: the code may or may not have been redeemed
	RedeemUsage                   // exit 2: could not even start (credential path)
)

// RedeemResult carries the outcome and a one-line message that never contains the code.
type RedeemResult struct {
	Outcome RedeemOutcome
	Message string
}

// Redeem redeems the single-use code once and writes the credential at mode 0600. The credential file is
// created with O_EXCL BEFORE the code is redeemed, so a bad path cannot consume the code and an existing file
// is never overwritten. It is the one redemption code: `pair --pair-code-file` and the setup engine both call
// it, and neither puts the code on disk, in argv, in the environment or in a log.
func Redeem(cfg *config.Config, pairCode string) RedeemResult {
	file, err := os.OpenFile(cfg.CredentialPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return RedeemResult{RedeemUsage, "the credential file already exists; remove it or set another credential_path"}
		}
		return RedeemResult{RedeemUsage, "cannot create the credential file"}
	}
	released := false
	defer func() {
		_ = file.Close()
		if !released {
			_ = os.Remove(cfg.CredentialPath)
		}
	}()

	client := gw.New(gw.Options{Scheme: cfg.Scheme, Host: cfg.Host, Pin: cfg.Pin})
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, e := client.Discover(ctx); e != nil {
		return RedeemResult{RedeemFailed, "discovery failed: " + e.Msg}
	}
	credential, e := client.Pair(ctx, pairCode, cfg.Participant)
	if e != nil {
		// The code may have been redeemed when the request was written and no usable answer came back.
		if e.Wrote && (e.Kind == gw.KindNetwork || e.Kind == gw.KindServer || e.Kind == gw.KindProtocol) {
			released = true // keep the reserved (empty) file as a marker; the owner decides
			_ = file.Close()
			return RedeemResult{RedeemUncertain, "the pairing request did not complete: the code may or may not have been redeemed (the empty credential file was left; remove it before trying a new code)"}
		}
		return RedeemResult{RedeemFailed, e.Msg}
	}
	if _, err := file.WriteString(credential + "\n"); err != nil {
		released = true
		return RedeemResult{RedeemUncertain, "the code was redeemed but the credential could not be written: pair again with a new code after removing the credential file"}
	}
	if err := file.Sync(); err != nil {
		released = true
		return RedeemResult{RedeemUncertain, "the code was redeemed but the credential could not be synced to disk"}
	}
	released = true
	_ = os.Remove(stoppedPath(cfg))
	return RedeemResult{Paired, "paired as " + cfg.Participant + "; credential written to " + cfg.CredentialPath + " (mode 0600)"}
}

func stoppedPath(cfg *config.Config) string { return cfg.StoppedPath() }
