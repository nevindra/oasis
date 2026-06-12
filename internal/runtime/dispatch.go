package runtime

import (
	"context"
	"encoding/json"

	"github.com/nevindra/oasis/core"
)

// DispatchResult holds the result of a single tool or agent dispatch.
type DispatchResult struct {
	Content     string
	Usage       core.Usage
	Attachments []core.Attachment
	// IsError signals that Content represents an error message.
	IsError bool
	// UI, when non-nil, carries a renderable component descriptor produced by
	// the tool. Copied from ToolResult.UI on the success path.
	UI *core.UIComponent
}

// DispatchFunc executes a single tool call and returns the result.
type DispatchFunc func(ctx context.Context, tc core.ToolCall) DispatchResult

// ToolExecFunc executes a tool by name.
type ToolExecFunc = func(ctx context.Context, name string, args json.RawMessage) (core.ToolResult, error)

// ToolExecStreamFunc executes a tool with streaming progress support.
type ToolExecStreamFunc = func(ctx context.Context, name string, args json.RawMessage, ch chan<- core.StreamEvent) (core.ToolResult, error)
