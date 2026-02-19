// Package mcp implements a Model Context Protocol (MCP) server over stdio.
// It exposes tools and resources via JSON-RPC 2.0, enabling AI assistants
// (Claude Code, Cursor, etc.) to discover and query framework documentation,
// invoke search, and read individual resource pages.
//
// The protocol follows the MCP specification (revision 2025-03-26).
// Transport is newline-delimited JSON over stdin/stdout.
package mcp

import "encoding/json"

// --- JSON-RPC 2.0 types ---

// request is an incoming JSON-RPC 2.0 request or notification.
// Notifications have a nil ID.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// isNotification returns true if this is a notification (no ID field).
func (r *request) isNotification() bool {
	return len(r.ID) == 0 || string(r.ID) == "null"
}

// response is an outgoing JSON-RPC 2.0 response.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC 2.0 error codes.
const (
	errCodeParse          = -32700
	errCodeInvalidRequest = -32600
	errCodeMethodNotFound = -32601
	errCodeInvalidParams  = -32602
	errCodeInternal       = -32603
)

// --- MCP protocol types ---

// protocolVersion is the MCP protocol version this server implements.
const protocolVersion = "2025-03-26"

// initializeParams is the client's initialize request payload.
type initializeParams struct {
	ProtocolVersion string     `json:"protocolVersion"`
	Capabilities    any        `json:"capabilities"`
	ClientInfo      clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeResult is the server's response to an initialize request.
type initializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    serverCapabilities `json:"capabilities"`
	ServerInfo      serverInfo         `json:"serverInfo"`
}

type serverCapabilities struct {
	Tools     *capability `json:"tools,omitempty"`
	Resources *capability `json:"resources,omitempty"`
}

type capability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// --- Tool types ---

// ToolDefinition describes a tool exposed via MCP.
type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

// toolsListResult is the response to tools/list.
type toolsListResult struct {
	Tools []ToolDefinition `json:"tools"`
}

// toolCallParams is the request payload for tools/call.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// ToolCallResult is the response payload for tools/call.
type ToolCallResult struct {
	Content []textContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// textContent is a text content block in MCP responses.
type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// TextResult creates a successful ToolCallResult with a single text content block.
func TextResult(text string) ToolCallResult {
	return ToolCallResult{
		Content: []textContent{{Type: "text", Text: text}},
	}
}

// ErrorResult creates an error ToolCallResult with a single text content block.
func ErrorResult(text string) ToolCallResult {
	return ToolCallResult{
		Content: []textContent{{Type: "text", Text: text}},
		IsError: true,
	}
}

// --- Resource types ---

// resourceDef describes a resource in resources/list responses.
type resourceDef struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// resourcesListResult is the response to resources/list.
type resourcesListResult struct {
	Resources []resourceDef `json:"resources"`
}

// resourceReadParams is the request payload for resources/read.
type resourceReadParams struct {
	URI string `json:"uri"`
}

// resourceContent is a single content item in a resources/read response.
type resourceContent struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// resourceReadResult is the response to resources/read.
type resourceReadResult struct {
	Contents []resourceContent `json:"contents"`
}
