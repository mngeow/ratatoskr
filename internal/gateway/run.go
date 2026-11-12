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
	"net"
	"path/filepath"
	"time"
)

const (
	// shutdownGrace bounds the HTTP shutdown when a signal ends the process.
	shutdownGrace = 5 * time.Second // UNTESTED guess
)

// Run serves one gateway until ctx ends, with the participant API off (Run predates it and keeps its
// behaviour; `rtk gateway` goes through RunWith).
//
// UNTESTED.
func Run(ctx context.Context, stateDir, listen string, https bool, cardPoll time.Duration, status io.Writer) error {
	return RunWith(ctx, stateDir, listen, https, ParticipantOptions{Listen: ParticipantListenOff}, cardPoll, status)
}

// RunWith serves one gateway until ctx ends, and a second listener for the participant API when
// participant.Listen is not ParticipantListenOff.
//
// The order is part of the contract: the addresses and the flags are validated before any file or lock
// is touched (both plans, and they must not name the same endpoint), so a mistyped address or a flag that
// does not apply fails without creating state; then the database is opened ONCE under the gateway lock,
// so a second gateway on the same state directory is refused before a socket exists; then the directory
// is prepared; then, if EITHER listener is HTTPS, the one identity is loaded or created, before either
// bind; then the host address is bound, then the participant address, and a failure of the second closes
// the first.
//
// With the participant API off this is the single-listener startup it always was.
//
// UNTESTED.
func RunWith(ctx context.Context, stateDir, listen string, https bool, participant ParticipantOptions, cardPoll time.Duration, status io.Writer) error {
	if listen == "" {
		listen = DefaultListen
	}
	plan, participantPlan, participantDefaulted, err := planBoth(listen, https, participant)
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
	// created before any socket exists: a refused identity must not leave a bound
	// address behind. One identity serves both listeners, loaded once, and either may be HTTPS
	// without the other.
	participantHTTPS := participantPlan != nil && participantPlan.Transport == TransportHTTPS
	var cert tls.Certificate
	identityFingerprint := ""
	if plan.Transport == TransportHTTPS || participantHTTPS {
		cert, identityFingerprint, err = LoadOrCreateIdentity(filepath.Join(dir, "tls"))
		if err != nil {
			return err
		}
	}
	fingerprint := ""
	if plan.Transport == TransportHTTPS {
		fingerprint = identityFingerprint
	}
	spkiPin := ""
	if participantHTTPS {
		spkiPin, err = SPKIPin(cert.Certificate[0])
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

	var participantListener net.Listener
	if participantPlan != nil {
		participantListener, err = ListenFor(participantSubject, participantPlan.Addr)
		if err != nil {
			if participantDefaulted {
				err = fmt.Errorf("%w (%s was the default participant address; use %s IP:PORT to choose another, or %s %s to turn the participant API off)",
					err, participantPlan.Addr, FlagParticipantListen, FlagParticipantListen, ParticipantListenOff)
			}
			// Roll back: nothing is left bound.
			_ = listener.Close()
			return err
		}
		if participantHTTPS {
			// A separate tls.Config, never shared with the host listener's.
			participantListener = tls.NewListener(participantListener, serverTLSConfig(cert))
		}
	}

	srv := NewServer(store, Options{DashboardHTTPS: plan.Transport == TransportHTTPS})
	if participantPlan != nil {
		srv.EnableParticipantServer()
	}

	// The card poller runs for the life of the gateway, after the bind and independent of any viewer.
	// It is built and started BEFORE Serve begins, so the server's reference to it is written before
	// any request goroutine can read it (there is no lock around that field). It is stopped (cancelled
	// and waited for) before the deferred store.Close above runs, because deferred calls run last-in
	// first-out and this one is registered after it.
	stopCardPoller := srv.StartCardPoller(ctx, cardPoll, func(format string, args ...any) {
		fmt.Fprintf(status, format+"\n", args...)
	})
	defer stopCardPoller()

	serveErr := make(chan error, 2)
	go func() {
		serveErr <- srv.Serve(listener)
	}()
	if participantListener != nil {
		go func() {
			serveErr <- srv.ServeParticipant(participantListener)
		}()
	}

	addr := listener.Addr().String()

	// Written after a successful bind and before the status lines, so `rtk gateway
	// pair` on this machine can print a ready command. Failing to record it is a
	// startup failure: both servers stop again and the error is returned.
	if err := WriteEndpoint(dir, plan, fingerprint); err != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		return errors.Join(err, srv.Shutdown(shutdownCtx))
	}
	// The participant endpoint is its own file: the host endpoint file above is never touched by it. With the
	// participant API off, a regular file left by an earlier run is removed so the owner commands do not print
	// an address nothing serves; anything else at that name is left alone and reported.
	if participantPlan != nil {
		participantFingerprint := ""
		if participantHTTPS {
			participantFingerprint = identityFingerprint
		}
		if err := WriteParticipantEndpoint(dir, *participantPlan, participantFingerprint, spkiPin); err != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			return errors.Join(err, srv.Shutdown(shutdownCtx))
		}
	} else if warning := RemoveStaleParticipantEndpoint(dir); warning != "" {
		// Reported, never fatal, and the host endpoint file is not involved.
		fmt.Fprintln(status, warning)
	}

	// Nothing secret in any of these lines: no credential, no pairing code, and the
	// fingerprint is public. The host lines come first and are unchanged.
	for _, line := range statusLines(plan, addr, dir, fingerprint) {
		fmt.Fprintln(status, line)
	}
	if participantPlan != nil {
		participantFingerprint := ""
		if participantHTTPS {
			participantFingerprint = identityFingerprint
		}
		for _, line := range participantStatusLines(*participantPlan, participantListener.Addr().String(), participantDefaulted, participantFingerprint, spkiPin) {
			fmt.Fprintln(status, line)
		}
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
		if participantPlan != nil {
			// Either server stopping on its own takes the other down with it: neither is left running
			// alone.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
			defer cancel()
			shutdownErr := srv.Shutdown(shutdownCtx)
			if err != nil {
				return errors.Join(fmt.Errorf("gateway: serve: %w", err), shutdownErr)
			}
			return shutdownErr
		}
		if err != nil {
			return fmt.Errorf("gateway: serve: %w", err)
		}
		return nil
	}
}

// statusLines is what a gateway prints once it is serving: the listening line, the transport, the
// dashboard's exposure and the firewall boundary, each with the ACTUAL address and port (D16, D17, D19).
// Nothing secret appears; the fingerprint is public.
//
// UNTESTED.
func statusLines(plan ListenPlan, addr, dir, fingerprint string) []string {
	lines := []string{fmt.Sprintf("gateway listening on %s (state in %s)", addr, dir)}
	if plan.Class == ClassWildcard {
		lines = append(lines, fmt.Sprintf("listening on every interface (%s), including any public one", plan.Addr.Addr()))
	}
	if plan.Transport == TransportHTTPS {
		lines = append(lines, "transport: HTTPS, self-signed certificate, fingerprint "+fingerprint+" (runtimes pair with --fingerprint; browsers warn about the certificate)")
	} else if plan.Class == ClassLoopback {
		lines = append(lines, "transport: plain HTTP; loopback only")
	} else {
		lines = append(lines, PlainHTTPWarning)
	}
	scheme := "http"
	if plan.Transport == TransportHTTPS {
		scheme = "https"
	}
	lines = append(lines,
		fmt.Sprintf("dashboard: %s://<this machine's address>:%d/ui/ - NO authentication: anyone who can reach this port can read it (hosts, session ids, Agent Cards); read-only", scheme, plan.Addr.Port()),
		fmt.Sprintf("rtk does not configure a firewall: restrict inbound TCP on port %d to the machines you trust; --listen IP:PORT narrows the bind", plan.Addr.Port()))
	return lines
}
