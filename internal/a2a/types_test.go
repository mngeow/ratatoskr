package a2a

// Written, not run (GR-3). Pure tests: no database, no network.

import (
	"errors"
	"testing"
)

func TestPrincipalKeyRoundTrip(t *testing.T) {
	for _, p := range []Principal{
		{Kind: KindSession, Host: "vmb", Session: "0123456789abcdef0123456789abcdef"},
		{Kind: KindParticipant, Name: "reference"},
	} {
		if got := ParsePrincipalKey(p.Key()); got != p {
			t.Fatalf("round trip of %+v gave %+v", p, got)
		}
	}
	for _, bad := range []string{"", "nonsense", "session:nohost", "other:x"} {
		if ParsePrincipalKey(bad).Valid() {
			t.Fatalf("%q parsed as a valid principal", bad)
		}
	}
}

func TestOriginIsTheAuthenticatedPrincipalOnly(t *testing.T) {
	if got := (Principal{Kind: KindSession, Host: "vmb", Session: "s1"}).Origin(); got != "s1@vmb" {
		t.Fatalf("session origin = %q", got)
	}
	if got := (Principal{Kind: KindParticipant, Name: "svc"}).Origin(); got != "svc" {
		t.Fatalf("participant origin = %q", got)
	}
}

func TestValidParticipantName(t *testing.T) {
	good := []string{"ref", "reference-adapter", "a1_b2", "x"}
	bad := []string{"", "Ref", "1abc", "-a", "has space", "a.b", "session1", "0123456789abcdef0123456789abcdef", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	for _, name := range good {
		if !ValidParticipantName(name) {
			t.Errorf("%q should be valid", name)
		}
	}
	for _, name := range bad {
		if ValidParticipantName(name) {
			t.Errorf("%q should be invalid", name)
		}
	}
}

func TestFinalStates(t *testing.T) {
	for state, want := range map[string]bool{
		StateSubmitted: false, StateWorking: false, StateInputRequired: false,
		StateCompleted: true, StateFailed: true, StateRejected: true, "CANCELED": false, "": false,
	} {
		if Final(state) != want {
			t.Errorf("Final(%q) = %t, want %t", state, !want, want)
		}
	}
}

func TestStateNamesMapToTheSDKStrings(t *testing.T) {
	if a2aState(StateInputRequired) != "TASK_STATE_INPUT_REQUIRED" || stateName("TASK_STATE_COMPLETED") != StateCompleted {
		t.Fatal("state name mapping is wrong")
	}
}

func TestCheckDestination(t *testing.T) {
	pin := "sha256:" + "ab"
	tests := []struct {
		name         string
		url, pin     string
		insecureHTTP bool
		want         error
	}{
		{"loopback http", "http://127.0.0.1:7790/", "", false, nil},
		{"tailnet http", "http://100.64.0.5:7790/", "", false, nil},
		{"lan http needs the opt-in", "http://192.168.1.9:7790/", "", false, ErrNeedsOptIn},
		{"lan http with the opt-in", "http://192.168.1.9:7790/", "", true, nil},
		{"lan https with a pin", "https://192.168.1.9:7790/", pin, false, nil},
		{"lan https without a pin", "https://192.168.1.9:7790/", "", false, ErrBadURL},
		{"loopback https is not offered (https is for a LAN address)", "https://127.0.0.1:7790/", pin, false, ErrBadURL},
		{"tailnet https is not offered", "https://100.64.0.5:7790/", pin, false, ErrBadURL},
		{"tailnet v6 http needs no opt-in", "http://[fd7a:115c:a1e0::5]:7790/", "", false, nil},
		{"tailnet v6 https is not offered", "https://[fd7a:115c:a1e0::5]:7790/", pin, false, ErrBadURL},
		{"loopback v6 http", "http://[::1]:7790/", "", false, nil},
		{"other unique-local v6 is LAN and needs the opt-in", "http://[fd00::5]:7790/", "", false, ErrNeedsOptIn},
		{"other unique-local v6 with the opt-in", "http://[fd00::5]:7790/", "", true, nil},
		{"public address", "http://8.8.8.8:80/", "", true, ErrBadURL},
		{"a name, not an address", "http://example.test:80/", "", true, ErrBadURL},
		{"no port", "http://127.0.0.1/", "", false, ErrBadURL},
		{"credentials in the url", "http://u:p@127.0.0.1:1/", "", false, ErrBadURL},
		{"query", "http://127.0.0.1:1/?x=1", "", false, ErrBadURL},
		{"scheme", "ftp://127.0.0.1:21/", "", false, ErrBadURL},
		{"pin on http", "http://127.0.0.1:1/", pin, false, ErrBadURL},
		{"garbage", "::::", "", false, ErrBadURL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckDestination(tc.url, tc.pin, tc.insecureHTTP)
			if !errors.Is(got, tc.want) && !(got == nil && tc.want == nil) {
				t.Fatalf("CheckDestination(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestBearerParsing(t *testing.T) {
	good := map[string]string{"Bearer abc": "abc"}
	for header, want := range good {
		if got, ok := bearer(header); !ok || got != want {
			t.Fatalf("bearer(%q) = %q, %t", header, got, ok)
		}
	}
	for _, header := range []string{"", "Bearer", "Bearer ", "bearer abc", "Basic abc", "Bearer a b", "Bearer\tabc"} {
		if _, ok := bearer(header); ok {
			t.Fatalf("bearer(%q) accepted", header)
		}
	}
}

func TestCredentialIsHashedNotStoredAndDiffers(t *testing.T) {
	a, err := NewCredential()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewCredential()
	if a == b || len(a) < 40 {
		t.Fatal("credentials are not distinct and long")
	}
	if HashCredential(a) == a || HashCredential(a) != HashCredential(a) || HashCredential(a) == HashCredential(b) {
		t.Fatal("hash is not a stable one-way digest")
	}
}

func TestEndpointCardDeclaresOnlyWhatIsTrue(t *testing.T) {
	card := EndpointCard("http://100.64.0.1:7777", Target{Kind: TargetSession, ID: "abc"}, "codex@ubuntu")
	if card.Capabilities.Streaming || card.Capabilities.PushNotifications || card.Capabilities.ExtendedAgentCard {
		t.Fatal("the card claims a capability that is not built")
	}
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != "http://100.64.0.1:7777/a2a/agents/abc" ||
		string(card.SupportedInterfaces[0].ProtocolVersion) != "1.0" || string(card.SupportedInterfaces[0].ProtocolBinding) != "JSONRPC" {
		t.Fatalf("interface = %+v", card.SupportedInterfaces)
	}
	if len(card.SecuritySchemes) != 1 || len(card.SecurityRequirements) != 1 {
		t.Fatal("the card must declare the bearer scheme the gateway enforces")
	}
}
