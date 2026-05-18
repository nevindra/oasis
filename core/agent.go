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
	// Context carries optional metadata (thread ID, user ID, etc.).
	// Use the With*ID builder methods to set values and the Task*ID accessors to read them.
	Context map[string]any
}

// Context key constants for AgentTask.Context (internal).
const (
	ContextThreadID = "thread_id"
	ContextUserID   = "user_id"
	ContextChatID   = "chat_id"
)

// WithThreadID sets the conversation thread ID on the task and returns it.
func (t AgentTask) WithThreadID(id string) AgentTask {
	if t.Context == nil {
		t.Context = map[string]any{}
	}
	t.Context[ContextThreadID] = id
	return t
}

// WithUserID sets the user ID on the task and returns it.
func (t AgentTask) WithUserID(id string) AgentTask {
	if t.Context == nil {
		t.Context = map[string]any{}
	}
	t.Context[ContextUserID] = id
	return t
}

// WithChatID sets the chat/channel ID on the task and returns it.
func (t AgentTask) WithChatID(id string) AgentTask {
	if t.Context == nil {
		t.Context = map[string]any{}
	}
	t.Context[ContextChatID] = id
	return t
}

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
