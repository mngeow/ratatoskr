package protocol

// Keeps docs/spec/participant-api and the Go types from drifting. Written, never run (GR-3): UNTESTED.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const participantSpecDir = "../../docs/spec/participant-api"

// exchange is the shape of one example file.
type exchange struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Request     struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	} `json:"response"`
}

// bodyTypes names the Go type of each example's request and response body. A nil constructor means the
// example has no body of that kind (or, for an empty object, an empty struct).
type bodyTypes struct {
	request  func() any
	response func() any
}

func wireError() any { return &WireError{} }

// exampleTypes is keyed by the example's name. Every example in the index must be here.
var exampleTypes = map[string]bodyTypes{
	"discovery":                   {nil, func() any { return &ParticipantDiscovery{} }},
	"pair-ok":                     {func() any { return &ParticipantPairRequest{} }, func() any { return &ParticipantPairResponse{} }},
	"pair-bad-code":               {func() any { return &ParticipantPairRequest{} }, wireError},
	"register-session":            {func() any { return &ParticipantAddSessionRequest{} }, func() any { return &ParticipantAddSessionResponse{} }},
	"register-ninth-session":      {func() any { return &ParticipantAddSessionRequest{} }, wireError},
	"send-to-managed-session":     {func() any { return &SendRequest{} }, func() any { return &SendResponse{} }},
	"send-to-unknown-name":        {func() any { return &SendRequest{} }, func() any { return &SendResponse{} }},
	"send-to-offline-participant": {func() any { return &SendRequest{} }, func() any { return &SendResponse{} }},
	"send-delivery-uncertain":     {func() any { return &SendRequest{} }, func() any { return &SendResponse{} }},
	"send-with-bare-to":           {func() any { return &SendRequest{} }, wireError},
	"poll-with-offer":             {nil, func() any { return &InboxResponse{} }},
	"poll-timeout":                {nil, func() any { return &InboxResponse{} }},
	"poll-replaced":               {nil, func() any { return &InboxResponse{} }},
	"ack-saved":                   {func() any { return &AckRequest{} }, func() any { return &struct{}{} }},
	"ack-refused":                 {func() any { return &AckRequest{} }, func() any { return &struct{}{} }},
	"status-accepted":             {func() any { return &StatusReport{} }, func() any { return &struct{}{} }},
	"status-out-of-order":         {func() any { return &StatusReport{} }, func() any { return &struct{}{} }},
	"read-message":                {nil, func() any { return &MessageInfo{} }},
	"revoked-credential":          {nil, wireError},
	"unsupported-contract":        {nil, wireError},
}

// roundTrip decodes raw into a new value of the type, refusing any field the type does not have, and checks
// that encoding the value again gives the same JSON. A renamed field, a field the type lacks and a field
// the type would drop are all caught.
func roundTrip(t *testing.T, label string, raw json.RawMessage, make func() any) {
	t.Helper()
	if make == nil {
		if len(raw) != 0 {
			t.Fatalf("%s: the example has a body but the test names no type for it", label)
		}
		return
	}
	if len(raw) == 0 {
		t.Fatalf("%s: the test names a type but the example has no body", label)
	}
	value := make()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		t.Fatalf("%s: decoding into %T: %v", label, value, err)
	}
	again, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("%s: encoding %T: %v", label, value, err)
	}
	var want, got any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(again, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s: %T encodes as %s, the example says %s", label, value, again, raw)
	}
}

func readParticipantSpecFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{participantSpecDir}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestParticipantExamplesDecodeWithTheGoTypes: every example's bodies decode into the Go types with no
// unknown field and encode back to the same JSON.
func TestParticipantExamplesDecodeWithTheGoTypes(t *testing.T) {
	var index struct {
		Contract string `json:"contract"`
		Examples []struct {
			File string `json:"file"`
			Name string `json:"name"`
			Flow string `json:"flow"`
		} `json:"examples"`
	}
	if err := json.Unmarshal(readParticipantSpecFile(t, "examples", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	if index.Contract != ParticipantContract {
		t.Fatalf("index contract = %q, want %q", index.Contract, ParticipantContract)
	}
	if len(index.Examples) != len(exampleTypes) {
		t.Fatalf("the index lists %d examples, the test knows %d", len(index.Examples), len(exampleTypes))
	}

	listed := map[string]bool{"index.json": true}
	for _, entry := range index.Examples {
		listed[entry.File] = true
		t.Run(entry.Name, func(t *testing.T) {
			types, known := exampleTypes[entry.Name]
			if !known {
				t.Fatalf("example %q has no entry in exampleTypes", entry.Name)
			}
			var ex exchange
			if err := json.Unmarshal(readParticipantSpecFile(t, "examples", entry.File), &ex); err != nil {
				t.Fatal(err)
			}
			if ex.Name != entry.Name || ex.Description == "" || entry.Flow == "" {
				t.Fatalf("name %q / description / flow do not match the index entry %+v", ex.Name, entry)
			}
			if !strings.HasPrefix(ex.Request.Path, ParticipantPathPrefix) {
				t.Fatalf("request path %q is outside %s", ex.Request.Path, ParticipantPathPrefix)
			}
			roundTrip(t, "request", ex.Request.Body, types.request)
			roundTrip(t, "response", ex.Response.Body, types.response)

			// Every participant response names the contract minor.
			if got := ex.Response.Headers[ParticipantContractHeader]; got != ParticipantContract {
				t.Fatalf("response %s = %q, want %q", ParticipantContractHeader, got, ParticipantContract)
			}
			// The only authentication-free calls are discovery and pair.
			_, hasAuth := ex.Request.Headers["Authorization"]
			open := ex.Request.Path == ParticipantPathPrefix || ex.Request.Path == ParticipantPairPath
			if hasAuth == open {
				t.Fatalf("Authorization present = %t for %s, which is wrong for this route", hasAuth, ex.Request.Path)
			}
			// Secrets in an example are obviously fake.
			if hasAuth && !strings.Contains(ex.Request.Headers["Authorization"], "FAKE") {
				t.Fatalf("the credential in %s does not look fake", entry.File)
			}
			if bytes.Contains(ex.Response.Body, []byte(`"credential"`)) && !bytes.Contains(ex.Response.Body, []byte("FAKE")) {
				t.Fatalf("the credential in %s does not look fake", entry.File)
			}
		})
	}

	// No example file is left out of the index.
	files, err := os.ReadDir(filepath.Join(participantSpecDir, "examples"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !listed[f.Name()] {
			t.Errorf("examples/%s is not in index.json", f.Name())
		}
	}
}

// schemaFiles is one schema per request and response type of contract section 4, plus the shared parts.
var schemaFiles = []string{
	"error", "empty", "pair-request", "pair-response", "discovery", "session-info", "directory-entry",
	"directory-response", "add-session-request", "add-session-response", "send-request", "send-response",
	"offer", "inbox-response", "ack-request", "status-request", "message-info",
}

// TestParticipantSchemasAreWellFormed: each schema parses, names draft 2020-12, carries an id of its own
// name, and every $ref points at a schema file that exists. Its properties match the Go type's JSON names
// where a type is named.
func TestParticipantSchemasAreWellFormed(t *testing.T) {
	typeFor := map[string]any{
		"error": WireError{}, "pair-request": ParticipantPairRequest{}, "pair-response": ParticipantPairResponse{},
		"discovery": ParticipantDiscovery{}, "session-info": SessionInfo{}, "directory-entry": DirectoryEntry{},
		"directory-response": ParticipantDirectoryResponse{}, "add-session-request": ParticipantAddSessionRequest{},
		"add-session-response": ParticipantAddSessionResponse{}, "send-request": SendRequest{},
		"send-response": SendResponse{}, "offer": Offer{}, "inbox-response": InboxResponse{},
		"ack-request": AckRequest{}, "status-request": StatusReport{}, "message-info": MessageInfo{},
	}
	for _, name := range schemaFiles {
		t.Run(name, func(t *testing.T) {
			raw := readParticipantSpecFile(t, "schemas", name+".schema.json")
			var schema struct {
				Schema     string                     `json:"$schema"`
				ID         string                     `json:"$id"`
				Properties map[string]json.RawMessage `json:"properties"`
				Required   []string                   `json:"required"`
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if schema.Schema != "https://json-schema.org/draft/2020-12/schema" {
				t.Fatalf("$schema = %q", schema.Schema)
			}
			if !strings.HasSuffix(schema.ID, "/"+name+".schema.json") {
				t.Fatalf("$id = %q", schema.ID)
			}
			for _, target := range refsIn(t, raw) {
				readParticipantSpecFile(t, "schemas", target)
			}

			value, typed := typeFor[name]
			if !typed {
				return
			}
			names := jsonNames(reflect.TypeOf(value))
			for property := range schema.Properties {
				if !names[property] {
					t.Errorf("schema property %q is not a JSON name of %T", property, value)
				}
			}
			for name := range names {
				if _, found := schema.Properties[name]; !found {
					t.Errorf("%T field %q has no schema property", value, name)
				}
			}
			for _, required := range schema.Required {
				if _, found := schema.Properties[required]; !found {
					t.Errorf("required %q is not a property", required)
				}
			}
		})
	}

	files, err := os.ReadDir(filepath.Join(participantSpecDir, "schemas"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(schemaFiles) {
		t.Fatalf("schemas/ holds %d files, the test lists %d", len(files), len(schemaFiles))
	}
}

// refsIn finds every "$ref" string in a schema document.
func refsIn(t *testing.T, raw []byte) []string {
	t.Helper()
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	var out []string
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for key, child := range v {
				if text, ok := child.(string); ok && key == "$ref" {
					out = append(out, text)
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(document)
	return out
}

// jsonNames returns the JSON names of a struct type's exported fields.
func jsonNames(typ reflect.Type) map[string]bool {
	names := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}
