package oasis

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

// Erase converts a Tool[In, Out] into AnyTool. Argument unmarshal errors and
// result marshal errors land in ToolResult.Error (business-error channel) per
// the contract that Go errors from ExecuteRaw are reserved for infrastructure
// failures.
func Erase[In, Out any](t Tool[In, Out]) AnyTool {
	return &erasedTool[In, Out]{tool: t}
}

type erasedTool[In, Out any] struct {
	tool Tool[In, Out]
}

func (e *erasedTool[In, Out]) Name() string               { return e.tool.Name() }
func (e *erasedTool[In, Out]) Definition() ToolDefinition { return e.tool.Definition() }
func (e *erasedTool[In, Out]) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
	}
	out, err := e.tool.Execute(ctx, in)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(body)}, nil
}
