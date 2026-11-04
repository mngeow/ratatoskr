package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

// Tool and server text. The instructions sentence is fixed by the design; the
// tool description is the model-visible hint for the only tool an agent has.
const (
	toolName          = "send_message"
	toolDescription   = "Send a message to a peer agent on the ratatoskr network."
	statusQueued      = "queued"
	internalErrorName = "internal_error"
)

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
func newAgentServer(id protocol.AgentID, name string, peers []string, inbox Inbox) *mcpServer {
	srv := mcp.NewServer(
		&mcp.Implementation{Name: "ratatoskr", Version: "0.0.0"},
		&mcp.ServerOptions{Instructions: instructions(name, peers)},
	)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        toolName,
		Description: toolDescription,
	}, sendMessageTool(id, inbox))

	return &mcpServer{agent: id, srv: srv}
}

// sendMessageTool is the one tool an agent has. It is a named function rather
// than a closure so a test can call it without going through a transport.
//
// The sender is read from the request context, which only the auth middleware
// writes. If it is missing or is not this server's agent, the tool refuses: the
// one thing this tool must never do is send as somebody else.
//
// UNTESTED.
func sendMessageTool(id protocol.AgentID, inbox Inbox) mcp.ToolHandlerFor[sendArgs, sendResult] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, args sendArgs) (*mcp.CallToolResult, sendResult, error) {
		from, ok := agentFromContext(ctx)
		if !ok || from != id {
			return nil, sendResult{}, errors.New(internalErrorName)
		}

		messageID, err := inbox.Send(ctx, from, args.To, args.Body, args.ReplyTo)
		if err != nil {
			// The reason is the whole message: a model can read it and act on it,
			// and no internal detail leaks into the transcript.
			return nil, sendResult{}, errors.New(refusalReason(err))
		}
		return nil, sendResult{ID: messageID, Status: statusQueued}, nil
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
		"You are %s on a local ratatoskr network. Your peers: %s. Use send_message(to, body, reply_to?) to message a peer. Your normal replies are not sent to peers.",
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
