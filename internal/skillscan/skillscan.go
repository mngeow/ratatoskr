// Package skillscan finds the skills installed on this host and describes them.
//
// It is evidence, not capability: it reads files and reports what exists. It never executes a skill or
// a server, never returns a skill's body, and never emits an absolute path, a username or an
// environment value - so nothing it reports can be mistaken for permission to use anything, and the
// caller can show it on another machine without leaking this one's layout.
//
// The daemon builds the roots; this package scans them, one level deep, through an os.Root handle so no
// entry can resolve outside the directory it was given.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No directory has been scanned and no file read.

package skillscan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// The limits' defaults, and the bounds on every string this package emits.
const (
	defaultMaxSkills    = 200
	defaultMaxFileBytes = 64 << 10

	maxNameRunes        = 64
	maxDescriptionRunes = 280
	maxDirNameRunes     = 128
)

// skillFileName is the one file that makes a directory a skill.
const skillFileName = "SKILL.md"

// Root is one directory to scan, as the daemon built it.
type Root struct {
	Harness string // "claude" | "codex"
	Scope   string // "user"
	// Label is how this root is named to a reader: a home-relative form like "~/.claude/skills", never
	// an absolute path.
	Label string
	// Dir is the absolute directory to scan. It is input only: no part of it is ever copied into a
	// Found or a Note.
	Dir string
	// LocationVerified records whether the location is confirmed by the harness's official documentation. The
	// scan copies it; it never decides anything itself.
	LocationVerified bool
	// SkipNames are child directory names this root never lists. The daemon uses it to leave out the
	// ".system" directory under $CODEX_HOME/skills, where Codex may keep skills it ships. That the
	// bundled skills live there is UNVERIFIED (no official page says so); skipping errs toward not
	// showing a bundled skill as the owner's, and each skipped directory is reported as a Note.
	SkipNames []string
}

// Limits bounds one scan.
type Limits struct {
	MaxSkills    int
	MaxFileBytes int64
}

// DefaultLimits is what a caller gets when it has no opinion.
//
// UNTESTED.
func DefaultLimits() Limits {
	return Limits{MaxSkills: defaultMaxSkills, MaxFileBytes: defaultMaxFileBytes}
}

// Found is one skill file that exists.
//
// Every string is cleaned and bounded. Nothing here claims the skill works, is allowed, or would be
// used in a session: it says a file was there and what its front matter called itself.
type Found struct {
	Harness          string
	Scope            string
	Label            string // "<root label>/<directory name>"
	DirName          string
	Name             string // front-matter name, "" when absent or unreadable
	Description      string // front-matter description, cleaned and bounded
	SHA256           string // lowercase hex of the whole SKILL.md bytes
	LocationVerified bool
}

// Note is something that could not be read. The Reason is our own sentence, never a Go error and never
// a path.
type Note struct {
	Label  string
	Reason string
}

// Result is one scan's answer.
//
// RootsAbsent is the normal case of a harness whose skills directory is not there; Notes is the
// abnormal one. A caller can tell them apart with these two lists alone.
type Result struct {
	Found       []Found
	Notes       []Note
	Truncated   bool
	RootsAbsent []string
}

// The reason sentences. They are ours, and none of them can carry a path or a Go error's text.
const (
	reasonSymlink      = "symlink not followed"
	reasonNotRegular   = "SKILL.md is not a regular file"
	reasonTooLarge     = "SKILL.md is larger than 64 KiB; not read"
	reasonUnreadable   = "could not be read"
	reasonSkillDir     = "the skill directory could not be read"
	reasonSkillsDirBad = "the skills directory could not be read"
	reasonSkipped      = "skipped: skills the harness ships are not listed as the owner's"
)

// Scan walks each root once and reports what it found.
//
// Roots are visited in the caller's order and children in name order, so two scans of one tree agree.
// A root that does not exist is normal and is listed in RootsAbsent; anything else that stops a read is
// a Note. Once MaxSkills is reached the scan stops opening files: Truncated is set, and the roots after
// the cut are not visited at all - they are not reported as absent either, because nothing looked.
//
// UNTESTED.
func Scan(roots []Root, limits Limits) Result {
	limits = normaliseLimits(limits)

	var result Result

	for _, root := range roots {
		if limits.MaxSkills <= len(result.Found) {
			// The cut is already reached: this root is never opened, so it is neither found nor absent.
			result.Truncated = true
			break
		}
		scanRoot(root, limits, &result)
	}

	return result
}

// normaliseLimits replaces a non-positive bound with the default, so a zero value can never mean
// "unlimited".
func normaliseLimits(limits Limits) Limits {
	if limits.MaxSkills <= 0 {
		limits.MaxSkills = defaultMaxSkills
	}
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaultMaxFileBytes
	}
	return limits
}

// scanRoot visits one root's immediate children.
func scanRoot(root Root, limits Limits, result *Result) {
	handle, err := os.OpenRoot(root.Dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		result.RootsAbsent = append(result.RootsAbsent, cleanLabel(root.Label))
		return
	case err != nil:
		result.Notes = append(result.Notes, Note{Label: cleanLabel(root.Label), Reason: reasonSkillsDirBad})
		return
	}
	defer func() {
		_ = handle.Close()
	}()

	entries, err := readDirNames(handle)
	if err != nil {
		result.Notes = append(result.Notes, Note{Label: cleanLabel(root.Label), Reason: reasonSkillsDirBad})
		return
	}

	for _, entry := range entries {
		if limits.MaxSkills <= len(result.Found) {
			result.Truncated = true
			return
		}
		scanChild(handle, root, entry, limits, result)
	}
}

// dirEntry is one child's name and whether it is a symlink or a real directory.
type dirEntry struct {
	name string
	// symlink marks an entry that was not followed.
	symlink bool
	// dir marks a real directory that may hold a SKILL.md.
	dir bool
}

// readDirNames lists one root's children in name order.
//
// Listing goes through the root handle, so a directory that is swapped for a link between the listing
// and the open cannot lead anywhere else.
func readDirNames(handle *os.Root) ([]dirEntry, error) {
	dir, err := handle.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = dir.Close()
	}()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	entries := make([]dirEntry, 0, len(names))
	for _, name := range names {
		info, err := handle.Lstat(name)
		if err != nil {
			// An entry that vanished between the listing and the stat is not worth a note.
			continue
		}
		entries = append(entries, dirEntry{
			name:    name,
			symlink: info.Mode()&os.ModeSymlink != 0,
			dir:     info.IsDir(),
		})
	}
	return entries, nil
}

// scanChild inspects one immediate child.
func scanChild(handle *os.Root, root Root, entry dirEntry, limits Limits, result *Result) {
	for _, skipped := range root.SkipNames {
		if entry.name == skipped {
			result.Notes = append(result.Notes, Note{
				Label:  safeLabel(root.Label, entry.name),
				Reason: reasonSkipped,
			})
			return
		}
	}
	if entry.symlink {
		// A symlinked skill directory is not followed even when the link stays inside the root: the rule
		// is about what this package will look through, not about what the kernel would allow.
		result.Notes = append(result.Notes, Note{
			Label:  safeLabel(root.Label, entry.name),
			Reason: reasonSymlink,
		})
		return
	}
	if !entry.dir {
		// A file where a skill directory would be is not a skill, and is not worth saying anything about.
		return
	}

	if !validName(entry.name) {
		// The directory's name cannot be shown safely, so the skill is skipped rather than mangled.
		result.Notes = append(result.Notes, Note{
			Label:  safeLabel(root.Label, entry.name),
			Reason: reasonSkillDir,
		})
		return
	}

	label := cleanLabel(root.Label) + "/" + entry.name

	info, err := handle.Lstat(entry.name + "/" + skillFileName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// A directory without a SKILL.md is simply not a skill.
		return
	case err != nil:
		result.Notes = append(result.Notes, Note{Label: label, Reason: reasonUnreadable})
		return
	case info.Mode()&os.ModeSymlink != 0:
		result.Notes = append(result.Notes, Note{Label: label, Reason: reasonSymlink})
		return
	case !info.Mode().IsRegular():
		result.Notes = append(result.Notes, Note{Label: label, Reason: reasonNotRegular})
		return
	case info.Size() > limits.MaxFileBytes:
		result.Notes = append(result.Notes, Note{Label: label, Reason: reasonTooLarge})
		return
	}

	data, ok := readSkillFile(handle, entry.name, limits, &result.Notes, label)
	if !ok {
		return
	}

	front := ParseFrontMatter(data)
	sum := sha256.Sum256(data)

	result.Found = append(result.Found, Found{
		Harness:          cleanLabel(root.Harness),
		Scope:            cleanLabel(root.Scope),
		Label:            label,
		DirName:          boundRunes(entry.name, maxDirNameRunes),
		Name:             boundRunes(front.Name, maxNameRunes),
		Description:      boundRunes(front.Description, maxDescriptionRunes),
		SHA256:           hex.EncodeToString(sum[:]),
		LocationVerified: root.LocationVerified,
	})
}

// readSkillFile reads one SKILL.md through the root, bounded.
//
// The size was checked from the directory entry; this re-checks what it actually read, so a file that
// grew in between is still refused rather than hashed in part.
func readSkillFile(handle *os.Root, dirName string, limits Limits, notes *[]Note, label string) ([]byte, bool) {
	file, err := handle.Open(dirName + "/" + skillFileName)
	if err != nil {
		*notes = append(*notes, Note{Label: label, Reason: reasonUnreadable})
		return nil, false
	}
	defer func() {
		_ = file.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(file, limits.MaxFileBytes+1))
	if err != nil {
		*notes = append(*notes, Note{Label: label, Reason: reasonUnreadable})
		return nil, false
	}
	if int64(len(data)) > limits.MaxFileBytes {
		*notes = append(*notes, Note{Label: label, Reason: reasonTooLarge})
		return nil, false
	}
	return data, true
}

// validName reports whether a directory name can be shown: valid UTF-8 with no control characters.
func validName(name string) bool {
	if name == "" || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if isControl(r) {
			return false
		}
	}
	return true
}

// isControl reports whether a rune would disturb a terminal line.
func isControl(r rune) bool {
	if r == 0x2028 || r == 0x2029 {
		return true
	}
	return r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// cleanLabel makes a root label safe to show.
func cleanLabel(label string) string {
	return cleanString(label, maxDirNameRunes)
}

// safeLabel builds a note's label from a root label and a name that may be unsafe.
func safeLabel(rootLabel, name string) string {
	return cleanLabel(rootLabel) + "/" + replaceUnsafe(name)
}

// replaceUnsafe shows a name with its unsafe runes replaced by "?", so a note can still say which entry
// it is talking about without letting the raw bytes through.
func replaceUnsafe(name string) string {
	var builder strings.Builder
	for _, r := range name {
		if r == utf8.RuneError || isControl(r) {
			builder.WriteByte('?')
			continue
		}
		builder.WriteRune(r)
	}
	return boundRunes(builder.String(), maxDirNameRunes)
}

// cleanString removes control characters and invalid sequences and bounds the rune count.
func cleanString(value string, max int) string {
	var builder strings.Builder
	builder.Grow(len(value))
	count := 0
	for _, r := range value {
		if r == utf8.RuneError || isControl(r) {
			continue
		}
		builder.WriteRune(r)
		count++
		if count >= max {
			break
		}
	}
	return strings.TrimSpace(builder.String())
}

// boundRunes cleans a value and bounds it to max runes.
func boundRunes(value string, max int) string {
	return cleanString(value, max)
}
