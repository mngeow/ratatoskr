package tui

// UNTESTED under GR-3. Working-copy writes go only to the owning local daemon.
import (
	tea "charm.land/bubbletea/v2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
	"github.com/mngeow/ratatoskr/internal/windowclient"
	"strings"
	"unicode/utf8"
)

type cardEditor struct {
	view                                                 protocol.CardView
	have, open, pending, dirty, blocked                  bool
	reloadDiscard, uncertain                             bool
	previewReady                                         bool
	baseToken, name, description, mode, problem, confirm string
	skills                                               []wire.CardSkillEdit
	field, caret, choice, revision, scroll               int
	generation, editSeq                                  uint64
}
type cardMsg struct {
	generation, editSeq uint64
	view                protocol.CardView
	op                  string
	err                 error
}

var cardEditorActions = []string{"Save working copy", "Validate", "Diff", "Publish", "History / rollback", "Rescan found skills", "Add declared skill", "Remove selected skill", "Found skills", "Draft (not configured)", "Reload current copy", "Close editor"}

func (m *Model) loadCard() tea.Cmd {
	e := &m.graph.editor
	if m.daemon == nil || m.daemon.card == nil || e.pending {
		return nil
	}
	e.pending = true
	e.generation++
	generation, seq := e.generation, e.editSeq
	ask := m.daemon.card
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), networkRequestBudget)
		defer cancel()
		view, err := ask(ctx)
		return cardMsg{generation, seq, view, "get", err}
	}
}
func (e *cardEditor) adopt(view protocol.CardView) {
	e.view = view
	e.have = true
	e.baseToken = view.Token
	e.name = view.Working.Card.Name
	e.description = view.Working.Card.Description
	e.skills = nil
	for _, skill := range view.Working.Card.Skills {
		s := wire.CardSkillEdit{ID: skill.ID, Name: skill.Name, Description: skill.Description, Tags: append([]string(nil), skill.Tags...)}
		// Omitted FoundLabel preserves the daemon's existing evidence for this ID.
		e.skills = append(e.skills, s)
	}
	e.dirty = false
	e.blocked = false
	e.problem = ""
	e.uncertain = false
	e.caret = 0
	e.field = min(e.field, e.fieldCount()+len(cardEditorActions)-1)
}
func (m *Model) applyCard(msg cardMsg) {
	e := &m.graph.editor
	if msg.generation != e.generation {
		return
	}
	e.pending = false
	if msg.err != nil {
		e.problem = cleanUISpan(msg.err.Error())
		var opErr *windowclient.CardOpError
		if errors.As(msg.err, &opErr) && (opErr.Code == wire.CodeCardConflict || opErr.Uncertain) {
			e.blocked = true
			e.uncertain = opErr.Uncertain
			e.mode = "conflict"
		}
		if msg.view.Schema == protocol.CardViewSchema {
			e.view = msg.view
			e.have = true
		}
		return
	}
	if msg.view.Schema != protocol.CardViewSchema {
		e.problem = "local card schema unsupported"
		return
	}
	if msg.op == wire.CardOpHistory && msg.view.Preview != nil && msg.view.Preview.N == e.revision {
		e.previewReady = true
	}
	if msg.op == "get" {
		e.uncertain = false
	}
	if e.reloadDiscard {
		e.reloadDiscard = false
		e.adopt(msg.view)
		return
	}
	if e.dirty && msg.editSeq != e.editSeq {
		e.view = msg.view
		e.have = true
		if msg.op != "get" {
			e.baseToken = msg.view.Token
		} else if e.baseToken != msg.view.Token {
			e.blocked = true
			e.mode = "conflict"
			e.problem = "local card changed; hand edits kept"
		}
		return
	}
	if msg.op != wire.CardOpEdit && msg.op != wire.CardOpPublish && msg.op != wire.CardOpRollback && e.dirty {
		e.view = msg.view
		e.have = true
		if e.baseToken != msg.view.Token {
			e.blocked = true
			e.mode = "conflict"
			e.problem = "local card changed; hand edits kept"
		}
		return
	}
	e.adopt(msg.view)
}
func (m *Model) cardOperation(op string) tea.Cmd {
	e := &m.graph.editor
	if !e.have {
		e.problem = "load the local card before changing it"
		return nil
	}
	if op == wire.CardOpEdit || op == wire.CardOpPublish {
		if op == wire.CardOpPublish && strings.TrimSpace(e.description) == "" {
			e.problem = "A card description is required for publishing; a working copy can be saved without it."
			return nil
		}
		for _, skill := range e.skills {
			if strings.TrimSpace(skill.Description) == "" {
				e.problem = "A skill needs a description you wrote before it can be declared."
				return nil
			}
			if !hasCardTag(skill.Tags) {
				e.problem = "A declared skill needs at least one tag; edit its Tags field."
				return nil
			}
		}
	}
	if e.pending || m.daemon == nil || m.daemon.cardOp == nil {
		return nil
	}
	if e.blocked {
		e.problem = "reload/review current card before another operation"
		e.mode = "conflict"
		return nil
	}
	request := wire.CardOp{Op: op, BaseToken: e.baseToken, Revision: e.revision}
	if op == wire.CardOpEdit {
		name, description := e.name, e.description
		skills := append([]wire.CardSkillEdit(nil), e.skills...)
		for i := range skills {
			skills[i].Tags = append([]string(nil), skills[i].Tags...)
		}
		request.Edit = &wire.CardEdit{Name: &name, Description: &description, Skills: &skills}
	}
	e.pending = true
	e.generation++
	generation, seq := e.generation, e.editSeq
	call := m.daemon.cardOp
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), networkRequestBudget)
		defer cancel()
		view, err := call(ctx, request)
		return cardMsg{generation, seq, view, op, err}
	}
}
func (e *cardEditor) fieldCount() int { return 2 + len(e.skills)*4 }
func hasCardTag(tags []string) bool {
	for _, tag := range tags {
		if strings.TrimSpace(tag) != "" {
			return true
		}
	}
	return false
}
func (e *cardEditor) fieldValue(index int) (string, string, int) {
	if index == 0 {
		return "Name", e.name, protocol.MaxCardName
	}
	if index == 1 {
		return "Description (needed to publish)", e.description, protocol.MaxCardDescription
	}
	index -= 2
	if index < 0 || index/4 >= len(e.skills) {
		return "", "", 0
	}
	skill := e.skills[index/4]
	switch index % 4 {
	case 0:
		return fmt.Sprintf("Skill%d ID", index/4+1), skill.ID, protocol.MaxCardSkillID
	case 1:
		return "Skill name", skill.Name, protocol.MaxCardSkillName
	case 2:
		return "Owner description (needed to declare)", skill.Description, protocol.MaxCardSkillDescription
	default:
		return "Tags (comma-separated)", strings.Join(skill.Tags, ","), protocol.MaxCardSkillTags * (protocol.MaxCardSkillTag + 1)
	}
}
func (e *cardEditor) setField(index int, value string) {
	switch index {
	case 0:
		e.name = value
	case 1:
		e.description = value
	default:
		index -= 2
		if index < 0 || index/4 >= len(e.skills) {
			return
		}
		skill := &e.skills[index/4]
		switch index % 4 {
		case 0:
			skill.ID = value
		case 1:
			skill.Name = value
		case 2:
			skill.Description = value
		case 3:
			skill.Tags = strings.Split(value, ",")
			for i := range skill.Tags {
				skill.Tags[i] = strings.TrimSpace(skill.Tags[i])
			}
			if strings.TrimSpace(value) == "" {
				skill.Tags = nil
			}
			if !hasCardTag(skill.Tags) {
				e.problem = "A declared skill needs at least one tag; defaults are not restored automatically."
			}
		}
	}
	e.dirty = true
	e.editSeq++
}
func (m *Model) cardInsert(text string) {
	e := &m.graph.editor
	if !e.open || e.mode != "" || e.field >= e.fieldCount() {
		return
	}
	_, value, bound := e.fieldValue(e.field)
	runes := []rune(value)
	e.caret = max(0, min(e.caret, len(runes)))
	insert := []rune(cleanUISpan(text))
	room := max(0, bound-len(runes))
	if len(insert) > room {
		insert = insert[:room]
		e.problem = "field limit reached; extra text was not inserted"
	}
	runes = append(runes[:e.caret], append(insert, runes[e.caret:]...)...)
	e.setField(e.field, string(runes))
	e.caret += len(insert)
}
func (m *Model) cardEditorKey(k tea.KeyPressMsg) tea.Cmd {
	e := &m.graph.editor
	if e.mode != "" {
		return m.cardModalKey(k)
	}
	if isKey(k, tea.KeyEsc) {
		if e.dirty {
			e.mode = "discard"
			e.choice = 0
		} else {
			e.open = false
		}
		return nil
	}
	if k.Code == tea.KeyTab {
		delta := 1
		if k.Mod&tea.ModShift != 0 {
			delta = -1
		}
		e.field = (e.field + delta + e.fieldCount() + len(cardEditorActions)) % (e.fieldCount() + len(cardEditorActions))
		_, value, _ := e.fieldValue(e.field)
		e.caret = utf8.RuneCountInString(value)
		return nil
	}
	if e.field < e.fieldCount() {
		_, value, _ := e.fieldValue(e.field)
		runes := []rune(value)
		e.caret = max(0, min(e.caret, len(runes)))
		switch k.Code {
		case tea.KeyLeft:
			e.caret = max(0, e.caret-1)
		case tea.KeyRight:
			e.caret = min(len(runes), e.caret+1)
		case tea.KeyHome:
			e.caret = 0
		case tea.KeyEnd:
			e.caret = len(runes)
		case tea.KeyBackspace:
			if e.caret > 0 {
				runes = append(runes[:e.caret-1], runes[e.caret:]...)
				e.caret--
				e.setField(e.field, string(runes))
			}
		case tea.KeyDelete:
			if e.caret < len(runes) {
				e.setField(e.field, string(append(runes[:e.caret], runes[e.caret+1:]...)))
			}
		case tea.KeyEnter:
			e.field = (e.field + 1) % (e.fieldCount() + len(cardEditorActions))
		default:
			if k.Mod&^(tea.ModShift|tea.ModCapsLock|tea.ModNumLock|tea.ModScrollLock) == 0 {
				if k.Text != "" {
					m.cardInsert(k.Text)
				} else if k.Code >= 32 && k.Code <= 0x10ffff {
					m.cardInsert(string(k.Code))
				}
			}
		}
		return nil
	}
	if !isKey(k, tea.KeyEnter) {
		return nil
	}
	switch e.field - e.fieldCount() {
	case 0:
		return m.cardOperation(wire.CardOpEdit)
	case 1:
		if e.dirty {
			e.problem = "save working copy before validation"
			return nil
		}
		return m.cardOperation(wire.CardOpValidate)
	case 2:
		e.mode = "diff"
		e.scroll = 0
	case 3:
		if e.dirty {
			e.problem = "save working copy before publishing"
			return nil
		}
		e.mode = "confirm"
		e.confirm = wire.CardOpPublish
	case 4:
		e.mode = "history"
		e.choice = 0
	case 5:
		return m.cardOperation(wire.CardOpRescan)
	case 6:
		if len(e.skills) >= protocol.MaxCardSkills {
			e.problem = "declared skill limit reached"
			return nil
		}
		e.skills = append(e.skills, wire.CardSkillEdit{Tags: []string{"declared"}})
		e.dirty = true
		e.editSeq++
		e.field = e.fieldCount() - 4
		e.caret = 0
	case 7:
		e.mode = "remove"
		e.choice = 0
	case 8:
		e.mode = "found"
		e.choice = 0
	case 9:
		e.mode = "draft"
	case 10:
		if e.dirty {
			e.mode = "conflict"
			e.choice = 0
		} else {
			return m.loadCard()
		}
	case 11:
		if e.dirty {
			e.mode = "discard"
			e.choice = 0
		} else {
			e.open = false
		}
	}
	return nil
}
func (m *Model) cardModalKey(k tea.KeyPressMsg) tea.Cmd {
	e := &m.graph.editor
	if isKey(k, tea.KeyEsc) {
		e.mode = ""
		return nil
	}
	if isKey(k, tea.KeyTab) || isKey(k, tea.KeyDown) || isKey(k, tea.KeyRight) {
		e.choice++
		return nil
	}
	if isKey(k, tea.KeyUp) || isKey(k, tea.KeyLeft) {
		e.choice = max(0, e.choice-1)
		return nil
	}
	if isKey(k, tea.KeyPgDown) {
		e.scroll += max(1, m.screenRows()-3)
		return nil
	}
	if isKey(k, tea.KeyPgUp) {
		e.scroll = max(0, e.scroll-max(1, m.screenRows()-3))
		return nil
	}
	if !isKey(k, tea.KeyEnter) {
		return nil
	}
	switch e.mode {
	case "remove":
		if len(e.skills) > 0 {
			i := e.choice % len(e.skills)
			e.skills = append(e.skills[:i], e.skills[i+1:]...)
			e.dirty = true
			e.editSeq++
			e.field = min(e.field, e.fieldCount()+len(cardEditorActions)-1)
		}
		e.mode = ""
	case "confirm":
		op := e.confirm
		if op == wire.CardOpRollback && (!e.previewReady || e.pending) {
			e.problem = "wait for the selected revision preview before confirming"
			return nil
		}
		e.mode = ""
		return m.cardOperation(op)
	case "discard":
		switch e.choice % 3 {
		case 0:
			cmd := m.cardOperation(wire.CardOpEdit)
			e.mode = ""
			return cmd
		case 1:
			e.adopt(e.view)
			e.mode = ""
			e.open = false
		case 2:
			e.mode = ""
		}
	case "history":
		if len(e.view.Revisions) > 0 {
			e.revision = e.view.Revisions[e.choice%len(e.view.Revisions)].N
			e.confirm = wire.CardOpRollback
			e.mode = "confirm"
			e.previewReady = false
			return m.cardOperation(wire.CardOpHistory)
		}
	case "conflict":
		switch e.choice % 3 {
		case 0:
			e.reloadDiscard = true
			return m.loadCard()
		case 1:
			if e.uncertain {
				e.problem = "reload the current card before reviewing an unknown outcome"
				return nil
			}
			e.baseToken = e.view.Token
			e.blocked = false
			e.mode = "diff"
			e.scroll = 0
		case 2:
			e.mode = ""
		}
	case "found":
		if len(e.view.Skills.Found) == 0 {
			return nil
		}
		found := e.view.Skills.Found[e.choice%len(e.view.Skills.Found)]
		for i, skill := range e.skills {
			if skill.FoundLabel == found.Label {
				e.skills = append(e.skills[:i], e.skills[i+1:]...)
				e.dirty = true
				e.editSeq++
				return nil
			}
			if skill.ID == found.DirName {
				// Selecting fresh scan evidence keeps the owner's declaration text.
				e.skills[i].FoundLabel = found.Label
				e.dirty = true
				e.editSeq++
				return nil
			}
		}
		if len(e.skills) < protocol.MaxCardSkills {
			e.skills = append(e.skills, wire.CardSkillEdit{ID: found.DirName, Name: found.Name, FoundLabel: found.Label, Tags: []string{found.Harness, "detected"}})
			e.dirty = true
			e.editSeq++
			e.mode = ""
			e.field = 2 + (len(e.skills)-1)*4 + 2
			e.caret = 0
			e.problem = "A skill needs a description you wrote before it can be declared."
		}
	}
	return nil
}
func (m *Model) cardEditorLines(width, rows int) []string {
	e := &m.graph.editor
	lines := []string{"Local host card editor - unpublished working copy", "Tab/Shift-Tab fields/actions; Enter activates; Esc back"}
	if !e.have {
		lines = append(lines, "Local card loading / unavailable", e.problem)
		return m.graphTextViewport(lines, width, rows)
	}
	lines = append(lines, cardListenerLines(e.view)...)
	if strings.TrimSpace(e.description) == "" {
		lines = append(lines, "Card description needed to publish; saving a working copy is allowed.")
	}
	status := "saved working copy"
	if e.dirty {
		status = "UNSAVED FIELDS - F10 detaches and loses these fields"
	}
	if e.pending {
		status += "; operation pending"
	}
	lines = append(lines, status)
	if e.problem != "" {
		lines = append(lines, "! "+e.problem)
	}
	if e.mode != "" {
		return m.cardModalLines(width, rows, lines)
	}
	form := []string{}
	for i := 0; i < e.fieldCount(); i++ {
		label, value, _ := e.fieldValue(i)
		prefix := "  "
		if i == e.field {
			prefix = "> "
			runes := []rune(value)
			caret := max(0, min(e.caret, len(runes)))
			// Keep the editing caret visible even for a long description or tag list.
			available := max(1, width-ansi.StringWidth(prefix+label+": "))
			for caret > 0 && ansi.StringWidth(string(runes[:caret])+"|") > available {
				runes = runes[1:]
				caret--
			}
			value = string(runes[:caret]) + "|" + string(runes[caret:])
		}
		form = append(form, prefix+label+": "+cleanUISpan(value))
	}
	for i, action := range cardEditorActions {
		prefix := "  "
		if e.field == e.fieldCount()+i {
			prefix = "> "
		}
		form = append(form, prefix+"["+action+"]")
	}
	visible := max(1, rows-len(lines))
	top := max(0, e.field-visible+1)
	top = min(top, max(0, len(form)-visible))
	lines = append(lines, form[top:]...)
	out := []string{}
	for _, line := range lines {
		if len(out) >= rows {
			break
		}
		out = append(out, m.uiLine(line, width, colText, colTermBG, false))
	}
	for len(out) < rows {
		out = append(out, m.uiLine("", width, colText, colTermBG, false))
	}
	return out
}
func (m *Model) cardModalLines(width, rows int, header []string) []string {
	e := &m.graph.editor
	lines := append([]string{}, header...)
	switch e.mode {
	case "remove":
		lines = append(lines, "Choose a declared skill to remove; Enter removes; Esc cancels")
		for i, skill := range e.skills {
			prefix := "  "
			if i == e.choice%len(e.skills) {
				prefix = "> "
			}
			lines = append(lines, prefix+skill.ID+" - "+skill.Name)
		}
		if len(e.skills) == 0 {
			lines = append(lines, "No declared skills")
		}
	case "draft":
		lines = append(lines, "Drafting: not configured", "No provider/model selected. Manual editing is available.", "No model or drafting endpoint is called. Esc returns.")
	case "confirm":
		lines = append(lines, fmt.Sprintf("Confirm %s revision %d?", e.confirm, e.revision), "Enter confirms; Esc cancels. Last good card stays on validation failure.")
		if e.confirm == wire.CardOpRollback {
			lines = append(lines, "Rollback publishes the chosen revision as a NEW revision.")
			for _, rev := range e.view.Revisions {
				if rev.N == e.revision {
					lines = append(lines, fmt.Sprintf("Chosen revision %d at %s caller %s", rev.N, rev.At, rev.Caller))
				}
			}
			if e.previewReady && e.view.Preview != nil && e.view.Preview.N == e.revision {
				lines = append(lines, cardSectionLines(&e.view.Preview.Card, nil, nil, cardSectionCard, width)...)
				lines = append(lines, cardSectionLines(&e.view.Preview.Card, nil, nil, cardSectionSkills, width)...)
			} else {
				lines = append(lines, "Selected revision preview pending / unavailable; confirmation disabled")
			}
		} else {
			lines = append(lines, m.cardDiffLines()...)
		}
	case "discard":
		lines = append(lines, "Unsaved fields: choose with arrows/Tab, Enter", []string{"[Save working copy] discard cancel", "save [Discard fields] cancel", "save discard [Cancel]"}[e.choice%3])
	case "conflict":
		lines = append(lines, "Current local card and your fields may differ.", "Choose: reload discards fields; rebase keeps them for review.", []string{"[Reload current] Review/rebase Cancel", "Reload current [Review/rebase] Cancel", "Reload current Review/rebase [Cancel]"}[e.choice%3])
	case "history":
		for i, rev := range e.view.Revisions {
			prefix := "  "
			if len(e.view.Revisions) > 0 && i == e.choice%len(e.view.Revisions) {
				prefix = "> "
			}
			lines = append(lines, fmt.Sprintf("%srevision %d %s caller %s", prefix, rev.N, rev.At, rev.Caller))
		}
		lines = append(lines, "Enter reviews rollback as a NEW revision; Esc returns")
	case "found":
		lines = append(lines, "Found files are evidence, not permission or capability.", "Project, plugin and managed: not scanned.", "This list can be shorter than what a harness loads: both harnesses follow symlinked skill folders; this scan does not.", "Enter selects/unselects; a new selection needs your own description before declaration.")
		for i, found := range e.view.Skills.Found {
			prefix := "  "
			if len(e.view.Skills.Found) > 0 && i == e.choice%len(e.view.Skills.Found) {
				prefix = "> "
			}
			location := "undocumented location"
			if found.LocationVerified {
				location = "location confirmed by docs"
			}
			lines = append(lines, fmt.Sprintf("%s%s %s %s; %s; hash %s", prefix, found.Harness, found.Scope, found.Label, location, found.SHA256))
		}
		if e.view.Skills.Truncated {
			lines = append(lines, "inventory truncated")
		}
	case "diff":
		lines = append(lines, m.cardDiffLines()...)
	}
	return m.graphTextAt(lines, width, rows, e.scroll)
}
func (m *Model) cardDiffLines() []string {
	e := &m.graph.editor
	lines := []string{"Diff / review: published -> current fields"}
	if e.view.Published != nil {
		lines = append(lines, "Published name: "+e.view.Published.Card.Name, "Published description: "+e.view.Published.Card.Description)
		lines = append(lines, "Published skills:")
		for _, skill := range e.view.Published.Card.Skills {
			lines = append(lines, fmt.Sprintf("%s %s: %s [%s]", skill.ID, skill.Name, skill.Description, strings.Join(skill.Tags, ",")))
		}
	} else {
		lines = append(lines, "No published card")
	}
	lines = append(lines, "Working name: "+e.name, "Working description: "+e.description, "Declared skills (owner text):")
	for _, skill := range e.skills {
		lines = append(lines, fmt.Sprintf("%s %s: %s [%s]", skill.ID, skill.Name, skill.Description, strings.Join(skill.Tags, ",")))
	}
	for _, problem := range e.view.Working.Problems {
		lines = append(lines, "Validation "+problem.Field+": "+problem.Message)
	}
	if e.view.Published != nil {
		data, _ := json.MarshalIndent(e.view.Published.Sources, "", "  ")
		lines = append(lines, "Published field sources:")
		lines = append(lines, strings.Split(string(data), "\n")...)
	}
	return lines
}
