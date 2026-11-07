package entry

// Untested: under GR-3 nothing here has been run. Every case uses a temporary directory; no ssh and
// no daemon is involved.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHosts writes a hosts.json with the given mode and body.
func writeHosts(t *testing.T, dir, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, "hosts.json")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatalf("write hosts.json: %v", err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod hosts.json: %v", err)
	}
	return path
}

// TestReadHostMapMissing: a missing file is the sentinel, not a failure.
func TestReadHostMapMissing(t *testing.T) {
	_, err := ReadHostMap(filepath.Join(t.TempDir(), "hosts.json"))
	if !errors.Is(err, ErrNoHostMap) {
		t.Fatalf("error = %v, want ErrNoHostMap", err)
	}
}

// TestReadHostMapSafety: every safety refusal is a sentence naming the file.
func TestReadHostMapSafety(t *testing.T) {
	t.Run("a symbolic link is refused", func(t *testing.T) {
		dir := t.TempDir()
		target := writeHosts(t, dir, `{"version":1,"hosts":{}}`, 0o600)
		link := filepath.Join(dir, "link.json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		hosts, err := ReadHostMap(link)
		if err != nil {
			t.Fatalf("ReadHostMap returned an error for a link: %v", err)
		}
		if hosts.Problem == "" || !strings.Contains(hosts.Problem, "symbolic link") {
			t.Fatalf("problem = %q, want a link refusal", hosts.Problem)
		}
	})

	for _, mode := range []os.FileMode{0o666, 0o664, 0o622} {
		t.Run("mode "+mode.String()+" is refused", func(t *testing.T) {
			path := writeHosts(t, t.TempDir(), `{"version":1,"hosts":{}}`, mode)
			hosts, err := ReadHostMap(path)
			if err != nil {
				t.Fatalf("ReadHostMap: %v", err)
			}
			if hosts.Problem == "" || !strings.Contains(hosts.Problem, "writable") {
				t.Fatalf("problem = %q, want a writability refusal", hosts.Problem)
			}
		})
	}

	t.Run("0600 is accepted", func(t *testing.T) {
		path := writeHosts(t, t.TempDir(), `{"version":1,"hosts":{"vmb":{"ssh":"me@host","rtk":"/usr/local/bin/rtk"}}}`, 0o600)
		hosts, err := ReadHostMap(path)
		if err != nil {
			t.Fatalf("ReadHostMap: %v", err)
		}
		if hosts.Problem != "" {
			t.Fatalf("problem = %q", hosts.Problem)
		}
		if _, ok := hosts.Hosts["vmb"]; !ok {
			t.Fatalf("hosts = %+v", hosts.Hosts)
		}
	})

	t.Run("an oversize file is refused", func(t *testing.T) {
		body := `{"version":1,"hosts":{},"padding":"` + strings.Repeat("x", MaxHostsBytes) + `"}`
		path := writeHosts(t, t.TempDir(), body, 0o600)
		hosts, err := ReadHostMap(path)
		if err != nil {
			t.Fatalf("ReadHostMap: %v", err)
		}
		if hosts.Problem == "" || !strings.Contains(hosts.Problem, "larger than") {
			t.Fatalf("problem = %q, want an oversize refusal", hosts.Problem)
		}
	})

	t.Run("a directory is refused", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "hosts.json")
		if err := os.Mkdir(path, 0o600); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		hosts, err := ReadHostMap(path)
		if err != nil {
			t.Fatalf("ReadHostMap: %v", err)
		}
		if hosts.Problem == "" || !strings.Contains(hosts.Problem, "not a regular file") {
			t.Fatalf("problem = %q", hosts.Problem)
		}
	})
}

// TestReadHostMapVersionsAndJSON: a version this build does not know disables every host.
func TestReadHostMapVersionsAndJSON(t *testing.T) {
	dir := t.TempDir()

	path := writeHosts(t, dir, `{"version":2,"hosts":{"vmb":{"ssh":"me@host","rtk":"/bin/rtk"}}}`, 0o600)
	hosts, err := ReadHostMap(path)
	if err != nil {
		t.Fatalf("ReadHostMap: %v", err)
	}
	if !strings.Contains(hosts.Problem, "version 2 is not supported") {
		t.Fatalf("problem = %q", hosts.Problem)
	}
	if len(hosts.Hosts) != 0 {
		t.Fatalf("hosts = %+v, want none usable", hosts.Hosts)
	}

	path = writeHosts(t, dir, `{"hosts":{"vmb":{"ssh":"me@host","rtk":"/bin/rtk"}}}`, 0o600)
	hosts, err = ReadHostMap(path)
	if err != nil {
		t.Fatalf("ReadHostMap: %v", err)
	}
	if !strings.Contains(hosts.Problem, "version 0 is not supported") {
		t.Fatalf("a missing version said %q", hosts.Problem)
	}

	path = writeHosts(t, dir, `not json`, 0o600)
	hosts, err = ReadHostMap(path)
	if err != nil {
		t.Fatalf("ReadHostMap: %v", err)
	}
	if !strings.Contains(hosts.Problem, "not valid JSON") {
		t.Fatalf("problem = %q", hosts.Problem)
	}

	// Top-level unknown fields are ignored.
	path = writeHosts(t, dir, `{"version":1,"hosts":{},"comment":"anything"}`, 0o600)
	hosts, err = ReadHostMap(path)
	if err != nil {
		t.Fatalf("ReadHostMap: %v", err)
	}
	if hosts.Problem != "" {
		t.Fatalf("a top-level unknown field was refused: %q", hosts.Problem)
	}
}

// TestReadHostMapOneBadHost: one bad host costs only that host.
func TestReadHostMapOneBadHost(t *testing.T) {
	body := `{"version":1,"hosts":{
		"good":{"ssh":"me@host","rtk":"/usr/local/bin/rtk"},
		"bad":{"ssh":"me@host","rtk":"/bin/rtk","extra":1},
		"missingrtk":{"ssh":"me@host"}
	}}`
	hosts, err := ReadHostMap(writeHosts(t, t.TempDir(), body, 0o600))
	if err != nil {
		t.Fatalf("ReadHostMap: %v", err)
	}
	if hosts.Problem != "" {
		t.Fatalf("a bad host set a file problem: %q", hosts.Problem)
	}

	if hosts.Hosts["good"].Reason != "" {
		t.Fatalf("the good host is unusable: %q", hosts.Hosts["good"].Reason)
	}
	if !strings.Contains(hosts.Hosts["bad"].Reason, "could not be read") {
		t.Fatalf("the unknown field said %q", hosts.Hosts["bad"].Reason)
	}
	if !strings.Contains(hosts.Hosts["missingrtk"].Reason, "rtk must be an absolute path") {
		t.Fatalf("a missing rtk said %q", hosts.Hosts["missingrtk"].Reason)
	}
}

// TestValidateHostPatterns: every accept and reject case in the contract.
func TestValidateHostPatterns(t *testing.T) {
	good := []HostEntry{
		{SSH: "host", RTK: "/bin/rtk"},
		{SSH: "me@192.168.1.23", RTK: "/usr/local/bin/rtk"},
		{SSH: "build-vmc", RTK: "/opt/rtk/bin/rtk", StateHome: "/home/me/.local/state"},
		{SSH: "tailnet-name", RTK: "/bin/rtk"},
		{SSH: "_leading", RTK: "/bin/rtk"},
	}
	for _, entry := range good {
		if reason := validateHost(entry); reason != "" {
			t.Fatalf("validateHost(%+v) = %q", entry, reason)
		}
	}

	bad := []HostEntry{
		{SSH: "-oProxyCommand=x", RTK: "/bin/rtk"},
		{SSH: "a b", RTK: "/bin/rtk"},
		{SSH: "a\nb", RTK: "/bin/rtk"},
		{SSH: "host", RTK: "rtk"},
		{SSH: "host", RTK: "/bin/../rtk"},
		{SSH: "host", RTK: "/bin/rtk --flag"},
		{SSH: "host", RTK: "/"},
		{SSH: "host", RTK: "/bin/rtk", StateHome: "relative"},
		{SSH: "", RTK: "/bin/rtk"},
		{SSH: strings.Repeat("a", 254), RTK: "/bin/rtk"},
	}
	for _, entry := range bad {
		if reason := validateHost(entry); reason == "" {
			t.Fatalf("validateHost(%+v) accepted an unsafe value", entry)
		}
	}
}

// TestIsUUID: the canonical form, and the near misses.
func TestIsUUID(t *testing.T) {
	const good = "0192f2b4-1111-2222-3333-444455556666"
	if !IsUUID(good) {
		t.Fatalf("IsUUID(%q) = false", good)
	}

	for _, bad := range []string{
		"",
		"0192f2b4-1111-2222-3333-44445555666",
		"0192f2b4-1111-2222-3333-4444555566666",
		"0192f2b4111122223333444455556666",
		"zzzzzzzz-1111-2222-3333-444455556666",
		"0192f2b4-1111-2222-3333-44445555666g",
		" 0192f2b4-1111-2222-3333-444455556666",
	} {
		if IsUUID(bad) {
			t.Fatalf("IsUUID(%q) = true", bad)
		}
	}
}

// TestCleanText: control characters go, the length is bounded.
func TestCleanText(t *testing.T) {
	if got := cleanText("a\x00b\x1bc", 10); got != "abc" {
		t.Fatalf("cleanText = %q", got)
	}
	if got := cleanText(strings.Repeat("é", 200), 128); len([]rune(got)) != 128 {
		t.Fatalf("cleaned length = %d", len([]rune(got)))
	}
	if got := cleanText("\x00\x01", 10); got != "" {
		t.Fatalf("cleanText of controls = %q", got)
	}
}
