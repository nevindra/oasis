package core

import (
	"context"
	"encoding/json"
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
//
// Implemented by LLMAgent, Network, and Workflow.
type StreamingAgent interface {
	Agent
	// ExecuteStream runs the agent like Execute, but emits StreamEvent values
	// into ch throughout execution. Events include text deltas, tool call
	// deltas/start/result/progress, agent start/finish (Networks), step
	// start/finish/progress (Workflows), and routing decisions (Networks).
	//
	// Contract: implementations MUST close ch before returning. Callers
	// (including ServeSSE) use `for ev := range ch` to consume events,
	// which blocks until ch is closed. Failing to close ch causes a deadlock.
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
	// ThreadID identifies the conversation thread. Empty when no thread is set.
	// Memory uses this to scope history loading and persistence.
	ThreadID string
	// UserID identifies the end user. Empty when no user is set.
	// Dynamic prompts/models/tools may inspect this for per-user behavior.
	UserID string
	// ChatID identifies the chat/channel for messaging integrations (Telegram, Slack, etc.).
	// Empty when no chat is set.
	ChatID string
	// Extra carries arbitrary app-defined metadata. The framework never reads
	// this map; it is opaque pass-through for dynamic resolvers and processors.
	// Use ThreadID/UserID/ChatID for framework-recognized identifiers.
	Extra map[string]any
}

// WithThreadID sets the conversation thread ID on the task and returns it.
func (t AgentTask) WithThreadID(id string) AgentTask { t.ThreadID = id; return t }

// WithUserID sets the user ID on the task and returns it.
func (t AgentTask) WithUserID(id string) AgentTask { t.UserID = id; return t }

// WithChatID sets the chat/channel ID on the task and returns it.
func (t AgentTask) WithChatID(id string) AgentTask { t.ChatID = id; return t }

// AgentResult is the output of an Agent.
type AgentResult struct {
	// Output is the agent's final response text.
	Output string
	// Thinking carries the LLM's reasoning/chain-of-thought from the final response.
	// Populated when the provider returns thinking content (e.g. Gemini thought parts).
	// Empty when the provider does not support thinking or thinking is disabled.
	Thinking string
	// Attachments carries optional multimodal content (images, audio, etc.) from the LLM response.
	// Populated when the provider returns media alongside or instead of text.
	Attachments []Attachment
	// Usage tracks aggregate token usage across all LLM calls.
	Usage Usage
	// Steps records per-tool and per-agent execution traces in chronological order.
	// Populated by LLMAgent (tool calls) and Network (tool + agent delegations).
	// Nil when no tools were called.
	Steps []StepTrace

	// FinishReason indicates why the run ended. Zero value is empty string.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	// Sources are citations declared by tools, retrievers, or the model
	// via the Sourced interface. Nil when no source was declared.
	Sources []Source `json:"sources,omitempty"`
	// Files are attachments produced during the run (sandbox artifacts,
	// generated images). Aggregated from EventFileAttachment.
	Files []Attachment `json:"files,omitempty"`
	// Warnings are non-fatal notes accumulated from providers and
	// decorators. Empty when none.
	Warnings []string `json:"warnings,omitempty"`
	// ProviderMeta carries provider-specific opaque metadata from the
	// final LLM call. Nil when no provider populated it.
	ProviderMeta json.RawMessage `json:"provider_meta,omitempty"`
	// SuspendPayload is set when FinishReason == FinishSuspended. Carries
	// the payload from *ErrSuspended for caller convenience.
	SuspendPayload json.RawMessage `json:"suspend_payload,omitempty"`
	// SuspendProtocol is set when FinishReason == FinishSuspended. Carries
	// the typed protocol's tag from *ErrSuspended.tag (see SuspendProtocol[Req, Resp]).
	// Empty for suspends made via the untyped Suspend(json.RawMessage) escape hatch.
	SuspendProtocol string `json:"suspend_protocol,omitempty"`
	// Object is the final structured output when WithResponseSchema was
	// configured. Nil when the schema was not set or the response did
	// not validate.
	Object json.RawMessage `json:"object,omitempty"`
	// Iterations records per-iteration timing and usage. One entry per
	// LLM call. Nil for runs that hit cancellation before the first call.
	Iterations []IterationTrace `json:"iterations,omitempty"`
}

// ModelFunc resolves the LLM provider per-request.
// When set via WithDynamicModel or history.Compress, it is called at the start
// of every Execute/ExecuteStream call (or compression event). The returned
// Provider replaces the construction-time provider for that execution.
// A nil return falls back to the agent's main provider.
type ModelFunc func(ctx context.Context, task AgentTask) Provider

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

// ToolCallTrace is a per-tool-call execution record. It is an alias for
// StepTrace, introduced for naming consistency with IterationTrace and
// LLMCallTrace. New code should use ToolCallTrace; StepTrace is kept as
// a name alias for back-compat for one minor release and will be removed
// in the next major.
type ToolCallTrace = StepTrace

// IterationTrace records one iteration of the agent's tool-calling loop.
// One LLM call plus zero or more tool dispatches. Collected automatically
// during runs and exposed on AgentResult.Iterations.
type IterationTrace struct {
	// Iter is the 0-indexed iteration number.
	Iter int `json:"iter"`
	// Model is the provider model used for this iteration (e.g. "gpt-4o").
	Model string `json:"model,omitempty"`
	// StartedAt is the wall-clock time the iteration began.
	StartedAt time.Time `json:"started_at"`
	// Duration is the wall-clock time for the entire iteration (LLM call
	// + tool dispatches).
	Duration time.Duration `json:"duration"`
	// LLMCall records the model call timing and usage for this iteration.
	LLMCall LLMCallTrace `json:"llm_call"`
	// ToolCalls records the tool calls that fired in this iteration.
	// In execution order. Empty if the iteration was text-only.
	ToolCalls []ToolCallTrace `json:"tool_calls,omitempty"`
	// Usage is the per-iteration token usage (excluding tool-side usage).
	Usage Usage `json:"usage"`
	// FinishReason is the reason this iteration ended. Carries
	// FinishSuspended when a Suspend-class error fired during the iteration,
	// FinishToolCalls when the iteration completed with tool calls pending,
	// FinishStop when the model returned a natural end, etc. Empty only
	// when the iteration is mid-run (during stream events). Mirrors the
	// FinishReason emitted on EventIterationFinish.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
}

// LLMCallTrace records a single LLM model call. Nested inside
// IterationTrace.
type LLMCallTrace struct {
	// Duration is the model-side wall-clock time.
	Duration time.Duration `json:"duration"`
	// InputTokens is the prompt token count for this call.
	InputTokens int `json:"input_tokens"`
	// OutputTokens is the generated token count.
	OutputTokens int `json:"output_tokens"`
	// FinishReason is the model-reported reason for stopping this call.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
}

// Text returns the agent's final text output. Alias for r.Output that exists
// for symmetry with the Stream wrapper, so synchronous and streaming
// consumers use the same method name.
func (r AgentResult) Text() string { return r.Output }

// Reasoning returns the agent's reasoning text from the final LLM call.
// Alias for r.Thinking that exists for symmetry with the Stream wrapper.
func (r AgentResult) Reasoning() string { return r.Thinking }

// ToolCalls returns the tool calls captured in r.Steps, in execution order.
// Returns nil if no tools were called. Each call's Name and Args
// mirror the ToolCall the LLM produced.
func (r AgentResult) ToolCalls() []ToolCall {
	if len(r.Steps) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(r.Steps))
	for _, s := range r.Steps {
		out = append(out, ToolCall{
			Name: s.Name,
			Args: []byte(s.Input),
		})
	}
	return out
}

// ToolResults returns the tool results captured in r.Steps, in execution order.
// Each result's Content mirrors the JSON the tool returned to the LLM.
func (r AgentResult) ToolResults() []ToolResult {
	if len(r.Steps) == 0 {
		return nil
	}
	out := make([]ToolResult, 0, len(r.Steps))
	for _, s := range r.Steps {
		out = append(out, ToolResult{Content: []byte(s.Output)})
	}
	return out
}

// LastStep returns the final StepTrace in r.Steps, or the zero value if no
// steps were recorded.
func (r AgentResult) LastStep() StepTrace {
	if len(r.Steps) == 0 {
		return StepTrace{}
	}
	return r.Steps[len(r.Steps)-1]
}

// StepByTool returns the first StepTrace whose Name matches name.
// Returns (zero, false) if no step matches.
func (r AgentResult) StepByTool(name string) (StepTrace, bool) {
	for _, s := range r.Steps {
		if s.Name == name {
			return s, true
		}
	}
	return StepTrace{}, false
}

// Suspended reports whether the run paused awaiting human input.
// Shorthand for r.FinishReason == FinishSuspended.
func (r AgentResult) Suspended() bool { return r.FinishReason == FinishSuspended }

// SuspendedProtocol returns the typed protocol tag for a suspended run.
// Empty for untyped suspends or runs that did not suspend.
func (r AgentResult) SuspendedProtocol() string { return r.SuspendProtocol }
