package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	name         string
	description  string
	router       Provider
	agents       map[string]Agent // keyed by name
	tools        *ToolRegistry
	processors   *ProcessorChain
	systemPrompt string
	maxIter      int
	inputHandler InputHandler
	mem          agentMemory
}

// NewNetwork creates a Network with the given router provider and options.
func NewNetwork(name, description string, router Provider, opts ...AgentOption) *Network {
	cfg := buildConfig(opts)
	n := &Network{
		name:         name,
		description:  description,
		router:       router,
		agents:       make(map[string]Agent),
		tools:        NewToolRegistry(),
		processors:   NewProcessorChain(),
		systemPrompt: cfg.prompt,
		maxIter:      defaultMaxIter,
		mem: agentMemory{
			store:             cfg.store,
			embedding:         cfg.embedding,
			memory:            cfg.memory,
			crossThreadSearch: cfg.crossThreadSearch,
			semanticMinScore:  cfg.semanticMinScore,
			provider:          router,
		},
	}
	if cfg.maxIter > 0 {
		n.maxIter = cfg.maxIter
	}
	for _, t := range cfg.tools {
		n.tools.Add(t)
	}
	for _, a := range cfg.agents {
		n.agents[a.Name()] = a
	}
	for _, p := range cfg.processors {
		n.processors.Add(p)
	}
	n.inputHandler = cfg.inputHandler
	return n
}

func (n *Network) Name() string        { return n.name }
func (n *Network) Description() string { return n.description }

// Execute runs the network's routing loop.
func (n *Network) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	return runLoop(ctx, n.buildLoopConfig(task), task, nil)
}

// ExecuteStream runs the network's routing loop like Execute, but streams the
// final text response into ch. Tool-calling/routing iterations use blocking
// ChatWithTools; only the final response (no tool calls) is streamed.
// The channel is closed when streaming completes.
func (n *Network) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error) {
	return runLoop(ctx, n.buildLoopConfig(task), task, ch)
}

// buildLoopConfig wires Network fields into a loopConfig for runLoop.
func (n *Network) buildLoopConfig(task AgentTask) loopConfig {
	toolDefs := n.buildToolDefs()
	if n.inputHandler != nil {
		toolDefs = append(toolDefs, askUserToolDef)
	}
	return loopConfig{
		name:         "network:" + n.name,
		provider:     n.router,
		tools:        toolDefs,
		processors:   n.processors,
		maxIter:      n.maxIter,
		mem:          &n.mem,
		inputHandler: n.inputHandler,
		dispatch:     n.makeDispatch(task),
		systemPrompt: n.systemPrompt,
	}
}

// makeDispatch returns a dispatchFunc that routes tool calls to subagents,
// the ask_user handler, or direct tools.
func (n *Network) makeDispatch(parentTask AgentTask) dispatchFunc {
	return func(ctx context.Context, tc ToolCall) (string, Usage) {
		// Special case: ask_user tool
		if tc.Name == "ask_user" && n.inputHandler != nil {
			content, err := executeAskUser(ctx, n.inputHandler, n.name, tc)
			if err != nil {
				return "error: " + err.Error(), Usage{}
			}
			return content, Usage{}
		}

		// Check if it's an agent call (prefixed with "agent_")
		if len(tc.Name) > 6 && tc.Name[:6] == "agent_" {
			agentName := tc.Name[6:]
			agent, ok := n.agents[agentName]
			if !ok {
				return fmt.Sprintf("error: unknown agent %q", agentName), Usage{}
			}

			var params struct {
				Task string `json:"task"`
			}
			if err := json.Unmarshal(tc.Args, &params); err != nil {
				return "error: invalid agent call args: " + err.Error(), Usage{}
			}

			log.Printf("[network:%s] -> agent_%s: %s", n.name, agentName, truncateStr(params.Task, 80))

			result, err := agent.Execute(ctx, AgentTask{
				Input:       params.Task,
				Attachments: parentTask.Attachments,
				Context:     parentTask.Context,
			})
			if err != nil {
				return "error: " + err.Error(), Usage{}
			}
			return result.Output, result.Usage
		}

		// Regular tool call
		result, err := n.tools.Execute(ctx, tc.Name, tc.Args)
		if err != nil {
			return "error: " + err.Error(), Usage{}
		}
		if result.Error != "" {
			return "error: " + result.Error, Usage{}
		}
		return result.Content, Usage{}
	}
}

// buildToolDefs builds tool definitions from subagents and direct tools.
func (n *Network) buildToolDefs() []ToolDefinition {
	var defs []ToolDefinition

	// Agent tool definitions
	for name, agent := range n.agents {
		defs = append(defs, ToolDefinition{
			Name:        "agent_" + name,
			Description: agent.Description(),
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"task":{"type":"string","description":"The user's original message, copied verbatim. Do not paraphrase, translate, or summarize."}},"required":["task"]}`,
			),
		})
	}

	// Direct tool definitions
	defs = append(defs, n.tools.AllDefinitions()...)
	return defs
}

// compile-time checks
var (
	_ Agent          = (*Network)(nil)
	_ StreamingAgent = (*Network)(nil)
)
