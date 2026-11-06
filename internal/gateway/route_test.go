package gateway

// Written under GR-3 and never run. Behaviour asserted here is unverified.

import (
	"strings"
	"testing"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

func TestParseAddress(t *testing.T) {
	tests := []struct {
		address string
		name    string
		host    string
		ok      bool
	}{
		{address: "claude@alpha", name: "claude", host: "alpha", ok: true},
		{address: "codex@ubuntu-b", name: "codex", host: "ubuntu-b", ok: true},
		{address: "a@b", name: "a", host: "b", ok: true},
		{address: "", ok: false},
		{address: "claude", ok: false},
		{address: "@alpha", ok: false},
		{address: "claude@", ok: false},
		{address: "claude@alpha@beta", ok: false},
		{address: "Claude@alpha", ok: false},
		{address: "claude@Alpha", ok: false},
		{address: "cla ude@alpha", ok: false},
		{address: "claude@alpha ", ok: false},
		{address: strings.Repeat("a", 33) + "@alpha", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.address, func(t *testing.T) {
			name, host, ok := parseAddress(tc.address)
			if ok != tc.ok {
				t.Fatalf("parseAddress(%q) ok = %v, want %v", tc.address, ok, tc.ok)
			}
			if ok && (name != tc.name || host != tc.host) {
				t.Fatalf("parseAddress(%q) = %q@%q, want %q@%q", tc.address, name, host, tc.name, tc.host)
			}
		})
	}
}

func TestReportRank(t *testing.T) {
	tests := []struct {
		state protocol.MessageState
		rank  int
	}{
		{state: protocol.MessageQueued, rank: 1},
		{state: protocol.MessageHeld, rank: 2},
		{state: protocol.MessageDelivering, rank: 3},
		{state: protocol.MessagePasted, rank: 4},
		{state: protocol.MessageUnconfirmed, rank: 5},
		{state: protocol.MessageAccepted, rank: 6},
		{state: protocol.MessageFailed, rank: 6},
		{state: protocol.MessageInterrupted, rank: 6},
		{state: "", rank: 0},
		{state: "nonsense", rank: 0},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			if got := reportRank(tc.state); got != tc.rank {
				t.Fatalf("reportRank(%q) = %d, want %d", tc.state, got, tc.rank)
			}
		})
	}
}

func TestCleanReason(t *testing.T) {
	long := strings.Repeat("é", reasonMaxRunes+50)

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain", in: "no acknowledgement in time", want: "no acknowledgement in time"},
		{name: "newlines and tabs", in: "line one\nsecond\tline", want: "line one second line"},
		{name: "escape dropped", in: "bad \x1b[31mreason", want: "bad [31mreason"},
		{name: "runs collapse", in: "  too   many    spaces  ", want: "too many spaces"},
		{name: "trimmed", in: "\n\n reason \n", want: "reason"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanReason(tc.in); got != tc.want {
				t.Fatalf("cleanReason(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	// A long reason is cut at the rune limit, never mid-character.
	cut := cleanReason(long)
	if runes := []rune(cut); len(runes) > reasonMaxRunes {
		t.Fatalf("cleaned reason has %d runes, want at most %d", len(runes), reasonMaxRunes)
	}
	if !strings.HasPrefix(cut, "é") {
		t.Fatalf("cleaned reason lost its first character: %q", cut)
	}
}

// TestMessageInfoForStored: an undecided message reads as uncertain, which is the
// only honest word for it.
func TestMessageInfoForStored(t *testing.T) {
	info := MessageInfoFor(MessageRow{
		ID:           "m1",
		Result:       resultStored,
		ResultReason: "",
		Report:       string(protocol.MessageHeld),
		ReportReason: "busy",
	})
	if info.Result != protocol.SendGatewayUncertain {
		t.Fatalf("result = %q, want %q", info.Result, protocol.SendGatewayUncertain)
	}
	if info.Report != protocol.MessageHeld || info.ReportReason != "busy" {
		t.Fatalf("report = %q / %q", info.Report, info.ReportReason)
	}

	decided := MessageInfoFor(MessageRow{ID: "m2", Result: string(protocol.SendHostStored), ResultReason: "saved"})
	if decided.Result != protocol.SendHostStored || decided.ResultReason != "saved" {
		t.Fatalf("decided message = %+v", decided)
	}
}

// TestHubExpectAck covers the wake-up registry, including the identity check that
// stops a finished handler deleting its replacement.
func TestHubExpectAck(t *testing.T) {
	h := newHub()

	// Waking an id nobody waits for is a no-op.
	h.wake("m1")

	firstWake, firstForget := h.expectAck("m1")
	h.wake("m1")

	select {
	case <-firstWake:
	default:
		t.Fatal("wake did not signal the waiter")
	}

	// A second waiter replaces the first.
	secondWake, _ := h.expectAck("m1")
	firstForget()
	h.wake("m1")

	select {
	case <-secondWake:
	default:
		t.Fatal("the first forget removed the replacement registration")
	}

	// forget is idempotent.
	firstForget()
	firstForget()
}
