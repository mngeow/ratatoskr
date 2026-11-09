package sidecar

// The local socket handler, the status report and Run (contract sections 11 and 13).
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"context"
	"encoding/json"
	"net"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/gw"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/ipc"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/render"
)

const maxOutboundBody = 64 * 1024

func rejected(detail string) ipc.Response {
	return ipc.Response{Outcome: ipc.OutcomeRejected, Detail: detail}
}

func entries(in []gw.DirectoryEntry) []ipc.DirectoryEntry {
	out := make([]ipc.DirectoryEntry, 0, len(in))
	for _, e := range in {
		out = append(out, ipc.DirectoryEntry{Address: e.Address, Live: e.Live})
	}
	return out
}

// Handle answers one local request.
func (s *Sidecar) Handle(ctx context.Context, req ipc.Request) ipc.Response {
	switch req.Op {
	case ipc.OpSend:
		return s.handleSend(ctx, req)
	case ipc.OpDirectory:
		dir, e := s.GW.Directory(ctx)
		s.setGateway(e)
		if e != nil {
			return s.errorResponse(e, "")
		}
		return ipc.Response{Outcome: ipc.OutcomeOK, Directory: entries(dir)}
	case ipc.OpStatus:
		raw, _ := json.Marshal(s.Status(ctx))
		return ipc.Response{Outcome: ipc.OutcomeOK, Status: raw}
	}
	return rejected("unknown op")
}

func (s *Sidecar) handleSend(ctx context.Context, req ipc.Request) ipc.Response {
	switch {
	case !render.ValidID(req.ID):
		return rejected("id")
	case !render.ValidAddress(req.To):
		return rejected("to")
	case req.ReplyTo != "" && !render.ValidID(req.ReplyTo):
		return rejected("reply_to")
	case req.Body == "" || len(req.Body) > maxOutboundBody || !utf8.ValidString(req.Body):
		return rejected("body")
	}
	result, e := s.GW.Send(ctx, gw.SendRequest{ID: req.ID, From: s.Cfg.Session, To: req.To, ReplyTo: req.ReplyTo, Body: req.Body})
	s.setGateway(e)
	if e != nil {
		return s.errorResponse(e, req.ID)
	}
	resp := ipc.Response{ID: req.ID, Result: result.Result, Detail: terminalSafe(result.Detail)}
	switch result.Result {
	case "host_stored":
		resp.Outcome = ipc.OutcomeStored
	case "session_gone", "destination_unreachable":
		resp.Outcome = ipc.OutcomeNotDelivered
	case "unknown_recipient":
		resp.Outcome = ipc.OutcomeNotDelivered
		resp.Directory = entries(result.Directory)
	default:
		// delivery_uncertain, gateway_uncertain and every unknown word: uncertain, never failed.
		resp.Outcome = ipc.OutcomeUncertain
	}
	return resp
}

// errorResponse maps a request failure to an outcome. For a send, "the request may have been written" decides
// between uncertain and not sent.
func (s *Sidecar) errorResponse(e *gw.Error, id string) ipc.Response {
	r := ipc.Response{ID: id, Detail: e.Msg}
	switch e.Kind {
	case gw.KindRevoked:
		r.Outcome = ipc.OutcomeRevoked
	case gw.KindPin:
		r.Outcome = ipc.OutcomeNotSent
	case gw.KindRefused:
		r.Outcome = ipc.OutcomeNotSent
	case gw.KindNetwork:
		if e.Wrote {
			r.Outcome = ipc.OutcomeUncertain
		} else {
			r.Outcome = ipc.OutcomeNotSent
		}
	default: // server error, protocol surprise: the gateway may have stored it
		r.Outcome = ipc.OutcomeUncertain
	}
	return r
}

// terminalSafe keeps gateway text printable.
func terminalSafe(s string) string {
	if len(s) > 200 {
		s = s[:200]
	}
	return render.Sanitize(s)
}

// Run is everything after Start: it serves the socket and polls until ctx ends or the sidecar must stop.
// It returns the exit code.
func (s *Sidecar) Run(ctx context.Context, listener net.Listener) int {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		ipc.Serve(serveCtx, listener, s.Handle)
		close(done)
	}()
	code := s.PollLoop(ctx)
	_ = listener.Close()
	cancel()
	<-done
	s.Wait()
	return code
}
