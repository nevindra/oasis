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
	agent.AgentCore
	agents           map[string]agent.Agent // keyed by name
	sortedAgentNames []string               // pre-sorted for deterministic tool ordering
}

// NewNetwork creates a Network with the given router provider and options.
func NewNetwork(name, description string, router core.Provider, opts ...agent.AgentOption) *Network {
	cfg := agent.BuildConfig(opts)
	n := &Network{
		agents: make(map[string]agent.Agent),
	}
	agent.InitCore(&n.AgentCore, name, description, router, cfg)

	for _, a := range cfg.Agents() {
		n.agents[a.Name()] = a
		n.sortedAgentNames = append(n.sortedAgentNames, a.Name())
	}
	sort.Strings(n.sortedAgentNames)

	// Pre-compute tool definitions for the non-dynamic path.
	// Includes agent tools + direct tools + built-in tools.
	if !n.HasDynamicTools() {
		n.SetCachedToolDefs(n.CacheBuiltinToolDefs(n.buildToolDefs(n.Tools().AllDefinitions())))
	}

	return n
}

// Execute runs the network's routing loop.
func (n *Network) Execute(ctx context.Context, task agent.AgentTask) (agent.AgentResult, error) {
	ctx = agent.WithTaskContext(ctx, task)
	return n.ExecuteWithSpan(ctx, task, nil, "Network", "network", func(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) agent.LoopConfig {
		return n.buildLoopConfig(ctx, task, ch, nil)
	})
}

// ExecuteStream runs the network's routing loop like Execute, but emits
// StreamEvent values into ch throughout execution. Events include text deltas,
// tool call start/result, and agent start/finish for subagent delegation.
// The channel is closed when streaming completes.
func (n *Network) ExecuteStream(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) (agent.AgentResult, error) {
	ctx = agent.WithTaskContext(ctx, task)
	return n.ExecuteWithSpan(ctx, task, ch, "Network", "network", func(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) agent.LoopConfig {
		return n.buildLoopConfig(ctx, task, ch, nil)
	})
}

// ExecuteWith runs the network like Execute and applies per-call RunOptions
// to the network's router (Prompt, Generation, MaxIter, MaxSteps,
// MaxPlanSteps, ResponseSchema, MaxAttachmentBytes, MaxToolResultLen, Hooks,
// Tracer, Logger, Metadata, Memory). RunOptions are NOT propagated to
// subagents — they keep the per-agent configuration they were constructed
// with. To override a subagent, pass that agent's own ExecuteWith call site
// or rebuild the subagent.
func (n *Network) ExecuteWith(ctx context.Context, task core.AgentTask, opts *agent.RunOptions) (core.AgentResult, error) {
	if err := opts.Validate(); err != nil {
		return core.AgentResult{}, err
	}
	ctx = agent.WithTaskContext(ctx, task)
	return n.ExecuteWithSpan(ctx, task, nil, "Network", "network", func(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) agent.LoopConfig {
		return n.buildLoopConfig(ctx, task, ch, opts)
	})
}

// ExecuteStreamWith runs the network like ExecuteStream and applies per-call
// RunOptions to the network's router. See ExecuteWith for the propagation
// caveat; this method also closes ch on validation error.
func (n *Network) ExecuteStreamWith(ctx context.Context, task core.AgentTask, ch chan<- core.StreamEvent, opts *agent.RunOptions) (core.AgentResult, error) {
	if err := opts.Validate(); err != nil {
		close(ch)
		return core.AgentResult{}, err
	}
	ctx = agent.WithTaskContext(ctx, task)
	return n.ExecuteWithSpan(ctx, task, ch, "Network", "network", func(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) agent.LoopConfig {
		return n.buildLoopConfig(ctx, task, ch, opts)
	})
}

// buildLoopConfig wires Network fields into a LoopConfig for runLoop.
// Used by both Execute / ExecuteStream (opts = nil) and
// ExecuteWith / ExecuteStreamWith (opts != nil). Resolves dynamic prompt,
// model, and tools, and applies RunOptions overrides to the router config.
func (n *Network) buildLoopConfig(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent, opts *agent.RunOptions) agent.LoopConfig {
	cfg := n.ApplyRunOptions(opts)
	prompt, provider := n.ResolvePromptAndProviderWith(ctx, task, cfg)
	toolDefs, executeTool, executeToolStream, _ := n.ResolveTools(ctx, task, n.buildToolDefs)
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
	return agent.NewStandardDispatch(agent.StandardDispatchConfig{
		Builtins:          n.DispatchBuiltins,
		SpawnHandler:      n.ExecuteSpawn,
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
		case ch <- core.StreamEvent{Type: agent.EventAgentStart, Name: agentName, Content: params.Task}:
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
			Type:     agent.EventAgentFinish,
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
var (
	_ agent.Agent                     = (*Network)(nil)
	_ agent.StreamingAgent            = (*Network)(nil)
	_ agent.AgentWithOptions          = (*Network)(nil)
	_ agent.StreamingAgentWithOptions = (*Network)(nil)
)
