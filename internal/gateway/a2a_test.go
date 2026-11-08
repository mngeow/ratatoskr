package gateway

// Tests for the A2A participant layer: the SQLite store over migration 0003, the task service, and the HTTP
// handler with the SDK behind it. UNTESTED: written and type-checked, not run (GR-3).

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mngeow/ratatoskr/internal/a2a"
)

// fakeDispatcher records what the executor hands to a target.
type fakeDispatcher struct {
	mu         sync.Mutex
	unavailble error
	failWith   error
	calls      int
	sent       []a2a.Dispatch
}

func (f *fakeDispatcher) Available(context.Context, a2a.Target) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.unavailble
}

func (f *fakeDispatcher) Dispatch(_ context.Context, d a2a.Dispatch) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failWith != nil {
		return f.failWith
	}
	f.sent = append(f.sent, d)
	return nil
}

// a2aFixture is a store with one paired host that has two sessions, two enrolled participants (one with a
// URL) and the A2A handler over a fake dispatcher.
type a2aFixture struct {
	store      *Store
	ctx        context.Context
	handler    *a2a.Handler
	svc        *a2a.Service
	dispatcher *fakeDispatcher
	hostCred   string
	sessionA   string // a live session on host "vmb", name "alpha"
	sessionB   string // a live session on host "vmb", name "beta"
	service    string // credential of participant "svc" (registered, has a URL)
	ingress    string // credential of participant "ingress" (no URL)
	now        time.Time
}

func newA2AFixture(t *testing.T) *a2aFixture {
	t.Helper()
	f := &a2aFixture{ctx: context.Background(), now: time.Unix(1700000000, 0), dispatcher: &fakeDispatcher{}}
	f.store, _ = openStore(t, false)
	f.hostCred = pairHostFrom(t, f.store, "vmb", f.now)

	add := func(name string) string {
		row, err := f.store.AddSession(f.ctx, "vmb", name, f.now, randomID)
		if err != nil {
			t.Fatalf("AddSession(%s): %v", name, err)
		}
		return row.ID
	}
	f.sessionA, f.sessionB = add("alpha"), add("beta")

	admin := newA2AAdmin(f.store, func() time.Time { return f.now })
	var err error
	if f.service, err = admin.Enrol(f.ctx, "svc"); err != nil {
		t.Fatalf("enrol svc: %v", err)
	}
	if f.ingress, err = admin.Enrol(f.ctx, "ingress"); err != nil {
		t.Fatalf("enrol ingress: %v", err)
	}
	// Register without a card fetch: the test only needs the stored destination.
	if err := f.store.A2AStore().SetRegistration(f.ctx, "svc", "http://127.0.0.1:7790/", "", false, `{"name":"svc"}`, a2a.AuthBearer, "fixture-outbound-secret"); err != nil {
		t.Fatalf("register svc: %v", err)
	}

	f.svc = &a2a.Service{Store: f.store.A2AStore(), Dispatcher: f.dispatcher, Now: func() time.Time { return f.now }}
	f.handler, err = newA2AHandler(f.store, "http://100.64.0.1:7777", f.svc)
	if err != nil {
		t.Fatalf("newA2AHandler: %v", err)
	}
	return f
}

// rpc posts one JSON-RPC call and returns the HTTP status and the decoded body.
func (f *a2aFixture) rpc(t *testing.T, ref, credential, session, version, method, params string) (int, map[string]any) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
	request := httptest.NewRequest(http.MethodPost, a2a.AgentsPath+ref, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if credential != "" {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	if session != "" {
		request.Header.Set(a2a.SessionHeader, session)
	}
	if version != "" {
		request.Header.Set("A2A-Version", version)
	}
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)

	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded
}

func errorCode(body map[string]any) int {
	if e, ok := body["error"].(map[string]any); ok {
		if code, ok := e["code"].(float64); ok {
			return int(code)
		}
	}
	return 0
}

// taskIDOf finds the task id in a SendMessage or GetTask result, whichever shape the SDK wraps it in.
func taskIDOf(body map[string]any) string {
	result, _ := body["result"].(map[string]any)
	if task, ok := result["task"].(map[string]any); ok {
		result = task
	}
	id, _ := result["id"].(string)
	return id
}

const textMessage = `{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello"}]}}`

func TestA2AStoreRoundTrip(t *testing.T) {
	f := newA2AFixture(t)
	store := f.store.A2AStore()
	owner := a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionA}
	target := a2a.Target{Kind: a2a.TargetService, ID: "svc"}

	// A duplicate enrolment is refused, and a revoked name stays taken.
	if err := store.CreateParticipant(f.ctx, a2a.Participant{ID: "svc", CreatedAt: f.now}, "hash"); !errors.Is(err, a2a.ErrExists) {
		t.Fatalf("duplicate enrol = %v", err)
	}

	create := func(id string) {
		t.Helper()
		err := store.CreateTask(f.ctx, a2a.Task{ID: id, ContextID: "c", Owner: owner, Target: target, State: a2a.StateSubmitted,
			Body: []byte(`{"id":"` + id + `","contextId":"c","status":{"state":"TASK_STATE_SUBMITTED"}}`), CreatedAt: f.now, UpdatedAt: f.now})
		if err != nil {
			t.Fatalf("CreateTask(%s): %v", id, err)
		}
	}
	create("t1")
	create("t2")
	create("t3")
	if err := store.CreateTask(f.ctx, a2a.Task{ID: "t1", Owner: owner, Target: target, Body: []byte("{}")}); !errors.Is(err, a2a.ErrExists) {
		t.Fatalf("duplicate task = %v", err)
	}

	// SaveTask is a compare-and-set on the store's own version.
	got, found, _ := store.GetTask(f.ctx, "t1")
	if !found || got.Version != 1 {
		t.Fatalf("t1 = %+v found=%t", got, found)
	}
	saved, err := store.SaveTask(f.ctx, got)
	if err != nil || saved.Version != 2 {
		t.Fatalf("save: %+v %v", saved, err)
	}
	if _, err := store.SaveTask(f.ctx, got); !errors.Is(err, a2a.ErrConflict) {
		t.Fatalf("a stale save = %v, want a conflict", err)
	}

	// Listing is caller-scoped and cursor-paginated, newest first.
	page, next, err := store.ListTasks(f.ctx, a2a.ListFilter{Caller: owner, PageSize: 2})
	if err != nil || len(page) != 2 || next == "" || page[0].ID != "t3" {
		t.Fatalf("first page: %v next=%q err=%v", ids(page), next, err)
	}
	page, next, err = store.ListTasks(f.ctx, a2a.ListFilter{Caller: owner, PageSize: 2, Cursor: next})
	if err != nil || len(page) != 1 || page[0].ID != "t1" || next != "" {
		t.Fatalf("second page: %v next=%q err=%v", ids(page), next, err)
	}
	stranger := a2a.Principal{Kind: a2a.KindParticipant, Name: "ingress"}
	if page, _, _ := store.ListTasks(f.ctx, a2a.ListFilter{Caller: stranger}); len(page) != 0 {
		t.Fatalf("a stranger listed %v", ids(page))
	}
	party := a2a.Principal{Kind: a2a.KindParticipant, Name: "svc"}
	if page, _, _ := store.ListTasks(f.ctx, a2a.ListFilter{Caller: party}); len(page) != 3 {
		t.Fatalf("the target listed %v, want 3", ids(page))
	}

	// Observations, deletion by the owner's filters only, and unpairing never touching tasks.
	if err := store.AddObservation(f.ctx, a2a.Observation{TaskID: "t1", Kind: a2a.ObsDelivery, Value: "stored", At: f.now}); err != nil {
		t.Fatal(err)
	}
	if obs, _ := store.Observations(f.ctx, "t1"); len(obs) != 1 {
		t.Fatalf("observations = %v", obs)
	}
	if _, err := f.store.db.Exec(`DELETE FROM hosts WHERE name='vmb'`); err != nil {
		t.Fatal(err)
	}
	if n, err := store.DeleteTasks(f.ctx, a2a.DeleteFilter{AllFinal: true}); err != nil || n != 0 {
		t.Fatalf("delete all final = %d, %v (open tasks must survive, and so must tasks after unpairing)", n, err)
	}
	if n, err := store.DeleteTasks(f.ctx, a2a.DeleteFilter{TaskID: "t2"}); err != nil || n != 1 {
		t.Fatalf("delete t2 = %d, %v", n, err)
	}
	if _, found, _ := store.GetTask(f.ctx, "t1"); !found {
		t.Fatal("t1 vanished")
	}
}

func ids(tasks []a2a.Task) []string {
	out := make([]string, len(tasks))
	for i, t := range tasks {
		out[i] = t.ID
	}
	return out
}

func TestA2AAuthentication(t *testing.T) {
	f := newA2AFixture(t)
	const params = textMessage
	tests := []struct {
		name                         string
		credential, session, version string
		ref                          string
		wantStatus                   int
	}{
		{"no credential", "", "", "1.0", f.sessionB, http.StatusUnauthorized},
		{"unknown credential", "nonsense", "", "1.0", f.sessionB, http.StatusUnauthorized},
		{"host credential without a session", f.hostCred, "", "1.0", f.sessionB, http.StatusForbidden},
		{"host credential naming an unknown session", f.hostCred, "ffffffffffffffffffffffffffffffff", "1.0", f.sessionB, http.StatusForbidden},
		{"host credential naming a current session", f.hostCred, f.sessionA, "1.0", f.sessionB, http.StatusOK},
		{"participant credential", f.ingress, "", "1.0", f.sessionB, http.StatusOK},
		{"unknown target", f.ingress, "", "1.0", "nobody", http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := f.rpc(t, tc.ref, tc.credential, tc.session, tc.version, "SendMessage", params)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}

	// A revoked participant is rejected on its very next call, like any unknown credential.
	if _, err := newA2AAdmin(f.store, nil).Revoke(f.ctx, "ingress", nil); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage); status != http.StatusUnauthorized {
		t.Fatalf("revoked participant status = %d", status)
	}

	// A host credential is also not enough once the session it names has ended.
	if _, err := f.store.db.Exec(`UPDATE sessions SET ended_at=1 WHERE id=?`, f.sessionA); err != nil {
		t.Fatal(err)
	}
	if status, _ := f.rpc(t, f.sessionB, f.hostCred, f.sessionA, "1.0", "SendMessage", textMessage); status != http.StatusForbidden {
		t.Fatalf("ended session status = %d", status)
	}
}

func TestA2ASendMessageOriginScopingAndRefusals(t *testing.T) {
	f := newA2AFixture(t)

	// The origin is the authenticated principal; a "from" in the body is ignored.
	forged := `{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hello"}],"from":"someone-else","metadata":{"from":"someone-else"}}}`
	status, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", forged)
	taskID := taskIDOf(body)
	if status != http.StatusOK || taskID == "" {
		t.Fatalf("send: %d %v", status, body)
	}
	f.dispatcher.mu.Lock()
	sent := append([]a2a.Dispatch(nil), f.dispatcher.sent...)
	f.dispatcher.mu.Unlock()
	if len(sent) != 1 || sent[0].From.Origin() != "ingress" || sent[0].Text != "hello" || sent[0].Task.Target.ID != f.sessionB {
		t.Fatalf("dispatched = %+v", sent)
	}

	// The owner and the target can read the task; a third principal gets TaskNotFound, and its list omits it.
	if code := errorCode(mustRPC(f, t, f.sessionB, f.hostCred, f.sessionA, "GetTask", `{"id":"`+taskID+`"}`)); code != -32001 {
		t.Fatalf("a session that is not a party got code %d, want -32001", code)
	}
	if got := taskIDOf(mustRPC(f, t, f.sessionB, f.ingress, "", "GetTask", `{"id":"`+taskID+`"}`)); got != taskID {
		t.Fatalf("the owner could not read its own task: %q", got)
	}
	_, list := f.rpc(t, f.sessionB, f.hostCred, f.sessionA, "1.0", "ListTasks", `{}`)
	if result, _ := list["result"].(map[string]any); result != nil {
		if tasks, _ := result["tasks"].([]any); len(tasks) != 0 {
			t.Fatalf("a stranger's ListTasks returned %d tasks", len(tasks))
		}
	}

	// Refusals: version, streaming, push, extended card, cancel of an open task, unknown method.
	tests := []struct {
		name, version, method, params string
		want                          int
	}{
		{"empty version", "", "SendMessage", textMessage, -32009},
		{"unsupported version", "0.3", "SendMessage", textMessage, -32009},
		{"streaming", "1.0", "SendStreamingMessage", textMessage, -32004},
		{"subscribe", "1.0", "SubscribeToTask", `{"id":"` + taskID + `"}`, -32004},
		{"push", "1.0", "CreateTaskPushNotificationConfig", `{"taskId":"` + taskID + `"}`, -32003},
		{"extended card", "1.0", "GetExtendedAgentCard", `{}`, -32004},
		{"cancel an open task", "1.0", "CancelTask", `{"id":"` + taskID + `"}`, -32002},
		{"unknown method", "1.0", "tasks/frobnicate", `{}`, -32601},
		{"non-text part", "1.0", "SendMessage", `{"message":{"messageId":"m2","role":"ROLE_USER","parts":[{"url":"http://x/"}]}}`, -32005},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, got := f.rpc(t, f.sessionB, f.ingress, "", tc.version, tc.method, tc.params)
			if code := errorCode(got); code != tc.want {
				t.Fatalf("code = %d, want %d (%v)", code, tc.want, got)
			}
		})
	}
	// The only effect of all those refusals is no new dispatch.
	f.dispatcher.mu.Lock()
	defer f.dispatcher.mu.Unlock()
	if len(f.dispatcher.sent) != 1 {
		t.Fatalf("%d dispatches, want only the first", len(f.dispatcher.sent))
	}
}

func mustRPC(f *a2aFixture, t *testing.T, ref, credential, session, method, params string) map[string]any {
	t.Helper()
	_, body := f.rpc(t, ref, credential, session, "1.0", method, params)
	return body
}

func TestA2AOfflineTargetCreatesNothing(t *testing.T) {
	f := newA2AFixture(t)
	f.dispatcher.unavailble = a2a.ErrTargetOffline
	status, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
	if status != http.StatusOK || errorCode(body) == 0 {
		t.Fatalf("offline: %d %v", status, body)
	}
	if n, _, _ := f.store.A2AStore().ListTasks(f.ctx, a2a.ListFilter{Caller: a2a.Principal{Kind: a2a.KindParticipant, Name: "ingress"}}); len(n) != 0 {
		t.Fatalf("an offline target stored %d task(s)", len(n))
	}
}

func TestA2AAmbiguousAliasIsRefusedWithCandidates(t *testing.T) {
	f := newA2AFixture(t)
	// A second host with a session also named "alpha".
	pairHostFrom(t, f.store, "other", f.now)
	if _, err := f.store.AddSession(f.ctx, "other", "alpha", f.now, randomID); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, a2a.AgentsPath+"alpha", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+f.ingress)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || !bytes.Contains(recorder.Body.Bytes(), []byte("ambiguous_reference")) ||
		bytes.Count(recorder.Body.Bytes(), []byte(`"host"`)) != 2 {
		t.Fatalf("ambiguous alias: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestA2ACardIsPublicAndDeclaresNothingFalse(t *testing.T) {
	f := newA2AFixture(t)
	request := httptest.NewRequest(http.MethodGet, a2a.AgentsPath+f.sessionA+a2a.CardSuffix, nil)
	recorder := httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("card: %d %s", recorder.Code, recorder.Body.String())
	}
	var card map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card["name"] != "alpha@vmb" {
		t.Fatalf("card name = %v", card["name"])
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte("http://100.64.0.1:7777/a2a/agents/"+f.sessionA)) {
		t.Fatalf("the card's own URL is not the configured base: %s", recorder.Body.String())
	}
	// A request header never decides the card's URL.
	request = httptest.NewRequest(http.MethodGet, a2a.AgentsPath+f.sessionA+a2a.CardSuffix, nil)
	request.Host = "evil.example"
	request.Header.Set("X-Forwarded-Host", "evil.example")
	recorder = httptest.NewRecorder()
	f.handler.ServeHTTP(recorder, request)
	if bytes.Contains(recorder.Body.Bytes(), []byte("evil.example")) {
		t.Fatal("the card took its URL from a request header")
	}
}

func TestA2AServiceRules(t *testing.T) {
	f := newA2AFixture(t)
	owner := a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionA}
	service := &a2a.Service{Store: f.store.A2AStore(), Dispatcher: f.dispatcher, Now: func() time.Time { return f.now }}
	target := a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"}
	create := func(id string) {
		t.Helper()
		body := `{"id":"` + id + `","contextId":"c","status":{"state":"TASK_STATE_WORKING"}}`
		if err := f.store.A2AStore().CreateTask(f.ctx, a2a.Task{ID: id, ContextID: "c", Owner: owner, Target: target, State: a2a.StateWorking, Body: []byte(body), CreatedAt: f.now, UpdatedAt: f.now}); err != nil {
			t.Fatal(err)
		}
	}
	create("w1")

	// Only the target may report; anyone else sees not-found.
	if _, err := service.Report(f.ctx, owner, "w1", "completed", "done", ""); !errors.Is(err, a2a.ErrNotFound) {
		t.Fatalf("the owner reported on its own task: %v", err)
	}
	targetPrincipal := a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionB}
	if _, err := service.Report(f.ctx, targetPrincipal, "w1", "WORKING", "", ""); err == nil {
		t.Fatal("a report may not set WORKING")
	}
	if task, err := service.Report(f.ctx, targetPrincipal, "w1", "input_required", "which format?", ""); err != nil || task.State != a2a.StateInputRequired {
		t.Fatalf("input_required: %+v %v", task, err)
	}
	if task, err := service.Report(f.ctx, targetPrincipal, "w1", "completed", "done", "the result"); err != nil || task.State != a2a.StateCompleted {
		t.Fatalf("completed: %+v %v", task, err)
	}
	if _, err := service.Report(f.ctx, targetPrincipal, "w1", "failed", "late", ""); !errors.Is(err, a2a.ErrFinal) {
		t.Fatalf("a final task changed again: %v", err)
	}

	// A lost connection is an observation, never a state change.
	create("w2")
	if err := service.ConnectionLost(f.ctx, target); err != nil {
		t.Fatal(err)
	}
	got, _, _ := f.store.A2AStore().GetTask(f.ctx, "w2")
	obs, _ := f.store.A2AStore().Observations(f.ctx, "w2")
	if got.State != a2a.StateWorking || len(obs) != 1 || obs[0].Kind != a2a.ObsConnection {
		t.Fatalf("after a lost connection: state %s, observations %v", got.State, obs)
	}

	// A session that ends fails its open tasks with the reason; final tasks are left alone.
	if err := service.SessionEnded(f.ctx, f.sessionB); err != nil {
		t.Fatal(err)
	}
	got, _, _ = f.store.A2AStore().GetTask(f.ctx, "w2")
	done, _, _ := f.store.A2AStore().GetTask(f.ctx, "w1")
	if got.State != a2a.StateFailed || got.Reason != "session ended" || done.State != a2a.StateCompleted {
		t.Fatalf("after the session ended: w2=%s/%q w1=%s", got.State, got.Reason, done.State)
	}
}

// TestA2AUncertainDispatchLeavesTheTaskSubmitted: an unknown outcome is not a state. Only a dispatch known
// not to have arrived ends the task, and nothing is retried.
func TestA2AUncertainDispatchLeavesTheTaskSubmitted(t *testing.T) {
	stateOf := func(f *a2aFixture, taskID string) string {
		got, _, _ := f.store.A2AStore().GetTask(f.ctx, taskID)
		return got.State
	}
	attempts := func(f *a2aFixture) int {
		f.dispatcher.mu.Lock()
		defer f.dispatcher.mu.Unlock()
		return f.dispatcher.calls
	}

	t.Run("unknown outcome", func(t *testing.T) {
		f := newA2AFixture(t)
		f.dispatcher.failWith = errors.New("the stream write was cut")
		_, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
		taskID := taskIDOf(body)
		if taskID == "" {
			t.Fatalf("no task came back: %v", body)
		}
		if got := stateOf(f, taskID); got != a2a.StateSubmitted {
			t.Fatalf("state after an uncertain dispatch = %s, want SUBMITTED", got)
		}
		if attempts(f) != 1 {
			t.Fatalf("%d dispatch attempts, want exactly one (never retried)", attempts(f))
		}
		// The target can still report, because the task is not final.
		service := &a2a.Service{Store: f.store.A2AStore(), Dispatcher: f.dispatcher}
		target := a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionB}
		if task, err := service.Report(f.ctx, target, taskID, "completed", "done", ""); err != nil || task.State != a2a.StateCompleted {
			t.Fatalf("the target could not report on an uncertain task: %+v %v", task, err)
		}
	})

	t.Run("known not delivered", func(t *testing.T) {
		f := newA2AFixture(t)
		f.dispatcher.failWith = errors.Join(a2a.ErrNotDelivered, errors.New("refused before any write"))
		_, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
		taskID := taskIDOf(body)
		if taskID == "" {
			t.Fatalf("no task came back: %v", body)
		}
		if got := stateOf(f, taskID); got != a2a.StateFailed {
			t.Fatalf("state after a known non-delivery = %s, want FAILED", got)
		}
	})
}

// TestExtOriginIsNeverAHostOrSessionAddress: an external sender's origin cannot equal or parse as a
// name@host address, so it can never be mistaken for a paired session.
func TestExtOriginIsNeverAHostOrSessionAddress(t *testing.T) {
	for _, name := range []string{"svc", "reference-adapter", "a1_b2"} {
		origin := extOrigin(name)
		if _, _, ok := parseAddress(origin); ok {
			t.Fatalf("%q parses as a network address", origin)
		}
		if agentNamePattern.MatchString(origin) || hostNamePattern.MatchString(origin) {
			t.Fatalf("%q is a valid agent or host name", origin)
		}
		if strings.Contains(origin, "@") {
			t.Fatalf("%q contains @", origin)
		}
	}
	// And nothing a host or session may be called contains the colon that makes the prefix.
	for _, valid := range []string{"ext", "a2a", "vmb", "x-1", "codex"} {
		if !agentNamePattern.MatchString(valid) || strings.Contains(valid, ":") {
			t.Fatalf("test name %q is not a plain valid name", valid)
		}
	}
	if agentNamePattern.MatchString("ext:svc") || hostNamePattern.MatchString("ext:svc") {
		t.Fatal("a name or host with a colon was accepted")
	}
}

func (f *a2aFixture) task(t *testing.T, id, state string, target a2a.Target) {
	t.Helper()
	body := `{"id":"` + id + `","contextId":"c","status":{"state":"TASK_STATE_` + state + `"}}`
	err := f.store.A2AStore().CreateTask(f.ctx, a2a.Task{
		ID: id, ContextID: "c", Owner: a2a.Principal{Kind: a2a.KindParticipant, Name: "ingress"},
		Target: target, State: state, Body: []byte(body), CreatedAt: f.now, UpdatedAt: f.now,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *a2aFixture) report(server *Server, taskID, credential, session, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/tasks/"+taskID+"/report", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("X-Ratatoskr-Protocol", version())
	if session != "" {
		request.Header.Set(a2a.SessionHeader, session)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestA2ATaskReportRoute(t *testing.T) {
	f := newA2AFixture(t)
	server := NewServer(f.store, Options{Now: func() time.Time { return f.now }})
	f.task(t, "r1", a2a.StateWorking, a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"})

	// Same not-found for an unknown task and for a session that is not the bound target.
	unknown := f.report(server, "nope", f.hostCred, f.sessionB, `{"state":"completed"}`)
	foreign := f.report(server, "r1", f.hostCred, f.sessionA, `{"state":"completed"}`)
	if unknown.Code != http.StatusNotFound || foreign.Code != http.StatusNotFound || unknown.Body.String() != foreign.Body.String() {
		t.Fatalf("unknown %d %q vs foreign %d %q: must be the same answer", unknown.Code, unknown.Body.String(), foreign.Code, foreign.Body.String())
	}
	// A session that is not current for its host is also not found.
	if got := f.report(server, "r1", f.hostCred, "ffffffffffffffffffffffffffffffff", `{"state":"completed"}`); got.Code != http.StatusNotFound {
		t.Fatalf("a non-current session got %d", got.Code)
	}
	if got := f.report(server, "r1", f.hostCred, f.sessionB, `{"state":"working"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("a bad state got %d", got.Code)
	}
	if got := f.report(server, "r1", "wrong", f.sessionB, `{"state":"completed"}`); got.Code != http.StatusUnauthorized {
		t.Fatalf("a bad credential got %d", got.Code)
	}

	ok := f.report(server, "r1", f.hostCred, f.sessionB, `{"state":"completed","text":"done","result":"42"}`)
	if ok.Code != http.StatusNoContent {
		t.Fatalf("the bound target was refused: %d %s", ok.Code, ok.Body.String())
	}
	if got, _, _ := f.store.A2AStore().GetTask(f.ctx, "r1"); got.State != a2a.StateCompleted {
		t.Fatalf("state = %s", got.State)
	}
	// A report on a final task is refused, not applied.
	if again := f.report(server, "r1", f.hostCred, f.sessionB, `{"state":"failed"}`); again.Code != http.StatusConflict {
		t.Fatalf("a report on a final task got %d", again.Code)
	}
	if got, _, _ := f.store.A2AStore().GetTask(f.ctx, "r1"); got.State != a2a.StateCompleted {
		t.Fatalf("a refused report changed the state to %s", got.State)
	}
}

func TestA2ASessionEndFailsTheTasksAndNothingIsRedirected(t *testing.T) {
	f := newA2AFixture(t)
	server := NewServer(f.store, Options{Now: func() time.Time { return f.now }})
	f.task(t, "e1", a2a.StateSubmitted, a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"})
	f.task(t, "e2", a2a.StateCompleted, a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"})

	// A replacement session with the same name is a different participant.
	if _, err := f.store.AddSession(f.ctx, "vmb", "beta", f.now, randomID); err != nil {
		t.Fatal(err)
	}
	server.a2aSessionsChanged(f.ctx)

	e1, _, _ := f.store.A2AStore().GetTask(f.ctx, "e1")
	e2, _, _ := f.store.A2AStore().GetTask(f.ctx, "e2")
	if e1.State != a2a.StateFailed || e1.Reason != "session ended" || e2.State != a2a.StateCompleted {
		t.Fatalf("e1=%s/%q e2=%s", e1.State, e1.Reason, e2.State)
	}
}

func TestA2AReceiptsBecomeObservationsAndAcceptedIsWorking(t *testing.T) {
	f := newA2AFixture(t)
	server := NewServer(f.store, Options{Now: func() time.Time { return f.now }})
	f.task(t, "d1", a2a.StateSubmitted, a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"})
	if err := f.store.A2AStore().LinkMessage(f.ctx, "msg-d1", "d1", a2a.DispatchKind, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.store.A2AStore().LinkMessage(f.ctx, "msg-wake", "d1", a2a.WakeKind, f.now); err != nil {
		t.Fatal(err)
	}

	state := func() string { got, _, _ := f.store.A2AStore().GetTask(f.ctx, "d1"); return got.State }
	server.a2aReceipt(f.ctx, "msg-d1", "pasted")
	if state() != a2a.StateSubmitted {
		t.Fatalf("pasted moved the task to %s; only accepted may set WORKING", state())
	}
	server.a2aReceipt(f.ctx, "msg-wake", "accepted")
	if state() != a2a.StateSubmitted {
		t.Fatal("a wake message's receipt changed a task")
	}
	server.a2aReceipt(f.ctx, "msg-d1", "accepted")
	if state() != a2a.StateWorking {
		t.Fatalf("accepted left the task at %s", state())
	}
	obs, _ := f.store.A2AStore().Observations(f.ctx, "d1")
	if len(obs) != 2 || obs[0].Value != "pasted" || obs[1].Value != "accepted" {
		t.Fatalf("observations = %+v", obs)
	}
}

func TestA2ADeleteTasksAlsoRemovesTheLedgerMessages(t *testing.T) {
	f := newA2AFixture(t)
	f.task(t, "x1", a2a.StateCompleted, a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"})
	f.task(t, "x2", a2a.StateWorking, a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"})
	for _, m := range []struct{ id, task string }{{"mx1", "x1"}, {"mx2", "x2"}} {
		if _, _, err := f.store.InsertMessage(f.ctx, MessageIn{ID: m.id, From: "ext:ingress", To: "beta@vmb", Body: "b", At: f.now}); err != nil {
			t.Fatal(err)
		}
		if err := f.store.A2AStore().LinkMessage(f.ctx, m.id, m.task, a2a.DispatchKind, f.now); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.store.A2AStore().AddObservation(f.ctx, a2a.Observation{TaskID: "x1", Kind: a2a.ObsDelivery, Value: "stored", At: f.now}); err != nil {
		t.Fatal(err)
	}

	n, err := newA2AAdmin(f.store, nil).DeleteTasks(f.ctx, a2a.DeleteFilter{AllFinal: true})
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v", n, err)
	}
	if _, _, err := f.store.Message(f.ctx, "mx1"); !errors.Is(err, ErrNoSuchMessage) {
		t.Fatalf("the deleted task's ledger message is still there: %v", err)
	}
	if _, _, err := f.store.Message(f.ctx, "mx2"); err != nil {
		t.Fatalf("the open task's ledger message went too: %v", err)
	}
	if obs, _ := f.store.A2AStore().Observations(f.ctx, "x1"); len(obs) != 0 {
		t.Fatalf("observations survived: %v", obs)
	}
	if _, _, found, _ := f.store.A2AStore().TaskForMessage(f.ctx, "mx1"); found {
		t.Fatal("the dispatch link survived")
	}
}

func TestA2AClientFactoryAppliesTheTransportRules(t *testing.T) {
	ctx := context.Background()
	f0, _ := openStore(t, false)
	tests := []struct {
		name string
		dest a2a.Destination
		ok   bool
	}{
		{"loopback http", a2a.Destination{URL: "http://127.0.0.1:7790/rpc"}, true},
		{"tailnet http", a2a.Destination{URL: "http://100.64.0.5:7790/"}, true},
		{"lan http without the opt-in", a2a.Destination{URL: "http://192.168.1.9:7790/"}, false},
		{"lan http with the opt-in", a2a.Destination{URL: "http://192.168.1.9:7790/", InsecureHTTP: true}, true},
		{"public address", a2a.Destination{URL: "http://8.8.8.8:80/", InsecureHTTP: true}, false},
		{"a name", a2a.Destination{URL: "http://svc.test:80/"}, false},
		{"lan https without a pin", a2a.Destination{URL: "https://192.168.1.9:7790/"}, false},
		{"tailnet v6 http without the opt-in", a2a.Destination{URL: "http://[fd7a:115c:a1e0::5]:7790/"}, true},
		{"loopback https is refused", a2a.Destination{URL: "https://127.0.0.1:7790/", Fingerprint: "sha256:" + strings.Repeat("ab", 32)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client, err := a2aClientFactoryFor(f0)(ctx, tc.dest)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok=%t", err, tc.ok)
			}
			if err == nil {
				if client.Transport == nil || client.CheckRedirect == nil || client.Timeout != 5*time.Second {
					t.Fatalf("client = %+v: needs a transport, no redirects and a 5 s total timeout", client)
				}
			}
		})
	}
}

func TestA2AApplyRemoteMirrorsTheServicesOwnState(t *testing.T) {
	f := newA2AFixture(t)
	service := &a2a.Service{Store: f.store.A2AStore(), Dispatcher: f.dispatcher, Now: func() time.Time { return f.now }}
	f.task(t, "m1", a2a.StateSubmitted, a2a.Target{Kind: a2a.TargetService, ID: "svc"})
	get := func() a2a.Task { got, _, _ := f.store.A2AStore().GetTask(f.ctx, "m1"); return got }

	changed, err := service.ApplyRemote(f.ctx, "m1", a2a.RemoteTask{ID: "r-1", State: "INPUT_REQUIRED", Text: "which format?"})
	if err != nil || !changed || get().State != a2a.StateInputRequired || get().RemoteTaskID != "r-1" {
		t.Fatalf("input required: changed=%t err=%v task=%+v", changed, err, get())
	}
	// The same view again changes nothing (no version churn at the poll rate).
	version := get().Version
	if changed, _ := service.ApplyRemote(f.ctx, "m1", a2a.RemoteTask{ID: "r-1", State: "INPUT_REQUIRED", Text: "which format?"}); changed || get().Version != version {
		t.Fatalf("an unchanged mirror wrote: changed=%t version %d->%d", changed, version, get().Version)
	}
	// An unmapped state changes nothing and leaves an observation.
	if changed, _ := service.ApplyRemote(f.ctx, "m1", a2a.RemoteTask{ID: "r-1", State: "AUTH_REQUIRED"}); changed || get().State != a2a.StateInputRequired {
		t.Fatalf("an unmapped state moved the task to %s", get().State)
	}
	if obs, _ := f.store.A2AStore().Observations(f.ctx, "m1"); len(obs) != 1 || obs[0].Kind != a2a.ObsRemoteState {
		t.Fatalf("observations = %+v", obs)
	}
	// Completion carries the service's results.
	if _, err := service.ApplyRemote(f.ctx, "m1", a2a.RemoteTask{ID: "r-1", State: "COMPLETED", Results: []string{"lines: 3"}}); err != nil || get().State != a2a.StateCompleted {
		t.Fatalf("completed: %v %+v", err, get())
	}
	body, _ := a2a.DecodeBody(get())
	if len(body.Artifacts) != 1 || body.Artifacts[0].Parts[0].Text() != "lines: 3" {
		t.Fatalf("artifacts = %+v", body.Artifacts)
	}
	// Final stays final.
	if changed, _ := service.ApplyRemote(f.ctx, "m1", a2a.RemoteTask{ID: "r-1", State: "WORKING"}); changed || get().State != a2a.StateCompleted {
		t.Fatalf("a final task moved to %s", get().State)
	}

	// A service-side CANCELED is FAILED here: no CANCELED state is produced.
	f.task(t, "m2", a2a.StateWorking, a2a.Target{Kind: a2a.TargetService, ID: "svc"})
	if _, err := service.ApplyRemote(f.ctx, "m2", a2a.RemoteTask{ID: "r-2", State: "CANCELED"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.store.A2AStore().GetTask(f.ctx, "m2"); got.State != a2a.StateFailed || got.Reason != "the service canceled the task" {
		t.Fatalf("canceled -> %s/%q", got.State, got.Reason)
	}
}

// fakeFetcher stands in for the card fetch so Register can be driven without a service.
type fakeFetcher struct{ auth a2a.CardAuth }

func (f fakeFetcher) FetchCard(context.Context, a2a.Destination) (string, a2a.CardAuth, error) {
	return `{"name":"svc"}`, f.auth, nil
}

// TestA2ALanHTTPWithTheOptInRegistersAndBuildsAClient: the owner's opt-in is the A2A gate; the card-target
// parser underneath must not be asked for it a second time (it rejects the flag for http).
func TestA2ALanHTTPWithTheOptInRegistersAndBuildsAClient(t *testing.T) {
	f := newA2AFixture(t)
	admin := &a2a.Admin{Store: f.store.A2AStore(), Fetcher: fakeFetcher{auth: a2a.CardAuthBearer}, Now: func() time.Time { return f.now }}
	const lan = "http://192.168.1.9:7790/"

	if _, err := admin.Register(f.ctx, "ingress", lan, "", false, false); !errors.Is(err, a2a.ErrNeedsOptIn) {
		t.Fatalf("LAN http without the opt-in = %v", err)
	}
	result, err := admin.Register(f.ctx, "ingress", lan, "", true, false)
	if err != nil || result.Secret == "" || !result.PlainHTTPLAN {
		t.Fatalf("LAN http with the opt-in = %+v, %v", result, err)
	}
	p, found, _ := f.store.A2AStore().GetParticipant(f.ctx, "ingress")
	if !found || p.URL != lan || !p.InsecureHTTP || p.Auth != a2a.AuthBearer {
		t.Fatalf("stored participant = %+v", p)
	}
	if _, err := a2aClientFactoryFor(f.store)(f.ctx, destinationOf(p)); err != nil {
		t.Fatalf("the stored destination cannot build a client: %v", err)
	}
}

// TestA2ARegisterAuthMatrix: the card's declaration decides, and --no-auth is explicit.
func TestA2ARegisterAuthMatrix(t *testing.T) {
	tests := []struct {
		name      string
		declared  a2a.CardAuth
		noAuth    bool
		wantErr   error
		wantAuth  string
		wantToken bool
	}{
		{"bearer card", a2a.CardAuthBearer, false, nil, a2a.AuthBearer, true},
		{"no security without the flag", a2a.CardAuthNone, false, a2a.ErrNoAuthDeclared, "", false},
		{"no security with --no-auth", a2a.CardAuthNone, true, nil, a2a.AuthNone, false},
		{"unsupported scheme", a2a.CardAuthUnsupported, false, a2a.ErrUnsupportedAuth, "", false},
		{"unsupported scheme even with --no-auth", a2a.CardAuthUnsupported, true, a2a.ErrUnsupportedAuth, "", false},
		{"bearer card with --no-auth", a2a.CardAuthBearer, true, a2a.ErrAuthMismatch, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newA2AFixture(t)
			admin := &a2a.Admin{Store: f.store.A2AStore(), Fetcher: fakeFetcher{auth: tc.declared}, Now: func() time.Time { return f.now }}
			result, err := admin.Register(f.ctx, "ingress", "http://127.0.0.1:7790/", "", false, tc.noAuth)
			if !errors.Is(err, tc.wantErr) && !(err == nil && tc.wantErr == nil) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			p, _, _ := f.store.A2AStore().GetParticipant(f.ctx, "ingress")
			if tc.wantErr != nil {
				if p.URL != "" || result.Secret != "" {
					t.Fatalf("a refused registration stored or returned something: %+v %+v", p, result)
				}
				return
			}
			if p.Auth != tc.wantAuth || (result.Secret != "") != tc.wantToken {
				t.Fatalf("auth=%q secret=%t, want %q %t", p.Auth, result.Secret != "", tc.wantAuth, tc.wantToken)
			}
			if tc.wantToken && len(result.Secret) < 43 {
				t.Fatalf("secret is only %d chars (256 bits need 43)", len(result.Secret))
			}
		})
	}
}

// TestA2AReRegisterRotatesTheSecret: a new secret replaces the old; the old one is gone from the store.
func TestA2AReRegisterRotatesTheSecret(t *testing.T) {
	f := newA2AFixture(t)
	admin := &a2a.Admin{Store: f.store.A2AStore(), Fetcher: fakeFetcher{auth: a2a.CardAuthBearer}, Now: func() time.Time { return f.now }}
	first, err := admin.Register(f.ctx, "ingress", "http://127.0.0.1:7790/", "", false, false)
	if err != nil || first.Rotated {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := admin.Register(f.ctx, "ingress", "http://127.0.0.1:7790/", "", false, false)
	if err != nil || !second.Rotated || second.Secret == first.Secret {
		t.Fatalf("second: %+v %v", second, err)
	}
	stored, err := f.store.A2AStore().OutboundSecret(f.ctx, "ingress")
	if err != nil || stored != second.Secret || stored == first.Secret {
		t.Fatalf("stored secret is not the rotated one: %v", err)
	}
}

// TestA2AOutboundSecretIsAttachedOnlyToItsOwnDestination: the factory adds the header for a Bearer
// registration, to its own host only, and adds nothing for a no-auth registration or the card fetch.
func TestA2AOutboundSecretIsAttachedOnlyToItsOwnDestination(t *testing.T) {
	f := newA2AFixture(t)
	var seen http.Header
	base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen = r.Header.Clone()
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: r}, nil
	})
	wrap := func(auth string) *http.Client {
		dest := a2a.Destination{ID: "svc", URL: "http://127.0.0.1:7790/", Auth: auth}
		client, err := a2aClientFactoryFor(f.store)(f.ctx, dest)
		if err != nil {
			t.Fatal(err)
		}
		if bt, ok := client.Transport.(*bearerTransport); ok {
			bt.base = base
		} else {
			client.Transport = base
		}
		return client
	}
	do := func(client *http.Client, url string) error {
		request, _ := http.NewRequest(http.MethodPost, url, nil)
		_, err := client.Do(request)
		return err
	}

	seen = nil
	if err := do(wrap(a2a.AuthBearer), "http://127.0.0.1:7790/rpc"); err != nil || seen.Get("Authorization") != "Bearer fixture-outbound-secret" {
		t.Fatalf("bearer registration: header=%q err=%v", seen.Get("Authorization"), err)
	}
	// Another host through the same client: refused, nothing sent.
	seen = nil
	if err := do(wrap(a2a.AuthBearer), "http://127.0.0.1:9999/rpc"); err == nil || seen != nil {
		t.Fatalf("a request for another host went out (err=%v, header=%v)", err, seen)
	}
	// No-auth registration and the public card fetch carry no Authorization.
	for _, auth := range []string{a2a.AuthNone, ""} {
		seen = nil
		if err := do(wrap(auth), "http://127.0.0.1:7790/rpc"); err != nil || seen.Get("Authorization") != "" {
			t.Fatalf("auth=%q: header=%q err=%v", auth, seen.Get("Authorization"), err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestA2AOutboundSecretNeverAppearsInListOrErrors: the secret is absent from list output, from the
// participant struct, from a failing send's error text and from observations.
func TestA2AOutboundSecretNeverAppearsInListOrErrors(t *testing.T) {
	f := newA2AFixture(t)
	const secret = "fixture-outbound-secret"

	var out, errOut bytes.Buffer
	if err := runParticipant(f.ctx, f.store, []string{"list"}, &out, &errOut, f.now); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), secret) || strings.Contains(errOut.String(), secret) || !strings.Contains(out.String(), "bearer") {
		t.Fatalf("list output:\n%s", out.String())
	}
	all, _ := f.store.A2AStore().ListParticipants(f.ctx)
	if encoded, _ := json.Marshal(all); strings.Contains(string(encoded), secret) {
		t.Fatalf("a participant struct carries the secret: %s", encoded)
	}

	// A failing request: the service is unreachable, and the error text must not carry the secret.
	remote := &a2a.Remote{Factory: func(ctx context.Context, dest a2a.Destination) (*http.Client, error) {
		client, err := a2aClientFactoryFor(f.store)(ctx, dest)
		if err != nil {
			return nil, err
		}
		client.Transport.(*bearerTransport).base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed for " + r.Header.Get("Authorization"))
		})
		return client, nil
	}}
	p, _, _ := f.store.A2AStore().GetParticipant(f.ctx, "svc")
	_, err := remote.Send(f.ctx, destinationOf(p), a2a.RemoteSend{MessageID: "m1", Text: "hi"})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("send error = %v", err)
	}
	if _, err := remote.Get(f.ctx, destinationOf(p), "r1"); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("get error = %v", err)
	}
	// Observations recorded for refusals carry only fixed words.
	f.task(t, "sx", a2a.StateSubmitted, a2a.Target{Kind: a2a.TargetService, ID: "svc"})
	if err := (&Server{store: f.store, opts: Options{Now: func() time.Time { return f.now }}}).a2aObserve(f.ctx, "sx", a2a.ObsAuth, "refused"); err != nil {
		t.Fatal(err)
	}
	obs, _ := f.store.A2AStore().Observations(f.ctx, "sx")
	if encoded, _ := json.Marshal(obs); strings.Contains(string(encoded), secret) {
		t.Fatalf("observations carry the secret: %s", encoded)
	}
}

// TestA2AListTasksTotalSizeIsBeforePaginationAndNewestFirst.
func TestA2AListTasksTotalSizeIsBeforePaginationAndNewestFirst(t *testing.T) {
	f := newA2AFixture(t)
	store := f.store.A2AStore()
	owner := a2a.Principal{Kind: a2a.KindParticipant, Name: "ingress"}
	target := a2a.Target{Kind: a2a.TargetSession, ID: f.sessionB, Host: "vmb"}
	for i, id := range []string{"o1", "o2", "o3"} {
		err := store.CreateTask(f.ctx, a2a.Task{ID: id, ContextID: "c", Owner: owner, Target: target, State: a2a.StateSubmitted,
			Body: []byte("{}"), CreatedAt: f.now.Add(time.Duration(i) * time.Second), UpdatedAt: f.now.Add(time.Duration(i) * time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}
	// o1 was updated last, so it is the newest by status timestamp even though it was created first.
	old, _, _ := store.GetTask(f.ctx, "o1")
	old.UpdatedAt = f.now.Add(time.Hour)
	if _, err := store.SaveTask(f.ctx, old); err != nil {
		t.Fatal(err)
	}

	filter := a2a.ListFilter{Caller: owner, PageSize: 2}
	page, next, err := store.ListTasks(f.ctx, filter)
	if err != nil || len(page) != 2 || page[0].ID != "o1" || page[1].ID != "o3" || next == "" {
		t.Fatalf("first page %v next=%q err=%v: want o1,o3 (status time descending)", ids(page), next, err)
	}
	total, err := store.CountTasks(f.ctx, filter)
	if err != nil || total != 3 {
		t.Fatalf("total = %d, %v: the count is before pagination", total, err)
	}
	filter.State = a2a.StateCompleted
	if total, _ := store.CountTasks(f.ctx, filter); total != 0 {
		t.Fatalf("filtered total = %d", total)
	}
	filter = a2a.ListFilter{Caller: owner, PageSize: 2, Cursor: next}
	page, next, _ = store.ListTasks(f.ctx, filter)
	if len(page) != 1 || page[0].ID != "o2" || next != "" {
		t.Fatalf("second page %v next=%q", ids(page), next)
	}
	// A stranger sees none, and the count agrees.
	stranger := a2a.Principal{Kind: a2a.KindParticipant, Name: "svc"}
	if total, _ := store.CountTasks(f.ctx, a2a.ListFilter{Caller: stranger}); total != 0 {
		t.Fatalf("a stranger counts %d", total)
	}
}

// TestA2AMirrorNeverHasMoreThanFourPending: with more due tasks than slots, at most four goroutines exist
// (pending or running); the rest stay due and are served by later scans.
func TestA2AMirrorNeverHasMoreThanFourPending(t *testing.T) {
	f := newA2AFixture(t)
	server := NewServer(f.store, Options{Now: func() time.Time { return f.now }})
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("p%d", i)
		f.task(t, id, a2a.StateWorking, a2a.Target{Kind: a2a.TargetService, ID: "svc"})
		task, _, _ := f.store.A2AStore().GetTask(f.ctx, id)
		task.RemoteTaskID = "r-" + id
		if _, err := f.store.A2AStore().SaveTask(f.ctx, task); err != nil {
			t.Fatal(err)
		}
	}

	release := make(chan struct{})
	var mu sync.Mutex
	entered, peak := 0, 0
	factory := func(ctx context.Context, _ a2a.Destination) (*http.Client, error) {
		mu.Lock()
		entered++
		if entered > peak {
			peak = entered
		}
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		mu.Lock()
		entered--
		mu.Unlock()
		return nil, errors.New("no client in this test")
	}
	mirror := newA2AMirror(server, factory)

	for i := 0; i < 5; i++ {
		mirror.scan(f.ctx)
	}
	mirror.mu.Lock()
	inFlight := len(mirror.inFlight)
	mirror.mu.Unlock()
	if inFlight > 4 || len(mirror.slots) > 4 {
		t.Fatalf("%d in flight, %d slots taken: the limit is four", inFlight, len(mirror.slots))
	}
	if inFlight != 4 {
		t.Fatalf("%d in flight; with ten due tasks exactly four should have started", inFlight)
	}

	close(release)
	mirror.wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if peak > 4 {
		t.Fatalf("peak concurrency %d", peak)
	}
}

// TestA2ARefusedCredentialFailsTheTaskWithAnActionableReason: a definite refusal carries its reason into the
// task, and the reason names neither a secret nor a header.
func TestA2ARefusedCredentialFailsTheTaskWithAnActionableReason(t *testing.T) {
	f := newA2AFixture(t)
	const reason = "the service refused the gateway's credential (HTTP 401/403); re-register the service and update its token"
	f.dispatcher.failWith = a2a.NotDelivered(reason)

	_, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
	taskID := taskIDOf(body)
	if taskID == "" {
		t.Fatalf("no task: %v", body)
	}
	task, _, _ := f.store.A2AStore().GetTask(f.ctx, taskID)
	if task.State != a2a.StateFailed {
		t.Fatalf("state = %s, want FAILED", task.State)
	}
	decoded, err := a2a.DecodeBody(task)
	if err != nil || decoded.Status.Message == nil || len(decoded.Status.Message.Parts) == 0 ||
		decoded.Status.Message.Parts[0].Text() != reason {
		t.Fatalf("the task does not carry the reason: %+v %v", decoded.Status, err)
	}
	if strings.Contains(strings.ToLower(reason), "bearer") || strings.Contains(strings.ToLower(reason), "authorization") {
		t.Fatal("the reason names the header")
	}
}

// TestA2AOutboundSecretIsAbsentFromEveryLog: a bearer service answers 401 and, separately, fails at the
// transport with an error that quotes the header. A send and a mirror scan run against it; neither the
// server's log (Options.Logf), nor the standard log package, nor any returned error text or observation
// contains the secret or "Bearer ". The status writer of Run is not reachable from a unit test; it is fed
// by the same Logf.
func TestA2AOutboundSecretIsAbsentFromEveryLog(t *testing.T) {
	f := newA2AFixture(t)
	const secret = "fixture-outbound-secret"

	var captured bytes.Buffer
	var capturedMu sync.Mutex
	logf := func(format string, args ...any) {
		capturedMu.Lock()
		defer capturedMu.Unlock()
		fmt.Fprintf(&captured, format+"\n", args...)
	}
	var standard bytes.Buffer
	log.SetOutput(&standard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	// A stub service that always refuses the credential.
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer refusing.Close()
	if err := f.store.A2AStore().SetRegistration(f.ctx, "svc", refusing.URL+"/", "", false, `{"name":"svc"}`, a2a.AuthBearer, secret); err != nil {
		t.Fatal(err)
	}

	server := NewServer(f.store, Options{Now: func() time.Time { return f.now }, Logf: logf})
	var texts []string

	// 1. A send: the service answers 401.
	f.task(t, "lg1", a2a.StateSubmitted, a2a.Target{Kind: a2a.TargetService, ID: "svc"})
	err := a2aDispatcher{server: server}.Dispatch(f.ctx, a2a.Dispatch{
		Task: a2a.Task{ID: "lg1", Target: a2a.Target{Kind: a2a.TargetService, ID: "svc"}},
		From: a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionA}, Text: "hi",
	})
	if err == nil || !errors.Is(err, a2a.ErrNotDelivered) {
		t.Fatalf("a refused credential must be a definite non-delivery, got %v", err)
	}
	texts = append(texts, err.Error())

	// 2. A mirror scan against the same refusing service: an observation only, no state change.
	task, _, _ := f.store.A2AStore().GetTask(f.ctx, "lg1")
	task.RemoteTaskID = "remote-1"
	if _, err := f.store.A2AStore().SaveTask(f.ctx, task); err != nil {
		t.Fatal(err)
	}
	mirror := newA2AMirror(server, a2aClientFactoryFor(f.store))
	mirror.scan(f.ctx)
	mirror.wg.Wait()
	if got, _, _ := f.store.A2AStore().GetTask(f.ctx, "lg1"); got.State != a2a.StateSubmitted {
		t.Fatalf("a refused read changed the state to %s", got.State)
	}

	// 3. A transport failure whose own text quotes the header, through the real bearer transport.
	quoting := newA2AMirror(server, func(ctx context.Context, dest a2a.Destination) (*http.Client, error) {
		client, err := a2aClientFactoryFor(f.store)(ctx, dest)
		if err != nil {
			return nil, err
		}
		client.Transport.(*bearerTransport).base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("Post " + r.URL.String() + ": Authorization: " + r.Header.Get("Authorization"))
		})
		return client, nil
	})
	quoting.scan(f.ctx)
	quoting.wg.Wait()
	p, _, _ := f.store.A2AStore().GetParticipant(f.ctx, "svc")
	_, err = (&a2a.Remote{Factory: func(ctx context.Context, dest a2a.Destination) (*http.Client, error) {
		client, err := a2aClientFactoryFor(f.store)(ctx, dest)
		if err != nil {
			return nil, err
		}
		client.Transport.(*bearerTransport).base = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return nil, errors.New("Authorization: " + r.Header.Get("Authorization"))
		})
		return client, nil
	}}).Send(f.ctx, destinationOf(p), a2a.RemoteSend{MessageID: "m1", Text: "hi"})
	if err == nil {
		t.Fatal("a failing transport returned no error")
	}
	texts = append(texts, err.Error())

	// Nothing may carry the secret or the header: the logs, the error texts, the observations.
	obs, _ := f.store.A2AStore().Observations(f.ctx, "lg1")
	for _, o := range obs {
		texts = append(texts, o.Kind+" "+o.Value)
	}
	capturedMu.Lock()
	texts = append(texts, captured.String())
	capturedMu.Unlock()
	texts = append(texts, standard.String())
	for _, text := range texts {
		if strings.Contains(text, secret) || strings.Contains(text, "Bearer ") {
			t.Fatalf("the secret or the header reached an output: %q", text)
		}
	}
	// The refusal itself IS recorded, in fixed words.
	foundAuth := false
	for _, o := range obs {
		if o.Kind == a2a.ObsAuth && o.Value == "refused" {
			foundAuth = true
		}
	}
	if !foundAuth {
		t.Fatalf("no auth=refused observation: %+v", obs)
	}
}

// answerMessage is a SendMessage body that continues an existing task.
func answerMessage(taskID, messageID, text string) string {
	return `{"message":{"messageId":"` + messageID + `","taskId":"` + taskID + `","role":"ROLE_USER","parts":[{"text":"` + text + `"}]}}`
}

func errorText(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if message, ok := e["message"].(string); ok {
			return message
		}
	}
	return ""
}

func (f *a2aFixture) dispatches() []a2a.Dispatch {
	f.dispatcher.mu.Lock()
	defer f.dispatcher.mu.Unlock()
	return append([]a2a.Dispatch(nil), f.dispatcher.sent...)
}

func (f *a2aFixture) stateOf(taskID string) string {
	got, _, _ := f.store.A2AStore().GetTask(f.ctx, taskID)
	return got.State
}

// TestA2AAnswerOnTheSameTaskIsAccepted: SendMessage no longer leaves an SDK execution behind, so an answer
// to an INPUT_REQUIRED task (and a second one straight after) is accepted, goes back to SUBMITTED, and is
// dispatched as a continuation to the task's original target. Both target kinds.
func TestA2AAnswerOnTheSameTaskIsAccepted(t *testing.T) {
	t.Run("session target, moved by Report", func(t *testing.T) {
		f := newA2AFixture(t)
		_, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
		taskID := taskIDOf(body)
		if taskID == "" {
			t.Fatalf("no task: %v", body)
		}
		if _, err := f.svc.Accepted(f.ctx, taskID); err != nil {
			t.Fatalf("accepted: %v", err)
		}
		target := a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionB}
		if task, err := f.svc.Report(f.ctx, target, taskID, "input_required", "which format?", ""); err != nil || task.State != a2a.StateInputRequired {
			t.Fatalf("input_required: %+v %v", task, err)
		}

		status, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", answerMessage(taskID, "m2", "format: text"))
		if status != http.StatusOK || errorCode(body) != 0 || taskIDOf(body) != taskID {
			t.Fatalf("the answer was refused: %d %v", status, body)
		}
		if got := f.stateOf(taskID); got != a2a.StateSubmitted {
			t.Fatalf("state after the answer = %s, want SUBMITTED", got)
		}
		sent := f.dispatches()
		if len(sent) != 2 || sent[1].Continuation != true || sent[0].Continuation || sent[1].Text != "format: text" ||
			sent[1].Task.ID != taskID || sent[1].Task.Target.ID != f.sessionB {
			t.Fatalf("dispatched = %+v", sent)
		}

		// A second answer straight after: no lingering lock.
		_, body = f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", answerMessage(taskID, "m3", "and bold"))
		if errorCode(body) != 0 || taskIDOf(body) != taskID {
			t.Fatalf("the second answer was refused: %v", body)
		}
		if sent := f.dispatches(); len(sent) != 3 || !sent[2].Continuation {
			t.Fatalf("dispatched after the second answer = %+v", sent)
		}

		// The history keeps every message once.
		stored, _, _ := f.store.A2AStore().GetTask(f.ctx, taskID)
		decoded, err := a2a.DecodeBody(stored)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]int{}
		for _, m := range decoded.History {
			ids[m.ID]++
		}
		for _, id := range []string{"m1", "m2", "m3"} {
			if ids[id] != 1 {
				t.Fatalf("message %s appears %d times in the history %v", id, ids[id], ids)
			}
		}
	})

	t.Run("service target, moved by ApplyRemote", func(t *testing.T) {
		f := newA2AFixture(t)
		_, body := f.rpc(t, "svc", f.hostCred, f.sessionA, "1.0", "SendMessage", textMessage)
		taskID := taskIDOf(body)
		if taskID == "" {
			t.Fatalf("no task: %v", body)
		}
		changed, err := f.svc.ApplyRemote(f.ctx, taskID, a2a.RemoteTask{ID: "r-1", State: "INPUT_REQUIRED", Text: "which format?"})
		if err != nil || !changed || f.stateOf(taskID) != a2a.StateInputRequired {
			t.Fatalf("input required: changed=%t err=%v state=%s", changed, err, f.stateOf(taskID))
		}

		_, body = f.rpc(t, "svc", f.hostCred, f.sessionA, "1.0", "SendMessage", answerMessage(taskID, "m2", "format: text"))
		if errorCode(body) != 0 || taskIDOf(body) != taskID {
			t.Fatalf("the answer was refused: %v", body)
		}
		if got := f.stateOf(taskID); got != a2a.StateSubmitted {
			t.Fatalf("state after the answer = %s, want SUBMITTED", got)
		}
		sent := f.dispatches()
		if len(sent) != 2 || !sent[1].Continuation || sent[1].Task.Target.Kind != a2a.TargetService || sent[1].Task.Target.ID != sent[0].Task.Target.ID {
			t.Fatalf("dispatched = %+v", sent)
		}
	})
}

// TestA2AAnswerRefusals: a non-party sees not-found, a final task takes no more input, and a wrong
// contextId is a parameter error. None of them dispatches or changes the task.
func TestA2AAnswerRefusals(t *testing.T) {
	f := newA2AFixture(t)
	_, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
	taskID := taskIDOf(body)
	if taskID == "" {
		t.Fatalf("no task: %v", body)
	}

	// A session that is not a party.
	_, got := f.rpc(t, f.sessionB, f.hostCred, f.sessionA, "1.0", "SendMessage", answerMessage(taskID, "m2", "hi"))
	if code := errorCode(got); code != -32001 {
		t.Fatalf("a non-party got code %d, want -32001 (%v)", code, got)
	}
	// An unknown task id looks the same.
	_, got = f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", answerMessage("no-such-task", "m3", "hi"))
	if code := errorCode(got); code != -32001 {
		t.Fatalf("an unknown task got code %d, want -32001 (%v)", code, got)
	}
	// A context id that is not the task's.
	wrong := `{"message":{"messageId":"m4","taskId":"` + taskID + `","contextId":"other","role":"ROLE_USER","parts":[{"text":"hi"}]}}`
	_, got = f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", wrong)
	if code := errorCode(got); code != -32602 {
		t.Fatalf("a wrong contextId got code %d, want -32602 (%v)", code, got)
	}

	// A final task takes no more input.
	target := a2a.Principal{Kind: a2a.KindSession, Host: "vmb", Session: f.sessionB}
	if _, err := f.svc.Accepted(f.ctx, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Report(f.ctx, target, taskID, "completed", "done", ""); err != nil {
		t.Fatalf("completed: %v", err)
	}
	_, got = f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", answerMessage(taskID, "m5", "more"))
	if errorCode(got) == 0 || !strings.Contains(errorText(got), "terminal state") {
		t.Fatalf("an answer on a final task: %v", got)
	}
	if f.stateOf(taskID) != a2a.StateCompleted {
		t.Fatalf("the final task changed to %s", f.stateOf(taskID))
	}
	if sent := f.dispatches(); len(sent) != 1 {
		t.Fatalf("%d dispatches, want only the first", len(sent))
	}
}

// TestA2ASendMessageInterceptorsStillApplyToAnAnswer: the A2A-Version check and the card enforcer run in
// front of the gateway's own SendMessage, for a continuation as for a new task.
func TestA2ASendMessageInterceptorsStillApplyToAnAnswer(t *testing.T) {
	f := newA2AFixture(t)
	_, body := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", textMessage)
	taskID := taskIDOf(body)
	if taskID == "" {
		t.Fatalf("no task: %v", body)
	}
	answer := answerMessage(taskID, "m2", "hi")

	for _, version := range []string{"", "0.3"} {
		_, got := f.rpc(t, f.sessionB, f.ingress, "", version, "SendMessage", answer)
		if code := errorCode(got); code != -32009 {
			t.Fatalf("version %q: code %d, want -32009 (%v)", version, code, got)
		}
	}
	_, got := f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendStreamingMessage", answer)
	if code := errorCode(got); code != -32004 {
		t.Fatalf("streaming: code %d, want -32004 (%v)", code, got)
	}
	if sent := f.dispatches(); len(sent) != 1 {
		t.Fatalf("%d dispatches, want only the first", len(sent))
	}

	// A blocking call (no returnImmediately) returns the task at once, in its current state (contract 14.2).
	_, got = f.rpc(t, f.sessionB, f.ingress, "", "1.0", "SendMessage", answerMessage(taskID, "m3", "ok"))
	if errorCode(got) != 0 || taskIDOf(got) != taskID {
		t.Fatalf("a blocking answer: %v", got)
	}
}
