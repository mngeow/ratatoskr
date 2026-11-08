package daemon

// The card listener: the one new network surface this feature adds. It serves the host's PUBLISHED
// card, read-only, on exactly one address, and nothing else.
//
// It is off unless the owner starts the daemon with --card-listen. The address rules are the gateway's
// (loopback and Tailscale plain HTTP; a LAN address needs --allow-lan and serves HTTPS with a
// self-signed certificate whose fingerprint the owner registers on the gateway; plain HTTP on a LAN
// needs --insecure-http; no public address), with the messages naming the card listener. Plain HTTP
// on Tailscale is the existing pattern, not a claim of HTTPS.
//
// It never shares the Unix socket's handlers and has no route to any operation: GET and HEAD on the
// two well-known paths, and everything else is a 404 or a 405. No cookies, no CORS headers, no
// directory listing, no redirects. The card's own URL in the served bytes comes from the BOUND
// address, never from a request header.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No socket has been bound.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mngeow/ratatoskr/internal/gateway"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// subjectCardListener names this listener in the shared address messages.
	subjectCardListener = "card listener"

	cardServerTimeout    = 5 * time.Second // UNTESTED guess; contract default
	cardServerMaxHeader  = 8 << 10         // UNTESTED guess; contract default
	cardServerShutdown   = 2 * time.Second // UNTESTED guess
	cardCacheControl     = "max-age=30"    // UNTESTED guess; contract default
	cardTLSDirName       = "card-tls"
	cardFirewallHintLead = "if another machine cannot reach this card, check inbound TCP"
)

// planCardListen validates the listener flags without touching a file or a socket. A zero CardListen
// means the listener is off; the other two flags then mean nothing and are refused, so a flag that does
// nothing cannot hide a mistake.
//
// UNTESTED.
func planCardListen(cfg Config) (plan gateway.ListenPlan, on bool, err error) {
	if cfg.CardListen == "" {
		if cfg.CardAllowLAN || cfg.CardInsecureHTTP {
			return gateway.ListenPlan{}, false, errors.New("daemon: --allow-lan and --insecure-http apply only with --card-listen ADDR")
		}
		return gateway.ListenPlan{}, false, nil
	}
	plan, err = gateway.PlanListenFor(subjectCardListener, cfg.CardListen, cfg.CardAllowLAN, cfg.CardInsecureHTTP)
	if err != nil {
		return gateway.ListenPlan{}, false, err
	}
	return plan, true, nil
}

// boundCard is a card listener that has been bound but is not serving yet.
type boundCard struct {
	plan        gateway.ListenPlan
	listener    net.Listener
	fingerprint string
	view        protocol.CardListener
}

// bindCardListener binds the listener, after the plan has been validated. The certificate for HTTPS is
// loaded or created before the socket exists, so a refused identity leaves no bound address behind.
// One bind, no fallback, no retry: a failed bind is a failed start that names what to fix.
//
// UNTESTED.
func bindCardListener(plan gateway.ListenPlan, daemonDirectory string) (*boundCard, error) {
	var cert tls.Certificate
	fingerprint := ""
	if plan.Transport == gateway.TransportHTTPS {
		var err error
		cert, fingerprint, err = gateway.LoadOrCreateIdentity(filepath.Join(daemonDirectory, cardTLSDirName))
		if err != nil {
			return nil, err
		}
	}

	listener, err := gateway.ListenFor(subjectCardListener, plan.Addr)
	if err != nil {
		return nil, fmt.Errorf("%w; bring up the address (or Tailscale) and start the daemon again", err)
	}

	scheme := "http"
	if plan.Transport == gateway.TransportHTTPS {
		scheme = "https"
		listener = tls.NewListener(listener, gateway.ServerTLSConfig(cert))
	}

	actual, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("daemon: the card listener did not report a TCP address")
	}
	bound := actual.AddrPort()
	if bound.Addr().Is4In6() {
		bound = netip.AddrPortFrom(bound.Addr().Unmap(), bound.Port())
	}

	return &boundCard{
		plan:        plan,
		listener:    listener,
		fingerprint: fingerprint,
		view: protocol.CardListener{
			State:     protocol.CardListenerOn,
			URL:       scheme + "://" + bound.String() + protocol.CardPath,
			Transport: scheme,
		},
	}, nil
}

// startupLines are what the daemon logs once the listener is bound: where, how, and what the owner must
// do next. Nothing secret: the fingerprint is public.
func (b *boundCard) startupLines() []string {
	lines := []string{"the host card is served at " + b.view.URL}
	switch b.plan.Transport {
	case gateway.TransportHTTPS:
		lines = append(lines, "card transport: HTTPS, self-signed certificate, fingerprint "+b.fingerprint+" (register it with: rtk gateway card-target set HOST URL --fingerprint ...)")
	case gateway.TransportLANHTTP:
		lines = append(lines, gateway.PlainHTTPWarning)
	default:
		if b.plan.Class == gateway.ClassTailscale {
			lines = append(lines, "card transport: plain HTTP; encrypted only if this address really is Tailscale (A2A asks for HTTPS in production; this is the gateway's existing pattern)")
		} else {
			lines = append(lines, "card transport: plain HTTP; loopback only")
		}
	}
	if b.plan.Class == gateway.ClassLAN {
		lines = append(lines, fmt.Sprintf("%s %d on this machine's firewall; restrict any allow rule to the intended source hosts or interface", cardFirewallHintLead, b.plan.Addr.Port()))
	}
	return lines
}

// cardServer serves the published card.
type cardServer struct {
	service *cardService
	log     *logger
	http    *http.Server
}

// newCardServer builds the server over the service's published bytes.
func newCardServer(service *cardService, log *logger) *cardServer {
	server := &cardServer{service: service, log: log}
	server.http = &http.Server{
		Handler:           server.handler(),
		ReadHeaderTimeout: cardServerTimeout,
		ReadTimeout:       cardServerTimeout,
		WriteTimeout:      cardServerTimeout,
		IdleTimeout:       cardServerTimeout,
		MaxHeaderBytes:    cardServerMaxHeader,
		// A non-nil empty map keeps HTTP/2 off.
		TLSNextProto:                 map[string]func(*http.Server, *tls.Conn, http.Handler){},
		DisableGeneralOptionsHandler: true,
	}
	return server
}

// serve runs until shutdown. The caller runs it on a goroutine it owns.
func (c *cardServer) serve(listener net.Listener) {
	if err := c.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		c.log.logf("the card listener stopped: %v", err)
	}
}

// shutdown stops the server within a short grace.
func (c *cardServer) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), cardServerShutdown)
	defer cancel()
	if err := c.http.Shutdown(ctx); err != nil {
		_ = c.http.Close()
	}
}

// handler is the whole surface: GET and HEAD on two paths.
func (c *cardServer) handler() http.Handler {
	router := chi.NewRouter()
	router.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNotFound)
	})
	router.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	for _, path := range []string{protocol.CardPath, protocol.CardAliasPath} {
		router.Get(path, c.serveCard)
		router.Head(path, c.serveCard)
	}
	return router
}

// serveCard answers a card request: the published bytes with a validator, a 304 for a matching
// If-None-Match, or a bodyless 503 when nothing is published.
func (c *cardServer) serveCard(w http.ResponseWriter, r *http.Request) {
	body, etag, ok := c.service.servedBytes()
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", cardCacheControl)
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// etagMatches reports whether an If-None-Match header names the current validator. The comparison is
// the weak one RFC 9110 prescribes for If-None-Match (a W/ prefix is ignored), and "*" matches
// anything that exists.
func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
			return true
		}
	}
	return false
}
