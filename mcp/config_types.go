package mcp

import "time"

// ServerConfig is implemented by transport-specific configs (StdioConfig,
// HTTPConfig). Users do not implement this directly.
type ServerConfig interface {
	serverName() string
	isServerConfig()
}

// StdioConfig configures an MCP server launched as a child process via stdio.
type StdioConfig struct {
	Name     string
	Command  string
	Args     []string
	Env      map[string]string // merged with os.Environ() at spawn time
	WorkDir  string            // default: current working directory
	Disabled bool
	Filter   *ToolFilter
	Aliases  map[string]string // raw tool name → registry short name
}

func (c StdioConfig) serverName() string { return c.Name }
func (c StdioConfig) isServerConfig()    {}

// HTTPConfig configures an MCP server accessed via HTTP/SSE.
type HTTPConfig struct {
	Name     string
	URL      string
	Headers  map[string]string // ${ENV_VAR} interpolation done by loader
	Auth     Auth              // pluggable; nil = no auth
	Timeout  time.Duration     // per-request; default 30s if zero
	Disabled bool
	Filter   *ToolFilter
	Aliases  map[string]string
}

func (c HTTPConfig) serverName() string { return c.Name }
func (c HTTPConfig) isServerConfig()    {}

// ToolFilter restricts which tools are exposed from a server.
// Include and Exclude are glob patterns matched against the raw tool name
// (before alias). Mutually exclusive: setting both causes a registration error.
type ToolFilter struct {
	Include []string
	Exclude []string
}
