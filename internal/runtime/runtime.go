// Package runtime holds the shared agent execution engine used by both
// agent.LLMAgent and network.Network. It is an internal package; only code
// under agent/ may import it.
package runtime

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
	"github.com/nevindra/oasis/processor"
	"github.com/nevindra/oasis/skills"
)

const defaultMaxIter = 25

// Runtime holds fields shared by LLMAgent and Network.
// Replaces the former AgentCore embedded struct.
//
// Config is embedded so that all option-set fields (SystemPrompt, GenParams,
// SpawnDepthLimit, etc.) promote directly onto Runtime without a copy step.
// The fields below are runtime-only: they are either computed during Init
// (name, description, provider, tools, processors, mem, etc.) or mutated
// during execution (suspendCount, suspendBytes, suspendMu).
//
// Why Compressor is separate from Config.Compactor:
//
//	Config.Compactor is the per-thread compactor (history compaction).
//	Runtime.Compressor is the per-turn tool-result compressor. Both satisfy
//	Compactor but serve different purposes and are set on different lifecycles.
//	Init copies cfg.Compactor → c.Compressor at build time; the runtime uses
//	c.Compressor directly.
//
// Why MaxStepsResolved is separate from Config.MaxSteps (*int):
//
//	Config.MaxSteps is a pointer so BuildConfig can distinguish "explicitly set
//	to 0 (unbounded)" from "not set (default 100)". Once Init dereferences it
//	into Runtime.MaxStepsResolved (int), that distinction is no longer needed.
type Runtime struct {
	Config // embedded — option-set fields promote up

	// Identity and runtime-wired dependencies (passed into Init; not in Config).
	name        string
	description string
	provider    core.Provider

	// Infrastructure built during Init (not option fields).
	tools      *core.ToolRegistry
	processors *processor.Chain
	mem        memory.AgentMemory

	// Cached / computed at build time.
	cachedToolDefs          []core.ToolDefinition
	activeSkillInstructions string
	maxStepsResolved        int // dereferenced from *Config.MaxSteps in Init

	// Suspension counters — mutated during execution, guarded by suspendMu.
	suspendCount int64
	suspendBytes int64
	suspendMu    sync.Mutex

	// Compressor is the per-turn tool-result compressor.
	Compressor core.Compactor

	// NewAgentFunc is set by agent.New at construction time to allow
	// ExecuteSpawn to create ephemeral sub-agents without importing the agent
	// package (which would create a cycle). The function has the same signature
	// as agent.New.
	NewAgentFunc func(name, description string, provider core.Provider, opts ...AgentOption) core.Agent
}

// Init initializes shared fields on a Runtime from the given config.
// Called by agent.New and network.New on the already-allocated struct.
// Uses field-by-field assignment to avoid copying sync primitives in AgentMemory.
func Init(c *Runtime, name, description string, provider core.Provider, cfg *Config) {
	c.Config = *cfg
	c.name = name
	c.description = description
	c.provider = provider
	c.tools = core.NewToolRegistry()
	c.processors = processor.NewChain()

	// Default maxIter when not explicitly set.
	if c.MaxIter == 0 {
		c.MaxIter = defaultMaxIter
	}

	// Dereference *Config.MaxSteps into a plain int for the runtime.
	c.maxStepsResolved = *cfg.MaxSteps

	// Alias: Config.Compactor (per-thread history compaction) is reused as
	// Runtime.Compressor (per-turn tool-result compression).
	c.Compressor = cfg.Compactor

	// Wire memory.
	memCfg := cfg.MemoryConfig
	if !cfg.MemoryInitialized {
		memCfg = memory.AgentMemoryConfig{
			Embedding: cfg.Embedding,
			Provider:  provider,
			Tracer:    cfg.Tracer,
			Logger:    cfg.Logger,
		}
	}
	c.mem.Init(memCfg)

	// Compute effective middleware chain: user-provided + auto-OTel when
	// a tracer is configured and the user hasn't already included one.
	effectiveMiddleware := cfg.ToolMiddleware
	if cfg.Tracer != nil && !HasOTelSpanMiddleware(effectiveMiddleware) {
		effectiveMiddleware = append(effectiveMiddleware, OTelSpanMiddleware(cfg.Tracer))
	}

	// Approval middlewares are outermost.
	for _, ac := range cfg.ToolApprovals {
		effectiveMiddleware = append(effectiveMiddleware, ApprovalMiddleware(ac, cfg.InputHandler))
	}

	for _, t := range cfg.Tools {
		c.tools.Add(core.ApplyToolMiddleware(t, effectiveMiddleware))
	}

	// Register sandbox tools when a sandbox is configured.
	if cfg.Sandbox != nil {
		for _, t := range cfg.SandboxTools {
			c.tools.Add(core.ApplyToolMiddleware(t, effectiveMiddleware))
		}
	}

	// Register skill tools when a skill provider is configured.
	if cfg.SkillProvider != nil {
		for _, t := range skills.NewSkillTools(cfg.SkillProvider) {
			c.tools.Add(core.ApplyToolMiddleware(t, effectiveMiddleware))
		}
	}

	for _, p := range cfg.PreProcessors {
		c.processors.AddPre(p)
	}
	for _, p := range cfg.PostProcessors {
		c.processors.AddPost(p)
	}
	for _, p := range cfg.PostToolProcessors {
		c.processors.AddPostTool(p)
	}

	// Build active skill instructions block.
	if len(cfg.ActiveSkills) > 0 {
		var parts []string
		for _, s := range cfg.ActiveSkills {
			parts = append(parts, "## Skill: "+s.Name+"\n\n"+s.Instructions)
		}
		c.activeSkillInstructions = strings.Join(parts, "\n\n---\n\n")
	}
}

// Name returns the agent's name.
func (c *Runtime) Name() string { return c.name }

// Description returns the agent's description.
func (c *Runtime) Description() string { return c.description }

// Tools returns the agent's tool registry.
func (c *Runtime) Tools() *core.ToolRegistry { return c.tools }

// Memory returns a pointer to the agent's memory orchestrator.
func (c *Runtime) Memory() *memory.AgentMemory { return &c.mem }

// ActiveSkillInstructions returns the compiled skill instructions block.
func (c *Runtime) ActiveSkillInstructions() string { return c.activeSkillInstructions }

// Logger returns the agent's structured logger.
func (c *Runtime) Logger() *slog.Logger { return c.Logger_() }

// Logger_ returns the agent's structured logger (alias avoiding naming conflict).
func (c *Runtime) Logger_() *slog.Logger { return c.Config.Logger }

// CachedToolDefs returns the pre-computed tool definition slice.
func (c *Runtime) CachedToolDefs() []core.ToolDefinition { return c.cachedToolDefs }

// SetCachedToolDefs replaces the cached tool definitions.
func (c *Runtime) SetCachedToolDefs(defs []core.ToolDefinition) { c.cachedToolDefs = defs }

// Close waits for all in-flight background persist goroutines and releases
// memory orchestrator resources.
func (c *Runtime) Close() error { return c.mem.Close() }

// Generation returns a copy of this agent's current Generation parameters.
func (c *Runtime) Generation() Generation {
	p := c.GenParams
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

// Limits returns a copy of this agent's current resource budgets.
func (c *Runtime) Limits() Limits { return LimitsFromConfig(&c.Config) }

// --- Spawn depth tracking ---

// spawnDepthKey is the context key for sub-agent nesting depth.
type spawnDepthKey struct{}

// SpawnDepth returns the current sub-agent nesting depth from ctx.
func SpawnDepth(ctx context.Context) int {
	if v, ok := ctx.Value(spawnDepthKey{}).(int); ok {
		return v
	}
	return 0
}

// WithSpawnDepth returns a child context with the given spawn depth.
func WithSpawnDepth(ctx context.Context, depth int) context.Context {
	return context.WithValue(ctx, spawnDepthKey{}, depth)
}

// CacheBuiltinToolDefs appends built-in tool definitions based on config.
// The implementations of the tool definitions themselves are in the agent package.
func (c *Runtime) CacheBuiltinToolDefs(defs []core.ToolDefinition, inputHandlerDef, executePlanDef, spawnAgentDef *core.ToolDefinition) []core.ToolDefinition {
	if c.InputHandler != nil && inputHandlerDef != nil {
		defs = append(defs, *inputHandlerDef)
	}
	if c.PlanExecution && executePlanDef != nil {
		defs = append(defs, *executePlanDef)
	}
	if c.SpawnEnabled && spawnAgentDef != nil {
		defs = append(defs, *spawnAgentDef)
	}
	return defs
}

// ResolvePromptAndProvider returns the effective prompt and provider for this request.
func (c *Runtime) ResolvePromptAndProvider(ctx context.Context, task core.AgentTask) (string, core.Provider) {
	return c.ResolvePromptAndProviderWith(ctx, task, &c.Config)
}

// ResolvePromptAndProviderWith is the per-call variant of ResolvePromptAndProvider.
func (c *Runtime) ResolvePromptAndProviderWith(ctx context.Context, task core.AgentTask, cfg *Config) (string, core.Provider) {
	prompt := cfg.SystemPrompt
	// Dynamic prompt applies only when there's no per-call override.
	if cfg.SystemPrompt == c.SystemPrompt && c.DynamicPrompt != nil {
		prompt = c.DynamicPrompt(ctx, task)
	}
	p := c.provider
	if c.DynamicModel != nil {
		p = c.DynamicModel(ctx, task)
	}
	if c.activeSkillInstructions != "" {
		prompt = prompt + "\n\n# Active Skills\n\n" + c.activeSkillInstructions
	}
	return prompt, p
}

// ApplyRunOptions returns a Config snapshot with opts merged onto the agent's
// base Config. Returns &c.Config unchanged when opts is nil or has no overrides.
func (c *Runtime) ApplyRunOptions(opts *RunOptions) *Config {
	return ApplyRunOptionsToConfig(&c.Config, opts)
}

// ResolveMem returns the memory orchestrator for a request.
func (c *Runtime) ResolveMem(opts *RunOptions) *memory.AgentMemory {
	if opts != nil && opts.Memory != nil {
		return opts.Memory
	}
	return &c.mem
}

// ResolveDynamicTools returns tool definitions and executors for a dynamic request.
func (c *Runtime) ResolveDynamicTools(ctx context.Context, task core.AgentTask) ([]core.ToolDefinition, ToolExecFunc, ToolExecStreamFunc) {
	if c.DynamicTools == nil {
		return nil, nil, nil
	}
	dynTools := c.DynamicTools(ctx, task)
	var toolDefs []core.ToolDefinition
	index := make(map[string]core.AnyTool, len(dynTools))
	for _, t := range dynTools {
		toolDefs = append(toolDefs, t.Definition())
		index[t.Name()] = t
	}
	executeTool := func(ctx context.Context, name string, args json.RawMessage) (core.ToolResult, error) {
		if t, ok := index[name]; ok {
			return t.ExecuteRaw(ctx, args)
		}
		return core.ToolResult{Error: "unknown tool: " + name}, nil
	}
	executeToolStream := func(ctx context.Context, name string, args json.RawMessage, ch chan<- core.StreamEvent) (core.ToolResult, error) {
		t, ok := index[name]
		if !ok {
			return core.ToolResult{Error: "unknown tool: " + name}, nil
		}
		if ch != nil {
			if st, ok := t.(core.StreamingAnyTool); ok {
				return st.ExecuteStream(ctx, args, ch)
			}
		}
		return t.ExecuteRaw(ctx, args)
	}
	return toolDefs, executeTool, executeToolStream
}

// HasDynamicTools reports whether the agent has a dynamic tool resolver configured.
func (c *Runtime) HasDynamicTools() bool {
	return c.DynamicTools != nil
}

// ResolveTools returns the tool definitions and executors for an iteration.
func (c *Runtime) ResolveTools(
	ctx context.Context,
	task core.AgentTask,
	prebuild func([]core.ToolDefinition) []core.ToolDefinition,
	inputHandlerDef, executePlanDef, spawnAgentDef *core.ToolDefinition,
) (defs []core.ToolDefinition, exec ToolExecFunc, execStream ToolExecStreamFunc, isStream func(string) bool) {
	if dynDefs, dynExec, dynExecStream := c.ResolveDynamicTools(ctx, task); dynDefs != nil {
		c.Config.Logger.Debug("using dynamic tools", "agent", c.name, "tool_count", len(dynDefs))
		if prebuild != nil {
			dynDefs = prebuild(dynDefs)
		}
		return c.CacheBuiltinToolDefs(dynDefs, inputHandlerDef, executePlanDef, spawnAgentDef), dynExec, dynExecStream, func(string) bool { return false }
	}
	return c.cachedToolDefs, c.tools.Execute, c.tools.ExecuteStream, c.tools.IsStreamingTool
}

// BaseLoopConfig assembles a LoopConfig from resolved values.
func (c *Runtime) BaseLoopConfig(
	name, prompt string,
	provider core.Provider,
	tools []core.ToolDefinition,
	dispatch DispatchFunc,
	cfg *Config,
	mem *memory.AgentMemory,
) LoopConfig {
	maxSteps := 0
	if cfg.MaxSteps != nil {
		maxSteps = *cfg.MaxSteps
	}
	return LoopConfig{
		Config: *cfg,
		// identity / per-call wiring
		Name:         name,
		Provider:     provider,
		Tools:        tools,
		Dispatch:     dispatch,
		SystemPrompt: prompt,
		// maxSteps: dereferenced from *Config.MaxSteps to avoid pointer indirection.
		MaxStepsResolved: maxSteps,
		// non-overridable runtime state
		Processors:   c.processors,
		Mem:          mem,
		SuspendCount: &c.suspendCount,
		SuspendBytes: &c.suspendBytes,
		SuspendMu:    &c.suspendMu,
		Compressor:   c.Compressor,
		LookupTool:   c.tools.Lookup,
	}
}

// ExecuteWithSpan wraps runLoop with tracing, logging, and input-received event emission.
// runLoopFn is a callback that actually calls runLoop (defined in agent/loop.go).
func (c *Runtime) ExecuteWithSpan(
	ctx context.Context,
	task core.AgentTask,
	ch chan<- core.StreamEvent,
	agentType, logKey string,
	buildCfg func(ctx context.Context, task core.AgentTask, ch chan<- core.StreamEvent) LoopConfig,
	runLoopFn func(ctx context.Context, cfg LoopConfig, task core.AgentTask, ch chan<- core.StreamEvent) (core.AgentResult, error),
) (core.AgentResult, error) {
	// Emit run-start as the first event on every stream.
	if ch != nil {
		select {
		case ch <- core.StreamEvent{Type: core.EventRunStart, Name: c.name, Content: task.Input}:
		case <-ctx.Done():
			return core.AgentResult{}, ctx.Err()
		}
	}

	var span core.Span
	if c.Tracer != nil {
		ctx, span = c.Tracer.Start(ctx, "agent.execute",
			core.StringAttr("agent.name", c.name),
			core.StringAttr("agent.type", agentType))
		defer span.End()
	}

	c.Config.Logger.Info(logKey+" started", logKey, c.name)
	start := time.Now()
	result, err := runLoopFn(ctx, buildCfg(ctx, task, ch), task, ch)

	if span != nil {
		span.SetAttr(
			core.IntAttr("tokens.input", result.Usage.InputTokens),
			core.IntAttr("tokens.output", result.Usage.OutputTokens))
		if err != nil {
			span.Error(err)
			span.SetAttr(core.StringAttr("agent.status", "error"))
		} else {
			span.SetAttr(core.StringAttr("agent.status", "ok"))
		}
	}

	if err != nil {
		c.Config.Logger.Error(logKey+" failed", logKey, c.name,
			"error", err,
			"duration", time.Since(start),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens)
	} else {
		c.Config.Logger.Info(logKey+" completed", logKey, c.name,
			"duration", time.Since(start),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens,
			"steps", len(result.Steps))
	}
	return result, err
}

// ExecuteSpawn handles a spawn_agent tool call.
func (c *Runtime) ExecuteSpawn(ctx context.Context, args json.RawMessage, toolDefs []core.ToolDefinition, executeTool ToolExecFunc, ch chan<- core.StreamEvent,
	executeAgentFn func(ctx context.Context, agent core.Agent, agentName string, task core.AgentTask, ch chan<- core.StreamEvent, logger *slog.Logger) (core.AgentResult, error),
) DispatchResult {
	if !c.SpawnEnabled {
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
	depth := SpawnDepth(ctx)
	if depth >= c.SpawnDepthLimit {
		return DispatchResult{Content: fmt.Sprintf("error: max spawn depth (%d) exceeded", c.SpawnDepthLimit), IsError: true}
	}

	name := spawnAgentName(params)

	// Filter tool definitions: remove denied tools + ask_user.
	childAtMaxDepth := depth+1 >= c.SpawnDepthLimit
	deny := make(map[string]bool, len(c.DeniedSpawnTools)+2)
	deny["ask_user"] = true
	for _, n := range c.DeniedSpawnTools {
		deny[n] = true
	}
	if childAtMaxDepth {
		deny["spawn_agent"] = true
	}
	filteredDefs := make([]core.ToolDefinition, 0, len(toolDefs))
	for _, d := range toolDefs {
		if deny[d.Name] || strings.HasPrefix(d.Name, "agent_") {
			continue
		}
		filteredDefs = append(filteredDefs, d)
	}

	// Build filtered executor that respects deny list.
	filteredExec := func(ctx context.Context, toolName string, toolArgs json.RawMessage) (core.ToolResult, error) {
		if deny[toolName] {
			return core.ToolResult{Error: "tool " + toolName + " is not available to sub-agents"}, nil
		}
		return executeTool(ctx, toolName, toolArgs)
	}

	// Build ephemeral options. Wrap each definition as its own AnyTool.
	subTools := make([]core.AnyTool, len(filteredDefs))
	for i, d := range filteredDefs {
		subTools[i] = &funcTool{def: d, exec: filteredExec}
	}

	// Use the injected NewAgentFunc to avoid importing agent (cycle prevention).
	if c.NewAgentFunc == nil {
		return DispatchResult{Content: "error: spawn_agent not configured (NewAgentFunc is nil)", IsError: true}
	}

	opts := []AgentOption{
		WithPromptOption(subAgentPrompt),
		WithToolsOption(subTools...),
		WithLimitsOption(Limits{MaxIter: c.MaxIter}),
		WithLoggerOption(c.Config.Logger),
	}
	if c.GenParams != nil {
		opts = append(opts, WithGenerationOption(Generation{
			Temperature: c.GenParams.Temperature,
			TopP:        c.GenParams.TopP,
			TopK:        c.GenParams.TopK,
			MaxTokens:   c.GenParams.MaxTokens,
		}))
	}
	if c.Tracer != nil {
		opts = append(opts, WithTracerOption(c.Tracer))
	}
	if !childAtMaxDepth {
		opts = append(opts, WithSubAgentSpawningOption(c.SpawnDepthLimit, c.DeniedSpawnTools))
	}
	if c.PlanExecution {
		opts = append(opts, WithPlanExecutionOption())
	}

	child := c.NewAgentFunc("sub:"+name, "sub-agent: "+params.Task, c.provider, opts...)

	childCtx := WithSpawnDepth(ctx, depth+1)
	result, err := executeAgentFn(childCtx, child, name, core.AgentTask{Input: params.Task}, ch, c.Config.Logger)
	if err != nil {
		return DispatchResult{Content: "error: sub-agent failed: " + err.Error(), IsError: true}
	}
	return DispatchResult{Content: result.Output, Usage: result.Usage, Attachments: result.Attachments}
}

// DispatchBuiltins handles built-in tool calls (ask_user, execute_plan).
// executePlanFn and executeAskUserFn are callbacks from agent to avoid cycles.
func (c *Runtime) DispatchBuiltins(
	ctx context.Context,
	tc core.ToolCall,
	dispatch DispatchFunc,
	executeAskUserFn func(ctx context.Context, handler InputHandler, agentName string, tc core.ToolCall) (string, error),
	executePlanFn func(ctx context.Context, args json.RawMessage, dispatch DispatchFunc, planStepsLimit, parallelLimit int) DispatchResult,
) (DispatchResult, bool) {
	if tc.Name == "ask_user" && c.InputHandler != nil {
		content, err := executeAskUserFn(ctx, c.InputHandler, c.name, tc)
		if err != nil {
			return DispatchResult{Content: "error: " + err.Error(), IsError: true}, true
		}
		return DispatchResult{Content: content}, true
	}
	if tc.Name == "execute_plan" && c.PlanExecution {
		return executePlanFn(ctx, tc.Args, dispatch, c.MaxPlanSteps, c.MaxParallelDispatch), true
	}
	return DispatchResult{}, false
}

// --- spawn_agent tool internals ---

type spawnAgentArgs struct {
	Task string `json:"task" describe:"Clear instruction for what the sub-agent should accomplish"`
	Name string `json:"name,omitempty" describe:"Short label for this sub-agent (for logging). Auto-generated if omitted."`
}

func spawnAgentName(args spawnAgentArgs) string {
	if args.Name != "" {
		return args.Name
	}
	name := truncateStr(args.Task, 20)
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, name)
}

func truncateStr(s string, maxRunes int) string {
	count := 0
	for i := range s {
		if count >= maxRunes {
			return s[:i]
		}
		count++
	}
	return s
}

const subAgentPrompt = "You are a sub-agent. Complete the given task thoroughly and return the result. Be concise."

// funcTool adapts a single ToolDefinition + executor into AnyTool.
type funcTool struct {
	def  core.ToolDefinition
	exec ToolExecFunc
}

func (f *funcTool) Name() string                    { return f.def.Name }
func (f *funcTool) Definition() core.ToolDefinition { return f.def }
func (f *funcTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	return f.exec(ctx, f.def.Name, args)
}

// --- ApplyRunOptionsToConfig ---

// ApplyRunOptionsToConfig returns a Config snapshot with RunOptions overrides applied
// on top of base. base is never mutated. Returns base unchanged when opts is nil.
func ApplyRunOptionsToConfig(base *Config, opts *RunOptions) *Config {
	if opts == nil || !opts.HasOverrides() {
		return base
	}
	out := *base
	c := &out

	if opts.Prompt != nil {
		c.SystemPrompt = *opts.Prompt
	}
	if opts.Generation != nil {
		c.GenParams = mergeGenerationParams(base.GenParams, opts.Generation)
	}
	if opts.ResponseSchema != nil {
		c.ResponseSchema = opts.ResponseSchema
	}

	if opts.Tools != nil {
		c.Tools = opts.Tools
	}
	if opts.Agents != nil {
		c.Agents = opts.Agents
	}
	if opts.ActiveSkills != nil {
		c.ActiveSkills = opts.ActiveSkills
	}

	if opts.Limits != nil {
		opts.Limits.ApplyTo(c)
	}

	if opts.PreProcessors != nil {
		c.PreProcessors = opts.PreProcessors
	}
	if opts.PostProcessors != nil {
		c.PostProcessors = opts.PostProcessors
	}
	if opts.PostToolProcessors != nil {
		c.PostToolProcessors = opts.PostToolProcessors
	}

	if opts.PrepareStep != nil {
		c.PrepareStep = opts.PrepareStep
	}
	if opts.OnIterationComplete != nil {
		c.OnIterationComplete = opts.OnIterationComplete
	}
	if opts.OnError != nil {
		c.OnError = opts.OnError
	}

	if opts.InputHandler != nil {
		c.InputHandler = opts.InputHandler
	}
	if opts.Tracer != nil {
		c.Tracer = opts.Tracer
	}
	if opts.Logger != nil {
		c.Logger = opts.Logger
	}

	if len(opts.Metadata) > 0 {
		merged := make(map[string]any, len(base.Metadata)+len(opts.Metadata))
		for k, v := range base.Metadata {
			merged[k] = v
		}
		for k, v := range opts.Metadata {
			merged[k] = v
		}
		c.Metadata = merged
	}

	return c
}

func mergeGenerationParams(base *core.GenerationParams, override *Generation) *core.GenerationParams {
	out := &core.GenerationParams{}
	if base != nil {
		*out = *base
	}
	if override.Temperature != nil {
		out.Temperature = override.Temperature
	}
	if override.TopP != nil {
		out.TopP = override.TopP
	}
	if override.TopK != nil {
		out.TopK = override.TopK
	}
	if override.MaxTokens != nil {
		out.MaxTokens = override.MaxTokens
	}
	return out
}

// --- Minimal AgentOption constructors needed by ExecuteSpawn ---
// These are internal to runtime only; the full set lives in agent/agent.go.

// WithPromptOption sets the system prompt.
func WithPromptOption(s string) AgentOption {
	return func(c *Config) { c.SystemPrompt = s }
}

// WithToolsOption adds tools.
func WithToolsOption(tools ...core.AnyTool) AgentOption {
	return func(c *Config) { c.Tools = append(c.Tools, tools...) }
}

// WithLimitsOption sets resource limits.
func WithLimitsOption(lim Limits) AgentOption {
	return func(c *Config) { lim.ApplyTo(c) }
}

// WithLoggerOption sets the logger.
func WithLoggerOption(l *slog.Logger) AgentOption {
	return func(c *Config) { c.Logger = l }
}

// WithGenerationOption sets generation parameters.
func WithGenerationOption(g Generation) AgentOption {
	return func(c *Config) {
		if c.GenParams == nil {
			c.GenParams = &core.GenerationParams{}
		}
		if g.Temperature != nil {
			v := *g.Temperature
			c.GenParams.Temperature = &v
		} else {
			c.GenParams.Temperature = nil
		}
		if g.TopP != nil {
			v := *g.TopP
			c.GenParams.TopP = &v
		} else {
			c.GenParams.TopP = nil
		}
		if g.TopK != nil {
			v := *g.TopK
			c.GenParams.TopK = &v
		} else {
			c.GenParams.TopK = nil
		}
		if g.MaxTokens != nil {
			v := *g.MaxTokens
			c.GenParams.MaxTokens = &v
		} else {
			c.GenParams.MaxTokens = nil
		}
	}
}

// WithTracerOption sets the tracer.
func WithTracerOption(t core.Tracer) AgentOption {
	return func(c *Config) { c.Tracer = t }
}

// WithSubAgentSpawningOption enables spawn_agent with given depth and denied tools.
func WithSubAgentSpawningOption(depthLimit int, deniedTools []string) AgentOption {
	return func(c *Config) {
		c.SpawnEnabled = true
		c.SpawnDepthLimit = depthLimit
		c.DeniedSpawnTools = append(c.DeniedSpawnTools, deniedTools...)
	}
}

// WithPlanExecutionOption enables execute_plan.
func WithPlanExecutionOption() AgentOption {
	return func(c *Config) { c.PlanExecution = true }
}
