// UNTESTED (GR-3): written, never run.

package gw

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func tlsClient(t *testing.T, handler http.HandlerFunc, pinOf func(*httptest.Server) [32]byte, credential string) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	c := New(Options{Scheme: "https", Host: strings.TrimPrefix(server.URL, "https://"), Pin: pinOf(server), Credential: credential})
	t.Cleanup(c.Close)
	return c, server
}

func goodPin(s *httptest.Server) [32]byte { return sha256.Sum256(s.Certificate().Raw) }
func wrongPin(*httptest.Server) [32]byte  { return [32]byte{1} }

func TestPinMismatchSendsNoHTTP(t *testing.T) {
	hit := false
	c, _ := tlsClient(t, func(http.ResponseWriter, *http.Request) { hit = true }, wrongPin, "SECRET-CRED")
	_, e := c.Send(context.Background(), SendRequest{ID: "m1", From: "operator", To: "a@b", Body: "x"})
	if e == nil || e.Kind != KindPin || e.Wrote {
		t.Fatalf("error = %+v", e)
	}
	if hit {
		t.Fatal("a request reached the server despite the pin mismatch")
	}
	if strings.Contains(e.Msg, "SECRET-CRED") {
		t.Fatal("the error carries the credential")
	}
}

func TestDiscoverNeedsHeaderAndMajor(t *testing.T) {
	serve := func(header string, versions string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if header != "" {
				w.Header().Set(ContractHeader, header)
			}
			_, _ = w.Write([]byte(`{"contract":"1.0","versions":` + versions + `}`))
		}
	}
	c, _ := tlsClient(t, serve("1.0", `["1"]`), goodPin, "")
	if _, e := c.Discover(context.Background()); e != nil {
		t.Fatalf("good discovery: %+v", e)
	}
	c, _ = tlsClient(t, serve("", `["1"]`), goodPin, "")
	if _, e := c.Discover(context.Background()); e == nil {
		t.Fatal("a discovery without the contract header was accepted")
	}
	c, _ = tlsClient(t, serve("1.0", `["2"]`), goodPin, "")
	if _, e := c.Discover(context.Background()); e == nil {
		t.Fatal("a discovery without major 1 was accepted")
	}
}

func TestRequestsCarryTheContractHeaderAndBearer(t *testing.T) {
	var gotContract, gotAuth string
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotContract, gotAuth = r.Header.Get(ContractHeader), r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"offers":[],"replaced":false}`))
	}, goodPin, "CRED")
	if _, e := c.Poll(context.Background()); e != nil {
		t.Fatalf("%+v", e)
	}
	if gotContract != Contract || gotAuth != "Bearer CRED" {
		t.Fatalf("headers = %q %q", gotContract, gotAuth)
	}
}

func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		status int
		kind   Kind
	}{{401, KindRevoked}, {400, KindRefused}, {404, KindRefused}, {500, KindServer}, {302, KindProtocol}} {
		c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://elsewhere/")
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(`{"code":"bad_request","message":"CRED-in-message"}`))
		}, goodPin, "CRED")
		_, e := c.Send(context.Background(), SendRequest{ID: "m1", From: "operator", To: "a@b", Body: "x"})
		if e == nil || e.Kind != tc.kind || !e.Wrote {
			t.Fatalf("status %d: %+v", tc.status, e)
		}
		if strings.Contains(e.Msg, "CRED") {
			t.Fatalf("status %d: the error repeats the server's message: %s", tc.status, e.Msg)
		}
	}
}

func TestSendAnswerMustBeForThisMessage(t *testing.T) {
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"other","result":"host_stored"}`))
	}, goodPin, "CRED")
	if _, e := c.Send(context.Background(), SendRequest{ID: "m1", From: "operator", To: "a@b", Body: "x"}); e == nil || e.Kind != KindProtocol {
		t.Fatalf("error = %+v", e)
	}
}

func TestPairSendsNoCredentialAndRejectsAnotherName(t *testing.T) {
	var auth string
	c, _ := tlsClient(t, func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"participant":"someone-else","credential":"C"}`))
	}, goodPin, "")
	if _, e := c.Pair(context.Background(), "CODE", "openrig-main"); e == nil {
		t.Fatal("a pairing answer for another participant was accepted")
	}
	if auth != "" {
		t.Fatalf("pair sent Authorization %q", auth)
	}
}
