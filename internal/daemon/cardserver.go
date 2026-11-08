package daemon

// The card listener: the one network surface this feature adds. It serves the host's card, read-only,
// on exactly one address, and nothing else.
//
// It is ON BY DEFAULT, at 0.0.0.0:7778 (the gateway is 7777), by the owner's decision: the card is
// public, plain HTTP, unauthenticated and readable by anyone who can reach the port, so nothing in it
// may be sensitive (the existing redaction rules stay mandatory). --card-listen ADDR changes the
// address and --card-listen off turns it off. A bind failure does NOT stop the daemon: it is the
// "not bound" state with a reason, shown in the log, in `rtk daemon status` and in the Graph tab. A
// running daemon needs a restart (rtk daemon stop ends sessions) to change any of this.
//
// It never shares the Unix socket's handlers and has no route to any operation: GET and HEAD on the
// two well-known paths, and everything else is a 404 or a 405. No cookies, no CORS headers, no
// directory listing, no redirects. The card's own URL in the served bytes comes from the BOUND
// address, never from a request header; when the bound address is a wildcard, an address of this
// machine is advertised (advertiseAddress), and the gateway still polls the address its owner
// registered, not this text.
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
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	cardServerTimeout   = 5 * time.Second // UNTESTED guess; contract default
	cardServerMaxHeader = 8 << 10         // UNTESTED guess; contract default
	cardServerShutdown  = 2 * time.Second // UNTESTED guess
	cardCacheControl    = "max-age=30"    // UNTESTED guess; contract default

	// cardListenOff is the --card-listen value that turns the listener off.
	cardListenOff = "off"
)

// planCardListen turns the flag into an address, or says the listener is off. The empty flag is the
// default address. The address must be an IP literal with a port (no name is resolved).
//
// UNTESTED.
func planCardListen(flag string) (addr netip.AddrPort, on bool, err error) {
	switch value := strings.TrimSpace(flag); {
	case strings.EqualFold(value, cardListenOff):
		return netip.AddrPort{}, false, nil
	case value == "":
		return netip.MustParseAddrPort(protocol.DefaultCardListen), true, nil
	default:
		parsed, err := netip.ParseAddrPort(value)
		if err != nil {
			return netip.AddrPort{}, false, fmt.Errorf("daemon: --card-listen takes an IP address and a port such as 0.0.0.0:7778 or [::]:7778, or %q", cardListenOff)
		}
		return parsed, true, nil
	}
}

// boundCard is a card listener that has been bound but is not serving yet.
type boundCard struct {
	listener net.Listener
	view     protocol.CardListener
}

// bindCardListener binds the address. It returns the state to show either way: listening with the
// listener, or not bound with a reason and no listener. One bind, no fallback, no retry.
//
// UNTESTED.
func bindCardListener(addr netip.AddrPort) (*boundCard, protocol.CardListener) {
	listener, err := net.Listen("tcp", addr.String())
	if err != nil {
		return nil, protocol.CardListener{State: protocol.CardListenerNotBound, Reason: bindReason(err)}
	}
	actual, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, protocol.CardListener{State: protocol.CardListenerNotBound, Reason: "the listener did not report a TCP address"}
	}
	bound := actual.AddrPort()
	if bound.Addr().Is4In6() {
		bound = netip.AddrPortFrom(bound.Addr().Unmap(), bound.Port())
	}
	advertised := advertiseAddress(bound)
	view := protocol.CardListener{
		State: protocol.CardListenerListening,
		Addr:  bound.String(),
		URL:   "http://" + advertised.String() + protocol.CardPath,
	}
	return &boundCard{listener: listener, view: view}, view
}

// bindReason is our sentence for a bind error. The operating system's text is not repeated.
func bindReason(err error) string {
	switch {
	case errors.Is(err, syscall.EADDRINUSE):
		return "the port is already in use"
	case errors.Is(err, syscall.EACCES):
		return "permission to use that port was denied"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "that address is not on this machine"
	}
	return "the address could not be bound"
}

// advertiseAddress is the address written into the card's own URL. A specific bound address is used
// as is. For a wildcard the owner's machine has no single answer, so it takes the first usable
// interface address, Tailscale (100.64.0.0/10) first, then a private LAN address, then any other
// non-loopback one, and falls back to loopback. The URL is information for readers; nothing dials it.
func advertiseAddress(bound netip.AddrPort) netip.AddrPort {
	if !bound.Addr().IsUnspecified() {
		return bound
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), bound.Port())
	}
	tailscale := netip.MustParsePrefix("100.64.0.0/10")
	var private, other netip.Addr
	for _, item := range addresses {
		prefix, err := netip.ParsePrefix(item.String())
		if err != nil {
			continue
		}
		address := prefix.Addr().Unmap()
		if !address.Is4() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsMulticast() || address.IsUnspecified() {
			continue
		}
		switch {
		case tailscale.Contains(address):
			return netip.AddrPortFrom(address, bound.Port())
		case address.IsPrivate() && !private.IsValid():
			private = address
		case !other.IsValid():
			other = address
		}
	}
	switch {
	case private.IsValid():
		return netip.AddrPortFrom(private, bound.Port())
	case other.IsValid():
		return netip.AddrPortFrom(other, bound.Port())
	}
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), bound.Port())
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
		// The state must not keep saying "listening" once nothing is: the view, the log and
		// `rtk daemon status` all read it.
		c.service.setListener(protocol.CardListener{State: protocol.CardListenerNotBound, Reason: "the listener stopped"})
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
