package oasis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
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
	planExecution     bool            // enabled by WithPlanExecution option
	codeRunner        CodeRunner      // set by WithCodeExecution option
	responseSchema    *ResponseSchema // set by WithResponseSchema option
	dynamicPrompt     PromptFunc      // set by WithDynamicPrompt option
	dynamicModel      ModelFunc       // set by WithDynamicModel option
	dynamicTools      ToolsFunc       // set by WithDynamicTools option
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


func buildConfig(opts []AgentOption) agentConfig {
	var c agentConfig
	for _, opt := range opts {
		opt(&c)
	}
	// Warn about misconfigurations that can't be caught at compile time.
	if c.memory != nil && c.store == nil {
		log.Printf("[oasis] warning: WithUserMemory without WithConversationMemory — fact extraction (write) will be silently skipped")
	}
	return c
}

// --- shared execution loop ---

// DispatchFunc executes a single tool call and returns its content and usage.
// LLMAgent provides one that calls ToolRegistry.Execute + ask_user.
// Network provides one that also routes to subagents via the agent_* prefix.
type DispatchFunc func(ctx context.Context, tc ToolCall) (string, Usage)

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
}

// runLoop is the shared tool-calling loop used by both LLMAgent and Network.
// When ch is nil, it operates in blocking mode (Execute). When ch is non-nil,
// it emits StreamEvent values and closes ch when done (ExecuteStream).
func runLoop(ctx context.Context, cfg loopConfig, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
	var totalUsage Usage
	var steps []StepTrace

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

	// lastAgentOutput tracks the most recent sub-agent result so we can fall
	// back to it when the router produces an empty final response (common for
	// pure-routing LLMs that don't synthesize a reply after delegating).
	// For LLMAgent this is never set (no agent_* tools).
	var lastAgentOutput string

	for i := 0; i < cfg.maxIter; i++ {
		req := ChatRequest{Messages: messages, ResponseSchema: cfg.responseSchema}

		// PreProcessor hook.
		if err := cfg.processors.RunPreLLM(ctx, &req); err != nil {
			if ch != nil {
				close(ch)
			}
			if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, s
			}
			return handleProcessorErrorWithSteps(err, totalUsage, steps)
		}

		var resp ChatResponse
		var err error

		if len(cfg.tools) > 0 {
			resp, err = cfg.provider.ChatWithTools(ctx, req, cfg.tools)
		} else if ch != nil {
			// No tools, streaming — stream the response directly.
			resp, err = cfg.provider.ChatStream(ctx, req, ch)
			if err != nil {
				return AgentResult{Usage: totalUsage, Steps: steps}, err
			}
			totalUsage.InputTokens += resp.Usage.InputTokens
			totalUsage.OutputTokens += resp.Usage.OutputTokens
			cfg.mem.persistMessages(ctx, cfg.name, task, task.Input, resp.Content)
			return AgentResult{Output: resp.Content, Attachments: resp.Attachments, Usage: totalUsage, Steps: steps}, nil
		} else {
			resp, err = cfg.provider.Chat(ctx, req)
		}

		if err != nil {
			if ch != nil {
				close(ch)
			}
			return AgentResult{Usage: totalUsage, Steps: steps}, err
		}
		totalUsage.InputTokens += resp.Usage.InputTokens
		totalUsage.OutputTokens += resp.Usage.OutputTokens

		// PostProcessor hook.
		if err := cfg.processors.RunPostLLM(ctx, &resp); err != nil {
			if ch != nil {
				close(ch)
			}
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
				ch <- StreamEvent{Type: EventTextDelta, Content: content}
				close(ch)
			}
			cfg.mem.persistMessages(ctx, cfg.name, task, task.Input, content)
			return AgentResult{Output: content, Attachments: resp.Attachments, Usage: totalUsage, Steps: steps}, nil
		}

		// Append assistant message with tool calls.
		messages = append(messages, ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Emit tool-call-start events before dispatch.
		if ch != nil {
			for _, tc := range resp.ToolCalls {
				ch <- StreamEvent{Type: EventToolCallStart, Name: tc.Name, Args: tc.Args}
			}
		}

		// Execute tool calls in parallel.
		results := dispatchParallel(ctx, resp.ToolCalls, cfg.dispatch)

		// Process results sequentially (PostToolProcessor + message assembly + trace collection).
		for j, tc := range resp.ToolCalls {
			totalUsage.InputTokens += results[j].usage.InputTokens
			totalUsage.OutputTokens += results[j].usage.OutputTokens

			// Emit tool-call-result event.
			if ch != nil {
				ch <- StreamEvent{
					Type:     EventToolCallResult,
					Name:     tc.Name,
					Content:  results[j].content,
					Usage:    results[j].usage,
					Duration: results[j].duration,
				}
			}

			// Build step trace.
			trace := buildStepTrace(tc, results[j])
			steps = append(steps, trace)

			result := ToolResult{Content: results[j].content}
			if err := cfg.processors.RunPostTool(ctx, tc, &result); err != nil {
				if ch != nil {
					close(ch)
				}
				if s := checkSuspendLoop(err, cfg, messages, task); s != nil {
					return AgentResult{Usage: totalUsage, Steps: steps}, s
				}
				return handleProcessorErrorWithSteps(err, totalUsage, steps)
			}
			messages = append(messages, ToolResultMessage(tc.ID, result.Content))

			// Track the last sub-agent output for fallback.
			if len(tc.Name) > 6 && tc.Name[:6] == "agent_" {
				lastAgentOutput = result.Content
			}
		}
	}

	// Max iterations — force synthesis.
	log.Printf("[%s] max iterations reached, forcing synthesis", cfg.name)
	messages = append(messages, UserMessage(
		"You have used all available tool calls. Summarize what you found and respond to the user."))

	var resp ChatResponse
	var err error
	if ch != nil {
		resp, err = cfg.provider.ChatStream(ctx, ChatRequest{Messages: messages}, ch)
	} else {
		resp, err = cfg.provider.Chat(ctx, ChatRequest{Messages: messages})
	}
	if err != nil {
		return AgentResult{Usage: totalUsage, Steps: steps}, err
	}
	totalUsage.InputTokens += resp.Usage.InputTokens
	totalUsage.OutputTokens += resp.Usage.OutputTokens

	cfg.mem.persistMessages(ctx, cfg.name, task, task.Input, resp.Content)
	return AgentResult{Output: resp.Content, Attachments: resp.Attachments, Usage: totalUsage, Steps: steps}, nil
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

	if len(name) > 6 && name[:6] == "agent_" {
		name = name[6:]
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

// checkSuspendLoop checks if a processor error is a suspend signal.
// Returns a fully-wired ErrSuspended (with resume closure) if it is, nil otherwise.
// The resume closure captures the current conversation messages, appends the
// human's response, and re-enters runLoop.
func checkSuspendLoop(err error, cfg loopConfig, messages []ChatMessage, task AgentTask) *ErrSuspended {
	var suspend *errSuspend
	if !errors.As(err, &suspend) {
		return nil
	}

	// Snapshot messages for resume closure.
	snapshot := make([]ChatMessage, len(messages))
	copy(snapshot, messages)

	return &ErrSuspended{
		Step:    cfg.name,
		Payload: suspend.payload,
		resume: func(ctx context.Context, data json.RawMessage) (AgentResult, error) {
			resumed := make([]ChatMessage, len(snapshot)+1)
			copy(resumed, snapshot)
			resumed[len(snapshot)] = UserMessage("Human input: " + string(data))
			resumeCfg := cfg
			resumeCfg.resumeMessages = resumed
			return runLoop(ctx, resumeCfg, task, nil)
		},
	}
}

// --- parallel tool dispatch ---

// toolExecResult holds the result of a single parallel tool call.
type toolExecResult struct {
	content  string
	usage    Usage
	duration time.Duration
}

// dispatchParallel runs all tool calls concurrently via the dispatch function
// and returns results in the same order as the input calls.
func dispatchParallel(ctx context.Context, calls []ToolCall, dispatch DispatchFunc) []toolExecResult {
	results := make([]toolExecResult, len(calls))
	var wg sync.WaitGroup

	for i, tc := range calls {
		wg.Add(1)
		go func(idx int, tc ToolCall) {
			defer wg.Done()
			start := time.Now()
			content, usage := dispatch(ctx, tc)
			results[idx] = toolExecResult{content: content, usage: usage, duration: time.Since(start)}
		}(i, tc)
	}

	wg.Wait()
	return results
}

// truncateStr truncates a string to n runes.
func truncateStr(s string, n int) string {
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
type ErrSuspended struct {
	// Step is the name of the step or processor hook that suspended.
	Step string
	// Payload carries context for the human (what to show, what to decide).
	Payload json.RawMessage
	// resume is the closure that continues execution with human input.
	resume func(ctx context.Context, data json.RawMessage) (AgentResult, error)
}

func (e *ErrSuspended) Error() string {
	return fmt.Sprintf("suspended at step %q", e.Step)
}

// Resume continues execution with the human's response data.
// The data is made available to the step via ResumeData().
// Resume is single-use: calling it more than once is undefined behavior.
// Returns an error if called on an ErrSuspended not produced by the engine.
func (e *ErrSuspended) Resume(ctx context.Context, data json.RawMessage) (AgentResult, error) {
	if e.resume == nil {
		return AgentResult{}, fmt.Errorf("ErrSuspended: resume closure is nil (constructed outside engine)")
	}
	return e.resume(ctx, data)
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

	type execResult struct {
		result AgentResult
		err    error
	}
	resultCh := make(chan execResult, 1)

	go func() {
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

	fmt.Fprintf(w, "event: done\ndata: [DONE]\n\n")
	flusher.Flush()

	return res.result, nil
}
