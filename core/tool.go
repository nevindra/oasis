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
	Name() string
	Definition() ToolDefinition
	ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// StreamingAnyTool is the optional streaming capability for AnyTool.
type StreamingAnyTool interface {
	AnyTool
	ExecuteStream(ctx context.Context, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error)
}

// ToolMeta is the author-written metadata for a typed Tool[In, Out]. Erase
// composes it with the reflection-derived JSON Schema (from In) to build
// the full ToolDefinition shown to the LLM.
type ToolMeta struct {
	Name        string
	Description string
}

// Tool is the type-safe atomic tool interface. One Tool = one operation.
// Authors write Definition() ToolMeta (name + description) and Execute;
// the input schema is derived from In by reflection inside Erase.
//
// Use Erase to register with the loop.
type Tool[In, Out any] interface {
	Definition() ToolMeta
	Execute(ctx context.Context, in In) (Out, error)
}

// StreamingTool is the type-safe streaming-capable tool interface. A tool
// that satisfies StreamingTool[In, Out] also satisfies Tool[In, Out].
// Use EraseStreaming to register with the loop.
type StreamingTool[In, Out any] interface {
	Tool[In, Out]
	ExecuteStream(ctx context.Context, in In, ch chan<- StreamEvent) (Out, error)
}
