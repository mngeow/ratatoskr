package dashboard

// Written under GR-3 and never run. Uses httptest recorders only (no socket) and the embedded templates, so it
// also checks that the frontend's templates define the four names this package executes.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSource struct {
	data Data
	err  error
}

func (f fakeSource) Snapshot(context.Context) (Data, error) { return f.data, f.err }

func serve(t *testing.T, src Source, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	h, err := NewHandler(src, Info{})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	for _, target := range []string{"/ui/", "/ui/network", "/ui/network/status", "/ui/network/graph.json", "/ui/hosts/mac/card", "/ui/hosts/nobody/card", "/ui/static/", "/ui/nothing"} {
		rec := serve(t, fakeSource{data: fixture()}, http.MethodGet, target)
		for name, want := range map[string]string{
			"Content-Security-Policy": ContentSecurityPolicy, "X-Content-Type-Options": "nosniff",
			"X-Frame-Options": "DENY", "Referrer-Policy": "no-referrer",
		} {
			if got := rec.Header().Get(name); got != want {
				t.Errorf("%s: %s = %q, want %q", target, name, got, want)
			}
		}
	}
	if !strings.Contains(ContentSecurityPolicy, "default-src 'self'") || strings.Contains(ContentSecurityPolicy, "unsafe-inline") {
		t.Fatalf("CSP = %q", ContentSecurityPolicy)
	}
}

func TestPagesRenderAndPartialsAreHTML(t *testing.T) {
	for target, name := range map[string]string{"/ui/": "overview", "/ui/network": "network", "/ui/network/status": "status", "/ui/hosts/mac/card": "card"} {
		rec := serve(t, fakeSource{data: fixture()}, http.MethodGet, target)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || rec.Header().Get("Cache-Control") != "no-store" || rec.Body.Len() == 0 {
			t.Errorf("%s (%s): %d %q %q", target, name, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
		}
	}
}

func TestCardTextIsEscaped(t *testing.T) {
	rec := serve(t, fakeSource{data: fixture()}, http.MethodGet, "/ui/hosts/mac/card")
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatalf("host-written card text was not escaped (S5):\n%s", body)
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("the card description is not shown (escaped):\n%s", body)
	}
}

func TestUnknownHostCardIs404WithThePartial(t *testing.T) {
	rec := serve(t, fakeSource{data: fixture()}, http.MethodGet, "/ui/hosts/%3Cb%3E/card")
	if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "<b>") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestGraphJSON(t *testing.T) {
	rec := serve(t, fakeSource{data: fixture()}, http.MethodGet, "/ui/network/graph.json")
	var doc GraphDocument
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || json.Unmarshal(rec.Body.Bytes(), &doc) != nil || len(doc.Elements) == 0 {
		t.Fatalf("%d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestReadOnly(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		for _, target := range []string{"/ui/", "/ui/network", "/ui/hosts/mac/card"} {
			if rec := serve(t, fakeSource{data: fixture()}, method, target); rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d, want 405", method, target, rec.Code)
			}
		}
	}
}

func TestASourceErrorIsAFixedSentence(t *testing.T) {
	rec := serve(t, fakeSource{err: errors.New("sqlite: secret detail")}, http.MethodGet, "/ui/")
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "secret detail") {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestStaticIsNeverListed(t *testing.T) {
	if rec := serve(t, fakeSource{}, http.MethodGet, "/ui/static/"); rec.Code != http.StatusNotFound {
		t.Fatalf("directory listing: %d", rec.Code)
	}
}
