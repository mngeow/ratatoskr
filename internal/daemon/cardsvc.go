package daemon

// The host card, owner's side: card.yaml, the card served from it, and the skills found on this machine.
//
// The card is a file the owner edits in their own editor (<state>/daemon/card.yaml, mode 0600). The
// daemon re-reads it when its modification time or size changes: on every request for the card and
// on a 2 s tick. A file that is half-saved or wrong keeps the LAST GOOD card served and reports the
// error with its line. There is no publish step, no history and no CLI.
//
// Editing a card is a LOCAL-HOST matter. The only operations (card_ops.go) are reached from the
// daemon's Unix socket by a window, after the same-user and not-from-a-harness checks every
// connection passes. The card listener (cardserver.go) can only read the bytes this service hands it;
// it can never reach an operation. That is the whole "no remote write path" guarantee, and the
// same-user limit is real: any process of the owner can call the socket or edit the file.
//
// The served card is built by card.Project from the decoded file, never by marshalling the file's own
// type, so the private drafting: section can never reach the wire.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/skillscan"
)

const (
	// cardYAMLName is the owner's card file inside the daemon directory; cardBackupName is the copy
	// Accept makes before it rewrites it.
	cardYAMLName   = "card.yaml"
	cardBackupName = "card.yaml.bak"
	// cardWatchEvery is how often card.yaml is checked besides on each request.
	cardWatchEvery = 2 * time.Second
	// scanBudget bounds one skill scan, including the harness version probes.
	scanBudget = 8 * time.Second // UNTESTED guess
)

// fileStamp is what says card.yaml has changed.
type fileStamp struct {
	modTime time.Time
	size    int64
}

// servedCard is the last good card.
type servedCard struct {
	card      protocol.HostCard
	canonical []byte // the card without its URL
	hash      string
	since     time.Time
}

// renderedCard is the served card for one listener URL.
type renderedCard struct {
	hash string
	url  string
	body []byte
	ok   bool
}

// cardService owns the card state.
type cardService struct {
	path string
	host func() string
	log  *logger
	now  func() time.Time

	scanning atomic.Bool
	drafts   *draftManager

	mu        sync.Mutex
	listener  protocol.CardListener
	inventory protocol.SkillInventory

	stamp      fileStamp
	haveStamp  bool
	data       []byte
	loaded     card.Loaded
	status     protocol.CardFileStatus
	token      string
	served     *servedCard
	rendered   renderedCard
	warnedMode bool
}

// newCardService creates the card service over <dir>/card.yaml. A missing file is written from the
// template (0600, never over an existing file), which does not validate until the description is
// filled, so nothing is served until the owner writes one.
//
// UNTESTED.
func newCardService(dir string, host func() string, log *logger) *cardService {
	service := &cardService{
		path:      filepath.Join(dir, cardYAMLName),
		host:      host,
		log:       log,
		now:       time.Now,
		listener:  protocol.CardListener{State: protocol.CardListenerOff},
		inventory: emptyInventory(),
	}
	service.drafts = newDraftManager(service, log)
	service.ensureTemplate(dir)
	service.mu.Lock()
	service.refresh(true)
	service.mu.Unlock()
	return service
}

// emptyInventory is the inventory before the first scan.
func emptyInventory() protocol.SkillInventory {
	return protocol.SkillInventory{
		Found:      []protocol.FoundSkill{},
		NotScanned: []string{"project skills", "plugin skills", "managed skills"},
	}
}

// hostName is the paired host's name, or "".
func (s *cardService) hostName() string {
	if s.host == nil {
		return ""
	}
	return s.host()
}

// ensureTemplate writes the commented template when card.yaml does not exist. It never replaces a file.
func (s *cardService) ensureTemplate(dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.log.logf("card.yaml: the directory could not be created")
		return
	}
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if !errors.Is(err, fs.ErrExist) {
			s.log.logf("card.yaml: the template could not be written")
		}
		return
	}
	_, writeErr := file.Write(card.Template(s.hostName()))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		s.log.logf("card.yaml: the template could not be written")
		return
	}
	s.log.logf("card.yaml: wrote a template at %s; fill in the description to serve the card", s.path)
}

// Path is where card.yaml lives.
func (s *cardService) Path() string { return s.path }

// setListener records where the listener serves, or that it is off or not bound.
func (s *cardService) setListener(listener protocol.CardListener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listener = listener
	s.rendered = renderedCard{}
}

// listenerView is the listener state for the status and the view.
func (s *cardService) listenerView() protocol.CardListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener
}

// evidenceByDir is the evidence the last scan gives a declared skill whose id equals the found skill's
// directory name. Evidence says a skill file was FOUND on this host, never that the skill works. The
// first match in scan order wins.
func (s *cardService) evidenceByDir() map[string]protocol.SkillEvidence {
	out := map[string]protocol.SkillEvidence{}
	for _, found := range s.inventory.Found {
		if _, done := out[found.DirName]; done {
			continue
		}
		out[found.DirName] = protocol.SkillEvidence{
			Harness: found.Harness, HarnessVersion: found.HarnessVersion, Scope: found.Scope,
			SHA256: found.SHA256, ScannedAt: s.inventory.ScannedAt,
		}
	}
	return out
}

// refresh reads card.yaml again if it changed (or always when force). The caller holds the mutex.
//
// A file that cannot be used leaves the last good served card in place and records why.
func (s *cardService) refresh(force bool) {
	info, err := os.Lstat(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		s.note(nil, fileStamp{}, protocol.CardFileMissing, 0, "card.yaml does not exist; the daemon writes the template when it starts")
		s.haveStamp = false
		return
	case err != nil:
		s.note(nil, fileStamp{}, protocol.CardFileInvalid, 0, "card.yaml could not be read")
		s.haveStamp = false
		return
	case !info.Mode().IsRegular():
		s.note(nil, fileStamp{}, protocol.CardFileInvalid, 0, "card.yaml must be a regular file, not a link or directory")
		s.haveStamp = false
		return
	case info.Size() > protocol.MaxCardFileBytes:
		s.note(nil, fileStamp{}, protocol.CardFileInvalid, 0, fmt.Sprintf("card.yaml is larger than %d KiB", protocol.MaxCardFileBytes>>10))
		s.haveStamp = false
		return
	}

	stamp := fileStamp{modTime: info.ModTime(), size: info.Size()}
	if !force && s.haveStamp && stamp == s.stamp {
		return
	}
	if info.Mode().Perm()&0o077 != 0 && !s.warnedMode {
		s.warnedMode = true
		s.log.logf("card.yaml can be read by other users of this machine; run chmod 600 on it")
	}

	data, err := os.ReadFile(s.path)
	if err != nil {
		s.note(nil, fileStamp{}, protocol.CardFileInvalid, 0, "card.yaml could not be read")
		s.haveStamp = false
		return
	}
	// The stamp was taken before the read, so a save in between shows as a change on the next tick.
	s.stamp, s.haveStamp = stamp, true
	s.note(data, stamp, "", 0, "")
}

// note records the outcome of reading the file (data nil = no usable bytes) and updates the served card.
func (s *cardService) note(data []byte, stamp fileStamp, state string, line int, message string) {
	s.data = data
	sum := sha256.Sum256(data)
	s.token = hex.EncodeToString(sum[:12])
	s.rendered = renderedCard{}

	if data == nil {
		s.loaded = card.Loaded{}
		s.status = protocol.CardFileStatus{State: state, Line: line, Message: message}
		return
	}

	loaded := card.Load(data, s.hostName(), s.evidenceByDir())
	s.loaded = loaded
	s.status = protocol.CardFileStatus{ModTime: protocol.FormatTime(stamp.modTime)}

	switch {
	case loaded.Valid:
		s.status.State = protocol.CardFileOK
		if loaded.Defaulted {
			s.status.State = protocol.CardFileDefaulted
			s.status.Message = "serving the default description; set description: in card.yaml"
		}
		s.adopt(loaded.Card)
	default:
		s.status.State = protocol.CardFileInvalid
		first := loaded.Problems[0]
		s.status.Line = first.Line
		s.status.Message = first.Message
		if first.Field != "" {
			s.status.Message = first.Field + ": " + first.Message
		}
		s.log.logf("card.yaml: line %d: %s (the last good card stays served)", first.Line, s.status.Message)
	}
}

// adopt makes a valid card the served one when it differs from the current served card.
func (s *cardService) adopt(projected protocol.HostCard) {
	canonical, err := card.Canonical(projected)
	if err != nil {
		return
	}
	hash := card.Hash(canonical)
	if s.served != nil && s.served.hash == hash {
		return
	}
	s.served = &servedCard{card: projected, canonical: canonical, hash: hash, since: s.now()}
}

// servedBytes is the card as the listener serves it: canonical bytes with the card's own URL filled in
// from the BOUND listener address (never from a request header), .
// ok is false when no card is served or the listener is not listening. The file is checked first, so
// an edit is visible to the next request.
//
// UNTESTED.
func (s *cardService) servedBytes() (body []byte, ok bool) {
	body, _ = s.serve()
	return body, body != nil
}

// The reasons a public 503 can give (contract section 15). They are fixed words: never the file's path,
// a line, a value or an error's text.
const (
	unavailableOwnerFileInvalid = "owner_file_invalid"
	unavailableOwnerFileMissing = "owner_file_missing"
	unavailableListenerURL      = "listener_url_unavailable"
	unavailableFailedValidation = "card_failed_validation"
)

// serve is servedBytes with the reason there is no card when there is none. The file is checked first.
func (s *cardService) serve() (body []byte, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(false)

	if s.served == nil {
		if s.status.State == protocol.CardFileMissing {
			return nil, unavailableOwnerFileMissing
		}
		return nil, unavailableOwnerFileInvalid
	}
	if s.listener.State != protocol.CardListenerListening || s.listener.URL == "" {
		return nil, unavailableListenerURL
	}
	if s.rendered.ok && s.rendered.hash == s.served.hash && s.rendered.url == s.listener.URL {
		return s.rendered.body, ""
	}

	canonical, err := card.Canonical(card.WithURL(s.served.card, s.listener.URL))
	if err != nil {
		return nil, unavailableFailedValidation
	}
	// The same rules the gateway applies: a card this build would reject is not served.
	if _, problems := card.Parse(canonical, card.Strict, true); len(problems) > 0 {
		s.log.logf("the card is not valid as served; it is not served")
		return nil, unavailableFailedValidation
	}
	s.rendered = renderedCard{hash: s.served.hash, url: s.listener.URL, body: canonical, ok: true}
	return s.rendered.body, ""
}

// view builds the answer. The caller holds the mutex.
func (s *cardService) view() protocol.CardFileView {
	view := protocol.CardFileView{
		Schema:   protocol.CardFileViewSchema,
		Host:     protocol.CardHost{Name: s.hostName(), Paired: s.hostName() != ""},
		Path:     s.path,
		Token:    s.token,
		File:     s.status,
		Listener: s.listener,
		Skills:   s.inventoryWithDeclared(),
		Drafting: s.draftingInfo(),
		Readable: protocol.CardReadableNotice,
	}
	if s.served != nil {
		view.Served = &protocol.CardServed{
			Card:    cloneCard(s.served.card),
			Since:   protocol.FormatTime(s.served.since),
			Current: s.status.State == protocol.CardFileOK,
		}
	}
	return view
}

// inventoryWithDeclared is the last scan with each found skill marked declared when card.yaml
// declares a skill whose id is its directory name.
func (s *cardService) inventoryWithDeclared() protocol.SkillInventory {
	inventory := s.inventory
	inventory.Found = make([]protocol.FoundSkill, len(s.inventory.Found))
	copy(inventory.Found, s.inventory.Found)
	if !s.loaded.Decoded {
		return inventory
	}
	for index, found := range inventory.Found {
		for _, skill := range s.loaded.Owner.Skills {
			if skill.ID == found.DirName {
				inventory.Found[index].Declared = true
				break
			}
		}
	}
	return inventory
}

// draftingInfo says whether a draft can be prepared.
func (s *cardService) draftingInfo() protocol.CardDraftingInfo {
	switch {
	case !s.loaded.Decoded:
		return protocol.CardDraftingInfo{Message: "fix card.yaml first: it cannot be read yet"}
	case s.loaded.Owner.Drafting.Preset == "":
		return protocol.CardDraftingInfo{Message: "no preset is set under drafting: in card.yaml (claude, codex or custom)"}
	}
	return protocol.CardDraftingInfo{
		Preset:    s.loaded.Owner.Drafting.Preset,
		Available: true,
		Message:   "g prepares a draft with your installed " + s.loaded.Owner.Drafting.Preset + "; nothing runs until you confirm the exact command",
	}
}

// watch re-reads card.yaml every cardWatchEvery until ctx ends, calling tick (if set) each time.
func (s *cardService) watch(ctx context.Context, tick func()) {
	ticker := time.NewTicker(cardWatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.mu.Lock()
			s.refresh(false)
			s.mu.Unlock()
			if tick != nil {
				tick()
			}
		}
	}
}

// cloneCard copies a card deeply enough that two owners never share a slice or map.
func cloneCard(c protocol.HostCard) protocol.HostCard {
	out := c
	out.Skills = make([]protocol.CardSkill, len(c.Skills))
	for index, skill := range c.Skills {
		skill.Tags = append([]string{}, skill.Tags...)
		out.Skills[index] = skill
	}
	out.SupportedInterfaces = append([]protocol.CardInterface{}, c.SupportedInterfaces...)
	out.DefaultInputModes = append([]string{}, c.DefaultInputModes...)
	out.DefaultOutputModes = append([]string{}, c.DefaultOutputModes...)
	out.Capabilities.Extensions = make([]protocol.CardExtension, len(c.Capabilities.Extensions))
	for index, extension := range c.Capabilities.Extensions {
		evidence := make(map[string]protocol.SkillEvidence, len(extension.Params.Skills))
		for key, value := range extension.Params.Skills {
			evidence[key] = value
		}
		extension.Params.Skills = evidence
		out.Capabilities.Extensions[index] = extension
	}
	return out
}

// scanInBackground runs a skill scan without delaying the caller, then reads card.yaml again because
// the evidence a declared skill carries comes from the scan. One scan runs at a time.
func (s *cardService) scanInBackground(ctx context.Context) {
	if !s.scanning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.scanning.Store(false)
		inventory := scanSkills(ctx)
		s.mu.Lock()
		s.inventory = inventory
		s.refresh(true)
		s.mu.Unlock()
	}()
}

// scanSkills lists the skill files found in the user-scope locations, with each harness's version.
//
// It reads files and never executes a skill. The harness version comes from the same function the
// rest of the daemon already uses; it runs only for a harness with something found.
func scanSkills(parent context.Context) protocol.SkillInventory {
	ctx, cancel := context.WithTimeout(parent, scanBudget)
	defer cancel()

	inventory := emptyInventory()
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		inventory.Notes = []protocol.SkillNote{{Label: "skills", Reason: "the home directory could not be found"}}
		return inventory
	}

	codexDir, codexLabel := filepath.Join(home, ".codex", "skills"), "~/.codex/skills"
	if value := os.Getenv("CODEX_HOME"); value != "" {
		codexDir, codexLabel = filepath.Join(value, "skills"), "$CODEX_HOME/skills"
	}

	result := skillscan.Scan([]skillscan.Root{
		{Harness: "claude", Scope: "user", Label: "~/.claude/skills", Dir: filepath.Join(home, ".claude", "skills"), LocationVerified: true},
		{Harness: "codex", Scope: "user", Label: "~/.agents/skills", Dir: filepath.Join(home, ".agents", "skills"), LocationVerified: true},
		// Undocumented: no official page names this location, so it is scanned but never marked verified,
		// and ".system" (which may hold skills Codex ships) is not listed as the owner's.
		{Harness: "codex", Scope: "user", Label: codexLabel, Dir: codexDir, LocationVerified: false, SkipNames: []string{".system"}},
	}, skillscan.DefaultLimits())

	versions := map[string]string{}
	for _, found := range result.Found {
		if _, done := versions[found.Harness]; !done {
			versions[found.Harness] = agent.Version(ctx, found.Harness)
		}
	}

	inventory.ScannedAt = protocol.FormatTime(time.Now())
	inventory.Truncated = result.Truncated
	inventory.RootsAbsent = result.RootsAbsent
	for _, found := range result.Found {
		inventory.Found = append(inventory.Found, protocol.FoundSkill{
			Harness: found.Harness, HarnessVersion: versions[found.Harness], Scope: found.Scope,
			Label: found.Label, DirName: found.DirName, Name: found.Name, Description: found.Description,
			SHA256: found.SHA256, LocationVerified: found.LocationVerified,
		})
	}
	for _, note := range result.Notes {
		inventory.Notes = append(inventory.Notes, protocol.SkillNote{Label: note.Label, Reason: note.Reason})
	}
	return inventory
}
