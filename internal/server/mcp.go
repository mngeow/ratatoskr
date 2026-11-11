package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mngeow/ratatoskr/internal/discover"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Tool and server text. The instructions sentence is fixed by the design; the
// tool description is the model-visible hint for the only tool an agent has.
const (
	toolName = "ratatoskr_send"
	// toolDescription is read by the model, so it names the tool and rules out
	// the harness's own messaging tools: Codex 0.166 ships a built-in
	// send_message, and a bare "send a message" instruction sent it there.
	toolDescription   = "Send a message to a peer agent on the ratatoskr network. This is the ratatoskr_send tool of the ratatoskr MCP server; use it, not any built-in messaging tool, to reach a peer."
	statusQueued      = "queued"
	statusHostStored  = "host_stored"
	internalErrorName = "internal_error"

	// connectedToolDescription is the model-visible description for a connected
	// agent, where a recipient is addressed by name@host and success means the
	// recipient's machine stored the message.
	connectedToolDescription = "Send a message to a peer agent on the ratatoskr network. This is the ratatoskr_send tool of the ratatoskr MCP server; use it, not any built-in messaging tool, to reach a peer. to is the peer's address, name@host (for example codex@ubuntu-b); a bare name means an agent on this machine. Success means the recipient's machine has saved the message, not that it has been read or acted on."
)

// discoverToolName is the discovery tool (ADR-0009; the name was OPEN there and is decided by lead). It is
// registered for a connected agent only: a local network has no gateway and no directory to read.
const (
	discoverToolName = "ratatoskr_discover"

	// discoverToolDescription is read by the model. It says what the reply is and is not: a directory the
	// model reads and chooses from, declarations that are not verified, and the exact ids to send to.
	discoverToolDescription = "List the hosts on the ratatoskr network and the sessions you can contact, then choose a recipient for ratatoskr_send. No arguments. The reply is TOON text: a hosts table (connection, Agent Card freshness, the description the host owner wrote), a skills table (what each host owner DECLARES; nothing verifies that a session can do it), and a sessions table (exact address and session_id, and whether the gateway shows it connected). null means unknown. This is the ratatoskr_discover tool of the ratatoskr MCP server."

	// discoveryUnavailable is the whole error when the directory cannot be read; no internal detail is added.
	discoveryUnavailable = "discovery_unavailable: this machine's daemon has no network view right now (it may not be connected to a gateway). Try ratatoskr_send with a name@host you already know."
)

// DiscoverySource gives the discovery tool the two views the daemon already keeps: the network view and the
// graph view. It reads in-process; the tool adds no request to the gateway.
type DiscoverySource func(ctx context.Context) (protocol.NetworkView, protocol.GraphView, error)

// discoverArgs is the tool's input: none.
type discoverArgs struct{}

// sendArgs is the tool's input. The sender is deliberately absent: identity is
// the request's token, never an argument a model could choose.
type sendArgs struct {
	To      string `json:"to"`
	Body    string `json:"body"`
	ReplyTo string `json:"reply_to,omitempty"`
}

// sendResult is the tool's output. Status is always "queued": the message is
// committed, not delivered.
type sendResult struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// mcpServer is one agent's MCP server.
type mcpServer struct {
	agent protocol.AgentID
	srv   *mcp.Server
}

func (m *mcpServer) handler() *mcp.Server { return m.srv }

// newAgentServer builds the MCP server for one agent: who it is, who its peers
// are, and one tool that sends a message as that agent.
//
// UNTESTED.
func newAgentServer(id protocol.AgentID, name, address string, peers []string, connected bool, inbox Inbox, discovery func() DiscoverySource) *mcpServer {
	description := toolDescription
	instructions := instructionsText(name, peers)
	if connected {
		description = connectedToolDescription
		instructions = connectedInstructions(address, peers)
	}

	srv := mcp.NewServer(
		&mcp.Implementation{Name: "ratatoskr", Version: "0.0.0"},
		&mcp.ServerOptions{Instructions: instructions},
	)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        toolName,
		Description: description,
	}, sendMessageTool(id, connected, inbox))

	if connected {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        discoverToolName,
			Description: discoverToolDescription,
		}, discoverTool(id, discovery))
	}

	return &mcpServer{agent: id, srv: srv}
}

// discoverTool is the discovery tool: read the daemon's two views, join them, and return the directory as
// TOON text (D1: TOON appears only here). The gateway does no search or ranking; the model chooses. Like
// ratatoskr_send it refuses unless the request's token is this server's agent. The reply is plain text, with
// no structured output, so the model sees exactly what discover.Render produced.
//
// UNTESTED.
func discoverTool(id protocol.AgentID, discovery func() DiscoverySource) mcp.ToolHandlerFor[discoverArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ discoverArgs) (*mcp.CallToolResult, any, error) {
		from, ok := agentFromContext(ctx)
		if !ok || from != id {
			return nil, nil, errors.New(internalErrorName)
		}
		var source DiscoverySource
		if discovery != nil {
			source = discovery()
		}
		if source == nil {
			return nil, nil, errors.New(discoveryUnavailable)
		}
		network, graph, err := source(ctx)
		if err != nil {
			return nil, nil, errors.New(discoveryUnavailable)
		}
		text := discover.Render(discover.FromViews(network, graph, time.Now()), discover.FormatTOON)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	}
}

// instructionsText is the local-mode instructions, kept as one name so the
// connected branch reads as a choice rather than a rewrite.
func instructionsText(name string, peers []string) string {
	return instructions(name, peers)
}

// sendMessageTool is the one tool an agent has. It is a named function rather
// than a closure so a test can call it without going through a transport.
//
// The sender is read from the request context, which only the auth middleware
// writes. If it is missing or is not this server's agent, the tool refuses: the
// one thing this tool must never do is send as somebody else.
//
// UNTESTED.
func sendMessageTool(id protocol.AgentID, connected bool, inbox Inbox) mcp.ToolHandlerFor[sendArgs, sendResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args sendArgs) (result *mcp.CallToolResult, output sendResult, err error) {
		from, ok := agentFromContext(ctx)
		if !ok || from != id {
			return nil, sendResult{}, errors.New(internalErrorName)
		}

		messageID, sendErr := inbox.Send(ctx, from, args.To, args.Body, args.ReplyTo)
		if sendErr != nil {
			if connected {
				// A connected refusal says whether the message may still arrive,
				// beside its reason and its id, so a model can act on it.
				return nil, sendResult{}, errors.New(connectedFailureText(sendErr))
			}
			// The reason is the whole message: a model can read it and act on it,
			// and no internal detail leaks into the transcript.
			return nil, sendResult{}, errors.New(refusalReason(sendErr))
		}

		status := statusQueued
		if connected {
			// A connected success means the recipient's machine saved it, not that
			// a model has read or acted on it.
			status = statusHostStored
		}
		return nil, sendResult{ID: messageID, Status: status}, nil
	}
}

// instructions tells the agent who it is and how to talk. Without it a harness
// would treat its peers as ordinary conversation.
func instructions(name string, peers []string) string {
	joined := strings.Join(peers, ", ")
	if joined == "" {
		joined = "(none)"
	}
	return fmt.Sprintf(
		"You are %s on a local ratatoskr network. Your peers: %s. To message a peer, use the ratatoskr_send tool from the ratatoskr MCP server: ratatoskr_send(to, body, reply_to?). Other messaging tools, including any built-in send_message, do not reach your peers. Your normal replies are not sent to peers.",
		name, joined,
	)
}

// refusalReason maps a router refusal onto the tool's error text.
//
// The router's error carries its reason behind SendReason(), which is why this
// package needs no import of the router. Anything unrecognised becomes a single
// opaque word: an unknown failure must not describe itself to a model.
//
// UNTESTED.
func refusalReason(err error) string {
	var reason interface{ SendReason() string }
	if errors.As(err, &reason) {
		if text := reason.SendReason(); text != "" {
			return text
		}
	}
	return internalErrorName
}

// mcpHandler serves /mcp. Each request is independent (Stateless) and answered
// as JSON (JSONResponse), because identity comes from that request's token and
// no session is shared between agents.
//
// UNTESTED.
func (s *Server) mcpHandler() http.Handler {
	return mcp.NewStreamableHTTPHandler(s.serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: s.opts.MaxBodyBytes,
	})
}

// serverForRequest picks the MCP server for an authenticated request.
func (s *Server) serverForRequest(r *http.Request) *mcp.Server {
	id, ok := agentFromContext(r.Context())
	if !ok {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, found := s.byID[id]
	if !found {
		return nil
	}
	return entry.srv.handler()
}

// connectedInstructions is the instruction text for an agent on a network that spans
// machines. The address is this agent's own network name; the peers are the ones
// the gateway listed at launch, which is why the text says they can change.
//
// UNTESTED.
func connectedInstructions(address string, peers []string) string {
	joined := strings.Join(peers, ", ")
	if joined == "" {
		joined = "(none)"
	}

	return fmt.Sprintf(
		"You are %s on a ratatoskr network that can span machines. Your peers at launch: %s. Peers can change; if a name is not found, the tool answers with the agents that exist. To find out who exists, call ratatoskr_discover (no arguments) before you send: it lists the hosts, what each host owner says they offer (declared, not verified) and the exact sessions you can reach. To message a peer, use the ratatoskr_send tool from the ratatoskr MCP server: ratatoskr_send(to, body, reply_to?), where to is name@host, for example codex@ubuntu-b; a bare name means an agent on this machine. Other messaging tools, including any built-in send_message, do not reach your peers. A successful result means the recipient's machine has saved the message, not that it has been read or acted on. Your normal replies are not sent to peers.",
		address, joined,
	)
}

// The optional interfaces a connected refusal is read through. internal/server must
// not import internal/router, so the router's SendError is read structurally.
type (
	sendReasoner    interface{ SendReason() string }
	sendDetailer    interface{ SendDetail() string }
	sendIDer        interface{ SendID() string }
	sendUncertainer interface{ Uncertain() bool }
)

// connectedFailureText is the tool's error text for a connected send.
//
// The one rule that matters: an uncertain result is never worded as a failure. It
// says the message may still arrive, carries the message id so a later reply can
// refer to it, and warns that sending again would create a second message. A
// definite failure says plainly that it was not delivered. Anything unrecognised
// stays the same opaque word local mode uses.
//
// UNTESTED.
func connectedFailureText(err error) string {
	if err == nil {
		return internalErrorName
	}

	// errors.As, like refusalReason: a wrapped error is read the same way, and an
	// error that implements none of these cannot panic.
	reason := ""
	var reasoner sendReasoner
	if errors.As(err, &reasoner) {
		reason = strings.TrimSpace(reasoner.SendReason())
	}
	if reason == "" {
		// An unknown failure must not describe itself.
		return internalErrorName
	}

	detail := ""
	var detailer sendDetailer
	if errors.As(err, &detailer) {
		detail = strings.TrimSpace(detailer.SendDetail())
	}
	id := ""
	var ider sendIDer
	if errors.As(err, &ider) {
		id = strings.TrimSpace(ider.SendID())
	}
	uncertain := false
	var uncertainer sendUncertainer
	if errors.As(err, &uncertainer) {
		uncertain = uncertainer.Uncertain()
	}

	switch {
	case uncertain:
		parts := []string{
			reason + ": it is not known whether the message arrived. It may still arrive; do not treat it as failed.",
		}
		if id != "" {
			parts = append(parts, "Message id: "+id+".")
		}
		parts = append(parts, detail)
		parts = append(parts, "Sending it again would be a NEW message and the recipient could receive both.")
		return joinText(parts...)

	case reason == "gateway_unreachable":
		return joinText(reason+": not sent.", detail,
			"The message was not stored anywhere and will not be delivered later.")

	case reason == "destination_unreachable":
		return joinText(reason+": not delivered. The recipient's machine is not connected.", detail,
			"It will not be delivered later; you can send it again when it is back.")

	case reason == "unknown_recipient":
		// The detail, when present, is the list of agents that do exist.
		return joinText(reason+": no such agent.", detail)

	case reason == "session_gone":
		// session_gone also carries a recipient host's refusal (for example its
		// storage being down). Then the host's own reason is the whole story, and
		// saying "no current session" would mislead.
		if strings.Contains(detail, refusalMarker) {
			return joinText(reason+": not delivered.", detail)
		}
		return joinText(reason+": not delivered.", detail,
			"That agent has no current session, and a reply to a session that ended does not carry over.")

	default:
		// Every local refusal keeps exactly the text it had before.
		return refusalReason(err)
	}
}

// refusalMarker is the start of the detail the router builds when the recipient's
// host refused an offer: "the recipient host refused it: <reason>".
const refusalMarker = "the recipient host refused it"

// joinText joins the non-empty parts with single spaces, so no double space or
// trailing space can appear.
func joinText(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " ")
}
