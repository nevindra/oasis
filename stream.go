package oasis

import "encoding/json"

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
}
