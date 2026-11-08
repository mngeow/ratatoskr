package daemon

// Model-drafted cards and the card operations a window may send.
//
// A draft is the owner's installed AI harness asked to propose a description and skills. It is
// strictly opt-in and strictly in steps, and the steps are separate operations so nothing runs by
// accident:
//
//	draft_prepare   builds the confirmation (the exact command, payload and size). EXECUTES NOTHING.
//	draft_run       starts the prepared command in the background; answers at once.
//	draft_status    polls; the answer is the draft in its current state.
//	draft_accept    writes the ready candidate to card.yaml (the old file is copied to card.yaml.bak first).
//	draft_cancel    stops a running draft or discards any other.
//
// A draft belongs to the window that prepared it (its owner key), one per window; a second prepare
// replaces the first. A window that closes cancels its running draft and discards the rest. At most
// maxRunningDrafts run at once. A prepared or ready draft expires draftIdle after its last operation.
// Nothing about a draft is stored anywhere but this process's memory.
//
// What reaches the model is the payload shown on the confirmation screen and nothing else: the host's
// name, the harnesses found with their versions, and found skills' names, descriptions and scope words.
// Never a skill body, a path, a user name, the environment, session text, or the current card. The
// model's answer is parsed strictly, then written to YAML and read back through card.Load, so a draft
// meets exactly the rules card.yaml meets, credential check included. A candidate that fails them is a
// failed draft with our own sentence; nothing the model wrote is echoed in an error.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No model has been called.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const (
	// draftIdle is how long a prepared or ready draft is kept after its last operation.
	draftIdle = 15 * time.Minute
	// maxRunningDrafts bounds drafts running at once in this daemon.
	maxRunningDrafts = 2
	// maxPayloadSkills bounds the found skills sent, so the payload stays well under the output cap.
	maxPayloadSkills = 100
	maxPayloadText   = 280 // runes

	draftBackupNotice  = "card.yaml is copied to card.yaml.bak first; if anything fails card.yaml is left as it is."
	draftRewriteNotice = "Accept rewrites card.yaml: the comments you wrote in it are not kept (card.yaml.bak keeps them)."

	// draftInstruction is the instruction inside the payload.
	draftInstruction = "You write the public profile of a software host that runs AI coding agents, for other hosts in a team. " +
		"From the host and skills below, reply with ONLY one JSON object, no prose and no code fence: " +
		"{\"description\": one sentence of at most 280 characters saying what the host is for, " +
		"\"skills\": [{\"id\": lowercase letters, digits, dot, dash or underscore, \"name\": short name, " +
		"\"description\": one sentence, \"tags\": [one to three short lowercase words]}]}. " +
		"List only skills from the input that are worth advertising, using each skill's dir_name as its id. " +
		"The profile is public: never include file paths, user names, keys, tokens or anything private."
)

// draftRunner is a configured command: a card.Drafter that can also say what it will run.
type draftRunner interface {
	card.Drafter
	Binary() string
	Argv() []string
}

// draft is one attempt. All fields are guarded by the manager's mutex.
type draft struct {
	view      protocol.CardDraft
	owner     any
	runner    draftRunner
	payload   []byte
	base      card.Owner
	candidate []byte // the YAML Accept would write
	cancel    context.CancelFunc
	last      time.Time
}

// draftManager holds the drafts of one daemon.
type draftManager struct {
	service   *cardService
	log       *logger
	newRunner func(card.CommandSpec) (draftRunner, error)
	getenv    func(string) string
	newID     func() string

	mu      sync.Mutex
	drafts  map[string]*draft // by id
	byOwner map[any]string
	running int
	bg      context.Context
}

func newDraftManager(service *cardService, log *logger) *draftManager {
	return &draftManager{
		service: service,
		log:     log,
		newRunner: func(spec card.CommandSpec) (draftRunner, error) {
			return card.NewCommandDrafter(spec)
		},
		getenv:  os.Getenv,
		newID:   randomID,
		drafts:  map[string]*draft{},
		byOwner: map[any]string{},
		bg:      context.Background(),
	}
}

func randomID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("d%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

// setBackground sets the context drafts run under: the daemon's, so shutdown stops them.
func (m *draftManager) setBackground(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bg = ctx
}

// Do performs one card operation for an owner key (the window).
//
// UNTESTED.
func (s *cardService) Do(op wire.CardOp, owner any) wire.Result {
	switch op.Op {
	case wire.CardOpGet:
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refresh(false)
		view := s.view()
		return wire.Result{Card: &view}

	case wire.CardOpReload:
		s.mu.Lock()
		s.refresh(true)
		view := s.view()
		s.mu.Unlock()
		s.scanInBackground(s.drafts.background())
		return wire.Result{Card: &view}

	case wire.CardOpDraftPrepare:
		return s.drafts.prepare(owner)
	case wire.CardOpDraftRun:
		return s.drafts.run(owner, op.DraftID)
	case wire.CardOpDraftStatus:
		return s.drafts.status(owner, op.DraftID)
	case wire.CardOpDraftCancel:
		return s.drafts.cancel(owner, op.DraftID)
	case wire.CardOpDraftAccept:
		return s.drafts.accept(owner, op.DraftID, op.BaseToken)
	}
	return refusal(wire.CodeBadRequest, "that card operation is not supported")
}

func refusal(code wire.Code, message string) wire.Result {
	return wire.Result{Error: &wire.Error{Code: code, Message: message}}
}

func (m *draftManager) background() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bg
}

// release drops an owner's draft: a running one is cancelled. Called when a window closes.
func (m *draftManager) release(owner any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id, ok := m.byOwner[owner]; ok {
		m.discardLocked(id)
	}
}

// discardLocked stops and forgets a draft.
func (m *draftManager) discardLocked(id string) {
	item, ok := m.drafts[id]
	if !ok {
		return
	}
	if item.cancel != nil {
		item.cancel()
	}
	delete(m.drafts, id)
	if m.byOwner[item.owner] == id {
		delete(m.byOwner, item.owner)
	}
}

// sweep expires prepared and ready drafts that have been idle.
func (m *draftManager) sweep() {
	now := m.service.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, item := range m.drafts {
		idle := item.view.State == protocol.CardDraftPrepared || item.view.State == protocol.CardDraftReady
		if idle && now.Sub(item.last) > draftIdle {
			expireLocked(item)
		}
	}
}

// expireLocked turns a draft into a cancelled one that says why and drops what it held.
func expireLocked(item *draft) {
	item.view.State = protocol.CardDraftCancelled
	item.view.Error = "expired"
	item.view.Diff = nil
	item.candidate = nil
	item.view.CandidateBytes = 0
}

// find returns the owner's draft by id.
func (m *draftManager) find(owner any, id string) (*draft, *wire.Result) {
	item, ok := m.drafts[id]
	if !ok || item.owner != owner {
		result := refusal(wire.CodeBadRequest, "there is no such draft; prepare a new one")
		return nil, &result
	}
	item.last = m.service.now()
	return item, nil
}

func (m *draftManager) answer(item *draft) wire.Result {
	view := item.view
	view.Stale = view.State == protocol.CardDraftReady && view.BaseToken != "" && view.BaseToken != m.service.currentToken()
	view.Argv = append([]string{}, view.Argv...)
	view.EnvNames = append([]string{}, view.EnvNames...)
	view.Caveats = append([]string{}, view.Caveats...)
	view.Diff = append([]protocol.CardDiffEntry(nil), view.Diff...)
	return wire.Result{Draft: &view}
}

// currentToken is the token of the file as last read.
func (s *cardService) currentToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

// prepare builds the confirmation. It executes nothing.
func (m *draftManager) prepare(owner any) wire.Result {
	s := m.service
	s.mu.Lock()
	s.refresh(false)
	loaded, token, inventory, host := s.loaded, s.token, s.inventory, s.hostName()
	s.mu.Unlock()

	switch {
	case !loaded.Decoded:
		return refusal(wire.CodeBadRequest, "card.yaml cannot be read yet; fix it first, then prepare a draft")
	case loaded.Owner.Drafting.Preset == "":
		return refusal(wire.CodeBadRequest, "no preset is set under drafting: in card.yaml (claude, codex or custom)")
	}

	command, err := buildDraftCommand(loaded.Owner.Drafting, m.getenv)
	if err != nil {
		return refusal(wire.CodeBadRequest, err.Error())
	}
	runner, err := m.newRunner(command.spec)
	if err != nil {
		return refusal(wire.CodeBadRequest, strings.TrimPrefix(err.Error(), "card: "))
	}

	payload, truncated, err := buildDraftPayload(host, inventory)
	if err != nil {
		return refusal(wire.CodeInternal, "the payload could not be built")
	}
	caveats := append([]string(nil), command.caveats...)
	if truncated {
		caveats = append(caveats, fmt.Sprintf("Only the first %d found skills are included in the payload.", maxPayloadSkills))
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if previous, ok := m.byOwner[owner]; ok {
		m.discardLocked(previous)
	}
	id := m.newID()
	item := &draft{
		owner:   owner,
		runner:  runner,
		payload: payload,
		base:    loaded.Owner,
		last:    s.now(),
		view: protocol.CardDraft{
			ID:             id,
			State:          protocol.CardDraftPrepared,
			Preset:         loaded.Owner.Drafting.Preset,
			Binary:         runner.Binary(),
			Argv:           runner.Argv(),
			Payload:        string(payload),
			PayloadBytes:   len(payload),
			EnvNames:       command.envNames,
			TimeoutSec:     protocol.CardDraftTimeoutSec,
			OutputCapBytes: protocol.CardDraftOutputCapBytes,
			Caveats:        caveats,
			BaseToken:      token,
		},
	}
	m.drafts[id] = item
	m.byOwner[owner] = id
	return m.answer(item)
}

// run starts a prepared draft.
func (m *draftManager) run(owner any, id string) wire.Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, bad := m.find(owner, id)
	if bad != nil {
		return *bad
	}
	if item.view.State != protocol.CardDraftPrepared {
		return refusal(wire.CodeBadRequest, "only a prepared draft can be run; prepare a new one")
	}
	if m.running >= maxRunningDrafts {
		return refusal(wire.CodeBusy, "other drafts are running; try again when one ends")
	}

	ctx, cancel := context.WithCancel(m.bg)
	item.cancel = cancel
	item.view.State = protocol.CardDraftRunning
	m.running++
	go m.execute(ctx, item)
	return m.answer(item)
}

// execute runs the command and records the outcome. It owns no lock while the command runs.
func (m *draftManager) execute(ctx context.Context, item *draft) {
	output, err := item.runner.Draft(ctx, item.payload)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.running--
	if item.cancel != nil {
		item.cancel()
		item.cancel = nil
	}
	if _, kept := m.drafts[item.view.ID]; !kept || item.view.State != protocol.CardDraftRunning {
		return // discarded or cancelled while it ran
	}

	if err != nil {
		if errors.Is(err, card.ErrDraftCancelled) {
			item.view.State = protocol.CardDraftCancelled
			return
		}
		item.view.State, item.view.Error = protocol.CardDraftFailed, draftFailure(err)
		m.log.logf("card draft failed: %s", item.view.Error)
		return
	}

	candidate, diff, problem := m.candidate(item.base, output)
	if problem != "" {
		item.view.State, item.view.Error = protocol.CardDraftFailed, problem
		m.log.logf("card draft failed: %s", problem)
		return
	}
	item.candidate = candidate
	item.view.State = protocol.CardDraftReady
	item.view.Error = ""
	item.view.Diff = diff
	item.view.CandidateBytes = len(candidate)
	item.view.BackupNotice = draftBackupNotice
	item.view.RewriteNotice = draftRewriteNotice
	item.last = m.service.now()
}

// draftFailure is our sentence for a runner error. The sentinels carry no output, so they are safe to show.
func draftFailure(err error) string {
	for _, known := range []error{
		card.ErrDraftTimeout, card.ErrDraftTooLarge, card.ErrDraftStart, card.ErrDraftFailed, card.ErrDraftEmpty,
	} {
		if errors.Is(err, known) {
			if known == card.ErrDraftTimeout {
				return fmt.Sprintf("the command ran past %d seconds and was stopped", protocol.CardDraftTimeoutSec)
			}
			return known.Error()
		}
	}
	return "the command could not be run on this platform"
}

// status answers the draft's current state.
func (m *draftManager) status(owner any, id string) wire.Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, bad := m.find(owner, id)
	if bad != nil {
		return *bad
	}
	return m.answer(item)
}

// cancel stops a running draft or discards a prepared or ready one. The answer is the draft, cancelled.
func (m *draftManager) cancel(owner any, id string) wire.Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, bad := m.find(owner, id)
	if bad != nil {
		return *bad
	}
	if item.cancel != nil {
		item.cancel()
	}
	item.view.State = protocol.CardDraftCancelled
	item.view.Error = ""
	item.view.Diff = nil
	item.candidate = nil
	item.view.CandidateBytes = 0
	result := m.answer(item)
	delete(m.drafts, id)
	if m.byOwner[owner] == id {
		delete(m.byOwner, owner)
	}
	return result
}

// accept writes the ready candidate to card.yaml.
//
// It is refused with card_conflict when the file changed since the draft was made or since the caller
// last looked; the draft then stays ready, for review or discard only (the Stale flag says so). A
// failure to back up or to write leaves card.yaml exactly as it was.
func (m *draftManager) accept(owner any, id, baseToken string) wire.Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	item, bad := m.find(owner, id)
	if bad != nil {
		return *bad
	}
	if item.view.State != protocol.CardDraftReady || item.candidate == nil {
		return refusal(wire.CodeBadRequest, "there is no ready draft to accept")
	}

	s := m.service
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refresh(false)
	if baseToken != s.token || item.view.BaseToken != s.token {
		view := s.view()
		result := wire.Result{
			Card:  &view,
			Error: &wire.Error{Code: wire.CodeCardConflict, Message: "card.yaml changed since this draft was made; prepare a new draft"},
		}
		return result
	}

	if err := s.writeAccepted(item.candidate); err != nil {
		s.log.logf("card draft: %v", err)
		return refusal(wire.CodeInternal, err.Error())
	}
	s.refresh(true)
	delete(m.drafts, id)
	if m.byOwner[owner] == id {
		delete(m.byOwner, owner)
	}
	view := s.view()
	return wire.Result{Card: &view}
}

// writeAccepted copies card.yaml to card.yaml.bak, then replaces card.yaml atomically. The caller holds
// the service mutex. If the copy fails, card.yaml is not touched.
func (s *cardService) writeAccepted(data []byte) error {
	dir := filepath.Dir(s.path)

	if existing, err := os.ReadFile(s.path); err == nil {
		if err := atomicWrite(filepath.Join(dir, cardBackupName), existing); err != nil {
			return errors.New("card.yaml.bak could not be written, so card.yaml was left as it is")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("card.yaml could not be read for the backup, so it was left as it is")
	}

	if err := atomicWrite(s.path, data); err != nil {
		return errors.New("card.yaml could not be written; it was left as it is")
	}
	return nil
}

// atomicWrite writes a file (mode 0600) by writing a complete temporary file in the same directory and
// renaming it over the target, so a reader sees the old file or the new one and never half of either.
func atomicWrite(path string, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	fail := func(err error) error {
		_ = temp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := temp.Write(data); err != nil {
		return fail(err)
	}
	if err := temp.Sync(); err != nil {
		return fail(err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// ---- payload and answer ----

type draftPayload struct {
	Instruction string         `json:"instruction"`
	Host        string         `json:"host"`
	Harnesses   []draftHarness `json:"harnesses"`
	Skills      []draftSkill   `json:"skills"`
}

type draftHarness struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type draftSkill struct {
	Harness     string `json:"harness"`
	Scope       string `json:"scope"`
	DirName     string `json:"dir_name"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// buildDraftPayload is the exact stdin text. A name or description that looks like a credential or a
// path is left out rather than sent. Labels (which carry location text) are never sent.
func buildDraftPayload(host string, inventory protocol.SkillInventory) (payload []byte, truncated bool, err error) {
	body := draftPayload{Instruction: draftInstruction, Host: host, Harnesses: []draftHarness{}, Skills: []draftSkill{}}

	seen := map[string]bool{}
	for _, found := range inventory.Found {
		if !seen[found.Harness] {
			seen[found.Harness] = true
			body.Harnesses = append(body.Harnesses, draftHarness{Name: found.Harness, Version: found.HarnessVersion})
		}
	}
	sort.Slice(body.Harnesses, func(i, j int) bool { return body.Harnesses[i].Name < body.Harnesses[j].Name })

	for _, found := range inventory.Found {
		if len(body.Skills) >= maxPayloadSkills {
			truncated = true
			break
		}
		skill := draftSkill{Harness: found.Harness, Scope: found.Scope, DirName: found.DirName}
		if card.PublicProblem(found.DirName) != "" {
			continue
		}
		if card.PublicProblem(found.Name) == "" {
			skill.Name = clip(found.Name, protocol.MaxCardSkillName)
		}
		if card.PublicProblem(found.Description) == "" {
			skill.Description = clip(found.Description, maxPayloadText)
		}
		body.Skills = append(body.Skills, skill)
	}

	payload, err = json.MarshalIndent(body, "", "  ")
	if err != nil {
		return nil, false, err
	}
	return append(payload, '\n'), truncated, nil
}

// clip shortens text to a number of runes.
func clip(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

// draftAnswer is the object the model is asked for.
type draftAnswer struct {
	Description string `json:"description"`
	Skills      []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
	} `json:"skills"`
}

// candidate turns the model's output into a new card.yaml and the diff against the base. problem is
// our sentence when the output is unusable; it never repeats the output.
func (m *draftManager) candidate(base card.Owner, output []byte) (yamlBytes []byte, diff []protocol.CardDiffEntry, problem string) {
	text := string(output)
	start, end := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}')
	if start < 0 || end <= start {
		return nil, nil, "the answer was not the JSON object that was asked for"
	}
	var answer draftAnswer
	decoder := json.NewDecoder(strings.NewReader(text[start : end+1]))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil {
		return nil, nil, "the answer was not the JSON object that was asked for"
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, "the answer was not the JSON object that was asked for"
	}

	next := base
	next.Description = answer.Description
	next.Skills = append([]card.OwnerSkill(nil), base.Skills...)
	index := map[string]int{}
	for position, skill := range next.Skills {
		index[skill.ID] = position
	}
	for _, generated := range answer.Skills {
		skill := card.OwnerSkill{ID: generated.ID, Name: generated.Name, Description: generated.Description, Tags: generated.Tags}
		if position, ok := index[skill.ID]; ok {
			next.Skills[position] = skill
			continue
		}
		index[skill.ID] = len(next.Skills)
		next.Skills = append(next.Skills, skill)
	}

	encoded, err := card.Marshal(next)
	if err != nil {
		return nil, nil, "the draft could not be written as YAML"
	}
	// The candidate meets exactly the rules card.yaml meets.
	loaded := card.Load(encoded, m.service.hostName(), nil)
	if !loaded.Valid {
		first := loaded.Problems[0]
		if first.Field != "" {
			return nil, nil, "the draft does not meet the card rules (" + first.Field + ": " + first.Message + ")"
		}
		return nil, nil, "the draft does not meet the card rules (" + first.Message + ")"
	}
	return encoded, diffOwners(base, next), ""
}

// diffOwners lists what a draft changes, every entry marked generated.
func diffOwners(old, next card.Owner) []protocol.CardDiffEntry {
	var diff []protocol.CardDiffEntry
	add := func(path, before, after string) {
		if before != after {
			diff = append(diff, protocol.CardDiffEntry{Path: path, Old: clip(before, maxPayloadText), New: clip(after, maxPayloadText), Generated: true})
		}
	}
	add("description", old.Description, next.Description)

	previous := map[string]card.OwnerSkill{}
	for _, skill := range old.Skills {
		previous[skill.ID] = skill
	}
	for position, skill := range next.Skills {
		path := fmt.Sprintf("skills[%d]", position)
		before, existed := previous[skill.ID]
		if !existed {
			add(path, "", skill.ID+": "+skill.Name+" - "+skill.Description+" ["+strings.Join(skill.Tags, ", ")+"]")
			continue
		}
		add(path+".name", before.Name, skill.Name)
		add(path+".description", before.Description, skill.Description)
		add(path+".tags", strings.Join(before.Tags, ", "), strings.Join(skill.Tags, ", "))
	}
	return diff
}
