package server

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// maxHookStdinBytes caps how much of the harness's hook JSON is forwarded.
	// A hook payload carries the user's prompt, and a long pasted prompt must
	// not be truncated into looking like a missing session id.
	maxHookStdinBytes = 4 << 20 // UNTESTED guess

	// hookPostTimeout bounds the whole report. A hook must not delay a harness
	// for longer than this, whatever the server is doing.
	hookPostTimeout = 2 * time.Second // UNTESTED guess
)

// hookClient is shared so the timeout and connection pooling are set once. It
// never follows a redirect: a redirect is a way to make this client talk to a
// host it did not choose.
var hookClient = &http.Client{
	Timeout: hookPostTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// RunHook reports one harness hook event to the local server and always returns
// nil.
//
// This runs inside a harness's hook process, so it must never influence what the
// harness does next: nothing goes to stdout, no error reaches the caller, and a
// harness question is never answered. At most one short line goes to stderr, and
// it never contains the token.
//
// A URL that is not loopback http is dropped without a request. A hook file is a
// local secret, but the address in it is still not something this command will
// dial blind.
//
// UNTESTED.
func RunHook(args []string, stdin io.Reader) (err error) {
	// A panic must not escape. An uncaught panic exits the process non-zero, and
	// a non-zero exit from a UserPromptSubmit or PermissionRequest hook can block
	// or decide for the harness. Nothing this command does is worth that risk, so
	// a panic is swallowed exactly like a failed report.
	defer func() {
		if recovered := recover(); recovered != nil { //nolint:revive // deliberate catch-all.
			err = nil
		}
	}()

	return runHook(args, stdin)
}

// runHook is the body of RunHook, split out so the panic guard above covers
// every path through it.
func runHook(args []string, stdin io.Reader) error {
	flags := flag.NewFlagSet("rtk hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	agent := flags.String("agent", "", "agent name")
	event := flags.String("event", "", "hook event name")
	sessionDir := flags.String("session-dir", "", "session directory")

	if err := flags.Parse(args); err != nil {
		reportNotSent("")
		return nil
	}
	if !agentNamePattern.MatchString(*agent) || *sessionDir == "" {
		reportNotSent(*event)
		return nil
	}

	// The payload is optional: if the harness sent nothing readable, the event
	// is still reported, without it.
	payload, payloadOK := readHookPayload(stdin)

	serverURL, token, err := readHookFile(*sessionDir, *agent)
	if err != nil {
		reportNotSent(*event)
		return nil
	}

	target, ok := hookTarget(serverURL)
	if !ok {
		reportNotSent(*event)
		return nil
	}

	body, err := hookBody(*event, payload, payloadOK)
	if err != nil {
		reportNotSent(*event)
		return nil
	}

	if err := postHook(target, token, body); err != nil {
		reportNotSent(*event)
	}
	return nil
}

// readHookPayload reads at most maxHookStdinBytes. Over the cap, or unreadable,
// the payload is dropped rather than truncated: half a JSON document is not a
// payload, and the router treats its absence as "no session id", which fails to
// hold and is the safe side.
func readHookPayload(stdin io.Reader) ([]byte, bool) {
	if stdin == nil {
		return nil, false
	}

	limited := io.LimitReader(stdin, maxHookStdinBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil || int64(len(data)) > maxHookStdinBytes {
		return nil, false
	}
	if len(bytes.TrimSpace(data)) == 0 || !json.Valid(data) {
		return nil, false
	}
	return data, true
}

// hookBody builds the request body. The payload is embedded verbatim so the
// server, not this command, decides what a harness field means.
func hookBody(event string, payload []byte, payloadOK bool) ([]byte, error) {
	if !payloadOK {
		body, err := json.Marshal(map[string]any{"event": event})
		if err != nil {
			return nil, fmt.Errorf("server: run hook: marshal body: %w", err)
		}
		return body, nil
	}

	var buffer bytes.Buffer
	buffer.WriteString(`{"event":`)
	encodedEvent, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("server: run hook: marshal event: %w", err)
	}
	buffer.Write(encodedEvent)
	buffer.WriteString(`,"payload":`)
	buffer.Write(payload)
	buffer.WriteString(`}`)
	return buffer.Bytes(), nil
}

// hookTarget validates the recorded server URL and builds the endpoint.
//
// Only plain http to a loopback literal is accepted: no https (nothing local
// serves it), no userinfo, and no path beyond the root, so the URL cannot point
// this command at somewhere else on the machine.
func hookTarget(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "http" {
		return "", false
	}
	if parsed.User != nil {
		return "", false
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return "", false
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	if parsed.Port() == "" {
		return "", false
	}

	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimSuffix(parsed.String(), "/") + "/hook", true
}

// postHook sends the report. Any non-2xx answer is a failure, because a hook
// server that refused the event is a router that does not know about it.
func postHook(target, token string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), hookPostTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("server: run hook: build request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")

	response, err := hookClient.Do(request) //nolint:gosec // target is validated as loopback http.
	if err != nil {
		return fmt.Errorf("server: run hook: post: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<10))

	if response.StatusCode < 200 || response.StatusCode > 299 {
		return fmt.Errorf("server: run hook: status %d", response.StatusCode)
	}
	return nil
}

// reportNotSent writes the command's single line of output. It says which event
// was not reported and nothing else: no URL, no status, no token.
func reportNotSent(event string) {
	if event == "" {
		event = "hook"
	}
	_, _ = fmt.Fprintf(os.Stderr, "rtk hook: %s not reported\n", event)
}
