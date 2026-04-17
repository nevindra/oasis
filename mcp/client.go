package mcp

import (
	"context"
	"encoding/json"
)

// Client is the transport-agnostic interface to an MCP server.
// Implementations: StdioClient (child process), HTTPClient.
type Client interface {
	// Initialize performs the JSON-RPC handshake and returns the server's
	// declared info + capabilities. Must be called once before ListTools/CallTool.
	Initialize(ctx context.Context) (*InitializeResult, error)

	// ListTools fetches the server's tool catalog. Schemas included.
	// Servers with `tools.listChanged: true` may emit notifications; clients
	// should re-call ListTools when notified (handled by registry layer).
	ListTools(ctx context.Context) (*ListToolsResult, error)

	// CallTool invokes a tool by its server-side raw name (NOT the namespaced
	// mcp__server__tool form). Args are passed through verbatim as JSON.
	CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error)

	// Close releases transport resources (kills child process, closes HTTP
	// connections). Must be idempotent. Best-effort: errors are informational.
	Close(ctx context.Context) error

	// OnDisconnect registers a callback fired when the underlying transport
	// detects that the server is gone (process exit, EOF, etc.). Used by the
	// registry to trigger reconnection. Single callback; second registration
	// replaces the first.
	OnDisconnect(fn func(error))
}
