package core

import (
	"encoding/json"
	"time"
)

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
	// EventRunStart is the first event on every stream. Name carries the
	// agent name; Content carries the task input.
	EventRunStart StreamEventType = "run-start"
	// EventRunFinish is the last event on every stream before channel close.
	// FinishReason indicates why the run ended; Warnings, ProviderMeta carry
	// additional context. Content carries the final output for FinishHalted
	// (canned response) and FinishSuspended (suspend payload as text).
	EventRunFinish StreamEventType = "run-finish"
	// EventIterationStart marks the beginning of one LLM-call iteration.
	// Name carries the iteration index ("0", "1", ...).
	EventIterationStart StreamEventType = "iteration-start"
	// EventIterationFinish marks the end of one LLM-call iteration. Usage
	// carries that iteration's token usage; Duration carries wall-clock time.
	EventIterationFinish StreamEventType = "iteration-finish"
	// EventObjectDelta carries a partial JSON snapshot of the structured
	// output produced under WithResponseSchema. Object carries the snapshot
	// bytes. Emitted only when ResponseSchema is set on the request.
	EventObjectDelta StreamEventType = "object-delta"
	// EventObjectFinish carries the final validated structured output.
	// Object carries the final JSON bytes. Always preceded by zero or more
	// EventObjectDelta events.
	EventObjectFinish StreamEventType = "object-finish"
	// EventElementDelta is emitted once per completed array element when the
	// top-level schema is a JSON array (e.g. []Item). Content / Object carry
	// the just-completed element. Not emitted for nested arrays.
	EventElementDelta StreamEventType = "element-delta"
	// EventToolCallSuspended is emitted when a tool dispatch (the tool's
	// ExecuteRaw call, a tool middleware, or a PostToolProcessor) returns a
	// Suspend-class error. ID carries the tool call ID; Name carries the tool
	// name; Args carries the tool's input arguments (same as the preceding
	// EventToolCallStart); Protocol carries the typed protocol tag (empty for
	// untyped Suspend); SuspendPayload carries the bytes from Suspend().
	// No EventToolCallResult follows.
	EventToolCallSuspended StreamEventType = "tool-call-suspended"
	// EventStepSuspended is emitted when a workflow step's Execute returns
	// a Suspend-class error. Name carries the step name; Protocol carries
	// the typed protocol tag (empty for untyped Suspend); SuspendPayload
	// carries the bytes from Suspend().
	EventStepSuspended StreamEventType = "step-suspended"
	// EventProcessorSuspended is emitted when a PreLLM, PostLLM, or PostTool
	// processor returns a Suspend-class error. Name carries the processor's
	// reflect.Type name; Protocol carries the typed protocol tag (empty for
	// untyped Suspend); SuspendPayload carries the bytes from Suspend().
	// Content carries the processor kind: "pre", "post", or "post-tool".
	EventProcessorSuspended StreamEventType = "processor-suspended"
	// EventUIComponent signals a tool produced a renderable UI component.
	// ID correlates with the preceding EventToolCallStart/Result; Name carries
	// the component name; Object carries the props JSON. Emitted directly after
	// the tool's EventToolCallResult on the success path only.
	EventUIComponent StreamEventType = "ui-component"
)

// AllStreamEventTypes returns every StreamEventType constant defined by the
// framework. Consumers can use this in tests to verify exhaustive handling
// of event types across oasis upgrades.
func AllStreamEventTypes() []StreamEventType {
	return []StreamEventType{
		EventTextDelta,
		EventToolCallStart,
		EventToolCallResult,
		EventUIComponent,
		EventThinking,
		EventAgentStart,
		EventAgentFinish,
		EventToolCallDelta,
		EventToolProgress,
		EventStepStart,
		EventStepFinish,
		EventStepProgress,
		EventRoutingDecision,
		EventFileAttachment,
		EventMaxIterReached,
		EventReasoningStart,
		EventReasoningDelta,
		EventReasoningEnd,
		EventHalt,
		EventError,
		EventStreamWarning,
		EventToolApprovalPending,
		EventRunStart,
		EventRunFinish,
		EventIterationStart,
		EventIterationFinish,
		EventObjectDelta,
		EventObjectFinish,
		EventElementDelta,
		EventToolCallSuspended,
		EventStepSuspended,
		EventProcessorSuspended,
	}
}

// FinishReason describes why an agent run ended. It is carried on
// EventRunFinish and on AgentResult.FinishReason.
type FinishReason string

const (
	// FinishStop — model produced a natural stop (no further tool calls).
	FinishStop FinishReason = "stop"
	// FinishToolCalls — model stopped to request tool calls. Intermediate
	// state on per-iteration finish; not emitted on EventRunFinish.
	FinishToolCalls FinishReason = "tool-calls"
	// FinishLength — model hit max_tokens before completing.
	FinishLength FinishReason = "length"
	// FinishContentFilter — provider safety / content filter blocked output.
	FinishContentFilter FinishReason = "content-filter"
	// FinishHalted — a processor returned *ErrHalt. Content carries the
	// canned response; Name carries the processor name on EventRunFinish.
	FinishHalted FinishReason = "halted"
	// FinishSuspended — the run paused awaiting human input. SuspendPayload
	// on AgentResult carries the payload (if any).
	FinishSuspended FinishReason = "suspended"
	// FinishMaxIter — the run hit the MaxIter cap before completing.
	FinishMaxIter FinishReason = "max-iterations"
	// FinishError — the run terminated with an error.
	FinishError FinishReason = "error"
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
	// FinishReason is set on EventRunFinish events only. Empty on other types.
	FinishReason FinishReason `json:"finish_reason,omitempty"`
	// Warnings is set on EventRunFinish events when the run accumulated
	// non-fatal provider warnings (e.g. fallback model used, rate-limit
	// throttling, deprecated parameter ignored). Empty on other events.
	Warnings []string `json:"warnings,omitempty"`
	// ProviderMeta carries provider-specific opaque metadata on
	// EventRunFinish (e.g. Gemini safety ratings, Anthropic stop reason).
	// Consumers may decode it according to the provider's documentation.
	ProviderMeta json.RawMessage `json:"provider_meta,omitempty"`
	// Object carries the partial JSON snapshot on EventObjectDelta and
	// the final validated bytes on EventObjectFinish / EventElementDelta.
	// Empty on all other event types.
	Object json.RawMessage `json:"object,omitempty"`
	// Protocol carries the typed SuspendProtocol's tag on EventToolCallSuspended,
	// EventStepSuspended, EventProcessorSuspended, EventToolApprovalPending,
	// and EventRunFinish (when FinishReason=FinishSuspended). Empty for events
	// not related to a suspend, and for suspends made via the untyped
	// Suspend(json.RawMessage) escape hatch.
	Protocol string `json:"protocol,omitempty"`
	// SuspendPayload carries the suspend payload bytes on the same set of
	// events listed under Protocol. Nil for non-suspend events. Distinct from
	// Args (which carries tool call arguments) to avoid semantic overload —
	// EventToolCallSuspended carries both: Args is the proposed tool input,
	// SuspendPayload is the human-facing context.
	SuspendPayload json.RawMessage `json:"suspend_payload,omitempty"`
}
