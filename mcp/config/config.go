package mcpconfig

import "encoding/json"

// fileSchema is the on-disk JSON shape (Claude Desktop compatible).
type fileSchema struct {
	Version    int                        `json:"version"`
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// rawServer is the inner per-server shape, capturing all known fields.
// Discriminator: presence of "command" → stdio; presence of "url" → http.
type rawServer struct {
	// Stdio-specific
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"workDir,omitempty"`

	// HTTP-specific
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Auth    *rawAuth          `json:"auth,omitempty"`
	Timeout string            `json:"timeout,omitempty"` // parsed as time.Duration

	// Common
	Disabled bool              `json:"disabled,omitempty"`
	Filter   *rawFilter        `json:"filter,omitempty"`
	Aliases  map[string]string `json:"aliases,omitempty"`
}

type rawAuth struct {
	Type   string `json:"type"`            // "bearer" only in v1
	Token  string `json:"token,omitempty"`
	EnvVar string `json:"envVar,omitempty"`
}

type rawFilter struct {
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}
