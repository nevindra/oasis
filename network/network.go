package network

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
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

// Option configures a Network. Pass Option values to NewWithOptions.
// Use WithRouter to forward agent.AgentOption values to the router LLM.
type Option func(*Network)

// WithRouter wraps agent.AgentOption values for the Network's router LLM.
// Use this to give the router its own tools, memory, tracer, etc.
//
//	net := network.NewWithOptions("team", "...", routerP, []core.Agent{a, b},
//	    network.WithRouter(agent.WithTracer(t), agent.WithMemory(...)),
//	)
func WithRouter(opts ...agent.AgentOption) Option {
	return func(n *Network) { n.pendingRouterOpts = append(n.pendingRouterOpts, opts...) }
}

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	runtime.Runtime
	mu               sync.RWMutex           // guards agents + sortedAgentNames
	agents           map[string]agent.Agent // keyed by name
	sortedAgentNames []string               // pre-sorted for deterministic tool ordering

	// pendingRouterOpts is non-nil only between Option application and
	// runtime.Init. Released to nil immediately after BuildConfig consumes it.
	pendingRouterOpts []agent.AgentOption

	// parallelDispatch controls per-iteration tool-call parallelism.
	// Set via WithParallelDispatch; default (zero value) means ParallelDefault.
	parallelDispatch ParallelDispatch

	// supervisor is a network-wide SupervisorPolicy applied to every child.
	// Per-child policies (supervisorPerChild) compose on top via Chain.
	supervisor         SupervisorPolicy
	supervisorPerChild map[string]SupervisorPolicy

	// spawnPolicy is non-nil when WithDynamicSpawning is configured.
	// It controls the spawn_agent tool injected into the router's tool list.
	// spawnCount is incremented on each successful spawn; protected by n.mu.
	spawnPolicy *SpawnPolicy
	spawnCount  int
}

// New constructs a Network — a router LLM coordinating one or more child
// agents. Children are passed as positional varargs. For orchestration
// policies (supervisors, parallel dispatch, dynamic spawning) or
// router-side configuration (memory, tracer, tools), use NewWithOptions.
func New(name, description string, router core.Provider, children ...core.Agent) *Network {
	return NewWithOptions(name, description, router, children)
}

// NewWithOptions constructs a Network with explicit children and network-level
// options. Children are passed as a slice; opts configure orchestration
// policies and router-side settings via WithRouter.
func NewWithOptions(name, description string, router core.Provider, children []core.Agent, opts ...Option) *Network {
	n := &Network{
		agents: make(map[string]agent.Agent, len(children)),
	}

	// Apply options first — they may mutate Network fields BEFORE runtime init.
	for _, opt := range opts {
		if opt != nil {
			opt(n)
		}
	}

	// Build router's Config from any WithRouter-supplied opts, then init runtime.
	cfg := agent.BuildConfig(n.pendingRouterOpts)
	n.pendingRouterOpts = nil
	// Apply parallelDispatch override AFTER BuildConfig so we can override the
	// default (BuildConfig sets MaxParallelDispatch=10 when unset).
	if n.parallelDispatch == ParallelDisabled {
		cfg.MaxParallelDispatch = 1
	}
	runtime.Init(&n.Runtime, name, description, router, cfg)

	// Register children directly (no agent.WithAgents indirection).
	// Each child is wrapped with any configured supervisor policies.
	for _, ch := range children {
		n.agents[ch.Name()] = n.wrapChild(ch)
		n.sortedAgentNames = append(n.sortedAgentNames, ch.Name())
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

// wrapChild applies the Network's supervisor policies to child before storing
// it. The network-wide policy (WithSupervisor) wraps first; per-child policy
// (WithSupervisorFor) wraps outermost. Used at construction and by AddAgent.
func (n *Network) wrapChild(child core.Agent) core.Agent {
	wrapped := child
	if n.supervisor != nil {
		wrapped = n.supervisor.Wrap(wrapped)
	}
	if perChild := n.supervisorPerChild[child.Name()]; perChild != nil {
		wrapped = perChild.Wrap(wrapped)
	}
	return wrapped
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
		if tc.Name == "spawn_agent" {
			if n.spawnPolicy == nil {
				return agent.DispatchResult{Content: "error: spawn_agent invoked without WithDynamicSpawning", IsError: true}, true
			}
			return n.dispatchSpawn(ctx, tc.Args), true
		}
		const prefix = "agent_"
		if !strings.HasPrefix(tc.Name, prefix) {
			return agent.DispatchResult{}, false
		}
		return n.dispatchAgent(ctx, tc, prefix, parentTask, ch), true
	}
	// Wrap DispatchBuiltins to inject ask_user and execute_plan callbacks,
	// breaking the runtime→agent cycle.
	builtins := func(ctx context.Context, tc core.ToolCall, dispatch agent.DispatchFunc) (agent.DispatchResult, bool) {
		return n.DispatchBuiltins(ctx, tc, dispatch, agent.ExecuteAskUser, agent.ExecutePlan)
	}
	return agent.NewStandardDispatch(agent.StandardDispatchConfig{
		Builtins:          builtins,
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
	n.mu.RLock()
	sub, ok := n.agents[agentName]
	n.mu.RUnlock()
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
// When WithDynamicSpawning is enabled, a spawn_agent tool def is appended
// after agent tools so the router LLM can create new child agents at runtime.
func (n *Network) buildToolDefs(toolDefs []core.ToolDefinition) []core.ToolDefinition {
	n.mu.RLock()
	defer n.mu.RUnlock()
	defs := make([]core.ToolDefinition, 0, len(n.sortedAgentNames)+len(toolDefs)+1)
	for _, name := range n.sortedAgentNames {
		defs = append(defs, core.ToolDefinition{
			Name:        "agent_" + name,
			Description: n.agents[name].Description(),
			Parameters:  agentToolParamSchema,
		})
	}
	if n.spawnPolicy != nil {
		defs = append(defs, core.ToolDefinition{
			Name:        "spawn_agent",
			Description: "Dynamically create a new sub-agent to handle a specialized task. Provide name (unique), description, and system prompt.",
			Parameters:  spawnAgentParamSchema,
		})
	}
	defs = append(defs, toolDefs...)
	return defs
}

// compile-time checks
var _ core.Agent = (*Network)(nil)
