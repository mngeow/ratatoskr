package daemon

// The host card, owner's side: the working copy, the published card, its history and the skills found
// on this machine.
//
// Editing is a LOCAL-HOST operation. Every function here is reached only from the daemon's Unix socket
// (a window or the CLI), after the same-user and not-from-a-harness checks every connection passes. The
// card listener (cardserver.go) can only read the published bytes this service hands it; it can never
// reach an operation. That is the whole "no remote write path" guarantee, and the same-user limit is
// real: any process of the owner can call the socket.
//
// State is one file, <state>/daemon/card.json, 0600 in a 0700 directory, replaced atomically (a complete
// temporary file, then a rename), and only AFTER the new state has been validated: a failed publish
// leaves the previous file, and the last good card keeps being served. A file this build cannot read is
// moved aside, never overwritten, and the service starts from the defaults.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mngeow/ratatoskr/internal/agent"
	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/skillscan"
)

const (
	// cardFileName is the card state file inside the daemon directory.
	cardFileName = "card.json"
	// cardStateSchema is the file's own schema; a larger one is moved aside, not read.
	cardStateSchema = 1
	// scanBudget bounds one skill scan, including the harness version probes.
	scanBudget = 8 * time.Second // UNTESTED guess
	// maxCardFileBytes bounds what is read back: 20 revisions of a 64 KiB card plus the working copy.
	maxCardFileBytes = 4 << 20
)

// cardFile is what is written to disk.
type cardFile struct {
	Schema    int                     `json:"schema"`
	Published *protocol.CardRevision  `json:"published,omitempty"`
	Revisions []protocol.CardRevision `json:"revisions"`
	Working   cardWorkingFile         `json:"working"`
	Counter   int                     `json:"counter"`
}

// cardWorkingFile is the owner's working copy as stored.
type cardWorkingFile struct {
	Card    protocol.HostCard `json:"card"`
	Sources map[string]string `json:"sources,omitempty"`
	BasedOn int               `json:"based_on,omitempty"`
}

// cardService owns the card state.
type cardService struct {
	dir  string
	host func() string
	log  *logger
	now  func() time.Time

	mu        sync.Mutex
	state     cardFile
	inventory protocol.SkillInventory
	listener  protocol.CardListener

	// served caches the rendered published card for the current revision and listener URL.
	served servedCard
}

// servedCard is the published card as the listener serves it.
type servedCard struct {
	revision int
	url      string
	body     []byte
	etag     string
	ok       bool
}

// newCardService loads the card state from <dir>/card.json. dir is the daemon directory; nothing is
// created until the first write.
//
// UNTESTED.
func newCardService(dir string, host func() string, log *logger) *cardService {
	service := &cardService{
		dir:      dir,
		host:     host,
		log:      log,
		now:      time.Now,
		listener: protocol.CardListener{State: protocol.CardListenerOff},
	}
	service.state = service.load()
	service.inventory = emptyInventory()
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

// defaultWorking is the working copy of a host that has never edited: its name, nothing declared.
func (s *cardService) defaultWorking() cardWorkingFile {
	name := s.hostName()
	if name == "" {
		name = "host"
	}
	return cardWorkingFile{
		Card: protocol.HostCard{
			Name:        name,
			Description: "",
			SupportedInterfaces: []protocol.CardInterface{{
				ProtocolBinding: protocol.CardProtocolBinding,
				ProtocolVersion: protocol.CardProtocolVersion,
			}},
			Version: "1",
			Capabilities: protocol.CardCapabilities{
				Extensions: []protocol.CardExtension{{
					URI:    protocol.CardExtensionURI,
					Params: protocol.CardExtensionParams{Skills: map[string]protocol.SkillEvidence{}},
				}},
			},
			DefaultInputModes:  []string{"text/plain"},
			DefaultOutputModes: []string{"text/plain"},
			Skills:             []protocol.CardSkill{},
		},
		Sources: map[string]string{"name": protocol.CardSourceDetection},
	}
}

// load reads the state file. A missing file is the defaults; an unreadable one is moved aside (with a
// log line) and the defaults are used, so a corrupt file is never silently overwritten.
func (s *cardService) load() cardFile {
	path := filepath.Join(s.dir, cardFileName)

	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cardFile{Schema: cardStateSchema, Working: s.defaultWorking(), Revisions: []protocol.CardRevision{}}
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCardFileBytes {
		s.setAside(path, "the card file is not a regular file of a sane size")
		return cardFile{Schema: cardStateSchema, Working: s.defaultWorking(), Revisions: []protocol.CardRevision{}}
	}

	data, err := os.ReadFile(path)
	var state cardFile
	if err != nil || json.Unmarshal(data, &state) != nil || state.Schema != cardStateSchema {
		s.setAside(path, "the card file could not be read")
		return cardFile{Schema: cardStateSchema, Working: s.defaultWorking(), Revisions: []protocol.CardRevision{}}
	}
	if state.Revisions == nil {
		state.Revisions = []protocol.CardRevision{}
	}
	if state.Working.Card.Name == "" {
		state.Working = s.defaultWorking()
	}
	return state
}

// setAside moves an unusable card file out of the way, keeping it for the owner to inspect.
func (s *cardService) setAside(path, reason string) {
	aside := fmt.Sprintf("%s.unreadable-%d", path, s.now().Unix())
	if err := os.Rename(path, aside); err != nil {
		s.log.logf("%s, and it could not be moved aside; the card starts from its defaults and will not be saved over it", reason)
		return
	}
	s.log.logf("%s; it was moved to %s and the card starts from its defaults", reason, filepath.Base(aside))
}

// save writes the state atomically. The caller holds the mutex. The previous file stays until the
// complete new one has been written, flushed and renamed over it.
func (s *cardService) save() error {
	data, err := json.MarshalIndent(s.state, "", " ")
	if err != nil {
		return fmt.Errorf("the card could not be encoded: %w", err)
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("the card directory could not be created: %w", err)
	}

	temp, err := os.CreateTemp(s.dir, cardFileName+".tmp-*")
	if err != nil {
		return fmt.Errorf("the card could not be written: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() { _ = os.Remove(tempName) }

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		cleanup()
		return fmt.Errorf("the card could not be written: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		cleanup()
		return fmt.Errorf("the card could not be written: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		cleanup()
		return fmt.Errorf("the card could not be written: %w", err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("the card could not be written: %w", err)
	}
	if err := os.Rename(tempName, filepath.Join(s.dir, cardFileName)); err != nil {
		cleanup()
		return fmt.Errorf("the card could not be saved: %w", err)
	}
	return nil
}

// setListener records where the listener serves, or that it is off. The served bytes are rendered
// again on the next read.
func (s *cardService) setListener(listener protocol.CardListener) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.listener = listener
	s.served = servedCard{}
}

// listenerView is the listener state for the status and the view.
func (s *cardService) listenerView() protocol.CardListener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listener
}

// servedBytes is the published card as the listener serves it: canonical bytes with the card's own URL
// filled in from the BOUND listener address (never from a request header), and the ETag of exactly
// those bytes. ok is false when nothing is published or the listener is off.
//
// UNTESTED.
func (s *cardService) servedBytes() (body []byte, etag string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state.Published == nil || s.listener.State != protocol.CardListenerOn || s.listener.URL == "" {
		return nil, "", false
	}
	if s.served.ok && s.served.revision == s.state.Published.N && s.served.url == s.listener.URL {
		return s.served.body, s.served.etag, true
	}

	rendered := card.WithURL(s.state.Published.Card, s.listener.URL)
	canonical, err := card.Canonical(rendered)
	if err != nil {
		return nil, "", false
	}
	// The same rules the gateway applies: a card this build would reject is not served.
	if _, problems := card.Parse(canonical, card.Strict, true); len(problems) > 0 {
		s.log.logf("the published card is not valid as served; it is not served")
		return nil, "", false
	}

	s.served = servedCard{
		revision: s.state.Published.N, url: s.listener.URL,
		body: canonical, etag: card.ETag(canonical), ok: true,
	}
	return s.served.body, s.served.etag, true
}

// token identifies the working copy and the published revision together. It changes whenever either
// does, and is what an edit, publish or rollback must carry.
func (s *cardService) token() string {
	hash := sha256.New()
	working, _ := json.Marshal(s.state.Working)
	hash.Write(working)
	var published int
	if s.state.Published != nil {
		published = s.state.Published.N
	}
	fmt.Fprintf(hash, "|%d|%d", published, s.state.Counter)
	return hex.EncodeToString(hash.Sum(nil)[:12])
}

// problems validates the working copy: the card rules plus the public-content check.
func (s *cardService) problems() []protocol.CardProblem {
	return problemsFor(s.state.Working.Card)
}

// problemsFor validates any card the way the working copy is validated.
func problemsFor(c protocol.HostCard) []protocol.CardProblem {
	problems := card.Validate(c, false)
	problems = append(problems, card.CheckPublic(c)...)
	if problems == nil {
		problems = []protocol.CardProblem{}
	}
	return problems
}

// dirty reports whether the working copy differs from the published card in anything the owner can
// change (the version number and URL are the daemon's).
func (s *cardService) dirty() bool {
	if s.state.Published == nil {
		return true
	}
	return normalizedBytes(s.state.Working.Card) != normalizedBytes(s.state.Published.Card)
}

// normalizedBytes is a card's canonical form without the fields the daemon owns.
func normalizedBytes(c protocol.HostCard) string {
	c.Version = ""
	interfaces := make([]protocol.CardInterface, len(c.SupportedInterfaces))
	copy(interfaces, c.SupportedInterfaces)
	for index := range interfaces {
		interfaces[index].URL = ""
	}
	c.SupportedInterfaces = interfaces
	encoded, err := card.Canonical(c)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// view builds the answer. The caller holds the mutex.
func (s *cardService) view() protocol.CardView {
	view := protocol.CardView{
		Schema: protocol.CardViewSchema,
		Host:   protocol.CardHost{Name: s.hostName(), Paired: s.hostName() != ""},
		Token:  s.token(),
		Working: protocol.CardWorking{
			Card:     s.state.Working.Card,
			Dirty:    s.dirty(),
			BasedOn:  s.state.Working.BasedOn,
			Sources:  copySources(s.state.Working.Sources),
			Problems: s.problems(),
		},
		Revisions: make([]protocol.CardRevisionSummary, 0, len(s.state.Revisions)),
		Skills:    s.inventoryWithDeclared(),
		Listener:  s.listener,
		Drafting: protocol.CardDrafting{
			State:   protocol.DraftNotConfigured,
			Message: "no drafting provider is configured, so no model is ever called; the card is edited by hand",
		},
	}
	if s.state.Published != nil {
		published := *s.state.Published
		view.Published = &published
	}
	for index := len(s.state.Revisions) - 1; index >= 0; index-- {
		revision := s.state.Revisions[index]
		view.Revisions = append(view.Revisions, protocol.CardRevisionSummary{N: revision.N, At: revision.At, Caller: revision.Caller})
	}
	return view
}

// copySources copies a source map so a view never aliases the state.
func copySources(sources map[string]string) map[string]string {
	if len(sources) == 0 {
		return nil
	}
	out := make(map[string]string, len(sources))
	for key, value := range sources {
		out[key] = value
	}
	return out
}

// inventoryWithDeclared is the last scan with each found skill marked declared when the working copy
// carries evidence that matches its harness and hash.
func (s *cardService) inventoryWithDeclared() protocol.SkillInventory {
	inventory := s.inventory
	inventory.Found = make([]protocol.FoundSkill, len(s.inventory.Found))
	copy(inventory.Found, s.inventory.Found)

	evidence := s.workingEvidence()
	for index, found := range inventory.Found {
		for _, item := range evidence {
			if item.Harness == found.Harness && item.SHA256 == found.SHA256 && item.SHA256 != "" {
				inventory.Found[index].Declared = true
				break
			}
		}
	}
	return inventory
}

// workingEvidence is the evidence the working copy carries, in declared-skill order.
func (s *cardService) workingEvidence() []protocol.SkillEvidence {
	extensions := s.state.Working.Card.Capabilities.Extensions
	if len(extensions) != 1 {
		return nil
	}
	var out []protocol.SkillEvidence
	for _, skill := range s.state.Working.Card.Skills {
		if item, ok := extensions[0].Params.Skills[skill.ID]; ok {
			out = append(out, item)
		}
	}
	return out
}

// Do runs one card operation for a caller kind (window or cli), taken from the connection's role and
// never from the request. It returns the view in every case; the error, when set, is a refusal, and
// for a stale token the view is the current one.
//
// UNTESTED.
func (s *cardService) Do(ctx context.Context, op wire.CardOp, caller string) (protocol.CardView, *wire.Error) {
	switch op.Op {
	case wire.CardOpGet, wire.CardOpValidate:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.view(), nil

	case wire.CardOpHistory:
		s.mu.Lock()
		defer s.mu.Unlock()
		view := s.view()
		if op.Revision != 0 {
			revision, ok := s.findRevision(op.Revision)
			if !ok {
				return view, &wire.Error{Code: wire.CodeBadRequest, Message: "there is no such revision"}
			}
			view.Preview = &revision
		}
		return view, nil

	case wire.CardOpRescan:
		inventory := scanSkills(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.inventory = inventory
		return s.view(), nil

	case wire.CardOpEdit:
		s.mu.Lock()
		defer s.mu.Unlock()
		if conflict := s.checkToken(op.BaseToken); conflict != nil {
			return s.view(), conflict
		}
		if op.Edit == nil {
			return s.view(), &wire.Error{Code: wire.CodeBadRequest, Message: "that edit has nothing in it"}
		}
		s.applyEdit(*op.Edit)
		if err := s.save(); err != nil {
			s.log.logf("the card could not be saved: %v", err)
			return s.view(), &wire.Error{Code: wire.CodeInternal, Message: "the card could not be saved"}
		}
		return s.view(), nil

	case wire.CardOpPublish:
		s.mu.Lock()
		defer s.mu.Unlock()
		if conflict := s.checkToken(op.BaseToken); conflict != nil {
			return s.view(), conflict
		}
		return s.publishLocked(caller)

	case wire.CardOpRollback:
		s.mu.Lock()
		defer s.mu.Unlock()
		if conflict := s.checkToken(op.BaseToken); conflict != nil {
			return s.view(), conflict
		}
		revision, ok := s.findRevision(op.Revision)
		if !ok {
			return s.view(), &wire.Error{Code: wire.CodeBadRequest, Message: "there is no such revision"}
		}
		// A revision is judged by today's rules BEFORE it touches the working copy. One that fails them
		// (one saved under an older, looser rule) is refused with the first problem named, and the
		// working copy, the published card and the token are exactly as they were.
		if problems := problemsFor(revision.Card); len(problems) > 0 {
			return s.view(), &wire.Error{
				Code: wire.CodeBadRequest,
				Message: fmt.Sprintf("revision %d cannot be restored: it does not meet today's rules (%s: %s)",
					revision.N, problems[0].Field, problems[0].Message),
			}
		}
		s.state.Working = cardWorkingFile{
			Card:    cloneCard(revision.Card),
			Sources: copySources(revision.Sources),
			BasedOn: revision.N,
		}
		return s.publishLocked(caller)
	}
	return protocol.CardView{}, &wire.Error{Code: wire.CodeBadRequest, Message: "that card operation is not supported"}
}

// checkToken refuses a stale token with card_conflict. The caller holds the mutex.
func (s *cardService) checkToken(base string) *wire.Error {
	if base == s.token() {
		return nil
	}
	return &wire.Error{
		Code:    wire.CodeCardConflict,
		Message: "the card changed since you last looked; reload it and try again",
	}
}

// findRevision returns one stored revision by number.
func (s *cardService) findRevision(n int) (protocol.CardRevision, bool) {
	for _, revision := range s.state.Revisions {
		if revision.N == n {
			revision.Card = cloneCard(revision.Card)
			return revision, true
		}
	}
	return protocol.CardRevision{}, false
}

// applyEdit changes the working copy. The caller holds the mutex.
func (s *cardService) applyEdit(edit wire.CardEdit) {
	working := &s.state.Working
	if working.Sources == nil {
		working.Sources = map[string]string{}
	}

	if edit.Name != nil {
		working.Card.Name = *edit.Name
		working.Sources["name"] = protocol.CardSourceOwner
	}
	if edit.Description != nil {
		working.Card.Description = *edit.Description
		working.Sources["description"] = protocol.CardSourceOwner
	}
	if edit.Skills != nil {
		s.replaceSkills(*edit.Skills)
		working.Sources["skills"] = protocol.CardSourceOwner
	}
	s.state.Counter++
}

// replaceSkills sets the declared skills. An entry with a FoundLabel takes fresh evidence from the last
// scan; an entry without one keeps the evidence the working copy already holds for that ID; a new ID
// with neither gets none.
func (s *cardService) replaceSkills(edits []wire.CardSkillEdit) {
	working := &s.state.Working

	if len(working.Card.Capabilities.Extensions) != 1 {
		working.Card.Capabilities.Extensions = []protocol.CardExtension{{URI: protocol.CardExtensionURI}}
	}
	extension := working.Card.Capabilities.Extensions[0]
	previous := extension.Params.Skills

	skills := make([]protocol.CardSkill, 0, len(edits))
	evidence := make(map[string]protocol.SkillEvidence, len(edits))

	for _, edit := range edits {
		tags := edit.Tags
		if tags == nil {
			tags = []string{}
		}
		skills = append(skills, protocol.CardSkill{ID: edit.ID, Name: edit.Name, Description: edit.Description, Tags: tags})

		if edit.FoundLabel != "" {
			if found, ok := s.findFound(edit.FoundLabel); ok {
				evidence[edit.ID] = protocol.SkillEvidence{
					Harness: found.Harness, HarnessVersion: found.HarnessVersion, Scope: found.Scope,
					SHA256: found.SHA256, ScannedAt: s.inventory.ScannedAt,
				}
				continue
			}
		}
		if existing, ok := previous[edit.ID]; ok && edit.FoundLabel == "" {
			evidence[edit.ID] = existing
		}
	}

	extension.Params = protocol.CardExtensionParams{Skills: evidence}
	working.Card.Capabilities.Extensions = []protocol.CardExtension{extension}
	working.Card.Skills = skills
}

// findFound returns a found skill by its label.
func (s *cardService) findFound(label string) (protocol.FoundSkill, bool) {
	for _, found := range s.inventory.Found {
		if found.Label == label {
			return found, true
		}
	}
	return protocol.FoundSkill{}, false
}

// publishLocked validates the working copy and, only if it is clean, publishes it as a new revision.
// Nothing on disk changes unless validation passes. The caller holds the mutex.
func (s *cardService) publishLocked(caller string) (protocol.CardView, *wire.Error) {
	problems := s.problems()
	if len(problems) > 0 {
		return s.view(), &wire.Error{Code: wire.CodeBadRequest, Message: "the card has problems; fix them and publish again"}
	}

	next := s.state.Counter + 1
	published := cloneCard(s.state.Working.Card)
	published.Version = fmt.Sprint(next)
	for index := range published.SupportedInterfaces {
		published.SupportedInterfaces[index].URL = ""
	}

	// Parsing the exact bytes that would be served is the last gate: a card the gateway would reject
	// is never published.
	canonical, err := card.Canonical(card.WithURL(published, "http://127.0.0.1:1"+protocol.CardPath))
	if err != nil {
		return s.view(), &wire.Error{Code: wire.CodeInternal, Message: "the card could not be encoded"}
	}
	if _, parseProblems := card.Parse(canonical, card.Strict, true); len(parseProblems) > 0 {
		return s.view(), &wire.Error{Code: wire.CodeBadRequest, Message: "the card is not valid as it would be served"}
	}

	revision := protocol.CardRevision{
		N:       next,
		At:      protocol.FormatTime(s.now()),
		Caller:  caller,
		Card:    published,
		Sources: copySources(s.state.Working.Sources),
	}

	previous := s.state
	s.state.Counter = next
	s.state.Published = &revision
	s.state.Revisions = append(append([]protocol.CardRevision{}, s.state.Revisions...), revision)
	if over := len(s.state.Revisions) - protocol.MaxCardRevisions; over > 0 {
		s.state.Revisions = s.state.Revisions[over:]
	}
	s.state.Working.Card.Version = published.Version
	s.state.Working.BasedOn = next

	if err := s.save(); err != nil {
		// The disk was not changed, so neither is memory: the published card stays what it was.
		s.state = previous
		s.log.logf("the card could not be saved: %v", err)
		return s.view(), &wire.Error{Code: wire.CodeInternal, Message: "the card could not be saved"}
	}
	s.served = servedCard{}
	return s.view(), nil
}

// cloneCard copies a card deeply enough that two owners never share a slice or map.
func cloneCard(c protocol.HostCard) protocol.HostCard {
	data, err := json.Marshal(c)
	if err != nil {
		return c
	}
	var out protocol.HostCard
	if err := json.NewDecoder(bytes.NewReader(data)).Decode(&out); err != nil {
		return c
	}
	if out.Skills == nil {
		out.Skills = []protocol.CardSkill{}
	}
	return out
}

// scan runs the first inventory scan in the background so the first view is not delayed by it.
func (s *cardService) scanInBackground(ctx context.Context) {
	go func() {
		inventory := scanSkills(ctx)
		s.mu.Lock()
		s.inventory = inventory
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
