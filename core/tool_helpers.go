package core

import (
	"context"
	"encoding/json"
)

// JSONContent converts pre-encoded JSON bytes to a string for ToolResult.Content.
func JSONContent(raw []byte) string { return string(raw) }

// TextResult is a convenience for hand-rolled tools producing plain text.
func TextResult(s string) ToolResult {
	return ToolResult{Content: s}
}

// JSONResult marshals v to JSON and returns a ToolResult with the encoded string
// as Content. Panics if json.Marshal fails — a marshal failure on a value the
// caller constructs is a programming error, not a runtime condition.
func JSONResult[T any](v T) ToolResult {
	b, err := json.Marshal(v)
	if err != nil {
		panic("core.JSONResult: json.Marshal failed: " + err.Error())
	}
	return ToolResult{Content: string(b)}
}

// UIResult builds a ToolResult that renders as the named frontend component.
// props is marshaled to JSON for both UI.Props and Content, so the LLM still
// "sees" the data it rendered and the loop can continue with context. Panics
// on marshal failure — a programming error, matching JSONResult's convention.
func UIResult[T any](name string, props T) ToolResult {
	b, err := json.Marshal(props)
	if err != nil {
		panic("core.UIResult: json.Marshal failed: " + err.Error())
	}
	return ToolResult{
		Content: string(b),
		UI:      &UIComponent{Name: name, Props: b},
	}
}

// ErrorResult returns a ToolResult carrying msg as the error string and no
// content. Use this when a tool execution fails and the error should be
// surfaced to the LLM as a tool result rather than returned as a Go error.
func ErrorResult(msg string) ToolResult {
	return ToolResult{Error: msg}
}

// Text returns the Content string directly.
func (r ToolResult) Text() string {
	return r.Content
}

// RawTool constructs an AnyTool from its constituent parts without requiring a
// named struct type. Useful for tests and for tools whose logic is expressed as
// a closure rather than a named type.
func RawTool(
	name, description string,
	schema json.RawMessage,
	fn func(ctx context.Context, args json.RawMessage) (ToolResult, error),
) AnyTool {
	return &rawTool{
		name: name,
		def: ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  schema,
		},
		fn: fn,
	}
}

type rawTool struct {
	name string
	def  ToolDefinition
	fn   func(context.Context, json.RawMessage) (ToolResult, error)
}

func (t *rawTool) Name() string               { return t.name }
func (t *rawTool) Definition() ToolDefinition { return t.def }
func (t *rawTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	return t.fn(ctx, args)
}
