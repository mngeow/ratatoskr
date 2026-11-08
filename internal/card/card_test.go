package card

// Untested: under GR-3 nothing here has been run.

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

const sampleURL = "http://100.64.0.5:7778/.well-known/agent-card.json"

// sampleCard is a valid card as served.
func sampleCard() protocol.HostCard {
	return protocol.HostCard{
		Name:        "vmb",
		Description: "Build host",
		SupportedInterfaces: []protocol.CardInterface{{
			URL: sampleURL, ProtocolBinding: protocol.CardProtocolBinding, ProtocolVersion: protocol.CardProtocolVersion,
		}},
		Version: "3",
		Capabilities: protocol.CardCapabilities{
			Extensions: []protocol.CardExtension{{
				URI:    protocol.CardExtensionURI,
				Params: protocol.CardExtensionParams{Skills: map[string]protocol.SkillEvidence{"pdf-fill": {Harness: "claude", Scope: "user"}}},
			}},
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills:             []protocol.CardSkill{{ID: "pdf-fill", Name: "PDF fill", Description: "Fills forms", Tags: []string{"pdf"}}},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}

// mutate returns the sample card as a generic map after fn changed it.
func mutate(t *testing.T, fn func(map[string]any)) []byte {
	t.Helper()
	var generic map[string]any
	if err := json.Unmarshal(mustJSON(t, sampleCard()), &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fn(generic)
	return mustJSON(t, generic)
}

func fields(problems []protocol.CardProblem) []string {
	out := make([]string, len(problems))
	for index, problem := range problems {
		out[index] = problem.Field
	}
	return out
}

// TestParseAcceptsAValidCard: a valid card parses in both modes with no unknown fields.
func TestParseAcceptsAValidCard(t *testing.T) {
	data := mustJSON(t, sampleCard())
	for _, mode := range []Mode{Strict, Polled} {
		parsed, problems := Parse(data, mode, true)
		if len(problems) != 0 {
			t.Fatalf("mode %d: problems %+v", mode, problems)
		}
		if parsed.UnknownFields != 0 || parsed.Card.Name != "vmb" {
			t.Fatalf("mode %d: parsed = %+v", mode, parsed)
		}
	}
}

// TestStrictRefusesUnknownFieldsAndPolledCountsThem: the two modes.
func TestStrictRefusesUnknownFieldsAndPolledCountsThem(t *testing.T) {
	data := mutate(t, func(card map[string]any) {
		card["newTopLevel"] = "x"
		card["capabilities"].(map[string]any)["newCapability"] = true
		card["skills"].([]any)[0].(map[string]any)["newSkillField"] = 1
	})

	if _, problems := Parse(data, Strict, true); len(problems) != 1 || problems[0].Field != "capabilities.newCapability" {
		t.Fatalf("strict problems = %+v, want the first unknown field in sorted order", problems)
	}

	parsed, problems := Parse(data, Polled, true)
	if len(problems) != 0 {
		t.Fatalf("polled problems = %+v", problems)
	}
	if parsed.UnknownFields != 3 {
		t.Fatalf("unknown fields = %d, want 3", parsed.UnknownFields)
	}
	// What is kept has none of them.
	canonical, err := Canonical(parsed.Card)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for _, leaked := range []string{"newTopLevel", "newCapability", "newSkillField"} {
		if bytes.Contains(canonical, []byte(leaked)) {
			t.Fatalf("an unknown field %q was kept", leaked)
		}
	}
}

// TestCaseVariantKeysAreUnknownNotMatched: Go's decoder matches keys case-insensitively; the pruning
// step must stop "Name" from filling "name".
func TestCaseVariantKeysAreUnknownNotMatched(t *testing.T) {
	data := mutate(t, func(card map[string]any) {
		delete(card, "name")
		card["Name"] = "sneaky"
	})
	if _, problems := Parse(data, Polled, true); len(problems) == 0 || problems[0].Field != "name" {
		t.Fatalf("problems = %+v, want name required", problems)
	}
}

// TestKnownFieldsStillGetFullValidationWhenPolled: ignoring unknown fields never loosens the rest.
func TestKnownFieldsStillGetFullValidationWhenPolled(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(map[string]any)
		field string
	}{
		{"control character in name", func(c map[string]any) { c["name"] = "a\x1bb" }, "name"},
		{"long description", func(c map[string]any) { c["description"] = strings.Repeat("x", 281) }, "description"},
		{"missing description", func(c map[string]any) { delete(c, "description") }, "description"},
		{"empty description", func(c map[string]any) { c["description"] = "" }, "description"},
		{"skill without a description", func(c map[string]any) { c["skills"].([]any)[0].(map[string]any)["description"] = "" }, "skills[0].description"},
		{"skill without tags", func(c map[string]any) { c["skills"].([]any)[0].(map[string]any)["tags"] = []any{} }, "skills[0].tags"},
		{"skill with a null tag list", func(c map[string]any) { c["skills"].([]any)[0].(map[string]any)["tags"] = nil }, "skills[0].tags"},
		{"empty name", func(c map[string]any) { c["name"] = "" }, "name"},
		{"two interfaces", func(c map[string]any) {
			c["supportedInterfaces"] = append(c["supportedInterfaces"].([]any), c["supportedInterfaces"].([]any)[0])
		}, "supportedInterfaces"},
		{"wrong binding", func(c map[string]any) {
			c["supportedInterfaces"].([]any)[0].(map[string]any)["protocolBinding"] = "JSONRPC"
		}, "supportedInterfaces[].protocolBinding"},
		{"streaming on", func(c map[string]any) { c["capabilities"].(map[string]any)["streaming"] = true }, "capabilities"},
		{"bad version", func(c map[string]any) { c["version"] = "v1" }, "version"},
		{"other modes", func(c map[string]any) { c["defaultInputModes"] = []any{"image/png"} }, "defaultInputModes"},
		{"bad skill id", func(c map[string]any) { c["skills"].([]any)[0].(map[string]any)["id"] = "Bad ID" }, "skills[0].id"},
		{"evidence for an undeclared skill", func(c map[string]any) {
			c["capabilities"].(map[string]any)["extensions"].([]any)[0].(map[string]any)["params"].(map[string]any)["skills"].(map[string]any)["ghost"] = map[string]any{}
		}, "capabilities.extensions[].params.skills{}"},
		{"bad evidence hash", func(c map[string]any) {
			c["capabilities"].(map[string]any)["extensions"].([]any)[0].(map[string]any)["params"].(map[string]any)["skills"].(map[string]any)["pdf-fill"].(map[string]any)["sha256"] = "XYZ"
		}, "capabilities.extensions[].params.skills{}.sha256"},
		{"required extension", func(c map[string]any) {
			c["capabilities"].(map[string]any)["extensions"].([]any)[0].(map[string]any)["required"] = true
		}, "capabilities.extensions[].required"},
		{"wrong extension uri", func(c map[string]any) {
			c["capabilities"].(map[string]any)["extensions"].([]any)[0].(map[string]any)["uri"] = "urn:other"
		}, "capabilities.extensions[].uri"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, problems := Parse(mutate(t, tc.edit), Polled, true)
			found := false
			for _, field := range fields(problems) {
				if field == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("problems = %+v, want one for %q", problems, tc.field)
			}
		})
	}
}

// TestParseRefusals: size, encoding, shape and type errors, and no value is repeated.
func TestParseRefusals(t *testing.T) {
	secret := "SECRETVALUE12345"

	tests := []struct {
		name  string
		data  []byte
		field string
	}{
		{"too large", bytes.Repeat([]byte(" "), protocol.MaxCardBytes+1), ""},
		{"not utf-8", []byte("{\"name\":\"\xff\xfe\"}"), ""},
		{"not json", []byte("{nope"), ""},
		{"an array", []byte("[]"), ""},
		{"a string", []byte(`"` + secret + `"`), ""},
		{"wrong type", []byte(`{"name": 5}`), "name"},
		{"wrong type nested", []byte(`{"capabilities": {"streaming": "` + secret + `"}}`), "capabilities.streaming"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, problems := Parse(tc.data, Polled, true)
			if len(problems) == 0 {
				t.Fatal("accepted")
			}
			if tc.field != "" && problems[0].Field != tc.field {
				t.Fatalf("field = %q, want %q", problems[0].Field, tc.field)
			}
			for _, problem := range problems {
				if strings.Contains(problem.Message, secret) || strings.Contains(problem.Field, secret) {
					t.Fatalf("a problem repeated a value: %+v", problem)
				}
			}
		})
	}
}

// TestUnknownFieldPathIsCleaned: a remote key name never carries control characters into a problem.
func TestUnknownFieldPathIsCleaned(t *testing.T) {
	data := []byte(`{"bad\u001bkey": 1}`)
	_, problems := Parse(data, Strict, true)
	if len(problems) != 1 || strings.ContainsRune(problems[0].Field, 0x1b) {
		t.Fatalf("problems = %+v", problems)
	}
}

// TestServedVersusStoredURL: a stored card may leave the URL empty; a served card may not.
func TestServedVersusStoredURL(t *testing.T) {
	data := mutate(t, func(c map[string]any) {
		c["supportedInterfaces"].([]any)[0].(map[string]any)["url"] = ""
	})
	if _, problems := Parse(data, Strict, false); len(problems) != 0 {
		t.Fatalf("stored card without a URL: %+v", problems)
	}
	if _, problems := Parse(data, Strict, true); len(problems) != 1 || problems[0].Field != "supportedInterfaces[].url" {
		t.Fatalf("served card without a URL: %+v", problems)
	}
}

// TestValidURL: the one accepted shape.
func TestValidURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{sampleURL, true},
		{"https://192.168.1.4:8443/.well-known/agent-card.json", true},
		{"http://[fd7a:115c:a1e0::5]:7778/.well-known/agent-card.json", true},
		{"http://127.0.0.1:1/.well-known/agent-card.json", true},
		{"http://example.com:7778/.well-known/agent-card.json", false},
		{"http://100.64.0.5/.well-known/agent-card.json", false},
		{"http://100.64.0.5:0/.well-known/agent-card.json", false},
		{"http://100.64.0.5:99999/.well-known/agent-card.json", false},
		{"ftp://100.64.0.5:7778/.well-known/agent-card.json", false},
		{"http://100.64.0.5:7778/.well-known/agent.json", false},
		{"http://100.64.0.5:7778/other", false},
		{"http://100.64.0.5:7778/.well-known/agent-card.json?x=1", false},
		{"http://100.64.0.5:7778/.well-known/agent-card.json#f", false},
		{"http://user:pw@100.64.0.5:7778/.well-known/agent-card.json", false},
		{"HTTP://100.64.0.5:7778/.well-known/agent-card.json", false},
		{"http://[fe80::1%25eth0]:7778/.well-known/agent-card.json", false},
		{"http://[::ffff:100.64.0.5]:7778/.well-known/agent-card.json", false},
		{"javascript:alert(1)", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := ValidURL(tc.url); got != tc.want {
			t.Errorf("ValidURL(%q) = %t, want %t", tc.url, got, tc.want)
		}
	}
}

// TestCanonicalIsStableAndETagTracksBytes: the same card always yields the same bytes and tag; a change
// moves both; HTML characters are not escaped.
func TestCanonicalIsStableAndETagTracksBytes(t *testing.T) {
	first, err := Canonical(sampleCard())
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	second, _ := Canonical(sampleCard())
	if !bytes.Equal(first, second) {
		t.Fatal("canonical bytes differ between calls")
	}
	if bytes.HasSuffix(first, []byte("\n")) {
		t.Fatal("canonical bytes end in a newline")
	}
	if ETag(first) != ETag(second) || !strings.HasPrefix(ETag(first), `"`) || !strings.HasSuffix(ETag(first), `"`) {
		t.Fatalf("ETag = %q", ETag(first))
	}

	changed := sampleCard()
	changed.Description = "Build <host> & more"
	other, _ := Canonical(changed)
	if ETag(other) == ETag(first) {
		t.Fatal("a change did not move the ETag")
	}
	if !bytes.Contains(other, []byte("<host> & more")) {
		t.Fatalf("HTML characters were escaped: %s", other)
	}
}

// TestCanonicalRoundTripsThroughStrictParse: what the daemon serves is what the gateway accepts.
func TestCanonicalRoundTripsThroughStrictParse(t *testing.T) {
	data, err := Canonical(sampleCard())
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	parsed, problems := Parse(data, Strict, true)
	if len(problems) != 0 {
		t.Fatalf("problems = %+v", problems)
	}
	again, _ := Canonical(parsed.Card)
	if !bytes.Equal(data, again) {
		t.Fatal("a parse and re-render changed the bytes")
	}
}

// TestWithURLDoesNotModifyTheOriginal: serving fills the URL on a copy.
func TestWithURLDoesNotModifyTheOriginal(t *testing.T) {
	original := sampleCard()
	original.SupportedInterfaces[0].URL = ""

	served := WithURL(original, sampleURL)
	if served.SupportedInterfaces[0].URL != sampleURL {
		t.Fatalf("served url = %q", served.SupportedInterfaces[0].URL)
	}
	if original.SupportedInterfaces[0].URL != "" {
		t.Fatal("the original card was modified")
	}
}

// TestCheckPublic: obvious secrets and paths are blocked, ordinary text is not.
func TestCheckPublic(t *testing.T) {
	tests := []struct {
		text    string
		blocked bool
	}{
		{"Fills PDF forms and Word/Excel files", false},
		{"Build host for the mac mini", false},
		{"internationalization", false},
		{"sk-abcdefghijklmnop", true},
		{"token ghp_abcdefghijklmnop here", true},
		{"AKIAABCDEFGHIJKLMNOP", true},
		{"Authorization: Bearer abc", true},
		{"-----BEGIN PRIVATE KEY-----", true},
		{"a1b2c3d4e5f6a7b8c9d0a1b2c3d4e5f6a7b8", true},
		{"see /Users/someone/work", true},
		{"lives in /etc/hosts", true},
		{"config at ~/.ssh/config", true},
		{"C:\\Users\\me\\x", true},
		{"a/b", false},
		{"https://example.com/docs", false},
	}
	for _, tc := range tests {
		card := sampleCard()
		card.Description = tc.text
		got := len(CheckPublic(card)) > 0
		if got != tc.blocked {
			t.Errorf("CheckPublic(%q) blocked = %t, want %t", tc.text, got, tc.blocked)
		}
	}

	// Every text field is checked, not only the description.
	card := sampleCard()
	card.Skills[0].Tags = []string{"/Users/x/y"}
	if problems := CheckPublic(card); len(problems) == 0 || problems[0].Field != "skills[0].tags[0]" {
		t.Fatalf("tag problems = %+v", problems)
	}

	// The SHA-256 evidence is 64 hex characters by design and is not a finding.
	card = sampleCard()
	evidence := card.Capabilities.Extensions[0].Params.Skills["pdf-fill"]
	evidence.SHA256 = strings.Repeat("ab12", 16)
	card.Capabilities.Extensions[0].Params.Skills["pdf-fill"] = evidence
	if problems := CheckPublic(card); len(problems) != 0 {
		t.Fatalf("evidence hash was blocked: %+v", problems)
	}
}

// TestRequiredFieldsAreProblemsNotPanics: an incomplete working copy lists readable problems, in order,
// and the messages say what to do.
func TestRequiredFieldsAreProblemsNotPanics(t *testing.T) {
	incomplete := sampleCard()
	incomplete.Description = ""
	incomplete.Skills = []protocol.CardSkill{{ID: "pdf-fill", Name: "PDF fill"}}

	problems := Validate(incomplete, false)
	want := map[string]string{
		"description":           "description is required",
		"skills[0].description": "description is required for each declared skill",
		"skills[0].tags":        "at least one tag",
	}
	for field, fragment := range want {
		found := false
		for _, problem := range problems {
			if problem.Field == field && strings.Contains(problem.Message, fragment) {
				found = true
			}
		}
		if !found {
			t.Errorf("no problem for %q containing %q in %+v", field, fragment, problems)
		}
	}
}
