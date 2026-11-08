package gateway

// The card poller: for the life of the gateway, every paired host with a registered card target is
// polled on a fixed schedule, whether or not anything is watching.
//
// What a poll means is fixed by the contract (section 4.5) and enforced here and in the words the API
// uses: a poll says the CARD ENDPOINT answered. It says nothing about sessions, and it never feeds
// the heartbeat or the stream, which stay the authority on connection (liveness.go is untouched).
//
// The poller reads its targets from the database every second, not from memory, because the admin
// commands (`card-target set` and `clear`, `unpair`) run in other processes. It holds no transaction
// across a network call, runs at most one poll per host and a bounded number at once, and stops
// (cancel and wait) before the store closes.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No poll has been made.

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// DefaultCardPollInterval is the poll period when the flag is not given. It is a default, not a
	// choice the user made.
	DefaultCardPollInterval = 5 * time.Second // user decision, contract section 16
	// MinCardPollInterval and MaxCardPollInterval bound the flag. Below two seconds the timeout rule
	// leaves no room.
	MinCardPollInterval = 2 * time.Second
	MaxCardPollInterval = time.Hour

	// cardScanEvery is how often the coordinator re-reads the targets from the database. It is short
	// enough that a 5 s schedule does not drift to 6 s.
	cardScanEvery = 250 * time.Millisecond
	// cardPollConcurrency bounds polls in flight across hosts.
	cardPollConcurrency = 8
	// cardScanTimeout bounds one read of the targets.
	cardScanTimeout = 5 * time.Second
	// maxReasonRunes bounds a stored reason.
	maxReasonRunes = 200
)

// ClampCardPollInterval puts a flag value inside the allowed range; 0 means the default.
func ClampCardPollInterval(interval time.Duration) time.Duration {
	switch {
	case interval == 0:
		return DefaultCardPollInterval
	case interval < MinCardPollInterval:
		return MinCardPollInterval
	case interval > MaxCardPollInterval:
		return MaxCardPollInterval
	}
	return interval
}

// nextDue is when the poll after the one due at `due` is due. It is fixed-rate: one interval after the
// previous due time, not after `now`, so a late scan does not accumulate. A host more than one interval
// behind starts over from now.
func nextDue(due, now time.Time, interval time.Duration) time.Time {
	if now.Sub(due) > interval {
		return now.Add(interval)
	}
	return due.Add(interval)
}

// hostSchedule is one host's in-memory poll state.
type hostSchedule struct {
	// updatedAt is the target's updated_at this schedule was made for; a different value means the
	// owner changed the target and the host starts over.
	updatedAt time.Time
	next      time.Time
	inFlight  bool
}

// cardPoller owns the schedule and the goroutines.
type cardPoller struct {
	store    *Store
	interval time.Duration
	now      func() time.Time
	// logf reports a problem once; it never receives a card body, a header or a credential.
	logf func(format string, args ...any)
	// onChange is called after a poll result was stored, so the graph recomputes and wakes readers.
	onChange func()

	mu    sync.Mutex
	hosts map[string]*hostSchedule
	// polled is the set of hosts polled since this gateway started. A stored card is "restored" until
	// its host is in it.
	polled map[string]bool
	// reported holds problems already logged, so a persistent one is logged once.
	reported map[string]bool
	// skipped counts polls that came due while the host's previous poll was still running. They are
	// dropped, not queued.
	skipped map[string]int

	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{}
}

// newCardPoller builds a poller; start runs it.
func newCardPoller(store *Store, interval time.Duration, now func() time.Time, logf func(string, ...any), onChange func()) *cardPoller {
	if now == nil {
		now = time.Now
	}
	return &cardPoller{
		store:    store,
		interval: ClampCardPollInterval(interval),
		now:      now,
		logf:     logf,
		onChange: onChange,
		hosts:    make(map[string]*hostSchedule, 4),
		polled:   make(map[string]bool, 4),
		reported: make(map[string]bool, 4),
		skipped:  make(map[string]int, 4),
		slots:    make(chan struct{}, cardPollConcurrency),
	}
}

// start runs the coordinator until stop. It is safe to call once.
//
// UNTESTED.
func (p *cardPoller) start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	p.cancel = cancel

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.run(ctx)
	}()
}

// stop cancels the coordinator and every poll in flight and waits for them. Idempotent. It must run
// before the store closes.
//
// UNTESTED.
func (p *cardPoller) stop() {
	if p == nil || p.cancel == nil {
		return
	}
	p.cancel()
	p.wg.Wait()
}

// polledSinceBoot reports whether a host has been polled since this gateway started.
func (p *cardPoller) polledSinceBoot(host string) bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.polled[host]
}

// pollInterval is the period the freshness rule is measured against.
func (p *cardPoller) pollInterval() time.Duration {
	if p == nil {
		return DefaultCardPollInterval
	}
	return p.interval
}

// run is the coordinator loop.
func (p *cardPoller) run(ctx context.Context) {
	ticker := time.NewTicker(cardScanEvery)
	defer ticker.Stop()

	for {
		p.scan(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scan reads the targets and starts every poll that is due.
func (p *cardPoller) scan(ctx context.Context) {
	readCtx, cancel := context.WithTimeout(ctx, cardScanTimeout)
	defer cancel()

	targets, unreadable, err := p.store.CardTargets(readCtx)
	if err != nil {
		p.logOnce("targets", "the card targets could not be read; polling continues with the next scan")
		return
	}
	rows, err := p.store.CardRows(readCtx)
	if err != nil {
		p.logOnce("rows", "the stored cards could not be read; polling continues with the next scan")
		return
	}
	for _, host := range unreadable {
		p.logOnce("target:"+host, "a card target could not be read and is skipped: "+cleanLog(host))
	}

	now := p.now()
	present := make(map[string]bool, len(targets))

	p.mu.Lock()
	for _, target := range targets {
		present[target.Host] = true

		schedule := p.hosts[target.Host]
		if schedule == nil || !schedule.updatedAt.Equal(target.UpdatedAt) {
			// New or changed: start over, with a start jitter of up to a tenth of the interval so
			// every host is not hit in the same second.
			inFlight := schedule != nil && schedule.inFlight
			schedule = &hostSchedule{
				updatedAt: target.UpdatedAt,
				next:      now.Add(time.Duration(rand.Int64N(int64(p.interval/10) + 1))),
				inFlight:  inFlight,
			}
			p.hosts[target.Host] = schedule
			// A changed target is a different endpoint: it has not been polled since boot.
			delete(p.polled, target.Host)
		}

		row := rows[target.Host]
		// A certificate mismatch stops polling until the owner registers the target again, which
		// moves updated_at past the last attempt.
		paused := row.LastResult == protocol.PollCertificate && !row.LastAttemptAt.Before(target.UpdatedAt)

		if paused || now.Before(schedule.next) {
			continue
		}
		// Fixed rate: the next poll is one interval after the one that was due, so a late scan does
		// not accumulate. A host more than one interval behind starts over from now.
		schedule.next = nextDue(schedule.next, now, p.interval)
		if schedule.inFlight {
			p.skipped[target.Host]++
			continue
		}
		schedule.inFlight = true

		p.wg.Add(1)
		go func(target CardTarget) {
			defer p.wg.Done()
			p.pollOne(ctx, target)
		}(target)
	}
	for host := range p.hosts {
		if !present[host] {
			// The target was cleared or the host was unpaired.
			delete(p.hosts, host)
			delete(p.polled, host)
			delete(p.skipped, host)
		}
	}
	p.mu.Unlock()
}

// pollOne makes one poll and stores its result.
func (p *cardPoller) pollOne(ctx context.Context, target CardTarget) {
	slot := p.slots
	select {
	case slot <- struct{}{}:
		defer func() { <-slot }()
	case <-ctx.Done():
		p.finish(target.Host)
		return
	}

	started := p.now()
	record := p.exchange(ctx, target)
	record.Host = target.Host
	record.TargetUpdatedAt = target.UpdatedAt
	record.At = started
	record.Duration = p.now().Sub(started)

	if ctx.Err() != nil {
		// The gateway is stopping; a half-finished poll is not recorded.
		p.finish(target.Host)
		return
	}

	storeCtx, cancel := context.WithTimeout(ctx, cardScanTimeout)
	applied, changed, err := p.store.RecordPollChange(storeCtx, record)
	cancel()

	if err != nil {
		p.logOnce("record:"+target.Host, "a poll result could not be stored for "+cleanLog(target.Host))
	}
	p.mu.Lock()
	if applied {
		p.polled[target.Host] = true
	}
	p.mu.Unlock()
	p.finish(target.Host)

	if applied && changed && p.onChange != nil {
		p.onChange()
	}
}

// finish clears a host's in-flight flag.
func (p *cardPoller) finish(host string) {
	p.mu.Lock()
	if schedule := p.hosts[host]; schedule != nil {
		schedule.inFlight = false
	}
	p.mu.Unlock()
}

// exchange does the HTTP request and turns what happened into a PollRecord (without host and times).
//
// The request carries no credential: no Authorization, no cookie, no host token, and no conditional
// header: every poll asks for the full card (contract section 16).
func (p *cardPoller) exchange(ctx context.Context, target CardTarget) PollRecord {
	client, err := newCardClient(target, p.interval)
	if err != nil {
		// An unusable pin is a certificate-class failure; polling for the host pauses.
		return PollRecord{Outcome: protocol.PollCertificate, Reason: "the registered fingerprint is not usable"}
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL(), nil)
	if err != nil {
		return PollRecord{Outcome: protocol.PollRefused, Reason: "the card URL could not be built"}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", cardUserAgent)

	response, err := client.Do(request)
	if err != nil {
		class := ClassifyPoll(PollInput{Err: err})
		return PollRecord{Outcome: class.Outcome, Reason: class.Reason}
	}
	defer func() {
		_ = response.Body.Close()
	}()

	input := PollInput{
		Status:      response.StatusCode,
		ContentType: response.Header.Get("Content-Type"),
	}

	var body []byte
	if response.StatusCode == http.StatusOK {
		body, err = io.ReadAll(io.LimitReader(response.Body, protocol.MaxCardPollBytes+1))
		if err != nil {
			class := ClassifyPoll(PollInput{Err: err})
			return PollRecord{Outcome: class.Outcome, Reason: class.Reason}
		}
		input.BodyOverflow = len(body) > protocol.MaxCardPollBytes
	}

	class := ClassifyPoll(input)
	record := PollRecord{Outcome: class.Outcome, Status: response.StatusCode, Reason: class.Reason}
	if !class.ParseBody {
		return record
	}

	parsed, problems := card.Parse(body, card.Polled, true)
	if len(problems) > 0 {
		record.Outcome = protocol.PollInvalid
		record.Reason = problemReason(problems[0])
		return record
	}
	canonical, err := card.Canonical(parsed.Card)
	if err != nil {
		record.Outcome = protocol.PollInvalid
		record.Reason = "the card could not be re-rendered"
		return record
	}

	record.CardJSON = string(canonical)
	record.Revision = parsed.Card.Version
	record.Hash = card.Hash(canonical)
	record.UnknownFields = parsed.UnknownFields
	return record
}

// problemReason renders one card problem as a stored reason: the field and our own sentence, never a
// value. The field comes from card.Parse, which has already cleaned it.
func problemReason(problem protocol.CardProblem) string {
	if problem.Field == "" {
		return problem.Message
	}
	return fmt.Sprintf("%s: %s", problem.Field, problem.Message)
}

// cleanLog makes a host name safe for one log line.
func cleanLog(value string) string {
	var builder strings.Builder
	count := 0
	for _, r := range value {
		if r == utf8.RuneError || r < 0x20 || r == 0x7f {
			continue
		}
		builder.WriteRune(r)
		count++
		if count >= maxReasonRunes {
			break
		}
	}
	return builder.String()
}

// logOnce reports a message the first time its key is seen.
func (p *cardPoller) logOnce(key, message string) {
	if p == nil || p.logf == nil {
		return
	}
	p.mu.Lock()
	seen := p.reported[key]
	p.reported[key] = true
	p.mu.Unlock()
	if !seen {
		p.logf("card poller: %s", message)
	}
}
