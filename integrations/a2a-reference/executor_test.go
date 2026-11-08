package main

// Untested: under GR-3 nothing here has been run. Every case is a pure function: no port, no model, no
// network, and no SDK call.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// TestParseFormatLine: only the two exact answers count.
func TestParseFormatLine(t *testing.T) {
	tests := []struct {
		line  string
		want  string
		found bool
	}{
		{line: "format: json", want: formatJSON, found: true},
		{line: "format: text", want: formatText, found: true},
		{line: "  FORMAT:   JSON  ", want: formatJSON, found: true},
		{line: "\tformat: text\t", want: formatText, found: true},
		{line: "format:json", found: false},
		{line: "format: yaml", found: false},
		{line: "format:", found: false},
		{line: "the format: json", found: false},
		{line: "", found: false},
	}

	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			got, found := parseFormatLine(tc.line)
			if found != tc.found || got != tc.want {
				t.Fatalf("parseFormatLine(%q) = %q, %t; want %q, %t", tc.line, got, found, tc.want, tc.found)
			}
		})
	}
}

// TestSplitFormat: the first format line wins and is removed, everything else is body.
func TestSplitFormat(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantFormat string
		wantBody   string
	}{
		{name: "no format line", input: "one\ntwo", wantBody: "one\ntwo"},
		{name: "format then body", input: "format: json\none\ntwo", wantFormat: formatJSON, wantBody: "one\ntwo"},
		{name: "body then format", input: "one\ntwo\nformat: text", wantFormat: formatText, wantBody: "one\ntwo"},
		{
			name:       "first of two wins, the second is body",
			input:      "format: json\nformat: text\ntail",
			wantFormat: formatJSON,
			wantBody:   "format: text\ntail",
		},
		{name: "format only", input: "format: text", wantFormat: formatText, wantBody: ""},
		{name: "format:yaml is body", input: "format:yaml", wantBody: "format:yaml"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			format, body := splitFormat(tc.input)
			if format != tc.wantFormat {
				t.Fatalf("format = %q, want %q", format, tc.wantFormat)
			}
			if body != tc.wantBody {
				t.Fatalf("body = %q, want %q", body, tc.wantBody)
			}
		})
	}
}

// TestCountText: the counting rules, including the trailing-newline change.
func TestCountText(t *testing.T) {
	tests := []struct {
		name string
		body string
		want counts
	}{
		{name: "one word", body: "a", want: counts{lines: 1, words: 1, characters: 1}},
		{name: "a trailing newline does not add a line", body: "a\n", want: counts{lines: 1, words: 1, characters: 2}},
		{name: "a blank line does", body: "a\n\n", want: counts{lines: 2, words: 1, characters: 3}},
		{name: "two lines", body: "one two\nthree", want: counts{lines: 2, words: 3, characters: 13}},
		{name: "CRLF is not normalised", body: "a\r\nb", want: counts{lines: 2, words: 2, characters: 4}},
		{name: "unicode counts runes", body: "héllo", want: counts{lines: 1, words: 1, characters: 5}},
		{name: "an invalid byte is one character", body: "a\xffb", want: counts{lines: 1, words: 1, characters: 3}},
		{name: "whitespace runs collapse for words", body: "  a \t b  ", want: counts{lines: 1, words: 2, characters: 10}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := countText(tc.body)
			if got != tc.want {
				t.Fatalf("countText(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

// TestChooseBody: the current body wins, else the last earlier user message, else none.
func TestChooseBody(t *testing.T) {
	current := &a2a.Message{ID: "m3", Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart("format: json")}}
	earlier := &a2a.Message{ID: "m1", Role: a2a.MessageRoleUser, Parts: a2a.ContentParts{a2a.NewTextPart("one two")}}
	answer := &a2a.Message{ID: "m2", Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.NewTextPart(questionText)}}

	tests := []struct {
		name    string
		current string
		task    *a2a.Task
		message *a2a.Message
		want    string
		ok      bool
	}{
		{
			name:    "the current message wins",
			current: "in this message", message: current,
			task: &a2a.Task{History: []*a2a.Message{earlier, answer, current}},
			want: "in this message", ok: true,
		},
		{
			name:    "an earlier user message supplies it",
			current: "", message: current,
			task: &a2a.Task{History: []*a2a.Message{earlier, answer, current}},
			want: "one two", ok: true,
		},
		{
			name:    "blank current lines fall back too",
			current: "   \n\t\n", message: current,
			task: &a2a.Task{History: []*a2a.Message{earlier, current}},
			want: "one two", ok: true,
		},
		{
			name:    "nothing anywhere",
			current: "", message: current,
			task: &a2a.Task{History: []*a2a.Message{answer, current}},
			ok:   false,
		},
		{
			name:    "no stored task",
			current: "", message: current,
			ok: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ec := &a2asrv.ExecutorContext{Message: tc.message, StoredTask: tc.task}
			got, ok := chooseBody(tc.current, ec)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("chooseBody = %q, %t; want %q, %t", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestMessageText: text parts join with newlines; any other part is refused.
func TestMessageText(t *testing.T) {
	message := &a2a.Message{Parts: a2a.ContentParts{
		a2a.NewTextPart("one"),
		a2a.NewTextPart("two"),
	}}
	got, err := messageText(message)
	if err != nil {
		t.Fatalf("messageText: %v", err)
	}
	if got != "one\ntwo" {
		t.Fatalf("messageText = %q", got)
	}

	mixed := &a2a.Message{Parts: a2a.ContentParts{
		a2a.NewTextPart("one"),
		a2a.NewDataPart(map[string]any{"a": 1}),
	}}
	if _, err := messageText(mixed); err == nil {
		t.Fatal("a data part was accepted")
	} else if !errors.Is(err, a2a.ErrUnsupportedContentType) {
		t.Fatalf("error = %v, want ErrUnsupportedContentType", err)
	}
}

// TestArtifactBuilders: the JSON keys and the text layout.
func TestArtifactBuilders(t *testing.T) {
	result := counts{lines: 2, words: 3, characters: 13}

	jsonPart := jsonCountsPart(result)
	data, ok := jsonPart.Data().(map[string]any)
	if !ok {
		t.Fatalf("json part = %#v", jsonPart)
	}
	if len(data) != 3 || data["lines"] != 2 || data["words"] != 3 || data["characters"] != 13 {
		t.Fatalf("json data = %#v", data)
	}

	textPart := textCountsPart(result)
	want := "lines: 2\nwords: 3\ncharacters: 13"
	if textPart.Text() != want {
		t.Fatalf("text part = %q, want %q", textPart.Text(), want)
	}
	if !strings.Contains(textPart.Text(), "\n") {
		t.Fatal("the text artifact is not multiline")
	}
}

// TestCancelIsTheRefusal: cancelling answers "not cancelable" rather than inventing a state.
func TestCancelIsTheRefusal(t *testing.T) {
	executor := &countExecutor{}
	var gotErr error
	for _, err := range executor.Cancel(context.Background(), &a2asrv.ExecutorContext{}) {
		gotErr = err
	}
	if !errors.Is(gotErr, a2a.ErrTaskNotCancelable) {
		t.Fatalf("cancel error = %v, want ErrTaskNotCancelable", gotErr)
	}
}
