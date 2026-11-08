package link

// Untested: under GR-3 nothing here has been run. The gateway is an httptest server.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// TestFetchGraphStatuses: 200 decodes, 204 is unchanged, 404 is unsupported, a newer schema is refused,
// other 4xx are refusals.
func TestFetchGraphStatuses(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantErr       error
		wantUnchanged bool
		wantSnapshot  bool
		anyErr        bool
	}{
		{name: "200", status: http.StatusOK, body: `{"schema":1,"revision":7,"hosts":[],"edges":[]}`, wantSnapshot: true},
		{name: "204", status: http.StatusNoContent, wantUnchanged: true},
		{name: "404 is unsupported", status: http.StatusNotFound, body: `{"code":"not_found"}`, wantErr: ErrGraphUnsupported},
		{name: "newer schema", status: http.StatusOK, body: `{"schema":2,"revision":1}`, wantErr: ErrGraphSchemaTooNew},
		{name: "unreadable", status: http.StatusOK, body: `not json`, anyErr: true},
		{name: "401 is a refusal", status: http.StatusUnauthorized, body: `{"code":"unauthorized","message":"no"}`, anyErr: true},
		{name: "500", status: http.StatusInternalServerError, anyErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, recorded := networkServer(t, tc.status, tc.body)
			fetch, err := client.FetchGraph(context.Background(), 0, 0)

			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
			case tc.anyErr:
				if err == nil {
					t.Fatal("no error")
				}
			case err != nil:
				t.Fatalf("err = %v", err)
			}
			if fetch.Unchanged != tc.wantUnchanged {
				t.Fatalf("unchanged = %t", fetch.Unchanged)
			}
			if (fetch.Snapshot != nil) != tc.wantSnapshot {
				t.Fatalf("snapshot = %+v", fetch.Snapshot)
			}
			if recorded.URL.Path != protocol.GraphPath {
				t.Fatalf("path = %q", recorded.URL.Path)
			}
		})
	}
}

// TestFetchGraphQuery: since is omitted at zero, the wait is sent in milliseconds and clamped.
func TestFetchGraphQuery(t *testing.T) {
	client, recorded := networkServer(t, http.StatusNoContent, "")

	if _, err := client.FetchGraph(context.Background(), 5, 3*time.Second); err != nil {
		t.Fatalf("err = %v", err)
	}
	query := recorded.URL.Query()
	if query.Get("since") != "5" || query.Get("wait_ms") != "3000" {
		t.Fatalf("query = %q", recorded.URL.RawQuery)
	}

	if _, err := client.FetchGraph(context.Background(), 0, time.Hour); err != nil {
		t.Fatalf("err = %v", err)
	}
	query = recorded.URL.Query()
	if query.Has("since") {
		t.Fatalf("since sent at zero: %q", recorded.URL.RawQuery)
	}
	if query.Get("wait_ms") != "20000" {
		t.Fatalf("an over-long wait was not clamped: %q", recorded.URL.RawQuery)
	}
	if strings.Contains(recorded.URL.RawQuery, "-") {
		t.Fatalf("query = %q", recorded.URL.RawQuery)
	}
}
