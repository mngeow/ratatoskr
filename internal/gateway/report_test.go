package gateway

// Untested: under GR-3 nothing here has been run. Every case is in memory or through an httptest
// recorder; no socket is opened.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// reportAt builds a minimal valid report.
func reportAt(sessions ...protocol.ReportSession) protocol.ReportRequest {
	return protocol.ReportRequest{
		Schema:     protocol.NetworkSchema,
		Seq:        1,
		ReportedAt: "2026-11-06T14:02:11Z",
		Daemon:     protocol.ReportDaemon{Build: "build-1", StartedAt: "2026-11-06T12:01:00Z"},
		Sessions:   sessions,
	}
}

// sessionAt builds one reported session.
func sessionAt(id, localID, name string) protocol.ReportSession {
	return protocol.ReportSession{
		SessionID: id, LocalID: localID, Name: name, Harness: name,
		StartedAt: "2026-11-06T12:01:05Z", State: protocol.SessionStateBusy,
		StateAt: "2026-11-06T14:02:08Z", Input: protocol.InputHeld,
	}
}

// TestSanitizeReport cleans what it must and refuses what the contract refuses.
func TestSanitizeReport(t *testing.T) {
	t.Run("control characters are stripped from every string", func(t *testing.T) {
		request := reportAt(sessionAt("s1", "l1", "code\x1b[31mx"))
		request.Daemon.Build = "bui\x00ld"

		clean, err := sanitizeReport(request)
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if strings.ContainsRune(clean.Sessions[0].Name, 0x1b) || strings.ContainsRune(clean.Daemon.Build, 0x00) {
			t.Fatalf("control characters survived: %+v", clean)
		}
	})

	t.Run("a string is cut to 128 runes, multibyte safe", func(t *testing.T) {
		long := strings.Repeat("é", 200)
		clean, err := sanitizeReport(reportAt(sessionAt("s1", "l1", long)))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if got := len([]rune(clean.Sessions[0].Name)); got != protocol.MaxReportString {
			t.Fatalf("name is %d runes, want %d", got, protocol.MaxReportString)
		}
		if !strings.HasPrefix(clean.Sessions[0].Name, "é") {
			t.Fatal("the cut broke a rune")
		}
	})

	t.Run("unknown state and input become unknown", func(t *testing.T) {
		session := sessionAt("s1", "l1", "claude")
		session.State = "typing-ish"
		session.Input = "somebody"

		clean, err := sanitizeReport(reportAt(session))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if clean.Sessions[0].State != protocol.SessionStateUnknown {
			t.Fatalf("state = %q, want unknown", clean.Sessions[0].State)
		}
		if clean.Sessions[0].Input != protocol.InputUnknown {
			t.Fatalf("input = %q, want unknown", clean.Sessions[0].Input)
		}
	})

	t.Run("known state and input survive", func(t *testing.T) {
		clean, err := sanitizeReport(reportAt(sessionAt("s1", "l1", "claude")))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if clean.Sessions[0].State != protocol.SessionStateBusy || clean.Sessions[0].Input != protocol.InputHeld {
			t.Fatalf("session = %+v", clean.Sessions[0])
		}
	})

	t.Run("unparseable times are dropped, offset times are converted", func(t *testing.T) {
		session := sessionAt("s1", "l1", "claude")
		session.StartedAt = "yesterday"
		session.StateAt = "2026-11-06T14:02:08.9+01:00"

		clean, err := sanitizeReport(reportAt(session))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if clean.Sessions[0].StartedAt != "" {
			t.Fatalf("started_at = %q, want empty", clean.Sessions[0].StartedAt)
		}
		// An offset time with a fraction is a real time: it is converted, not discarded.
		if clean.Sessions[0].StateAt != "2026-11-06T13:02:08Z" {
			t.Fatalf("state_at = %q, want the UTC whole-second form", clean.Sessions[0].StateAt)
		}
	})

	t.Run("a good time is re-emitted in UTC whole seconds", func(t *testing.T) {
		clean, err := sanitizeReport(reportAt(sessionAt("s1", "l1", "claude")))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if clean.ReportedAt != "2026-11-06T14:02:11Z" {
			t.Fatalf("reported_at = %q", clean.ReportedAt)
		}
	})

	t.Run("a schema this gateway does not know is refused", func(t *testing.T) {
		for _, schema := range []int{0, 2, 99} {
			request := reportAt()
			request.Schema = schema
			if _, err := sanitizeReport(request); err == nil {
				t.Fatalf("sanitizeReport accepted schema %d", schema)
			}
		}
	})

	t.Run("more than sixteen sessions is refused", func(t *testing.T) {
		request := reportAt()
		for i := 0; i < protocol.MaxReportSessions+1; i++ {
			request.Sessions = append(request.Sessions, sessionAt(string(rune('a'+i)), string(rune('a'+i)), "claude"))
		}
		if _, err := sanitizeReport(request); err == nil {
			t.Fatal("sanitizeReport accepted too many sessions")
		}
	})

	t.Run("a session with no name after cleaning is refused", func(t *testing.T) {
		request := reportAt(protocol.ReportSession{SessionID: "s1", Name: "\x1b\x00"})
		if _, err := sanitizeReport(request); err == nil {
			t.Fatal("sanitizeReport accepted a nameless session")
		}
	})

	t.Run("a duplicate keeps the first entry", func(t *testing.T) {
		first := sessionAt("s1", "l1", "claude")
		first.State = protocol.SessionStateIdle
		second := sessionAt("s1", "l2", "claude")
		second.State = protocol.SessionStateBusy

		clean, err := sanitizeReport(reportAt(first, second))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if len(clean.Sessions) != 1 {
			t.Fatalf("kept %d sessions, want 1", len(clean.Sessions))
		}
		if clean.Sessions[0].State != protocol.SessionStateIdle {
			t.Fatalf("kept the later entry: %+v", clean.Sessions[0])
		}

		// A duplicate local_id with a different session_id is also the first one's loss.
		third := sessionAt("s3", "l1", "codex")
		clean, err = sanitizeReport(reportAt(first, third))
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		if len(clean.Sessions) != 1 || clean.Sessions[0].SessionID != "s1" {
			t.Fatalf("sessions = %+v, want only the first", clean.Sessions)
		}
	})

	t.Run("only the fields the gateway knows are copied", func(t *testing.T) {
		body := `{"schema":1,"seq":3,"daemon":{"build":"b"},"sessions":[{"session_id":"s1","name":"claude","state":"idle","input":"free","secret":"do not keep"}],"extra":"do not keep either"}`

		var decoded map[string]any
		if err := json.Unmarshal([]byte(body), &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		// Decoding into the typed request drops everything the type does not name, which is the
		// property the gateway relies on.
		var request protocol.ReportRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatalf("decode into the request: %v", err)
		}
		clean, err := sanitizeReport(request)
		if err != nil {
			t.Fatalf("sanitizeReport: %v", err)
		}
		encoded, err := json.Marshal(clean)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, leaked := range []string{"secret", "extra"} {
			if strings.Contains(string(encoded), leaked) {
				t.Fatalf("the sanitized report kept %q: %s", leaked, encoded)
			}
		}
	})
}

// TestReportStorePut: the same report twice does not count as a change, a new one does, and a
// re-pair (a new epoch) does.
func TestReportStorePut(t *testing.T) {
	store := newReportStore()
	epoch := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	clean, err := sanitizeReport(reportAt(sessionAt("s1", "l1", "claude")))
	if err != nil {
		t.Fatalf("sanitizeReport: %v", err)
	}

	if !store.put("laptop", epoch, clean, gatewayNow) {
		t.Fatal("the first report did not count as a change")
	}
	if store.put("laptop", epoch, clean, gatewayNow.Add(time.Second)) {
		t.Fatal("an identical report counted as a change")
	}

	different := clean
	different.Seq = 9
	if !store.put("laptop", epoch, different, gatewayNow) {
		t.Fatal("a new sequence did not count as a change")
	}

	if !store.put("laptop", epoch.Add(time.Minute), clean, gatewayNow) {
		t.Fatal("a new epoch did not count as a change")
	}
}

// TestReportStorePrune: only the epochs the keep function accepts survive.
func TestReportStorePrune(t *testing.T) {
	store := newReportStore()
	clean, _ := sanitizeReport(reportAt())

	keptEpoch := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	store.put("keep", keptEpoch, clean, gatewayNow)
	store.put("drop", keptEpoch, clean, gatewayNow)

	store.prune(func(host string, epoch time.Time) bool { return host == "keep" })

	if _, ok := store.get("keep"); !ok {
		t.Fatal("the kept report was pruned")
	}
	if _, ok := store.get("drop"); ok {
		t.Fatal("the pruned report survived")
	}
}

// TestReportBodyBounds: too large is 413, malformed is 400, and neither replaces the previous
// report.
func TestReportBodyBounds(t *testing.T) {
	server, store, credential := newGatewayServer(t, Options{})

	// A good report first, so "kept" means something.
	good := mustJSON(t, reportAt(sessionAt("s1", "l1", "claude")))
	if recorder := call(server, "PUT", protocol.ReportPath, version(), credential, good); recorder.Code != 204 {
		t.Fatalf("report = %d, want 204: %s", recorder.Code, recorder.Body.String())
	}
	before, ok := server.network.reports.get("laptop")
	if !ok {
		t.Fatal("the first report was not stored")
	}

	oversized := `{"schema":1,"padding":"` + strings.Repeat("x", protocol.MaxReportBytes) + `"}`
	recorder := call(server, "PUT", protocol.ReportPath, version(), credential, oversized)
	if recorder.Code != 413 {
		t.Fatalf("oversized report = %d, want 413: %s", recorder.Code, recorder.Body.String())
	}
	if code := wireCode(t, recorder); code != protocol.WireErrBadRequest {
		t.Fatalf("code = %q, want %q", code, protocol.WireErrBadRequest)
	}

	recorder = call(server, "PUT", protocol.ReportPath, version(), credential, "not json")
	if recorder.Code != 400 {
		t.Fatalf("malformed report = %d, want 400", recorder.Code)
	}

	after, ok := server.network.reports.get("laptop")
	if !ok || after.request.Seq != before.request.Seq {
		t.Fatalf("a refused report replaced the previous one: %+v", after)
	}

	// And the host table is untouched by all of it.
	hosts, err := store.Hosts(context.Background())
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Name != "laptop" {
		t.Fatalf("hosts = %+v", hosts)
	}
}
