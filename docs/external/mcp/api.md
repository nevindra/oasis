# mcp API reference

Import: `github.com/nevindra/oasis/mcp`

---

## Server

### `New`

```go
func New(name, version string, opts ...ServerOption) *Server
```

Creates an MCP server that reads from `os.Stdin` and writes to `os.Stdout`. `name` and `version` are reported to MCP clients in the `initialize` response. Register tools and resources before calling `Serve`.

### `ServerOption`

```go
type ServerOption func(*Server)
```

Functional option for `New`. Use the constructor functions below rather than constructing this type directly.

### `WithServerLogger`

```go
func WithServerLogger(l *slog.Logger) ServerOption
```

Sets the logger used by the server for internal diagnostics (failed marshals, write errors). Default: `slog.Default()`.

Note: use `WithLogger` (a `RegistryOption`) to set the logger on a `Registry`; `WithServerLogger` is specific to `Server`.

### `(*Server).AddTool`

```go
func (s *Server) AddTool(h ToolHandler)
```

Registers a tool handler. Must be called before `Serve`. Subsequent calls to `AddTool` after `Serve` has started have no effect.

### `(*Server).AddResource`

```go
func (s *Server) AddResource(r Resource)
```

Registers a readable resource. Must be called before `Serve`. Resources appear in `resources/list` responses and are readable via `resources/read`.

### `(*Server).Serve`

```go
func (s *Server) Serve(ctx context.Context) error
```

Blocks, scanning stdin for newline-delimited JSON-RPC messages and dispatching them to registered handlers. Returns when stdin is closed, an unrecoverable read error occurs, or `ctx` is cancelled. Returns `nil` on clean EOF; returns `ctx.Err()` on context cancellation; wraps scan errors with `"mcp: read stdin: ..."`.

---

## Server types

### `ToolHandler`

```go
type ToolHandler struct {
    Definition ToolDefinition
    Execute    func(ctx context.Context, args json.RawMessage) ToolCallResult
}
```

Associates a tool definition with its execution function. `Execute` is called on each `tools/call` request for this tool. `args` is the raw JSON arguments object sent by the client; unmarshal it into a typed struct inside `Execute`. Return `TextResult` on success or `ErrorResult` on failure.

### `Resource`

```go
type Resource struct {
    URI         string
    Name        string
    Description string
    MimeType    string
    Read        func() string
}
```

A readable data source. `Read` is called on each `resources/read` request and must return the current content as a string. `MimeType` is passed through verbatim (e.g. `"text/plain"`, `"application/json"`).

### `ToolDefinition`

```go
type ToolDefinition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    InputSchema json.RawMessage `json:"inputSchema"`
}
```

Describes a tool in `tools/list` responses. `InputSchema` is the tool's JSON Schema as pre-serialized JSON bytes (`json.RawMessage`). Use a raw JSON string literal (e.g. `` json.RawMessage(`{...}`) ``) or `json.Marshal` a typed struct to produce the value. `json.RawMessage` avoids double-encoding that occurred when `any` was used with `map[string]any`.

### `ToolCallResult`

```go
type ToolCallResult struct {
    Content []ContentBlock
    IsError bool
}
```

The value returned by a `ToolHandler.Execute` function. Use `TextResult` or `ErrorResult` constructors — do not construct this type by hand.

### `ContentBlock`

```go
type ContentBlock struct {
    Type string
    Text string
}
```

A single content item inside a `ToolCallResult`. `Type` is always `"text"` for server-side results. Use `TextResult` and `ErrorResult` to construct these.

### `TextResult`

```go
func TextResult(text string) ToolCallResult
```

Returns a successful `ToolCallResult` containing a single text content block.

### `ErrorResult`

```go
func ErrorResult(text string) ToolCallResult
```

Returns an error `ToolCallResult` (`IsError: true`) containing a single text content block. MCP clients display these as tool errors.

---

## Client

### `Client` interface

```go
type Client interface {
    Initialize(ctx context.Context) (*InitializeResult, error)
    ListTools(ctx context.Context) (*ListToolsResult, error)
    CallTool(ctx context.Context, name string, args json.RawMessage) (*CallToolResult, error)
    Close(ctx context.Context) error
    OnDisconnect(fn func(error))
}
```

Transport-agnostic interface to an MCP server. The Registry works against this interface; you can implement it for testing or in-process MCP servers. `name` in `CallTool` is the raw server-side tool name (without the `mcp__server__` prefix added by the Registry).

### `NewStdioClient`

```go
func NewStdioClient(cmd *exec.Cmd) (*StdioClient, error)
```

Spawns the command as a child process and connects to its stdin/stdout. Returns an error if the process cannot be started. The process is killed when `Close` is called (after a graceful-exit window determined by the `ctx` deadline passed to `Close`).

### `NewStdioClientFromPipes`

```go
func NewStdioClientFromPipes(r io.ReadCloser, w io.WriteCloser) *StdioClient
```

Constructs a `StdioClient` from existing pipes instead of spawning a process. Useful for testing with in-process MCP servers or for pre-opened subprocess pipes.

### `NewHTTPClient`

```go
func NewHTTPClient(url string, extraHeaders map[string]string, auth Auth, timeout time.Duration) *HTTPClient
```

Constructs an HTTP-transport MCP client. `extraHeaders` are added to every request before authentication. `auth` may be `nil` for unauthenticated endpoints. `timeout` is per-request; pass `0` for no timeout (default applied by the Registry is 30 s).

### `Auth` interface

```go
type Auth interface {
    Apply(req *http.Request) error
}
```

Pluggable authentication for `HTTPClient`. Called before every request. Implementations: `BearerAuth`.

### `BearerAuth`

```go
type BearerAuth struct {
    Token  string // literal token; never logged but kept in memory
    EnvVar string // read token from this environment variable at call time
}
```

Sets `Authorization: Bearer <token>` on each request. When both `Token` and `EnvVar` are set, `Token` wins. Using `EnvVar` is preferred — the token is read from the environment on each request rather than stored as a plain string in your source.

---

## Registry

### `NewRegistry`

```go
func NewRegistry(opts ...RegistryOption) *Registry
```

Constructs a fresh Registry. Multiple agents can share one Registry by passing the same `*Registry` to each agent's `WithTools(reg.Tools()...)` at construction. The Registry's internal logger defaults to `slog.Default()`.

### `RegistryOption`

```go
type RegistryOption func(*Registry)
```

Functional option for `NewRegistry`.

### `WithLogger` (Registry)

```go
func WithLogger(l *slog.Logger) RegistryOption
```

Sets the logger for registry warnings (registration failures, tool name collisions, reconnect attempts). Pass `nil` to disable logging.

### `WithLifecycleHandler`

```go
func WithLifecycleHandler(h LifecycleHandler) RegistryOption
```

Installs a handler that receives connect/disconnect/tool-call/tool-result events for every registered server. Pass `nil` to reset to no-op.

### `WithDeferredSchemas`

```go
func WithDeferredSchemas(opts ...DeferOption) RegistryOption
```

Opts the Registry into deferred schema loading. MCP tools are advertised to the LLM by name and description only; input schemas are loaded on-demand via the auto-included `ToolSearch` tool. See the [deferred schemas section in index.md](./index.md#deferred-schema-loading) for the cost/benefit analysis.

When enabled, prepend `DeferredToolsPromptSection()` to the agent's system prompt so the model knows how to use `ToolSearch`.

### `WithProgressEvents`

```go
func WithProgressEvents() RegistryOption
```

Enables opt-in tool-call progress reporting. When set, `CallTool` encodes a `progressToken` in each request and the Registry emits `EventProgress` events as the server sends `notifications/progress`. Off by default — the tool-dispatch hot path is unchanged unless this option is set. Progress notifications only arrive over stdio transports; HTTP servers that return progress via polling are not affected by this option.

### `(*Registry).Register`

```go
func (r *Registry) Register(ctx context.Context, cfg ServerConfig) error
```

Connects to an MCP server, runs the initialize handshake, fetches the tool list, and adds each tool to the registry. Blocks until initialization succeeds or times out (10 s). Returns an error if the server is unreachable, if `cfg.Name` is empty, or if a server with the same name is already registered.

### `(*Registry).Unregister`

```go
func (r *Registry) Unregister(ctx context.Context, name string) error
```

Disconnects a server and removes all of its tools from the registry.

### `(*Registry).Reload`

```go
func (r *Registry) Reload(ctx context.Context, name string, cfg ServerConfig) error
```

Replaces a server's config by calling `Unregister` then `Register`. Use this to change a server's transport or arguments at runtime.

### `(*Registry).Reconnect`

```go
func (r *Registry) Reconnect(ctx context.Context, name string) error
```

Manually triggers a reconnect attempt on a server that has reached the `StateDead` state (all automatic retry attempts exhausted).

### `(*Registry).Tools`

```go
func (r *Registry) Tools() []oasis.AnyTool
```

Returns a snapshot of all registered tools. When deferred-schema mode is enabled, the snapshot also includes the auto-managed `ToolSearch` tool. The returned slice is decoupled from internal state — safe to retain or pass to multiple agents.

For static tool sets (all servers registered before agent construction), pass the snapshot directly:

```go
agent := oasis.NewAgent("a", "desc", provider, oasis.WithTools(reg.Tools()...))
```

### `(*Registry).GetTool`

```go
func (r *Registry) GetTool(server, tool string) (oasis.AnyTool, bool)
```

Returns the wrapped `oasis.AnyTool` for a specific server and short tool name (before `mcp__` namespacing). Returns `false` if the server or tool is not registered.

### `(*Registry).List`

```go
func (r *Registry) List() []ServerStatus
```

Returns a snapshot of all registered servers and their current state. Useful for health checks and observability dashboards.

### `(*Registry).Subscribe`

```go
func (r *Registry) Subscribe() <-chan Event
```

Returns a buffered channel (capacity 64) of lifecycle events. When the channel is full, the oldest event is dropped. Read from this channel in a dedicated goroutine to observe connect/disconnect/tool-call/tool-result events.

---

## Client primitives

The Registry exposes flat methods for consuming MCP server capabilities beyond tools. All methods resolve the server entry fresh on each call and are safe for concurrent use alongside in-flight tool calls.

### Capability matrix

| Method | Stdio | HTTP | Notes |
|---|---|---|---|
| `ListResources` | yes | yes | request/response only |
| `ReadResource` | yes | yes | request/response only |
| `SubscribeResource` | yes | **no** | requires persistent read loop |
| `UnsubscribeResource` | yes | **no** | requires persistent read loop |
| `ListPrompts` | yes | yes | request/response only |
| `GetPrompt` | yes | yes | request/response only |
| `SetLogLevel` | yes | yes | log events arrive only over stdio |
| Progress notifications | yes | **no** | requires persistent read loop |
| Roots (`StdioConfig.Roots`) | yes | **no** | answers `roots/list` from server |

Calling a method marked **no** over an HTTP transport returns `ErrUnsupported`. Calling any method for an unknown server returns `ErrServerNotFound`.

### Error sentinels

```go
var ErrUnsupported  = errors.New("mcp: capability not supported by server or transport")
var ErrServerNotFound = errors.New("MCP server not found")
```

`ErrUnsupported` is returned when the server did not advertise the capability during `initialize`, or when the transport cannot support it (subscribe/progress/roots over HTTP). `ErrServerNotFound` is returned for all capability methods when the server name is not registered. Both wrap cleanly with `errors.Is`.

### `(*Registry).ListResources`

```go
func (r *Registry) ListResources(ctx context.Context, server string) ([]ResourceInfo, error)
```

Returns the resources advertised by the named MCP server (`resources/list`). Concurrent-safe; does not hold the tool-call mutex.

```go
resources, err := reg.ListResources(ctx, "fs")
if errors.Is(err, mcp.ErrUnsupported) {
    // server did not advertise resources capability
}
for _, res := range resources {
    fmt.Println(res.URI, res.Name, res.MimeType)
}
```

### `(*Registry).ReadResource`

```go
func (r *Registry) ReadResource(ctx context.Context, server, uri string) ([]ResourceContent, error)
```

Reads a resource by URI (`resources/read`). Returns one or more `ResourceContent` items; exactly one of `Text` or `Blob` is populated per item.

```go
contents, err := reg.ReadResource(ctx, "fs", "file:///project/README.md")
for _, c := range contents {
    fmt.Println(c.Text) // or base64-decode c.Blob for binary
}
```

### `(*Registry).SubscribeResource`

```go
func (r *Registry) SubscribeResource(ctx context.Context, server, uri string) error
```

Subscribes to change notifications for a resource URI. When the server pushes `notifications/resources/updated`, the Registry emits `EventResourceUpdated` on the `Subscribe()` channel with `Event.URI` set. **Stdio only** — returns `ErrUnsupported` over HTTP.

### `(*Registry).UnsubscribeResource`

```go
func (r *Registry) UnsubscribeResource(ctx context.Context, server, uri string) error
```

Cancels a resource subscription. **Stdio only.**

### `(*Registry).ListPrompts`

```go
func (r *Registry) ListPrompts(ctx context.Context, server string) ([]Prompt, error)
```

Returns the prompt templates advertised by the named server (`prompts/list`).

```go
prompts, err := reg.ListPrompts(ctx, "docs")
for _, p := range prompts {
    fmt.Println(p.Name, p.Description)
    for _, arg := range p.Arguments {
        fmt.Printf("  arg: %s (required=%v)\n", arg.Name, arg.Required)
    }
}
```

### `(*Registry).GetPrompt`

```go
func (r *Registry) GetPrompt(ctx context.Context, server, name string, args map[string]string) (*PromptResult, error)
```

Fetches a rendered prompt by name with string-valued arguments (`prompts/get`).

```go
result, err := reg.GetPrompt(ctx, "docs", "summarize", map[string]string{"tone": "concise"})
for _, msg := range result.Messages {
    fmt.Println(msg.Role, msg.Content.Text)
}
```

### `(*Registry).SetLogLevel`

```go
func (r *Registry) SetLogLevel(ctx context.Context, server string, level LogLevel) error
```

Asks the server to emit log messages at or above `level` (`logging/setLevel`). The request/response works over both transports. Log messages arrive only over stdio as `EventLog` events on `Subscribe()`.

```go
_ = reg.SetLogLevel(ctx, "myserver", mcp.LogLevelWarning)

// in a separate goroutine:
for e := range reg.Subscribe() {
    if e.Type == mcp.EventLog {
        fmt.Printf("[%s] %s: %s\n", e.Server, e.Level, e.Message)
    }
}
```

---

## Client primitive types

### `ResourceInfo`

```go
type ResourceInfo struct {
    URI         string
    Name        string
    Description string
    MimeType    string
}
```

A resource advertised by an MCP server (`resources/list` entry).

### `ResourceContent`

```go
type ResourceContent struct {
    URI      string
    MimeType string
    Text     string // populated for text resources
    Blob     string // base64-encoded; populated for binary resources
}
```

One content item returned by `ReadResource`. Exactly one of `Text` or `Blob` is populated.

### `Prompt`

```go
type Prompt struct {
    Name        string
    Description string
    Arguments   []PromptArgument
}
```

A prompt template advertised by an MCP server (`prompts/list` entry).

### `PromptArgument`

```go
type PromptArgument struct {
    Name        string
    Description string
    Required    bool
}
```

A single argument accepted by a prompt template.

### `PromptResult`

```go
type PromptResult struct {
    Description string
    Messages    []PromptMessage
}
```

The result of `GetPrompt`.

### `PromptMessage`

```go
type PromptMessage struct {
    Role    string // "user" or "assistant"
    Content ContentBlock
}
```

One message in a prompt result.

### `LogLevel`

```go
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
```

RFC 5424 syslog severity levels. Pass to `SetLogLevel`; received on `Event.Level` for `EventLog` events.

### `Root`

```go
type Root struct {
    URI  string // must be a file:// URI
    Name string
}
```

A filesystem boundary advertised to an MCP server via `StdioConfig.Roots`. The Registry announces the roots capability during `initialize` and returns the configured list in response to server `roots/list` requests.

---

## ServerConfig types

### `StdioConfig`

```go
type StdioConfig struct {
    Name     string
    Command  string
    Args     []string
    Env      map[string]string // merged with os.Environ() at spawn time
    WorkDir  string            // default: current working directory
    Disabled bool
    Filter   *ToolFilter
    Aliases  map[string]string // raw tool name → registry short name
    Roots    []Root            // filesystem roots; stdio only
}
```

Configuration for an MCP server launched as a child process. `Env` entries are appended to the current environment. `Disabled: true` causes `Register` to silently skip the server. `Aliases` maps the server's raw tool names to shorter names in the registry. `Roots`, when non-empty, advertises the roots capability during `initialize` and answers server-initiated `roots/list` requests. This field is ignored on `HTTPConfig` — roots require a persistent read loop.

### `HTTPConfig`

```go
type HTTPConfig struct {
    Name     string
    URL      string
    Headers  map[string]string
    Auth     Auth
    Timeout  time.Duration // per-request; default 30s if zero
    Disabled bool
    Filter   *ToolFilter
    Aliases  map[string]string
}
```

Configuration for an MCP server accessed via HTTP. `Auth` applies after headers on each request.

### `ToolFilter`

```go
type ToolFilter struct {
    Include []string // glob patterns; if non-empty, only matching tools are registered
    Exclude []string // glob patterns; matching tools are skipped
}
```

Restricts which tools from a server are registered. Patterns follow `filepath.Match` semantics. `Include` and `Exclude` are mutually exclusive — setting both causes `Register` to return an error.

---

## Lifecycle types

### `LifecycleHandler`

```go
type LifecycleHandler interface {
    OnConnect(name string, info ServerMetadata)
    OnDisconnect(name string, err error)
    OnToolCall(name, tool string, args json.RawMessage)
    OnToolResult(name, tool string, result *CallToolResult, err error)
}
```

Receives lifecycle notifications from MCP servers registered with the Registry. Panics inside handlers are recovered by the Registry.

### `NoopLifecycle`

```go
type NoopLifecycle struct{}
```

A no-op `LifecycleHandler`. Embed it in your own struct to provide partial implementations:

```go
type MyHandler struct{ mcp.NoopLifecycle }
func (h MyHandler) OnConnect(name string, info mcp.ServerMetadata) {
    log.Printf("connected: %s", name)
}
```

### `Event`

```go
type Event struct {
    Type      EventType
    Server    string
    Tool      string    // populated for tool-related events
    Err       error
    Timestamp time.Time

    URI      string   // EventResourceUpdated: the resource URI that changed
    Progress float64  // EventProgress: fraction complete (0–1)
    Total    float64  // EventProgress: total units (0 = unknown)
    Level    LogLevel // EventLog: the severity level reported by the server
    Message  string   // EventProgress / EventLog: human-readable message
}
```

A single lifecycle event emitted by the Registry. Read from `reg.Subscribe()`. Fields below `Timestamp` are zero-valued for event types that do not use them.

### `EventType`

```go
type EventType int

const (
    EventConnected          EventType = iota // server connected or reconnected
    EventDisconnected                        // server disconnected
    EventReconnecting                        // reconnect loop started
    EventToolCall                            // tool invocation dispatched to server
    EventToolResult                          // tool invocation returned
    EventProgress                            // tool-call progress (opt-in via WithProgressEvents)
    EventLog                                 // server logging/message (after SetLogLevel)
    EventResourceUpdated                     // a subscribed resource changed (stdio only)
    EventResourceListChanged                 // server's resource list changed (stdio only)
    EventPromptListChanged                   // server's prompt list changed (stdio only)
)
```

---

## Deferred schema types

### `DeferOption`

```go
type DeferOption func(*deferConfig)
```

Configures `WithDeferredSchemas`. Combine multiple options in one call.

### `DeferAlwaysOn`

```go
func DeferAlwaysOn() DeferOption
```

Forces all MCP tool schemas to be deferred regardless of any threshold setting.

### `DeferExclude`

```go
func DeferExclude(serverNames ...string) DeferOption
```

Keeps the named MCP servers' schemas eager (never deferred). Use this to exclude servers whose tools are called unconditionally or frequently, so their schemas are always available without a `ToolSearch` round-trip.

### `DeferThreshold`

```go
func DeferThreshold(percent int) DeferOption
```

Reserved for v1.x. Accepted but has no effect — deferred mode is always-on when enabled.

### `DeferredToolsPromptSection`

```go
func DeferredToolsPromptSection() string
```

Returns the system-prompt block that explains the `mcp__` deferral mechanism to the LLM. Prepend this to the agent's system prompt when using `WithDeferredSchemas`:

```go
prompt := mcp.DeferredToolsPromptSection() + "\n\n" + userPrompt
agent := oasis.NewAgent("a", "desc", provider,
    oasis.WithPrompt(prompt),
    oasis.WithTools(reg.Tools()...),
)
```

### `ToolSearchName`

```go
const ToolSearchName = "ToolSearch"
```

The name of the schema-fetching tool auto-injected by the Registry when deferred schemas are enabled. Use this constant if you need to check for or exclude the `ToolSearch` tool by name.

---

## Server state types

### `ServerState`

```go
type ServerState int

const (
    StateConnecting   ServerState = iota // initial connect in progress
    StateHealthy                         // connected and operational
    StateReconnecting                    // reconnect loop running
    StateDead                            // all reconnect attempts exhausted
)
```

### `ServerStatus`

```go
type ServerStatus struct {
    Name        string
    Transport   string      // "stdio" or "http"
    State       ServerState
    ToolCount   int
    LastError   error
    ConnectedAt time.Time
    Server      ServerMetadata
}
```

Returned by `(*Registry).List()`. A snapshot of a single server's runtime state.

### `ServerMetadata`

```go
type ServerMetadata struct {
    Name            string
    Version         string
    ProtocolVersion string
    Capabilities    ServerCapabilities
}
```

Metadata reported by the MCP server during initialization.

### `ServerCapabilities`

```go
type ServerCapabilities struct {
    Tools     *CapabilityFlag `json:"tools,omitempty"`
    Resources *CapabilityFlag `json:"resources,omitempty"`
    Prompts   *CapabilityFlag `json:"prompts,omitempty"`
    Logging   *CapabilityFlag `json:"logging,omitempty"`
}
```

Optional features advertised by an MCP server in the `initialize` response. A non-nil pointer means the server supports that feature.

### `CapabilityFlag`

```go
type CapabilityFlag struct {
    ListChanged bool `json:"listChanged,omitempty"`
}
```

Per-feature object inside `ServerCapabilities`. `ListChanged` is `true` when the server emits `list-changed` notifications.

---

## Wire types (advanced)

These types are used by the `Client` interface and returned from client methods. You rarely construct them directly.

### `InitializeResult`

```go
type InitializeResult struct {
    ProtocolVersion string
    Capabilities    ServerCapabilities
    ServerInfo      ServerInfo
}
```

Returned by `Client.Initialize`.

### `ServerInfo`

```go
type ServerInfo struct {
    Name    string
    Version string
}
```

The name and version string reported by an MCP server in the `initialize` response.

### `ListToolsResult`

```go
type ListToolsResult struct {
    Tools      []ToolDefinition
    NextCursor string
}
```

Returned by `Client.ListTools`. `NextCursor` is reserved for paginated tool lists.

### `CallToolResult`

```go
type CallToolResult struct {
    Content []ContentBlock
    IsError bool
    Meta    json.RawMessage // _meta field; opaque
}
```

Returned by `Client.CallTool`. The Registry maps this to an `oasis.ToolResult` before handing it back to the agent loop.
