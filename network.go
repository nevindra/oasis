package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	agentCore
	agents           map[string]Agent // keyed by name
	sortedAgentNames []string         // pre-sorted for deterministic tool ordering
}

// NewNetwork creates a Network with the given router provider and options.
func NewNetwork(name, description string, router Provider, opts ...AgentOption) *Network {
	cfg := buildConfig(opts)
	n := &Network{
		agents: make(map[string]Agent),
	}
	initCore(&n.agentCore, name, description, router, cfg)

	for _, a := range cfg.agents {
		n.agents[a.Name()] = a
		n.sortedAgentNames = append(n.sortedAgentNames, a.Name())
	}
	sort.Strings(n.sortedAgentNames)

	// Pre-compute tool definitions for the non-dynamic path.
	// Includes agent tools + direct tools + built-in tools.
	if n.dynamicTools == nil {
		n.cachedToolDefs = n.cacheBuiltinToolDefs(n.buildToolDefs(n.tools.AllDefinitions()))
	}

	return n
}

// Execute runs the network's routing loop.
func (n *Network) Execute(ctx context.Context, task AgentTask) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return n.executeWithSpan(ctx, task, nil, "Network", "network", n.buildLoopConfig)
}

// ExecuteStream runs the network's routing loop like Execute, but emits
// StreamEvent values into ch throughout execution. Events include text deltas,
// tool call start/result, and agent start/finish for subagent delegation.
// The channel is closed when streaming completes.
func (n *Network) ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	ctx = WithTaskContext(ctx, task)
	return n.executeWithSpan(ctx, task, ch, "Network", "network", n.buildLoopConfig)
}

// buildLoopConfig wires Network fields into a loopConfig for runLoop.
// Resolves dynamic prompt, model, and tools when configured.
// ch is passed through so makeDispatch can emit agent-start/finish events.
func (n *Network) buildLoopConfig(ctx context.Context, task AgentTask, ch chan<- StreamEvent) loopConfig {
	prompt, provider := n.resolvePromptAndProvider(ctx, task)

	// Resolve tools: dynamic replaces static.
	var toolDefs []ToolDefinition
	var executeTool toolExecFunc
	if dynDefs, dynExec := n.resolveDynamicTools(ctx, task); dynDefs != nil {
		toolDefs = n.cacheBuiltinToolDefs(n.buildToolDefs(dynDefs))
		executeTool = dynExec
	} else {
		toolDefs = n.cachedToolDefs
		executeTool = n.tools.Execute
	}

	return n.baseLoopConfig("network:"+n.name, prompt, provider, toolDefs, n.makeDispatch(task, ch, executeTool))
}

// makeDispatch returns a DispatchFunc that routes tool calls to subagents,
// the shared built-in tools, or direct tools. When ch is non-nil, agent-start
// and agent-finish events are emitted for subagent delegation.
func (n *Network) makeDispatch(parentTask AgentTask, ch chan<- StreamEvent, executeTool toolExecFunc) DispatchFunc {
	var dispatch DispatchFunc
	dispatch = func(ctx context.Context, tc ToolCall) DispatchResult {
		// Built-in tools: ask_user, execute_plan, execute_code.
		if r, ok := dispatchBuiltins(ctx, tc, dispatch, n.inputHandler, n.name, n.planExecution, n.codeRunner); ok {
			return r
		}

		// Check if it's an agent call (prefixed with "agent_")
		const agentPrefix = "agent_"
		if strings.HasPrefix(tc.Name, agentPrefix) {
			return n.dispatchAgent(ctx, tc, agentPrefix, parentTask, ch)
		}

		// Regular tool call.
		return dispatchTool(ctx, executeTool, tc.Name, tc.Args)
	}
	return dispatch
}

// dispatchAgent handles delegation to a subagent. Emits agent-start/finish
// streaming events when ch is non-nil.
func (n *Network) dispatchAgent(ctx context.Context, tc ToolCall, agentPrefix string, parentTask AgentTask, ch chan<- StreamEvent) DispatchResult {
	agentName := tc.Name[len(agentPrefix):]
	agent, ok := n.agents[agentName]
	if !ok {
		return DispatchResult{Content: fmt.Sprintf("error: unknown agent %q", agentName), IsError: true}
	}

	var params struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(tc.Args, &params); err != nil {
		return DispatchResult{Content: "error: invalid agent call args: " + err.Error(), IsError: true}
	}

	n.logger.Info("delegating to subagent", "network", n.name, "agent", agentName, "task", truncateStr(params.Task, 80))

	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventAgentStart, Name: agentName, Content: params.Task}:
		case <-ctx.Done():
			return DispatchResult{Content: ctx.Err().Error(), IsError: true}
		}
	}

	subTask := AgentTask{
		Input:       params.Task,
		Attachments: parentTask.Attachments,
		Context:     parentTask.Context,
	}

	start := time.Now()
	result, err := executeAgent(ctx, agent, agentName, subTask, ch, n.logger)
	elapsed := time.Since(start)

	if ch != nil {
		output := ""
		if err == nil {
			output = result.Output
		}
		select {
		case ch <- StreamEvent{
			Type:     EventAgentFinish,
			Name:     agentName,
			Content:  output,
			Usage:    result.Usage,
			Duration: elapsed,
		}:
		case <-ctx.Done():
		}
	}

	if err != nil {
		return DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}

// buildToolDefs builds tool definitions from subagents and the given tool definitions.
// Agent tools use pre-sorted names for deterministic ordering across calls.
func (n *Network) buildToolDefs(toolDefs []ToolDefinition) []ToolDefinition {
	var defs []ToolDefinition

	// Agent tool definitions (order fixed at construction time).
	for _, name := range n.sortedAgentNames {
		defs = append(defs, ToolDefinition{
			Name:        "agent_" + name,
			Description: n.agents[name].Description(),
			Parameters: json.RawMessage(
				`{"type":"object","properties":{"task":{"type":"string","description":"The user's original message, copied verbatim. Do not paraphrase, translate, or summarize."}},"required":["task"]}`,
			),
		})
	}

	// Direct tool definitions
	defs = append(defs, toolDefs...)
	return defs
}

// compile-time checks
var (
	_ Agent          = (*Network)(nil)
	_ StreamingAgent = (*Network)(nil)
)
