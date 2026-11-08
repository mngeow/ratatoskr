package card

// The owner's card.yaml: decoding, validation with line numbers, the template, the rewrite, and the
// ONE projection from the file to the public card.
//
// card.yaml holds private configuration (the drafting: section) next to the fields that become the
// public card. Two rules keep the private part private (docs/implementation/host-cards-contract.md
// 13.6):
//
//   - The owner-file type (Owner) is never the served type. The served card (protocol.HostCard) is
//     built by Project, which copies named fields one by one. Nothing marshals an Owner to the wire
//     or to a peer, and Owner is not a field of any protocol type.
//   - The file is decoded by walking the YAML tree against a fixed set of keys, so an unknown key is
//     an error with a line number and is never passed through.
//
// A value that looks like a credential anywhere in the file is an error naming the line; the value is
// never echoed. Messages are our own sentences. The YAML library's own error text is never shown
// (it can quote input); only the line number is taken from it.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Preset names under drafting:.
const (
	PresetClaude = "claude"
	PresetCodex  = "codex"
	PresetCustom = "custom"
)

// Bounds on the drafting section; the card fields are bounded by Validate.
const (
	maxOwnerSkills   = 256 // looked at before Validate's own (smaller) limit reports it
	maxDraftArgv     = 64
	maxDraftArgvItem = 2048 // bytes
	maxDraftPassEnv  = 32
)

var passEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)

// Owner is the content of card.yaml. It is private: see the file comment.
type Owner struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Version     string       `yaml:"version"`
	Skills      []OwnerSkill `yaml:"skills"`
	Drafting    OwnerDraft   `yaml:"drafting"`
}

// OwnerSkill is one skill the owner declares.
type OwnerSkill struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
}

// OwnerDraft is the private drafting configuration. PassEnv holds variable NAMES only.
type OwnerDraft struct {
	Preset  string   `yaml:"preset"`
	Argv    []string `yaml:"argv"`
	PassEnv []string `yaml:"pass_env"`
}

// LineProblem is one thing wrong with card.yaml. Line is 1-based, 0 when no line applies. Field is
// the path of the field ("skills[0].tags"), "" for a file-level problem. Message is our sentence.
type LineProblem struct {
	Line    int
	Field   string
	Message string
}

// Loaded is the result of reading a card.yaml.
type Loaded struct {
	Owner    Owner
	Problems []LineProblem
	// Card is the projected public card (without its URL, which the daemon fills at serve time). It is
	// meaningful only when Valid.
	Card protocol.HostCard
	// Decoded says the file's structure was understood (every key known, every value the right kind),
	// whether or not the card passes the card rules. Owner is trustworthy only when Decoded.
	Decoded bool
	// Valid says the file decoded and the projected card passes every rule.
	Valid bool
	// Defaulted says the file's description is empty, so the card carries DefaultDescription instead
	// (contract section 15). The file is never rewritten; Owner keeps the file's own empty value.
	Defaulted bool
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

// Load decodes and validates a card.yaml. host is the paired host name used when the file has no
// name. evidence is what the daemon found for declared skill ids; entries for ids the file does not
// declare are dropped by Project.
//
// UNTESTED.
func Load(data []byte, host string, evidence map[string]protocol.SkillEvidence) Loaded {
	var loaded Loaded
	fail := func(line int, field, message string) {
		loaded.Problems = append(loaded.Problems, LineProblem{Line: line, Field: field, Message: message})
	}

	if len(data) > protocol.MaxCardFileBytes {
		fail(0, "", fmt.Sprintf("card.yaml is larger than %d KiB", protocol.MaxCardFileBytes>>10))
		return loaded
	}
	if bytes.IndexByte(data, 0) >= 0 {
		fail(0, "", "card.yaml has a NUL byte; it must be a text file")
		return loaded
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		line := 0
		if match := yamlLine.FindStringSubmatch(err.Error()); match != nil {
			line, _ = strconv.Atoi(match[1])
		}
		fail(line, "", "card.yaml is not valid YAML")
		return loaded
	}

	reader := &ownerReader{fail: fail, lines: map[string]int{}}
	body := &root
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			body = nil
		} else {
			body = root.Content[0]
		}
	}
	if body != nil && body.Kind != 0 && !(body.Kind == yaml.ScalarNode && body.Tag == "!!null") {
		loaded.Owner = reader.owner(body)
	}

	if len(loaded.Problems) > 0 {
		return loaded
	}

	loaded.Decoded = true
	projected := loaded.Owner
	if strings.TrimSpace(projected.Description) == "" {
		loaded.Defaulted = true
		projected.Description = DefaultDescription(projected.Name, host)
	}
	loaded.Card = Project(projected, host, evidence)
	problems := Validate(loaded.Card, false)
	problems = append(problems, CheckPublic(loaded.Card)...)
	for _, problem := range problems {
		fail(reader.lineFor(problem.Field), problem.Field, problem.Message)
	}
	loaded.Valid = len(loaded.Problems) == 0
	loaded.Defaulted = loaded.Defaulted && loaded.Valid
	return loaded
}

// DefaultDescription is the card description served when the owner's is empty. It is plain and says
// the owner wrote nothing.
func DefaultDescription(name, host string) string {
	if name == "" {
		name = host
	}
	if name == "" {
		name = "host"
	}
	return "Ratatoskr host daemon on " + name + ". The owner has not written a description."
}

// ownerReader walks the YAML tree. lines maps a field path to the line it is written on.
type ownerReader struct {
	fail  func(line int, field, message string)
	lines map[string]int
}

// lineFor is the line of a field, or of its nearest parent that has one.
func (r *ownerReader) lineFor(field string) int {
	for field != "" {
		if line, ok := r.lines[field]; ok {
			return line
		}
		cut := strings.LastIndexAny(field, ".[")
		if cut < 0 {
			break
		}
		field = field[:cut]
	}
	return 0
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// mapping checks a mapping against the allowed keys and returns the value node for each key present.
func (r *ownerReader) mapping(node *yaml.Node, path string, allowed ...string) map[string]*yaml.Node {
	if node.Kind == yaml.AliasNode {
		r.fail(node.Line, path, "YAML aliases are not supported here")
		return nil
	}
	if node.Kind != yaml.MappingNode {
		r.fail(node.Line, path, "this must be a mapping of keys to values")
		return nil
	}
	out := map[string]*yaml.Node{}
	for index := 0; index+1 < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Kind != yaml.ScalarNode {
			r.fail(key.Line, path, "this key is not recognised")
			continue
		}
		known := false
		for _, name := range allowed {
			if key.Value == name {
				known = true
				break
			}
		}
		if !known {
			// The key is not echoed: a mistyped secret could be a key.
			r.fail(key.Line, path, "this key is not recognised")
			continue
		}
		if _, dup := out[key.Value]; dup {
			r.fail(key.Line, joinPath(path, key.Value), "this key appears twice")
			continue
		}
		out[key.Value] = value
		r.lines[joinPath(path, key.Value)] = key.Line
	}
	return out
}

// text reads a scalar as a string. A number is accepted only where numberOK (the version). A
// credential-looking value is an error that names the line and does not repeat the value.
func (r *ownerReader) text(node *yaml.Node, path string, numberOK bool) string {
	switch {
	case node.Kind == yaml.AliasNode:
		r.fail(node.Line, path, "YAML aliases are not supported here")
		return ""
	case node.Kind != yaml.ScalarNode:
		r.fail(node.Line, path, "this must be text")
		return ""
	case node.Tag == "!!null":
		return ""
	case node.Tag != "!!str" && !(numberOK && node.Tag == "!!int"):
		r.fail(node.Line, path, "this must be text; put it in quotes")
		return ""
	}
	if reason := CredentialProblem(node.Value); reason != "" {
		r.fail(node.Line, path, "this value looks like a credential; card.yaml must not hold keys or tokens")
		return ""
	}
	return node.Value
}

// list reads a sequence of text. null is an empty list.
func (r *ownerReader) list(node *yaml.Node, path string, limit int) []string {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		r.fail(node.Line, path, "this must be a list")
		return nil
	}
	if len(node.Content) > limit {
		r.fail(node.Line, path, fmt.Sprintf("this list has more than %d entries", limit))
		return nil
	}
	out := make([]string, 0, len(node.Content))
	for index, item := range node.Content {
		field := fmt.Sprintf("%s[%d]", path, index)
		r.lines[field] = item.Line
		out = append(out, r.text(item, field, false))
	}
	return out
}

func (r *ownerReader) owner(node *yaml.Node) Owner {
	var owner Owner
	fields := r.mapping(node, "", "name", "description", "version", "skills", "drafting")
	if value, ok := fields["name"]; ok {
		owner.Name = r.text(value, "name", false)
	}
	if value, ok := fields["description"]; ok {
		owner.Description = r.text(value, "description", false)
	}
	if value, ok := fields["version"]; ok {
		owner.Version = r.text(value, "version", true)
	}
	if value, ok := fields["skills"]; ok {
		owner.Skills = r.skills(value)
	}
	if value, ok := fields["drafting"]; ok {
		owner.Drafting = r.drafting(value)
	}
	return owner
}

func (r *ownerReader) skills(node *yaml.Node) []OwnerSkill {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return nil
	}
	if node.Kind != yaml.SequenceNode {
		r.fail(node.Line, "skills", "this must be a list")
		return nil
	}
	if len(node.Content) > maxOwnerSkills {
		r.fail(node.Line, "skills", fmt.Sprintf("this list has more than %d entries", maxOwnerSkills))
		return nil
	}
	out := make([]OwnerSkill, 0, len(node.Content))
	for index, item := range node.Content {
		path := fmt.Sprintf("skills[%d]", index)
		r.lines[path] = item.Line
		var skill OwnerSkill
		fields := r.mapping(item, path, "id", "name", "description", "tags")
		if value, ok := fields["id"]; ok {
			skill.ID = r.text(value, path+".id", false)
		}
		if value, ok := fields["name"]; ok {
			skill.Name = r.text(value, path+".name", false)
		}
		if value, ok := fields["description"]; ok {
			skill.Description = r.text(value, path+".description", false)
		}
		if value, ok := fields["tags"]; ok {
			skill.Tags = r.list(value, path+".tags", protocol.MaxCardSkillTags+1)
		}
		out = append(out, skill)
	}
	return out
}

func (r *ownerReader) drafting(node *yaml.Node) OwnerDraft {
	var draft OwnerDraft
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		return draft
	}
	fields := r.mapping(node, "drafting", "preset", "argv", "pass_env")
	if value, ok := fields["preset"]; ok {
		draft.Preset = r.text(value, "drafting.preset", false)
	}
	if value, ok := fields["argv"]; ok {
		draft.Argv = r.list(value, "drafting.argv", maxDraftArgv)
		for index, item := range draft.Argv {
			if len(item) > maxDraftArgvItem || strings.ContainsRune(item, 0) {
				r.fail(r.lines[fmt.Sprintf("drafting.argv[%d]", index)], "drafting.argv", "an argument is too long or has a NUL byte")
			}
		}
	}
	if value, ok := fields["pass_env"]; ok {
		draft.PassEnv = r.list(value, "drafting.pass_env", maxDraftPassEnv)
		for index, item := range draft.PassEnv {
			if !passEnvName.MatchString(item) {
				r.fail(r.lines[fmt.Sprintf("drafting.pass_env[%d]", index)], "drafting.pass_env",
					"pass_env holds variable names only (letters, digits, underscore); never a value")
			}
		}
	}

	switch draft.Preset {
	case "", PresetClaude, PresetCodex:
		if len(draft.Argv) > 0 {
			r.fail(r.lines["drafting.argv"], "drafting.argv", "argv is used only with preset: custom")
		}
	case PresetCustom:
		if len(draft.Argv) == 0 {
			r.fail(r.lines["drafting.preset"], "drafting.argv", "preset: custom needs an argv list")
		}
	default:
		r.fail(r.lines["drafting.preset"], "drafting.preset", "the preset must be claude, codex or custom")
	}
	return draft
}

// Project builds the public card from the owner's file. It is the ONLY way an Owner becomes a
// HostCard, and it copies named fields one at a time: drafting is never read here, and an Owner is
// never marshalled. The card has no URL (the daemon fills it from the bound listener at serve time).
//
// host is the name used when the file has none. evidence is attached only for skill ids the file
// declares.
//
// UNTESTED.
func Project(owner Owner, host string, evidence map[string]protocol.SkillEvidence) protocol.HostCard {
	name := owner.Name
	if name == "" {
		name = host
	}
	if name == "" {
		name = "host"
	}
	version := owner.Version
	if version == "" {
		version = "1"
	}

	skills := make([]protocol.CardSkill, 0, len(owner.Skills))
	attached := map[string]protocol.SkillEvidence{}
	for _, skill := range owner.Skills {
		tags := make([]string, 0, len(skill.Tags))
		tags = append(tags, skill.Tags...)
		skills = append(skills, protocol.CardSkill{
			ID:          skill.ID,
			Name:        skill.Name,
			Description: skill.Description,
			Tags:        tags,
		})
		if item, ok := evidence[skill.ID]; ok {
			attached[skill.ID] = item
		}
	}

	return protocol.HostCard{
		Name:        name,
		Description: owner.Description,
		SupportedInterfaces: []protocol.CardInterface{{
			ProtocolBinding: protocol.CardProtocolBinding,
			ProtocolVersion: protocol.CardProtocolVersion,
		}},
		Version: version,
		Capabilities: protocol.CardCapabilities{
			Extensions: []protocol.CardExtension{{
				URI:    protocol.CardExtensionURI,
				Params: protocol.CardExtensionParams{Skills: attached},
			}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             skills,
	}
}

// templateText is what a new card.yaml holds. It is valid as it stands: an empty description serves the
// default text.
const templateText = `# Ratatoskr host card. Edit this file in your own editor; the daemon notices the change within
# a couple of seconds and serves the new card.
#
# THE CARD IS PUBLIC. Anyone who can reach the card port can read it, with no password. Put nothing
# sensitive in it: no keys, tokens, paths, usernames or private project names.
#
# The daemon fills in the interface, capabilities and modes. Only the fields below are yours.

name: "%s"          # shown to other hosts

# One sentence saying what this host is for. If it stays empty, the card says only that this is a
# Ratatoskr host daemon and that the owner has not written a description.
description: ""

version: "1"        # digits only

# Skills you want to advertise. Detected skills are NOT added automatically.
# Each needs an id (lowercase), a name, a description and at least one tag.
skills: []
#  - id: code-review
#    name: Code review
#    description: Reviews a diff and lists defects.
#    tags: [claude, declared]

# PRIVATE: this section is never part of the card. It configures "g" (generate a draft) in the
# Graph tab, which runs YOUR installed harness after you confirm. Choose claude, codex or custom.
# pass_env lists environment variable NAMES to hand the harness (never a value). Do not put keys here.
drafting:
  preset: claude
  argv: []          # only for preset: custom, one entry per argument; no shell
  pass_env: []
`

// Template is a new card.yaml. host is the paired host name, or "".
func Template(host string) []byte {
	if host == "" || CredentialProblem(host) != "" || strings.ContainsAny(host, "\n\r\"'#:") {
		host = "host"
	}
	return []byte(fmt.Sprintf(templateText, host))
}

// rewriteHeader begins a card.yaml that Accept has rewritten.
const rewriteHeader = `# Ratatoskr host card (rewritten when a generated draft was accepted; comments from the previous
# file are kept in card.yaml.bak). The card is PUBLIC: nothing sensitive belongs here.
`

// Marshal writes an Owner as a card.yaml for Accept. It rewrites the whole file; comments are not kept.
func Marshal(owner Owner) ([]byte, error) {
	if owner.Skills == nil {
		owner.Skills = []OwnerSkill{}
	}
	if owner.Drafting.Argv == nil {
		owner.Drafting.Argv = []string{}
	}
	if owner.Drafting.PassEnv == nil {
		owner.Drafting.PassEnv = []string{}
	}
	var buffer bytes.Buffer
	buffer.WriteString(rewriteHeader)
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(owner); err != nil {
		return nil, fmt.Errorf("card: the card.yaml could not be encoded")
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("card: the card.yaml could not be encoded")
	}
	return buffer.Bytes(), nil
}
