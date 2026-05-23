// network/spawn.go
//
// Dynamic spawning lets a Network's router LLM create new child agents at
// runtime in response to task complexity. Enabled via WithDynamicSpawning;
// the framework parses the router's spawn_agent tool call, calls
// SpawnPolicy.ChildBuilder to build the new agent, registers it via
// AddAgent, and returns a confirmation to the router.
package network

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

// SpawnRequest is the router LLM's spawn_agent payload. Parsed from the
// tool call's JSON args before invoking SpawnPolicy.ChildBuilder.
type SpawnRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"`
}

// SpawnPolicy controls dynamic agent creation. When non-nil on a Network,
// the router gains a spawn_agent tool; calls to it are routed through
// ChildBuilder, which constructs and returns the new Agent.
//
// MaxChildren caps the total number of spawned children for a single
// Network across its lifetime. 0 means unbounded.
//
// MaxDepth is reserved for nested-network depth limits; not enforced in
// this release.
//
// ChildBuilder is required. If nil, WithDynamicSpawning panics at
// construction time.
type SpawnPolicy struct {
	MaxDepth     int
	MaxChildren  int
	ChildBuilder func(req SpawnRequest) (core.Agent, error)
}

// spawnAgentParamSchema is the JSON Schema injected into the router's tool
// list when WithDynamicSpawning is enabled.
var spawnAgentParamSchema = json.RawMessage(
	`{"type":"object","properties":{` +
		`"name":{"type":"string","description":"Unique name for the new agent (used as agent_<name> tool)."},` +
		`"description":{"type":"string","description":"Short description of the agent's role."},` +
		`"prompt":{"type":"string","description":"System prompt for the new agent."},` +
		`"tools":{"type":"array","items":{"type":"string"},"description":"Optional names of inherited tools."}` +
		`},"required":["name","description","prompt"]}`,
)

// WithDynamicSpawning enables the spawn_agent tool on the Network's router.
// The framework parses each spawn_agent tool call into a SpawnRequest, calls
// policy.ChildBuilder to build the new agent, and registers it via AddAgent.
// The router receives a confirmation as the tool result.
//
// Policy.ChildBuilder is required. MaxChildren caps the total number of
// spawned children (0 = unbounded).
//
//	net := network.NewWithOptions("team", "Self-organizing team", routerP, nil,
//	    network.WithDynamicSpawning(network.SpawnPolicy{
//	        MaxChildren: 5,
//	        ChildBuilder: func(req network.SpawnRequest) (core.Agent, error) {
//	            return agent.New(req.Name, req.Description, p, agent.WithPrompt(req.Prompt)), nil
//	        },
//	    }),
//	)
func WithDynamicSpawning(policy SpawnPolicy) Option {
	if policy.ChildBuilder == nil {
		panic("network.WithDynamicSpawning: SpawnPolicy.ChildBuilder is required")
	}
	return func(n *Network) {
		n.spawnPolicy = &policy
		n.spawnCount = 0
	}
}

// dispatchSpawn handles a spawn_agent tool call by parsing the SpawnRequest,
// invoking SpawnPolicy.ChildBuilder, and registering the new agent via
// AddAgent. Returns a JSON confirmation as the tool result.
func (n *Network) dispatchSpawn(ctx context.Context, args json.RawMessage) agent.DispatchResult {
	if n.spawnPolicy == nil {
		return agent.DispatchResult{Content: "error: spawn_agent invoked without WithDynamicSpawning", IsError: true}
	}

	n.mu.Lock()
	if n.spawnPolicy.MaxChildren > 0 && n.spawnCount >= n.spawnPolicy.MaxChildren {
		n.mu.Unlock()
		return agent.DispatchResult{
			Content: fmt.Sprintf("error: spawn limit reached (max %d)", n.spawnPolicy.MaxChildren),
			IsError: true,
		}
	}
	n.spawnCount++
	n.mu.Unlock()

	var req SpawnRequest
	if err := json.Unmarshal(args, &req); err != nil {
		return agent.DispatchResult{Content: "error: parse spawn_agent args: " + err.Error(), IsError: true}
	}

	child, err := n.spawnPolicy.ChildBuilder(req)
	if err != nil {
		return agent.DispatchResult{Content: "error: ChildBuilder: " + err.Error(), IsError: true}
	}
	if err := n.AddAgent(child); err != nil {
		return agent.DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}

	confirm := map[string]string{"spawned": req.Name, "agent_tool": "agent_" + req.Name}
	body, _ := json.Marshal(confirm)
	return agent.DispatchResult{Content: string(body)}
}
