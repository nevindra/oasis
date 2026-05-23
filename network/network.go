package network

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/internal/runtime"
)

// agentToolParamSchema is the shared parameter schema for all agent_* tool
// definitions. Allocated once at init time; consumers treat it as immutable.
var agentToolParamSchema = json.RawMessage(
	`{"type":"object","properties":{"task":{"type":"string","description":"The user's original message, copied verbatim. Do not paraphrase, translate, or summarize."}},"required":["task"]}`,
)

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	runtime.Runtime
	agents           map[string]agent.Agent // keyed by name
	sortedAgentNames []string               // pre-sorted for deterministic tool ordering
}

// New constructs a Network — a router LLM + child agents wired via
// agent.WithAgents. Plan B will change this signature to accept children
// as positional varargs; for now, the AgentOption-based signature is
// preserved.
func New(name, description string, router core.Provider, opts ...agent.AgentOption) *Network {
	cfg := agent.BuildConfig(opts)
	n := &Network{
		agents: make(map[string]agent.Agent),
	}
	runtime.Init(&n.Runtime, name, description, router, cfg)

	// Wire spawn callback so ExecuteSpawn can create ephemeral sub-agents.
	// Networks spawn LLMAgents (not Networks), just like LLMAgent does.
	n.NewAgentFunc = func(childName, childDesc string, p core.Provider, opts ...agent.AgentOption) core.Agent {
		return agent.New(childName, childDesc, p, opts...)
	}

	for _, a := range cfg.GetAgents() {
		n.agents[a.Name()] = a
		n.sortedAgentNames = append(n.sortedAgentNames, a.Name())
	}
	sort.Strings(n.sortedAgentNames)

	// Pre-compute tool definitions for the non-dynamic path.
	// Includes agent tools + direct tools + built-in tools.
	if !n.HasDynamicTools() {
		// Network does not register ask_user, execute_plan, or spawn_agent
		// builtins (those are LLMAgent-only). Pass nil so CacheBuiltinToolDefs
		// skips them.
		n.SetCachedToolDefs(n.CacheBuiltinToolDefs(n.buildToolDefs(n.Tools().AllDefinitions()), nil, nil, nil))
	}

	return n
}

// Execute runs the network's routing loop.
// Optional RunOption values configure per-call behaviour (streaming, deadline, overrides).
func (n *Network) Execute(ctx context.Context, task agent.AgentTask, opts ...core.RunOption) (agent.AgentResult, error) {
	rcfg := core.ApplyRunOptions(opts...)
	var ro *agent.RunOptions
	if rcfg.Overrides != nil {
		if v, ok := rcfg.Overrides.(*agent.RunOptions); ok {
			ro = v
		}
	}
	if err := ro.Validate(); err != nil {
		if rcfg.Stream != nil {
			close(rcfg.Stream)
		}
		return agent.AgentResult{}, err
	}
	if rcfg.Deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, rcfg.Deadline)
		defer cancel()
	}
	ctx = agent.WithTaskContext(ctx, task)
	return n.ExecuteWithSpan(ctx, task, rcfg.Stream, "Network", "network",
		func(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) agent.LoopConfig {
			return n.buildLoopConfig(ctx, task, ch, ro)
		},
		agent.RunLoop,
	)
}

// buildLoopConfig wires Network fields into a LoopConfig for runLoop.
// Used by both Execute / ExecuteStream (opts = nil) and
// ExecuteWith / ExecuteStreamWith (opts != nil). Resolves dynamic prompt,
// model, and tools, and applies RunOptions overrides to the router config.
func (n *Network) buildLoopConfig(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent, opts *agent.RunOptions) agent.LoopConfig {
	cfg := n.ApplyRunOptions(opts)
	prompt, provider := n.ResolvePromptAndProviderWith(ctx, task, cfg)
	// Network does not use ask_user, execute_plan, or spawn_agent builtins.
	toolDefs, executeTool, executeToolStream, _ := n.ResolveTools(ctx, task, n.buildToolDefs, nil, nil, nil)
	return n.BaseLoopConfig("network:"+n.Name(), prompt, provider, toolDefs, n.makeDispatch(task, ch, executeTool, executeToolStream, toolDefs), cfg, n.ResolveMem(opts))
}

// makeDispatch returns a DispatchFunc that routes tool calls to subagents,
// the shared built-in tools, or direct tools. When ch is non-nil, agent-start
// and agent-finish events are emitted for subagent delegation. Tools
// implementing StreamingAnyTool emit progress events via executeToolStream.
func (n *Network) makeDispatch(parentTask agent.AgentTask, ch chan<- core.StreamEvent, executeTool agent.ToolExecFunc, executeToolStream agent.ToolExecStreamFunc, resolvedToolDefs []core.ToolDefinition) agent.DispatchFunc {
	agentRouter := func(ctx context.Context, tc core.ToolCall) (agent.DispatchResult, bool) {
		const prefix = "agent_"
		if !strings.HasPrefix(tc.Name, prefix) {
			return agent.DispatchResult{}, false
		}
		return n.dispatchAgent(ctx, tc, prefix, parentTask, ch), true
	}
	// Capture ch so spawn_agent forwards the child's stream events through
	// the parent's channel when the parent is running under ExecuteStream.
	spawnHandler := func(ctx context.Context, args json.RawMessage, defs []core.ToolDefinition, exec agent.ToolExecFunc) agent.DispatchResult {
		return n.ExecuteSpawn(ctx, args, defs, exec, ch, agent.ExecuteAgent)
	}
	// Wrap DispatchBuiltins to inject ask_user and execute_plan callbacks,
	// breaking the runtime→agent cycle.
	builtins := func(ctx context.Context, tc core.ToolCall, dispatch agent.DispatchFunc) (agent.DispatchResult, bool) {
		return n.DispatchBuiltins(ctx, tc, dispatch, agent.ExecuteAskUser, agent.ExecutePlan)
	}
	return agent.NewStandardDispatch(agent.StandardDispatchConfig{
		Builtins:          builtins,
		SpawnHandler:      spawnHandler,
		AgentRouter:       agentRouter,
		ExecuteTool:       executeTool,
		ExecuteToolStream: executeToolStream,
		ResolvedToolDefs:  resolvedToolDefs,
		StreamCh:          ch,
	})
}

// dispatchAgent handles delegation to a subagent. Emits agent-start/finish
// streaming events when ch is non-nil.
func (n *Network) dispatchAgent(ctx context.Context, tc core.ToolCall, agentPrefix string, parentTask agent.AgentTask, ch chan<- core.StreamEvent) agent.DispatchResult {
	agentName := tc.Name[len(agentPrefix):]
	sub, ok := n.agents[agentName]
	if !ok {
		return agent.DispatchResult{Content: fmt.Sprintf("error: unknown agent %q", agentName), IsError: true}
	}

	var params struct {
		Task string `json:"task"`
	}
	if err := json.Unmarshal(tc.Args, &params); err != nil {
		return agent.DispatchResult{Content: "error: invalid agent call args: " + err.Error(), IsError: true}
	}

	n.Logger().Info("delegating to subagent", "network", n.Name(), "agent", agentName, "task", agent.TruncateStr(params.Task, 80))

	if ch != nil {
		select {
		case ch <- core.StreamEvent{Type: core.EventAgentStart, Name: agentName, Content: params.Task}:
		case <-ctx.Done():
			return agent.DispatchResult{Content: ctx.Err().Error(), IsError: true}
		}
	}

	subTask := agent.AgentTask{
		Input:       params.Task,
		Attachments: parentTask.Attachments,
		ThreadID:    parentTask.ThreadID,
		UserID:      parentTask.UserID,
		ChatID:      parentTask.ChatID,
		Extra:       parentTask.Extra,
	}

	start := time.Now()
	result, err := agent.ExecuteAgent(ctx, sub, agentName, subTask, ch, n.Logger())
	elapsed := time.Since(start)

	if ch != nil {
		output := ""
		if err == nil {
			output = result.Output
		}
		select {
		case ch <- core.StreamEvent{
			Type:     core.EventAgentFinish,
			Name:     agentName,
			Content:  output,
			Usage:    result.Usage,
			Duration: elapsed,
		}:
		case <-ctx.Done():
		}
	}

	if err != nil {
		n.Logger().Error("subagent failed", "network", n.Name(), "agent", agentName, "error", err, "duration", elapsed)
		return agent.DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	n.Logger().Info("subagent completed", "network", n.Name(), "agent", agentName,
		"duration", elapsed,
		"input_tokens", result.Usage.InputTokens,
		"output_tokens", result.Usage.OutputTokens)
	return agent.DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}

// buildToolDefs builds tool definitions from subagents and the given tool definitions.
// Agent tools use pre-sorted names for deterministic ordering across calls.
func (n *Network) buildToolDefs(toolDefs []core.ToolDefinition) []core.ToolDefinition {
	defs := make([]core.ToolDefinition, 0, len(n.sortedAgentNames)+len(toolDefs))
	for _, name := range n.sortedAgentNames {
		defs = append(defs, core.ToolDefinition{
			Name:        "agent_" + name,
			Description: n.agents[name].Description(),
			Parameters:  agentToolParamSchema,
		})
	}
	defs = append(defs, toolDefs...)
	return defs
}

// compile-time checks
var _ core.Agent = (*Network)(nil)
