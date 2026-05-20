package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

const defaultMaxIter = 10

// AgentCore holds fields shared by LLMAgent and Network.
// Both types embed this struct to eliminate field duplication.
// New agent-level options only need to be wired here once.
// Exported for use by network subpackage during Phase 2 migration.
// This is an internal type; do not depend on stability.
type AgentCore struct {
	name             string
	description      string
	LLMProvider      Provider         // exported for network subpackage access (avoid clash with Provider interface name)
	Tools            *ToolRegistry    // exported for network subpackage access
	processors       *ProcessorChain
	systemPrompt     string
	MaxIter          int              // exported for network subpackage access
	Handler          InputHandler     // exported for network subpackage access (avoid clash with InputHandler interface name)
	PlanExecution    bool             // exported for network subpackage access
	sandbox          core.Sandbox     // holds a sandbox.Sandbox when set
	responseSchema   *ResponseSchema
	dynamicPrompt    PromptFunc
	dynamicModel     ModelFunc
	DynamicTools     ToolsFunc        // exported for network subpackage access
	tracer           Tracer
	Logger           *slog.Logger     // exported for network subpackage access
	mem              memory.AgentMemory
	CachedToolDefs   []ToolDefinition // computed once at construction for the non-dynamic path; exported for network subpackage
	maxAttachmentBytes  int64
	suspendCount        atomic.Int64
	suspendBytes        atomic.Int64
	suspendMu           sync.Mutex // guards check-then-add on suspendCount/suspendBytes
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int
	GenParams        *GenerationParams // exported for network subpackage access (avoid clash with GenerationParams type name)
	SpawnEnabled     bool              // exported for network subpackage access
	SpawnDepthLimit  int               // exported for network subpackage access (avoid clash with MaxSpawnDepth option func)
	DeniedSpawnTools []string          // exported for network subpackage access (avoid clash with DenySpawnTools option func)
	ActiveSkillInstructions string // built from WithActiveSkills during initCore; exported for network subpackage
}

// initCore initializes shared fields on an AgentCore from the given config.
// Called by NewLLMAgent and NewNetwork on the already-allocated parent struct.
// Uses field-by-field assignment to avoid copying sync primitives in agentMemory.
func InitCore(c *AgentCore, name, description string, provider Provider, cfg agentConfig) {
	c.name = name
	c.description = description
	c.LLMProvider = provider
	c.Tools = NewToolRegistry()
	c.processors = NewProcessorChain()
	c.systemPrompt = cfg.prompt
	c.MaxIter = defaultMaxIter
	if cfg.maxIter > 0 {
		c.MaxIter = cfg.maxIter
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

	for _, t := range cfg.tools {
		c.Tools.Add(t)
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

	c.Handler = cfg.inputHandler
	c.PlanExecution = cfg.planExecution
	c.sandbox = cfg.Sandbox
	c.responseSchema = cfg.responseSchema
	c.dynamicPrompt = cfg.dynamicPrompt
	c.dynamicModel = cfg.dynamicModel
	c.DynamicTools = cfg.dynamicTools
	c.tracer = cfg.tracer
	c.Logger = cfg.logger
	c.maxAttachmentBytes = cfg.maxAttachmentBytes
	c.maxSuspendSnapshots = cfg.maxSuspendSnapshots
	c.maxSuspendBytes = cfg.maxSuspendBytes
	c.compressModel = cfg.compressModel
	c.compressThreshold = cfg.compressThreshold
	c.GenParams = cfg.generationParams
	c.SpawnEnabled = cfg.spawnEnabled
	c.SpawnDepthLimit = cfg.maxSpawnDepth
	c.DeniedSpawnTools = cfg.denySpawnTools

	// Build active skill instructions block.
	if len(cfg.activeSkills) > 0 {
		var parts []string
		for _, s := range cfg.activeSkills {
			parts = append(parts, "## Skill: "+s.Name+"\n\n"+s.Instructions)
		}
		c.ActiveSkillInstructions = strings.Join(parts, "\n\n---\n\n")
	}
}

func (c *AgentCore) Name() string        { return c.name }
func (c *AgentCore) Description() string { return c.description }

// Close waits for all in-flight background persist goroutines to finish and
// releases any memory orchestrator resources. Call during shutdown to ensure
// the last messages are written to the store.
//
// Returns nil today; reserved for future flush errors. Embedders that wrap
// AgentCore inherit this signature.
func (c *AgentCore) Close() error { return c.mem.Close() }

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
// based on the agent's configuration. Exported for network subpackage access.
func (c *AgentCore) CacheBuiltinToolDefs(defs []ToolDefinition) []ToolDefinition {
	if c.Handler != nil {
		defs = append(defs, askUserToolDef())
	}
	if c.PlanExecution {
		defs = append(defs, executePlanToolDef())
	}
	if c.SpawnEnabled {
		defs = append(defs, spawnAgentToolDef())
	}
	return defs
}

// ResolvePromptAndProvider returns the effective prompt and provider for this request.
// Dynamic overrides take precedence over construction-time values.
// Exported for network subpackage access.
func (c *AgentCore) ResolvePromptAndProvider(ctx context.Context, task AgentTask) (string, Provider) {
	prompt := c.systemPrompt
	if c.dynamicPrompt != nil {
		prompt = c.dynamicPrompt(ctx, task)
	}
	provider := c.LLMProvider
	if c.dynamicModel != nil {
		provider = c.dynamicModel(ctx, task)
	}
	return prompt, provider
}

// ResolveDynamicTools returns tool definitions and an executor for a dynamic request.
// Returns nil, nil when dynamicTools is not configured (caller should use cached defs).
// Exported for network subpackage access.
func (c *AgentCore) ResolveDynamicTools(ctx context.Context, task AgentTask) ([]ToolDefinition, ToolExecFunc) {
	if c.DynamicTools == nil {
		return nil, nil
	}
	dynTools := c.DynamicTools(ctx, task)
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

// baseLoopConfig assembles a LoopConfig from resolved values.
// Callers provide the name prefix, resolved prompt/provider/tools, and dispatch function.
// BaseLoopConfig assembles a LoopConfig from resolved values.
// Callers provide the name prefix, resolved prompt/provider/tools, and dispatch function.
// Exported for network subpackage access.
func (c *AgentCore) BaseLoopConfig(name, prompt string, provider Provider, tools []ToolDefinition, dispatch DispatchFunc) LoopConfig {
	return LoopConfig{
		name:                name,
		provider:            provider,
		tools:               tools,
		processors:          c.processors,
		maxIter:             c.MaxIter,
		mem:                 &c.mem,
		inputHandler:        c.Handler,
		dispatch:            dispatch,
		systemPrompt:        prompt,
		responseSchema:      c.responseSchema,
		tracer:              c.tracer,
		logger:              c.Logger,
		maxAttachmentBytes:  c.maxAttachmentBytes,
		suspendCount:        &c.suspendCount,
		suspendBytes:        &c.suspendBytes,
		suspendMu:           &c.suspendMu,
		maxSuspendSnapshots: c.maxSuspendSnapshots,
		maxSuspendBytes:     c.maxSuspendBytes,
		compressModel:       c.compressModel,
		compressThreshold:   c.compressThreshold,
		generationParams:    c.GenParams,
	}
}

// ExecuteWithSpan wraps runLoop with tracing, logging, and input-received event emission.
// agentType is used in span attributes ("LLMAgent" or "Network").
// logKey is used in log messages ("agent" or "network").
// buildCfg constructs the LoopConfig; it receives the (possibly span-wrapped) context.
// Exported for network subpackage access.
func (c *AgentCore) ExecuteWithSpan(
	ctx context.Context,
	task AgentTask,
	ch chan<- StreamEvent,
	agentType, logKey string,
	buildCfg func(ctx context.Context, task AgentTask, ch chan<- StreamEvent) LoopConfig,
) (AgentResult, error) {
	// Emit input-received event so consumers know a task arrived.
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventInputReceived, Name: c.name, Content: task.Input}:
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

	c.Logger.Info(logKey+" started", logKey, c.name)
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
		c.Logger.Error(logKey+" failed", logKey, c.name,
			"error", err,
			"duration", time.Since(start),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens)
	} else {
		c.Logger.Info(logKey+" completed", logKey, c.name,
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
// Exported for network subpackage access.
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
			// Filter EventInputReceived from sub-agents — Network's
			// EventAgentStart is the canonical signal for delegation.
			if ev.Type == EventInputReceived {
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
