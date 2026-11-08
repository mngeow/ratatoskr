// Command a2a-reference is a tiny, model-free A2A v1.0 server.
//
// It exists so an owner can run a real A2A endpoint and be messaged by the Ratatoskr gateway without a
// second model: it asks one clarifying question (INPUT_REQUIRED) and then counts lines, words and
// characters. It is not part of Ratatoskr, imports nothing from it, and knows nothing about it.
//
// The gateway sends Authorization: Bearer <secret> on every call to a registered service, so this
// adapter checks it and refuses anything else before the request reaches the A2A handler. The secret
// never appears on the command line: it comes from a file re-read on every request, or from
// A2A_GATEWAY_TOKEN read once at start.
//
// UNTESTED. Under GR-3 nothing here has been run: no server, no socket, no request. Only gofmt, go vet
// and go build have been applied.
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// defaultListen is loopback on purpose: the secret is the only thing between this adapter and any
// caller on its network, so it must not be reachable by accident.
const defaultListen = "127.0.0.1:7790"

const (
	// tokenFileLimit bounds the token file. A secret is a few dozen bytes; anything near this is not one.
	tokenFileLimit = 4 << 10

	// gatewayTokenVariable is the environment source for the secret, used only when no file is given.
	gatewayTokenVariable = "A2A_GATEWAY_TOKEN"

	// demoBanner is printed once, verbatim, when --no-auth is used.
	demoBanner = "DEMO MODE: --no-auth is set. This adapter accepts any caller and the Ratatoskr gateway will only register it with --no-auth. Do not use this outside a demonstration."

	// refusalLogText is the whole log line for a refused request: no token, no header, no path.
	refusalLogText = "refused a request without a valid bearer credential"

	// refusalInterval is how rarely a refusal may be logged, so a flood of bad requests cannot flood
	// the log.
	refusalInterval = time.Second
)

// The fixed sentences this program reports. None of them carries a token, a header or a path.
var (
	errNoToken        = fmt.Errorf("no gateway token: set %s or --gateway-token-file, or pass --no-auth for a demonstration", gatewayTokenVariable)
	errNoAuthWithAuth = errors.New("--no-auth cannot be combined with a token source")
	errTokenFileRead  = errors.New("the token file cannot be read")
	errTokenFileKind  = errors.New("the token file is not a regular file")
	errTokenFileLarge = errors.New("the token file is larger than 4 KiB")
	errTokenFileEmpty = errors.New("the token file is empty")
)

func main() {
	listen := flag.String("listen", defaultListen, "address to listen on (loopback by default; any other address must be set deliberately)")
	publicURL := flag.String("url", "", "the public URL written into the agent card (default http://<listen>/)")
	tokenFile := flag.String("gateway-token-file", "", "path to a file holding the gateway's bearer secret; it is re-read on every request")
	noAuth := flag.Bool("no-auth", false, "DEMO ONLY: check no credential and declare no security scheme")
	flag.Parse()

	if strings.TrimSpace(*listen) == "" {
		fmt.Fprintln(os.Stderr, "a2a-reference: --listen needs an address")
		os.Exit(2)
	}

	// Everything about the credential is settled before anything listens: a refused start leaves no
	// socket behind.
	source, err := resolveTokenSource(*tokenFile, os.Getenv(gatewayTokenVariable), *noAuth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "a2a-reference: %v\n", err)
		os.Exit(2)
	}
	if source.auth() {
		// Which source is used, never what it holds.
		log.Printf("a2a-reference: gateway token source: %s", source.name)
	} else {
		fmt.Fprintln(os.Stderr, demoBanner)
	}

	card := agentCard(*publicURL, *listen, source.auth())

	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("a2a-reference: cannot listen on %s: %v", *listen, err)
	}
	log.Printf("a2a-reference: listening on %s", *listen)
	log.Printf("a2a-reference: agent card at %s%s", cardURL(*publicURL, *listen), a2asrv.WellKnownAgentCardPath)

	// The SDK serves streaming and push unless told otherwise: the enforcer makes the handler refuse
	// what the card says is unsupported (UnsupportedOperationError, PushNotificationNotSupportedError)
	// and any non-text input mode (ContentTypeNotSupportedError).
	enforcer, err := a2asrv.NewAgentCardEnforcer(card)
	if err != nil {
		log.Fatalf("a2a-reference: agent card: %v", err)
	}
	requestHandler := a2asrv.NewHandler(&countExecutor{}, a2asrv.WithCallInterceptors(enforcer))
	rpc := a2asrv.NewJSONRPCHandler(requestHandler)

	mux := http.NewServeMux()
	// The card is public and always served, even while the token file still holds a placeholder: a
	// caller must be able to read the card to learn that a secret is required. Only the RPC route is
	// gated.
	if source.auth() {
		mux.Handle("/", requireBearer(rpc, source.token))
	} else {
		mux.Handle("/", rpc)
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))

	if err := http.Serve(listener, mux); err != nil {
		log.Fatalf("a2a-reference: server stopped: %v", err)
	}
}

// tokenSource is where the expected secret comes from.
//
// A file source is remembered as a path and read again on every request, so the owner can paste the
// secret printed by `rtk gateway participant register` and the next request already uses it. An
// environment source is read once at start; changing it later needs a restart.
type tokenSource struct {
	name  string
	file  string
	value string
}

// auth reports whether requests must carry a credential.
func (s tokenSource) auth() bool { return s.name != "" }

// token returns the expected secret, reading the file again when that is the source.
func (s tokenSource) token() (string, error) {
	if s.file != "" {
		return readTokenFile(s.file)
	}
	if s.value == "" {
		return "", errNoToken
	}
	return s.value, nil
}

// resolveTokenSource picks the credential source and checks that it starts.
//
// The file wins over the environment variable when both are set. With --no-auth there must be no source
// at all: naming both would leave it unclear which one was meant, so it is refused before anything is
// read.
func resolveTokenSource(filePath, envValue string, noAuth bool) (tokenSource, error) {
	filePath = strings.TrimSpace(filePath)
	envValue = strings.TrimSpace(envValue)

	if noAuth {
		if filePath != "" || envValue != "" {
			return tokenSource{}, errNoAuthWithAuth
		}
		return tokenSource{}, nil
	}

	if filePath != "" {
		// Checked now as well as on every request: a start with no usable secret is refused rather than
		// left listening and failing every call.
		if _, err := readTokenFile(filePath); err != nil {
			return tokenSource{}, err
		}
		return tokenSource{name: "file", file: filePath}, nil
	}

	if envValue != "" {
		return tokenSource{name: "environment", value: envValue}, nil
	}

	return tokenSource{}, errNoToken
}

// readTokenFile reads and trims the secret.
//
// The read is bounded (a byte past the limit is enough to know it is too large) and only a regular file
// is accepted: a device or a pipe is not a secret somebody pasted. The file is checked before opening so
// a pipe cannot block the read.
func readTokenFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", errTokenFileRead
	}
	if !info.Mode().IsRegular() {
		return "", errTokenFileKind
	}

	file, err := os.Open(path)
	if err != nil {
		return "", errTokenFileRead
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("a2a-reference: closing the token file: %v", err)
		}
	}()

	content, err := io.ReadAll(io.LimitReader(file, tokenFileLimit+1))
	if err != nil {
		return "", errTokenFileRead
	}
	if len(content) > tokenFileLimit {
		return "", errTokenFileLarge
	}

	token := strings.TrimSpace(string(content))
	if token == "" {
		return "", errTokenFileEmpty
	}
	return token, nil
}

// checkBearer reports whether a presented Authorization header carries the expected secret.
//
// Both values are hashed first and the digests are compared in constant time, so the comparison is a
// fixed 32 bytes whatever the header holds: nothing about its length, and no early exit at the first
// differing byte, can be observed.
func checkBearer(header, token string) bool {
	if token == "" {
		return false
	}
	presented := sha256.Sum256([]byte(header))
	expected := sha256.Sum256([]byte("Bearer " + token))
	return subtle.ConstantTimeCompare(presented[:], expected[:]) == 1
}

// bearerGate refuses an unauthenticated RPC request before it reaches the handler.
type bearerGate struct {
	next  http.Handler
	token func() (string, error)

	// now is the clock; a test replaces it rather than sleeping.
	now func() time.Time

	mu          sync.Mutex
	lastRefusal time.Time
}

// requireBearer gates next: a POST must carry the credential, everything else is passed through and let
// the A2A handler answer for itself.
func requireBearer(next http.Handler, token func() (string, error)) http.Handler {
	return &bearerGate{next: next, token: token, now: time.Now}
}

// ServeHTTP checks the credential on POST and answers 401 otherwise.
func (g *bearerGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only the RPC call carries a credential. A GET of the card is handled by its own route, and a GET
	// here is the A2A handler's to refuse.
	if r.Method != http.MethodPost {
		g.next.ServeHTTP(w, r)
		return
	}

	expected, err := g.token()
	// A source that cannot be read fails closed: the request is refused exactly as a wrong secret is,
	// never answered with a 500 that would tell a caller something about the configuration.
	if err != nil || !checkBearer(r.Header.Get("Authorization"), expected) {
		g.logRefusal()
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	g.next.ServeHTTP(w, r)
}

// logRefusal notes a refused request at most once per refusalInterval.
func (g *bearerGate) logRefusal() {
	now := g.now()

	g.mu.Lock()
	if !g.lastRefusal.IsZero() && now.Sub(g.lastRefusal) < refusalInterval {
		g.mu.Unlock()
		return
	}
	g.lastRefusal = now
	g.mu.Unlock()

	log.Printf("a2a-reference: %s", refusalLogText)
}

// agentCard is the card this adapter publishes.
//
// It declares only what is true: no streaming, no push, no extended card, one JSON-RPC interface, text
// modes, and one skill that counts text. When a credential is required the card says so the standard
// way, with the scheme and the requirement naming it.
func agentCard(publicURL, listen string, auth bool) *a2a.AgentCard {
	card := &a2a.AgentCard{
		Name:        "ratatoskr-reference",
		Description: "Counts the lines, words and characters of a text you send; asks which format you want first.",
		Version:     "1",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cardURL(publicURL, listen), a2a.TransportProtocolJSONRPC),
		},
		Capabilities:       a2a.AgentCapabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{
			{
				ID:          "count",
				Name:        "Count text",
				Description: "Counts lines, words and characters, as JSON or as plain text.",
				Tags:        []string{"reference", "text"},
			},
		},
	}

	if auth {
		// Value types, not pointers: NamedSecuritySchemes.MarshalJSON type-switches on the value types,
		// so a pointer would fail to marshal the card at all.
		card.SecuritySchemes = a2a.NamedSecuritySchemes{
			"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"},
		}
		card.SecurityRequirements = a2a.SecurityRequirementsOptions{
			a2a.SecurityRequirements{"bearer": a2a.SecuritySchemeScopes{}},
		}
	}

	return card
}

// cardURL is the URL written into the card: --url when given, otherwise http://<listen>/.
//
// A wildcard listen address cannot be dialled, so the default is wrong behind one; the README says to
// set --url in that case.
func cardURL(publicURL, listen string) string {
	if strings.TrimSpace(publicURL) != "" {
		return publicURL
	}
	return "http://" + listen + "/"
}
