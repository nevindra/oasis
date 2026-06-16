# mcp examples

Copy-paste recipes. All examples import `github.com/nevindra/oasis/mcp`.

---

## 1. Minimal MCP server (stdio)

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "log"

    "github.com/nevindra/oasis/mcp"
)

func main() {
    srv := mcp.New("my-tools", "1.0.0")

    srv.AddTool(mcp.ToolHandler{
        Definition: mcp.ToolDefinition{
            Name:        "add",
            Description: "Add two integers",
            InputSchema: json.RawMessage(`{
                "type": "object",
                "properties": {
                    "a": {"type": "integer"},
                    "b": {"type": "integer"}
                },
                "required": ["a", "b"]
            }`),
        },
        Execute: func(ctx context.Context, args json.RawMessage) mcp.ToolCallResult {
            var in struct {
                A int `json:"a"`
                B int `json:"b"`
            }
            if err := json.Unmarshal(args, &in); err != nil {
                return mcp.ErrorResult(err.Error())
            }
            return mcp.TextResult(fmt.Sprintf("%d", in.A+in.B))
        },
    })

    if err := srv.Serve(context.Background()); err != nil {
        log.Fatal(err)
    }
}
```

Compile and register the binary as an MCP server in your AI assistant's config. The assistant discovers and calls `add` via JSON-RPC over the process's stdio.

---

## 2. MCP server with `WithServerLogger`

```go
import (
    "log/slog"
    "os"

    "github.com/nevindra/oasis/mcp"
)

logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

srv := mcp.New("my-tools", "1.0.0", mcp.WithServerLogger(logger))
```

The logger receives internal server diagnostics (write failures, marshal errors) on stderr, which does not interfere with the stdio JSON-RPC transport (which uses stdout).

---

## 3. MCP server with a resource

```go
import (
    "context"
    "os"

    "github.com/nevindra/oasis/mcp"
)

srv := mcp.New("docs-server", "1.0.0")

srv.AddResource(mcp.Resource{
    URI:         "docs://readme",
    Name:        "README",
    Description: "Project README",
    MimeType:    "text/markdown",
    Read: func() string {
        content, _ := os.ReadFile("README.md")
        return string(content)
    },
})

srv.Serve(context.Background())
```

`Read` is called on each `resources/read` request. Use it to serve dynamic content; the function is not cached by the server.

---

## 4. Stdio client (connect to a subprocess MCP server)

```go
import (
    "context"
    "encoding/json"
    "fmt"
    "log"
    "os"
    "os/exec"

    "github.com/nevindra/oasis/mcp"
)

cmd := exec.Command("npx", "-y", "@modelcontextprotocol/server-github")
cmd.Env = append(os.Environ(), "GITHUB_PERSONAL_ACCESS_TOKEN="+os.Getenv("GITHUB_TOKEN"))

client, err := mcp.NewStdioClient(cmd)
if err != nil {
    log.Fatal(err)
}
defer client.Close(context.Background())

// Run the initialize handshake.
info, err := client.Initialize(ctx)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("connected to %s v%s\n", info.ServerInfo.Name, info.ServerInfo.Version)

// List available tools.
tools, err := client.ListTools(ctx)
for _, t := range tools.Tools {
    fmt.Println(t.Name, "-", t.Description)
}

// Call a tool.
result, err := client.CallTool(ctx, "list_issues", json.RawMessage(`{"owner":"my-org","repo":"my-repo"}`))
```

`CallTool` takes the server-side raw tool name (no `mcp__` prefix). The Registry handles namespacing; direct client calls do not.

---

## 5. HTTP client with bearer auth

```go
import (
    "time"

    "github.com/nevindra/oasis/mcp"
)

client := mcp.NewHTTPClient(
    "https://mcp.example.com/rpc",
    map[string]string{"X-App-ID": "my-agent"},
    mcp.BearerAuth{EnvVar: "MCP_API_TOKEN"}, // reads from env at call time
    30*time.Second,
)
defer client.Close(context.Background())

info, err := client.Initialize(ctx)
```

`BearerAuth.EnvVar` reads the token from the environment on each request. Prefer this over `BearerAuth.Token` (literal) so the secret is not embedded in your source.

---

## 6. Registry with multiple servers

```go
import (
    "context"
    "fmt"
    "log"
    "os"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/mcp"
)

ctx := context.Background()

reg := mcp.NewRegistry()

// Register a stdio server (GitHub MCP).
if err := reg.Register(ctx, mcp.StdioConfig{
    Name:    "github",
    Command: "npx",
    Args:    []string{"-y", "@modelcontextprotocol/server-github"},
    Env:     map[string]string{"GITHUB_PERSONAL_ACCESS_TOKEN": os.Getenv("GITHUB_TOKEN")},
}); err != nil {
    log.Fatal(err)
}

// Register an HTTP server.
if err := reg.Register(ctx, mcp.HTTPConfig{
    Name: "search",
    URL:  "https://mcp.tavily.com/rpc",
    Auth: mcp.BearerAuth{EnvVar: "TAVILY_API_KEY"},
}); err != nil {
    log.Fatal(err)
}

// Wire all MCP tools to the agent.
a := agent.New(provider,
    agent.WithTools(reg.Tools()...),
)

result, _ := a.Execute(ctx, oasis.AgentTask{Input: "List open issues in my-org/my-repo"})
fmt.Println(result.Output)
```

All tools from both servers are available to the agent under their namespaced names (`mcp__github__list_issues`, `mcp__search__search`, etc.).

---

## 7. Deferred schemas with system prompt injection

```go
import (
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/mcp"
)

reg := mcp.NewRegistry(
    mcp.WithDeferredSchemas(
        mcp.DeferAlwaysOn(),
        mcp.DeferExclude("github"), // github tools always have full schemas
    ),
)

// Register many MCP servers...
reg.Register(ctx, mcp.StdioConfig{Name: "github", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"}})
reg.Register(ctx, mcp.HTTPConfig{Name: "search", URL: "https://mcp.tavily.com/rpc"})

// Inject the deferred-tools prompt section so the model knows to call ToolSearch.
systemPrompt := mcp.DeferredToolsPromptSection() + "\n\n" +
    "You are a research assistant with access to many tools."

a := agent.New(provider,
    agent.WithPrompt(systemPrompt),
    agent.WithTools(reg.Tools()...),
)
```

When the LLM encounters a deferred `mcp__search__*` tool, it calls `ToolSearch(query="...")` first to fetch the schema, then calls the actual tool. The `github` server is excluded from deferral so its schemas are always available. The `ToolSearch` tool is automatically included in `reg.Tools()` when deferred mode is on.

---

## 8. Client primitives — resources, prompts, logging, progress, and roots

This recipe wires all five new client capabilities in one place: register a stdio server with roots, list and read a resource, subscribe to resource change notifications, fetch a prompt, and enable opt-in progress events.

```go
package main

import (
    "context"
    "errors"
    "fmt"
    "log"

    "github.com/nevindra/oasis/mcp"
)

func main() {
    ctx := context.Background()

    // Build the registry with opt-in progress events.
    reg := mcp.NewRegistry(
        mcp.WithProgressEvents(),
    )

    // Register a stdio server and advertise two filesystem roots.
    // Roots are answered automatically when the server sends roots/list.
    if err := reg.Register(ctx, mcp.StdioConfig{
        Name:    "fs",
        Command: "my-mcp-server",
        Roots: []mcp.Root{
            {URI: "file:///home/user/project", Name: "project"},
            {URI: "file:///home/user/data", Name: "data"},
        },
    }); err != nil {
        log.Fatal(err)
    }

    // List the server's resources.
    resources, err := reg.ListResources(ctx, "fs")
    if errors.Is(err, mcp.ErrUnsupported) {
        log.Fatal("server did not advertise resources capability")
    }
    if err != nil {
        log.Fatal(err)
    }
    for _, r := range resources {
        fmt.Printf("resource: %s (%s)\n", r.URI, r.MimeType)
    }

    // Read a specific resource by URI.
    if len(resources) > 0 {
        contents, err := reg.ReadResource(ctx, "fs", resources[0].URI)
        if err != nil {
            log.Fatal(err)
        }
        for _, c := range contents {
            fmt.Printf("content: %s\n", c.Text)
        }
    }

    // Subscribe to change notifications for a resource URI.
    // Updates arrive as EventResourceUpdated on the Subscribe() channel.
    // This is stdio-only; returns ErrUnsupported over HTTP.
    if err := reg.SubscribeResource(ctx, "fs", "file:///home/user/project/config.json"); err != nil && !errors.Is(err, mcp.ErrUnsupported) {
        log.Fatal(err)
    }

    // Watch for resource updates, progress, and log events.
    events := reg.Subscribe()
    go func() {
        for e := range events {
            switch e.Type {
            case mcp.EventResourceUpdated:
                fmt.Printf("resource changed: %s on %s\n", e.URI, e.Server)
            case mcp.EventProgress:
                fmt.Printf("progress on %s/%s: %.0f%%\n", e.Server, e.Tool, e.Progress*100)
            case mcp.EventLog:
                fmt.Printf("[%s] %s: %s\n", e.Server, e.Level, e.Message)
            }
        }
    }()

    // Ask the server to emit warning-level and above log messages.
    // Log events arrive on Subscribe() as EventLog. Works over both
    // transports; over HTTP the setLevel request succeeds but log
    // notifications only arrive if the server also has a stdio channel.
    if err := reg.SetLogLevel(ctx, "fs", mcp.LogLevelWarning); err != nil && !errors.Is(err, mcp.ErrUnsupported) {
        log.Fatal(err)
    }

    // List and fetch a prompt template.
    prompts, err := reg.ListPrompts(ctx, "fs")
    if err != nil && !errors.Is(err, mcp.ErrUnsupported) {
        log.Fatal(err)
    }
    if len(prompts) > 0 {
        p := prompts[0]
        fmt.Printf("prompt: %s — %s\n", p.Name, p.Description)

        // Build the args map from the prompt's declared arguments.
        args := make(map[string]string)
        for _, arg := range p.Arguments {
            if arg.Required {
                args[arg.Name] = "example value"
            }
        }

        result, err := reg.GetPrompt(ctx, "fs", p.Name, args)
        if err != nil {
            log.Fatal(err)
        }
        fmt.Printf("prompt description: %s\n", result.Description)
        for _, msg := range result.Messages {
            fmt.Printf("  [%s] %s\n", msg.Role, msg.Content.Text)
        }
    }

    // The agent still uses reg.Tools() for tool calls, which now also
    // carries progress tokens when WithProgressEvents() is set.
    fmt.Println("tools:", len(reg.Tools()))
}
```

**Key points:**

- `WithProgressEvents()` is set on the registry, not per-server. All stdio servers automatically inject progress tokens from that point on.
- `SubscribeResource`, roots, and progress notifications are **stdio-only**. Calling them on an HTTP-registered server returns `mcp.ErrUnsupported`.
- The `Subscribe()` channel is buffered (capacity 64) and shared across all event types. A single goroutine can handle `EventResourceUpdated`, `EventProgress`, `EventLog`, and the existing lifecycle events (`EventConnected`, `EventToolCall`, etc.) with one `switch`.
- `SetLogLevel` takes effect immediately on the server. The `LogLevel` constants follow RFC 5424 severity: `LogLevelDebug` through `LogLevelEmergency`.

---

## See also

- [mcp concept](index.md) — when to use server vs client vs registry
- [mcp API reference](api.md) — full type and function reference
