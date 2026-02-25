package oasis

import (
	"context"
	"log/slog"
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
