package card

// Tests for card.yaml: decoding, line numbers, the credential rule, the template, the rewrite and the
// projection. UNTESTED: written, not run (GR-3).

import (
	"reflect"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const goodFile = `name: vmb
description: Build host for the mac mini.
version: "3"
skills:
  - id: code-review
    name: Code review
    description: Reviews a diff.
    tags: [claude, declared]
drafting:
  preset: claude
  argv: []
  pass_env: [ANTHROPIC_API_KEY]
`

func problemAt(loaded Loaded, line int) *LineProblem {
	for index := range loaded.Problems {
		if loaded.Problems[index].Line == line {
			return &loaded.Problems[index]
		}
	}
	return nil
}

func TestLoadTemplateServesTheDefaultDescription(t *testing.T) {
	loaded := Load(Template("vmb"), "vmb", nil)
	if !loaded.Valid || !loaded.Defaulted || !loaded.Decoded || len(loaded.Problems) != 0 {
		t.Fatalf("template: valid=%v defaulted=%v decoded=%v problems=%+v", loaded.Valid, loaded.Defaulted, loaded.Decoded, loaded.Problems)
	}
	if want := "Ratatoskr host daemon on vmb. The owner has not written a description."; loaded.Card.Description != want {
		t.Fatalf("description = %q", loaded.Card.Description)
	}
	if loaded.Owner.Description != "" || len(loaded.Card.Skills) != 0 {
		t.Fatalf("owner description %q, skills %d", loaded.Owner.Description, len(loaded.Card.Skills))
	}
	if loaded.Owner.Drafting.Preset != PresetClaude {
		t.Fatalf("template preset = %q", loaded.Owner.Drafting.Preset)
	}
}

func TestLoadGoodFile(t *testing.T) {
	loaded := Load([]byte(goodFile), "ignored", nil)
	if !loaded.Valid {
		t.Fatalf("problems = %+v", loaded.Problems)
	}
	if loaded.Card.Name != "vmb" || loaded.Card.Version != "3" || len(loaded.Card.Skills) != 1 {
		t.Fatalf("card = %+v", loaded.Card)
	}
	if problems := Validate(loaded.Card, false); len(problems) > 0 {
		t.Fatalf("projected card fails Validate: %+v", problems)
	}
}

func TestLoadNameDefaultsToTheHost(t *testing.T) {
	loaded := Load([]byte("description: Build host.\n"), "mini", nil)
	if !loaded.Valid || loaded.Card.Name != "mini" || loaded.Card.Version != "1" {
		t.Fatalf("valid=%v card=%+v problems=%+v", loaded.Valid, loaded.Card, loaded.Problems)
	}
}

// TestLoadUnknownKeysAreRefusedWithALine: an unknown key anywhere is an error at its own line, and the
// key is not echoed.
func TestLoadUnknownKeysAreRefusedWithALine(t *testing.T) {
	loaded := Load([]byte("name: a\ndescription: d\nsecret_mode: on\n"), "h", nil)
	if loaded.Valid || loaded.Decoded {
		t.Fatal("unknown top-level key accepted")
	}
	problem := problemAt(loaded, 3)
	if problem == nil {
		t.Fatalf("no problem at line 3: %+v", loaded.Problems)
	}
	if strings.Contains(problem.Message, "secret_mode") {
		t.Fatalf("message echoes the key: %q", problem.Message)
	}

	nested := Load([]byte("description: d\nskills:\n  - id: a\n    name: A\n    colour: red\n"), "h", nil)
	if nested.Decoded || problemAt(nested, 5) == nil {
		t.Fatalf("nested unknown key: %+v", nested.Problems)
	}
	drafting := Load([]byte("description: d\ndrafting:\n  preset: claude\n  model: x\n"), "h", nil)
	if drafting.Decoded || problemAt(drafting, 4) == nil {
		t.Fatalf("unknown drafting key: %+v", drafting.Problems)
	}
}

func TestLoadDuplicateKey(t *testing.T) {
	loaded := Load([]byte("description: a\ndescription: b\n"), "h", nil)
	if loaded.Decoded || problemAt(loaded, 2) == nil {
		t.Fatalf("problems = %+v", loaded.Problems)
	}
}

// TestLoadCredentialIsAnErrorWithoutEcho: a credential-looking value anywhere, including drafting, names
// its line and is never repeated.
func TestLoadCredentialIsAnErrorWithoutEcho(t *testing.T) {
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123"
	for name, file := range map[string]string{
		"description": "description: " + secret + "\n",
		"argv":        "description: d\ndrafting:\n  preset: custom\n  argv: [tool, " + secret + "]\n",
		"skill":       "description: d\nskills:\n  - id: a\n    name: A\n    description: " + secret + "\n    tags: [x]\n",
	} {
		loaded := Load([]byte(file), "h", nil)
		if loaded.Valid || len(loaded.Problems) == 0 {
			t.Fatalf("%s: accepted", name)
		}
		for _, problem := range loaded.Problems {
			if strings.Contains(problem.Message, secret) || strings.Contains(problem.Field, secret) {
				t.Fatalf("%s: problem repeats the value: %+v", name, problem)
			}
			if problem.Line == 0 {
				t.Fatalf("%s: problem has no line: %+v", name, problem)
			}
		}
	}
}

func TestLoadPassEnvHoldsNamesOnly(t *testing.T) {
	loaded := Load([]byte("description: d\ndrafting:\n  preset: claude\n  pass_env: [\"KEY=value\"]\n"), "h", nil)
	if loaded.Decoded || problemAt(loaded, 4) == nil {
		t.Fatalf("problems = %+v", loaded.Problems)
	}
}

func TestLoadPresetRules(t *testing.T) {
	cases := map[string]string{
		"argv with a preset":      "description: d\ndrafting:\n  preset: claude\n  argv: [x]\n",
		"custom without argv":     "description: d\ndrafting:\n  preset: custom\n",
		"unknown preset":          "description: d\ndrafting:\n  preset: gpt\n",
		"argv is not a list":      "description: d\ndrafting:\n  preset: custom\n  argv: tool\n",
		"description is a number": "description: 12\n",
	}
	for name, file := range cases {
		if loaded := Load([]byte(file), "h", nil); loaded.Decoded {
			t.Fatalf("%s: decoded", name)
		}
	}
	for _, file := range []string{"description: d\n", "description: d\ndrafting:\n  preset: codex\n", "description: d\ndrafting:\n  preset: custom\n  argv: [/usr/local/bin/tool, --json]\n"} {
		if loaded := Load([]byte(file), "h", nil); !loaded.Valid {
			t.Fatalf("%q: %+v", file, loaded.Problems)
		}
	}
}

func TestLoadRefusesAliasesBadYAMLAndSize(t *testing.T) {
	if loaded := Load([]byte("description: &a d\nname: *a\n"), "h", nil); loaded.Decoded {
		t.Fatal("alias accepted")
	}

	bad := Load([]byte("description: [unclosed\n"), "h", nil)
	if bad.Decoded || len(bad.Problems) != 1 || bad.Problems[0].Line == 0 {
		t.Fatalf("syntax error: %+v", bad.Problems)
	}
	if strings.Contains(bad.Problems[0].Message, "unclosed") {
		t.Fatalf("syntax error message echoes input: %q", bad.Problems[0].Message)
	}

	huge := Load([]byte("description: "+strings.Repeat("a", protocol.MaxCardFileBytes)), "h", nil)
	if huge.Decoded || len(huge.Problems) != 1 || huge.Problems[0].Line != 0 {
		t.Fatalf("oversize: %+v", huge.Problems)
	}
	if loaded := Load([]byte("name: a\x00\n"), "h", nil); loaded.Decoded {
		t.Fatal("NUL accepted")
	}
	if loaded := Load(nil, "h", nil); !loaded.Valid || !loaded.Defaulted {
		t.Fatalf("empty file: %+v", loaded)
	}
	// A written description is used as written, and a bad one is still an error.
	if loaded := Load([]byte("description: Mine.\n"), "h", nil); !loaded.Valid || loaded.Defaulted || loaded.Card.Description != "Mine." {
		t.Fatalf("written description: %+v", loaded)
	}
}

// TestLoadCardRuleProblemsPointAtTheirLines: a rule the card breaks is reported at the line of the field.
func TestLoadCardRuleProblemsPointAtTheirLines(t *testing.T) {
	file := "description: d\nskills:\n  - id: Bad Id\n    name: A\n    description: x\n    tags: [t]\n"
	loaded := Load([]byte(file), "h", nil)
	if loaded.Valid || !loaded.Decoded {
		t.Fatalf("valid=%v decoded=%v", loaded.Valid, loaded.Decoded)
	}
	if problemAt(loaded, 3) == nil {
		t.Fatalf("no problem at the id line: %+v", loaded.Problems)
	}
}

// TestProjectionNeverCarriesPrivateConfiguration is the 13.6 requirement: a card.yaml with drafting:
// (and an extra key) produces served JSON with neither. The extra key is refused at load, so it can
// never reach Project; the drafting section is decoded but Project does not read it.
func TestProjectionNeverCarriesPrivateConfiguration(t *testing.T) {
	loaded := Load([]byte(goodFile), "vmb", nil)
	if !loaded.Valid || loaded.Owner.Drafting.Preset != PresetClaude {
		t.Fatalf("setup: %+v", loaded.Problems)
	}

	served, err := Canonical(WithURL(loaded.Card, "http://100.64.0.5:7778"+protocol.CardPath))
	if err != nil {
		t.Fatal(err)
	}
	text := string(served)
	for _, word := range []string{"drafting", "preset", "pass_env", "passEnv", "argv", "ANTHROPIC_API_KEY", "claude\"}"} {
		if strings.Contains(text, word) {
			t.Fatalf("served JSON contains %q:\n%s", word, text)
		}
	}
	if parsed, problems := Parse(served, Strict, true); len(problems) > 0 || parsed.Card.Name != "vmb" {
		t.Fatalf("served bytes do not parse strictly: %+v", problems)
	}

	withExtra := Load([]byte(goodFile+"internal_note: secret plan\n"), "vmb", nil)
	if withExtra.Valid || withExtra.Decoded {
		t.Fatal("an extra top-level key was accepted")
	}
	if withExtra.Card.Name != "" || len(withExtra.Card.Skills) != 0 {
		t.Fatal("an invalid file produced a projected card")
	}
}

func TestProjectAttachesEvidenceOnlyForDeclaredSkills(t *testing.T) {
	owner := Owner{Description: "d", Skills: []OwnerSkill{{ID: "a", Name: "A", Description: "x", Tags: []string{"t"}}}}
	evidence := map[string]protocol.SkillEvidence{
		"a":     {Harness: "claude", SHA256: strings.Repeat("a", 64)},
		"other": {Harness: "codex", SHA256: strings.Repeat("b", 64)},
	}
	projected := Project(owner, "h", evidence)
	got := projected.Capabilities.Extensions[0].Params.Skills
	if len(got) != 1 || got["a"].Harness != "claude" {
		t.Fatalf("evidence = %+v", got)
	}
}

func TestMarshalRoundTrips(t *testing.T) {
	loaded := Load([]byte(goodFile), "vmb", nil)
	encoded, err := Marshal(loaded.Owner)
	if err != nil {
		t.Fatal(err)
	}
	again := Load(encoded, "vmb", nil)
	if !again.Valid || !reflect.DeepEqual(again.Owner, loaded.Owner) {
		t.Fatalf("round trip changed the owner:\n%s\n%+v\n%+v", encoded, loaded.Owner, again.Owner)
	}
}

func TestTemplateSanitisesTheHostName(t *testing.T) {
	for _, host := range []string{"", "bad\"name", "a: b", "line\nbreak"} {
		if loaded := Load(Template(host), "", nil); !loaded.Decoded {
			t.Fatalf("host %q: template does not decode: %+v", host, loaded.Problems)
		}
	}
}
