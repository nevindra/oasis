package core

import (
	"encoding/json"
	"time"
)

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
	// EventThinking carries the LLM's reasoning/chain-of-thought content.
	// Emitted after each LLM call when the provider returns thinking content.
	EventThinking StreamEventType = "thinking"
	// EventAgentStart signals a subagent has been delegated to (Network only).
	EventAgentStart StreamEventType = "agent-start"
	// EventAgentFinish signals a subagent has completed (Network only).
	EventAgentFinish StreamEventType = "agent-finish"
	// EventToolCallDelta carries an incremental chunk of tool call arguments.
	// Emitted by ChatStream when req.Tools is non-empty. ID carries the tool
	// call ID for correlation with the eventual tool-call-start/result events.
	EventToolCallDelta StreamEventType = "tool-call-delta"
	// EventToolProgress carries intermediate progress from a long-running tool.
	// Emitted by tools that implement StreamingAnyTool. Name carries the tool name;
	// Content carries free-form progress JSON.
	EventToolProgress StreamEventType = "tool-progress"
	// EventStepStart signals a workflow step has begun execution.
	// Name carries the step name.
	EventStepStart StreamEventType = "step-start"
	// EventStepFinish signals a workflow step has completed.
	// Name carries the step name; Content carries the output (success) or
	// error message (failure); Duration carries the step wall-clock time.
	EventStepFinish StreamEventType = "step-finish"
	// EventStepProgress carries intermediate progress from a ForEach workflow step.
	// Name carries the step name; Content carries progress JSON
	// (e.g. {"completed":3,"total":10}).
	EventStepProgress StreamEventType = "step-progress"
	// EventRoutingDecision signals the Network router has decided which agents/tools
	// to invoke. Name carries the network name; Content carries a JSON summary
	// (e.g. {"agents":["researcher"],"tools":["search"]}).
	EventRoutingDecision StreamEventType = "routing-decision"
	// EventFileAttachment signals that a file has been delivered from a sandbox
	// and is available for download. Content carries JSON metadata:
	// {"name":"report.pdf","mime_type":"application/pdf","size":12345,"url":"/api/files/…/download"}.
	EventFileAttachment StreamEventType = "file_attachment"
	// EventMaxIterReached signals that the agent loop hit its iteration limit
	// and is about to force a synthesis LLM call. Content carries a JSON object
	// {"iter":N,"max_iter":M}. Emitted exactly once per execution that hits the
	// cap.
	EventMaxIterReached StreamEventType = "max-iter-reached"
	// EventReasoningStart marks the beginning of a reasoning block from a
	// provider that emits reasoning incrementally (Claude extended thinking,
	// OpenAI o1). No payload.
	EventReasoningStart StreamEventType = "reasoning-start"
	// EventReasoningDelta carries an incremental reasoning text chunk.
	EventReasoningDelta StreamEventType = "reasoning-delta"
	// EventReasoningEnd marks the end of a reasoning block. Content carries
	// the full reassembled reasoning text for consumers that prefer the
	// monolithic form. Always followed by zero or more subsequent events.
	EventReasoningEnd StreamEventType = "reasoning-end"
	// EventHalt signals a processor halted the loop via *ErrHalt. Name carries
	// the processor name; Content carries the canned response shown to the user.
	EventHalt StreamEventType = "halt"
	// EventError signals terminal failure of the run. Content carries the
	// error message. Always immediately followed by channel close. ExecuteStream
	// returns the same error to the caller.
	EventError StreamEventType = "error"
	// EventStreamWarning carries non-fatal stream-wrapper notifications.
	// Content is one of:
	//   - "replay-truncated": ring buffer overflowed; some history lost.
	//   - "subscriber-dropped": a slow subscriber was removed; its channel
	//     received this warning then closed.
	EventStreamWarning StreamEventType = "stream-warning"
	// EventToolApprovalPending is emitted by the tool approval middleware
	// before a guarded tool runs. ID carries the tool call ID; Name carries
	// the tool name; Args carries the proposed arguments. A subsequent
	// EventToolCallStart appears only if the approval is granted.
	EventToolApprovalPending StreamEventType = "tool-approval-pending"
)

// StreamEvent is a typed event emitted during agent streaming.
// Consumers receive these on the channel passed to ExecuteStream.
type StreamEvent struct {
	// Type identifies the event kind.
	Type StreamEventType `json:"type"`
	// ID is the tool call ID for correlation across tool-call-delta,
	// tool-call-start, and tool-call-result events. Empty for non-tool events.
	ID string `json:"id,omitempty"`
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
