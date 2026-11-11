package gateway

// Written under GR-3 and never run. httptest only: no socket.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDashboardRoutesNeedNoAuthNorWireVersion: a browser sends neither a credential nor the wire version
// header, and gets the page; the same request to /v1 is still refused.
func TestDashboardRoutesNeedNoAuthNorWireVersion(t *testing.T) {
	server, _, _ := newGatewayServer(t, Options{})
	h := server.Handler()

	for _, path := range []string{"/ui/", "/ui/network", "/ui/network/graph.json", "/ui/network/status"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 with no credential", path, rec.Code)
		}
		if rec.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s has no CSP", path)
		}
	}

	for _, path := range []string{"/v1/network", "/v1/graph"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s answered 200 without a credential: the dashboard must not open /v1", path)
		}
	}
}

// TestDashboardIsReadOnlyAndRootRedirects.
func TestDashboardIsReadOnlyAndRootRedirects(t *testing.T) {
	server, _, _ := newGatewayServer(t, Options{})
	h := server.Handler()

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, "/ui/network", strings.NewReader("x")))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /ui/network = %d, want 405", method, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/ui/" {
		t.Errorf("GET / = %d %q, want a redirect to /ui/", rec.Code, rec.Header().Get("Location"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code == http.StatusFound {
		t.Error("POST / must not redirect")
	}
}
