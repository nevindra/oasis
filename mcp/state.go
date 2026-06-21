package mcp

import (
	"encoding/json"
	"time"
)

// ServerState represents the connection state of an MCP server.
type ServerState int

const (
	StateConnecting   ServerState = iota // 0
	StateHealthy                         // 1
	StateReconnecting                    // 2
	StateDead                            // 3
)

// String returns a human-readable name for the server state.
func (s ServerState) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateHealthy:
		return "healthy"
	case StateReconnecting:
		return "reconnecting"
	case StateDead:
		return "dead"
	default:
		return "unknown"
	}
}

// ServerStatus is a snapshot of an MCP server's runtime state.
type ServerStatus struct {
	Name        string
	Transport   string
	State       ServerState
	ToolCount   int
	LastError   error
	ConnectedAt time.Time
	Server      ServerMetadata
}

// ServerMetadata holds metadata reported by the MCP server during initialisation.
// Distinct from the wire-level ServerInfo (mcp/protocol.go) which carries only
// {Name, Version}; this extends with ProtocolVersion + Capabilities captured
// post-Initialize.
type ServerMetadata struct {
	Name            string
	Version         string
	ProtocolVersion string
	Capabilities    ServerCapabilities
}

// LifecycleHandler receives lifecycle notifications from MCP servers.
// Result type is *CallToolResult (the wire type) — the redundant public
// mirror struct from the previous root-coupled API has been removed.
type LifecycleHandler interface {
	OnConnect(name string, info ServerMetadata)
	OnDisconnect(name string, err error)
	OnToolCall(name, tool string, args json.RawMessage)
	OnToolResult(name, tool string, result *CallToolResult, err error)
}

// NoopLifecycle is a no-op default. Embed it for partial implementations:
//
//	type MyHandler struct{ mcp.NoopLifecycle }
//	func (h MyHandler) OnConnect(name string, info mcp.ServerMetadata) { /* ... */ }
type NoopLifecycle struct{}

func (NoopLifecycle) OnConnect(string, ServerMetadata)                    {}
func (NoopLifecycle) OnDisconnect(string, error)                          {}
func (NoopLifecycle) OnToolCall(string, string, json.RawMessage)          {}
func (NoopLifecycle) OnToolResult(string, string, *CallToolResult, error) {}

// EventType classifies an Event.
type EventType int

const (
	EventConnected           EventType = iota
	EventDisconnected                  // 1
	EventReconnecting                  // 2
	EventToolCall                      // 3
	EventToolResult                    // 4
	EventProgress                      // 5 — tool-call progress (opt-in)
	EventLog                           // 6 — server logging/message
	EventResourceUpdated               // 7 — resources/updated for a subscribed URI
	EventResourceListChanged           // 8 — resources/list_changed
	EventPromptListChanged             // 9 — prompts/list_changed
)

// Event is a single lifecycle event emitted by the registry. Fields below the
// core set are zero-valued for event types that don't use them.
type Event struct {
	Type      EventType
	Server    string
	Tool      string // populated for tool-related events
	Err       error
	Timestamp time.Time

	URI      string   // EventResourceUpdated
	Progress float64  // EventProgress
	Total    float64  // EventProgress (0 = unknown)
	Level    LogLevel // EventLog
	Message  string   // EventProgress / EventLog
}
