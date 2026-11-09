// Package journal is the sidecar's append-only record of what it did with each offer
// (docs/implementation/openrig-bridge-contract.md sections 8.3 and 8.4).
//
// One JSON object per line, fsync after every record. It never holds a message body or a credential: only the
// message id, the sender address, an event word and a short reason. The restart rule lives in the sidecar, which
// reads Unfinished; the journal only remembers the last event of every id.
//
// UNTESTED. Under GR-3 nothing here has been run.
package journal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

// Event is what happened to an offer, in the order it can happen.
type Event string

const (
	Accepted    Event = "accepted"   // journalled; the acknowledgement has not been confirmed yet
	Acked       Event = "acked"      // acknowledged saved:true with a 200
	AckFailed   Event = "ack_failed" // the acknowledgement was not confirmed; nothing is typed
	Expired     Event = "expired"    // acknowledged, but the typing window had passed or the bridge restarted
	Typing      Event = "typing"     // rig send is about to run or is running
	Pasted      Event = "pasted"
	Failed      Event = "failed"
	Unconfirmed Event = "unconfirmed"
	Refused     Event = "refused" // acknowledged saved:false
)

const (
	// rotateBytes is the size at which Open moves the journal aside. One previous file is kept.
	rotateBytes = 1 << 20
	// maxLine bounds one journal line when it is read back.
	maxLine = 16 * 1024
	// maxReason bounds the reason stored with a record.
	maxReason = 200
)

// Record is one journal line.
type Record struct {
	At     time.Time `json:"at"`
	ID     string    `json:"id"`
	Event  Event     `json:"event"`
	From   string    `json:"from,omitempty"`
	Reason string    `json:"reason,omitempty"`
}

// Journal is an open journal. It is safe for concurrent use.
type Journal struct {
	mu   sync.Mutex
	file *os.File
	last map[string]Record
}

// Open opens (creating, mode 0600) the journal at path, replays it and its one rotated predecessor, and
// rotates when the file has grown past a bound. A final line that was torn by a crash is ignored.
func Open(path string) (*Journal, error) {
	j := &Journal{last: make(map[string]Record)}
	for _, p := range []string{path + ".1", path} {
		if err := j.replay(p); err != nil {
			return nil, err
		}
	}
	if info, err := os.Lstat(path); err == nil && info.Size() > rotateBytes {
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, fmt.Errorf("journal: cannot rotate: %w", err)
		}
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("journal: cannot open: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, errors.New("journal: the file is not a regular file with mode 0600")
	}
	j.file = file
	return j, nil
}

func (j *Journal) replay(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("journal: %s is not a regular file", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("journal: cannot read: %w", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 4096), maxLine)
	for scanner.Scan() {
		var r Record
		if json.Unmarshal(scanner.Bytes(), &r) != nil || r.ID == "" || r.Event == "" {
			continue
		}
		j.last[r.ID] = r
	}
	return nil
}

// Append writes one record and syncs it to disk before returning.
func (j *Journal) Append(r Record) error {
	if r.At.IsZero() {
		r.At = time.Now().UTC()
	}
	if len(r.Reason) > maxReason {
		r.Reason = r.Reason[:maxReason]
	}
	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("journal: encode: %w", err)
	}
	line = append(line, '\n')

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return errors.New("journal: closed")
	}
	if _, err := j.file.Write(line); err != nil {
		return fmt.Errorf("journal: write: %w", err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("journal: sync: %w", err)
	}
	j.last[r.ID] = r
	return nil
}

// Last returns the last record of an id.
func (j *Journal) Last(id string) (Record, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	r, ok := j.last[id]
	return r, ok
}

// Unfinished returns, oldest first, the records whose last event leaves the offer in a state the restart rule
// must settle: accepted, acked or typing.
func (j *Journal) Unfinished() []Record {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []Record
	for _, r := range j.last {
		switch r.Event {
		case Accepted, Acked, Typing:
			out = append(out, r)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].At.Before(out[b].At) })
	return out
}

// Close closes the file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}
