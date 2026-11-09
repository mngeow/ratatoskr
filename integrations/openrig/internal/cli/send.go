package cli

// send, directory and status: the thin client. It reads no credential and makes no network call; it talks to
// the sidecar over the Unix socket.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/ipc"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/render"
)

const maxBody = 64 * 1024

// resultMeanings are the Python adapter's RESULT_MEANINGS. host_stored is never described as delivered or read.
var resultMeanings = map[string]string{
	"host_stored":             "host_stored: the other side saved it; delivery and read are not confirmed",
	"session_gone":            "session_gone: the recipient session is gone; it was not saved",
	"unknown_recipient":       "unknown_recipient: no agent has that address",
	"destination_unreachable": "destination_unreachable: the destination was not reachable; it was not queued",
	"delivery_uncertain":      "delivery_uncertain: it may have been saved",
	"gateway_uncertain":       "gateway_uncertain: the first send with this id is still in progress",
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// call loads the config (the client never connects to the gateway, so the URL scheme is not checked) and
// calls the sidecar.
func call(configPath string, io IO, req ipc.Request) (ipc.Response, int) {
	cfg, code := loadConfig(configPath, true, io)
	if cfg == nil {
		return ipc.Response{}, code
	}
	resp, err := ipc.Call(context.Background(), cfg.StateDir, cfg.SocketPath(), req)
	var lost *ipc.LostReply
	switch {
	case err == nil:
		return resp, -1
	case errors.As(err, &lost):
		return ipc.Response{}, ExitUncertain
	case errors.Is(err, ipc.ErrBridgeDown):
		return ipc.Response{}, ExitBridgeDown
	}
	return ipc.Response{}, ExitUsage
}

func runSend(args []string, io IO) int {
	fs := newFlags("send")
	configPath := fs.String("config", "", "")
	to := fs.String("to", "", "")
	replyTo := fs.String("reply-to", "", "")
	id := fs.String("id", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fail(io, ExitUsage, "usage: send --to NAME@HOST [--reply-to ID] [--id ID] [--config PATH]  (message on standard input)")
	}
	if !render.ValidAddress(*to) {
		return fail(io, ExitUsage, "--to must look like name@host")
	}
	if *replyTo != "" && !render.ValidID(*replyTo) {
		return fail(io, ExitUsage, "--reply-to is not a valid message id")
	}
	if *id == "" {
		generated, err := newID()
		if err != nil {
			return fail(io, ExitUsage, "cannot make a message id")
		}
		*id = generated
	} else if !render.ValidID(*id) {
		return fail(io, ExitUsage, "--id is not a valid message id")
	}
	body, err := readAll(io.Stdin, maxBody)
	switch {
	case err != nil:
		return fail(io, ExitUsage, "%v", err)
	case len(body) == 0:
		return fail(io, ExitUsage, "the message on standard input is empty")
	case !utf8.Valid(body):
		return fail(io, ExitUsage, "the message is not valid UTF-8")
	}

	resp, code := call(*configPath, io, ipc.Request{Op: ipc.OpSend, ID: *id, To: *to, ReplyTo: *replyTo, Body: string(body)})
	switch code {
	case ExitBridgeDown:
		fmt.Fprintln(io.Stderr, "NOT SENT: the bridge is not running: start the sidecar (ratatoskr-openrig serve), then send again")
		return ExitBridgeDown
	case ExitUncertain:
		fmt.Fprintf(io.Stderr, "UNCERTAIN: the request reached the bridge but no answer came back; it may have been stored. Do not resend under a new id: repeat the same command with --id %s\n", *id)
		fmt.Fprintf(io.Stdout, "id: %s\n", *id)
		return ExitUncertain
	case -1:
	default:
		return code
	}

	printResult := func() {
		if m, ok := resultMeanings[resp.Result]; ok {
			fmt.Fprintln(io.Stdout, m)
		}
		if resp.Detail != "" {
			fmt.Fprintf(io.Stdout, "detail: %s\n", terminalText(resp.Detail))
		}
		fmt.Fprintf(io.Stdout, "id: %s\n", *id)
	}
	switch resp.Outcome {
	case ipc.OutcomeStored:
		printResult()
		return ExitOK
	case ipc.OutcomeNotDelivered:
		printResult()
		if resp.Result == "unknown_recipient" {
			for _, d := range resp.Directory {
				fmt.Fprintf(io.Stdout, "  %s%s\n", terminalText(d.Address), liveNote(d.Live))
			}
		}
		return ExitFailed
	case ipc.OutcomeUncertain:
		if _, known := resultMeanings[resp.Result]; known {
			printResult()
		} else {
			fmt.Fprintf(io.Stdout, "UNCERTAIN: %s\nid: %s\n", terminalText(resp.Detail), *id)
		}
		fmt.Fprintln(io.Stdout, "do not resend under a new id; repeat with --id "+*id+" only after checking")
		return ExitUncertain
	case ipc.OutcomeNotSent:
		fmt.Fprintf(io.Stderr, "NOT SENT: %s\n", terminalText(resp.Detail))
		return ExitFailed
	case ipc.OutcomeRevoked:
		fmt.Fprintln(io.Stderr, "the gateway refused this credential: it is revoked or unknown")
		return ExitRevoked
	case ipc.OutcomeBusy:
		fmt.Fprintln(io.Stderr, "NOT SENT: the bridge is busy; try again")
		return ExitFailed
	case ipc.OutcomeRejected:
		fmt.Fprintf(io.Stderr, "ratatoskr-openrig: the bridge rejected the request: %s\n", terminalText(resp.Detail))
		return ExitUsage
	}
	fmt.Fprintf(io.Stdout, "UNCERTAIN: the bridge answered something unexpected\nid: %s\n", *id)
	return ExitUncertain
}

func liveNote(live bool) string {
	if live {
		return ""
	}
	return " (not live)"
}

func readAll(r io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, errors.New("cannot read standard input")
	}
	if len(data) > limit {
		return nil, fmt.Errorf("the message is larger than %d bytes", limit)
	}
	return data, nil
}

func runDirectory(args []string, io IO) int {
	fs := newFlags("directory")
	configPath := fs.String("config", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 {
		return fail(io, ExitUsage, "usage: directory [--config PATH]")
	}
	resp, code := call(*configPath, io, ipc.Request{Op: ipc.OpDirectory})
	switch code {
	case ExitBridgeDown:
		fmt.Fprintln(io.Stderr, "the bridge is not running")
		return ExitBridgeDown
	case -1:
	default:
		return code
	}
	switch resp.Outcome {
	case ipc.OutcomeOK:
		for _, d := range resp.Directory {
			fmt.Fprintf(io.Stdout, "%s%s\n", terminalText(d.Address), liveNote(d.Live))
		}
		return ExitOK
	case ipc.OutcomeRevoked:
		fmt.Fprintln(io.Stderr, "the gateway refused this credential: it is revoked or unknown")
		return ExitRevoked
	}
	fmt.Fprintf(io.Stderr, "ratatoskr-openrig: the directory could not be read: %s\n", terminalText(resp.Detail))
	return ExitFailed
}

// pinSummary shows the first 8 hex digits of the configured pin, never the whole pin.
func pinSummary(cfg *config.Config) string {
	if cfg.Scheme != "https" {
		return "none (plain HTTP)"
	}
	return "sha256:" + hex.EncodeToString(cfg.Pin[:4]) + "..."
}
