package link

// Untested: under GR-3 nothing here has been run. The gateway is an httptest server, so no real
// gateway and no daemon is involved.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// networkServer answers every request with one fixed status and body, and records the last request.
func networkServer(t *testing.T, status int, body string) (*Client, *http.Request) {
	t.Helper()

	recorded := &http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*recorded = *r
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	client, err := NewClient(Pairing{
		Gateway:    strings.TrimPrefix(server.URL, "http://"),
		Host:       "laptop",
		Credential: "abcDEF012_-",
	}, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client, recorded
}

// TestReportSelfStatuses: 204 and 200 are success, 404 is unsupported, other 4xx are refusals.
func TestReportSelfStatuses(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
	}{
		{name: "204", status: http.StatusNoContent},
		{name: "200", status: http.StatusOK},
		{name: "404 is unsupported", status: http.StatusNotFound, body: `{"code":"not_found"}`, wantErr: ErrNetworkUnsupported},
		{name: "413 is a refusal", status: http.StatusRequestEntityTooLarge, body: `{"code":"bad_request","message":"too large"}`},
		{name: "401 is a refusal", status: http.StatusUnauthorized, body: `{"code":"unauthorized","message":"no"}`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, recorded := networkServer(t, tc.status, tc.body)
			err := client.ReportSelf(context.Background(), protocol.ReportRequest{Schema: protocol.NetworkSchema})

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			} else if err == nil {
				// success
			} else if tc.status >= 300 && tc.status < 500 {
				if _, ok := Refusal(err); !ok {
					t.Fatalf("error = %v, want a refusal", err)
				}
			} else if err != nil {
				t.Fatalf("ReportSelf: %v", err)
			}

			if recorded.Method != http.MethodPut || recorded.URL.Path != protocol.ReportPath {
				t.Fatalf("request = %s %s", recorded.Method, recorded.URL.Path)
			}
			if recorded.Header.Get(protocol.WireVersionHeader) == "" {
				t.Fatal("the request carried no protocol version")
			}
		})
	}
}

// TestFetchNetworkPaths: 200 decodes, 204 is unchanged, 404 unsupported, a newer schema is refused.
func TestFetchNetworkPaths(t *testing.T) {
	snapshot := protocol.NetworkSnapshot{Schema: protocol.NetworkSchema, Revision: 4}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	t.Run("200 installs", func(t *testing.T) {
		client, recorded := networkServer(t, http.StatusOK, string(body))
		fetch, err := client.FetchNetwork(context.Background(), 3, 100*time.Millisecond)
		if err != nil {
			t.Fatalf("FetchNetwork: %v", err)
		}
		if fetch.Unchanged || fetch.Snapshot == nil || fetch.Snapshot.Revision != 4 {
			t.Fatalf("fetch = %+v", fetch)
		}
		if recorded.URL.Path != protocol.NetworkPath {
			t.Fatalf("path = %q", recorded.URL.Path)
		}
		query := recorded.URL.Query()
		if query.Get("since") != "3" {
			t.Fatalf("since = %q", query.Get("since"))
		}
		if query.Get("wait_ms") != "100" {
			t.Fatalf("wait_ms = %q", query.Get("wait_ms"))
		}
	})

	t.Run("no since when zero", func(t *testing.T) {
		client, recorded := networkServer(t, http.StatusOK, string(body))
		if _, err := client.FetchNetwork(context.Background(), 0, 0); err != nil {
			t.Fatalf("FetchNetwork: %v", err)
		}
		if recorded.URL.Query().Has("since") {
			t.Fatalf("since was sent: %q", recorded.URL.RawQuery)
		}
		if recorded.URL.Query().Has("wait_ms") {
			t.Fatalf("wait_ms was sent: %q", recorded.URL.RawQuery)
		}
	})

	t.Run("the wait is clamped", func(t *testing.T) {
		client, recorded := networkServer(t, http.StatusOK, string(body))
		if _, err := client.FetchNetwork(context.Background(), 1, time.Hour); err != nil {
			t.Fatalf("FetchNetwork: %v", err)
		}
		want := protocol.MaxNetworkWait.Milliseconds()
		if got := recorded.URL.Query().Get("wait_ms"); got != networkItoa(want) {
			t.Fatalf("wait_ms = %q, want %d", got, want)
		}
	})

	t.Run("204 is unchanged", func(t *testing.T) {
		client, _ := networkServer(t, http.StatusNoContent, "")
		fetch, err := client.FetchNetwork(context.Background(), 4, 0)
		if err != nil {
			t.Fatalf("FetchNetwork: %v", err)
		}
		if !fetch.Unchanged || fetch.Snapshot != nil {
			t.Fatalf("fetch = %+v, want unchanged", fetch)
		}
	})

	t.Run("404 is unsupported", func(t *testing.T) {
		client, _ := networkServer(t, http.StatusNotFound, `{"code":"not_found"}`)
		if _, err := client.FetchNetwork(context.Background(), 4, 0); !errors.Is(err, ErrNetworkUnsupported) {
			t.Fatalf("error = %v, want unsupported", err)
		}
	})

	t.Run("a newer schema is refused", func(t *testing.T) {
		newer, err := json.Marshal(protocol.NetworkSnapshot{Schema: protocol.NetworkSchema + 1})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		client, _ := networkServer(t, http.StatusOK, string(newer))
		if _, err := client.FetchNetwork(context.Background(), 0, 0); !errors.Is(err, ErrNetworkSchemaTooNew) {
			t.Fatalf("error = %v, want schema too new", err)
		}
	})

	t.Run("an unreadable body is refused", func(t *testing.T) {
		client, _ := networkServer(t, http.StatusOK, "not json")
		if _, err := client.FetchNetwork(context.Background(), 0, 0); !errors.Is(err, errUnreadableAnswer) {
			t.Fatalf("error = %v, want unreadable", err)
		}
	})
}

// TestNetworkTextsNameNoAddress: a transport failure says what kind, never where.
func TestNetworkTextsNameNoAddress(t *testing.T) {
	client, err := NewClient(Pairing{Gateway: "127.0.0.1:1", Host: "laptop", Credential: "abcDEF012_-"}, Options{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	reportErr := client.ReportSelf(ctx, protocol.ReportRequest{Schema: protocol.NetworkSchema})
	if reportErr == nil {
		t.Fatal("ReportSelf reached a closed port")
	}
	for _, forbidden := range []string{"127.0.0.1", "http://", "abcDEF012_-"} {
		if strings.Contains(reportErr.Error(), forbidden) {
			t.Fatalf("the error text %q contains %q", reportErr.Error(), forbidden)
		}
	}

	_, fetchErr := client.FetchNetwork(ctx, 0, 0)
	if fetchErr == nil {
		t.Fatal("FetchNetwork reached a closed port")
	}
	for _, forbidden := range []string{"127.0.0.1", "http://", "abcDEF012_-"} {
		if strings.Contains(fetchErr.Error(), forbidden) {
			t.Fatalf("the error text %q contains %q", fetchErr.Error(), forbidden)
		}
	}
}

// itoa renders a small integer for a query comparison.
func networkItoa(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
