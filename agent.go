package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Agent is a unit of work that takes a task and returns a result.
// Implementations range from single LLM tool-calling agents (LLMAgent)
// to multi-agent coordinators (Network).
type Agent interface {
	// Name returns the agent's identifier.
	Name() string
	// Description returns a human-readable description of what the agent does.
	// Used by Network to generate tool definitions for the routing LLM.
	Description() string
	// Execute runs the agent on the given task and returns a result.
	Execute(ctx context.Context, task AgentTask) (AgentResult, error)
}

// StreamingAgent is an optional capability for agents that support event streaming.
// Check via type assertion: if sa, ok := agent.(StreamingAgent); ok { ... }
type StreamingAgent interface {
	Agent
	// ExecuteStream runs the agent like Execute, but emits StreamEvent values
	// into ch throughout execution. Events include text deltas, tool call
	// start/result, and agent start/finish (for Networks). The channel is
	// closed when streaming completes.
	ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error)
}

// AgentTask is the input to an Agent.
type AgentTask struct {
	// Input is the natural language task description.
	Input string
	// Attachments carries optional multimodal content (photos, PDFs, documents, etc.) to pass to the LLM.
	// Providers that support multimodal input will attach these to the user message as inline data.
	// Providers that don't support it will ignore this field.
	Attachments []Attachment
	// Context carries optional metadata (thread ID, user ID, etc.).
	// Use the Context* constants as keys and the Task* accessors for type-safe reads.
	Context map[string]any
}

// Context key constants for AgentTask.Context.
const (
	// ContextThreadID identifies the conversation thread.
	ContextThreadID = "thread_id"
	// ContextUserID identifies the user.
	ContextUserID = "user_id"
	// ContextChatID identifies the chat/channel.
	ContextChatID = "chat_id"
)

// TaskThreadID returns the thread ID from task context, or "" if absent.
func (t AgentTask) TaskThreadID() string {
	if v, ok := t.Context[ContextThreadID].(string); ok {
		return v
	}
	return ""
}

// TaskUserID returns the user ID from task context, or "" if absent.
func (t AgentTask) TaskUserID() string {
	if v, ok := t.Context[ContextUserID].(string); ok {
		return v
	}
	return ""
}

// TaskChatID returns the chat ID from task context, or "" if absent.
func (t AgentTask) TaskChatID() string {
	if v, ok := t.Context[ContextChatID].(string); ok {
		return v
	}
	return ""
}

// AgentResult is the output of an Agent.
type AgentResult struct {
	// Output is the agent's final response text.
	Output string
	// Attachments carries optional multimodal content (images, audio, etc.) from the LLM response.
	// Populated when the provider returns media alongside or instead of text.
	Attachments []Attachment
	// Usage tracks aggregate token usage across all LLM calls.
	Usage Usage
	// Steps records per-tool and per-agent execution traces in chronological order.
	// Populated by LLMAgent (tool calls) and Network (tool + agent delegations).
	// Nil when no tools were called.
	Steps []StepTrace
}

// StepTrace records the execution of a single tool call or agent delegation.
// Collected automatically during the agent's tool-calling loop.
type StepTrace struct {
	// Name is the tool or agent name (e.g. "web_search", "researcher").
	// For agent delegations, the "agent_" prefix is stripped.
	Name string `json:"name"`
	// Type is "tool" or "agent".
	Type string `json:"type"`
	// Input is the tool arguments or agent task, truncated to 200 characters.
	Input string `json:"input"`
	// Output is the result content, truncated to 500 characters.
	Output string `json:"output"`
	// Usage is the token usage for this individual step.
	Usage Usage `json:"usage"`
	// Duration is the wall-clock time for this step.
	Duration time.Duration `json:"duration"`
}

// agentConfig holds shared configuration for LLMAgent and Network.
type agentConfig struct {
	tools            []Tool
	agents           []Agent
	prompt           string
	maxIter          int
	processors       []any
	inputHandler     InputHandler
	store            Store
	embedding        EmbeddingProvider
	memory           MemoryStore
	crossThreadSearch bool    // enabled by CrossThreadSearch option
	semanticMinScore  float32 // set by MinScore inside CrossThreadSearch
	maxHistory        int     // set by MaxHistory inside WithConversationMemory
	maxTokens         int     // set by MaxTokens inside WithConversationMemory
	autoTitle         bool    // set by AutoTitle inside WithConversationMemory
	planExecution     bool            // enabled by WithPlanExecution option
	codeRunner        CodeRunner      // set by WithCodeExecution option
	responseSchema    *ResponseSchema // set by WithResponseSchema option
	dynamicPrompt     PromptFunc      // set by WithDynamicPrompt option
	dynamicModel      ModelFunc       // set by WithDynamicModel option
	dynamicTools       ToolsFunc       // set by WithDynamicTools option
	tracer             Tracer          // set by WithTracer option
	logger             *slog.Logger    // set by WithLogger option
	maxAttachmentBytes  int64          // set by WithMaxAttachmentBytes option
	maxSuspendSnapshots int            // set by WithSuspendBudget
	maxSuspendBytes     int64          // set by WithSuspendBudget
	compressModel       ModelFunc      // set by WithCompressModel
	compressThreshold   int            // set by WithCompressThreshold
}

// AgentOption configures an LLMAgent or Network.
type AgentOption func(*agentConfig)

// PromptFunc resolves the system prompt per-request.
// When set via WithDynamicPrompt, it is called at the start of every
// Execute/ExecuteStream call. The returned string replaces the static
// WithPrompt value for that execution.
type PromptFunc func(ctx context.Context, task AgentTask) string

// ModelFunc resolves the LLM provider per-request.
// When set via WithDynamicModel, it is called at the start of every
// Execute/ExecuteStream call. The returned Provider replaces the
// construction-time provider for that execution.
type ModelFunc func(ctx context.Context, task AgentTask) Provider

// ToolsFunc resolves the tool set per-request.
// When set via WithDynamicTools, it is called at the start of every
// Execute/ExecuteStream call. The returned tools REPLACE (not append to)
// the construction-time tools for that execution.
type ToolsFunc func(ctx context.Context, task AgentTask) []Tool

// WithTools adds tools to the agent or network.
func WithTools(tools ...Tool) AgentOption {
	return func(c *agentConfig) { c.tools = append(c.tools, tools...) }
}

// WithPrompt sets the system prompt for the agent or network router.
func WithPrompt(s string) AgentOption {
	return func(c *agentConfig) { c.prompt = s }
}

// WithMaxIter sets the maximum tool-calling iterations.
func WithMaxIter(n int) AgentOption {
	return func(c *agentConfig) { c.maxIter = n }
}

// WithMaxAttachmentBytes sets the maximum total bytes of attachments
// accumulated from tool results during the execution loop. Default is 50 MB.
// Zero means use the default.
func WithMaxAttachmentBytes(n int64) AgentOption {
	return func(c *agentConfig) { c.maxAttachmentBytes = n }
}

// WithSuspendBudget sets per-agent limits on concurrent suspended snapshots.
// maxSnapshots caps the number of active suspensions. maxBytes caps total
// estimated memory held by snapshot closures. Defaults: 20 snapshots, 256 MB.
// When either limit is exceeded, new suspensions are rejected (the underlying
// processor error is returned instead of ErrSuspended).
func WithSuspendBudget(maxSnapshots int, maxBytes int64) AgentOption {
	return func(c *agentConfig) {
		c.maxSuspendSnapshots = maxSnapshots
		c.maxSuspendBytes = maxBytes
	}
}

// WithCompressModel sets a per-request provider for context compression.
// When the message history exceeds the compress threshold, older tool results
// are summarized using this provider. Falls back to the agent's main provider
// when nil.
func WithCompressModel(fn ModelFunc) AgentOption {
	return func(c *agentConfig) { c.compressModel = fn }
}

// WithCompressThreshold sets the rune count at which context compression
// triggers. When the total message content exceeds this threshold, older
// tool results are summarized via an LLM call. Default is 200,000 runes
// (~50K tokens). Negative value disables compression.
func WithCompressThreshold(n int) AgentOption {
	return func(c *agentConfig) { c.compressThreshold = n }
}

// WithAgents adds subagents to a Network. Ignored by LLMAgent.
func WithAgents(agents ...Agent) AgentOption {
	return func(c *agentConfig) { c.agents = append(c.agents, agents...) }
}

// WithPlanExecution enables the built-in "execute_plan" tool that batches
// multiple tool calls in a single LLM turn. The LLM can call execute_plan
// with an array of steps (each specifying a tool name and arguments), and
// the framework executes all steps in parallel without re-sampling the LLM
// between each call. Returns structured per-step results.
//
// This reduces latency and token usage for fan-out patterns where the LLM
// needs to call the same or different tools multiple times with known inputs.
func WithPlanExecution() AgentOption {
	return func(c *agentConfig) { c.planExecution = true }
}

// WithCodeExecution enables the built-in "execute_code" tool that lets the LLM
// write and execute Python code in a sandboxed subprocess. The code has access
// to all agent tools via call_tool(name, args) and call_tools_parallel(calls).
//
// This complements WithPlanExecution: use execute_plan for simple parallel
// fan-out, use execute_code for complex logic (conditionals, loops, data flow).
func WithCodeExecution(runner CodeRunner) AgentOption {
	return func(c *agentConfig) { c.codeRunner = runner }
}

// WithResponseSchema sets the response schema for structured JSON output.
// When set, the provider enforces structured output matching the schema.
// Providers translate this to their native mechanism (e.g. Gemini responseSchema,
// OpenAI response_format).
func WithResponseSchema(s *ResponseSchema) AgentOption {
	return func(c *agentConfig) { c.responseSchema = s }
}

// WithDynamicPrompt sets a per-request prompt resolution function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value is used as the system prompt for that execution.
// Overrides WithPrompt when set. If the function returns "", no system prompt
// is used (same as omitting WithPrompt).
func WithDynamicPrompt(fn PromptFunc) AgentOption {
	return func(c *agentConfig) { c.dynamicPrompt = fn }
}

// WithDynamicModel sets a per-request model selection function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value is used as the LLM provider for that execution.
// Overrides the construction-time provider when set.
func WithDynamicModel(fn ModelFunc) AgentOption {
	return func(c *agentConfig) { c.dynamicModel = fn }
}

// WithDynamicTools sets a per-request tool selection function.
// When set, the function is called at the start of every Execute/ExecuteStream
// call, and its return value REPLACES the construction-time tools for that
// execution. To remove all tools for a request, return nil or an empty slice.
func WithDynamicTools(fn ToolsFunc) AgentOption {
	return func(c *agentConfig) { c.dynamicTools = fn }
}

// WithTracer sets the tracer for the agent. When set, the agent emits
// spans for execution, memory, and loop operations. Use observer.NewTracer()
// for an OTEL-backed implementation.
func WithTracer(t Tracer) AgentOption {
	return func(c *agentConfig) { c.tracer = t }
}

// WithLogger sets the structured logger for the agent. When set, replaces
// all log.Printf calls with structured slog output. If not set, a no-op
// logger is used (no output).
func WithLogger(l *slog.Logger) AgentOption {
	return func(c *agentConfig) { c.logger = l }
}

// WithProcessors adds processors to the agent's execution pipeline.
// Each processor must implement at least one of PreProcessor, PostProcessor,
// or PostToolProcessor. Processors run in registration order at their
// respective hook points during Execute().
func WithProcessors(processors ...any) AgentOption {
	return func(c *agentConfig) { c.processors = append(c.processors, processors...) }
}

// WithInputHandler sets the handler for human-in-the-loop interactions.
// When set, the agent gains an "ask_user" tool (LLM-driven) and processors
// can access the handler via InputHandlerFromContext(ctx).
func WithInputHandler(h InputHandler) AgentOption {
	return func(c *agentConfig) { c.inputHandler = h }
}

// ConversationOption configures conversation memory behavior.
// Pass to WithConversationMemory to enable optional features like cross-thread search.
type ConversationOption func(*agentConfig)

// CrossThreadSearch enables semantic recall across all conversation threads.
// When the agent receives a message, it embeds the input and searches all
// stored messages for semantically similar content from other threads.
// The embedding provider is required (compile-time enforced) and is also used
// to embed messages before storing them for future recall.
//
// Optional SemanticOption values tune recall behavior:
//
//	oasis.CrossThreadSearch(embedding)                    // default threshold (0.60)
//	oasis.CrossThreadSearch(embedding, oasis.MinScore(0.7)) // custom threshold
func CrossThreadSearch(e EmbeddingProvider, opts ...SemanticOption) ConversationOption {
	return func(c *agentConfig) {
		c.crossThreadSearch = true
		c.embedding = e
		for _, o := range opts {
			o(c)
		}
	}
}

// SemanticOption tunes semantic search parameters within CrossThreadSearch.
type SemanticOption func(*agentConfig)

// MinScore sets the minimum cosine similarity score for cross-thread semantic
// recall. Messages with a score below this threshold are silently dropped
// before being injected into the LLM context. The zero value (or omitting this
// option) uses a built-in default of 0.60.
func MinScore(score float32) SemanticOption {
	return func(c *agentConfig) { c.semanticMinScore = score }
}

// MaxHistory sets the maximum number of recent messages loaded from conversation
// history before the LLM call. The zero value (or omitting this option) uses
// a built-in default of 10.
func MaxHistory(n int) ConversationOption {
	return func(c *agentConfig) { c.maxHistory = n }
}

// MaxTokens sets a token budget for conversation history loaded before the LLM call.
// Messages are trimmed oldest-first until the total estimated tokens fit within n.
// Composes with MaxHistory — both limits apply, whichever triggers first.
// The zero value (or omitting this option) disables token-based trimming.
func MaxTokens(n int) ConversationOption {
	return func(c *agentConfig) { c.maxTokens = n }
}

// AutoTitle enables automatic thread title generation. When set, the agent
// generates a short title from the first user message and stores it on the
// thread. Titles are only generated once per thread (skipped if the thread
// already has a title). Runs in the background alongside message persistence.
func AutoTitle() ConversationOption {
	return func(c *agentConfig) { c.autoTitle = true }
}

// WithConversationMemory enables conversation history on the agent.
// When set and task.Context["thread_id"] is present, the agent loads
// recent messages before the LLM call and persists the exchange afterward.
//
// Optional ConversationOption values enable additional features:
//
//	oasis.WithConversationMemory(store)                                                  // history only
//	oasis.WithConversationMemory(store, oasis.MaxHistory(30))                            // custom history limit
//	oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding))              // + cross-thread recall
//	oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding, oasis.MinScore(0.7))) // + custom threshold
func WithConversationMemory(s Store, opts ...ConversationOption) AgentOption {
	return func(c *agentConfig) {
		c.store = s
		for _, o := range opts {
			o(c)
		}
	}
}

// WithUserMemory enables the full user memory pipeline: read + write.
//
// Read (every Execute call): embeds the input, retrieves relevant facts via
// BuildContext, and appends them to the system prompt.
//
// Write (after each turn, background): uses the agent's own LLM to extract
// durable user facts from the conversation exchange and persists them via
// UpsertFact. Write requires WithConversationMemory — without it, extraction
// is silently skipped (logged as a warning at construction time).
func WithUserMemory(m MemoryStore, e EmbeddingProvider) AgentOption {
	return func(c *agentConfig) {
		c.memory = m
		c.embedding = e
	}
}


// nopLogger is a logger that discards all output. Used when WithLogger is not set.
var nopLogger = slog.New(discardHandler{})

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool  { return false }
func (discardHandler) Handle(context.Context, slog.Record) error { return nil }
func (d discardHandler) WithAttrs([]slog.Attr) slog.Handler      { return d }
func (d discardHandler) WithGroup(string) slog.Handler            { return d }

func buildConfig(opts []AgentOption) agentConfig {
	var c agentConfig
	for _, opt := range opts {
		opt(&c)
	}
	if c.logger == nil {
		c.logger = nopLogger
	}
	// Warn about misconfigurations that can't be caught at compile time.
	if c.memory != nil && c.store == nil {
		c.logger.Warn("WithUserMemory without WithConversationMemory — fact extraction (write) will be silently skipped")
	}
	return c
}

// --- shared execution loop ---

// DispatchResult holds the result of a single tool or agent dispatch.
type DispatchResult struct {
	Content     string
	Usage       Usage
	Attachments []Attachment
	// IsError signals that Content represents an error message rather than
	// a successful tool result. This enables structural error detection
	// without relying on string-prefix heuristics.
	IsError bool
}

// DispatchFunc executes a single tool call and returns the result.
// LLMAgent provides one that calls ToolRegistry.Execute + ask_user.
// Network provides one that also routes to subagents via the agent_* prefix.
type DispatchFunc func(ctx context.Context, tc ToolCall) DispatchResult

// toolExecFunc executes a tool by name. Abstracts ToolRegistry.Execute so
// dispatch functions work without an intermediate registry allocation.
type toolExecFunc = func(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)

// dispatchBuiltins handles the built-in special-case tools (ask_user, execute_plan,
// execute_code). Returns (result, true) if the call was handled, or (zero, false)
// if the caller should proceed with its own routing (agent delegation, direct tools).
func dispatchBuiltins(ctx context.Context, tc ToolCall, dispatch DispatchFunc, ih InputHandler, agentName string, planExec bool, codeRunner CodeRunner) (DispatchResult, bool) {
	if tc.Name == "ask_user" && ih != nil {
		content, err := executeAskUser(ctx, ih, agentName, tc)
		if err != nil {
			return DispatchResult{Content: "error: " + err.Error(), IsError: true}, true
		}
		return DispatchResult{Content: content}, true
	}
	if tc.Name == "execute_plan" && planExec {
		return executePlan(ctx, tc.Args, dispatch), true
	}
	if tc.Name == "execute_code" && codeRunner != nil {
		// Wrap dispatch to block execute_plan/execute_code calls from within code,
		// preventing unbounded recursion via execute_code → execute_plan → execute_code.
		safeDispatch := func(ctx context.Context, tc ToolCall) DispatchResult {
			if tc.Name == "execute_plan" || tc.Name == "execute_code" {
				return DispatchResult{Content: "error: " + tc.Name + " cannot be called from within execute_code", IsError: true}
			}
			return dispatch(ctx, tc)
		}
		return executeCode(ctx, tc.Args, codeRunner, safeDispatch), true
	}
	return DispatchResult{}, false
}

// dispatchTool executes a tool via the given executor and converts the result
// to a DispatchResult. Shared by LLMAgent and Network for the common tool path.
func dispatchTool(ctx context.Context, executeTool toolExecFunc, name string, args json.RawMessage) DispatchResult {
	result, err := executeTool(ctx, name, args)
	if err != nil {
		return DispatchResult{Content: "error: " + err.Error(), IsError: true}
	}
	if result.Error != "" {
		return DispatchResult{Content: "error: " + result.Error, IsError: true}
	}
	return DispatchResult{Content: result.Content}
}

// loopConfig holds everything the shared runLoop needs to run.
type loopConfig struct {
	name           string           // for logging (e.g. "agent:foo", "network:bar")
	provider       Provider
	tools          []ToolDefinition // pre-built tool defs (including ask_user if applicable)
	processors     *ProcessorChain
	maxIter        int
	mem            *agentMemory
	inputHandler   InputHandler
	dispatch       DispatchFunc
	systemPrompt   string
	resumeMessages []ChatMessage    // if set, replaces buildMessages (used by suspend/resume)
	responseSchema *ResponseSchema  // if set, attached to every ChatRequest
	tracer              Tracer           // nil = no tracing
	logger              *slog.Logger     // never nil (nopLogger fallback)
	maxAttachmentBytes  int64            // attachment size budget (0 = default 50MB)
	suspendCount        *atomic.Int64    // nil = no budget tracking
	suspendBytes        *atomic.Int64
	maxSuspendSnapshots int
	maxSuspendBytes     int64
	compressModel       ModelFunc
	compressThreshold   int // 0 = default (200K runes), negative = disabled
}

// runLoop is the shared tool-calling loop used by both LLMAgent and Network.
// When ch is nil, it operates in blocking mode (Execute). When ch is non-nil,
// it emits StreamEvent values and closes ch when done (ExecuteStream).
func runLoop(ctx context.Context, cfg loopConfig, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	var totalUsage Usage
	var steps []StepTrace

	// safeCloseCh closes the streaming channel exactly once. All exit paths
	// use this instead of raw close(ch), preventing double-close panics if
	// a provider's ChatStream also closes the channel internally.
	var closeOnce sync.Once
	safeCloseCh := func() {
		if ch != nil {
			closeOnce.Do(func() {
				defer func() { recover() }()
				close(ch)
			})
		}
	}

	// Inject InputHandler into context for processors.
	if cfg.inputHandler != nil {
		ctx = WithInputHandlerContext(ctx, cfg.inputHandler)
	}

	// Build initial messages (system prompt + user memory + history + user input).
	// If resumeMessages is set (suspend/resume), use those instead.
	var messages []ChatMessage
	if len(cfg.resumeMessages) > 0 {
		messages = cfg.resumeMessages
	} else {
		messages = cfg.mem.buildMessages(ctx, cfg.name, cfg.systemPrompt, task)
	}

	// Emit processing-start event after context is built, before the loop.
	if ch != nil {
		select {
		case ch <- StreamEvent{Type: EventProcessingStart, Name: cfg.name}:
		case <-ctx.Done():
			safeCloseCh()
			return AgentResult{Usage: totalUsage}, ctx.Err()
		}
	}

	// lastAgentOutput tracks the most recent sub-agent result so we can fall
	// back to it when the router produces an empty final response (common for
	// pure-routing LLMs that don't synthesize a reply after delegating).
	// For LLMAgent this is never set (no agent_* tools).
	attachByteBudget := cfg.maxAttachmentBytes
	if attachByteBudget <= 0 {
		attachByteBudget = maxAccumulatedAttachmentBytes
	}

	// Track message rune count for compression.
	var messageRuneCount int
	for _, m := range messages {
		messageRuneCount += len([]rune(m.Content))
	}
	compressThreshold := cfg.compressThreshold
	if compressThreshold == 0 {
		compressThreshold = defaultCompressThreshold
	}

	var lastAgentOutput string
	var accumulatedAttachments []Attachment
	var accumulatedAttachmentBytes int64

	for i := 0; i < cfg.maxIter; i++ {
		// Start an iteration span if tracing is enabled.
		iterCtx := ctx
		var iterSpan Span
		if cfg.tracer != nil {
			iterCtx, iterSpan = cfg.tracer.Start(ctx, "agent.loop.iteration",
				IntAttr("iteration", i),
				BoolAttr("has_tools", len(cfg.tools) > 0))
		}
		endIter := func() {
			if iterSpan != nil {
				iterSpan.End()
			}
		}

		req := ChatRequest{Messages: messages, ResponseSchema: cfg.responseSchema}

		// PreProcessor hook.
		if err := cfg.processors.RunPreLLM(iterCtx, &req); err != nil {
			endIter()
			safeCloseCh()
			if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, s
			}
			return handleProcessorErrorWithSteps(err, totalUsage, steps)
		}

		var resp ChatResponse
		var err error

		if len(cfg.tools) > 0 {
			resp, err = cfg.provider.ChatWithTools(iterCtx, req, cfg.tools)
		} else if ch != nil {
			// No tools, streaming — stream the response directly.
			resp, err = cfg.provider.ChatStream(iterCtx, req, ch)
			if err != nil {
				endIter()
				safeCloseCh()
				return AgentResult{Usage: totalUsage, Steps: steps}, err
			}
			totalUsage.InputTokens += resp.Usage.InputTokens
			totalUsage.OutputTokens += resp.Usage.OutputTokens

			// PostProcessor hook (response already streamed, but processors
			// still run for side effects like logging and validation).
			if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
				endIter()
				safeCloseCh()
				if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
					return AgentResult{Usage: totalUsage, Steps: steps}, s
				}
				return handleProcessorErrorWithSteps(err, totalUsage, steps)
			}

			endIter()
			safeCloseCh()
			cfg.mem.persistMessages(iterCtx, cfg.name, task, task.Input, resp.Content, steps)
			return AgentResult{Output: resp.Content, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
		} else {
			resp, err = cfg.provider.Chat(iterCtx, req)
		}

		if err != nil {
			endIter()
			safeCloseCh()
			return AgentResult{Usage: totalUsage, Steps: steps}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		// PostProcessor hook.
		if err := cfg.processors.RunPostLLM(iterCtx, &resp); err != nil {
			endIter()
			safeCloseCh()
			if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, s
			}
			return handleProcessorErrorWithSteps(err, totalUsage, steps)
		}

		// No tool calls — final response.
		if len(resp.ToolCalls) == 0 {
			content := resp.Content
			if content == "" {
				content = lastAgentOutput
			}
			if ch != nil {
				// Only emit text-delta if no sub-agent already streamed.
				// When a Network delegates to a streaming sub-agent, its
				// text-delta events are forwarded to the parent channel in
				// real time. The router's final response (echo, paraphrase,
				// or empty) would duplicate the content consumers already
				// received. Skip the delta entirely; AgentResult.Output
				// still carries the correct final text for non-streaming use.
				if lastAgentOutput == "" {
					select {
					case ch <- StreamEvent{Type: EventTextDelta, Content: content}:
					case <-ctx.Done():
					}
				}
			}
			safeCloseCh()
			endIter()
			cfg.mem.persistMessages(iterCtx, cfg.name, task, task.Input, content, steps)
			return AgentResult{Output: content, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
		}

		if iterSpan != nil {
			iterSpan.SetAttr(IntAttr("tool_count", len(resp.ToolCalls)))
		}

		// Append assistant message with tool calls.
		messages = append(messages, ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		messageRuneCount += len([]rune(resp.Content))

		// Emit tool-call-start events before dispatch.
		if ch != nil {
			for _, tc := range resp.ToolCalls {
				select {
				case ch <- StreamEvent{Type: EventToolCallStart, Name: tc.Name, Args: tc.Args}:
				case <-ctx.Done():
				}
			}
		}

		// Execute tool calls in parallel.
		results := dispatchParallel(iterCtx, resp.ToolCalls, cfg.dispatch)

		// Process results sequentially (PostToolProcessor + message assembly + trace collection).
		for j, tc := range resp.ToolCalls {
			totalUsage.InputTokens += results[j].usage.InputTokens
			totalUsage.OutputTokens += results[j].usage.OutputTokens

			// Emit tool-call-result event.
			if ch != nil {
				select {
				case ch <- StreamEvent{
					Type:     EventToolCallResult,
					Name:     tc.Name,
					Content:  results[j].content,
					Usage:    results[j].usage,
					Duration: results[j].duration,
				}:
				case <-ctx.Done():
				}
			}

			// Build step trace.
			trace := buildStepTrace(tc, results[j])
			steps = append(steps, trace)

			// Accumulate attachments from sub-agent results (e.g. image generation).
			// Capped by both count and total byte size to prevent unbounded memory growth.
			for _, a := range results[j].attachments {
				aSize := int64(len(a.Data))
				if len(accumulatedAttachments) >= maxAccumulatedAttachments ||
					accumulatedAttachmentBytes+aSize > attachByteBudget {
					break
				}
				accumulatedAttachments = append(accumulatedAttachments, a)
				accumulatedAttachmentBytes += aSize
			}

			result := ToolResult{Content: results[j].content}
			if err := cfg.processors.RunPostTool(iterCtx, tc, &result); err != nil {
				endIter()
				safeCloseCh()
				if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
					return AgentResult{Usage: totalUsage, Steps: steps}, s
				}
				return handleProcessorErrorWithSteps(err, totalUsage, steps)
			}
			// Truncate large tool results before appending to message history
			// to prevent unbounded memory growth across iterations. Stream
			// events and step traces retain the full content (transient).
			msgContent := result.Content
			if len([]rune(msgContent)) > maxToolResultMessageLen {
				msgContent = truncateStr(msgContent, maxToolResultMessageLen) + "\n\n[output truncated — original was longer]"
			}
			messages = append(messages, ToolResultMessage(tc.ID, msgContent))
			messageRuneCount += len([]rune(msgContent))

			// Track the last sub-agent output for fallback.
			if strings.HasPrefix(tc.Name, "agent_") {
				lastAgentOutput = result.Content
			}
		}
		endIter()

		// Compress context if over budget.
		if compressThreshold > 0 && messageRuneCount > compressThreshold {
			messages, messageRuneCount = compressMessages(ctx, cfg, task, messages, 2)
		}
	}

	// Max iterations — force synthesis.
	cfg.logger.Warn("max iterations reached, forcing synthesis", "agent", cfg.name, "iteration", cfg.maxIter)
	messages = append(messages, UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))

	// Start a synthesis span so the forced-response LLM call is visible in traces.
	synthCtx := ctx
	if cfg.tracer != nil {
		var synthSpan Span
		synthCtx, synthSpan = cfg.tracer.Start(ctx, "agent.loop.synthesis",
			IntAttr("iteration", cfg.maxIter),
			BoolAttr("forced", true))
		defer synthSpan.End()
	}

	var resp ChatResponse
	var err error
	if ch != nil {
		resp, err = cfg.provider.ChatStream(synthCtx, ChatRequest{Messages: messages}, ch)
	} else {
		resp, err = cfg.provider.Chat(synthCtx, ChatRequest{Messages: messages})
	}
	if err != nil {
		safeCloseCh()
		return AgentResult{Usage: totalUsage, Steps: steps}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	// PostProcessor hook.
	if err := cfg.processors.RunPostLLM(synthCtx, &resp); err != nil {
		safeCloseCh()
		if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
			return AgentResult{Usage: totalUsage, Steps: steps}, s
		}
		return handleProcessorErrorWithSteps(err, totalUsage, steps)
	}

	safeCloseCh()
	cfg.mem.persistMessages(synthCtx, cfg.name, task, task.Input, resp.Content, steps)
	return AgentResult{Output: resp.Content, Attachments: mergeAttachments(accumulatedAttachments, resp.Attachments), Usage: totalUsage, Steps: steps}, nil
}

// mergeAttachments combines accumulated sub-agent attachments with the final
// response attachments. Accumulated attachments come first (from tool calls
// during the loop), followed by any attachments from the final LLM response.
func mergeAttachments(accumulated, resp []Attachment) []Attachment {
	if len(accumulated) == 0 {
		return resp
	}
	if len(resp) == 0 {
		return accumulated
	}
	merged := make([]Attachment, 0, len(accumulated)+len(resp))
	merged = append(merged, accumulated...)
	merged = append(merged, resp...)
	return merged
}

// runeCount returns the total rune count of all message content.
func runeCount(messages []ChatMessage) int {
	var n int
	for _, m := range messages {
		n += len([]rune(m.Content))
	}
	return n
}

// compressMessages summarizes old tool-result messages via an LLM call.
// Keeps the last preserveIters iterations of tool results intact.
// Returns the compressed message slice and new rune count, or the
// original slice on error (degrade, don't die).
func compressMessages(ctx context.Context, cfg loopConfig, task AgentTask, messages []ChatMessage, preserveIters int) ([]ChatMessage, int) {
	// Pick compression provider.
	provider := cfg.provider
	if cfg.compressModel != nil {
		if p := cfg.compressModel(ctx, task); p != nil {
			provider = p
		}
	}

	// Identify tool-result messages to compress.
	// Walk backwards to find the boundary of the last preserveIters iterations.
	// An "iteration" is one assistant message (with tool calls) followed by
	// its tool-result messages.
	iterCount := 0
	preserveFrom := len(messages)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			iterCount++
			if iterCount >= preserveIters {
				preserveFrom = i
				break
			}
		}
	}

	// Collect old tool-result content and prior summaries (before preserveFrom).
	// Prior summaries are re-compressed so successive passes fold together.
	const summaryPrefix = "[Summary of earlier tool results]\n"
	var oldContent strings.Builder
	var toRemove []int
	for i := 0; i < preserveFrom; i++ {
		m := messages[i]
		switch {
		case m.ToolCallID != "" && m.Content != "":
			// Tool result message.
			oldContent.WriteString(m.Content)
			oldContent.WriteString("\n---\n")
			toRemove = append(toRemove, i)
		case m.Role == "user" && strings.HasPrefix(m.Content, summaryPrefix) && i > 0:
			// Prior summary from an earlier compression pass (skip the initial user message at i=0).
			oldContent.WriteString(m.Content)
			oldContent.WriteString("\n---\n")
			toRemove = append(toRemove, i)
		}
	}
	if len(toRemove) == 0 {
		return messages, runeCount(messages)
	}

	// Start compression span if tracing.
	compressCtx := ctx
	if cfg.tracer != nil {
		var span Span
		compressCtx, span = cfg.tracer.Start(ctx, "agent.loop.compress",
			IntAttr("original_runes", runeCount(messages)),
			IntAttr("messages_compressed", len(toRemove)))
		defer span.End()
	}

	// Call compression provider.
	summaryResp, err := provider.Chat(compressCtx, ChatRequest{
		Messages: []ChatMessage{
			SystemMessage("Summarize the following tool execution results concisely. Preserve key facts, data values, decisions, and errors. Omit redundant details."),
			UserMessage(oldContent.String()),
		},
	})
	if err != nil {
		cfg.logger.Warn("context compression failed, continuing uncompressed", "error", err)
		return messages, runeCount(messages)
	}

	// Build new message slice: keep non-removed messages, insert summary.
	removeSet := make(map[int]bool, len(toRemove))
	for _, idx := range toRemove {
		removeSet[idx] = true
	}
	var compressed []ChatMessage
	summaryInserted := false
	for i, m := range messages {
		if removeSet[i] {
			if !summaryInserted {
				compressed = append(compressed, UserMessage("[Summary of earlier tool results]\n"+summaryResp.Content))
				summaryInserted = true
			}
			continue
		}
		compressed = append(compressed, m)
	}

	newRuneCount := runeCount(compressed)
	cfg.logger.Info("context compressed",
		"agent", cfg.name,
		"before_runes", runeCount(messages),
		"after_runes", newRuneCount,
		"messages_removed", len(toRemove))

	return compressed, newRuneCount
}

// handleProcessorErrorWithSteps converts a processor error into an AgentResult.
// ErrHalt produces a graceful result; other errors propagate as failures.
// Any step traces collected before the error are preserved in the result.
func handleProcessorErrorWithSteps(err error, usage Usage, steps []StepTrace) (AgentResult, error) {
	var halt *ErrHalt
	if errors.As(err, &halt) {
		return AgentResult{Output: halt.Response, Usage: usage, Steps: steps}, nil
	}
	return AgentResult{Usage: usage, Steps: steps}, err
}

// buildStepTrace creates a StepTrace from a tool call and its execution result.
// Agent delegations (tool calls prefixed with "agent_") get Type "agent" and
// the prefix stripped from Name. All other calls get Type "tool".
func buildStepTrace(tc ToolCall, res toolExecResult) StepTrace {
	name := tc.Name
	traceType := "tool"
	input := string(tc.Args)

	if after, ok := strings.CutPrefix(name, "agent_"); ok {
		name = after
		traceType = "agent"
		// Extract the task field from agent call args for a cleaner trace.
		var params struct {
			Task string `json:"task"`
		}
		if json.Unmarshal(tc.Args, &params) == nil && params.Task != "" {
			input = params.Task
		}
	}

	return StepTrace{
		Name:     name,
		Type:     traceType,
		Input:    truncateStr(input, 200),
		Output:   truncateStr(res.content, 500),
		Usage:    res.usage,
		Duration: res.duration,
	}
}

// defaultSuspendTTL is the default time-to-live for ErrSuspended snapshots.
// When the TTL elapses without Resume(), the resume closure and captured
// message snapshot are released automatically, preventing memory leaks.
// Callers can override with WithSuspendTTL after receiving ErrSuspended.
const defaultSuspendTTL = 30 * time.Minute

// estimateSnapshotSize returns a rough byte count for a message slice.
// Counts Content, ToolCall Args/Metadata, and message-level Metadata.
// Attachment.Data is shared (not deep-copied), so excluded.
func estimateSnapshotSize(messages []ChatMessage) int64 {
	var size int64
	for _, m := range messages {
		size += int64(len(m.Content))
		for _, tc := range m.ToolCalls {
			size += int64(len(tc.Args))
			size += int64(len(tc.Metadata))
		}
		size += int64(len(m.Metadata))
	}
	return size
}

// checkSuspendLoop checks if a processor error is a suspend signal.
// Returns a fully-wired ErrSuspended (with resume closure) if it is, nil otherwise.
// The resume closure captures the current conversation messages, appends the
// human's response, and re-enters runLoop.
//
// A default TTL of 30 minutes is applied automatically. Callers can override
// with WithSuspendTTL or call Release() explicitly.
func checkSuspendLoop(err error, cfg loopConfig, messages []ChatMessage, task AgentTask) *ErrSuspended {
	var suspend *errSuspend
	if !errors.As(err, &suspend) {
		return nil
	}

	// Enforce per-agent suspend budget.
	if cfg.suspendCount != nil {
		maxSnap := cfg.maxSuspendSnapshots
		if maxSnap <= 0 {
			maxSnap = defaultMaxSuspendSnapshots
		}
		maxBytes := cfg.maxSuspendBytes
		if maxBytes <= 0 {
			maxBytes = defaultMaxSuspendBytes
		}
		snapSize := estimateSnapshotSize(messages)
		if cfg.suspendCount.Load() >= int64(maxSnap) ||
			cfg.suspendBytes.Load()+snapSize > maxBytes {
			cfg.logger.Warn("suspend budget exceeded, skipping suspension",
				"agent", cfg.name,
				"count", cfg.suspendCount.Load(),
				"bytes", cfg.suspendBytes.Load())
			return nil // caller propagates the original processor error
		}
		cfg.suspendCount.Add(1)
		cfg.suspendBytes.Add(snapSize)
	}

	// Deep-copy messages for resume closure so that ToolCalls, Attachments,
	// and Metadata slices don't share backing arrays with the original.
	// Inner byte slices (ToolCall.Args/Metadata, Attachment.Data) are also
	// deep-copied to prevent shared mutable state across the snapshot boundary.
	snapshot := make([]ChatMessage, len(messages))
	for i, m := range messages {
		snapshot[i] = m
		if len(m.ToolCalls) > 0 {
			snapshot[i].ToolCalls = make([]ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				snapshot[i].ToolCalls[j] = tc
				if len(tc.Args) > 0 {
					snapshot[i].ToolCalls[j].Args = make(json.RawMessage, len(tc.Args))
					copy(snapshot[i].ToolCalls[j].Args, tc.Args)
				}
				if len(tc.Metadata) > 0 {
					snapshot[i].ToolCalls[j].Metadata = make(json.RawMessage, len(tc.Metadata))
					copy(snapshot[i].ToolCalls[j].Metadata, tc.Metadata)
				}
			}
		}
		// Isolate the Attachments slice header so mutations to the original
		// (append, reorder) don't affect the snapshot. Attachment.Data is
		// treated as immutable throughout the framework, so sharing the
		// backing byte slice is safe and avoids duplicating large binary
		// content (images, PDFs, audio).
		if len(m.Attachments) > 0 {
			snapshot[i].Attachments = make([]Attachment, len(m.Attachments))
			copy(snapshot[i].Attachments, m.Attachments)
		}
		if len(m.Metadata) > 0 {
			snapshot[i].Metadata = make(json.RawMessage, len(m.Metadata))
			copy(snapshot[i].Metadata, m.Metadata)
		}
	}

	// Capture snapshot size for budget tracking.
	var snapSize int64
	if cfg.suspendCount != nil {
		snapSize = estimateSnapshotSize(messages)
	}

	suspended := &ErrSuspended{
		Step:         cfg.name,
		Payload:      suspend.payload,
		snapshotSize: snapSize,
		resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
			resumed := make([]ChatMessage, len(snapshot)+1)
			copy(resumed, snapshot)
			resumed[len(snapshot)] = UserMessage("Human input: " + string(data))
			resumeCfg := cfg
			resumeCfg.resumeMessages = resumed
			return runLoop(ctx, resumeCfg, task, nil)
		},
	}
	if cfg.suspendCount != nil {
		suspended.onRelease = func(size int64) {
			cfg.suspendCount.Add(-1)
			cfg.suspendBytes.Add(-size)
		}
	}
	// Apply default TTL to prevent memory leaks from abandoned suspensions.
	// Callers can override with WithSuspendTTL or disable by calling
	// suspended.WithSuspendTTL(0) (though that re-enables the leak risk).
	suspended.WithSuspendTTL(defaultSuspendTTL)
	return suspended
}

// maxToolResultMessageLen is the maximum rune length for a tool result stored
// in the conversation message history during the tool-calling loop. Results
// exceeding this limit are truncated with a marker so the LLM knows content
// was trimmed. This prevents unbounded memory growth from tools that return
// very large outputs (e.g. web scraping, file reads).
//
// Stream events and step traces retain the full content since they are
// transient and not accumulated across iterations.
const maxToolResultMessageLen = 100_000 // ~25K tokens

// --- parallel tool dispatch ---

// toolExecResult holds the result of a single parallel tool call.
type toolExecResult struct {
	content     string
	usage       Usage
	attachments []Attachment
	duration    time.Duration
	isError     bool
}

// maxAccumulatedAttachments caps the number of attachments collected from
// tool/agent results during the execution loop. Prevents unbounded memory
// growth when subagents produce large binary content (images, audio, etc.).
const maxAccumulatedAttachments = 50

// maxAccumulatedAttachmentBytes is the default size budget (bytes) for
// attachments collected from tool/agent results during the execution loop.
const maxAccumulatedAttachmentBytes int64 = 50 * 1024 * 1024 // 50 MB

const defaultMaxSuspendSnapshots = 20
const defaultMaxSuspendBytes int64 = 256 * 1024 * 1024 // 256 MB

// defaultCompressThreshold is the default rune count at which context
// compression triggers in the tool-calling loop. ~50K tokens.
const defaultCompressThreshold = 200_000

// maxParallelDispatch caps the number of concurrent tool call goroutines
// to avoid overwhelming external services with unbounded parallelism.
const maxParallelDispatch = 10

// indexedResult pairs a tool execution result with its position in the
// original call slice, allowing channel-based collection in order.
type indexedResult struct {
	idx    int
	result toolExecResult
}

// safeDispatch wraps a dispatch call with panic recovery. If the dispatched
// tool panics, the panic is caught and converted to an error result instead
// of crashing the process. Matches the recovery pattern used for subagent
// dispatch in Network.makeDispatch.
func safeDispatch(ctx context.Context, tc ToolCall, dispatch DispatchFunc) (dr DispatchResult) {
	defer func() {
		if p := recover(); p != nil {
			dr = DispatchResult{Content: fmt.Sprintf("error: tool %q panic: %v", tc.Name, p), IsError: true}
		}
	}()
	return dispatch(ctx, tc)
}

// dispatchParallel runs all tool calls concurrently via the dispatch function
// and returns results in the same order as the input calls.
// Single calls run inline (no goroutine). Multiple calls use a fixed worker
// pool of min(len(calls), maxParallelDispatch) goroutines pulling from a
// shared work channel, avoiding unbounded goroutine creation.
//
// The collection loop is context-aware: if ctx is cancelled while tool calls
// are still in-flight, the function returns immediately with context-error
// results for incomplete calls instead of blocking indefinitely.
func dispatchParallel(ctx context.Context, calls []ToolCall, dispatch DispatchFunc) []toolExecResult {
	// Fast path: single call, no goroutine needed.
	if len(calls) == 1 {
		start := time.Now()
		dr := safeDispatch(ctx, calls[0], dispatch)
		return []toolExecResult{{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError}}
	}

	resultCh := make(chan indexedResult, len(calls))

	// Work channel: each item is an (index, ToolCall) pair for workers to consume.
	type workItem struct {
		idx int
		tc  ToolCall
	}
	workCh := make(chan workItem, len(calls))
	for i, tc := range calls {
		workCh <- workItem{idx: i, tc: tc}
	}
	close(workCh)

	// Spawn a fixed pool of workers — never more goroutines than needed.
	numWorkers := min(len(calls), maxParallelDispatch)
	var wg sync.WaitGroup
	wg.Add(numWorkers)
	for range numWorkers {
		go func() {
			defer wg.Done()
			for w := range workCh {
				if ctx.Err() != nil {
					resultCh <- indexedResult{w.idx, toolExecResult{content: "error: " + ctx.Err().Error(), isError: true}}
					continue
				}
				start := time.Now()
				dr := safeDispatch(ctx, w.tc, dispatch)
				resultCh <- indexedResult{w.idx, toolExecResult{content: dr.Content, usage: dr.Usage, attachments: dr.Attachments, duration: time.Since(start), isError: dr.IsError}}
			}
		}()
	}

	// Close resultCh once all workers are done.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Collect results, bailing out if ctx is cancelled while calls are in-flight.
	results := make([]toolExecResult, len(calls))
	seen := make([]bool, len(calls))
collect:
	for received := 0; received < len(calls); received++ {
		select {
		case r, ok := <-resultCh:
			if !ok {
				break collect
			}
			results[r.idx] = r.result
			seen[r.idx] = true
		case <-ctx.Done():
			errResult := toolExecResult{content: "error: " + ctx.Err().Error(), isError: true}
			for i := range results {
				if !seen[i] {
					results[i] = errResult
				}
			}
			return results
		}
	}
	// Fill any unseen results (e.g. channel closed early) with error markers.
	for i := range results {
		if !seen[i] {
			results[i] = toolExecResult{content: "error: result not received", isError: true}
		}
	}
	return results
}

// truncateStr truncates a string to n runes.
func truncateStr(s string, n int) string {
	// Fast path: byte length ≤ n guarantees rune count ≤ n,
	// avoiding the []rune allocation for short/ASCII strings.
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// --- Input handler (human-in-the-loop) ---

// InputRequest describes what the agent needs from the human.
type InputRequest struct {
	// Question is the natural language prompt shown to the human.
	Question string
	// Options provides suggested choices. Empty = free-form input.
	Options []string
	// Metadata carries context for the handler (agent name, tool being approved, etc).
	Metadata map[string]string
}

// InputResponse is the human's reply.
type InputResponse struct {
	// Value is the human's text response.
	Value string
}

// InputHandler delivers questions to a human and returns their response.
// Implementations bridge to the actual communication channel (Telegram, CLI, HTTP, etc).
// Must block until a response is received or ctx is cancelled.
type InputHandler interface {
	RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}

// inputHandlerCtxKey is the context key for InputHandler.
type inputHandlerCtxKey struct{}

// WithInputHandlerContext returns a child context carrying the InputHandler.
func WithInputHandlerContext(ctx context.Context, h InputHandler) context.Context {
	return context.WithValue(ctx, inputHandlerCtxKey{}, h)
}

// InputHandlerFromContext retrieves the InputHandler from ctx.
// Returns nil, false if no handler is set.
func InputHandlerFromContext(ctx context.Context) (InputHandler, bool) {
	h, ok := ctx.Value(inputHandlerCtxKey{}).(InputHandler)
	return h, ok
}

// --- Task context propagation ---

// taskCtxKey is the context key for AgentTask.
type taskCtxKey struct{}

// WithTaskContext returns a child context carrying the AgentTask.
// Called automatically by LLMAgent and Network at Execute entry points.
// Tools and processors can retrieve the task via TaskFromContext.
func WithTaskContext(ctx context.Context, task AgentTask) context.Context {
	return context.WithValue(ctx, taskCtxKey{}, task)
}

// TaskFromContext retrieves the AgentTask from ctx.
// Returns the task and true if present, or zero AgentTask and false if not.
// Use this in Tool.Execute to access task metadata (user ID, thread ID, etc.)
// without changing the Tool interface.
func TaskFromContext(ctx context.Context) (AgentTask, bool) {
	task, ok := ctx.Value(taskCtxKey{}).(AgentTask)
	return task, ok
}

// --- Suspend / Resume ---

// errSuspend is the internal sentinel returned by step functions to signal
// that execution should pause for external input. The workflow/network engine
// catches it and converts to ErrSuspended with resume capabilities.
type errSuspend struct {
	payload json.RawMessage
}

func (e *errSuspend) Error() string { return "suspend" }

// Suspend returns an error that signals the workflow or network engine to
// pause execution. The payload provides context for the human (what they
// need to decide, what data to show).
func Suspend(payload json.RawMessage) error {
	return &errSuspend{payload: payload}
}

// ErrSuspended is returned by Execute() when a workflow step or network
// processor suspends execution to await external input.
// Inspect Payload for context, then call Resume() with the human's response.
//
// Retention: ErrSuspended holds a closure that captures the full conversation
// message history (including tool call arguments, results, and attachments).
// This data remains in memory until Resume() is called, Release() is called,
// the TTL expires, or the ErrSuspended value is garbage-collected.
//
// To prevent memory leaks in server environments, use WithSuspendTTL to set
// an automatic expiry. When the TTL elapses without Resume(), the snapshot
// is released automatically. Without a TTL, callers must call Release()
// explicitly when the resume window has passed (e.g. timeout, user abandonment).
// After release (manual or automatic), Resume() returns an error.
type ErrSuspended struct {
	// Step is the name of the step or processor hook that suspended.
	Step string
	// Payload carries context for the human (what to show, what to decide).
	Payload json.RawMessage
	// resume is the closure that continues execution with human input.
	// Guarded by mu when a TTL timer is active (timer callback writes from
	// a separate goroutine). Without a TTL, single-goroutine access is safe.
	resume func(ctx context.Context, data json.RawMessage) (AgentResult, error)
	// mu guards resume against concurrent access from the TTL timer goroutine.
	mu sync.Mutex
	// ttlTimer is the auto-release timer. Nil when no TTL is set.
	ttlTimer *time.Timer
	// snapshotSize is the estimated bytes of the captured snapshot.
	snapshotSize int64
	// onRelease decrements the agent's suspend budget counters.
	onRelease func(size int64)
}

func (e *ErrSuspended) Error() string {
	return fmt.Sprintf("suspended at step %q", e.Step)
}

// Resume continues execution with the human's response data.
// The data is made available to the step via ResumeData().
// Resume is single-use: calling it more than once is undefined behavior.
// Returns an error if called on a released, expired, or externally constructed ErrSuspended.
func (e *ErrSuspended) Resume(ctx context.Context, data json.RawMessage) (AgentResult, error) {
	e.mu.Lock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	fn := e.resume
	onRel := e.onRelease
	e.resume = nil // single-use: free the captured snapshot after resume
	e.onRelease = nil
	e.mu.Unlock()

	if fn == nil {
		return AgentResult{}, fmt.Errorf("ErrSuspended: resume closure is nil (released, expired, or constructed outside engine)")
	}
	if onRel != nil {
		onRel(e.snapshotSize)
	}
	return fn(ctx, data)
}

// Release nils out the resume closure, eagerly freeing the captured message
// snapshot and all referenced data (tool arguments, attachments, etc.).
// Call this when the suspend will not be resumed (timeout, user abandonment).
// After Release(), Resume() returns an error. Safe to call multiple times.
func (e *ErrSuspended) Release() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	if e.resume != nil && e.onRelease != nil {
		e.onRelease(e.snapshotSize)
		e.onRelease = nil // prevent double-decrement
	}
	e.resume = nil
}

// WithSuspendTTL sets an automatic expiry on the suspended state.
// When the TTL elapses without Resume() being called, the resume closure
// is released automatically, freeing the captured message snapshot.
//
// A default TTL of 30 minutes is applied automatically when ErrSuspended
// is created by the framework. Call this to override with a custom duration.
//
//	var suspended *oasis.ErrSuspended
//	if errors.As(err, &suspended) {
//	    suspended.WithSuspendTTL(5 * time.Minute)
//	    // ... store suspended for later resume ...
//	}
func (e *ErrSuspended) WithSuspendTTL(d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ttlTimer != nil {
		e.ttlTimer.Stop()
	}
	e.ttlTimer = time.AfterFunc(d, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.resume != nil && e.onRelease != nil {
			e.onRelease(e.snapshotSize)
			e.onRelease = nil
		}
		e.resume = nil
	})
}

// StepSuspended indicates a step that paused execution to await external input.
const StepSuspended StepStatus = "suspended"

// resumeDataKey is the reserved WorkflowContext key for resume data.
const resumeDataKey = "_resume_data"

// ResumeData retrieves resume data from the WorkflowContext.
// Returns the data and true if this step is being resumed, or nil and false
// on first execution. Safe to call with a nil WorkflowContext (returns nil, false).
func ResumeData(wCtx *WorkflowContext) (json.RawMessage, bool) {
	if wCtx == nil {
		return nil, false
	}
	v, ok := wCtx.Get(resumeDataKey)
	if !ok {
		return nil, false
	}
	data, ok := v.(json.RawMessage)
	return data, ok
}

// --- Batch execution ---

// BatchState represents the lifecycle state of a batch job.
type BatchState string

const (
	BatchPending   BatchState = "pending"
	BatchRunning   BatchState = "running"
	BatchSucceeded BatchState = "succeeded"
	BatchFailed    BatchState = "failed"
	BatchCancelled BatchState = "cancelled"
	BatchExpired   BatchState = "expired"
)

// BatchStats holds aggregate counts for a batch job's requests.
type BatchStats struct {
	TotalCount     int `json:"total_count"`
	SucceededCount int `json:"succeeded_count"`
	FailedCount    int `json:"failed_count"`
}

// BatchJob represents an asynchronous batch processing job.
// Use BatchStatus to poll for state changes and BatchChatResults or
// BatchEmbedResults to retrieve completed output.
type BatchJob struct {
	ID          string     `json:"id"`
	State       BatchState `json:"state"`
	DisplayName string     `json:"display_name,omitempty"`
	Stats       BatchStats `json:"stats"`
	CreateTime  time.Time  `json:"create_time"`
	UpdateTime  time.Time  `json:"update_time"`
}

// BatchProvider extends Provider with asynchronous batch chat capabilities.
// Batch requests are processed offline at reduced cost. Use BatchStatus to poll
// job progress and BatchChatResults to retrieve completed responses.
type BatchProvider interface {
	// BatchChat submits multiple chat requests as a single batch job.
	// Returns the created job with its ID for status tracking.
	BatchChat(ctx context.Context, requests []ChatRequest) (BatchJob, error)

	// BatchStatus returns the current state of a batch job.
	BatchStatus(ctx context.Context, jobID string) (BatchJob, error)

	// BatchChatResults retrieves chat responses for a completed batch job.
	// Returns error if the job has not yet succeeded.
	BatchChatResults(ctx context.Context, jobID string) ([]ChatResponse, error)

	// BatchCancel requests cancellation of a running or pending batch job.
	BatchCancel(ctx context.Context, jobID string) error
}

// BatchEmbeddingProvider extends EmbeddingProvider with batch embedding capabilities.
// Each element in the texts slice passed to BatchEmbed is a group of strings to embed.
type BatchEmbeddingProvider interface {
	// BatchEmbed submits multiple embedding requests as a single batch job.
	BatchEmbed(ctx context.Context, texts [][]string) (BatchJob, error)

	// BatchEmbedStatus returns the current state of a batch embedding job.
	BatchEmbedStatus(ctx context.Context, jobID string) (BatchJob, error)

	// BatchEmbedResults retrieves embedding vectors for a completed batch job.
	// Returns one vector per input text group.
	BatchEmbedResults(ctx context.Context, jobID string) ([][]float32, error)
}

// --- Streaming ---

// StreamEventType identifies the kind of streaming event.
type StreamEventType string

const (
	// EventInputReceived signals that a task has been received by an agent.
	// Name carries the agent name; Content carries the task input text.
	EventInputReceived StreamEventType = "input-received"
	// EventProcessingStart signals that the agent loop has begun processing
	// (after memory/context loading, before the first LLM call).
	// Name carries the loop identifier (e.g. "agent:name" or "network:name").
	EventProcessingStart StreamEventType = "processing-start"
	// EventTextDelta carries an incremental text chunk from the LLM.
	EventTextDelta StreamEventType = "text-delta"
	// EventToolCallStart signals a tool is about to be invoked.
	EventToolCallStart StreamEventType = "tool-call-start"
	// EventToolCallResult carries the result of a completed tool call.
	EventToolCallResult StreamEventType = "tool-call-result"
	// EventAgentStart signals a subagent has been delegated to (Network only).
	EventAgentStart StreamEventType = "agent-start"
	// EventAgentFinish signals a subagent has completed (Network only).
	EventAgentFinish StreamEventType = "agent-finish"
)

// StreamEvent is a typed event emitted during agent streaming.
// Consumers receive these on the channel passed to ExecuteStream.
type StreamEvent struct {
	// Type identifies the event kind.
	Type StreamEventType `json:"type"`
	// Name is the tool or agent name (set for tool/agent events, empty for text-delta).
	Name string `json:"name,omitempty"`
	// Content carries the text delta (text-delta), tool result (tool-call-result),
	// or agent task/output (agent-start/agent-finish).
	Content string `json:"content,omitempty"`
	// Args carries the tool call arguments (tool-call-start only).
	Args json.RawMessage `json:"args,omitempty"`
	// Usage carries token counts for the completed step.
	// Set on agent-finish and tool-call-result events. Zero value otherwise.
	Usage Usage `json:"usage,omitempty"`
	// Duration is the wall-clock time for the completed step.
	// Set on agent-finish and tool-call-result events. Zero value otherwise.
	Duration time.Duration `json:"duration,omitempty"`
}

// ServeSSE streams an agent's response as Server-Sent Events over HTTP.
//
// It validates that w implements [http.Flusher], sets SSE headers, creates a
// buffered [StreamEvent] channel, runs the agent in a background goroutine,
// and writes each event as:
//
//	event: <event-type>
//	data: <json-encoded StreamEvent>
//
// On completion it sends a final "done" event. If the agent returns an error,
// it is sent as an "error" event before returning.
//
// Client disconnection propagates via ctx cancellation to the agent.
// Callers typically pass r.Context() as ctx.
func ServeSSE(ctx context.Context, w http.ResponseWriter, agent StreamingAgent, task AgentTask) (AgentResult, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return AgentResult{}, fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan StreamEvent, 64)
	var closeOnce sync.Once
	safeClose := func() { closeOnce.Do(func() { close(ch) }) }

	type execResult struct {
		result AgentResult
		err    error
	}
	resultCh := make(chan execResult, 1)

	go func() {
		defer func() {
			if p := recover(); p != nil {
				// Ensure ch is closed so the for-range loop below
				// doesn't block forever, then signal the error.
				// Use sync.Once because ExecuteStream may have already
				// closed ch before the panic site.
				safeClose()
				resultCh <- execResult{AgentResult{}, fmt.Errorf("agent panic: %v", p)}
				return
			}
		}()
		r, err := agent.ExecuteStream(ctx, task, ch)
		resultCh <- execResult{r, err}
	}()

	for ev := range ch {
		data, err := json.Marshal(ev)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
		flusher.Flush()
	}

	res := <-resultCh

	if res.err != nil {
		errData, _ := json.Marshal(map[string]string{"error": res.err.Error()})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", errData)
		flusher.Flush()
		return res.result, res.err
	}

	doneData, _ := json.Marshal(res.result)
	fmt.Fprintf(w, "event: done\ndata: %s\n\n", doneData)
	flusher.Flush()

	return res.result, nil
}

// WriteSSEEvent writes a single Server-Sent Event to w and flushes.
// It validates that w implements [http.Flusher], JSON-marshals data into
// the SSE data field, and flushes immediately. eventType is the SSE event
// name (e.g. "text-delta", "done").
//
// Use this to compose custom SSE loops with [StreamingAgent.ExecuteStream]:
//
//	ch := make(chan oasis.StreamEvent, 64)
//	go agent.ExecuteStream(ctx, task, ch)
//	for ev := range ch {
//	    oasis.WriteSSEEvent(w, string(ev.Type), ev)
//	}
//	oasis.WriteSSEEvent(w, "done", customPayload)
func WriteSSEEvent(w http.ResponseWriter, eventType string, data any) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal sse data: %w", err)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, encoded)
	flusher.Flush()
	return nil
}
