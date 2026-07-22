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
//
// The task is a full, self-contained assignment written by the router — NOT a
// verbatim copy of the user's message. The subagent cannot see the parent
// conversation, so every fact it needs must be inlined here (mirrors the
// deepagents task(description) contract).
var agentToolParamSchema = json.RawMessage(
	`{"type":"object","properties":{"task":{"type":"string","description":"The complete, self-contained assignment for the subagent. The subagent CANNOT see this conversation: include every fact, constraint, and piece of prior context it needs, plus what its final report must contain. Preserve the user's language and exact figures."}},"required":["task"]}`,
)

// Option configures a Network. Pass Option values to network.New.
// Use WithAgentOptions to forward agent.AgentOption values to the router LLM.
type Option func(*Network)

// WithAgentOptions applies agent-level options (prompt, tools, memory, sandbox,
// etc.) to the network's internal routing agent.
//
//	net := network.New("team", "...", routerP,
//	    network.WithChildren(a, b),
//	    network.WithAgentOptions(agent.WithTracer(t), agent.WithMemory(...)),
//	)
func WithAgentOptions(opts ...agent.AgentOption) Option {
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

// WithChildTimeout bounds every delegation to a child agent. When d > 0 each
// dispatchAgent call runs the child under context.WithTimeout(ctx, d); on
// expiry that delegation fails with an "error: ..." tool result while sibling
// delegations keep running, so a hung child cannot stall the router forever.
// Zero (the default) means no per-delegation timeout.
func WithChildTimeout(d time.Duration) Option {
	return func(n *Network) { n.childTimeout = d }
}

// delegationToolDescription is the LLM-facing description of an agent_<name>
// tool. It wraps the child's own description with the delegation contract
// (blocking call, isolated context, parallel batching) so the router does not
// need prompt-side guidance to use the tool correctly.
func delegationToolDescription(name, desc string) string {
	return "Delegate a task to the '" + name + "' subagent. " + desc +
		" The call blocks until the subagent finishes and returns its final report as the tool result" +
		` (a result starting with "error: " means it failed — retry once or report the failure; never silently redo its work yourself).` +
		" The subagent cannot see this conversation, so the task must be fully self-contained." +
		" To run independent delegations in parallel, issue all of them together in a single turn." +
		" Never re-delegate a task that already returned a result."
}

// delegationLedger tracks delegations within one Execute run so the router
// cannot run the same (agent, task) twice: a repeat while the first call is
// in flight is rejected, and a repeat after completion replays the cached
// result. Failed delegations are evicted so the router may retry them.
type delegationLedger struct {
	mu   sync.Mutex
	recs map[string]*delegationRecord
}

type delegationRecord struct {
	done   bool
	output string
}

// Network is an Agent that coordinates subagents and tools via an LLM router.
// The router sees subagents as callable tools ("agent_<name>") and decides
// which primitives to invoke, in what order, and with what data.
// Optionally supports conversation memory, user memory, and cross-thread search
// when configured via WithConversationMemory, CrossThreadSearch, and WithUserMemory.
type Network struct {
	runtime.Runtime
	mu               sync.RWMutex           // guards agents + sortedAgentNames + toolDefsDirty + cachedBuildDefs
	agents           map[string]agent.Agent // keyed by name
	sortedAgentNames []string               // pre-sorted for deterministic tool ordering

	// toolDefsDirty is set when membership changes (AddAgent/RemoveAgent).
	// buildToolDefs checks this under mu and skips the allocation when clean.
	// cachedBuildDefs holds the last result of buildToolDefsLocked; non-nil only
	// after the first buildToolDefs call on the dynamic-tools path.
	toolDefsDirty   bool
	cachedBuildDefs []core.ToolDefinition

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

	// childTimeout, when > 0, bounds each delegation to a child agent.
	// Set via WithChildTimeout.
	childTimeout time.Duration
}

// New constructs a Network — a router LLM coordinating zero or more child
// agents. All configuration (children, supervisor, dynamic spawning, router
// options) flows through Options.
//
// New panics if two agents share a name.
//
//	net := network.New("coordinator", "...", routerP,
//	    network.WithChildren(searchAgent, summarizeAgent),
//	    network.WithAgentOptions(agent.WithTracer(t)),
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

	// Build router's Config from any WithAgentOptions-supplied opts, then init runtime.
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
	if n.SelfCloneMax > 0 {
		// Per-run spawn budget for the router's spawn_subagent built-in.
		ctx = agent.WithCloneScope(ctx)
	}
	return n.ExecuteWithSpan(ctx, task, rcfg.Stream, "Network", "network",
		func(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent) *agent.LoopConfig {
			return n.buildLoopConfig(ctx, task, ch, ro)
		},
		agent.RunLoop,
	)
}

// buildLoopConfig wires Network fields into a LoopConfig for runLoop.
// Used by both Execute / ExecuteStream (opts = nil) and
// ExecuteWith / ExecuteStreamWith (opts != nil). Resolves dynamic prompt,
// model, and tools, and applies RunOptions overrides to the router config.
func (n *Network) buildLoopConfig(ctx context.Context, task agent.AgentTask, ch chan<- core.StreamEvent, opts *agent.RunOptions) *agent.LoopConfig {
	cfg := n.ApplyRunOptions(opts)
	prompt, provider := n.ResolvePromptAndProviderWith(ctx, task, cfg)
	// Network does not use ask_user, execute_plan, or spawn_agent builtins.
	toolDefs, executeTool, executeToolStream, isStreamingTool := n.ResolveTools(ctx, task, n.buildToolDefs, nil, nil)
	lc := runtime.AcquireLoopConfig()
	*lc = n.BaseLoopConfig("network:"+n.Name(), prompt, provider, toolDefs, n.makeDispatch(task, ch, executeTool, executeToolStream, toolDefs, isStreamingTool, cfg, provider), cfg, n.ResolveMem(opts))
	return lc
}

// makeDispatch returns a DispatchFunc that routes tool calls to subagents,
// the shared built-in tools, or direct tools. When ch is non-nil, agent-start
// and agent-finish events are emitted for subagent delegation. Tools
// implementing StreamingAnyTool emit progress events via executeToolStream.
// Tool policies registered via WithRouter(agent.WithToolConfig(...)) are
// honoured via cfg.ResolveToolPolicy.
func (n *Network) makeDispatch(parentTask agent.AgentTask, ch chan<- core.StreamEvent, executeTool agent.ToolExecFunc, executeToolStream agent.ToolExecStreamFunc, resolvedToolDefs []core.ToolDefinition, isStreamingTool func(string) bool, cfg *agent.Config, provider core.Provider) agent.DispatchFunc {
	// One ledger per Execute run: makeDispatch is called from buildLoopConfig
	// on every Execute, so the dedup scope is exactly one routing loop.
	ledger := &delegationLedger{recs: make(map[string]*delegationRecord)}
	agentRouter := func(ctx context.Context, tc core.ToolCall) (agent.DispatchResult, bool) {
		if tc.Name == core.ToolSpawnAgent {
			if n.spawnPolicy == nil {
				return agent.DispatchResult{Content: "error: spawn_agent invoked without WithDynamicSpawning", IsError: true}, true
			}
			return n.dispatchSpawn(ctx, tc.Args), true
		}
		if tc.Name == core.ToolTask || tc.Name == core.ToolSelfClone {
			return n.dispatchTask(ctx, tc, parentTask, ch, ledger, cfg, provider), true
		}
		if !strings.HasPrefix(tc.Name, core.ToolPrefixAgent) {
			return agent.DispatchResult{}, false
		}
		// Legacy agent_<name> call shape — still dispatched, no longer
		// advertised (the unified task tool covers the roster).
		agentName := tc.Name[len(core.ToolPrefixAgent):]
		var params struct {
			Task string `json:"task"`
		}
		if err := json.Unmarshal(tc.Args, &params); err != nil {
			return agent.DispatchResult{Content: "error: invalid agent call args: " + err.Error(), IsError: true}, true
		}
		return n.dispatchAgent(ctx, agentName, params.Task, parentTask, ch, ledger), true
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

// dispatchTask routes one unified task tool call (or its legacy
// spawn_subagent alias): "self" spawns a clone of the router; any roster name
// delegates to that child; anything else errors with the valid targets.
func (n *Network) dispatchTask(ctx context.Context, tc core.ToolCall, parentTask agent.AgentTask, ch chan<- core.StreamEvent, ledger *delegationLedger, cfg *agent.Config, provider core.Provider) agent.DispatchResult {
	var args agent.TaskToolArgs
	if err := json.Unmarshal(tc.Args, &args); err != nil {
		return agent.DispatchResult{Content: "error: invalid " + tc.Name + " args: " + err.Error(), IsError: true}
	}
	if args.Task == "" {
		return agent.DispatchResult{Content: "error: " + tc.Name + " requires a non-empty task", IsError: true}
	}
	// Legacy spawn_subagent carries no subagent field — it always meant self.
	if args.Subagent == "" && tc.Name == core.ToolSelfClone {
		args.Subagent = agent.TaskSelf
	}
	if args.Subagent == agent.TaskSelf {
		if n.SelfCloneMax <= 0 {
			return agent.DispatchResult{Content: "error: subagent \"self\" is not available on this agent", IsError: true}
		}
		// The clone is a plain LLMAgent built from the router's own config —
		// it inherits the router's prompt and direct tools PLUS the router's
		// delegation surface: its task tool advertises the same roster, and a
		// call to a roster name routes back through this network's dispatch
		// (shared ledger, child timeout, stream events). Without this, a
		// clone whose inherited coordinator prompt says "route research to X"
		// could only fail with "unknown subagent" and grind through the work
		// itself. Clones still cannot spawn further clones.
		cloneCfg := *cfg
		cloneCfg.TaskRoster = n.taskRoster()
		cloneCfg.TaskDelegate = func(ctx context.Context, subagent, taskText string, cch chan<- core.StreamEvent) agent.DispatchResult {
			return n.dispatchAgent(ctx, subagent, taskText, parentTask, cch, ledger)
		}
		return agent.ExecuteSelfClone(ctx, n.Name(), n.Description(), provider, &cloneCfg, args.Task, ch, n.Logger())
	}
	return n.dispatchAgent(ctx, args.Subagent, args.Task, parentTask, ch, ledger)
}

// taskRoster snapshots the current roster as task-tool targets — the
// delegation surface handed to the router's self-clones.
func (n *Network) taskRoster() []agent.TaskTarget {
	n.mu.RLock()
	defer n.mu.RUnlock()
	targets := make([]agent.TaskTarget, 0, len(n.sortedAgentNames))
	for _, name := range n.sortedAgentNames {
		targets = append(targets, agent.TaskTarget{Name: name, Description: n.agents[name].Description()})
	}
	return targets
}

// dispatchAgent handles delegation to a named child. Emits agent-start/finish
// streaming events when ch is non-nil; the finish event carries IsError and
// the "error: ..." text when the child failed. The ledger rejects duplicate
// in-flight delegations and replays completed ones instead of re-executing.
func (n *Network) dispatchAgent(ctx context.Context, agentName, taskText string, parentTask agent.AgentTask, ch chan<- core.StreamEvent, ledger *delegationLedger) agent.DispatchResult {
	n.mu.RLock()
	sub, ok := n.agents[agentName]
	names := make([]string, len(n.sortedAgentNames))
	copy(names, n.sortedAgentNames)
	n.mu.RUnlock()
	if !ok {
		valid := strings.Join(names, ", ")
		if n.SelfCloneMax > 0 {
			valid += ", self"
		}
		return agent.DispatchResult{Content: fmt.Sprintf("error: unknown subagent %q — valid: %s", agentName, valid), IsError: true}
	}

	params := struct{ Task string }{Task: taskText}

	// Duplicate-delegation guard: identical (agent, task) within one run.
	ledgerKey := agentName + "\x00" + params.Task
	if ledger != nil {
		ledger.mu.Lock()
		if rec, exists := ledger.recs[ledgerKey]; exists {
			if !rec.done {
				ledger.mu.Unlock()
				return agent.DispatchResult{
					Content: fmt.Sprintf("error: duplicate delegation — %q is already running this exact task; wait for its result instead of re-delegating", agentName),
					IsError: true,
				}
			}
			output := rec.output
			ledger.mu.Unlock()
			n.Logger().Info("replaying completed delegation", "network", n.Name(), "agent", agentName)
			return agent.DispatchResult{
				Content: fmt.Sprintf("note: %q already completed this exact task earlier in this run. Its result is repeated below — do not delegate it again.\n\n%s", agentName, output),
			}
		}
		ledger.recs[ledgerKey] = &delegationRecord{}
		ledger.mu.Unlock()
	}
	// settle finalizes the ledger entry: successes are cached for replay,
	// failures are evicted so the router can retry the same task.
	settle := func(output string, failed bool) {
		if ledger == nil {
			return
		}
		ledger.mu.Lock()
		if failed {
			delete(ledger.recs, ledgerKey)
		} else if rec := ledger.recs[ledgerKey]; rec != nil {
			rec.done = true
			rec.output = output
		}
		ledger.mu.Unlock()
	}

	n.Logger().Info("delegating to subagent", "network", n.Name(), "agent", agentName, "task", agent.TruncateStr(params.Task, 80))

	if ch != nil {
		select {
		case ch <- core.StreamEvent{Type: core.EventAgentStart, Name: agentName, Content: params.Task}:
		case <-ctx.Done():
			settle("", true)
			return agent.DispatchResult{Content: ctx.Err().Error(), IsError: true}
		}
	}

	// Struct assignment forwards all current and future AgentTask fields;
	// only Input is overridden with the sub-agent's specific task.
	subTask := parentTask
	subTask.Input = params.Task

	// Per-delegation timeout: a hung child fails this one delegation instead
	// of stalling the whole routing loop.
	execCtx := ctx
	if n.childTimeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, n.childTimeout)
		defer cancel()
	}

	start := time.Now()
	result, err := agent.ExecuteAgent(execCtx, sub, agentName, subTask, ch, n.Logger())
	elapsed := time.Since(start)
	if err != nil && ctx.Err() == nil && execCtx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("subagent %q timed out after %s: %w", agentName, n.childTimeout, err)
	}

	if ch != nil {
		output := result.Output
		if err != nil {
			output = "error: " + err.Error()
		}
		select {
		case ch <- core.StreamEvent{
			Type:     core.EventAgentFinish,
			Name:     agentName,
			Content:  output,
			Usage:    result.Usage,
			Duration: elapsed,
			IsError:  err != nil,
		}:
		case <-ctx.Done():
		}
	}

	if err != nil {
		settle("", true)
		n.Logger().Error("subagent failed", "network", n.Name(), "agent", agentName, "error", err, "duration", elapsed)
		return agent.DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	settle(result.Output, false)
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
//
// When membership is stable (!toolDefsDirty) and a cached result exists, the
// cached slice is returned directly to avoid allocating on every Execute call.
// When the membership changes (AddAgent/RemoveAgent), toolDefsDirty is set and
// the next call rebuilds and re-caches under the write lock.
func (n *Network) buildToolDefs(toolDefs []core.ToolDefinition) []core.ToolDefinition {
	n.mu.RLock()
	if !n.toolDefsDirty && n.cachedBuildDefs != nil {
		cached := n.cachedBuildDefs
		n.mu.RUnlock()
		return cached
	}
	n.mu.RUnlock()

	n.mu.Lock()
	defer n.mu.Unlock()
	// Re-check under write lock: another goroutine may have rebuilt while we
	// waited for the lock.
	if !n.toolDefsDirty && n.cachedBuildDefs != nil {
		return n.cachedBuildDefs
	}
	result := n.buildToolDefsLocked(toolDefs)
	n.cachedBuildDefs = result
	n.toolDefsDirty = false
	return result
}

// buildToolDefsLocked is the lock-free body of buildToolDefs. Caller must
// hold n.mu (read or write). Lets membership-mutating paths (AddAgent,
// RemoveAgent, dispatchSpawn) rebuild the tool defs under the write lock
// without re-acquiring RLock and deadlocking.
func (n *Network) buildToolDefsLocked(toolDefs []core.ToolDefinition) []core.ToolDefinition {
	defs := make([]core.ToolDefinition, 0, len(toolDefs)+2)
	// ONE unified task tool covers the whole roster (and "self" when
	// self-cloning is enabled) — deepagents' task(description, subagent_type)
	// shape — instead of one agent_<name> tool per child. Legacy agent_* and
	// spawn_subagent calls still dispatch for compatibility; they are just no
	// longer advertised.
	if len(n.sortedAgentNames) > 0 || n.SelfCloneMax > 0 {
		targets := make([]agent.TaskTarget, 0, len(n.sortedAgentNames))
		for _, name := range n.sortedAgentNames {
			targets = append(targets, agent.TaskTarget{Name: name, Description: n.agents[name].Description()})
		}
		defs = append(defs, agent.BuildTaskToolDef(targets, n.SelfCloneMax > 0, n.SelfCloneMax))
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
