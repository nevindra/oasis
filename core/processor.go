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
