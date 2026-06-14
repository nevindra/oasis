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
            InputSchema: map[string]any{
                "type": "object",
                "properties": map[string]any{
                    "a": map[string]any{"type": "integer"},
                    "b": map[string]any{"type": "integer"},
                },
                "required": []string{"a", "b"},
            },
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

## See also

- [mcp concept](index.md) — when to use server vs client vs registry
- [mcp API reference](api.md) — full type and function reference
