// UNTESTED (GR-3): written, never run.

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pin = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func write(t *testing.T, mode os.FileMode, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func good(extra string) string {
	return `{"config_version":1,"participant_url":"https://100.64.0.1:7790","certificate_sha256":"sha256:` + pin + `",
	"participant":"openrig-main","session":"operator","operator_seat":"operator-agent@kernel",
	"rig_path":"/opt/rig/bin/rig","rig_env_path":"/opt/node/bin:/usr/bin","openrig_home":"/Users/x/.openrig"` + extra + `}`
}

func TestLoadAppliesDefaults(t *testing.T) {
	c, err := Load(write(t, 0o600, good("")), false)
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.InlineBodyBytes != 4096 || c.Limits.PerSenderPerMinute != 6 || c.Limits.TotalPerMinute != 20 || c.ReadyCheckTimeoutMS != 1500 {
		t.Fatalf("defaults = %+v", c)
	}
	if c.Scheme != "https" || c.Host != "100.64.0.1:7790" || c.Pin[0] != 0x01 {
		t.Fatalf("derived = %+v", c)
	}
	if !strings.HasSuffix(c.StateDir, "/.local/state/ratatoskr-openrig") || filepath.Base(c.CredentialPath) != "credential" {
		t.Fatalf("paths = %s %s", c.StateDir, c.CredentialPath)
	}
}

func TestLoadRefusals(t *testing.T) {
	tests := []struct {
		name    string
		mode    os.FileMode
		content string
		http    bool
		want    string
	}{
		{"group writable", 0o666, good(""), false, "writable by group"},
		{"unknown field", 0o600, good(`,"surprise":1`), false, "unknown field"},
		{"trailing content", 0o600, good("") + `{}`, false, "after the JSON"},
		{"key pin", 0o600, strings.Replace(good(""), "sha256:"+pin, "sha256//AAAA", 1), false, "key pin"},
		{"https without pin", 0o600, strings.Replace(good(""), `"certificate_sha256":"sha256:`+pin+`",`, "", 1), false, "certificate_sha256"},
		{"http needs the flag", 0o600, strings.Replace(strings.Replace(good(""), "https", "http", 1), `"certificate_sha256":"sha256:`+pin+`",`, "", 1), false, "--insecure-http"},
		{"http with a pin", 0o600, strings.Replace(good(""), "https", "http", 1), true, "forbidden"},
		{"url with a path", 0o600, strings.Replace(good(""), "7790", "7790/x", 1), false, "base URL"},
		{"relative rig path", 0o600, strings.Replace(good(""), "/opt/rig/bin/rig", "rig", 1), false, "rig_path"},
		{"tilde path", 0o600, strings.Replace(good(""), "/Users/x/.openrig", "~/.openrig", 1), false, "openrig_home"},
		{"bad session", 0o600, strings.Replace(good(""), `"operator"`, `"Operator"`, 1), false, "session"},
		{"limit out of range", 0o600, good(`,"limits":{"inline_body_bytes":70000}`), false, "inline_body_bytes"},
		{"ready timeout out of range", 0o600, good(`,"ready_check_timeout_ms":5000`), false, "ready_check_timeout_ms"},
		{"state dir too long", 0o600, good(`,"state_dir":"/` + strings.Repeat("a", 100) + `"`), false, "too long"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(write(t, tc.mode, tc.content), tc.http)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
			if strings.Contains(err.Error(), pin) {
				t.Fatal("the error echoes the pin")
			}
		})
	}
}

func TestPlainHTTPWithTheFlag(t *testing.T) {
	content := strings.Replace(strings.Replace(good(""), "https", "http", 1), `"certificate_sha256":"sha256:`+pin+`",`, "", 1)
	c, err := Load(write(t, 0o600, content), true)
	if err != nil || c.Scheme != "http" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestCheckStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	c := &Config{StateDir: dir}
	if err := c.CheckStateDir(true); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckStateDir(false); err == nil {
		t.Fatal("a 0755 state directory was accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := (&Config{StateDir: link}).CheckStateDir(false); err == nil {
		t.Fatal("a symlinked state directory was accepted")
	}
}
