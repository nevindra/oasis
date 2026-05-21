package core

import (
	"context"
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
