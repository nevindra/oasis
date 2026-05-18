package core

import (
	"context"
	"encoding/json"
)

// AnyTool is the type-erased tool interface consumed by the execution loop.
// Implementations describe exactly one operation; bundle-style tools (one
// implementation exposing many definitions) are not supported by this shape.
//
// AnyTool exists because the loop iterates a heterogeneous []AnyTool whose
// elements have different concrete In/Out types. Use Tool[In, Out] for
// type-safe authoring and Erase to convert to AnyTool for registration.
type AnyTool interface {
	// Name returns the unique tool name as advertised to the LLM.
	Name() string

	// Definition returns the JSON-schema description of this tool's inputs.
	// The Name field of the returned ToolDefinition must equal Name().
	Definition() ToolDefinition

	// ExecuteRaw runs the tool with JSON-encoded arguments and returns a
	// ToolResult. Business errors go in ToolResult.Error; the returned Go
	// error is reserved for infrastructure failures (network, panic recovery).
	ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// StreamingAnyTool is the optional streaming capability for AnyTool. The
// registry calls ExecuteStream when ch is non-nil and the tool implements it;
// otherwise falls back to ExecuteRaw.
type StreamingAnyTool interface {
	AnyTool
	ExecuteStream(ctx context.Context, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error)
}

// Tool is the type-safe atomic tool interface. One Tool = one operation.
// Use Erase to register with the loop.
type Tool[In, Out any] interface {
	Name() string
	Definition() ToolDefinition
	Execute(ctx context.Context, in In) (Out, error)
}

// StreamingTool is the type-safe streaming-capable tool interface. A tool that
// satisfies StreamingTool[In, Out] also satisfies Tool[In, Out] — non-streaming
// callers can still invoke Execute. Use EraseStreaming to register with the loop.
//
// Why this shape: mirrors the Tool[In, Out] / AnyTool / StreamingAnyTool
// triangle. EraseStreaming is a separate function rather than overloading
// Erase because Go generics on interfaces cannot easily branch on whether T
// also satisfies streaming.
type StreamingTool[In, Out any] interface {
	Tool[In, Out]
	// ExecuteStream runs the tool while emitting StreamEvents into ch.
	// The caller owns ch and closes it; ExecuteStream must not close ch.
	ExecuteStream(ctx context.Context, in In, ch chan<- StreamEvent) (Out, error)
}
