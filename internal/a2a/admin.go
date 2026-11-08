package a2a

// Enrolment and deletion logic for the owner's admin commands. It is the logic only: the CLI that
// parses arguments and prints (rtk gateway participant ...) is a thin layer over Admin, so a command
// shape can change without touching the rules here.
//
// Rules (contract section 5): there is no self-enrolment; the credential is printed once and only its
// hash is stored; revoking rejects the next call and ends open tasks with a reason; register fetches the
// service's card under the transport rules and validates it strictly before storing anything; a card
// that requires authentication is refused because this version sends no outbound credential.
//
// UNTESTED.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// CardFetcher fetches and strictly validates an A2A service's card from its registered URL. The gateway
// supplies one built on the dial-ruled HTTP client (no proxy, no redirects, size and time caps); this
// package never dials. It returns the validated card JSON and what the card declares about authentication.
// The card fetch itself carries no credential.
type CardFetcher interface {
	FetchCard(ctx context.Context, target Destination) (cardJSON string, auth CardAuth, err error)
}

// Destination is a validated place the gateway may call: the A2A URL and how to trust it. ID and Auth tell
// the outbound-client factory whose secret to attach (it reads the secret itself; it is never carried here).
type Destination struct {
	ID           string
	URL          string
	Fingerprint  string
	InsecureHTTP bool
	Auth         string // AuthBearer, AuthNone, or "" for the unauthenticated public card fetch
}

// Admin is the owner's enrolment surface.
type Admin struct {
	Store   Store
	Fetcher CardFetcher
	Now     func() time.Time
	// PurgeMessages removes the given gateway ledger messages (the dispatch and wake rows of deleted
	// tasks). The gateway supplies it; nil leaves the ledger alone.
	PurgeMessages func(ctx context.Context, messageIDs []string) error
}

func (a *Admin) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}
	return a.Now()
}

// Enrol creates a participant and returns its credential, shown once. The name is the owner's choice.
func (a *Admin) Enrol(ctx context.Context, name string) (credential string, err error) {
	if !ValidParticipantName(name) {
		return "", ErrBadName
	}
	credential, err = NewCredential()
	if err != nil {
		return "", err
	}
	err = a.Store.CreateParticipant(ctx, Participant{ID: name, CreatedAt: a.now()}, HashCredential(credential))
	if err != nil {
		return "", err
	}
	return credential, nil
}

// RegisterResult is what a successful register tells the owner.
type RegisterResult struct {
	// Secret is the new outbound bearer secret, shown ONCE; "" for a --no-auth registration.
	Secret string
	// Rotated says an earlier registration existed, so its secret has stopped working.
	Rotated bool
	// PlainHTTPLAN says the secret and the tasks will cross a LAN in the clear.
	PlainHTTPLAN bool
}

// Register stores where an enrolled participant's service lives and how the gateway authenticates to it.
// It fetches the (unauthenticated) card first and stores nothing if anything is wrong:
//   - the card must declare HTTP Bearer, and the gateway then mints a fresh 256-bit secret (running register
//     again ROTATES it: the old one stops working);
//   - a card that declares nothing is refused unless noAuth is given (the explicit demo mode: no header is
//     ever sent);
//   - a card that requires another scheme (OAuth2, OpenID Connect, mutual TLS, an API key) is refused;
//   - noAuth with a Bearer card is refused: sending nothing would simply be rejected.
//
// insecureHTTP is the owner's explicit opt-in for plain HTTP to a LAN address; loopback and tailnet addresses
// need none. An https URL needs a fingerprint and is for a LAN address only.
func (a *Admin) Register(ctx context.Context, name, rawURL, fingerprint string, insecureHTTP, noAuth bool) (RegisterResult, error) {
	p, found, err := a.Store.GetParticipant(ctx, name)
	if err != nil {
		return RegisterResult{}, err
	}
	if !found {
		return RegisterResult{}, ErrNotFound
	}
	if p.Revoked() {
		return RegisterResult{}, ErrRevoked
	}

	if err := CheckDestination(rawURL, fingerprint, insecureHTTP); err != nil {
		return RegisterResult{}, err
	}
	if a.Fetcher == nil {
		return RegisterResult{}, errors.New("a2a: no card fetcher is configured")
	}
	cardJSON, declared, err := a.Fetcher.FetchCard(ctx, Destination{ID: name, URL: rawURL, Fingerprint: fingerprint, InsecureHTTP: insecureHTTP})
	if err != nil {
		return RegisterResult{}, err
	}
	if cardJSON == "" {
		return RegisterResult{}, ErrCardInvalid
	}

	result := RegisterResult{Rotated: p.URL != "", PlainHTTPLAN: insecureHTTP && strings.HasPrefix(rawURL, "http://")}
	auth := AuthBearer
	switch {
	case declared == CardAuthUnsupported:
		return RegisterResult{}, ErrUnsupportedAuth
	case declared == CardAuthNone && !noAuth:
		return RegisterResult{}, ErrNoAuthDeclared
	case declared == CardAuthNone:
		auth = AuthNone
	case declared == CardAuthBearer && noAuth:
		return RegisterResult{}, ErrAuthMismatch
	}

	secret := ""
	if auth == AuthBearer {
		if secret, err = NewCredential(); err != nil {
			return RegisterResult{}, err
		}
		result.Secret = secret
	}
	if err := a.Store.SetRegistration(ctx, name, rawURL, fingerprint, insecureHTTP, cardJSON, auth, secret); err != nil {
		return RegisterResult{}, err
	}
	return result, nil
}

// Revoke disables a participant and ends its open tasks (as a target and as an owner) as FAILED with a
// reason. It reports how many tasks it ended.
func (a *Admin) Revoke(ctx context.Context, name string, finish func(context.Context, Task, string) error) (int, error) {
	changed, err := a.Store.Revoke(ctx, name, a.now())
	if err != nil {
		return 0, err
	}
	if !changed {
		return 0, ErrNotFound
	}
	ended := 0
	asTarget, err := a.Store.OpenTasksFor(ctx, Target{Kind: TargetService, ID: name})
	if err != nil {
		return 0, err
	}
	asOwner, err := a.Store.OpenTasksOwnedBy(ctx, Principal{Kind: KindParticipant, Name: name})
	if err != nil {
		return 0, err
	}
	for _, task := range append(asTarget, asOwner...) {
		if Final(task.State) {
			continue
		}
		if finish != nil {
			if err := finish(ctx, task, "participant revoked"); err != nil {
				return ended, err
			}
		}
		ended++
	}
	return ended, nil
}

// List returns every participant, revoked ones included.
func (a *Admin) List(ctx context.Context) ([]Participant, error) {
	return a.Store.ListParticipants(ctx)
}

// DeleteTasks removes tasks by the owner's choice. It is the only way a task leaves the store.
func (a *Admin) DeleteTasks(ctx context.Context, f DeleteFilter) (int, error) {
	set := 0
	if f.TaskID != "" {
		set++
	}
	if f.AllFinal {
		set++
	}
	if f.Participant != "" {
		set++
	}
	if set != 1 {
		return 0, errors.New("a2a: choose exactly one of a task id, all final tasks, or a participant")
	}
	messages, err := a.Store.MessagesFor(ctx, f)
	if err != nil {
		return 0, err
	}
	deleted, err := a.Store.DeleteTasks(ctx, f)
	if err != nil {
		return 0, err
	}
	if a.PurgeMessages != nil && len(messages) > 0 {
		if err := a.PurgeMessages(ctx, messages); err != nil {
			return deleted, fmt.Errorf("a2a: the tasks were deleted but their ledger messages could not be: %w", err)
		}
	}
	return deleted, nil
}

// CheckDestination applies the owner-facing transport rules to a registered URL before any dial. It is
// narrower than the card poller's rule on purpose and is kept consistent with it (cardtarget.go): A2A is
// loopback, tailnet and LAN only; plain http is accepted on loopback and the tailnet, and on a LAN address
// only with the explicit opt-in (which the gateway records here and does NOT pass on to the card-target
// parser, whose own rule is "LAN http needs no flag"); https is for a LAN address and ALWAYS needs a
// fingerprint, because that is the only https the dialler can pin. The dial-time deny list and class check
// still run in the gateway's dialer.
func CheckDestination(rawURL, fingerprint string, insecureHTTP bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return ErrBadURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ErrBadURL
	}
	addr, err := netip.ParseAddrPort(u.Host)
	if err != nil {
		// Only IP literals with a port: no names, so there is nothing to resolve or rebind.
		return fmt.Errorf("%w: use an IP address and a port", ErrBadURL)
	}
	switch classify(addr.Addr().Unmap()) {
	case classLoopback, classTailnet:
		if u.Scheme == "https" {
			return fmt.Errorf("%w: https is for a LAN address; use http on loopback or the tailnet", ErrBadURL)
		}
	case classLAN:
		if u.Scheme == "http" && !insecureHTTP {
			return ErrNeedsOptIn
		}
	default:
		return fmt.Errorf("%w: only loopback, tailnet and LAN addresses are allowed", ErrBadURL)
	}
	switch u.Scheme {
	case "http":
		if fingerprint != "" {
			return fmt.Errorf("%w: a fingerprint is for https", ErrBadURL)
		}
	case "https":
		if fingerprint == "" {
			return fmt.Errorf("%w: https needs --fingerprint", ErrBadURL)
		}
	}
	return nil
}

type addrClass int

const (
	classOther addrClass = iota
	classLoopback
	classTailnet
	classLAN
)

// classify mirrors the gateway's address classes (listen.go): the tailnet is checked BEFORE the LAN because
// Tailscale's IPv6 range is also a unique-local (private) address.
func classify(ip netip.Addr) addrClass {
	switch {
	case ip.IsLoopback():
		return classLoopback
	case tailscaleV4.Contains(ip) || tailscaleV6.Contains(ip):
		return classTailnet
	case ip.IsPrivate():
		return classLAN
	}
	return classOther
}

var (
	tailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	tailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// NewCredential mints a 256-bit bearer credential.
func NewCredential() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("a2a: mint credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// HashCredential is the stored form of a credential.
func HashCredential(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}

// NormalizeName lowercases and trims an owner-typed participant name.
func NormalizeName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }
