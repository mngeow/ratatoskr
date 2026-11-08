package link

// Written, not run (GR-3).

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeTaskReadsWrappedAndBareResults(t *testing.T) {
	task := `{"id":"t1","contextId":"c","status":{"state":"TASK_STATE_INPUT_REQUIRED","message":{"parts":[{"text":"which format?"}]}},
	  "artifacts":[{"parts":[{"text":"lines: 3"}]}],
	  "metadata":{"ratatoskr.observations":{"delivery":"accepted","participant_connection":"lost at 2026-11-08T02:00:00Z","reconciled":"by a report"}}}`
	for name, raw := range map[string]string{"bare": task, "wrapped": `{"task":` + task + `}`} {
		view, err := decodeTask(json.RawMessage(raw))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if view.ID != "t1" || view.State != "INPUT_REQUIRED" || view.Text != "which format?" || view.Result != "lines: 3" ||
			view.Delivery != "accepted" || !strings.HasPrefix(view.Connection, "lost at") || view.Reconciled != "by a report" {
			t.Fatalf("%s: view = %+v", name, view)
		}
	}
	for _, bad := range []string{`{}`, `{"task":{}}`, `[]`, `"x"`, ``} {
		if _, err := decodeTask(json.RawMessage(bad)); err == nil {
			t.Fatalf("%q decoded as a task", bad)
		}
	}
}

func TestDecodeTaskBoundsPeerText(t *testing.T) {
	long := strings.Repeat("é", 10000)
	raw := `{"id":"t","status":{"state":"TASK_STATE_COMPLETED","message":{"parts":[{"text":"` + long + `"}]}}}`
	view, err := decodeTask(json.RawMessage(raw))
	if err != nil || len(view.Text) > maxTaskText {
		t.Fatalf("text not bounded: %d bytes, %v", len(view.Text), err)
	}
	if strings.ToValidUTF8(view.Text, "") != view.Text {
		t.Fatal("the bound split a UTF-8 sequence")
	}
}

func TestTaskErrorCarriesCodeAndBoundedMessage(t *testing.T) {
	err := &TaskError{Code: -32001, Message: "task not found"}
	if err.TaskCode() != -32001 || err.TaskMessage() != "task not found" || !strings.Contains(err.Error(), "-32001") {
		t.Fatalf("error = %v", err)
	}
	if boundText(strings.Repeat("a", 500), 200) != strings.Repeat("a", 200) {
		t.Fatal("boundText did not cut")
	}
}

func TestNewTaskMessageIDIsDistinct(t *testing.T) {
	a, b := newTaskMessageID(), newTaskMessageID()
	if a == b || !strings.HasPrefix(a, "m-") || len(a) < 20 {
		t.Fatalf("ids %q %q", a, b)
	}
}
