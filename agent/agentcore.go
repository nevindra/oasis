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
type AgentCore struct {
	name             string
	description      string
	provider         Provider
	tools            *ToolRegistry
	processors       *ProcessorChain
	systemPrompt     string
	maxIter          int
	handler          InputHandler
	planExecution    bool
	sandbox          core.Sandbox
	responseSchema   *ResponseSchema
	dynamicPrompt    PromptFunc
	dynamicModel     ModelFunc
	dynamicTools     ToolsFunc
	tracer           Tracer
	logger           *slog.Logger
	mem              memory.AgentMemory
	cachedToolDefs   []ToolDefinition
	maxAttachmentBytes  int64
	suspendCount        int64      // guarded by suspendMu
	suspendBytes        int64      // guarded by suspendMu
	suspendMu           sync.Mutex // guards suspendCount/suspendBytes (Phase 4 finding 4.1.g)
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int
	compressor          Compactor
	genParams           *GenerationParams
	spawnEnabled        bool
	spawnDepthLimit     int
	deniedSpawnTools    []string
	activeSkillInstructions string
	maxParallelDispatch int
	maxPlanSteps        int
	maxToolResultLen    int
	toolResultStore     core.ToolResultStore
	toolPolicies        map[string]core.ToolPolicy
	toolPolicyMatchers  []toolPolicyMatcher
	maxSteps            int
	prepareStep         PrepareStep         // optional; set via WithPrepareStep
	onError             OnError             // optional; set via WithOnError
	onIterationComplete OnIterationComplete // optional; set via WithOnIterationComplete
}

// initCore initializes shared fields on an AgentCore from the given config.
// Called by NewLLMAgent and NewNetwork on the already-allocated parent struct.
// Uses field-by-field assignment to avoid copying sync primitives in agentMemory.
func InitCore(c *AgentCore, name, description string, provider Provider, cfg *Config) {
	c.name = name
	c.description = description
	c.provider = provider
	c.tools = NewToolRegistry()
	c.processors = NewProcessorChain()
	c.systemPrompt = cfg.prompt
	c.maxIter = defaultMaxIter
	if cfg.maxIter > 0 {
		c.maxIter = cfg.maxIter
	}

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

	c.handler = cfg.inputHandler
	c.planExecution = cfg.planExecution
	c.sandbox = cfg.sandbox
	c.responseSchema = cfg.responseSchema
	c.dynamicPrompt = cfg.dynamicPrompt
	c.dynamicModel = cfg.dynamicModel
	c.dynamicTools = cfg.dynamicTools
	c.tracer = cfg.tracer
	c.logger = cfg.logger
	c.maxAttachmentBytes = cfg.maxAttachmentBytes
	c.maxSuspendSnapshots = cfg.maxSuspendSnapshots
	c.maxSuspendBytes = cfg.maxSuspendBytes
	c.compressModel = cfg.compressModel
	c.compressThreshold = cfg.compressThreshold
	c.compressor = cfg.compactor // reuse the per-thread compactor for per-turn tool-result compression
	c.genParams = cfg.generationParams
	c.spawnEnabled = cfg.spawnEnabled
	c.spawnDepthLimit = cfg.maxSpawnDepth
	c.deniedSpawnTools = cfg.denySpawnTools
	c.maxParallelDispatch = cfg.maxParallelDispatch
	c.maxPlanSteps = cfg.maxPlanSteps
	c.maxToolResultLen = cfg.maxToolResultLen
	c.toolResultStore = cfg.toolResultStore
	c.toolPolicies = cfg.toolPolicies
	c.toolPolicyMatchers = cfg.toolPolicyMatchers
	c.maxSteps = *cfg.maxSteps
	c.prepareStep = cfg.prepareStep
	c.onError = cfg.onError
	c.onIterationComplete = cfg.onIterationComplete

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
	if c.handler != nil {
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
	prompt := c.systemPrompt
	if c.dynamicPrompt != nil {
		prompt = c.dynamicPrompt(ctx, task)
	}
	p := c.provider
	if c.dynamicModel != nil {
		p = c.dynamicModel(ctx, task)
	}
	return prompt, p
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

// BaseLoopConfig assembles a LoopConfig from resolved values.
// Callers provide the name prefix, resolved prompt/provider/tools, and dispatch function.
func (c *AgentCore) BaseLoopConfig(name, prompt string, provider Provider, tools []ToolDefinition, dispatch DispatchFunc) LoopConfig {
	return LoopConfig{
		name:                name,
		provider:            provider,
		tools:               tools,
		processors:          c.processors,
		maxIter:             c.maxIter,
		mem:                 &c.mem,
		inputHandler:        c.handler,
		dispatch:            dispatch,
		systemPrompt:        prompt,
		responseSchema:      c.responseSchema,
		tracer:              c.tracer,
		logger:              c.logger,
		maxAttachmentBytes:  c.maxAttachmentBytes,
		suspendCount:        &c.suspendCount,
		suspendBytes:        &c.suspendBytes,
		suspendMu:           &c.suspendMu,
		maxSuspendSnapshots: c.maxSuspendSnapshots,
		maxSuspendBytes:     c.maxSuspendBytes,
		compressModel:       c.compressModel,
		compressThreshold:   c.compressThreshold,
		compressor:          c.compressor,
		generationParams:    c.genParams,
		maxParallelDispatch: c.maxParallelDispatch,
		maxToolResultLen:    c.maxToolResultLen,
		maxPlanSteps:        c.maxPlanSteps,
		toolResultStore:     c.toolResultStore,
		maxSteps:            c.maxSteps,
		prepareStep:         c.prepareStep,
		onError:             c.onError,
		onIterationComplete: c.onIterationComplete,
		lookupTool:          c.tools.Lookup,
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
		WithMaxIter(c.maxIter),
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
	if tc.Name == "ask_user" && c.handler != nil {
		content, err := executeAskUser(ctx, c.handler, c.name, tc)
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
