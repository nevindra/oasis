package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	oasis "github.com/nevindra/oasis"
)

const defaultToolCallTimeout = 60 * time.Second

// toolWrapper implements oasis.AnyTool. Forwards calls to the underlying
// Client, translating between Oasis types and MCP wire types. Each wrapper
// represents exactly one MCP tool.
type toolWrapper struct {
	entry  *toolEntry
	server *serverEntry
	parent *Registry
}

// Name implements oasis.AnyTool. Returns the MCP tool's full registry name.
func (w *toolWrapper) Name() string {
	if d := w.entry.def.Load(); d != nil {
		return d.Name
	}
	return w.entry.fullName
}

// Definition implements oasis.AnyTool.
func (w *toolWrapper) Definition() oasis.ToolDefinition {
	if d := w.entry.def.Load(); d != nil {
		return *d
	}
	return oasis.ToolDefinition{}
}

// EnsureSchema implements oasis.SchemaEnsurer. Loads the cached schema (or
// re-fetches from the server on cache miss) into the tool's ToolDefinition.
func (w *toolWrapper) EnsureSchema(ctx context.Context) error {
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
		state := ServerState(w.server.state.Load())
		if state != StateHealthy {
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

	newDef := oasis.ToolDefinition{
		Name:        cur.Name,
		Description: cur.Description,
		Parameters:  newSchema,
	}
	w.entry.def.Store(&newDef)
	return nil
}

// ExecuteRaw implements oasis.AnyTool.
func (w *toolWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultToolCallTimeout)
	defer cancel()

	state := ServerState(w.server.state.Load())
	if state != StateHealthy {
		return oasis.ToolResult{
			Error: fmt.Sprintf("MCP server %q not healthy (%s)", w.server.cfg.serverName(), state),
		}, nil
	}

	if w.parent != nil {
		w.parent.fireOnToolCall(w.server.cfg.serverName(), w.entry.rawName, args)
	}

	w.server.callMu.Lock()
	res, err := w.server.client.CallTool(callCtx, w.entry.rawName, args)
	w.server.callMu.Unlock()

	if err != nil {
		if w.parent != nil {
			w.parent.markUnhealthy(w.server.cfg.serverName(), err)
		}
		return oasis.ToolResult{
			Error: fmt.Sprintf("MCP call to %s failed: %v", w.entry.fullName, err),
		}, nil
	}

	out := mapMCPResult(res)
	if w.parent != nil {
		w.parent.fireOnToolResult(w.server.cfg.serverName(), w.entry.rawName, res, nil)
	}
	return *out, nil
}
