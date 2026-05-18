package core

import (
	"context"
	"encoding/json"
)

// Erase converts a Tool[In, Out] into an AnyTool. Argument unmarshal errors
// and result marshal errors land in ToolResult.Error (business-error channel)
// per the contract that Go errors from ExecuteRaw are reserved for
// infrastructure failures.
func Erase[In, Out any](t Tool[In, Out]) AnyTool {
	return &erasedTool[In, Out]{tool: t}
}

type erasedTool[In, Out any] struct {
	tool Tool[In, Out]
}

func (e *erasedTool[In, Out]) Name() string                 { return e.tool.Name() }
func (e *erasedTool[In, Out]) Definition() ToolDefinition   { return e.tool.Definition() }
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

// EraseStreaming converts a StreamingTool[In, Out] into a StreamingAnyTool.
// Argument unmarshal errors and result marshal errors land in ToolResult.Error
// (business-error channel) per the contract that Go errors from ExecuteStream
// are reserved for infrastructure failures.
func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool {
	return &erasedStreamingTool[In, Out]{tool: t}
}

type erasedStreamingTool[In, Out any] struct {
	tool StreamingTool[In, Out]
}

func (e *erasedStreamingTool[In, Out]) Name() string               { return e.tool.Name() }
func (e *erasedStreamingTool[In, Out]) Definition() ToolDefinition { return e.tool.Definition() }

func (e *erasedStreamingTool[In, Out]) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
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

func (e *erasedStreamingTool[In, Out]) ExecuteStream(ctx context.Context, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
	}
	out, err := e.tool.ExecuteStream(ctx, in, ch)
	if err != nil {
		return ToolResult{Error: err.Error()}, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return ToolResult{Content: string(body)}, nil
}
