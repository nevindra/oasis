package core

import "encoding/json"

// ToolTransform rewrites a tool's payload per sink. Attach one per tool via
// agent.ToolConfig.Transforms (keyed by tool name) or a TransformMatcher.
//
// The three sinks are independent: a tool result can be redacted for the UI
// (Display) and the persisted transcript (Transcript) while the model still
// receives the unredacted payload (Model left nil). The zero value is a no-op —
// every nil sink/field passes the raw payload through unchanged.
type ToolTransform struct {
	// Model rewrites what the LLM sees. Replaces the removed
	// agent.TransformMiddleware.
	Model *SinkTransform
	// Display rewrites what the UI stream receives.
	Display *SinkTransform
	// Transcript rewrites what is persisted (step trace, result store, memory).
	Transcript *SinkTransform
}

// SinkTransform rewrites the two payload kinds destined for one sink. Either
// field may be nil (passthrough).
type SinkTransform struct {
	// Result rewrites the tool's output/error before this sink. The function
	// receives and returns a whole ToolResult so it may edit Content and Error.
	// (Per-sink wiring: Display also applies UI; Model/Transcript apply
	// Content+Error. See the tool-payload-transforms design doc.)
	Result func(name string, r ToolResult) ToolResult
	// Args rewrites the tool-call arguments before this sink. Not consulted for
	// the Model sink (the model authored its own arguments).
	Args func(name string, args json.RawMessage) json.RawMessage
}
