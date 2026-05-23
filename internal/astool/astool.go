// Package astool wraps a core.Agent so a parent's router LLM can call it via
// a tool call. This is the LLM-protocol bridge: LLMs only emit tool calls,
// never "agent calls", so when a Network needs to expose a child to its
// router, it does so through this wrapper.
//
// This is an implementation detail. The framework's public API is "use
// Network to compose agents." How Network exposes children to the router LLM
// is its own business.
package astool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nevindra/oasis/core"
)

// Prefix is the convention all agent-as-tool wrappers use. Network's dispatch
// path detects this prefix to distinguish sub-agent calls from regular tools.
const Prefix = "agent_"

// agentToolParams is the JSON Schema for the single "task" argument.
// Serialised once at init time; all wrappers share the same bytes.
var agentToolParams json.RawMessage

func init() {
	schema := core.SchemaObject{
		Type: "object",
		Properties: map[string]*core.SchemaObject{
			"task": {
				Type:        "string",
				Description: "The task description to delegate to the sub-agent.",
			},
		},
		Required: []string{"task"},
	}
	b, err := json.Marshal(schema)
	if err != nil {
		panic("astool: failed to marshal parameter schema: " + err.Error())
	}
	agentToolParams = b
}

// Wrap returns a core.AnyTool that, when invoked, calls ag.Execute with the
// "task" string from the tool call's arguments.
func Wrap(ag core.Agent) core.AnyTool {
	return &agentTool{
		ag: ag,
		def: core.ToolDefinition{
			Name:        Prefix + ag.Name(),
			Description: ag.Description(),
			Parameters:  agentToolParams,
		},
	}
}

// Unwrap recovers the original Agent from a tool returned by Wrap. Returns
// (nil, false) for tools not produced by this package.
func Unwrap(tool core.AnyTool) (core.Agent, bool) {
	at, ok := tool.(*agentTool)
	if !ok {
		return nil, false
	}
	return at.ag, true
}

type agentTool struct {
	ag  core.Agent
	def core.ToolDefinition
}

func (t *agentTool) Name() string                    { return t.def.Name }
func (t *agentTool) Definition() core.ToolDefinition { return t.def }

func (t *agentTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var p struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return core.ToolResult{Error: "astool: parse args: " + err.Error()}, nil
	}
	res, err := t.ag.Execute(ctx, core.AgentTask{Input: p.Task})
	if err != nil {
		return core.ToolResult{Error: err.Error()}, err
	}
	content, err := json.Marshal(res.Output)
	if err != nil {
		return core.ToolResult{Error: fmt.Sprintf("astool: marshal result: %v", err)}, nil
	}
	return core.ToolResult{Content: content}, nil
}
