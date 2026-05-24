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

// Option configures a Network. Pass Option values to network.New.
// Use WithRouter to forward agent.AgentOption values to the router LLM.
type Option func(*Network)

// WithRouter wraps agent.AgentOption values for the Network's router LLM.
// Use this to give the router its own tools, memory, tracer, etc.
//
//	net := network.New("team", "...", routerP,
//	    network.WithChildren(a, b),
//	    network.WithRouter(agent.WithTracer(t), agent.WithMemory(...)),
//	)
func WithRouter(opts ...agent.AgentOption) Option {
	return func(n *Network) { n.pendingRouterOpts = append(n.pendingRouterOpts, opts...) }
}

// WithChildren registers child agents on the Network. May be called multiple
// times; children accumulate. Each child is wrapped with any configured
// SupervisorPolicy at construction time, after all options are applied.
//
//	net := network.New("coordinator", "...", routerP,
//	    network.WithChildren(searchAgent, summarizeAgent),
//	)
func WithChildren(children ...core.Agent) Option {
	return func(n *Network) { n.pendingChildren = append(n.pendingChildren, children...) }
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

	// pendingChildren holds children registered via WithChildren before
	// construction completes. Cleared to nil after supervisor wrapping.
	pendingChildren []core.Agent

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

// New constructs a Network — a router LLM coordinating zero or more child
// agents. All configuration (children, supervisor, dynamic spawning, router
// options) flows through Options.
//
//	net := network.New("coordinator", "...", routerP,
//	    network.WithChildren(searchAgent, summarizeAgent),
//	    network.WithRouter(agent.WithTracer(t)),
//	)
func New(name, description string, router core.Provider, opts ...Option) *Network {
	n := &Network{
		agents: make(map[string]agent.Agent),
	}

	// Apply options first — they may mutate Network fields BEFORE runtime init.
	// WithSupervisor and WithChildren both write pendingChildren / supervisor,
	// so ordering within opts is respected naturally.
	for _, opt := range opts {
		if opt != nil {
			opt(n)
		}
	}

	// Build router's Config from any WithRouter-supplied opts, then init runtime.
	cfg := agent.BuildConfig(n.pendingRouterOpts)
	n.pendingRouterOpts = nil
	runtime.Init(&n.Runtime, name, description, router, cfg)

	// Register children from WithChildren calls. All opts have been applied
	// so supervisor policies are set — wrapChild sees the final supervisor state.
	// Why: fail at construction if two children share a name. Without this,
	// the duplicate would silently overwrite n.agents[name] while sortedAgentNames
	// accumulates a duplicate entry, emitting two identical agent_<name> tool
	// definitions to the router LLM.
	for _, ch := range n.pendingChildren {
		childName := ch.Name()
		if _, exists := n.agents[childName]; exists {
			panic("network: duplicate child agent name " + childName)
		}
		n.agents[childName] = n.wrapChild(ch)
		n.sortedAgentNames = append(n.sortedAgentNames, childName)
	}
	n.pendingChildren = nil
	sort.Strings(n.sortedAgentNames)

	// Pre-compute tool definitions for the non-dynamic path.
	// Includes agent tools + direct tools + built-in tools.
	if !n.HasDynamicTools() {
		n.rebuildCachedToolDefsLocked()
	}

	return n
}

// rebuildCachedToolDefsLocked recomputes the router's tool definitions and
// stores them in the Runtime's cache. Caller must hold n.mu (write lock) — or
// be running before any concurrent access exists (e.g. during construction).
//
// Why: Network membership (agents map, sortedAgentNames) and the spawn policy
// flag can change at runtime via AddAgent/RemoveAgent/dispatchSpawn. The
// runtime's non-dynamic ResolveTools path returns the cached slice unchanged,
// so the cache must be invalidated whenever membership changes — otherwise the
// router LLM never sees the new agent_<name> tool and silently can't delegate.
//
// Network does not register ask_user, execute_plan, or spawn_agent builtins
// (LLMAgent-only); nil placeholders make CacheBuiltinToolDefs skip them.
func (n *Network) rebuildCachedToolDefsLocked() {
	n.SetCachedToolDefs(n.CacheBuiltinToolDefs(n.buildToolDefsLocked(n.Tools().AllDefinitions()), nil, nil))
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
	toolDefs, executeTool, executeToolStream, isStreamingTool := n.ResolveTools(ctx, task, n.buildToolDefs, nil, nil)
	return n.BaseLoopConfig("network:"+n.Name(), prompt, provider, toolDefs, n.makeDispatch(task, ch, executeTool, executeToolStream, toolDefs, isStreamingTool, cfg), cfg, n.ResolveMem(opts))
}

// makeDispatch returns a DispatchFunc that routes tool calls to subagents,
// the shared built-in tools, or direct tools. When ch is non-nil, agent-start
// and agent-finish events are emitted for subagent delegation. Tools
// implementing StreamingAnyTool emit progress events via executeToolStream.
// Tool policies registered via WithRouter(agent.WithToolConfig(...)) are
// honoured via cfg.ResolveToolPolicy.
func (n *Network) makeDispatch(parentTask agent.AgentTask, ch chan<- core.StreamEvent, executeTool agent.ToolExecFunc, executeToolStream agent.ToolExecStreamFunc, resolvedToolDefs []core.ToolDefinition, isStreamingTool func(string) bool, cfg *agent.Config) agent.DispatchFunc {
	agentRouter := func(ctx context.Context, tc core.ToolCall) (agent.DispatchResult, bool) {
		if tc.Name == core.ToolSpawnAgent {
			if n.spawnPolicy == nil {
				return agent.DispatchResult{Content: "error: spawn_agent invoked without WithDynamicSpawning", IsError: true}, true
			}
			return n.dispatchSpawn(ctx, tc.Args), true
		}
		if !strings.HasPrefix(tc.Name, core.ToolPrefixAgent) {
			return agent.DispatchResult{}, false
		}
		return n.dispatchAgent(ctx, tc, parentTask, ch), true
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
		ResolvePolicy:     cfg.ResolveToolPolicy,
		IsStreamingTool:   isStreamingTool,
		Logger:            cfg.Logger,
	})
}

// dispatchAgent handles delegation to a subagent. Emits agent-start/finish
// streaming events when ch is non-nil.
func (n *Network) dispatchAgent(ctx context.Context, tc core.ToolCall, parentTask agent.AgentTask, ch chan<- core.StreamEvent) agent.DispatchResult {
	agentName := tc.Name[len(core.ToolPrefixAgent):]
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

	// Struct assignment forwards all current and future AgentTask fields;
	// only Input is overridden with the sub-agent's specific task.
	subTask := parentTask
	subTask.Input = params.Task

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
//
// Public entry point: takes the read lock. Used as the prebuild callback by
// the runtime's dynamic ResolveTools path.
func (n *Network) buildToolDefs(toolDefs []core.ToolDefinition) []core.ToolDefinition {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.buildToolDefsLocked(toolDefs)
}

// buildToolDefsLocked is the lock-free body of buildToolDefs. Caller must
// hold n.mu (read or write). Lets membership-mutating paths (AddAgent,
// RemoveAgent, dispatchSpawn) rebuild the tool defs under the write lock
// without re-acquiring RLock and deadlocking.
func (n *Network) buildToolDefsLocked(toolDefs []core.ToolDefinition) []core.ToolDefinition {
	defs := make([]core.ToolDefinition, 0, len(n.sortedAgentNames)+len(toolDefs)+1)
	for _, name := range n.sortedAgentNames {
		defs = append(defs, core.ToolDefinition{
			Name:        core.ToolPrefixAgent + name,
			Description: n.agents[name].Description(),
			Parameters:  agentToolParamSchema,
		})
	}
	if n.spawnPolicy != nil {
		defs = append(defs, core.ToolDefinition{
			Name:        core.ToolSpawnAgent,
			Description: "Dynamically create a new sub-agent to handle a specialized task. Provide name (unique), description, and system prompt.",
			Parameters:  spawnAgentParamSchema,
		})
	}
	defs = append(defs, toolDefs...)
	return defs
}

// compile-time checks
var _ core.Agent = (*Network)(nil)
