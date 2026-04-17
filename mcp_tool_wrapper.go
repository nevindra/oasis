package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const defaultMCPToolCallTimeout = 60 * time.Second

// mcpToolWrapper implements oasis.Tool. Forwards calls to the underlying
// mcp.Client, translating between Oasis types and MCP types.
type mcpToolWrapper struct {
	entry  *mcpToolEntry
	server *mcpServerEntry
	parent *MCPRegistry
}

// Definitions implements oasis.Tool. Returns the single tool definition.
func (w *mcpToolWrapper) Definitions() []ToolDefinition {
	return []ToolDefinition{w.entry.def}
}

// Execute implements oasis.Tool. The name parameter matches w.entry.fullName
// (registry-dispatched); it is not forwarded — the raw MCP name is used instead.
func (w *mcpToolWrapper) Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultMCPToolCallTimeout)
	defer cancel()

	state := MCPServerState(w.server.state.Load())
	if state != MCPStateHealthy {
		return ToolResult{
			Error: fmt.Sprintf("MCP server %q not healthy (%s)", w.server.cfg.mcpServerName(), state),
		}, nil
	}

	// Fire lifecycle hook before the call (best-effort).
	if w.parent != nil {
		w.parent.fireOnToolCall(w.server.cfg.mcpServerName(), w.entry.rawName, args)
	}

	// FIFO per server (single in-flight call per spec α decision Q6).
	w.server.callMu.Lock()
	res, err := w.server.client.CallTool(callCtx, w.entry.rawName, args)
	w.server.callMu.Unlock()

	if err != nil {
		// Transport error → mark unhealthy + surface as ToolResult.Error (PHILOSOPHY §4).
		if w.parent != nil {
			w.parent.markUnhealthy(w.server.cfg.mcpServerName(), err)
		}
		return ToolResult{
			Error: fmt.Sprintf("MCP call to %s failed: %v", w.entry.fullName, err),
		}, nil
	}

	out := mapMCPResult(res)
	if w.parent != nil {
		mres := mapMCPResultToPublic(res)
		w.parent.fireOnToolResult(w.server.cfg.mcpServerName(), w.entry.rawName, mres, nil)
	}
	// mapMCPResult returns *ToolResult; dereference to match value return.
	return *out, nil
}
