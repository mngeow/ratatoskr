package setup

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

const hex64 = "abababababababababababababababababababababababababababababababab"

func block(rotation bool, url string, cert, key bool) []byte {
	var b strings.Builder
	b.WriteString("PAIR-CODE-123\n")
	b.WriteString("participant: openrig-main\n")
	b.WriteString("expires in 10 minutes; single use\n")
	if rotation {
		b.WriteString("this is a rotation: redeeming the code replaces the credential and ends this participant's open sessions\n")
	}
	b.WriteString("participant API: " + url + "\n")
	if cert {
		b.WriteString("certificate SHA-256 (the pin to use when your TLS library can pin a certificate): sha256:" + hex64 + "\n")
	}
	if key {
		b.WriteString("key pin (a different rule, for clients that can pin only a public key): sha256//QUJD\n")
	}
	b.WriteString("the address above was recorded by the running gateway at its start; if the gateway has stopped or moved, it is stale (checked: /x)\n")
	return []byte(b.String())
}

func TestParsePasteHTTPS(t *testing.T) {
	p, err := ParsePaste(block(false, "https://10.0.0.5:7790", true, true))
	if err != nil {
		t.Fatal(err)
	}
	if string(p.Code) != "PAIR-CODE-123" || p.Participant != "openrig-main" || p.URL != "https://10.0.0.5:7790" {
		t.Fatalf("unexpected %+v", p)
	}
	if p.Pin != "sha256:"+hex64 || p.Rotation || !p.KeyPinSeen || p.StaleNote == "" {
		t.Fatalf("unexpected %+v", p)
	}
}

func TestParsePasteRotation(t *testing.T) {
	p, err := ParsePaste(block(true, "https://10.0.0.5:7790", true, true))
	if err != nil || !p.Rotation {
		t.Fatalf("rotation not seen: %v %+v", err, p)
	}
}

func TestParsePastePlainHTTP(t *testing.T) {
	p, err := ParsePaste(block(false, "http://10.0.0.5:7790", false, false))
	if err != nil || !p.Plain || p.Pin != "" {
		t.Fatalf("unexpected %v %+v", err, p)
	}
	if err := p.CheckAgainst("openrig-main"); err != nil {
		t.Fatal("a plain http block must parse and check: consent is asked later")
	}
}

func TestParsePasteKeyPinOnly(t *testing.T) {
	_, err := ParsePaste(block(false, "https://10.0.0.5:7790", false, true))
	if err == nil || !strings.Contains(err.Error(), "certificate SHA-256") {
		t.Fatalf("key-pin-only block must name the certificate line: %v", err)
	}
}

func TestParsePasteWrongParticipant(t *testing.T) {
	p, err := ParsePaste(block(false, "https://10.0.0.5:7790", true, true))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckAgainst("other-name"); err == nil {
		t.Fatal("a block for another participant must be refused")
	}
}

func TestParsePasteWithoutCode(t *testing.T) {
	_, err := ParsePaste([]byte("participant: openrig-main\nparticipant API: https://h:1\n"))
	if err == nil {
		t.Fatal("a block without the code line must be refused")
	}
}

func TestParsePasteNeverEchoesCode(t *testing.T) {
	_, err := ParsePaste([]byte("SECRET-CODE\nparticipant: openrig-main\n"))
	if err == nil || strings.Contains(err.Error(), "SECRET-CODE") {
		t.Fatalf("error must exist and not contain the code: %v", err)
	}
}

func TestBuildEnvPath(t *testing.T) {
	look := func(name string) (string, error) {
		switch name {
		case "rig", "node":
			return "/opt/node/bin/" + name, nil
		case "tmux":
			return "/usr/bin/tmux", nil
		}
		return "", errors.New("not found")
	}
	got, missing := BuildEnvPath(look, func(s string) (string, error) { return s, nil })
	if got != "/opt/node/bin:/usr/bin:/bin" {
		t.Fatalf("got %q", got)
	}
	if len(missing) != 1 || missing[0] != "herdr" {
		t.Fatalf("missing %v", missing)
	}
}

func TestRenderPlistEscapesAndFills(t *testing.T) {
	tmpl := "<string>__BINARY__</string><string>__CONFIG__</string><string>__STATE_DIR__</string><string>__LOG_DIR__</string><string>__HOME__</string>"
	out, err := RenderPlist(tmpl, PlistValues{Binary: "/a&b/bin", Config: "/c<d>/cfg", StateDir: "/s", LogDir: "/l", Home: "/h"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/a&amp;b/bin") || !strings.Contains(out, "/c&lt;d&gt;/cfg") || strings.Contains(out, "__") {
		t.Fatalf("unexpected %q", out)
	}
	if _, err := RenderPlist(tmpl, PlistValues{Binary: "relative", Config: "/c", StateDir: "/s", LogDir: "/l", Home: "/h"}); err == nil {
		t.Fatal("a relative path must be refused")
	}
	if _, err := RenderPlist(tmpl+"__UNKNOWN__", PlistValues{Binary: "/b", Config: "/c", StateDir: "/s", LogDir: "/l", Home: "/h"}); err == nil {
		t.Fatal("an unknown placeholder must be refused")
	}
}

func goodConfig(dir string) *config.Config {
	return &config.Config{
		ConfigVersion: config.Version, ParticipantURL: "https://10.0.0.5:7790", CertificateSHA256: "sha256:" + hex64,
		Participant: "openrig-main", Session: "operator", OperatorSeat: "operator-agent@kernel",
		RigPath: "/usr/bin/rig", RigEnvPath: "/usr/bin:/bin", OpenRigHome: "/tmp/openrig",
		StateDir: dir, CredentialPath: filepath.Join(dir, "credential"), Home: dir,
	}
}

func TestWriteConfigAtomicAndBackup(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	now := time.Date(2026, 11, 9, 1, 2, 3, 0, time.UTC)
	backup, err := WriteConfig(path, goodConfig(dir), false, now)
	if err != nil || backup != "" {
		t.Fatalf("first write: %v %q", err, backup)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v %v", err, info)
	}
	changed := goodConfig(dir)
	changed.OperatorSeat = "other@kernel"
	backup, err = WriteConfig(path, changed, false, now)
	if err != nil || backup == "" {
		t.Fatalf("second write: %v %q", err, backup)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if len(DiffConfig(goodConfig(dir), changed)) != 1 {
		t.Fatal("expected exactly one changed field")
	}
	bad := goodConfig(dir)
	bad.CertificateSHA256 = "sha256//QUJD"
	before, _ := os.ReadFile(path)
	if _, err := WriteConfig(path, bad, false, now); err == nil {
		t.Fatal("a key pin must not be written")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a refused write must leave the file untouched")
	}
}

func TestShellQuote(t *testing.T) {
	if shellQuote("/a/b-c.d") != "/a/b-c.d" {
		t.Fatal("plain path should stay unquoted")
	}
	if got := shellQuote("/a b/it's"); got != `'/a b/it'\''s'` {
		t.Fatalf("got %s", got)
	}
}

func TestParsePasteAcceptsCRCRLFAndLF(t *testing.T) {
	lf := block(false, "https://10.0.0.5:7790", true, true)
	for name, text := range map[string][]byte{
		"LF":   lf,
		"CRLF": bytes.ReplaceAll(lf, []byte("\n"), []byte("\r\n")),
		"CR":   bytes.ReplaceAll(lf, []byte("\n"), []byte("\r")),
	} {
		p, err := ParsePaste(append([]byte(nil), text...))
		if err != nil || p.Participant != "openrig-main" || p.URL != "https://10.0.0.5:7790" || p.Pin != "sha256:"+hex64 ||
			p.StaleNote == "" || len(p.Code) == 0 || bytes.ContainsAny(p.Code, "\r\n") {
			t.Errorf("%s: %v %+v", name, err, p)
		}
	}
}
