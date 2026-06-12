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
	inSchema := DeriveSchema[In]()
	outSchema := deriveOutSchema[Out](t)
	return &erasedTool[In, Out]{
		tool: t,
		def: ToolDefinition{
			Name:         meta.Name,
			Description:  meta.Description,
			Parameters:   inSchema,
			OutputSchema: outSchema,
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
	args = coerceArgs(args)
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	out, err := e.tool.Execute(ctx, in)
	if err != nil {
		result := ToolResult{Error: err.Error()}
		if IsInfraError(err) {
			return result, err
		}
		return result, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	res := ToolResult{Content: string(body)}
	if r, ok := any(out).(UIRenderable); ok {
		res.UI = &UIComponent{Name: r.UIComponent(), Props: body}
	}
	return res, nil
}

// EraseStreaming converts a StreamingTool[In, Out] into a StreamingAnyTool.
// Same schema-derivation behavior as Erase.
func EraseStreaming[In, Out any](t StreamingTool[In, Out]) StreamingAnyTool {
	meta := t.Definition()
	inSchema := DeriveSchema[In]()
	outSchema := deriveOutSchema[Out](t)
	return &erasedStreamingTool[In, Out]{
		erasedTool: erasedTool[In, Out]{
			tool: t,
			def: ToolDefinition{
				Name:         meta.Name,
				Description:  meta.Description,
				Parameters:   inSchema,
				OutputSchema: outSchema,
			},
		},
		streamTool: t,
	}
}

// erasedStreamingTool embeds erasedTool to inherit Name, Definition, and
// ExecuteRaw. streamTool holds the streaming-typed reference so ExecuteStream
// can call ExecuteStream without re-typing the embedded tool.
type erasedStreamingTool[In, Out any] struct {
	erasedTool[In, Out]
	streamTool StreamingTool[In, Out]
}

func (e *erasedStreamingTool[In, Out]) ExecuteStream(ctx context.Context, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error) {
	var in In
	args = coerceArgs(args)
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	out, err := e.streamTool.ExecuteStream(ctx, in, ch)
	if err != nil {
		result := ToolResult{Error: err.Error()}
		if IsInfraError(err) {
			return result, err
		}
		return result, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	res := ToolResult{Content: string(body)}
	if r, ok := any(out).(UIRenderable); ok {
		res.UI = &UIComponent{Name: r.UIComponent(), Props: body}
	}
	return res, nil
}

// deriveOutSchema returns the OutputSchema to publish for an erased tool.
// If t implements OutSchemaProvider, its override is used; otherwise the
// schema for Out is derived by reflection. The override is read via a type
// assertion on `any(t)`, mirroring the SchemaProvider pattern in DeriveSchema.
func deriveOutSchema[Out any](t any) json.RawMessage {
	if p, ok := t.(OutSchemaProvider); ok {
		return p.OutSchema()
	}
	return DeriveSchema[Out]()
}
