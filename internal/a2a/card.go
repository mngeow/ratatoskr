package a2a

// The endpoint card: what the gateway publishes for each participant endpoint. It declares only what is
// true (contract section 6): no streaming, no push, no extended card, one JSON-RPC interface at protocol
// 1.0, a bearer scheme, text modes. The card's own URL comes from configuration, never from a request
// header.
//
// UNTESTED.

import (
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// AgentsPath is the route prefix; {id} follows it.
const AgentsPath = "/a2a/agents/"

// CardSuffix is the public well-known path below an endpoint.
const CardSuffix = "/.well-known/agent-card.json"

// EndpointURL is the POST URL of a participant's endpoint.
func EndpointURL(base, id string) string {
	return strings.TrimRight(base, "/") + AgentsPath + id
}

// EndpointCard builds the card for one participant. name is a display name (a session name and host, or the
// enrolled participant name); it is not an identifier.
func EndpointCard(base string, target Target, name string) *a2a.AgentCard {
	description := "A managed Ratatoskr session. Messages are delivered through the gateway."
	if target.Kind == TargetService {
		description = "An A2A service registered with this Ratatoskr gateway. Messages are forwarded to it."
	}
	return &a2a.AgentCard{
		Name:        name,
		Description: description,
		Version:     "1",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(EndpointURL(base, target.ID), a2a.TransportProtocolJSONRPC),
		},
		Capabilities:       a2a.AgentCapabilities{},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []a2a.AgentSkill{{
			ID:          "message",
			Name:        "Receive a message",
			Description: fmt.Sprintf("Accepts a text message and returns a task you can read with GetTask."),
			Tags:        []string{"ratatoskr", "message"},
		}},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "Bearer", Description: "A credential issued by the gateway owner."},
		},
		SecurityRequirements: a2a.SecurityRequirementsOptions{a2a.SecurityRequirements{"bearer": {}}},
	}
}
