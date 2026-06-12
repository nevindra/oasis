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
	return toolResultFromOut(out, err)
}

// toolResultFromOut builds the ToolResult for a typed tool invocation's
// (out, err) pair. It is the single shared post-execute tail for every tool
// adapter (Func, Erase, EraseStreaming) — all three decode/coerce their own
// In and make the typed call, then delegate here so the marshal/UI behavior
// can never drift between them.
//
// Why: this tail repeats verbatim across three adapters, and a prior
// divergence (Func omitting the UIRenderable check) shipped silently.
// Centralizing it makes that class of drift impossible.
//
// Error semantics (must match the ToolResult/error contract):
//   - err is an infra error → ToolResult.Error is set AND the Go error
//     propagates so the caller can react to infrastructure failures.
//   - err is a non-infra (business) error → ToolResult.Error is set, Go error
//     is nil.
//   - marshal of out fails → ToolResult.Error carries a "marshal result: "
//     prefix, Go error is nil (a marshal failure is a tool-output bug, not an
//     infrastructure failure).
//   - success → Content is the marshaled JSON; if out implements UIRenderable,
//     UI is populated and UI.Props aliases the same body bytes as Content.
func toolResultFromOut[Out any](out Out, err error) (ToolResult, error) {
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
	return toolResultFromOut(out, err)
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
