package oasis

import (
	"encoding/json"
	"time"
)

// MCPServerState represents the connection state of an MCP server.
type MCPServerState int

const (
	MCPStateConnecting   MCPServerState = iota // 0
	MCPStateHealthy                            // 1
	MCPStateReconnecting                       // 2
	MCPStateDead                               // 3
)

// String returns a human-readable name for the server state.
func (s MCPServerState) String() string {
	switch s {
	case MCPStateConnecting:
		return "connecting"
	case MCPStateHealthy:
		return "healthy"
	case MCPStateReconnecting:
		return "reconnecting"
	case MCPStateDead:
		return "dead"
	default:
		return "unknown"
	}
}

// MCPServerStatus is a snapshot of an MCP server's runtime state.
type MCPServerStatus struct {
	Name        string
	Transport   string
	State       MCPServerState
	ToolCount   int
	LastError   error
	ConnectedAt time.Time
	ServerInfo  MCPServerInfo
}

// MCPServerInfo holds metadata reported by the MCP server during initialisation.
type MCPServerInfo struct {
	Name            string
	Version         string
	ProtocolVersion string
	Capabilities    map[string]interface{}
}

// MCPToolResult is the structured result returned by a remote MCP tool call.
type MCPToolResult struct {
	Content []MCPContent
	IsError bool
	Meta    json.RawMessage
}

// MCPContent is a single content block inside an MCPToolResult.
type MCPContent struct {
	Type     string // "text" | "image" | "resource"
	Text     string
	Data     string // base64-encoded for images
	MimeType string
	URI      string
}

// MCPLifecycleHandler receives lifecycle notifications from the MCP client.
type MCPLifecycleHandler interface {
	OnConnect(name string, info MCPServerInfo)
	OnDisconnect(name string, err error)
	OnToolCall(name, tool string, args json.RawMessage)
	OnToolResult(name, tool string, result *MCPToolResult, err error)
}

// NoopMCPLifecycle is a no-op default. Embed it for partial implementations:
//
//	type MyHandler struct{ oasis.NoopMCPLifecycle }
//	func (h MyHandler) OnConnect(name string, info oasis.MCPServerInfo) { /* ... */ }
type NoopMCPLifecycle struct{}

func (NoopMCPLifecycle) OnConnect(string, MCPServerInfo)                    {}
func (NoopMCPLifecycle) OnDisconnect(string, error)                         {}
func (NoopMCPLifecycle) OnToolCall(string, string, json.RawMessage)         {}
func (NoopMCPLifecycle) OnToolResult(string, string, *MCPToolResult, error) {}

// MCPEventType classifies an MCPEvent.
type MCPEventType int

const (
	MCPEventConnected    MCPEventType = iota
	MCPEventDisconnected              // 1
	MCPEventReconnecting              // 2
	MCPEventToolCall                  // 3
	MCPEventToolResult                // 4
)

// MCPEvent is a single lifecycle event emitted by the MCP client.
type MCPEvent struct {
	Type      MCPEventType
	Server    string
	Tool      string // populated for tool-related events
	Err       error
	Timestamp time.Time
}

// MCPAccessor is the optional capability interface for agents that expose MCP
// server management. Currently implemented only by *LLMAgent. Future Agent
// implementations (Network, custom) may implement this without breaking the
// Agent interface.
//
// Use a type assertion to check whether an Agent supports MCP:
//
//	if ma, ok := agent.(oasis.MCPAccessor); ok {
//	    ma.MCP().Register(ctx, cfg)
//	}
type MCPAccessor interface {
	Agent
	MCP() *MCPController
}
