package oasis

import (
	"time"

	"github.com/nevindra/oasis/mcp"
)

// MCPServerConfig is implemented by transport-specific configs
// (StdioMCPConfig, HTTPMCPConfig). Users do not implement this directly.
type MCPServerConfig interface {
	mcpServerName() string
	isMCPServerConfig()
}

// StdioMCPConfig configures an MCP server launched as a child process via stdio.
type StdioMCPConfig struct {
	Name     string
	Command  string
	Args     []string
	Env      map[string]string // merged with os.Environ() at spawn time
	WorkDir  string            // default: current working directory
	Disabled bool
	Filter   *MCPToolFilter
	Aliases  map[string]string // raw tool name → registry short name
}

func (c StdioMCPConfig) mcpServerName() string { return c.Name }
func (c StdioMCPConfig) isMCPServerConfig()    {}

// HTTPMCPConfig configures an MCP server accessed via HTTP/SSE.
type HTTPMCPConfig struct {
	Name     string
	URL      string
	Headers  map[string]string // applied to every request; ${ENV_VAR} interpolation done by loader
	Auth     Auth              // pluggable; nil = no auth (use Headers)
	Timeout  time.Duration     // per-request; default 30s if zero
	Disabled bool
	Filter   *MCPToolFilter
	Aliases  map[string]string
}

func (c HTTPMCPConfig) mcpServerName() string { return c.Name }
func (c HTTPMCPConfig) isMCPServerConfig()    {}

// Auth applies authentication to outgoing HTTP requests. Re-exported
// from mcp package for ergonomic user-facing API.
type Auth = mcp.Auth

// BearerAuth re-exported from mcp package.
type BearerAuth = mcp.BearerAuth

// MCPToolFilter restricts which tools are exposed from a server.
// Include and Exclude are glob patterns matched against the raw tool name
// (before alias). Mutually exclusive: setting both causes a registration error.
type MCPToolFilter struct {
	Include []string
	Exclude []string
}
