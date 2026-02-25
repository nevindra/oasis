package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const defaultMaxIter = 10

// agentCore holds fields shared by LLMAgent and Network.
// Both types embed this struct to eliminate field duplication.
// New agent-level options only need to be wired here once.
type agentCore struct {
	name             string
	description      string
	provider         Provider
	tools            *ToolRegistry
	processors       *ProcessorChain
	systemPrompt     string
	maxIter          int
	inputHandler     InputHandler
	planExecution    bool
	codeRunner       CodeRunner
	responseSchema   *ResponseSchema
	dynamicPrompt    PromptFunc
	dynamicModel     ModelFunc
	dynamicTools     ToolsFunc
	tracer           Tracer
	logger           *slog.Logger
	mem              agentMemory
	cachedToolDefs   []ToolDefinition // computed once at construction for the non-dynamic path
	maxAttachmentBytes  int64
	suspendCount        atomic.Int64
	suspendBytes        atomic.Int64
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int
	generationParams    *GenerationParams
}

// initCore initializes shared fields on an agentCore from the given config.
// Called by NewLLMAgent and NewNetwork on the already-allocated parent struct.
// Uses field-by-field assignment to avoid copying sync primitives in agentMemory.
func initCore(c *agentCore, name, description string, provider Provider, cfg agentConfig) {
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

	// Wire memory fields individually (agentMemory contains sync primitives).
	c.mem.store = cfg.store
	c.mem.embedding = cfg.embedding
	c.mem.memory = cfg.memory
	c.mem.crossThreadSearch = cfg.crossThreadSearch
	c.mem.semanticMinScore = cfg.semanticMinScore
	c.mem.maxHistory = cfg.maxHistory
	c.mem.maxTokens = cfg.maxTokens
	c.mem.autoTitle = cfg.autoTitle
	c.mem.provider = provider
	c.mem.semanticTrimming = cfg.semanticTrimming
	c.mem.trimmingEmbedding = cfg.trimmingEmbedding
	c.mem.keepRecent = cfg.keepRecent
	c.mem.tracer = cfg.tracer
	c.mem.logger = cfg.logger

	for _, t := range cfg.tools {
		c.tools.Add(t)
	}
	for _, p := range cfg.processors {
		c.processors.Add(p)
	}

	c.inputHandler = cfg.inputHandler
	c.planExecution = cfg.planExecution
	c.codeRunner = cfg.codeRunner
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
	c.generationParams = cfg.generationParams
}

func (c *agentCore) Name() string        { return c.name }
func (c *agentCore) Description() string { return c.description }

// Drain waits for all in-flight background persist goroutines to finish.
// Call during shutdown to ensure the last messages are written to the store.
func (c *agentCore) Drain() { c.mem.drain() }

// cacheBuiltinToolDefs appends built-in tool definitions (ask_user, execute_plan,
// execute_code) based on the agent's configuration.
func (c *agentCore) cacheBuiltinToolDefs(defs []ToolDefinition) []ToolDefinition {
	if c.inputHandler != nil {
		defs = append(defs, askUserToolDef)
	}
	if c.planExecution {
		defs = append(defs, executePlanToolDef)
	}
	if c.codeRunner != nil {
		defs = append(defs, executeCodeToolDef)
	}
	return defs
}

// resolvePromptAndProvider returns the effective prompt and provider for this request.
// Dynamic overrides take precedence over construction-time values.
func (c *agentCore) resolvePromptAndProvider(ctx context.Context, task AgentTask) (string, Provider) {
	prompt := c.systemPrompt
	if c.dynamicPrompt != nil {
		prompt = c.dynamicPrompt(ctx, task)
	}
	provider := c.provider
	if c.dynamicModel != nil {
		provider = c.dynamicModel(ctx, task)
	}
	return prompt, provider
}

// resolveDynamicTools returns tool definitions and an executor for a dynamic request.
// Returns nil, nil when dynamicTools is not configured (caller should use cached defs).
func (c *agentCore) resolveDynamicTools(ctx context.Context, task AgentTask) ([]ToolDefinition, toolExecFunc) {
	if c.dynamicTools == nil {
		return nil, nil
	}
	dynTools := c.dynamicTools(ctx, task)
	var toolDefs []ToolDefinition
	index := make(map[string]Tool, len(dynTools))
	for _, t := range dynTools {
		for _, d := range t.Definitions() {
			toolDefs = append(toolDefs, d)
			index[d.Name] = t
		}
	}
	executeTool := func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
		if t, ok := index[name]; ok {
			return t.Execute(ctx, name, args)
		}
		return ToolResult{Error: "unknown tool: " + name}, nil
	}
	return toolDefs, executeTool
}

// baseLoopConfig assembles a loopConfig from resolved values.
// Callers provide the name prefix, resolved prompt/provider/tools, and dispatch function.
func (c *agentCore) baseLoopConfig(name, prompt string, provider Provider, tools []ToolDefinition, dispatch DispatchFunc) loopConfig {
	return loopConfig{
		name:                name,
		provider:            provider,
		tools:               tools,
		processors:          c.processors,
		maxIter:             c.maxIter,
		mem:                 &c.mem,
		inputHandler:        c.inputHandler,
		dispatch:            dispatch,
		systemPrompt:        prompt,
		responseSchema:      c.responseSchema,
		tracer:              c.tracer,
		logger:              c.logger,
		maxAttachmentBytes:  c.maxAttachmentBytes,
		suspendCount:        &c.suspendCount,
		suspendBytes:        &c.suspendBytes,
		maxSuspendSnapshots: c.maxSuspendSnapshots,
		maxSuspendBytes:     c.maxSuspendBytes,
		compressModel:       c.compressModel,
		compressThreshold:   c.compressThreshold,
		generationParams:    c.generationParams,
	}
}

// executeWithSpan wraps runLoop with tracing, logging, and input-received event emission.
// agentType is used in span attributes ("LLMAgent" or "Network").
// logKey is used in log messages ("agent" or "network").
// buildCfg constructs the loopConfig; it receives the (possibly span-wrapped) context.
func (c *agentCore) executeWithSpan(
	ctx context.Context,
	task AgentTask,
	ch chan<- StreamEvent,
	agentType, logKey string,
	buildCfg func(ctx context.Context, task AgentTask, ch chan<- StreamEvent) loopConfig,
) (AgentResult, error) {
	// Emit input-received event so consumers know a task arrived.
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventInputReceived, Name: c.name, Content: task.Input}:
		case <-ctx.Done():
			return AgentResult{}, ctx.Err()
		}
	}

	if c.tracer != nil {
		var span Span
		ctx, span = c.tracer.Start(ctx, "agent.execute",
			StringAttr("agent.name", c.name),
			StringAttr("agent.type", agentType))
		defer span.End()

		c.logger.Info(logKey+" started", logKey, c.name)
		result, err := runLoop(ctx, buildCfg(ctx, task, ch), task, ch)

		span.SetAttr(
			IntAttr("tokens.input", result.Usage.InputTokens),
			IntAttr("tokens.output", result.Usage.OutputTokens))
		if err != nil {
			span.Error(err)
			span.SetAttr(StringAttr("agent.status", "error"))
		} else {
			span.SetAttr(StringAttr("agent.status", "ok"))
		}
		c.logger.Info(logKey+" completed", logKey, c.name,
			"status", statusStr(err),
			"tokens.input", result.Usage.InputTokens,
			"tokens.output", result.Usage.OutputTokens)
		return result, err
	}
	return runLoop(ctx, buildCfg(ctx, task, ch), task, ch)
}

func statusStr(err error) string {
	if err != nil {
		return "error"
	}
	return "ok"
}

// --- Shared agent dispatch helpers ---

// executeAgent runs a subagent with panic recovery. When ch is non-nil and
// the agent implements StreamingAgent, events are forwarded through the parent
// channel in real time via forwardSubagentStream.
func executeAgent(ctx context.Context, agent Agent, agentName string, task AgentTask, ch chan<- StreamEvent, logger *slog.Logger) (result AgentResult, err error) {
	if ch != nil {
		if sa, ok := agent.(StreamingAgent); ok {
			return forwardSubagentStream(ctx, sa, agentName, task, ch, logger)
		}
	}
	// Non-streaming path: Execute with panic recovery.
	defer func() {
		if p := recover(); p != nil {
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
// Safe to call multiple times; subsequent calls are no-ops.
func onceClose[T any](ch chan T) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			defer func() { recover() }()
			close(ch)
		})
	}
}

func safeAgentError(agentName string, p any) error {
	return fmt.Errorf("subagent %q panic: %v", agentName, p)
}
