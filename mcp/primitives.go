package mcp

import "errors"

// ErrUnsupported is returned by Registry capability methods when the target
// server did not advertise the capability or its transport cannot support it.
var ErrUnsupported = errors.New("mcp: capability not supported by server or transport")

// LogLevel is an RFC 5424 syslog severity used by logging/setLevel and reported
// on EventLog.
type LogLevel string

const (
	LogLevelDebug     LogLevel = "debug"
	LogLevelInfo      LogLevel = "info"
	LogLevelNotice    LogLevel = "notice"
	LogLevelWarning   LogLevel = "warning"
	LogLevelError     LogLevel = "error"
	LogLevelCritical  LogLevel = "critical"
	LogLevelAlert     LogLevel = "alert"
	LogLevelEmergency LogLevel = "emergency"
)

// ResourceInfo is a resource advertised by an MCP server (a resources/list
// entry). Named ResourceInfo to avoid collision with the server-side Resource
// (mcp/server.go); mirrors the os.FileInfo descriptor convention.
type ResourceInfo struct {
	URI         string
	Name        string
	Description string
	MimeType    string
}

// ResourceContent is one content item returned by resources/read. Exactly one
// of Text (text resources) or Blob (base64 binary) is populated.
type ResourceContent struct {
	URI      string
	MimeType string
	Text     string
	Blob     string
}

// Prompt is a prompt template advertised by an MCP server (prompts/list entry).
type Prompt struct {
	Name        string
	Description string
	Arguments   []PromptArgument
}

// PromptArgument describes a single argument accepted by a prompt template.
type PromptArgument struct {
	Name        string
	Description string
	Required    bool
}

// PromptResult is the result of prompts/get.
type PromptResult struct {
	Description string
	Messages    []PromptMessage
}

// PromptMessage is one message in a prompt result. Content reuses ContentBlock.
type PromptMessage struct {
	Role    string // "user" | "assistant"
	Content ContentBlock
}

// Root is a filesystem boundary advertised to an MCP server. URI must be a
// file:// URI.
type Root struct {
	URI  string
	Name string
}
