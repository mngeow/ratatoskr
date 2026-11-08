package skillscan

// Untested: under GR-3 nothing here has been run.

import (
	"strings"
	"testing"
)

// TestParseFrontMatterShape: every shape the reader claims to understand, and the ones it refuses.
func TestParseFrontMatterShape(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		wantName        string
		wantDescription string
		wantProblem     string
	}{
		{
			name:            "plain scalars",
			input:           "---\nname: pdf-tools\ndescription: Work with PDF files\n---\nbody\n",
			wantName:        "pdf-tools",
			wantDescription: "Work with PDF files",
		},
		{
			name:            "single quoted",
			input:           "---\nname: 'it''s a skill'\ndescription: 'quoted: yes'\n---\n",
			wantName:        "it's a skill",
			wantDescription: "quoted: yes",
		},
		{
			name:            "double quoted with escapes",
			input:           "---\nname: \"a \\\"quoted\\\" name\"\ndescription: \"line\\nnext\\ttabbed\"\n---\n",
			wantName:        `a "quoted" name`,
			wantDescription: "line next tabbed",
		},
		{
			name:            "an unknown escape is kept",
			input:           "---\ndescription: \"a \\q b\"\n---\n",
			wantDescription: `a \q b`,
		},
		{
			name:            "folded block",
			input:           "---\ndescription: >\n  first line\n  second line\n---\n",
			wantDescription: "first line second line",
		},
		{
			name:            "literal block",
			input:           "---\ndescription: |\n  first line\n  second line\n---\n",
			wantDescription: "first line second line",
		},
		{
			name:            "block with an indicator",
			input:           "---\ndescription: >-\n  folded\n---\n",
			wantDescription: "folded",
		},
		{
			name:            "CRLF",
			input:           "---\r\nname: crlf\r\ndescription: yes\r\n---\r\n",
			wantName:        "crlf",
			wantDescription: "yes",
		},
		{
			name:     "a BOM before the delimiter",
			input:    "\ufeff---\nname: bom\n---\n",
			wantName: "bom",
		},
		{
			name:        "no front matter",
			input:       "just a body\n",
			wantProblem: problemNoFrontMatter,
		},
		{
			name:        "unclosed",
			input:       "---\nname: x\n",
			wantProblem: problemUnclosed,
		},
		{
			name:        "only the delimiter",
			input:       "---\n",
			wantProblem: problemUnclosed,
		},
		{
			name:        "empty input",
			input:       "",
			wantProblem: problemNoFrontMatter,
		},
		{
			name:     "other keys are ignored",
			input:    "---\nname: x\nallowed-tools: [Bash]\nmodel: sonnet\nnested:\n  a: 1\n---\n",
			wantName: "x",
		},
		{
			name:            "duplicate keys keep the first",
			input:           "---\nname: first\nname: second\ndescription: one\ndescription: two\n---\n",
			wantName:        "first",
			wantDescription: "one",
		},
		{
			name:            "a plain comment is stripped",
			input:           "---\nname: x # the name\ndescription: y # a note\n---\n",
			wantName:        "x",
			wantDescription: "y",
		},
		{
			name:     "a hash inside a word stays",
			input:    "---\nname: c#sharp\n---\n",
			wantName: "c#sharp",
		},
		{
			name:     "empty values",
			input:    "---\nname:\ndescription:\n---\n",
			wantName: "",
		},
		{
			name:            "an unterminated quote is refused",
			input:           "---\ndescription: \"never closed\n---\n",
			wantProblem:     problemUnsupported,
			wantDescription: "",
		},
		{
			name:     "a line that is not a key line is ignored",
			input:    "---\nname: ok\n::: broken\n---\n",
			wantName: "ok",
		},
		{
			name:            "blank lines, comments and list items are ignored",
			input:           "---\n# a comment\n\nname: ok\ntags:\n- one\n- two\ndescription: fine\n---\n",
			wantName:        "ok",
			wantDescription: "fine",
		},
		{
			name:            "a quoted value may carry a trailing comment",
			input:           "---\nname: \"quoted\" # why\ndescription: 'x' #y\n---\n",
			wantName:        "quoted",
			wantDescription: "x",
		},
		{
			name:        "text after a closing quote is refused",
			input:       "---\nname: \"a\" b\n---\n",
			wantProblem: problemUnsupported,
		},
		{
			name:            "a plain value continues on indented lines",
			input:           "---\ndescription: first part\n  second part\nname: n\n---\n",
			wantName:        "n",
			wantDescription: "first part second part",
		},
		{
			name:            "a plain value may start on the next line",
			input:           "---\ndescription:\n  all on the next line\n---\n",
			wantDescription: "all on the next line",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseFrontMatter([]byte(tc.input))
			if got.Name != tc.wantName {
				t.Fatalf("name = %q, want %q", got.Name, tc.wantName)
			}
			if got.Description != tc.wantDescription {
				t.Fatalf("description = %q, want %q", got.Description, tc.wantDescription)
			}
			if got.Problem != tc.wantProblem {
				t.Fatalf("problem = %q, want %q", got.Problem, tc.wantProblem)
			}
		})
	}
}

// TestParseFrontMatterWindow: a closing delimiter beyond the window is not found.
func TestParseFrontMatterWindow(t *testing.T) {
	padding := strings.Repeat("x", frontMatterWindow)
	input := "---\nname: late\ndescription: " + padding + "\n---\n"

	got := ParseFrontMatter([]byte(input))
	if got.Problem != problemUnclosed {
		t.Fatalf("problem = %q, want %q", got.Problem, problemUnclosed)
	}
}

// TestParseFrontMatterHostileInputs: nothing panics and nothing loops.
func TestParseFrontMatterHostileInputs(t *testing.T) {
	inputs := []string{
		"",
		"---",
		"---\n",
		"---\n---",
		"---\nname\n---\n",
		"---\n" + strings.Repeat("-", 1000) + "\n---\n",
		"---\n" + strings.Repeat("\n", 100) + "---\n",
		"\x00\x01\x02\xff\xfe",
		"---\ndescription: " + strings.Repeat("|", 500) + "\n---\n",
		"---\ndescription: >\n" + strings.Repeat("  folded\n", 1000) + "---\n",
		"---\r\nname: x\r\n---\r\n",
	}

	for index, input := range inputs {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("input %d panicked: %v", index, recovered)
				}
			}()
			got := ParseFrontMatter([]byte(input))
			// Whatever it decided, the strings it returns must be safe to show.
			if strings.ContainsAny(got.Name, "\x00\x01") || strings.ContainsAny(got.Description, "\x00\x01") {
				t.Fatalf("input %d returned control characters: %+v", index, got)
			}
		}()
	}
}

// TestParseFrontMatterControlCharactersInValues: a value's control characters never reach the caller.
func TestParseFrontMatterControlCharactersInValues(t *testing.T) {
	got := ParseFrontMatter([]byte("---\nname: a\x1bb\ndescription: c\x07d\n---\n"))
	if strings.ContainsRune(got.Name, 0x1b) || strings.ContainsRune(got.Description, 0x07) {
		t.Fatalf("control characters survived: %+v", got)
	}
}

// TestParseFrontMatterOverLength: the fields are bounded by the caller, and ParseFrontMatter itself
// returns what it read.
func TestParseFrontMatterOverLength(t *testing.T) {
	long := strings.Repeat("é", 400)
	got := ParseFrontMatter([]byte("---\ndescription: " + long + "\n---\n"))
	if got.Description == "" {
		t.Fatal("the description was dropped")
	}
	if got.Problem != "" {
		t.Fatalf("problem = %q", got.Problem)
	}
}
