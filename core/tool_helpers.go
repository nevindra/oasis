package core

import (
	"encoding/json"
	"strconv"
)

// JSONContent wraps already-encoded JSON bytes as a ToolResult Content value.
func JSONContent(raw []byte) json.RawMessage { return raw }

// TextContent wraps a plain string as a JSON-quoted RawMessage suitable for
// ToolResult.Content from hand-rolled (non-Erase) tools.
func TextContent(s string) json.RawMessage {
	return json.RawMessage(strconv.Quote(s))
}

// TextResult is a convenience for hand-rolled tools producing plain text.
func TextResult(s string) ToolResult {
	return ToolResult{Content: TextContent(s)}
}
