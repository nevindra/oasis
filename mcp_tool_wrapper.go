package oasis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

const defaultMCPToolCallTimeout = 60 * time.Second

// mcpToolWrapper implements oasis.AnyTool. Forwards calls to the underlying
// mcp.Client, translating between Oasis types and MCP types. Each wrapper
// represents exactly one MCP tool (one definition).
type mcpToolWrapper struct {
	entry  *mcpToolEntry
	server *mcpServerEntry
	parent *MCPRegistry
}

// Name implements oasis.AnyTool. Returns the MCP tool's full registry name.
func (w *mcpToolWrapper) Name() string {
	if d := w.entry.def.Load(); d != nil {
		return d.Name
	}
	return w.entry.fullName
}

// Definition implements oasis.AnyTool. Returns the single tool definition.
// Reads through atomic.Pointer so a concurrent ensureSchema swap is observed
// safely (no torn reads of the Parameters slice header). Returns a zero
// ToolDefinition if the entry has not been populated yet.
func (w *mcpToolWrapper) Definition() ToolDefinition {
	if d := w.entry.def.Load(); d != nil {
		return *d
	}
	return ToolDefinition{}
}

// EnsureSchema implements oasis.SchemaEnsurer. Loads the cached schema (or
// re-fetches from the server on cache miss) into the tool's ToolDefinition.
// Idempotent and safe for concurrent calls — schemaMu serializes the swap.
func (w *mcpToolWrapper) EnsureSchema(ctx context.Context) error {
	w.entry.schemaMu.Lock()
	defer w.entry.schemaMu.Unlock()

	cur := w.entry.def.Load()
	if cur != nil && len(cur.Parameters) > 0 {
		return nil
	}

	var newSchema json.RawMessage
	if raw, ok := w.entry.schema.Load().(json.RawMessage); ok && len(raw) > 0 {
		newSchema = raw
	} else {
		// Cache miss: re-fetch from the server. Possible if the server mutated
		// its tool list since registration (notifications/tools/list_changed).
		state := MCPServerState(w.server.state.Load())
		if state != MCPStateHealthy {
			return fmt.Errorf("cannot ensure schema for %q: server not healthy (%s)",
				w.entry.fullName, state)
		}
		list, err := w.server.client.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		for _, t := range list.Tools {
			if t.Name != w.entry.rawName {
				continue
			}
			if raw, ok := t.InputSchema.(json.RawMessage); ok {
				newSchema = raw
			} else if b, merr := json.Marshal(t.InputSchema); merr == nil {
				newSchema = b
			}
			break
		}
		if len(newSchema) == 0 {
			return fmt.Errorf("tool %q not found on server after refetch", w.entry.rawName)
		}
	}

	newDef := ToolDefinition{
		Name:        cur.Name,
		Description: cur.Description,
		Parameters:  newSchema,
	}
	w.entry.def.Store(&newDef)
	return nil
}

// ExecuteRaw implements oasis.AnyTool. The raw MCP tool name (w.entry.rawName)
// is forwarded to the server.
func (w *mcpToolWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (ToolResult, error) {
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
