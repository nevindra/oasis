package core

import (
	"context"
	"encoding/json"
)

// Erase converts a Tool[In, Out] into an AnyTool. The JSON Schema for In is
// derived by reflection at this call (see DeriveSchema). Panics on
// unsupported types — schema-shape errors surface at registration time
// rather than at LLM-call time.
func Erase[In, Out any](t Tool[In, Out]) AnyTool {
	meta := t.Definition()
	schema := DeriveSchema[In]()
	return &erasedTool[In, Out]{
		tool: t,
		def: ToolDefinition{
			Name:        meta.Name,
			Description: meta.Description,
			Parameters:  schema,
		},
	}
}

type erasedTool[In, Out any] struct {
	tool Tool[In, Out]
	def  ToolDefinition
}

func (e *erasedTool[In, Out]) Name() string               { return e.def.Name }
func (e *erasedTool[In, Out]) Definition() ToolDefinition { return e.def }

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
	return ToolResult{Content: body}, nil
}

// EraseStreaming converts a StreamingTool[In, Out] into a StreamingAnyTool.
// Same schema-derivation behavior as Erase.
func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool {
	meta := t.Definition()
	schema := DeriveSchema[In]()
	return &erasedStreamingTool[In, Out]{
		tool: t,
		def: ToolDefinition{
			Name:        meta.Name,
			Description: meta.Description,
			Parameters:  schema,
		},
	}
}

type erasedStreamingTool[In, Out any] struct {
	tool StreamingTool[In, Out]
	def  ToolDefinition
}

func (e *erasedStreamingTool[In, Out]) Name() string               { return e.def.Name }
func (e *erasedStreamingTool[In, Out]) Definition() ToolDefinition { return e.def }

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
	return ToolResult{Content: body}, nil
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
	return ToolResult{Content: body}, nil
}
