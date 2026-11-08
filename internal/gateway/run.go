package gateway

// The `rtk gateway --listen ADDR` process: apply the address and flag rules, open the
// database under the gateway lock, load or create the TLS identity for HTTPS, bind the
// address, serve, record the endpoint, and stop cleanly on a signal.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No listener has been bound and
// no database has been opened by this path.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

const (
	// shutdownGrace bounds the HTTP shutdown when a signal ends the process.
	shutdownGrace = 5 * time.Second // UNTESTED guess
)

// Run serves one gateway until ctx ends.
//
// The order is part of the contract: the address and the flags are validated before any
// file or lock is touched, so a mistyped address or a flag that does not apply fails
// without creating state; then the database is opened under the gateway lock, so a
// second gateway on the same state directory is refused before a socket exists; then the
// directory is prepared; then, for HTTPS only, the identity is loaded or created; then
// the address is bound.
//
// UNTESTED.
func Run(ctx context.Context, stateDir, listen string, allowLAN, insecureHTTP bool, cardPoll time.Duration, status io.Writer) error {
	plan, err := PlanListen(listen, allowLAN, insecureHTTP)
	if err != nil {
		return err
	}
	// A card poll interval outside its range is a mistyped flag, refused before any file or lock is
	// touched (0 means the default).
	if cardPoll != 0 && (cardPoll < MinCardPollInterval || cardPoll > MaxCardPollInterval) {
		return fmt.Errorf("gateway: --card-poll-interval must be between %s and %s", MinCardPollInterval, MaxCardPollInterval)
	}

	store, err := Open(ctx, stateDir, true)
	if err != nil {
		return err
	}
	defer func() {
		_ = store.Close()
	}()

	// An upgraded database says so, with the backup's path, before anything else is printed: the
	// owner needs that path if the older binary has to come back.
	if migrated := store.Migration(); migrated.Backup != "" {
		fmt.Fprintf(status, "migrated the gateway database from schema %d to %d; backup at %s\n",
			migrated.From, migrated.To, migrated.Backup)
	}

	// PrepareDir is idempotent and returns the directory this gateway owns, which is
	// what the status line reports. It runs after Open so the order above holds.
	dir, err := PrepareDir(stateDir)
	if err != nil {
		return err
	}
	if dir == "" {
		dir = filepath.Join(stateDir, "gateway")
	}

	// HTTPS carries a pinned self-signed certificate, so the identity is loaded or
	// created before the socket exists: a refused identity must not leave a bound
	// address behind.
	var cert tls.Certificate
	fingerprint := ""
	if plan.Transport == TransportHTTPS {
		cert, fingerprint, err = LoadOrCreateIdentity(filepath.Join(dir, "tls"))
		if err != nil {
			return err
		}
	}

	listener, err := Listen(plan.Addr)
	if err != nil {
		return err
	}
	if plan.Transport == TransportHTTPS {
		// The handshake is bounded by the http.Server's ReadHeaderTimeout, so no
		// custom handshake listener is needed.
		listener = tls.NewListener(listener, serverTLSConfig(cert))
	}

	srv := NewServer(store, Options{})

	// The card poller runs for the life of the gateway, after the bind and independent of any viewer.
	// It is built and started BEFORE Serve begins, so the server's reference to it is written before
	// any request goroutine can read it (there is no lock around that field). It is stopped (cancelled
	// and waited for) before the deferred store.Close above runs, because deferred calls run last-in
	// first-out and this one is registered after it.
	stopCardPoller := srv.StartCardPoller(ctx, cardPoll, func(format string, args ...any) {
		fmt.Fprintf(status, format+"\n", args...)
	})
	defer stopCardPoller()

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()

	addr := listener.Addr().String()

	// Written after a successful bind and before the status lines, so `rtk gateway
	// pair` on this machine can print a ready command. Failing to record it is a
	// startup failure: the listener stops again and the error is returned.
	if err := WriteEndpoint(dir, plan, fingerprint); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return errors.Join(err, srv.Shutdown(shutdownCtx))
	}

	// Nothing secret in any of these lines: no credential, no pairing code, and the
	// fingerprint is public.
	for _, line := range statusLines(plan, addr, dir, fingerprint) {
		fmt.Fprintln(status, line)
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("gateway: shutdown: %w", err)
		}
		return nil
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("gateway: serve: %w", err)
		}
		return nil
	}
}

// statusLines is what a gateway prints once it is serving: the listening line, exactly
// one transport line, and - for both LAN modes - the firewall hint.
//
// UNTESTED.
func statusLines(plan ListenPlan, addr, dir, fingerprint string) []string {
	lines := []string{
		fmt.Sprintf("gateway listening on %s (state in %s)", addr, dir),
	}

	switch plan.Transport {
	case TransportHTTPS:
		lines = append(lines, "transport: HTTPS, self-signed certificate, fingerprint "+fingerprint)
	case TransportLANHTTP:
		lines = append(lines, PlainHTTPWarning)
	default:
		if plan.Class == ClassTailscale {
			lines = append(lines, "transport: plain HTTP; encrypted only if this address really is Tailscale")
		} else {
			lines = append(lines, "transport: plain HTTP; loopback only")
		}
	}

	if plan.Class == ClassLAN {
		lines = append(lines, fmt.Sprintf(
			"reachable by every device on this LAN; if a runtime cannot reach this address, check that this machine's firewall allows incoming TCP on port %d",
			plan.Addr.Port(),
		))
	}
	return lines
}
