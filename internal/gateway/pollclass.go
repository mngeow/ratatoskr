package gateway

// What one card poll did, and how fresh the card it left behind is.
//
// The gateway polls each host's card endpoint on a schedule. This file turns the exchange into exactly
// one recorded outcome, and the recorded history into one freshness state. It is pure: no IO, no clock
// read (time is a parameter), no goroutines, and it imports nothing beyond the standard library and
// internal/protocol.
//
// The vocabulary is the point. A poll result says that the CARD ENDPOINT answered, or did not. It says
// nothing about a session, a process or a host being up: the heartbeat and the stream stay the
// authority on connection, and nothing here feeds them. That is why the reasons below never contain an
// error's own text - a transport error quotes an address - and why no sentence here uses a word like
// alive, dead, up, down or online.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No poll has been classified.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"mime"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

var (
	// ErrPinMismatch is the certificate not matching the registered fingerprint. The dialer returns it,
	// and the classifier matches it with errors.Is.
	ErrPinMismatch = errors.New("card: the certificate does not match the pinned fingerprint")

	// ErrAddressRefused is an address the poller may not dial, refused before any connection.
	ErrAddressRefused = errors.New("card: the address is not allowed")
)

// The fixed sentences a poll result carries. Each is ours, short, and safe: none can contain an
// address, a port, or any part of a Go error's text.
const (
	reasonCertificate = "the certificate does not match the registered fingerprint"
	reasonAddress     = "the address is not allowed"
	reasonTimeout     = "no answer in time"
	reasonNotMade     = "the connection was not made or was cut"
	reasonRedirect    = "the host answered with a redirect; redirects are never followed"
	reason304         = "the host answered 304 but there is no stored card to confirm"
	reasonOversize    = "the card is larger than the poll limit"
	reasonContentType = "the content type is not JSON"
	// reasonHTTPStatus is a prefix: the integer status is appended, which is safe because a number is not
	// an address.
	reasonHTTPStatus = "the host answered HTTP "
)

// PollInput is what one poll's transport produced.
type PollInput struct {
	// Err is a transport error from the request or from reading the body; nil when a response was read.
	Err error
	// Status is the HTTP status, or 0 when there was no response.
	Status int
	// ContentType is the Content-Type header as received.
	ContentType string
	// BodyOverflow says the body was larger than the poll cap and the caller stopped reading.
	BodyOverflow bool
	// SentIfNoneMatch says the request carried If-None-Match.
	SentIfNoneMatch bool
	// HaveStoredCard says a valid stored card exists for this target and pin.
	HaveStoredCard bool
}

// PollClass is one poll's classification.
type PollClass struct {
	// Outcome is one of the protocol's Poll* values.
	Outcome string
	// Reason is a fixed sentence, or "" when nothing needs saying.
	Reason string
	// ParseBody is true only for a 200 whose headers are acceptable: the caller must now parse the body
	// with the card package, and a parse failure then becomes protocol.PollInvalid.
	ParseBody bool
}

// ClassifyPoll turns one exchange into one outcome.
//
// The order is the brief's, and it matters: a certificate problem is not a generic refusal, and an
// address the poller refused was never a network failure. Nothing below inspects an error's text.
//
// UNTESTED.
func ClassifyPoll(in PollInput) PollClass {
	if in.Err != nil {
		return classifyError(in.Err)
	}

	if in.Status <= 0 {
		// No response and no error cannot happen; a negative status is nonsense. Both are a poll that
		// did not happen.
		return PollClass{Outcome: protocol.PollRefused, Reason: reasonNotMade}
	}

	switch {
	case in.Status >= 300 && in.Status <= 399:
		return PollClass{Outcome: protocol.PollRedirect, Reason: reasonRedirect}

	case in.Status == 304:
		if in.SentIfNoneMatch && in.HaveStoredCard {
			// Success, and nothing to parse: the stored card is the card.
			return PollClass{Outcome: protocol.PollNotModified}
		}
		return PollClass{Outcome: protocol.PollHTTPStatus, Reason: reason304}

	case in.Status == 200:
		if in.BodyOverflow {
			return PollClass{Outcome: protocol.PollOversize, Reason: reasonOversize}
		}
		if !jsonContentType(in.ContentType) {
			return PollClass{Outcome: protocol.PollInvalid, Reason: reasonContentType}
		}
		return PollClass{Outcome: protocol.PollOK, ParseBody: true}
	}

	return PollClass{Outcome: protocol.PollHTTPStatus, Reason: reasonHTTPStatus + strconv.Itoa(in.Status)}
}

// classifyError maps a transport error to one outcome, without quoting it.
func classifyError(err error) PollClass {
	if isCertificateProblem(err) {
		return PollClass{Outcome: protocol.PollCertificate, Reason: reasonCertificate}
	}
	if errors.Is(err, ErrAddressRefused) {
		return PollClass{Outcome: protocol.PollRefused, Reason: reasonAddress}
	}
	if isTimeout(err) {
		return PollClass{Outcome: protocol.PollTimeout, Reason: reasonTimeout}
	}
	// Connection refused, no route, a reset, a handshake failure that is not about the pin, a record
	// header error, EOF: all of them are a connection that was not made or was cut. The caller never sees
	// which, because saying so would quote an address.
	return PollClass{Outcome: protocol.PollRefused, Reason: reasonNotMade}
}

// isCertificateProblem reports whether an error is about the certificate rather than the connection.
//
// The pin mismatch is ours; the tls and x509 types are the standard library's, and a *url.Error
// wrapping any of them still matches through errors.As.
func isCertificateProblem(err error) bool {
	if errors.Is(err, ErrPinMismatch) {
		return true
	}
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var invalid x509.CertificateInvalidError
	return errors.As(err, &invalid)
}

// isTimeout reports whether an error is a deadline rather than a refusal.
//
// A *url.Error is a net.Error and delegates Timeout() to what it wraps, so one As covers both shapes.
// A cancelled context is deliberately not a timeout: it is a poll that was called off.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// jsonContentType reports whether a Content-Type is acceptable for a card.
//
// The card is JSON: application/json, or any application/*+json. A charset, when present, must say
// UTF-8, because the card parser reads UTF-8 and nothing else. Every other parameter is ignored.
func jsonContentType(header string) bool {
	mediaType, params, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}

	acceptable := mediaType == "application/json"
	if !acceptable {
		slash := strings.IndexByte(mediaType, '/')
		if slash > 0 && mediaType[:slash] == "application" && strings.HasSuffix(mediaType[slash+1:], "+json") {
			acceptable = true
		}
	}
	if !acceptable {
		return false
	}

	if charset, ok := params["charset"]; ok && !strings.EqualFold(charset, "utf-8") {
		return false
	}
	return true
}

// Succeeded reports whether an outcome means the endpoint answered usefully.
//
// Only a good card and a confirmed not-modified are success; a redirect, an HTTP status, a timeout, a
// refusal, an invalid body, an oversize body and a certificate problem are not.
//
// UNTESTED.
func Succeeded(outcome string) bool {
	return outcome == protocol.PollOK || outcome == protocol.PollNotModified
}

// Rejection reports whether an outcome means the endpoint answered with something unusable.
//
// An unknown or empty outcome is neither a success nor a rejection: it is an unrecognised record, and
// treating it as a refusal would invent a fact.
//
// UNTESTED.
func Rejection(outcome string) bool {
	switch outcome {
	case protocol.PollInvalid, protocol.PollOversize, protocol.PollCertificate:
		return true
	}
	return false
}

// OfflineAfter is how long without a good poll makes a card offline: fifteen minutes, or four poll
// intervals, whichever is longer.
//
// This is the one place the rule lives; a caller reads it from here rather than re-deriving it. It is
// exported so a test can pin the boundary and so the poller's single call site names the same function.
//
// The longer-than-fresh relation is why the states cannot overlap: for any positive interval,
// 2*interval < max(15m, 4*interval), so a card that is still fresh is never offline, and stale always
// has a non-empty band between the two.
//
// UNTESTED.
func OfflineAfter(interval time.Duration) time.Duration {
	const floor = 15 * time.Minute
	if interval <= 0 {
		interval = time.Minute
	}
	if four := 4 * interval; four > floor {
		return four
	}
	return floor
}

// Freshness is the card state the recorded history implies.
//
// A rejected latest answer outranks a recent good poll - that is what "the newest answer was refused"
// means - and no stored card, or no good poll ever, is none rather than fresh. The time comes from the
// caller; nothing here reads a clock.
//
// UNTESTED.
func Freshness(now, lastOK time.Time, haveCard bool, lastOutcome string, interval time.Duration) string {
	if interval <= 0 {
		interval = time.Minute
	}

	if Rejection(lastOutcome) {
		return protocol.CardRejected
	}
	if !haveCard || lastOK.IsZero() {
		return protocol.CardNone
	}

	age := now.Sub(lastOK)
	if age < 0 {
		// A last-good time in the future is a clock problem, not extra freshness.
		age = 0
	}

	if age <= 2*interval {
		return protocol.CardFresh
	}
	if age >= OfflineAfter(interval) {
		return protocol.CardOffline
	}
	return protocol.CardStale
}
