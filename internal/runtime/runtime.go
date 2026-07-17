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

var loopConfigPool = sync.Pool{
	New: func() any { return new(LoopConfig) },
}

func AcquireLoopConfig() *LoopConfig {
	return loopConfigPool.Get().(*LoopConfig)
}

func ReleaseLoopConfig(lc *LoopConfig) {
	*lc = LoopConfig{}
	loopConfigPool.Put(lc)
}

// Runtime holds fields shared by LLMAgent and Network.
// Replaces the former AgentCore embedded struct.
//
// Config is embedded so that all option-set fields (SystemPrompt, GenParams,
// etc.) promote directly onto Runtime without a copy step.
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

	// scorePool runs async scorers off the hot path. Nil when no scorers are
	// attached. Drained in Close.
	scorePool *scorerPool

	// Cached / computed at build time.
	cachedToolDefs          []core.ToolDefinition
	activeSkillInstructions string

	// Cached method values — avoid per-call closure allocation.
	cachedExecuteTool       ToolExecFunc
	cachedExecuteToolStream ToolExecStreamFunc
	cachedIsStreamingTool   func(string) bool
	cachedLookupTool        func(string) (core.AnyTool, bool)

	// Suspension counters — mutated during execution, guarded by suspendMu.
	suspendCount int64
	suspendBytes int64
	suspendMu    sync.Mutex

	// Compressor is the per-turn tool-result compressor.
	Compressor core.Compactor
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

	// Wire the async scorer pool when scorers are attached. The pool persists to
	// ScoreStore / ScoreSink; inline scorers run synchronously in runScorers.
	if len(cfg.Scorers) > 0 {
		c.scorePool = newScorerPool(defaultScorerWorkers, defaultScorerBuffer, cfg.ScoreStore, cfg.ScoreSink, cfg.Logger)
	}

	// Wrap every registered tool with the effective middleware chain.
	// The same chain is reused for dynamically-resolved tools (see
	// ResolveDynamicTools) — otherwise the static and dynamic paths would
	// have divergent OTel / approval semantics.
	effectiveMiddleware := c.effectiveToolMiddleware()

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

	// Cache method values to avoid per-call closure allocation.
	c.cachedExecuteTool = c.tools.Execute
	c.cachedExecuteToolStream = c.tools.ExecuteStream
	c.cachedIsStreamingTool = c.tools.IsStreamingTool
	c.cachedLookupTool = c.tools.Lookup
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
func (c *Runtime) Logger() *slog.Logger { return c.Config.Logger }

// CachedToolDefs returns the pre-computed tool definition slice.
func (c *Runtime) CachedToolDefs() []core.ToolDefinition { return c.cachedToolDefs }

// SetCachedToolDefs replaces the cached tool definitions.
func (c *Runtime) SetCachedToolDefs(defs []core.ToolDefinition) { c.cachedToolDefs = defs }

// Processors returns the processor chain for this runtime.
func (c *Runtime) Processors() *processor.Chain { return c.processors }

// Close waits for all in-flight background persist goroutines and releases
// memory orchestrator resources.
func (c *Runtime) Close() error {
	// Drain in-flight async scores before tearing down (nil-safe).
	c.scorePool.close()
	return c.mem.Close()
}

// Generation returns a deep copy of this agent's current Generation parameters.
// Mutating fields on the returned value (including pointed-to data) does not
// affect the agent's internal state.
func (c *Runtime) Generation() Generation {
	if c.GenParams == nil {
		return Generation{}
	}
	return CloneGeneration(*c.GenParams)
}

// Limits returns a copy of this agent's current resource budgets.
func (c *Runtime) Limits() Limits { return LimitsFromConfig(&c.Config) }

// --- Spawn depth tracking ---

// CacheBuiltinToolDefs appends built-in tool definitions based on config.
// The implementations of the tool definitions themselves are in the agent package.
func (c *Runtime) CacheBuiltinToolDefs(defs []core.ToolDefinition, inputHandlerDef, executePlanDef *core.ToolDefinition) []core.ToolDefinition {
	if c.InputHandler != nil && inputHandlerDef != nil {
		defs = append(defs, *inputHandlerDef)
	}
	if c.PlanExecution && executePlanDef != nil {
		defs = append(defs, *executePlanDef)
	}
	return defs
}

// buildSkillCatalog formats the provider's discoverable skills into an
// "# Available Skills" prompt block, excluding any skill already injected via
// ActiveSkills. Returns "" on discover error, empty corpus, or all-excluded.
func buildSkillCatalog(ctx context.Context, p skills.SkillProvider, active []skills.Skill) string {
	summaries, err := p.Discover(ctx)
	if err != nil || len(summaries) == 0 {
		return ""
	}
	activeNames := make(map[string]bool, len(active))
	for _, s := range active {
		activeNames[s.Name] = true
	}
	var b strings.Builder
	b.WriteString("# Available Skills\n\n")
	b.WriteString("You can load any of these skills for detailed instructions. Use skill_activate(name) (or skill_search to find one by topic) before applying a skill.\n\n")
	n := 0
	for _, s := range summaries {
		if activeNames[s.Name] {
			continue
		}
		fmt.Fprintf(&b, "- %s — %s", s.Name, s.Description)
		if len(s.Tags) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(s.Tags, ", "))
		}
		b.WriteByte('\n')
		n++
	}
	if n == 0 {
		return ""
	}
	return b.String()
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
	if cfg.SkillCatalog && cfg.SkillProvider != nil {
		if block := buildSkillCatalog(ctx, cfg.SkillProvider, cfg.ActiveSkills); block != "" {
			prompt = prompt + "\n\n" + block
		}
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

// effectiveToolMiddleware returns the composed middleware chain that wraps
// every tool — user middleware + auto-OTel (when a tracer is configured and
// the user hasn't included one) + per-tool approval gates outermost.
//
// Why: both Init (static tools) and ResolveDynamicTools (per-call dynamic
// tools) must produce identical observability and approval semantics. Pulling
// the chain construction into one place eliminates the drift class — a tool
// promoted from static to dynamic registration keeps its OTel span and any
// approval gate.
func (c *Runtime) effectiveToolMiddleware() []core.ToolMiddleware {
	// Why: explicit copy prevents concurrent append (e.g. from ResolveDynamicTools
	// running from multiple goroutines) from aliasing the Config slice — when
	// cap > len both appends would write to the same slot beyond len.
	base := c.Config.ToolMiddleware
	n := len(base) + len(c.Config.ToolApprovals) + 1
	mws := make([]core.ToolMiddleware, len(base), n)
	copy(mws, base)
	if c.Config.Tracer != nil && !HasOTelSpanMiddleware(mws) {
		mws = append(mws, OTelSpanMiddleware(c.Config.Tracer))
	}
	for _, ac := range c.Config.ToolApprovals {
		mws = append(mws, ApprovalMiddleware(ac, c.Config.InputHandler))
	}
	return mws
}

// ResolveDynamicTools returns tool definitions and executors for a dynamic request.
//
// Dynamic tools are wrapped with the same middleware chain as statically
// registered tools — OTel spans, approval gates, and any user middleware —
// so the two registration paths cannot drift in observability or security
// posture.
func (c *Runtime) ResolveDynamicTools(ctx context.Context, task core.AgentTask) ([]core.ToolDefinition, ToolExecFunc, ToolExecStreamFunc) {
	if c.DynamicTools == nil {
		return nil, nil, nil
	}
	dynTools := c.DynamicTools(ctx, task)
	mws := c.effectiveToolMiddleware()
	var toolDefs []core.ToolDefinition
	index := make(map[string]core.AnyTool, len(dynTools))
	for _, t := range dynTools {
		wrapped := core.ApplyToolMiddleware(t, mws)
		toolDefs = append(toolDefs, wrapped.Definition())
		index[wrapped.Name()] = wrapped
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
	inputHandlerDef, executePlanDef *core.ToolDefinition,
) (defs []core.ToolDefinition, exec ToolExecFunc, execStream ToolExecStreamFunc, isStream func(string) bool) {
	if dynDefs, dynExec, dynExecStream := c.ResolveDynamicTools(ctx, task); dynDefs != nil {
		if c.Config.Logger.Enabled(ctx, slog.LevelDebug) {
			c.Config.Logger.Debug("using dynamic tools", "agent", c.name, "tool_count", len(dynDefs))
		}
		if prebuild != nil {
			dynDefs = prebuild(dynDefs)
		}
		return c.CacheBuiltinToolDefs(dynDefs, inputHandlerDef, executePlanDef), dynExec, dynExecStream, func(string) bool { return false }
	}
	return c.cachedToolDefs, c.cachedExecuteTool, c.cachedExecuteToolStream, c.cachedIsStreamingTool
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
		LookupTool:   c.cachedLookupTool,
	}
}

// ExecuteWithSpan wraps runLoop with tracing, logging, and input-received event emission.
// runLoopFn is a callback that actually calls runLoop (defined in agent/loop.go).
func (c *Runtime) ExecuteWithSpan(
	ctx context.Context,
	task core.AgentTask,
	ch chan<- core.StreamEvent,
	agentType, logKey string,
	buildCfg func(ctx context.Context, task core.AgentTask, ch chan<- core.StreamEvent) *LoopConfig,
	runLoopFn func(ctx context.Context, cfg *LoopConfig, task core.AgentTask, ch chan<- core.StreamEvent) (core.AgentResult, error),
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
		attrs := []core.SpanAttr{
			core.StringAttr("agent.name", c.name),
			core.StringAttr("agent.type", agentType),
			core.StringAttr("langfuse.observation.type", "agent"),
		}
		if core.TraceContentEnabled() {
			attrs = append(attrs, core.StringAttr("langfuse.observation.input", task.Input))
		}
		ctx, span = c.Tracer.Start(ctx, "agent.execute", attrs...)
		defer span.End()
	}

	if c.Config.Logger.Enabled(ctx, slog.LevelInfo) {
		c.Config.Logger.Info(logKey+" started", logKey, c.name)
	}
	start := time.Now()
	loopCfg := buildCfg(ctx, task, ch)
	result, err := runLoopFn(ctx, loopCfg, task, ch)
	if !result.Suspended() {
		ReleaseLoopConfig(loopCfg)
	}

	if span != nil {
		span.SetAttr(
			core.IntAttr("tokens.input", result.Usage.InputTokens),
			core.IntAttr("tokens.output", result.Usage.OutputTokens))
		if err != nil {
			span.Error(err)
			span.SetAttr(core.StringAttr("agent.status", "error"))
		} else {
			span.SetAttr(core.StringAttr("agent.status", "ok"))
			if core.TraceContentEnabled() && result.Output != "" {
				span.SetAttr(core.StringAttr("langfuse.observation.output", result.Output))
			}
		}
	}

	if err != nil {
		if c.Config.Logger.Enabled(ctx, slog.LevelError) {
			c.Config.Logger.Error(logKey+" failed", logKey, c.name,
				"error", err,
				"duration", time.Since(start),
				"tokens.input", result.Usage.InputTokens,
				"tokens.output", result.Usage.OutputTokens)
		}
	} else {
		if c.Config.Logger.Enabled(ctx, slog.LevelInfo) {
			c.Config.Logger.Info(logKey+" completed", logKey, c.name,
				"duration", time.Since(start),
				"tokens.input", result.Usage.InputTokens,
				"tokens.output", result.Usage.OutputTokens,
				"steps", len(result.Steps))
		}
	}
	return result, err
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
	if tc.Name == core.ToolAskUser && c.InputHandler != nil {
		content, err := executeAskUserFn(ctx, c.InputHandler, c.name, tc)
		if err != nil {
			return DispatchResult{Content: "error: " + err.Error(), IsError: true}, true
		}
		return DispatchResult{Content: content}, true
	}
	if tc.Name == core.ToolExecutePlan && c.PlanExecution {
		return executePlanFn(ctx, tc.Args, dispatch, c.MaxPlanSteps, c.MaxParallelDispatch), true
	}
	return DispatchResult{}, false
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
	if opts.ActiveSkills != nil {
		c.ActiveSkills = opts.ActiveSkills
	}

	if opts.Limits != nil {
		opts.Limits.ApplyTo(c)
	}

	// Why: per-call processors are appended (not replacing) the agent-level
	// chain, mirroring Processors.ApplyTo — so guardrails and other processors
	// wired at build time are not silently dropped by a per-call RunOptions.
	// Per-call hooks override agent-level hooks per Hooks.ApplyTo.
	Processors{
		Pre:      opts.PreProcessors,
		Post:     opts.PostProcessors,
		PostTool: opts.PostToolProcessors,
	}.ApplyTo(c)
	Hooks{
		PrepareStep:         opts.PrepareStep,
		OnIterationComplete: opts.OnIterationComplete,
		OnError:             opts.OnError,
	}.ApplyTo(c)

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
		merged := make(map[string]string, len(base.Metadata)+len(opts.Metadata))
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

// mergeGenerationParams overlays non-nil fields of override onto a deep copy of
// base, producing a fresh *core.GenerationParams. base is never mutated; the
// override's pointer fields are cloned so subsequent caller mutation cannot
// leak into the agent's state. The field list lives in overlayNonNilGeneration
// so it stays in sync with CloneGeneration.
func mergeGenerationParams(base *core.GenerationParams, override *Generation) *core.GenerationParams {
	out := &core.GenerationParams{}
	if base != nil {
		*out = *base
	}
	overlayNonNilGeneration(out, override)
	return out
}
