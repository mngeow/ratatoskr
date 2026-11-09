// UNTESTED (GR-3): written, never run.

package rigcli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	res  Result
	err  error
	args []string
	env  []string
}

func (f *fakeRunner) Run(_ context.Context, _ string, args []string, env []string) (Result, error) {
	f.args, f.env = args, env
	return f.res, f.err
}

func newRig(f *fakeRunner) *Rig {
	return &Rig{Path: "/x/rig", EnvPath: "/x:/usr/bin", Home: "/h", OpenRigHome: "/oh", Seat: "operator-agent@kernel",
		ReadyTimeout: time.Second, Runner: f}
}

func TestEnvIsBuiltFromScratch(t *testing.T) {
	t.Setenv("OPENRIG_SESSION_NAME", "evil@seat")
	t.Setenv("OPENRIG_URL", "http://elsewhere")
	env := strings.Join(newRig(&fakeRunner{}).Env(), "\n")
	for _, bad := range []string{"OPENRIG_SESSION_NAME", "elsewhere"} {
		if strings.Contains(env, bad) {
			t.Fatalf("the child environment carries %q: %s", bad, env)
		}
	}
	for _, want := range []string{"PATH=/x:/usr/bin", "HOME=/h", "OPENRIG_HOME=/oh"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %q in %s", want, env)
		}
	}
}

func TestReadyTable(t *testing.T) {
	entry := func(name, session, startup string, stopped bool) string {
		s := `{"canonicalSessionName":"` + name + `","sessionStatus":"` + session + `","startupStatus":"` + startup + `","intentionallyStopped":`
		if stopped {
			return s + "true}"
		}
		return s + "false}"
	}
	tests := []struct {
		name  string
		res   Result
		err   error
		ready bool
		word  string
	}{
		{"ready", Result{Stdout: []byte(`{"entries":[` + entry("operator-agent@kernel", "running", "ready", false) + `],"totalNodes":1,"truncated":false}`)}, nil, true, ""},
		{"other seats only", Result{Stdout: []byte(`{"entries":[` + entry("other@kernel", "running", "ready", false) + `]}`)}, nil, false, WordNotFound},
		{"not running", Result{Stdout: []byte(`{"entries":[` + entry("operator-agent@kernel", "stopped", "ready", false) + `]}`)}, nil, false, WordNotRunning},
		{"not started", Result{Stdout: []byte(`{"entries":[` + entry("operator-agent@kernel", "running", "pending", false) + `]}`)}, nil, false, WordNotStarted},
		{"intentionally stopped", Result{Stdout: []byte(`{"entries":[` + entry("operator-agent@kernel", "running", "ready", true) + `]}`)}, nil, false, WordStopped},
		{"truncated is unknown", Result{Stdout: []byte(`{"entries":[` + entry("operator-agent@kernel", "running", "ready", false) + `],"truncated":true}`)}, nil, false, WordCheckFailed},
		{"bare array is not the shape", Result{Stdout: []byte(`[` + entry("operator-agent@kernel", "running", "ready", false) + `]`)}, nil, false, WordCheckFailed},
		{"non-zero exit", Result{ExitCode: 1}, nil, false, WordCheckFailed},
		{"timeout", Result{TimedOut: true, ExitCode: -1}, nil, false, WordTimedOut},
		{"cannot start", Result{}, &StartError{Err: errors.New("no such file")}, false, WordCheckFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{res: tc.res, err: tc.err}
			ready, word := newRig(f).Ready(context.Background())
			if ready != tc.ready || word != tc.word {
				t.Fatalf("Ready = %v %q, want %v %q", ready, word, tc.ready, tc.word)
			}
			if got := strings.Join(f.args, " "); !strings.Contains(got, "--rig kernel") || strings.Contains(got, "--urgent") {
				t.Fatalf("args = %s", got)
			}
		})
	}
}

func TestSendOutcomes(t *testing.T) {
	tests := []struct {
		name string
		res  Result
		err  error
		want SendOutcome
	}{
		{"exit 0", Result{}, nil, Pasted},
		{"exit 1", Result{ExitCode: 1, Stderr: []byte("nope\nsecond")}, nil, Unconfirmed},
		{"signal", Result{ExitCode: -1}, nil, Unconfirmed},
		{"timeout", Result{TimedOut: true, ExitCode: -1}, nil, Unconfirmed},
		{"cannot start", Result{}, &StartError{Err: errors.New("x")}, Failed},
		{"other error", Result{}, errors.New("x"), Unconfirmed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{res: tc.res, err: tc.err}
			got := newRig(f).Send(context.Background(), "Ratatoskr message m1")
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %v, want %v", got.Outcome, tc.want)
			}
			if len(f.args) != 3 || f.args[0] != "send" || f.args[1] != "operator-agent@kernel" {
				t.Fatalf("args = %q", f.args)
			}
		})
	}
	if got := newRig(&fakeRunner{}).Send(context.Background(), "-rf"); got.Outcome != Failed {
		t.Fatalf("text starting with '-' ran: %v", got)
	}
}
