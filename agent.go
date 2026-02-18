package oasis

import "context"

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

// StreamingAgent is an optional capability for agents that support token streaming.
// Check via type assertion: if sa, ok := agent.(StreamingAgent); ok { ... }
type StreamingAgent interface {
	Agent
	// ExecuteStream runs the agent like Execute, but streams the final response
	// tokens into ch. The channel is closed when streaming completes.
	// Tool-calling iterations run in blocking mode; only the final text
	// response is streamed.
	ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error)
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
	// Usage tracks aggregate token usage across all LLM calls.
	Usage Usage
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
}

// AgentOption configures an LLMAgent or Network.
type AgentOption func(*agentConfig)

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
// Requires WithEmbedding to be set.
//
// Optional SemanticOption values tune recall behavior:
//
//	oasis.CrossThreadSearch()                    // default threshold (0.60)
//	oasis.CrossThreadSearch(oasis.MinScore(0.7)) // custom threshold
func CrossThreadSearch(opts ...SemanticOption) ConversationOption {
	return func(c *agentConfig) {
		c.crossThreadSearch = true
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

// WithConversationMemory enables conversation history on the agent.
// When set and task.Context["thread_id"] is present, the agent loads
// recent messages before the LLM call and persists the exchange afterward.
//
// Optional ConversationOption values enable additional features:
//
//	oasis.WithConversationMemory(store)                                     // history only
//	oasis.WithConversationMemory(store, oasis.CrossThreadSearch())          // + cross-thread recall
//	oasis.WithConversationMemory(store, oasis.CrossThreadSearch(oasis.MinScore(0.7))) // + custom threshold
func WithConversationMemory(s Store, opts ...ConversationOption) AgentOption {
	return func(c *agentConfig) {
		c.store = s
		for _, o := range opts {
			o(c)
		}
	}
}

// WithEmbedding sets the embedding provider for semantic features.
// Used by: CrossThreadSearch (cross-thread recall), WithUserMemory (fact retrieval),
// and message persistence (embed before storing).
func WithEmbedding(e EmbeddingProvider) AgentOption {
	return func(c *agentConfig) { c.embedding = e }
}

// WithUserMemory enables the full user memory pipeline: read + write.
//
// Read (every Execute call): embeds the input, retrieves relevant facts via
// BuildContext, and appends them to the system prompt. Requires WithEmbedding.
//
// Write (after each turn, background): uses the agent's own LLM to extract
// durable user facts from the conversation exchange and persists them via
// UpsertFact. Requires WithConversationMemory + WithEmbedding — without
// either, extraction is silently skipped.
func WithUserMemory(m MemoryStore) AgentOption {
	return func(c *agentConfig) { c.memory = m }
}


func buildConfig(opts []AgentOption) agentConfig {
	var c agentConfig
	for _, opt := range opts {
		opt(&c)
	}
	return c
}
