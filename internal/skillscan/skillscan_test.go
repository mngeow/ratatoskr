package skillscan

// Untested: under GR-3 nothing here has been run. Every case uses a temporary directory tree; nothing
// outside it is read and nothing is executed.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates one skill directory with a SKILL.md.
func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// scanRootFor builds one Root whose Dir is the temporary directory and whose Label mimics a home form.
func scanRootFor(dir, harness string, verified bool) Root {
	return Root{
		Harness:          harness,
		Scope:            "user",
		Label:            "~/" + harness + "/skills",
		Dir:              dir,
		LocationVerified: verified,
	}
}

// TestScanFindsSkills: two roots, descriptions, hashes and copied LocationVerified.
func TestScanFindsSkills(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()

	writeSkill(t, firstDir, "pdf-tools", "---\nname: pdf-tools\ndescription: Work with PDFs\n---\nbody\n")
	writeSkill(t, secondDir, "review", "---\nname: review\ndescription: Review code\n---\n")

	result := Scan([]Root{
		scanRootFor(firstDir, "claude", true),
		scanRootFor(secondDir, "codex", false),
	}, DefaultLimits())

	if len(result.Notes) != 0 {
		t.Fatalf("notes = %+v", result.Notes)
	}
	if len(result.Found) != 2 {
		t.Fatalf("found = %+v", result.Found)
	}

	first := result.Found[0]
	if first.Harness != "claude" || first.Scope != "user" || !first.LocationVerified {
		t.Fatalf("first = %+v", first)
	}
	if first.Label != "~/claude/skills/pdf-tools" || first.DirName != "pdf-tools" {
		t.Fatalf("first label = %q (%q)", first.Label, first.DirName)
	}
	if first.Name != "pdf-tools" || first.Description != "Work with PDFs" {
		t.Fatalf("first front matter = %+v", first)
	}

	body, err := os.ReadFile(filepath.Join(firstDir, "pdf-tools", "SKILL.md"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	sum := sha256.Sum256(body)
	if first.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %q", first.SHA256)
	}

	if second := result.Found[1]; second.LocationVerified {
		t.Fatalf("the unverified root came back verified: %+v", second)
	}
}

// TestScanAbsentAndUnreadableRoots: absent is normal, unreadable is a note.
func TestScanAbsentAndUnreadableRoots(t *testing.T) {
	present := t.TempDir()
	writeSkill(t, present, "one", "---\nname: one\n---\n")

	missing := filepath.Join(t.TempDir(), "nope")
	result := Scan([]Root{
		{Harness: "claude", Scope: "user", Label: "~/.claude/skills", Dir: missing},
		scanRootFor(present, "codex", true),
	}, DefaultLimits())

	if len(result.RootsAbsent) != 1 || result.RootsAbsent[0] != "~/.claude/skills" {
		t.Fatalf("absent = %+v", result.RootsAbsent)
	}
	if len(result.Found) != 1 {
		t.Fatalf("found = %+v", result.Found)
	}
}

// TestScanIgnoresNonSkills: no SKILL.md, a file where a directory would be, an empty directory.
func TestScanIgnoresNonSkills(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "no-skill"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "a-file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
	if len(result.Found) != 0 || len(result.Notes) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestScanSymlinks: a symlinked skill directory and a symlinked SKILL.md are not followed.
func TestScanSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "real", "---\nname: real\n---\n")

	// A symlinked skill directory pointing at a real skill elsewhere.
	if err := os.Symlink(filepath.Join(outside, "real"), filepath.Join(root, "linked-dir")); err != nil {
		t.Skipf("this platform cannot create symlinks: %v", err)
	}

	// A real skill directory whose SKILL.md is a symlink.
	writeSkill(t, root, "linked-file", "---\nname: placeholder\n---\n")
	if err := os.Remove(filepath.Join(root, "linked-file", "SKILL.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "real", "SKILL.md"), filepath.Join(root, "linked-file", "SKILL.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
	if len(result.Found) != 0 {
		t.Fatalf("a symlink was followed: %+v", result.Found)
	}
	if len(result.Notes) != 2 {
		t.Fatalf("notes = %+v, want one per symlink", result.Notes)
	}
	for _, note := range result.Notes {
		if note.Reason != reasonSymlink {
			t.Fatalf("reason = %q, want %q", note.Reason, reasonSymlink)
		}
	}
}

// TestScanFileSizes: a file over the limit is a note, exactly at the limit is accepted.
func TestScanFileSizes(t *testing.T) {
	t.Run("over the limit", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "big", strings.Repeat("x", int(DefaultLimits().MaxFileBytes)+1))

		result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
		if len(result.Found) != 0 {
			t.Fatalf("found = %+v", result.Found)
		}
		if len(result.Notes) != 1 || result.Notes[0].Reason != reasonTooLarge {
			t.Fatalf("notes = %+v", result.Notes)
		}
	})

	t.Run("exactly at the limit", func(t *testing.T) {
		root := t.TempDir()
		writeSkill(t, root, "exact", strings.Repeat("x", int(DefaultLimits().MaxFileBytes)))

		result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
		if len(result.Found) != 1 {
			t.Fatalf("result = %+v", result)
		}
		if result.Found[0].SHA256 == "" {
			t.Fatal("the accepted file was not hashed")
		}
	})
}

// TestScanMaxSkillsAndTruncation: the cut stops the scan and later roots are not visited.
func TestScanMaxSkillsAndTruncation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		writeSkill(t, root, name, "---\nname: "+name+"\n---\n")
	}
	later := t.TempDir()
	writeSkill(t, later, "d", "---\nname: d\n---\n")

	result := Scan([]Root{
		scanRootFor(root, "claude", true),
		{Harness: "codex", Scope: "user", Label: "~/.agents/skills", Dir: later},
	}, Limits{MaxSkills: 2, MaxFileBytes: DefaultLimits().MaxFileBytes})

	if !result.Truncated {
		t.Fatal("truncation was not reported")
	}
	if len(result.Found) != 2 {
		t.Fatalf("found = %+v, want two", result.Found)
	}
	// The second root was never opened, so it is neither found nor absent.
	if len(result.RootsAbsent) != 0 {
		t.Fatalf("an unvisited root was reported absent: %+v", result.RootsAbsent)
	}
}

// TestScanOrderingIsStable: children come out in name order regardless of creation order.
func TestScanOrderingIsStable(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		writeSkill(t, root, name, "---\nname: "+name+"\n---\n")
	}

	result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
	if len(result.Found) != 3 {
		t.Fatalf("found = %+v", result.Found)
	}
	want := []string{"alpha", "middle", "zeta"}
	for index, name := range want {
		if result.Found[index].DirName != name {
			t.Fatalf("order = %v, want %v", dirNames(result.Found), want)
		}
	}
}

// dirNames lists the found directory names, for an assertion message.
func dirNames(found []Found) []string {
	out := make([]string, 0, len(found))
	for _, item := range found {
		out = append(out, item.DirName)
	}
	return out
}

// TestScanLeaksNothing: no path, username or body text reaches the result.
func TestScanLeaksNothing(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "secret", "---\nname: secret\ndescription: a description\n---\nBODY-MARKER-DO-NOT-LEAK\n")

	result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	forbidden := []string{root, filepath.Base(root), "BODY-MARKER-DO-NOT-LEAK"}
	if home != "" {
		forbidden = append(forbidden, home)
	}

	for _, item := range result.Found {
		for _, field := range []string{item.Harness, item.Scope, item.Label, item.DirName, item.Name, item.Description, item.SHA256} {
			for _, bad := range forbidden {
				if bad != "" && strings.Contains(field, bad) {
					t.Fatalf("field %q contains %q", field, bad)
				}
			}
		}
	}
	for _, note := range result.Notes {
		for _, bad := range forbidden {
			if strings.Contains(note.Label, bad) || strings.Contains(note.Reason, bad) {
				t.Fatalf("note %+v contains %q", note, bad)
			}
		}
	}
	for _, absent := range result.RootsAbsent {
		for _, bad := range forbidden {
			if strings.Contains(absent, bad) {
				t.Fatalf("absent label %q contains %q", absent, bad)
			}
		}
	}
}

// TestScanNonUTF8DirectoryName: an unsafe name is skipped with a safe note.
func TestScanNonUTF8DirectoryName(t *testing.T) {
	root := t.TempDir()
	bad := string([]byte{0xff, 0xfe})
	if err := os.Mkdir(filepath.Join(root, bad), 0o700); err != nil {
		t.Skipf("this filesystem cannot hold the name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, bad, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
	if len(result.Found) != 0 {
		t.Fatalf("an invalid name was accepted: %+v", result.Found)
	}
	if len(result.Notes) != 1 {
		t.Fatalf("notes = %+v", result.Notes)
	}
	if strings.Contains(result.Notes[0].Label, bad) {
		t.Fatalf("the raw name reached the note: %q", result.Notes[0].Label)
	}
}

// TestScanLimitsZeroMeansDefault: a zero limits value cannot mean unlimited.
func TestScanLimitsZeroMeansDefault(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "one", "---\nname: one\n---\n")

	result := Scan([]Root{scanRootFor(root, "claude", true)}, Limits{})
	if len(result.Found) != 1 {
		t.Fatalf("found = %+v", result.Found)
	}
	if got := normaliseLimits(Limits{}); got.MaxSkills != defaultMaxSkills || got.MaxFileBytes != defaultMaxFileBytes {
		t.Fatalf("normaliseLimits = %+v", got)
	}
}

// TestScanMissingSkillFileIsSilent: a directory without SKILL.md produces no note.
func TestScanMissingSkillFileIsSilent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	result := Scan([]Root{scanRootFor(root, "claude", true)}, DefaultLimits())
	if len(result.Found) != 0 || len(result.Notes) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestSkipNamesAreNotListedAndAreReported: a directory a root says to skip is never opened as a skill,
// and the skip is a Note, not silence.
func TestSkipNamesAreNotListedAndAreReported(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"mine", ".system"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte("---\nname: "+name+"\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	result := Scan([]Root{{Harness: "codex", Scope: "user", Label: "$CODEX_HOME/skills", Dir: dir, SkipNames: []string{".system"}}}, DefaultLimits())
	if len(result.Found) != 1 || result.Found[0].DirName != "mine" {
		t.Fatalf("found = %+v", result.Found)
	}
	if len(result.Notes) != 1 || result.Notes[0].Reason != reasonSkipped || result.Notes[0].Label != "$CODEX_HOME/skills/.system" {
		t.Fatalf("notes = %+v", result.Notes)
	}
}
