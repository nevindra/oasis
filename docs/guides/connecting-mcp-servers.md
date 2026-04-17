# Connecting MCP Servers

Oasis can consume external [Model Context Protocol](https://modelcontextprotocol.io)
servers, exposing their tools to your agent through the same `ToolRegistry`
used for native tools.

## Quickstart: Local stdio server

Pass a server config at agent construction:

```go
agent := oasis.NewLLMAgent(
    "myagent", "description", provider,
    oasis.WithMCPServer(oasis.StdioMCPConfig{
        Name:    "fs",
        Command: "npx",
        Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/data"},
    }),
)
```

Tools from the server appear with namespaced names: `mcp__fs__read_file`,
`mcp__fs__write_file`, etc.

## HTTP server with bearer auth

```go
oasis.WithMCPServer(oasis.HTTPMCPConfig{
    Name: "github",
    URL:  "https://mcp.github.com/v1",
    Auth: oasis.BearerAuth{EnvVar: "GH_TOKEN"},
})
```

## File-based config

Drop `.oasis/mcp.json` (Claude Desktop compatible schema) anywhere in your project tree:

```json
{
  "version": 1,
  "mcpServers": {
    "fs":     { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/data"] },
    "github": { "url": "https://mcp.github.com/v1", "auth": { "type": "bearer", "envVar": "GH_TOKEN" } }
  }
}
```

Load it and pass the configs to `WithMCPServers`:

```go
import mcpconfig "github.com/nevindra/oasis/mcp/config"

servers, err := mcpconfig.Load(".")
if err != nil {
    log.Fatal(err)
}
agent := oasis.NewLLMAgent(
    "myagent", "description", provider,
    oasis.WithMCPServers(servers...),
)
```

> **Note:** `mcpconfig.Load` walks up the directory tree from the given path to
> find `.oasis/mcp.json`. Use `mcpconfig.LoadFile(path)` to load a specific file.

> **Why not `WithMCPConfigFile`?** The `mcp/config` subpackage imports the root
> `oasis` package to return typed configs. A `WithMCPConfigFile` option in the
> root package would create an import cycle. Instead, load the file yourself with
> `mcpconfig.LoadFile(path)` and pass the result to `WithMCPServers(cfgs...)`.

## Runtime management

After construction, use the `MCPController` returned by `agent.MCP()`:

```go
ctrl := agent.MCP()

// Add a new server at runtime.
if err := ctrl.Register(ctx, oasis.StdioMCPConfig{Name: "linear", Command: "npx", Args: []string{"-y", "@linear/mcp"}}); err != nil {
    log.Printf("register failed: %v", err)
}

// Inspect current state.
for _, s := range ctrl.List() {
    fmt.Printf("server=%s state=%s tools=%d\n", s.Name, s.State, s.ToolCount)
}

// Remove a server and its tools.
ctrl.Unregister(ctx, "github")
```

## Filtering and aliases

Use `MCPToolFilter` to whitelist or blacklist tools by glob pattern, and
`Aliases` to rename individual tools in the registry:

```go
oasis.HTTPMCPConfig{
    Name: "github",
    URL:  "...",
    Filter:  &oasis.MCPToolFilter{Include: []string{"create_*", "list_*"}},
    Aliases: map[string]string{"create_issue": "gh_new_issue"},
}
```

`Include` and `Exclude` are mutually exclusive — setting both causes a
registration error. Aliases apply after filtering; the registered tool name
becomes `mcp__github__gh_new_issue`.

## Sharing across agents

Create a `MCPRegistry` once and share it across agents to reuse connections
(especially important for stdio servers, which spawn child processes):

```go
shared := oasis.NewSharedMCPRegistry()
if err := shared.Register(ctx, cfg); err != nil {
    log.Fatal(err)
}

a1 := oasis.NewLLMAgent("a1", "description", p1, oasis.WithSharedMCPRegistry(shared))
a2 := oasis.NewLLMAgent("a2", "description", p2, oasis.WithSharedMCPRegistry(shared))
```

Both agents see the same MCP tools and reuse the same connections.

## Lifecycle observation

Implement `MCPLifecycleHandler` (embed `NoopMCPLifecycle` for partial
implementations):

```go
type myHandler struct{ oasis.NoopMCPLifecycle }

func (h myHandler) OnConnect(name string, info oasis.MCPServerInfo) {
    log.Printf("connected: %s (%s %s)", name, info.Name, info.Version)
}

agent := oasis.NewLLMAgent(
    "myagent", "description", provider,
    oasis.WithMCPServer(cfg),
    oasis.WithMCPLifecycleHandler(myHandler{}),
)
```

Or subscribe to the event channel for a lightweight stream of typed events:

```go
events := agent.MCP().Subscribe()
for e := range events {
    fmt.Printf("event: %v server=%s\n", e.Type, e.Server)
}
```

The channel is buffered (64 slots). The oldest event is dropped when the buffer
is full. `MCPEvent.Type` is one of:
`MCPEventConnected`, `MCPEventDisconnected`, `MCPEventReconnecting`,
`MCPEventToolCall`, `MCPEventToolResult`.

## Failure handling

- **Startup failures**: a server that fails to connect is logged and skipped — your
  agent continues running without that server (soft-degrade). For hard-fail
  behavior, call `agent.MCP().Register(ctx, cfg)` at runtime and check the
  returned error.
- **Runtime crash**: state transitions to `MCPStateReconnecting`, exponential
  backoff up to ~30 seconds, max 10 attempts, then `MCPStateDead`.
- **Manual recovery**: after a server reaches `Dead`, call
  `agent.MCP().Reconnect(ctx, "name")` to restart the backoff loop.
- **Tool call failure**: returned as `ToolResult.Error` — never a Go panic or error
  propagated to the caller.

## Dynamic type assertion

`MCPAccessor` is the optional capability interface for agents that expose MCP
management. Use it when you hold an `Agent` interface and want to conditionally
access MCP:

```go
if ma, ok := agent.(oasis.MCPAccessor); ok {
    ma.MCP().Register(ctx, cfg)
}
```

Currently implemented by `*LLMAgent` only.
