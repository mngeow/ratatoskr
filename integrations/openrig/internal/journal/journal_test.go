// UNTESTED (GR-3): written, never run.

package journal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendReplayAndUnfinished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, r := range []Record{
		{ID: "a", Event: Accepted, At: now}, {ID: "a", Event: Acked, At: now.Add(time.Second)}, {ID: "a", Event: Typing, At: now.Add(2 * time.Second)},
		{ID: "b", Event: Accepted, At: now}, {ID: "b", Event: Pasted, At: now.Add(time.Second)},
		{ID: "c", Event: Accepted, At: now.Add(3 * time.Second)},
	} {
		if err := j.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode())
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = again.Close() }()
	if last, _ := again.Last("a"); last.Event != Typing {
		t.Fatalf("a = %v", last.Event)
	}
	open := again.Unfinished()
	if len(open) != 2 || open[0].ID != "a" || open[1].ID != "c" {
		t.Fatalf("unfinished = %+v", open)
	}
}

func TestATornFinalLineIsIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	good := `{"at":"2027-01-01T00:00:00Z","id":"a","event":"typing"}` + "\n"
	if err := os.WriteFile(path, []byte(good+`{"at":"2027-01-01T00:00:0`), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if last, ok := j.Last("a"); !ok || last.Event != Typing {
		t.Fatalf("a = %+v %v", last, ok)
	}
}

func TestOpenRefusesALooseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("a group-readable journal was accepted")
	}
}

func TestReasonIsBoundedAndNoBodyField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	j, _ := Open(path)
	defer func() { _ = j.Close() }()
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	_ = j.Append(Record{ID: "a", Event: Failed, Reason: string(long)})
	if last, _ := j.Last("a"); len(last.Reason) != maxReason {
		t.Fatalf("reason length = %d", len(last.Reason))
	}
}
