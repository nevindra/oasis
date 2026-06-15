package core

import "context"

// PreProcessor runs before messages are sent to the LLM.
// Implementations can modify the request (add/remove/transform messages)
// or return an error to halt execution.
// Return ErrHalt to short-circuit with a canned response.
// Must be safe for concurrent use.
type PreProcessor interface {
	PreLLM(ctx context.Context, req *ChatRequest) error
}

// PostProcessor runs after the LLM responds, before tool execution.
// Implementations can modify the response (transform content, filter tool calls)
// or return an error to halt execution.
// Return ErrHalt to short-circuit with a canned response.
// Must be safe for concurrent use.
type PostProcessor interface {
	PostLLM(ctx context.Context, resp *ChatResponse) error
}

// PostToolProcessor runs after each tool execution, before the result
// is appended to the message history.
// Implementations can modify the result (redact content, transform output)
// or return an error to halt execution.
// Return ErrHalt to short-circuit with a canned response.
// Must be safe for concurrent use.
type PostToolProcessor interface {
	PostTool(ctx context.Context, call ToolCall, result *ToolResult) error
}

// StreamProcessor runs on each streamed text/thinking delta before it reaches
// the caller's channel. It is an optional capability: processors opt in by
// implementing it, and the chain invokes it only for registered implementers.
//
// Return the event (possibly mutated) to forward it, nil to drop it, or an
// error to fail the stream. Return *ErrHalt to stop the stream and surface a
// canned response. The hook is per-event and stateless from the framework's
// view; a processor needing cross-chunk context manages its own buffering.
// Must be safe for concurrent use.
type StreamProcessor interface {
	PostChunk(ctx context.Context, ev *StreamEvent) (*StreamEvent, error)
}

// ErrHalt signals that a processor wants to stop agent execution and return
// a specific response to the caller.
//
// To halt, return a pointer: `return &core.ErrHalt{Response: "..."}`. The
// `Error()` method has a pointer receiver, so only *ErrHalt satisfies the
// error interface; a value `ErrHalt{...}` would not match. The agent loop
// catches *ErrHalt via errors.As and returns AgentResult{Output: Response}
// with a nil error.
type ErrHalt struct {
	Response string
}

func (e *ErrHalt) Error() string { return "processor halted: " + e.Response }
