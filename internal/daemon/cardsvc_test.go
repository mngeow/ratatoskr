package daemon

// Tests for the card service (card.yaml and the served card) and the card listener's planning and
// binding. UNTESTED: written, not run (GR-3).

import (
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

const serviceFile = `name: vmb
description: Build host for the mac mini.
skills: []
drafting:
  preset: claude
  argv: []
  pass_env: []
`

func newTestService(t *testing.T) (*cardService, string) {
	t.Helper()
	dir := t.TempDir()
	service := newCardService(dir, func() string { return "vmb" }, &logger{out: io.Discard})
	return service, filepath.Join(dir, cardYAMLName)
}

func writeCardFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func getView(t *testing.T, service *cardService, op string) protocol.CardFileView {
	t.Helper()
	result := service.Do(wire.CardOp{Op: op}, "w1")
	if result.Error != nil || result.Card == nil {
		t.Fatalf("%s: %+v", op, result)
	}
	return *result.Card
}

func listening(service *cardService) {
	service.setListener(protocol.CardListener{
		State: protocol.CardListenerListening, Addr: "127.0.0.1:7778",
		URL: "http://127.0.0.1:7778" + protocol.CardPath,
	})
}

// TestFirstStartWritesATemplateThatServesNothing: the file is mode 0600, needs a description, and no
// card is served even with the listener up.
func TestFirstStartWritesATemplateThatServesNothing(t *testing.T) {
	service, path := newTestService(t)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("template: %v, %v", info, err)
	}
	listening(service)
	view := getView(t, service, wire.CardOpGet)
	if view.File.State != protocol.CardFileNeedsDescription || view.File.Line == 0 {
		t.Fatalf("file = %+v", view.File)
	}
	if view.Served != nil {
		t.Fatalf("served = %+v", view.Served)
	}
	if _, _, ok := service.servedBytes(); ok {
		t.Fatal("a card was served from the template")
	}
	if view.Path != path || view.Readable != protocol.CardReadableNotice {
		t.Fatalf("path/readable = %q / %q", view.Path, view.Readable)
	}
}

// TestTemplateIsNeverWrittenOverAnExistingFile.
func TestTemplateIsNeverWrittenOverAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	writeCardFile(t, filepath.Join(dir, cardYAMLName), serviceFile)
	newCardService(dir, func() string { return "vmb" }, &logger{out: io.Discard})
	data, _ := os.ReadFile(filepath.Join(dir, cardYAMLName))
	if string(data) != serviceFile {
		t.Fatalf("file was replaced:\n%s", data)
	}
}

// TestAValidFileIsServedWithoutPrivateConfiguration: the served bytes parse strictly and carry no
// drafting section.
func TestAValidFileIsServedWithoutPrivateConfiguration(t *testing.T) {
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	listening(service)
	getView(t, service, wire.CardOpReload)

	body, etag, ok := service.servedBytes()
	if !ok || etag == "" {
		t.Fatal("nothing served")
	}
	text := string(body)
	for _, word := range []string{"drafting", "preset", "pass_env", "argv"} {
		if strings.Contains(text, word) {
			t.Fatalf("served JSON contains %q:\n%s", word, text)
		}
	}
	parsed, problems := card.Parse(body, card.Strict, true)
	if len(problems) > 0 || parsed.Card.SupportedInterfaces[0].URL != "http://127.0.0.1:7778"+protocol.CardPath {
		t.Fatalf("served card: %+v %+v", problems, parsed.Card)
	}
	if again, tag, ok := service.servedBytes(); !ok || tag != etag || string(again) != text {
		t.Fatal("the served bytes or ETag changed with no edit")
	}
}

// TestABadEditKeepsTheLastGoodCardAndReportsTheLine.
func TestABadEditKeepsTheLastGoodCardAndReportsTheLine(t *testing.T) {
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	listening(service)
	getView(t, service, wire.CardOpReload)
	before, _, _ := service.servedBytes()

	writeCardFile(t, path, serviceFile+"surprise: 1\n")
	view := getView(t, service, wire.CardOpReload)
	if view.File.State != protocol.CardFileInvalid || view.File.Line != 8 {
		t.Fatalf("file = %+v", view.File)
	}
	if view.Served == nil || view.Served.Current || view.Served.Card.Description != "Build host for the mac mini." {
		t.Fatalf("served = %+v", view.Served)
	}
	after, _, ok := service.servedBytes()
	if !ok || string(after) != string(before) {
		t.Fatal("the last good card is no longer served")
	}
}

// TestAnEditIsNoticedByTheStat: no reload operation is needed when the modification time moves.
func TestAnEditIsNoticedByTheStat(t *testing.T) {
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	listening(service)
	getView(t, service, wire.CardOpReload)
	first, firstTag, _ := service.servedBytes()

	writeCardFile(t, path, strings.Replace(serviceFile, "Build host", "Test host", 1))
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	second, secondTag, ok := service.servedBytes()
	if !ok || secondTag == firstTag || string(second) == string(first) || !strings.Contains(string(second), "Test host") {
		t.Fatalf("edit not served: ok=%v\n%s", ok, second)
	}
}

// TestACardIsNeverServedUnlessTheListenerIsListening: off and not bound serve nothing, and the view
// reports each state.
func TestACardIsNeverServedUnlessTheListenerIsListening(t *testing.T) {
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	getView(t, service, wire.CardOpReload)

	for _, listener := range []protocol.CardListener{
		{State: protocol.CardListenerOff},
		{State: protocol.CardListenerNotBound, Reason: "the port is already in use"},
	} {
		service.setListener(listener)
		if _, _, ok := service.servedBytes(); ok {
			t.Fatalf("served with the listener %s", listener.State)
		}
		if view := getView(t, service, wire.CardOpGet); view.Listener != listener {
			t.Fatalf("listener = %+v, want %+v", view.Listener, listener)
		}
	}
}

func TestAMissingOrNonRegularFileIsReported(t *testing.T) {
	service, path := newTestService(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if view := getView(t, service, wire.CardOpReload); view.File.State != protocol.CardFileMissing {
		t.Fatalf("file = %+v", view.File)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if view := getView(t, service, wire.CardOpReload); view.File.State != protocol.CardFileInvalid {
		t.Fatalf("directory: %+v", view.File)
	}
}

// TestTheTokenFollowsTheFileBytes.
func TestTheTokenFollowsTheFileBytes(t *testing.T) {
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	one := getView(t, service, wire.CardOpReload).Token
	writeCardFile(t, path, serviceFile+"# comment\n")
	two := getView(t, service, wire.CardOpReload).Token
	if one == "" || one == two {
		t.Fatalf("tokens %q %q", one, two)
	}
}

func TestPlanCardListen(t *testing.T) {
	addr, on, err := planCardListen("")
	if err != nil || !on || addr != netip.MustParseAddrPort("0.0.0.0:7778") {
		t.Fatalf("default: %v %v %v", addr, on, err)
	}
	for _, value := range []string{"off", "OFF", " off "} {
		if _, on, err := planCardListen(value); err != nil || on {
			t.Fatalf("%q: on=%v err=%v", value, on, err)
		}
	}
	if addr, on, err := planCardListen("100.64.0.5:9000"); err != nil || !on || addr.Port() != 9000 {
		t.Fatalf("explicit: %v %v %v", addr, on, err)
	}
	for _, value := range []string{"7778", "localhost:7778", "0.0.0.0", ":7778", "example.com:80"} {
		if _, _, err := planCardListen(value); err == nil {
			t.Fatalf("%q accepted", value)
		}
	}
}

// TestABindFailureIsAStateNotAFatalError: a port in use is "not bound" with a reason, and a free port
// is "listening" with an address and a card URL of the accepted shape.
func TestABindFailureIsAStateNotAFatalError(t *testing.T) {
	bound, view := bindCardListener(netip.MustParseAddrPort("127.0.0.1:0"))
	if bound == nil || view.State != protocol.CardListenerListening || !card.ValidURL(view.URL) {
		t.Fatalf("free port: %+v", view)
	}
	defer bound.listener.Close()

	taken := netip.MustParseAddrPort(view.Addr)
	again, failed := bindCardListener(taken)
	if again != nil || failed.State != protocol.CardListenerNotBound || failed.Reason == "" {
		t.Fatalf("taken port: %+v", failed)
	}
	if strings.Contains(failed.Reason, view.Addr) {
		t.Fatalf("reason repeats the operating system's text: %q", failed.Reason)
	}
}

func TestAdvertiseAddress(t *testing.T) {
	specific := netip.MustParseAddrPort("100.64.0.5:7778")
	if got := advertiseAddress(specific); got != specific {
		t.Fatalf("specific address changed: %v", got)
	}
	got := advertiseAddress(netip.MustParseAddrPort("0.0.0.0:7778"))
	if got.Port() != 7778 || got.Addr().IsUnspecified() || !card.ValidURL("http://"+got.String()+protocol.CardPath) {
		t.Fatalf("wildcard advertised %v", got)
	}
}
