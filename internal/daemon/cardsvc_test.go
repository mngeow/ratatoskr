package daemon

// Untested: under GR-3 nothing here has been run. Everything uses t.TempDir() and the service's own
// methods; no socket and no real home directory.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/gateway"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

func newCardFixture(t *testing.T) (*cardService, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "daemon")
	service := newCardService(dir, func() string { return "laptop" }, &logger{out: io.Discard})
	service.now = func() time.Time { return time.Unix(1700000000, 0) }
	return service, dir
}

func str(value string) *string { return &value }

// doOK runs an operation that must succeed.
func doOK(t *testing.T, service *cardService, op wire.CardOp) protocol.CardView {
	t.Helper()
	view, refusal := service.Do(context.Background(), op, protocol.CardCallerWindow)
	if refusal != nil {
		t.Fatalf("%s refused: %+v", op.Op, refusal)
	}
	return view
}

// TestCardDefaultView: a host that never edited has its name, nothing published, no gateway needed.
func TestCardDefaultView(t *testing.T) {
	service, _ := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})

	if view.Host.Name != "laptop" || !view.Host.Paired {
		t.Fatalf("host = %+v", view.Host)
	}
	if view.Published != nil || len(view.Revisions) != 0 {
		t.Fatalf("published = %+v", view.Published)
	}
	if view.Working.Card.Name != "laptop" || !view.Working.Dirty {
		t.Fatalf("working = %+v", view.Working)
	}
	if view.Drafting.State != protocol.DraftNotConfigured || view.Listener.State != protocol.CardListenerOff {
		t.Fatalf("drafting/listener = %+v / %+v", view.Drafting, view.Listener)
	}
	if view.Token == "" || len(view.Skills.NotScanned) == 0 {
		t.Fatalf("token/skills = %q / %+v", view.Token, view.Skills)
	}
	// A first-ever working copy has no description, and says so readably rather than being unusable.
	described := false
	for _, problem := range view.Working.Problems {
		if problem.Field == "description" && strings.Contains(problem.Message, "description is required") {
			described = true
		}
	}
	if !described {
		t.Fatalf("problems = %+v, want a readable description problem", view.Working.Problems)
	}
}

// TestCardEditNeedsTheCurrentToken: a stale token is card_conflict with the current view.
func TestCardEditNeedsTheCurrentToken(t *testing.T) {
	service, _ := newCardFixture(t)
	first := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})

	edited := doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: first.Token, Edit: &wire.CardEdit{Description: str("Build host")}})
	if edited.Token == first.Token || edited.Working.Card.Description != "Build host" {
		t.Fatalf("edit did not apply: %+v", edited.Working.Card)
	}
	if edited.Working.Sources["description"] != protocol.CardSourceOwner {
		t.Fatalf("sources = %+v", edited.Working.Sources)
	}

	view, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpEdit, BaseToken: first.Token, Edit: &wire.CardEdit{Name: str("other")}}, protocol.CardCallerWindow)
	if refusal == nil || refusal.Code != wire.CodeCardConflict {
		t.Fatalf("refusal = %+v", refusal)
	}
	if view.Token != edited.Token || view.Working.Card.Name != "laptop" {
		t.Fatalf("the conflict view is not the current one: %+v", view.Working.Card)
	}
}

// TestCardPublishValidatesFirstAndNeverTouchesThePublishedFile: a card with problems is refused and the
// previous file stays; a valid one becomes a revision with the daemon's version.
func TestCardPublishValidatesFirstAndNeverTouchesThePublishedFile(t *testing.T) {
	service, dir := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})

	bad := doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Description: str("keys in /Users/me/.ssh/id_rsa")}})
	if len(bad.Working.Problems) == 0 {
		t.Fatal("a path in the description was not flagged")
	}
	if _, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpPublish, BaseToken: bad.Token}, protocol.CardCallerCLI); refusal == nil {
		t.Fatal("a card with problems was published")
	}
	if service.state.Published != nil {
		t.Fatal("a refused publish changed the published card")
	}

	good := doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: bad.Token, Edit: &wire.CardEdit{Description: str("Build host for the mac mini")}})
	published, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpPublish, BaseToken: good.Token}, protocol.CardCallerCLI)
	if refusal != nil {
		t.Fatalf("publish refused: %+v", refusal)
	}
	if published.Published == nil || published.Published.N != 1 || published.Published.Card.Version != "1" || published.Published.Caller != protocol.CardCallerCLI {
		t.Fatalf("published = %+v", published.Published)
	}
	if published.Working.Dirty {
		t.Fatal("the working copy is dirty right after publishing it")
	}

	// 0600 file in a 0700 directory, and nothing but the final file left behind.
	info, err := os.Stat(filepath.Join(dir, cardFileName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("card file: %v, %v", info, err)
	}
	dirInfo, _ := os.Stat(dir)
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("card directory mode = %o", dirInfo.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("the directory holds %d entries, want only the card file", len(entries))
	}
}

// TestCardSurvivesARestart: the published card, the revisions and the working copy come back.
func TestCardSurvivesARestart(t *testing.T) {
	service, dir := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	edited := doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Description: str("one")}})
	doOK(t, service, wire.CardOp{Op: wire.CardOpPublish, BaseToken: edited.Token})

	again := newCardService(dir, func() string { return "laptop" }, &logger{out: io.Discard})
	reloaded := doOK(t, again, wire.CardOp{Op: wire.CardOpGet})
	if reloaded.Published == nil || reloaded.Published.N != 1 || reloaded.Working.Card.Description != "one" {
		t.Fatalf("reloaded = %+v", reloaded)
	}
}

// TestCorruptCardFileIsMovedAsideNotOverwritten.
func TestCorruptCardFileIsMovedAsideNotOverwritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daemon")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, cardFileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := newCardService(dir, func() string { return "laptop" }, &logger{out: io.Discard})
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	if view.Published != nil || view.Working.Card.Name != "laptop" {
		t.Fatalf("view = %+v", view)
	}
	matches, _ := filepath.Glob(path + ".unreadable-*")
	if len(matches) != 1 {
		t.Fatalf("aside files = %v", matches)
	}
	content, _ := os.ReadFile(matches[0])
	if string(content) != "{not json" {
		t.Fatalf("the unreadable file was altered: %q", content)
	}
}

// TestCardRevisionsAreBoundedAndRollbackPublishesANewRevision.
func TestCardRevisionsAreBoundedAndRollbackPublishesANewRevision(t *testing.T) {
	service, _ := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})

	for i := 0; i < protocol.MaxCardRevisions+3; i++ {
		view = doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Description: str(strings.Repeat("a", i+1))}})
		view = doOK(t, service, wire.CardOp{Op: wire.CardOpPublish, BaseToken: view.Token})
	}
	if len(view.Revisions) != protocol.MaxCardRevisions {
		t.Fatalf("revisions = %d, want %d", len(view.Revisions), protocol.MaxCardRevisions)
	}
	newest := view.Revisions[0].N
	if newest != protocol.MaxCardRevisions+3 {
		t.Fatalf("newest = %d", newest)
	}

	// Unknown revision.
	if _, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpRollback, BaseToken: view.Token, Revision: 1}, protocol.CardCallerWindow); refusal == nil {
		t.Fatal("a revision that was trimmed away could be restored")
	}

	target := view.Revisions[3]
	preview := doOK(t, service, wire.CardOp{Op: wire.CardOpHistory, Revision: target.N})
	if preview.Preview == nil || preview.Preview.N != target.N {
		t.Fatalf("preview = %+v", preview.Preview)
	}
	if other := doOK(t, service, wire.CardOp{Op: wire.CardOpGet}); other.Preview != nil {
		t.Fatal("a plain get carried a preview")
	}
	if _, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpHistory, Revision: 9999}, protocol.CardCallerWindow); refusal == nil {
		t.Fatal("an unknown preview was accepted")
	}

	rolled := doOK(t, service, wire.CardOp{Op: wire.CardOpRollback, BaseToken: preview.Token, Revision: target.N})
	if rolled.Published.N != newest+1 {
		t.Fatalf("rollback published revision %d, want %d", rolled.Published.N, newest+1)
	}
	if rolled.Published.Card.Description != preview.Preview.Card.Description {
		t.Fatal("rollback did not restore the old description")
	}
}

// TestSkillEvidenceRules: a label takes fresh evidence, an unlabeled entry keeps what the working copy
// already holds, and a new unlabeled ID gets none.
func TestSkillEvidenceRules(t *testing.T) {
	service, _ := newCardFixture(t)
	service.inventory.ScannedAt = "2026-11-07T10:00:00Z"
	service.inventory.Found = []protocol.FoundSkill{{
		Harness: "claude", HarnessVersion: "2.1", Scope: "user", Label: "~/.claude/skills/pdf", DirName: "pdf", SHA256: strings.Repeat("ab", 32),
	}}

	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	view = doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Skills: &[]wire.CardSkillEdit{
		{ID: "pdf", Name: "PDF", Description: "Forms", FoundLabel: "~/.claude/skills/pdf"},
		{ID: "other", Name: "Other"},
	}}})

	evidence := view.Working.Card.Capabilities.Extensions[0].Params.Skills
	if evidence["pdf"].SHA256 != strings.Repeat("ab", 32) || evidence["pdf"].HarnessVersion != "2.1" || evidence["pdf"].ScannedAt == "" {
		t.Fatalf("evidence = %+v", evidence)
	}
	if _, ok := evidence["other"]; ok {
		t.Fatal("an unlabeled new skill got evidence")
	}
	if len(view.Skills.Found) != 1 || !view.Skills.Found[0].Declared {
		t.Fatalf("found = %+v", view.Skills.Found)
	}

	// Edit again without the label: the evidence stays.
	view = doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Skills: &[]wire.CardSkillEdit{
		{ID: "pdf", Name: "PDF forms", Description: "Fills forms"},
	}}})
	evidence = view.Working.Card.Capabilities.Extensions[0].Params.Skills
	if evidence["pdf"].SHA256 == "" {
		t.Fatal("an edit that left a skill alone lost its evidence")
	}
	if _, ok := evidence["other"]; ok {
		t.Fatal("a removed skill kept its evidence")
	}

	// A new ID, no label: nothing carried over.
	view = doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Skills: &[]wire.CardSkillEdit{
		{ID: "pdf-two", Name: "PDF two"},
	}}})
	if len(view.Working.Card.Capabilities.Extensions[0].Params.Skills) != 0 {
		t.Fatalf("evidence = %+v", view.Working.Card.Capabilities.Extensions[0].Params.Skills)
	}
}

// TestServedBytesAreRenderedFromTheBoundListener: no listener, no bytes; with one, the URL in the card
// is the bound address and the ETag covers exactly the served bytes; both well-known paths serve them.
func TestServedBytesAreRenderedFromTheBoundListener(t *testing.T) {
	service, _ := newCardFixture(t)
	if _, _, ok := service.servedBytes(); ok {
		t.Fatal("served with nothing published")
	}

	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	view = doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Description: str("Build host for the mac mini")}})
	doOK(t, service, wire.CardOp{Op: wire.CardOpPublish, BaseToken: view.Token})
	if _, _, ok := service.servedBytes(); ok {
		t.Fatal("served with the listener off")
	}

	url := "http://100.64.0.5:7778" + protocol.CardPath
	service.setListener(protocol.CardListener{State: protocol.CardListenerOn, URL: url, Transport: "http"})
	body, etag, ok := service.servedBytes()
	if !ok {
		t.Fatal("not served")
	}
	parsed, problems := card.Parse(body, card.Strict, true)
	if len(problems) != 0 || parsed.Card.SupportedInterfaces[0].URL != url {
		t.Fatalf("served card: %+v %+v", parsed.Card.SupportedInterfaces, problems)
	}
	if etag != card.ETag(body) {
		t.Fatalf("etag %q does not cover the served bytes", etag)
	}

	// The HTTP surface.
	server := newCardServer(service, &logger{out: io.Discard})
	handler := server.handler()
	get := func(method, path string, header http.Header) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, nil)
		for key, values := range header {
			request.Header[key] = values
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	main := get(http.MethodGet, protocol.CardPath, nil)
	if main.Code != 200 || !bytes.Equal(main.Body.Bytes(), body) || main.Header().Get("ETag") != etag {
		t.Fatalf("GET = %d etag %q", main.Code, main.Header().Get("ETag"))
	}
	if main.Header().Get("Cache-Control") != "max-age=30" || main.Header().Get("Set-Cookie") != "" || main.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("headers = %v", main.Header())
	}
	alias := get(http.MethodGet, protocol.CardAliasPath, nil)
	if alias.Code != 200 || !bytes.Equal(alias.Body.Bytes(), body) {
		t.Fatalf("alias = %d", alias.Code)
	}
	head := get(http.MethodHead, protocol.CardPath, nil)
	if head.Code != 200 || head.Body.Len() != 0 || head.Header().Get("ETag") != etag {
		t.Fatalf("HEAD = %d, body %d", head.Code, head.Body.Len())
	}
	notModified := get(http.MethodGet, protocol.CardPath, http.Header{"If-None-Match": {etag}})
	if notModified.Code != 304 || notModified.Body.Len() != 0 || notModified.Header().Get("ETag") != etag || notModified.Header().Get("Cache-Control") != "max-age=30" {
		t.Fatalf("304 = %d, headers %v", notModified.Code, notModified.Header())
	}
	if other := get(http.MethodGet, protocol.CardPath, http.Header{"If-None-Match": {`"other"`}}); other.Code != 200 {
		t.Fatalf("a different validator = %d", other.Code)
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		recorder := get(method, protocol.CardPath, nil)
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s = %d, allow %q", method, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
	for _, path := range []string{"/", "/v1/graph", "/.well-known/", "/.well-known/agent-card.json/extra", "/card"} {
		if recorder := get(http.MethodGet, path, nil); recorder.Code != http.StatusNotFound || recorder.Body.Len() != 0 {
			t.Fatalf("GET %s = %d, body %q", path, recorder.Code, recorder.Body.String())
		}
	}
}

// TestUnpublishedCardIs503WithNoStore.
func TestUnpublishedCardIs503WithNoStore(t *testing.T) {
	service, _ := newCardFixture(t)
	service.setListener(protocol.CardListener{State: protocol.CardListenerOn, URL: "http://127.0.0.1:1" + protocol.CardPath})
	recorder := httptest.NewRecorder()
	newCardServer(service, &logger{out: io.Discard}).handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, protocol.CardPath, nil))
	if recorder.Code != 503 || recorder.Body.Len() != 0 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("503 = %d, headers %v", recorder.Code, recorder.Header())
	}
}

// TestEtagMatches: weak comparison, lists and star.
func TestEtagMatches(t *testing.T) {
	tests := []struct {
		header string
		want   bool
	}{
		{`"abc"`, true}, {`W/"abc"`, true}, {`"x", "abc"`, true}, {`*`, true},
		{`"abd"`, false}, {``, false}, {`abc`, false},
	}
	for _, tc := range tests {
		if got := etagMatches(tc.header, `"abc"`); got != tc.want {
			t.Errorf("etagMatches(%q) = %t, want %t", tc.header, got, tc.want)
		}
	}
}

// TestPlanCardListen: the flags, with the card listener named in the messages and nothing touched.
func TestPlanCardListen(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantOn  bool
		wantErr string
	}{
		{"off", Config{}, false, ""},
		{"loopback", Config{CardListen: "127.0.0.1:7778"}, true, ""},
		{"tailscale", Config{CardListen: "100.64.0.5:7778"}, true, ""},
		{"lan needs allow-lan", Config{CardListen: "192.168.1.4:7778"}, false, "--allow-lan"},
		{"lan https", Config{CardListen: "192.168.1.4:7778", CardAllowLAN: true}, true, ""},
		{"lan http", Config{CardListen: "192.168.1.4:7778", CardAllowLAN: true, CardInsecureHTTP: true}, true, ""},
		{"public", Config{CardListen: "8.8.8.8:7778"}, false, "card listener"},
		{"hostname", Config{CardListen: "example.com:7778"}, false, "card listener"},
		{"flags without a listener", Config{CardAllowLAN: true}, false, "--card-listen"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, on, err := planCardListen(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				if strings.Contains(err.Error(), "gateway:") {
					t.Fatalf("a card listener error says gateway: %v", err)
				}
				return
			}
			if err != nil || on != tc.wantOn {
				t.Fatalf("on=%t err=%v", on, err)
			}
		})
	}
}

// TestGatewayMessagesAreUnchangedBySubject: the shared rules keep the gateway's own text.
func TestGatewayMessagesAreUnchangedBySubject(t *testing.T) {
	_, _, gatewayErr := gateway.ParseAddress("example.com:1")
	_, _, cardErr := gateway.ParseAddressFor("gateway", "example.com:1")
	if gatewayErr == nil || cardErr == nil || gatewayErr.Error() != cardErr.Error() {
		t.Fatalf("ParseAddress and ParseAddressFor(gateway) differ:\n%v\n%v", gatewayErr, cardErr)
	}
	if !strings.HasPrefix(gatewayErr.Error(), "gateway: ") {
		t.Fatalf("gateway text changed: %v", gatewayErr)
	}
}

// TestCardViewCarriesNoSecretFields: the view is built from the allow-listed card, so the usual
// secret shapes are absent from the encoded view of a default card.
func TestCardViewCarriesNoSecretFields(t *testing.T) {
	service, _ := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	encoded, _ := card.Canonical(view.Working.Card)
	for _, forbidden := range []string{"token", "credential", "password", "/Users/", "securitySchemes"} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(strings.ToLower(forbidden))) {
			t.Fatalf("the default card contains %q: %s", forbidden, encoded)
		}
	}
}

// TestCardShowCleansWorkingCopyText: the plain form never prints a control character from the working
// copy, whatever wrote it.
func TestCardShowCleansWorkingCopyText(t *testing.T) {
	hostile := "evil\x1b]0;owned\x07\x1b[2J name\u0085 line two\x00"
	view := protocol.CardView{
		Host:      protocol.CardHost{Name: hostile, Paired: true},
		Working:   protocol.CardWorking{Card: protocol.HostCard{Name: hostile, Description: hostile}, Problems: []protocol.CardProblem{{Field: hostile, Message: hostile}}},
		Listener:  protocol.CardListener{State: protocol.CardListenerOn, URL: hostile},
		Drafting:  protocol.CardDrafting{State: protocol.DraftNotConfigured, Message: "none"},
		Published: &protocol.CardRevision{N: 1, At: hostile, Caller: hostile},
	}

	var out bytes.Buffer
	if err := printCard(&out, view); err != nil {
		t.Fatalf("printCard: %v", err)
	}
	for _, r := range out.String() {
		if r == '\n' {
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == 0x2028 || r == 0x2029 {
			t.Fatalf("a control character %U reached the terminal output:\n%q", r, out.String())
		}
	}
	if !strings.Contains(out.String(), "evil") {
		t.Fatalf("the readable part was lost: %q", out.String())
	}
	// Each field is still one line.
	if lines := strings.Count(out.String(), "\n"); lines > 14 {
		t.Fatalf("an embedded line break split a field: %d lines", lines)
	}
}

// TestPublishWithoutADescriptionIsRefusedAndKeepsTheWorkingCopy: partial work saves, publishing needs
// the description.
func TestPublishWithoutADescriptionIsRefusedAndKeepsTheWorkingCopy(t *testing.T) {
	service, _ := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	saved := doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Name: str("renamed")}})
	if saved.Working.Card.Name != "renamed" || saved.Working.Card.Description != "" {
		t.Fatalf("a partial working copy did not save: %+v", saved.Working.Card)
	}

	refused, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpPublish, BaseToken: saved.Token}, protocol.CardCallerCLI)
	if refusal == nil || refusal.Code != wire.CodeBadRequest || refused.Published != nil {
		t.Fatalf("refusal = %+v, published = %+v", refusal, refused.Published)
	}
	if refused.Token != saved.Token || refused.Working.Card.Name != "renamed" {
		t.Fatal("a refused publish changed the working copy")
	}
}

// TestRollbackToARevisionThatFailsTodaysRulesIsAReadableRefusal: nothing changes.
func TestRollbackToARevisionThatFailsTodaysRulesIsAReadableRefusal(t *testing.T) {
	service, _ := newCardFixture(t)
	view := doOK(t, service, wire.CardOp{Op: wire.CardOpGet})
	view = doOK(t, service, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &wire.CardEdit{Description: str("fine")}})
	view = doOK(t, service, wire.CardOp{Op: wire.CardOpPublish, BaseToken: view.Token})

	// A revision saved under an older, looser rule: no description.
	old := cloneCard(view.Published.Card)
	old.Description = ""
	service.mu.Lock()
	service.state.Revisions = append([]protocol.CardRevision{{N: 99, At: "2026-11-07T10:00:00Z", Caller: protocol.CardCallerCLI, Card: old}}, service.state.Revisions...)
	token := service.token()
	service.mu.Unlock()

	after, refusal := service.Do(context.Background(), wire.CardOp{Op: wire.CardOpRollback, BaseToken: token, Revision: 99}, protocol.CardCallerWindow)
	if refusal == nil || refusal.Code != wire.CodeBadRequest || !strings.Contains(refusal.Message, "revision 99 cannot be restored") || !strings.Contains(refusal.Message, "description") {
		t.Fatalf("refusal = %+v", refusal)
	}
	if after.Token != token || after.Working.Card.Description != "fine" || after.Published.N != 1 {
		t.Fatalf("a refused rollback changed state: token %q vs %q, working %q, published %d", after.Token, token, after.Working.Card.Description, after.Published.N)
	}
}
