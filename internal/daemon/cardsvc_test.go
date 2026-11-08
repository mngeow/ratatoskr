package daemon

// Tests for the card service (card.yaml and the served card) and the card listener's planning and
// binding. UNTESTED: written, not run (GR-3).

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

// TestFirstStartServesTheDefaultCard: the template is mode 0600, is served with the default description,
// carries no skills and no drafting keys, and the file is never rewritten.
func TestFirstStartServesTheDefaultCard(t *testing.T) {
	service, path := newTestService(t)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("template: %v, %v", info, err)
	}
	before, _ := os.ReadFile(path)
	listening(service)
	view := getView(t, service, wire.CardOpGet)
	if view.File.State != protocol.CardFileDefaulted || view.File.Line != 0 {
		t.Fatalf("file = %+v", view.File)
	}
	if view.Served == nil {
		t.Fatal("nothing served from the template")
	}
	body, reason := service.serve()
	if body == nil || reason != "" {
		t.Fatalf("serve: %q", reason)
	}
	for _, banned := range []string{"drafting", "pass_env", "preset"} {
		if strings.Contains(string(body), banned) {
			t.Fatalf("served card carries %q", banned)
		}
	}
	if !strings.Contains(string(body), "The owner has not written a description.") || strings.Contains(string(body), `"skills":[{`) {
		t.Fatalf("served card = %s", body)
	}
	if _, problems := card.Parse(body, card.Strict, true); len(problems) > 0 {
		t.Fatalf("served card fails Strict: %+v", problems)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("the file was rewritten")
	}
	if view.Path != path || view.Readable != protocol.CardReadableNotice {
		t.Fatalf("path/readable = %q / %q", view.Path, view.Readable)
	}
}

// TestExistingEmptyDescriptionFileIsServedUntouched: a file written by the old template, and a hand-edited one.
func TestExistingEmptyDescriptionFileIsServedUntouched(t *testing.T) {
	service, path := newTestService(t)
	old := "name: vmb\ndescription: \"\"\nversion: \"1\"\nskills: []\n# my note\n"
	writeCardFile(t, path, old)
	listening(service)
	if body, reason := service.serve(); body == nil {
		t.Fatalf("not served: %s", reason)
	}
	if after, _ := os.ReadFile(path); string(after) != old {
		t.Fatalf("file changed: %q", after)
	}
}

// TestUnavailableReasons: a card-less 503 names a fixed reason and nothing from the file.
func TestUnavailableReasons(t *testing.T) {
	const secret = "sk-ant-api03-SECRETSECRETSECRET"
	service, path := newTestService(t)
	listening(service)
	writeCardFile(t, path, "name: vmb\ndescription: "+secret+"\nbogus: 1\n")
	service.mu.Lock()
	service.refresh(true)
	service.mu.Unlock()
	handler := newCardServer(service, &logger{out: io.Discard}).handler()
	get := func(method string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(method, protocol.CardPath, nil))
		return recorder
	}
	got := get(http.MethodGet)
	if got.Code != 503 || got.Header().Get("Retry-After") != "5" || got.Header().Get("Cache-Control") != "no-store" ||
		!strings.HasPrefix(got.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("503 = %d %v", got.Code, got.Header())
	}
	body := got.Body.String()
	if len(body) > 1024 || !strings.Contains(body, `"error":"card_unavailable"`) || !strings.Contains(body, `"reason":"owner_file_invalid"`) {
		t.Fatalf("body = %q", body)
	}
	for _, leaked := range []string{secret, path, "line", "bogus", "drafting"} {
		if strings.Contains(body, leaked) {
			t.Fatalf("body leaks %q: %s", leaked, body)
		}
	}
	if head := get(http.MethodHead); head.Code != 503 || head.Body.Len() != 0 {
		t.Fatalf("HEAD = %d, %d bytes", head.Code, head.Body.Len())
	}
	if post := get(http.MethodPost); post.Code != 405 {
		t.Fatalf("POST = %d", post.Code)
	}

	// Missing file, and no listener URL.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, reason := service.serve(); reason != unavailableOwnerFileMissing {
		t.Fatalf("missing: %q", reason)
	}
	service2, _ := newTestService(t)
	if _, reason := service2.serve(); reason != unavailableListenerURL {
		t.Fatalf("no listener: %q", reason)
	}
}

// TestWildcardAdvertiseServesAValidCard: whatever the interfaces are, the URL the card carries passes
// the Strict parse, so the default bind serves.
func TestWildcardAdvertiseServesAValidCard(t *testing.T) {
	address := advertiseAddress(netip.MustParseAddrPort("0.0.0.0:7778"))
	service, _ := newTestService(t)
	service.setListener(protocol.CardListener{
		State: protocol.CardListenerListening, Addr: "0.0.0.0:7778",
		URL: "http://" + address.String() + protocol.CardPath,
	})
	body, reason := service.serve()
	if body == nil {
		t.Fatalf("not served (%s) for %s", reason, address)
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

	body, ok := service.servedBytes()
	if !ok {
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
	if again, ok := service.servedBytes(); !ok || string(again) != text {
		t.Fatal("the served bytes changed with no edit")
	}
}

// TestABadEditKeepsTheLastGoodCardAndReportsTheLine.
func TestABadEditKeepsTheLastGoodCardAndReportsTheLine(t *testing.T) {
	service, path := newTestService(t)
	writeCardFile(t, path, serviceFile)
	listening(service)
	getView(t, service, wire.CardOpReload)
	before, _ := service.servedBytes()

	writeCardFile(t, path, serviceFile+"surprise: 1\n")
	view := getView(t, service, wire.CardOpReload)
	if view.File.State != protocol.CardFileInvalid || view.File.Line != 8 {
		t.Fatalf("file = %+v", view.File)
	}
	if view.Served == nil || view.Served.Current || view.Served.Card.Description != "Build host for the mac mini." {
		t.Fatalf("served = %+v", view.Served)
	}
	after, ok := service.servedBytes()
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
	first, _ := service.servedBytes()

	writeCardFile(t, path, strings.Replace(serviceFile, "Build host", "Test host", 1))
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	second, ok := service.servedBytes()
	if !ok || string(second) == string(first) || !strings.Contains(string(second), "Test host") {
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
		if _, ok := service.servedBytes(); ok {
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

// TestAdvertiseFromInterfaces: every interface list, including none and an error, gives a URL that
// passes the Strict parse, and the order is Tailscale, then private LAN, then other, then loopback.
func TestAdvertiseFromInterfaces(t *testing.T) {
	wildcard := netip.MustParseAddrPort("0.0.0.0:7778")
	addrs := func(cidrs ...string) func() ([]net.Addr, error) {
		return func() ([]net.Addr, error) {
			var out []net.Addr
			for _, cidr := range cidrs {
				ip, network, err := net.ParseCIDR(cidr)
				if err != nil {
					t.Fatal(err)
				}
				network.IP = ip
				out = append(out, network)
			}
			return out, nil
		}
	}
	cases := []struct {
		name       string
		interfaces func() ([]net.Addr, error)
		want       string
	}{
		{"tailscale before lan", addrs("192.168.1.9/24", "100.101.102.103/32"), "100.101.102.103:7778"},
		{"lan", addrs("127.0.0.1/8", "10.0.0.7/24"), "10.0.0.7:7778"},
		{"private before public", addrs("203.0.113.5/24", "172.16.4.2/16"), "172.16.4.2:7778"},
		{"public only", addrs("203.0.113.5/24"), "203.0.113.5:7778"},
		{"loopback and link-local only", addrs("127.0.0.1/8", "169.254.1.1/16", "fe80::1/64"), "127.0.0.1:7778"},
		{"none", addrs(), "127.0.0.1:7778"},
		{"error", func() ([]net.Addr, error) { return nil, errors.New("no interfaces") }, "127.0.0.1:7778"},
	}
	for _, c := range cases {
		got := advertiseFrom(wildcard, c.interfaces)
		if got.String() != c.want {
			t.Errorf("%s: advertised %v, want %s", c.name, got, c.want)
		}
		service, _ := newTestService(t)
		service.setListener(protocol.CardListener{
			State: protocol.CardListenerListening, Addr: wildcard.String(),
			URL: "http://" + got.String() + protocol.CardPath,
		})
		if body, reason := service.serve(); body == nil {
			t.Errorf("%s: not served (%s)", c.name, reason)
		}
	}
}

// TestServedCardIgnoresIfNoneMatch: every GET is a full 200 with no ETag, whatever If-None-Match says;
// HEAD has the same status and headers and no body; an edit shows on the next request (contract 16.1).
func TestServedCardIgnoresIfNoneMatch(t *testing.T) {
	service, path := newTestService(t)
	listening(service)
	handler := newCardServer(service, &logger{out: io.Discard}).handler()
	do := func(method, match string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, protocol.CardPath, nil)
		if match != "" {
			request.Header.Set("If-None-Match", match)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	first := do(http.MethodGet, "")
	if first.Code != 200 || first.Body.Len() == 0 || first.Header().Get("ETag") != "" || first.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET = %d, etag %q, cache %q", first.Code, first.Header().Get("ETag"), first.Header().Get("Cache-Control"))
	}
	for _, match := range []string{`"anything"`, "*", "W/" + `"x"`} {
		got := do(http.MethodGet, match)
		if got.Code != 200 || got.Body.String() != first.Body.String() || got.Header().Get("ETag") != "" {
			t.Fatalf("If-None-Match %s = %d, etag %q", match, got.Code, got.Header().Get("ETag"))
		}
	}
	head := do(http.MethodHead, "")
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD = %d, %d bytes", head.Code, head.Body.Len())
	}
	writeCardFile(t, path, "name: vmb\ndescription: Changed.\nversion: \"1\"\nskills: []\n")
	if got := do(http.MethodGet, ""); got.Code != 200 || !strings.Contains(got.Body.String(), "Changed.") {
		t.Fatalf("after an edit = %d: %s", got.Code, got.Body.String())
	}
}

// TestFreshDaemonCardIsWhatThePollerAccepts: a fresh template served over a real HTTP connection comes
// back 200 with a JSON content type and a body that passes the exact parse the gateway poller applies
// (card.Polled). The gateway's own exchange is covered in its package; this is the daemon half.
func TestFreshDaemonCardIsWhatThePollerAccepts(t *testing.T) {
	service, _ := newTestService(t)
	server := httptest.NewServer(newCardServer(service, &logger{out: io.Discard}).handler())
	defer server.Close()
	service.setListener(protocol.CardListener{
		State: protocol.CardListenerListening, Addr: server.Listener.Addr().String(),
		URL: server.URL + protocol.CardPath,
	})

	response, err := http.Get(server.URL + protocol.CardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, protocol.MaxCardPollBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, content type %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	if _, problems := card.Parse(body, card.Polled, true); len(problems) > 0 {
		t.Fatalf("the poller would reject the fresh card: %+v", problems)
	}
}
