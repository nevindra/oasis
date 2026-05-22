package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
	"github.com/nevindra/oasis/skills"
)

const defaultMaxIter = 25

// AgentCore holds fields shared by LLMAgent and Network.
// Both types embed this struct to eliminate field duplication.
// New agent-level options only need to be wired here once.
// This is an internal type; do not depend on stability.
//
// Config is embedded so that all option-set fields (systemPrompt, genParams,
// spawnDepthLimit, etc.) promote directly onto AgentCore without a copy step.
// The fields below are runtime-only: they are either computed during InitCore
// (name, description, provider, tools, processors, mem, cachedToolDefs,
// activeSkillInstructions, maxSteps, compressor) or mutated during execution
// (suspendCount, suspendBytes, suspendMu) and therefore cannot live in Config.
//
// Why compressor is separate from Config.compactor:
//   Config.compactor is the per-thread compactor (history compaction — reduces
//   old conversation turns). AgentCore.compressor is the per-turn tool-result
//   compressor (reduces oversized tool output mid-loop). Both satisfy the same
//   Compactor interface, but they serve different purposes and are set on
//   different lifecycles. InitCore copies cfg.compactor → c.compressor at build
//   time; the runtime uses c.compressor directly.
//
// Why maxSteps is separate from Config.maxSteps (*int):
//   Config.maxSteps is a pointer so BuildConfig can distinguish "explicitly set
//   to 0 (unbounded)" from "not set (default 100)". Once InitCore dereferences
//   it into AgentCore.maxSteps (value int), that distinction is no longer needed
//   and pointer indirection in the hot loop is avoided.
type AgentCore struct {
	Config // embedded — option-set fields promote up

	// Identity and runtime-wired dependencies (passed into InitCore; not in Config).
	name        string
	description string
	provider    Provider

	// Infrastructure built during InitCore (not option fields).
	tools      *ToolRegistry
	processors *ProcessorChain
	mem        memory.AgentMemory

	// Cached / computed at build time.
	cachedToolDefs          []ToolDefinition
	activeSkillInstructions string
	maxSteps                int // dereferenced from *Config.maxSteps in InitCore

	// Suspension counters — mutated during execution, guarded by suspendMu.
	// Live here (not Config) because they are runtime state, not configuration.
	suspendCount int64
	suspendBytes int64
	suspendMu    sync.Mutex

	// Why compressor is separate: see type comment above.
	compressor Compactor
}

// InitCore initializes shared fields on an AgentCore from the given config.
// Called by NewLLMAgent and NewNetwork on the already-allocated parent struct.
// Uses field-by-field assignment to avoid copying sync primitives in agentMemory.
//
// Field copies are intentionally minimal: Config is embedded in AgentCore so
// most option-set fields promote directly. Only runtime-computed or
// runtime-mutated fields need explicit assignment here.
func InitCore(c *AgentCore, name, description string, provider Provider, cfg *Config) {
	c.Config = *cfg
	c.name = name
	c.description = description
	c.provider = provider
	c.tools = NewToolRegistry()
	c.processors = NewProcessorChain()

	// Default maxIter when not explicitly set.
	if c.maxIter == 0 {
		c.maxIter = defaultMaxIter
	}

	// Dereference *Config.maxSteps into a plain int for the runtime.
	// See AgentCore type comment for why these are kept separate.
	c.maxSteps = *cfg.maxSteps

	// Alias: Config.compactor (per-thread history compaction) is reused as
	// AgentCore.compressor (per-turn tool-result compression). They serve
	// different purposes but share the same interface. See AgentCore type comment.
	c.compressor = cfg.compactor

	// Wire memory fields via Init to avoid accessing unexported fields across packages.
	c.mem.Init(memory.AgentMemoryConfig{
		Store:             cfg.store,
		Embedding:         cfg.embedding,
		Memory:            cfg.memory,
		CrossThreadSearch: cfg.crossThreadSearch,
		SemanticMinScore:  cfg.semanticMinScore,
		MaxHistory:        cfg.maxHistory,
		MaxTokens:         cfg.maxTokens,
		AutoTitle:         cfg.autoTitle,
		Provider:          provider,
		SemanticTrimming:  cfg.semanticTrimming,
		TrimmingEmbedding: cfg.trimmingEmbedding,
		KeepRecent:        cfg.keepRecent,
		Tracer:            cfg.tracer,
		Logger:            cfg.logger,
	})

	// Compute effective middleware chain: user-provided + auto-OTel when
	// a tracer is configured and the user hasn't already included one.
	effectiveMiddleware := cfg.toolMiddleware
	if cfg.tracer != nil && !hasOTelSpanMiddleware(effectiveMiddleware) {
		effectiveMiddleware = append(effectiveMiddleware, OTelSpanMiddleware(cfg.tracer))
	}

	// Approval middlewares are outermost — retries inside ToolPolicy do not
	// re-prompt the human, since the policy wrap (if any) sits inside.
	for _, ac := range cfg.toolApprovals {
		effectiveMiddleware = append(effectiveMiddleware, approvalMiddleware(ac, cfg.inputHandler))
	}

	for _, t := range cfg.tools {
		c.tools.Add(core.ApplyToolMiddleware(t, effectiveMiddleware))
	}

	// Register sandbox tools when a sandbox is configured.
	if cfg.sandbox != nil {
		for _, t := range cfg.sandboxTools {
			c.tools.Add(core.ApplyToolMiddleware(t, effectiveMiddleware))
		}
	}

	// Register skill tools when a skill provider is configured.
	if cfg.skillProvider != nil {
		for _, t := range skills.NewSkillTools(cfg.skillProvider) {
			c.tools.Add(core.ApplyToolMiddleware(t, effectiveMiddleware))
		}
	}

	for _, p := range cfg.preProcessors {
		c.processors.AddPre(p)
	}
	for _, p := range cfg.postProcessors {
		c.processors.AddPost(p)
	}
	for _, p := range cfg.postToolProcessors {
		c.processors.AddPostTool(p)
	}

	// Build active skill instructions block.
	if len(cfg.activeSkills) > 0 {
		var parts []string
		for _, s := range cfg.activeSkills {
			parts = append(parts, "## Skill: "+s.Name+"\n\n"+s.Instructions)
		}
		c.activeSkillInstructions = strings.Join(parts, "\n\n---\n\n")
	}
}

func (c *AgentCore) Name() string        { return c.name }
func (c *AgentCore) Description() string { return c.description }

// Tools returns the agent's tool registry.
// Used by network/ for Execute, ExecuteStream, AllDefinitions, and Add calls.
func (c *AgentCore) Tools() *ToolRegistry { return c.tools }

// ActiveSkillInstructions returns the compiled skill instructions block.
// Used by network/ to append skill instructions to the system prompt.
func (c *AgentCore) ActiveSkillInstructions() string { return c.activeSkillInstructions }

// Logger returns the agent's structured logger.
// Used by network/ for delegation event logging.
func (c *AgentCore) Logger() *slog.Logger { return c.logger }

// CachedToolDefs returns the pre-computed tool definition slice for the
// non-dynamic path. Used by network/ at construction time.
func (c *AgentCore) CachedToolDefs() []ToolDefinition { return c.cachedToolDefs }

// SetCachedToolDefs replaces the cached tool definitions.
// Used by network/ at construction time to prime the static-path cache.
func (c *AgentCore) SetCachedToolDefs(defs []ToolDefinition) { c.cachedToolDefs = defs }

// Close waits for all in-flight background persist goroutines to finish and
// releases any memory orchestrator resources. Call during shutdown to ensure
// the last messages are written to the store.
//
// Returns nil today; reserved for future flush errors. Embedders that wrap
// AgentCore inherit this signature.
func (c *AgentCore) Close() error { return c.mem.Close() }

// Generation returns a copy of this agent's current Generation parameters.
// Mutating the returned struct does not affect the agent. Use this to
// construct a RunOptions.Generation override that partially modifies the
// agent default:
//
//	gen := a.Generation()
//	*gen.Temperature = 0.9
//	result, err := a.ExecuteWith(ctx, task, &agent.RunOptions{Generation: &gen})
func (c *AgentCore) Generation() Generation {
	p := c.genParams
	if p == nil {
		return Generation{}
	}
	var g Generation
	if p.Temperature != nil {
		v := *p.Temperature
		g.Temperature = &v
	}
	if p.TopP != nil {
		v := *p.TopP
		g.TopP = &v
	}
	if p.TopK != nil {
		v := *p.TopK
		g.TopK = &v
	}
	if p.MaxTokens != nil {
		v := *p.MaxTokens
		g.MaxTokens = &v
	}
	return g
}

// Limits returns a copy of this agent's current resource budgets. Mutating
// the returned struct does not affect the agent. Use this to construct a
// RunOptions.Limits override that partially modifies the agent default:
//
//	lim := a.Limits()
//	lim.MaxIter = 5
//	result, err := a.ExecuteWith(ctx, task, &agent.RunOptions{Limits: &lim})
func (c *AgentCore) Limits() Limits {
	maxSteps := c.maxSteps
	if maxSteps == 0 {
		maxSteps = Unbounded
	}
	return Limits{
		MaxIter:             c.maxIter,
		MaxSteps:            maxSteps,
		MaxPlanSteps:        c.maxPlanSteps,
		MaxParallelDispatch: c.maxParallelDispatch,
		MaxAttachmentBytes:  c.maxAttachmentBytes,
		MaxToolResultLen:    c.maxToolResultLen,
		MaxSuspendSnapshots: c.maxSuspendSnapshots,
		MaxSuspendBytes:     c.maxSuspendBytes,
	}
}

// --- Spawn depth tracking ---

// spawnDepthKey is the context key for sub-agent nesting depth.
type spawnDepthKey struct{}

// spawnDepth returns the current sub-agent nesting depth from ctx.
// Returns 0 at the top level (no spawning has occurred).
func spawnDepth(ctx context.Context) int {
	if v, ok := ctx.Value(spawnDepthKey{}).(int); ok {
		return v
	}
	return 0
}

// withSpawnDepth returns a child context with the given spawn depth.
func withSpawnDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, spawnDepthKey{}, depth)
}

// CacheBuiltinToolDefs appends built-in tool definitions (ask_user, execute_plan)
// based on the agent's configuration.
func (c *AgentCore) CacheBuiltinToolDefs(defs []ToolDefinition) []ToolDefinition {
	if c.inputHandler != nil {
		defs = append(defs, askUserToolDef())
	}
	if c.planExecution {
		defs = append(defs, executePlanToolDef())
	}
	if c.spawnEnabled {
		defs = append(defs, spawnAgentToolDef())
	}
	return defs
}

// ResolvePromptAndProvider returns the effective prompt and provider for this request.
// Dynamic overrides take precedence over construction-time values.
func (c *AgentCore) ResolvePromptAndProvider(ctx context.Context, task AgentTask) (string, Provider) {
	return c.ResolvePromptAndProviderWith(ctx, task, &c.Config)
}

// ResolvePromptAndProviderWith is the per-call variant of ResolvePromptAndProvider.
// When cfg is the result of applyRunOptions over the agent's base Config, an
// explicit Prompt override in RunOptions wins over the dynamicPrompt
// resolver; without an override, the dynamic resolver is used as usual.
// Skill instructions are appended to whichever prompt wins.
func (c *AgentCore) ResolvePromptAndProviderWith(ctx context.Context, task AgentTask, cfg *Config) (string, Provider) {
	prompt := cfg.systemPrompt
	// Dynamic prompt applies only when there's no per-call override.
	if cfg.systemPrompt == c.systemPrompt && c.dynamicPrompt != nil {
		prompt = c.dynamicPrompt(ctx, task)
	}
	p := c.provider
	if c.dynamicModel != nil {
		p = c.dynamicModel(ctx, task)
	}
	if c.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + c.activeSkillInstructions
	}
	return prompt, p
}

// ApplyRunOptions returns a Config snapshot with opts merged onto the agent's
// base Config. Returns &c.Config unchanged when opts is nil or has no
// overrides. Exposed for external subpackages (e.g. network) that need to
// share the same RunOptions plumbing as LLMAgent.
func (c *AgentCore) ApplyRunOptions(opts *RunOptions) *Config {
	return applyRunOptions(&c.Config, opts)
}

// ResolveMem returns the memory orchestrator for a request. opts.Memory wins
// when set, otherwise the agent's construction-time memory.
func (c *AgentCore) ResolveMem(opts *RunOptions) *memory.AgentMemory {
	if opts != nil && opts.Memory != nil {
		return opts.Memory
	}
	return &c.mem
}

// ResolveDynamicTools returns tool definitions and an executor for a dynamic request.
// Returns nil, nil when dynamicTools is not configured (caller should use cached defs).
func (c *AgentCore) ResolveDynamicTools(ctx context.Context, task AgentTask) ([]ToolDefinition, ToolExecFunc) {
	if c.dynamicTools == nil {
		return nil, nil
	}
	dynTools := c.dynamicTools(ctx, task)
	var toolDefs []ToolDefinition
	index := make(map[string]AnyTool, len(dynTools))
	for _, t := range dynTools {
		toolDefs = append(toolDefs, t.Definition())
		index[t.Name()] = t
	}
	executeTool := func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
		if t, ok := index[name]; ok {
			return t.ExecuteRaw(ctx, args)
		}
		return ToolResult{Error: "unknown tool: " + name}, nil
	}
	return toolDefs, executeTool
}

// ResolveTools returns the tool definitions and executors for an iteration.
// Picks the dynamic path when WithDynamicTools is configured, otherwise the
// cached static path. prebuild, if non-nil, transforms the dynamic-tool
// definitions before built-ins are appended — Network uses this to inject
// agent_* delegation tool defs. Streaming-tool detection is reported via
// isStream (always false on the dynamic path; agent's registry lookup on the
// static path).
func (c *AgentCore) ResolveTools(
	ctx context.Context,
	task AgentTask,
	prebuild func([]ToolDefinition) []ToolDefinition,
) (defs []ToolDefinition, exec ToolExecFunc, execStream ToolExecStreamFunc, isStream func(string) bool) {
	if dynDefs, dynExec := c.ResolveDynamicTools(ctx, task); dynDefs != nil {
		c.logger.Debug("using dynamic tools", "agent", c.name, "tool_count", len(dynDefs))
		if prebuild != nil {
			dynDefs = prebuild(dynDefs)
		}
		return c.CacheBuiltinToolDefs(dynDefs), dynExec, nil, func(string) bool { return false }
	}
	return c.cachedToolDefs, c.tools.Execute, c.tools.ExecuteStream, c.tools.IsStreamingTool
}

// BaseLoopConfig assembles a LoopConfig from resolved values.
// Callers provide the name prefix, resolved prompt/provider/tools, dispatch,
// the effective Config (typically &c.Config, or the result of ApplyRunOptions
// for per-call overrides), and the memory orchestrator (typically &c.mem, or
// opts.Memory). Non-overridable runtime state (suspend counters, compressor,
// processors, tool lookup) is sourced from c regardless of cfg.
func (c *AgentCore) BaseLoopConfig(
	name, prompt string,
	provider Provider,
	tools []ToolDefinition,
	dispatch DispatchFunc,
	cfg *Config,
	mem *memory.AgentMemory,
) LoopConfig {
	maxSteps := 0
	if cfg.maxSteps != nil {
		maxSteps = *cfg.maxSteps
	}
	return LoopConfig{
		// identity / per-call wiring
		name:         name,
		provider:     provider,
		tools:        tools,
		dispatch:     dispatch,
		systemPrompt: prompt,
		// overridable config fields
		maxIter:             cfg.maxIter,
		inputHandler:        cfg.inputHandler,
		responseSchema:      cfg.responseSchema,
		tracer:              cfg.tracer,
		logger:              cfg.logger,
		maxAttachmentBytes:  cfg.maxAttachmentBytes,
		maxSuspendSnapshots: cfg.maxSuspendSnapshots,
		maxSuspendBytes:     cfg.maxSuspendBytes,
		compressModel:       cfg.compressModel,
		compressThreshold:   cfg.compressThreshold,
		generationParams:    cfg.genParams,
		maxParallelDispatch: cfg.maxParallelDispatch,
		maxToolResultLen:    cfg.maxToolResultLen,
		maxPlanSteps:        cfg.maxPlanSteps,
		toolResultStore:     cfg.toolResultStore,
		maxSteps:            maxSteps,
		prepareStep:         cfg.prepareStep,
		onError:             cfg.onError,
		onIterationComplete: cfg.onIterationComplete,
		// non-overridable runtime state
		processors:   c.processors,
		mem:          mem,
		suspendCount: &c.suspendCount,
		suspendBytes: &c.suspendBytes,
		suspendMu:    &c.suspendMu,
		compressor:   c.compressor,
		lookupTool:   c.tools.Lookup,
	}
}

// ExecuteWithSpan wraps runLoop with tracing, logging, and input-received event emission.
// agentType is used in span attributes ("LLMAgent" or "Network").
// logKey is used in log messages ("agent" or "network").
// buildCfg constructs the LoopConfig; it receives the (possibly span-wrapped) context.
func (c *AgentCore) ExecuteWithSpan(
	ctx context.Context,
	task AgentTask,
	ch chan<- StreamEvent,
	agentType, logKey string,
	buildCfg func(ctx context.Context, task AgentTask, ch chan<- StreamEvent) LoopConfig,
) (AgentResult, error) {
	// Emit run-start as the first event on every stream. Replaces the
	// deprecated EventInputReceived + EventProcessingStart pair.
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventRunStart, Name: c.name, Content: task.Input}:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}

	var span Span
	if c.tracer != nil {
		ctx, span = c.tracer.Start(ctx, "agent.execute",
			StringAttr("agent.name", c.name),
			StringAttr("agent.type", agentType))
		defer span.End()
	}

	c.logger.Info(logKey+" started", logKey, c.name)
	start := time.Now()
	result, err := runLoop(ctx, buildCfg(ctx, task, ch), task, ch)

	if span != nil {
		span.SetAttr(
			IntAttr("tokens.input", result.Usage.InputTokens),
			IntAttr("tokens.output", result.Usage.OutputTokens))
		if err != nil {
			span.Error(err)
			span.SetAttr(StringAttr("agent.status", "error"))
		} else {
			span.SetAttr(StringAttr("agent.status", "ok"))
		}
	}

	if err != nil {
		c.logger.Error(logKey+" failed", logKey, c.name,
			"error", err,
			"duration", time.Since(start),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens)
	} else {
		c.logger.Info(logKey+" completed", logKey, c.name,
			"duration", time.Since(start),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens,
			"steps", len(result.Steps))
	}
	return result, err
}

// --- Shared agent dispatch helpers ---

// ExecuteAgent runs a subagent with panic recovery. When ch is non-nil and
// the agent implements StreamingAgent, events are forwarded through the parent
// channel in real time via forwardSubagentStream.
func ExecuteAgent(ctx context.Context, agent Agent, agentName string, task AgentTask, ch chan<- StreamEvent, logger *slog.Logger) (result AgentResult, err error) {
	if ch != nil {
		if sa, ok := agent.(StreamingAgent); ok {
			logger.Debug("executing subagent (streaming)", "agent", agentName)
			return forwardSubagentStream(ctx, sa, agentName, task, ch, logger)
		}
	}
	// Non-streaming path: Execute with panic recovery.
	logger.Debug("executing subagent", "agent", agentName)
	defer func() {
		if p := recover(); p != nil {
			logger.Error("subagent panic", "agent", agentName, "panic", fmt.Sprintf("%v", p))
			result = AgentResult{}
			err = safeAgentError(agentName, p)
		}
	}()
	return agent.Execute(ctx, task)
}

// forwardSubagentStream runs a streaming subagent, forwarding events from
// the subagent's channel to the parent channel. Filters EventInputReceived
// (Network's EventAgentStart is the canonical signal). Handles panic recovery
// and a 60-second drain timeout for misbehaving subagents.
func forwardSubagentStream(
	ctx context.Context,
	sa StreamingAgent,
	agentName string,
	task AgentTask,
	ch chan<- StreamEvent,
	logger *slog.Logger,
) (AgentResult, error) {
	subCh := make(chan StreamEvent, 64)
	done := make(chan struct{})
	safeCloseSubCh := onceClose(subCh)

	// Forwarding goroutine: moves events from subCh to parent ch.
	go func() {
		defer close(done)
		for ev := range subCh {
			// Filter run/iteration envelope events from sub-agents — the parent
			// Network emits EventAgentStart / EventAgentFinish as its own envelope.
			// EventInputReceived is also suppressed for back-compat.
			if ev.Type == EventInputReceived ||
				ev.Type == EventRunStart || ev.Type == EventRunFinish ||
				ev.Type == EventIterationStart || ev.Type == EventIterationFinish {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				// Drain in background so this goroutine releases references
				// promptly, even if ExecuteStream is slow to close subCh.
				startDrainTimeout(subCh, safeCloseSubCh, logger, agentName)
				return
			}
		}
	}()

	// Execute with panic recovery.
	var result AgentResult
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				safeCloseSubCh() // unblock forwarding goroutine
				result = AgentResult{}
				err = safeAgentError(agentName, p)
			}
		}()
		result, err = sa.ExecuteStream(ctx, task, subCh)
	}()

	<-done // wait for all forwarded events
	return result, err
}

// startDrainTimeout drains remaining events from a subagent channel in the
// background with a 60-second timeout. If the subagent ignores cancellation,
// the channel is force-closed to trigger a panic caught by the recover wrapper.
//
// The spawned goroutine is intentionally untracked (no WaitGroup) — it is a
// best-effort safety valve with a bounded 60-second lifetime. Tracking it
// would require plumbing a WaitGroup through the streaming path for a case
// that only fires when a subagent misbehaves (ignores context cancellation).
func startDrainTimeout(subCh <-chan StreamEvent, safeClose func(), logger *slog.Logger, agentName string) {
	go func() {
		timeout := time.NewTimer(60 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case _, ok := <-subCh:
				if !ok {
					return
				}
			case <-timeout.C:
				logger.Warn("subagent stream drain timed out, closing subCh", "agent", agentName)
				safeClose()
				return
			}
		}
	}()
}

// onceClose returns a function that closes the given channel exactly once.
// Safe to call multiple times; subsequent calls are no-ops. Accepts send-only
// channels (close is valid on chan<- T per Go spec).
func onceClose[T any](ch chan<- T) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			close(ch)
		})
	}
}

func safeAgentError(agentName string, p any) error {
	return fmt.Errorf("subagent %q panic: %v", agentName, p)
}

// HasDynamicTools reports whether the agent has a dynamic tool resolver configured.
func (c *AgentCore) HasDynamicTools() bool {
	return c.dynamicTools != nil
}

// ExecuteSpawn handles a spawn_agent tool call. When spawn is disabled, returns
// an error result matching the dispatch-tool fallback semantics for unknown tools.
// Otherwise constructs an ephemeral LLMAgent with inherited tools (minus denied
// ones), executes it, and returns the result.
func (c *AgentCore) ExecuteSpawn(ctx context.Context, args json.RawMessage, toolDefs []ToolDefinition, executeTool ToolExecFunc) DispatchResult {
	if !c.spawnEnabled {
		return DispatchResult{Content: "error: unknown tool: spawn_agent", IsError: true}
	}

	var params spawnAgentArgs
	if err := json.Unmarshal(args, &params); err != nil {
		return DispatchResult{Content: "error: invalid spawn_agent args: " + err.Error(), IsError: true}
	}
	if params.Task == "" {
		return DispatchResult{Content: "error: spawn_agent requires non-empty task", IsError: true}
	}

	// Check depth limit.
	depth := spawnDepth(ctx)
	if depth >= c.spawnDepthLimit {
		return DispatchResult{Content: fmt.Sprintf("error: max spawn depth (%d) exceeded", c.spawnDepthLimit), IsError: true}
	}

	name := spawnAgentName(params)

	// Filter tool definitions: remove denied tools + ask_user.
	// When child will be at max depth, also strip spawn_agent.
	childAtMaxDepth := depth+1 >= c.spawnDepthLimit
	var filteredDefs []ToolDefinition
	deny := make(map[string]bool, len(c.deniedSpawnTools)+1)
	deny["ask_user"] = true
	for _, n := range c.deniedSpawnTools {
		deny[n] = true
	}
	if childAtMaxDepth {
		deny["spawn_agent"] = true
	}
	for _, d := range toolDefs {
		if !deny[d.Name] {
			filteredDefs = append(filteredDefs, d)
		}
	}

	// Build filtered executor that respects deny list.
	filteredExec := func(ctx context.Context, toolName string, toolArgs json.RawMessage) (ToolResult, error) {
		if deny[toolName] {
			return ToolResult{Error: "tool " + toolName + " is not available to sub-agents"}, nil
		}
		return executeTool(ctx, toolName, toolArgs)
	}

	// Build ephemeral options. Wrap each definition as its own AnyTool.
	subTools := make([]AnyTool, len(filteredDefs))
	for i, d := range filteredDefs {
		subTools[i] = &funcTool{def: d, exec: filteredExec}
	}
	opts := []AgentOption{
		WithPrompt(subAgentPrompt),
		WithTools(subTools...),
		WithLimits(Limits{MaxIter: c.maxIter}),
		WithLogger(c.logger),
	}
	if c.genParams != nil {
		opts = append(opts, WithGeneration(Generation{
			Temperature: c.genParams.Temperature,
			TopP:        c.genParams.TopP,
			TopK:        c.genParams.TopK,
			MaxTokens:   c.genParams.MaxTokens,
		}))
	}
	// Enable spawning on child if it won't be at max depth.
	if !childAtMaxDepth {
		opts = append(opts, WithSubAgentSpawning(
			MaxSpawnDepth(c.spawnDepthLimit),
			DenySpawnTools(c.deniedSpawnTools...),
		))
	}
	// Inherit plan execution from parent.
	if c.planExecution {
		opts = append(opts, WithPlanExecution())
	}

	child := NewLLMAgent("sub:"+name, "sub-agent: "+params.Task, c.provider, opts...)

	// Execute with incremented depth.
	childCtx := withSpawnDepth(ctx, depth+1)
	result, err := child.Execute(childCtx, AgentTask{Input: params.Task})
	if err != nil {
		return DispatchResult{Content: "error: sub-agent failed: " + err.Error(), IsError: true}
	}
	return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}

// DispatchBuiltins handles built-in tool calls (ask_user, execute_plan) using
// configuration drawn from the AgentCore fields. Returns (result, true) when
// the call was handled; (zero, false) otherwise.
func (c *AgentCore) DispatchBuiltins(ctx context.Context, tc core.ToolCall, dispatch DispatchFunc) (DispatchResult, bool) {
	if tc.Name == "ask_user" && c.inputHandler != nil {
		content, err := executeAskUser(ctx, c.inputHandler, c.name, tc)
		if err != nil {
			return DispatchResult{Content: "error: " + err.Error(), IsError: true}, true
		}
		return DispatchResult{Content: content}, true
	}
	if tc.Name == "execute_plan" && c.planExecution {
		return executePlan(ctx, tc.Args, dispatch, c.maxPlanSteps, c.maxParallelDispatch), true
	}
	return DispatchResult{}, false
}
