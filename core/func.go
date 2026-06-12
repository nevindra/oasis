package core

import (
	"context"
	"encoding/json"
)

// Func creates an AnyTool from a plain function. The JSON Schema for In is
// derived by reflection at this call (see [DeriveSchema]). Out is marshaled
// to JSON on each invocation. Panics on unsupported input types — schema
// errors surface at registration time, not at LLM-call time.
//
// Func is the recommended path for stateless tools. For tools that need
// instance state, custom output schema, or streaming, use [Tool] + [Erase].
func Func[In, Out any](name, description string,
	fn func(context.Context, In) (Out, error),
) AnyTool {
	return &funcTool[In, Out]{
		name: name,
		def: ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  DeriveSchema[In](),
		},
		fn: fn,
	}
}

type funcTool[In, Out any] struct {
	name string
	def  ToolDefinition
	fn   func(context.Context, In) (Out, error)
}

func (t *funcTool[In, Out]) Name() string               { return t.name }
func (t *funcTool[In, Out]) Definition() ToolDefinition { return t.def }

func (t *funcTool[In, Out]) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
	var in In
	args = coerceArgs(args)
	if err := json.Unmarshal(args, &in); err != nil {
		return ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	out, err := t.fn(ctx, in)
	return toolResultFromOut(out, err)
}
