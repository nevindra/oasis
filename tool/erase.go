package tool

import (
	"context"
	"encoding/json"

	"github.com/nevindra/oasis/core"
)

// Erase converts a core.Tool[In, Out] into a core.AnyTool. Argument unmarshal
// errors and result marshal errors land in core.ToolResult.Error (business-error
// channel) per the contract that Go errors from ExecuteRaw are reserved for
// infrastructure failures.
func Erase[In, Out any](t core.Tool[In, Out]) core.AnyTool {
	return &erasedTool[In, Out]{tool: t}
}

type erasedTool[In, Out any] struct {
	tool core.Tool[In, Out]
}

func (e *erasedTool[In, Out]) Name() string                    { return e.tool.Name() }
func (e *erasedTool[In, Out]) Definition() core.ToolDefinition { return e.tool.Definition() }
func (e *erasedTool[In, Out]) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
	var in In
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return core.ToolResult{Error: "invalid args: " + err.Error()}, nil
		}
	}
	out, err := e.tool.Execute(ctx, in)
	if err != nil {
		return core.ToolResult{Error: err.Error()}, nil
	}
	body, err := json.Marshal(out)
	if err != nil {
		return core.ToolResult{Error: "marshal result: " + err.Error()}, nil
	}
	return core.ToolResult{Content: string(body)}, nil
}
