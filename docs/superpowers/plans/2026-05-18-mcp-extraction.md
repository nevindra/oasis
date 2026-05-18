# MCP Module Extraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract MCP (Model Context Protocol) Registry + tool wrapper from the root `oasis` package into its own Go module `github.com/nevindra/oasis/mcp`, removing all MCP-specific options, fields, and accessors from kernel code. After this plan lands, `oasis` core has zero MCP references; the user wires MCP at the app layer via `mcp.NewRegistry(...)` + `oasis.WithTools(reg.Tools()...)`.

**Architecture:**
- Re-use the existing `mcp/` subdirectory (currently hosting the wire-level `mcp.Client` protocol). Add a `mcp/go.mod`, merge the root-level `mcp_*.go` files into `package mcp` with the `MCP` prefix dropped (since the package name now provides the namespace).
- The new `*mcp.Registry` owns its own internal `[]oasis.AnyTool` list instead of writing through to a `*oasis.ToolRegistry`. Users hand the list to `oasis.WithTools(reg.Tools()...)` themselves — no more auto-magic core wiring.
- Deferred-schema mode becomes a registry option (`mcp.WithDeferredSchemas(...)`); the prompt fragment is exposed as a public function (`mcp.DeferredToolsPromptSection() string`) the user prepends to their own prompt; the `ToolSearch` tool is auto-included in `reg.Tools()` when deferred mode is on.
- `LLMAgent.MCP()` accessor, `WithMCPServer`/`WithMCPServers`/`WithSharedMCPRegistry`/`WithMCPLifecycleHandler`/`WithDeferredSchemas` options, and all `mcp*` fields on `agentConfig`/`agentCore` are deleted from core.
- Single atomic source-move commit keeps the build green between commits (AI-native execution per spec §4.4 — no `LegacyMCPRegistry`-style bridge).

**Tech Stack:** Go 1.26.1, Go workspaces (`go.work`) for local dev, JSON/JSON-RPC over stdio + HTTP for the wire protocol, existing `mcp/mcptest` test fixture, `scripts/check-module-deps.sh` for CI enforcement.

**Pattern reference:** The `ratelimit` extraction (commits `b41fb3d` → `606b41a`) is the template — same shape but smaller surface. We follow the same commit order: scaffold → move impl → move tests → DX artifacts → CI guard → CHANGELOG.

---

## Type rename mapping (canonical reference)

These renames apply uniformly across **every** moved file and every importer. Memorize this table — every task below assumes it.

| Old name (root `oasis`)        | New name (`mcp` package)         | Notes |
|---|---|---|
| `MCPRegistry`                  | `Registry`                       | Exported. The central type. |
| `MCPController`                | **DELETED**                      | Per spec — replaced by direct `*Registry` usage. |
| `NewSharedMCPRegistry()`       | **DELETED**                      | Replaced by `NewRegistry(opts...)` (sharing happens via shared `*Registry` pointer). |
| `MCPServerConfig`              | `ServerConfig`                   | Sealed interface; same role. |
| `StdioMCPConfig`               | `StdioConfig`                    | Struct. |
| `HTTPMCPConfig`                | `HTTPConfig`                     | Struct. |
| `MCPToolFilter`                | `ToolFilter`                     | Struct. |
| `MCPServerStatus`              | `ServerStatus`                   | Struct. |
| `MCPServerState` + constants   | `ServerState` + constants        | `MCPStateConnecting` → `StateConnecting`, etc. |
| `MCPServerInfo`                | `ServerMetadata`                 | Renamed (NOT `ServerInfo`) to avoid collision with the existing `mcp.ServerInfo` wire type in `mcp/protocol.go`. |
| `MCPToolResult`                | **DELETED**                      | Was a redundant public mirror of `mcp.CallToolResult`. Use `*CallToolResult` directly in `LifecycleHandler.OnToolResult`. |
| `MCPContent`                   | **DELETED**                      | Was a redundant mirror of `mcp.ContentBlock`. Use `ContentBlock` directly. |
| `MCPLifecycleHandler`          | `LifecycleHandler`               | Interface. |
| `NoopMCPLifecycle`             | `NoopLifecycle`                  | Embeddable noop. |
| `MCPEvent`                     | `Event`                          | Struct. |
| `MCPEventType` + constants     | `EventType` + constants          | `MCPEventConnected` → `EventConnected`, etc. |
| `MCPAccessor`                  | **DELETED**                      | Per spec — no kernel-side MCP marker interface. |
| `Auth`, `BearerAuth`           | (use `mcp.Auth`, `mcp.BearerAuth`) | Root re-exports go away — the types are already in `mcp` package. |
| `ErrServerNotFound`            | `ErrServerNotFound`              | Same name; moves into `mcp`. |
| `ErrServerExists`              | `ErrServerExists`                | Same name; moves into `mcp`. |
| `DeferOption`, `DeferThreshold`, `DeferAlwaysOn`, `DeferExclude` | same names | Move into `mcp`. |
| `deferConfig` (unexported)     | `deferConfig`                    | Unexported; moves as-is. |
| `mcpServerEntry`, `mcpToolEntry`, `mcpToolWrapper` (unexported) | `serverEntry`, `toolEntry`, `toolWrapper` | Unexported; drop redundant `mcp` prefix. |
| `mcpServerName()` (unexported method on `ServerConfig`) | `serverName()` | Unexported method. |
| `isMCPServerConfig()` (unexported marker) | `isServerConfig()` | Unexported. |
| `deferredToolsPromptSection()` (unexported) | `DeferredToolsPromptSection()` (**exported**) | Public — users prepend to their own prompt. |
| `newToolSearchTool(reg *ToolRegistry)` | `newToolSearchTool(r *Registry)` | Now takes the registry directly; reads from registry's internal store. |
| `WithMCPServer`/`WithMCPServers`/`WithSharedMCPRegistry`/`WithMCPLifecycleHandler`/`WithDeferredSchemas` (core options) | **DELETED** from core | All MCP wiring happens through `*mcp.Registry`. |

**New API (registry-level options, replacing what was in core):**

```go
// mcp.NewRegistry constructs a registry. Sharing across agents happens by
// passing the same *Registry to multiple WithTools(reg.Tools()...) calls.
func NewRegistry(opts ...RegistryOption) *Registry

type RegistryOption func(*Registry)
func WithLogger(l *slog.Logger) RegistryOption
func WithLifecycleHandler(h LifecycleHandler) RegistryOption
func WithDeferredSchemas(opts ...DeferOption) RegistryOption

// Tools returns all registered MCP tools as []oasis.AnyTool. When deferred
// mode is on, the result also includes the ToolSearch tool. Take a snapshot:
// the returned slice is decoupled from internal state.
func (r *Registry) Tools() []oasis.AnyTool

// DeferredToolsPromptSection returns the system-prompt block that teaches
// the LLM about the mcp__ deferral. Prepend it to your prompt when using
// WithDeferredSchemas.
func DeferredToolsPromptSection() string
```

**Wiring before (old):**

```go
agent := oasis.NewLLMAgent("a", "d", p,
    oasis.WithMCPServer(stdioCfg),
    oasis.WithMCPLifecycleHandler(h),
    oasis.WithDeferredSchemas(),
)
agent.MCP().Register(ctx, anotherCfg)
```

**Wiring after (new):**

```go
reg := mcp.NewRegistry(
    mcp.WithLifecycleHandler(h),
    mcp.WithDeferredSchemas(),
)
if err := reg.Register(ctx, stdioCfg); err != nil {
    slog.Warn("MCP startup failed (continuing)", "err", err)
}

prompt := mcp.DeferredToolsPromptSection() + "\n\n" + userPrompt
agent := oasis.NewLLMAgent("a", "d", p,
    oasis.WithPrompt(prompt),
    oasis.WithTools(reg.Tools()...),                            // static snapshot
    // OR for runtime-changing tool set:
    // oasis.WithDynamicTools(func(_ context.Context, _ oasis.AgentTask) []oasis.AnyTool { return reg.Tools() }),
)
```

---

### Task 1: Scaffold the mcp module

**Files:**
- Create: `mcp/go.mod`
- Modify: `go.work`

- [ ] **Step 1: Create mcp/go.mod**

```bash
cd mcp && go mod init github.com/nevindra/oasis/mcp && cd ..
```

This produces a minimal `mcp/go.mod`. Open it and append the local-dev replace directive and the parent dependency stanza so it matches the ratelimit pattern.

Final contents of `mcp/go.mod`:

```go
module github.com/nevindra/oasis/mcp

go 1.26.1

replace github.com/nevindra/oasis => ../

require github.com/nevindra/oasis v0.0.0-00010101000000-000000000000
```

- [ ] **Step 2: Add mcp to go.work**

Edit `go.work` to include `./mcp`:

```go
go 1.26.1

use (
	.
	./guardrail
	./mcp
	./ratelimit
)
```

- [ ] **Step 3: Verify everything still builds**

Run from repo root:

```bash
go build ./...
```

Expected: success. The existing `mcp/` subpackage (`package mcp`) is now its own module, but root `oasis` still imports it (`import "github.com/nevindra/oasis/mcp"`). Modules can depend on each other freely.

Run:

```bash
bash scripts/check-module-deps.sh
```

Expected:
```
==> Kernel discipline: github.com/nevindra/oasis imports nothing under github.com/nevindra/oasis/*
  FAIL — kernel imports satellite modules:
    github.com/nevindra/oasis/mcp
    github.com/nevindra/oasis/mcp/config
...
```

Yes — the script will now fail because root oasis imports the mcp wire package. **This failure is expected** and will be fixed by Task 2 when all root `mcp_*.go` files move into the `mcp` module. Do not panic. Do not edit the script yet.

- [ ] **Step 4: Commit**

```bash
git add mcp/go.mod go.work
git commit -m "$(cat <<'EOF'
feat(mcp): scaffold module skeleton

Creates github.com/nevindra/oasis/mcp as a new Go module wrapping the
existing wire-protocol mcp/ subpackage. Adds the module to go.work for
local development. Files move into the module in the next commit.

Note: this commit temporarily breaks scripts/check-module-deps.sh
(root oasis still imports mcp_*.go which references the now-satellite
mcp package). Restored by the next commit when those files move out
of core.

Spec: docs/superpowers/specs/2026-05-17-microkernel-migration-design.md §7.3, §9.1
EOF
)"
```

---

### Task 2: Move and rewrite Registry source files; gut MCP from core

This is the big atomic surgery. The build is broken between Step 1 and Step N — that's fine because we commit as one unit at the end.

**Files (new — in `mcp/`):**
- Create: `mcp/registry.go` (was root `mcp_client.go`)
- Create: `mcp/config_types.go` (was root `mcp_config_types.go`)
- Create: `mcp/state.go` (was root `mcp_state.go`)
- Create: `mcp/defer.go` (was root `mcp_defer.go`)
- Create: `mcp/tool_wrapper.go` (was root `mcp_tool_wrapper.go`)
- Create: `mcp/toolsearch.go` (was root `mcp_toolsearch.go`)
- Create: `mcp/options.go` (NEW — registry-level functional options)

**Files (deleted from root):**
- Delete: `mcp_client.go`, `mcp_config_types.go`, `mcp_state.go`, `mcp_defer.go`, `mcp_tool_wrapper.go`, `mcp_toolsearch.go`
- Delete: `agent_mcp_test.go` (tests behavior that no longer exists in core)
- Delete: `agent_deferred_test.go` (same — tests move into mcp module in Task 4)

**Files (modified in root):**
- Modify: `agent.go` — remove MCP options + config fields
- Modify: `agentcore.go` — remove MCP init block + `mcpRegistry` field
- Modify: `llmagent.go` — remove `MCP()` method

**Files (modified in `mcp/config/`):**
- Modify: `mcp/config/load.go` — re-target type references to local `mcp` package (covered in detail in Task 3)

The ordering of steps below is logical, not strictly sequential — apply them all, then commit once at Step 15.

- [ ] **Step 1: Create mcp/state.go**

Move `mcp_state.go` into `mcp/state.go`. Package becomes `mcp`. **Drop** `MCPToolResult`, `MCPContent`, `MCPAccessor`. Apply renames per the table.

Final contents of `mcp/state.go`:

```go
package mcp

import (
	"encoding/json"
	"time"
)

// ServerState represents the connection state of an MCP server.
type ServerState int

const (
	StateConnecting   ServerState = iota // 0
	StateHealthy                         // 1
	StateReconnecting                    // 2
	StateDead                            // 3
)

// String returns a human-readable name for the server state.
func (s ServerState) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateHealthy:
		return "healthy"
	case StateReconnecting:
		return "reconnecting"
	case StateDead:
		return "dead"
	default:
		return "unknown"
	}
}

// ServerStatus is a snapshot of an MCP server's runtime state.
type ServerStatus struct {
	Name        string
	Transport   string
	State       ServerState
	ToolCount   int
	LastError   error
	ConnectedAt time.Time
	Server      ServerMetadata
}

// ServerMetadata holds metadata reported by the MCP server during initialisation.
// Distinct from the wire-level ServerInfo (mcp/protocol.go) which carries only
// {Name, Version}; this extends with ProtocolVersion + Capabilities captured
// post-Initialize.
type ServerMetadata struct {
	Name            string
	Version         string
	ProtocolVersion string
	Capabilities    map[string]interface{}
}

// LifecycleHandler receives lifecycle notifications from MCP servers.
// Result type is *CallToolResult (the wire type) — the redundant public
// mirror struct from the previous root-coupled API has been removed.
type LifecycleHandler interface {
	OnConnect(name string, info ServerMetadata)
	OnDisconnect(name string, err error)
	OnToolCall(name, tool string, args json.RawMessage)
	OnToolResult(name, tool string, result *CallToolResult, err error)
}

// NoopLifecycle is a no-op default. Embed it for partial implementations:
//
//	type MyHandler struct{ mcp.NoopLifecycle }
//	func (h MyHandler) OnConnect(name string, info mcp.ServerMetadata) { /* ... */ }
type NoopLifecycle struct{}

func (NoopLifecycle) OnConnect(string, ServerMetadata)                  {}
func (NoopLifecycle) OnDisconnect(string, error)                        {}
func (NoopLifecycle) OnToolCall(string, string, json.RawMessage)        {}
func (NoopLifecycle) OnToolResult(string, string, *CallToolResult, error) {}

// EventType classifies an Event.
type EventType int

const (
	EventConnected    EventType = iota
	EventDisconnected           // 1
	EventReconnecting           // 2
	EventToolCall               // 3
	EventToolResult             // 4
)

// Event is a single lifecycle event emitted by the registry.
type Event struct {
	Type      EventType
	Server    string
	Tool      string // populated for tool-related events
	Err       error
	Timestamp time.Time
}
```

- [ ] **Step 2: Create mcp/config_types.go**

Move `mcp_config_types.go` into `mcp/config_types.go`. Apply renames. **Drop** the `Auth = mcp.Auth` / `BearerAuth = mcp.BearerAuth` re-exports since we're now in `package mcp` already.

Final contents of `mcp/config_types.go`:

```go
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
```

- [ ] **Step 3: Create mcp/defer.go**

Move `mcp_defer.go` into `mcp/defer.go`. Identical except for `package mcp` and no other changes (the names `DeferOption`, `DeferThreshold`, `DeferAlwaysOn`, `DeferExclude`, `deferConfig` all carry over unchanged).

```go
package mcp

// deferConfig holds configuration for deferred MCP schema behavior.
type deferConfig struct {
	enabled          bool
	alwaysOn         bool
	thresholdPercent int
	exclude          map[string]bool
}

// DeferOption configures WithDeferredSchemas.
type DeferOption func(*deferConfig)

// DeferThreshold sets the percentage of context window above which deferred
// loading activates. Reserved for v1.x; accepted in v1 but ignored.
func DeferThreshold(percent int) DeferOption {
	return func(c *deferConfig) {
		if percent < 0 {
			percent = 0
		}
		if percent > 100 {
			percent = 100
		}
		c.thresholdPercent = percent
	}
}

// DeferAlwaysOn forces all MCP tool schemas to be deferred regardless of
// threshold. Equivalent to plain WithDeferredSchemas() in v1.
func DeferAlwaysOn() DeferOption {
	return func(c *deferConfig) { c.alwaysOn = true }
}

// DeferExclude keeps the named MCP servers' schemas eager (never deferred).
func DeferExclude(serverNames ...string) DeferOption {
	return func(c *deferConfig) {
		if c.exclude == nil {
			c.exclude = make(map[string]bool)
		}
		for _, n := range serverNames {
			c.exclude[n] = true
		}
	}
}
```

- [ ] **Step 4: Create mcp/options.go (NEW — registry constructor + options)**

This is the **new public API** that replaces the deleted core options. Functional options pattern.

```go
package mcp

import (
	"log/slog"
	"sync"

	oasis "github.com/nevindra/oasis"
)

// RegistryOption configures a Registry at construction time.
type RegistryOption func(*Registry)

// WithLogger sets the slog.Logger used by the registry for warnings (registration
// failures, name collisions, reconnect attempts). Defaults to slog.Default().
func WithLogger(l *slog.Logger) RegistryOption {
	return func(r *Registry) { r.logger = l }
}

// WithLifecycleHandler installs a handler that receives connect/disconnect/
// tool-call/tool-result events for every MCP server registered with this
// Registry. Pass nil to reset to no-op.
func WithLifecycleHandler(h LifecycleHandler) RegistryOption {
	return func(r *Registry) {
		if h == nil {
			h = NoopLifecycle{}
		}
		r.handler = h
	}
}

// WithDeferredSchemas opts the registry into deferred schema loading. MCP tools
// are advertised to the LLM by name + description only; their input schemas
// are loaded on-demand via the auto-included ToolSearch tool (returned by
// Tools()).
//
// When enabled, callers should prepend DeferredToolsPromptSection() to the
// agent's system prompt so the model knows how to use ToolSearch.
//
// Trade-off: adds one LLM round-trip per novel MCP tool call, but saves the
// context tokens of all unloaded schemas (~600 tokens/tool average). Worth it
// for setups with 20+ MCP tools; net loss for fewer than 10.
func WithDeferredSchemas(opts ...DeferOption) RegistryOption {
	return func(r *Registry) {
		cfg := &deferConfig{enabled: true}
		for _, o := range opts {
			o(cfg)
		}
		r.defer_ = cfg
	}
}

// NewRegistry constructs a fresh registry. Multiple agents can share one
// registry by passing the same *Registry pointer to each agent's
// WithTools(reg.Tools()...) at construction.
func NewRegistry(opts ...RegistryOption) *Registry {
	r := &Registry{
		servers:  make(map[string]*serverEntry),
		handler:  NoopLifecycle{},
		eventsCh: make(chan Event, 64),
		logger:   slog.Default(),
		toolList: nil,
		toolMu:   sync.RWMutex{},
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Compile-time guard: Registry tools satisfy oasis.AnyTool.
var _ oasis.AnyTool = (*toolWrapper)(nil)
```

- [ ] **Step 5: Create mcp/registry.go (was mcp_client.go)**

This is the biggest file. Apply renames per the table. **Key structural changes:**

1. Registry no longer holds a `*oasis.ToolRegistry`. It owns its own `toolList []oasis.AnyTool` + `toolIndex map[string]oasis.AnyTool` + `toolMu sync.RWMutex`.
2. `Tools()` returns `append(slice, allMCPTools...)` plus the ToolSearch tool if deferred mode is on. Snapshot — caller can mutate the returned slice safely.
3. The old `NewSharedMCPRegistry()` is **removed**; `NewRegistry(opts...)` is the only constructor.
4. The old name-collision check via `r.toolReg.index[fullName]` becomes a check against the registry's own `toolIndex` (collisions across MCP tools only, which is the only meaningful case).

Structure of `mcp/registry.go` (full file — copy from `mcp_client.go` and apply edits):

```go
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	oasis "github.com/nevindra/oasis"
)

// Registry is the per-process or per-agent owner of MCP server connections
// and the tools they expose. Construct via NewRegistry. Multiple agents share
// a registry by being constructed with the same *Registry passed to
// oasis.WithTools(reg.Tools()...).
type Registry struct {
	mu       sync.RWMutex
	servers  map[string]*serverEntry
	handler  LifecycleHandler // never nil; Noop default
	eventsCh chan Event       // buffered 64, drop-oldest
	logger   *slog.Logger
	defer_   *deferConfig // nil = deferred mode off

	// Registry owns its tool list. After extraction, the registry no longer
	// writes through to the agent's *oasis.ToolRegistry; the user dispenses
	// tools to the agent via reg.Tools() at construction time.
	toolMu    sync.RWMutex
	toolList  []oasis.AnyTool
	toolIndex map[string]oasis.AnyTool
}

const (
	initializeTimeout    = 10 * time.Second
	reconnectBaseDelay   = 500 * time.Millisecond
	reconnectMaxDelay    = 30 * time.Second
	reconnectMaxAttempts = 10
)

var ErrServerNotFound = errors.New("MCP server not found")
var ErrServerExists = errors.New("MCP server already registered")

// SetDeferredMode is retained as an unexported runtime mutator for the tests
// that re-set the mode after construction. New callers should use the
// WithDeferredSchemas option on NewRegistry.
func (r *Registry) setDeferredMode(cfg *deferConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defer_ = cfg
}

func (r *Registry) isDeferred(serverName string) bool {
	r.mu.RLock()
	cfg := r.defer_
	r.mu.RUnlock()
	if cfg == nil || !cfg.enabled {
		return false
	}
	if cfg.exclude[serverName] {
		return false
	}
	return true
}

// serverEntry is internal: one entry per registered server.
type serverEntry struct {
	cfg         ServerConfig
	client      Client
	state       atomic.Int32 // ServerState
	info        ServerMetadata
	tools       map[string]*toolEntry
	toolsMu     sync.RWMutex
	backoff     *backoffState
	callMu      sync.Mutex // FIFO per server
	reconnectMu sync.Mutex
	cancelCtx   context.CancelFunc
	parentCtx   context.Context
	lastErr     atomic.Value
	connectAt   atomic.Int64
	parent      *Registry
}

func (e *serverEntry) reconnectLoop() {
	e.reconnectMu.Lock()
	defer e.reconnectMu.Unlock()
	if e.parentCtx.Err() != nil {
		return
	}
	e.parent.emit(Event{Type: EventReconnecting, Server: e.cfg.serverName(), Timestamp: time.Now()})

	e.backoff.attempts = 0
	for e.backoff.attempts < reconnectMaxAttempts {
		select {
		case <-e.parentCtx.Done():
			return
		default:
		}
		delay := nextBackoff(e.backoff)
		select {
		case <-time.After(delay):
		case <-e.parentCtx.Done():
			return
		}
		if e.attemptReconnect() {
			return
		}
		e.backoff.attempts++
	}
	e.state.Store(int32(StateDead))
	if e.parent.logger != nil {
		e.parent.logger.Warn("MCP server dead after max reconnect attempts",
			"server", e.cfg.serverName())
	}
}

type toolEntry struct {
	serverName string
	rawName    string
	aliasName  string
	fullName   string
	def        atomic.Pointer[oasis.ToolDefinition]
	schema     atomic.Value // json.RawMessage; cached schema for deferred tools
	schemaMu   sync.Mutex
}

type backoffState struct {
	attempts    int
	lastAttempt int64
}

// mcpError wraps an error for consistent storage in atomic.Value.
type mcpError struct{ err error }

func storeMCPError(v *atomic.Value, err error) { v.Store(mcpError{err: err}) }

func loadMCPError(v *atomic.Value) error {
	if x := v.Load(); x != nil {
		if me, ok := x.(mcpError); ok {
			return me.err
		}
	}
	return nil
}

// mapMCPResult converts mcp.CallToolResult to oasis.ToolResult for the agent
// loop. MCP server-reported errors (IsError=true) become ToolResult.Error.
func mapMCPResult(r *CallToolResult) *oasis.ToolResult {
	if r == nil {
		return &oasis.ToolResult{Error: "MCP returned nil result"}
	}
	var content string
	for _, block := range r.Content {
		if block.Type == "text" {
			if content != "" {
				content += "\n"
			}
			content += block.Text
		}
	}
	if r.IsError {
		return &oasis.ToolResult{Error: content}
	}
	return &oasis.ToolResult{Content: content}
}

func (r *Registry) fireOnToolCall(server, tool string, args json.RawMessage) {
	defer func() { _ = recover() }()
	r.handler.OnToolCall(server, tool, args)
	r.emit(Event{Type: EventToolCall, Server: server, Tool: tool, Timestamp: time.Now()})
}

func (r *Registry) fireOnToolResult(server, tool string, result *CallToolResult, err error) {
	defer func() { _ = recover() }()
	r.handler.OnToolResult(server, tool, result, err)
	r.emit(Event{Type: EventToolResult, Server: server, Tool: tool, Err: err, Timestamp: time.Now()})
}

func (r *Registry) emit(e Event) {
	select {
	case r.eventsCh <- e:
	default:
		select {
		case <-r.eventsCh:
		default:
		}
		select {
		case r.eventsCh <- e:
		default:
		}
	}
}

func (r *Registry) markUnhealthy(name string, err error) {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return
	}
	storeMCPError(&entry.lastErr, err)
	if entry.state.CompareAndSwap(int32(StateHealthy), int32(StateReconnecting)) {
		go entry.reconnectLoop()
	}
}

// addTool appends a tool to the registry's internal tool list under toolMu.
// Returns true if added; false if a tool with the same Name() already exists.
func (r *Registry) addTool(t oasis.AnyTool) bool {
	r.toolMu.Lock()
	defer r.toolMu.Unlock()
	if r.toolIndex == nil {
		r.toolIndex = make(map[string]oasis.AnyTool)
	}
	name := t.Name()
	if _, exists := r.toolIndex[name]; exists {
		return false
	}
	r.toolIndex[name] = t
	r.toolList = append(r.toolList, t)
	return true
}

// removeTool deletes a tool by name from the registry's internal list.
func (r *Registry) removeTool(name string) {
	r.toolMu.Lock()
	defer r.toolMu.Unlock()
	if _, ok := r.toolIndex[name]; !ok {
		return
	}
	delete(r.toolIndex, name)
	out := r.toolList[:0]
	for _, t := range r.toolList {
		if t.Name() != name {
			out = append(out, t)
		}
	}
	r.toolList = out
}

// hasTool checks whether a tool name is already registered.
func (r *Registry) hasTool(name string) bool {
	r.toolMu.RLock()
	defer r.toolMu.RUnlock()
	_, ok := r.toolIndex[name]
	return ok
}

// Tools returns a snapshot of all registered tools. When deferred-schema mode
// is on (via WithDeferredSchemas), the snapshot also includes the auto-managed
// ToolSearch tool. The returned slice is decoupled from internal state — safe
// to retain or mutate.
//
// Typical wiring:
//
//	agent := oasis.NewLLMAgent("a", "d", p, oasis.WithTools(reg.Tools()...))
//
// For runtime-changing tool sets (e.g., registering more MCP servers after
// agent construction), prefer:
//
//	agent := oasis.NewLLMAgent("a", "d", p,
//	    oasis.WithDynamicTools(func(_ context.Context, _ oasis.AgentTask) []oasis.AnyTool {
//	        return reg.Tools()
//	    }),
//	)
func (r *Registry) Tools() []oasis.AnyTool {
	r.toolMu.RLock()
	out := make([]oasis.AnyTool, 0, len(r.toolList)+1)
	out = append(out, r.toolList...)
	r.toolMu.RUnlock()

	r.mu.RLock()
	deferOn := r.defer_ != nil && r.defer_.enabled
	r.mu.RUnlock()
	if deferOn {
		out = append(out, newToolSearchTool(r))
	}
	return out
}

// Register connects to an MCP server, fetches its tool list, and adds each
// tool to the registry's internal tool list.
func (r *Registry) Register(ctx context.Context, cfg ServerConfig) error {
	if cfg.serverName() == "" {
		return errors.New("MCP server config: Name required")
	}
	if disabled(cfg) {
		return nil
	}
	client, err := buildClient(cfg)
	if err != nil {
		return fmt.Errorf("build client: %w", err)
	}
	return r.registerWithClient(ctx, cfg, client)
}

// registerWithClient is the test seam: accepts a pre-constructed Client.
// (Exported lowercase intentionally — package-internal; the test file lives
// in the same package.)
func (r *Registry) registerWithClient(ctx context.Context, cfg ServerConfig, client Client) error {
	name := cfg.serverName()

	r.mu.Lock()
	if _, exists := r.servers[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("%w: %q", ErrServerExists, name)
	}
	parentCtx, cancelCtx := context.WithCancel(context.Background())
	entry := &serverEntry{
		cfg:       cfg,
		client:    client,
		tools:     make(map[string]*toolEntry),
		backoff:   &backoffState{},
		cancelCtx: cancelCtx,
		parentCtx: parentCtx,
		parent:    r,
	}
	entry.state.Store(int32(StateConnecting))
	r.servers[name] = entry
	r.mu.Unlock()

	initCtx, cancelInit := context.WithTimeout(ctx, initializeTimeout)
	defer cancelInit()

	info, err := client.Initialize(initCtx)
	if err != nil {
		r.failRegister(name, entry, err)
		return fmt.Errorf("initialize %q: %w", name, err)
	}
	list, err := client.ListTools(initCtx)
	if err != nil {
		r.failRegister(name, entry, err)
		return fmt.Errorf("list tools %q: %w", name, err)
	}
	if info != nil {
		entry.info = ServerMetadata{
			Name:            info.ServerInfo.Name,
			Version:         info.ServerInfo.Version,
			ProtocolVersion: info.ProtocolVersion,
			Capabilities:    info.Capabilities,
		}
	}
	if err := r.registerTools(entry, list.Tools); err != nil {
		r.failRegister(name, entry, err)
		return err
	}
	entry.state.Store(int32(StateHealthy))
	entry.connectAt.Store(time.Now().UnixNano())

	client.OnDisconnect(func(disconnectErr error) {
		r.markUnhealthy(name, disconnectErr)
	})

	func() {
		defer func() { _ = recover() }()
		r.handler.OnConnect(name, entry.info)
	}()
	r.emit(Event{Type: EventConnected, Server: name, Timestamp: time.Now()})
	return nil
}

func (r *Registry) failRegister(name string, entry *serverEntry, err error) {
	entry.state.Store(int32(StateDead))
	storeMCPError(&entry.lastErr, err)
	r.mu.Lock()
	delete(r.servers, name)
	r.mu.Unlock()
	if r.logger != nil {
		r.logger.Warn("MCP register failed", "server", name, "err", err)
	}
}

func (r *Registry) registerTools(entry *serverEntry, tools []ToolDefinition) error {
	serverName := entry.cfg.serverName()
	filter := getFilter(entry.cfg)
	aliases := getAliases(entry.cfg)

	if filter != nil && len(filter.Include) > 0 && len(filter.Exclude) > 0 {
		return errors.New("ToolFilter: Include and Exclude are mutually exclusive")
	}

	deferred := r.isDeferred(serverName)

	entry.toolsMu.Lock()
	defer entry.toolsMu.Unlock()

	for _, t := range tools {
		if filter != nil && !filter.matches(t.Name) {
			continue
		}
		shortName := t.Name
		if alias, ok := aliases[t.Name]; ok && alias != "" {
			shortName = alias
		}
		fullName := "mcp__" + serverName + "__" + shortName

		// Collision check: only across MCP tools owned by this registry.
		if r.hasTool(fullName) {
			if r.logger != nil {
				r.logger.Warn("MCP tool name collision; skipping", "tool", fullName, "server", serverName)
			}
			continue
		}

		var params json.RawMessage
		if t.InputSchema != nil {
			if raw, ok := t.InputSchema.(json.RawMessage); ok {
				params = raw
			} else if b, merr := json.Marshal(t.InputSchema); merr == nil {
				params = b
			}
		}

		te := &toolEntry{
			serverName: serverName,
			rawName:    t.Name,
			aliasName:  shortName,
			fullName:   fullName,
		}
		def := oasis.ToolDefinition{Name: fullName, Description: t.Description}
		if deferred {
			if len(params) > 0 {
				te.schema.Store(params)
			}
		} else {
			def.Parameters = params
		}
		te.def.Store(&def)
		entry.tools[shortName] = te
		r.addTool(&toolWrapper{entry: te, server: entry, parent: r})
	}
	return nil
}

func (f *ToolFilter) matches(rawName string) bool {
	if len(f.Include) > 0 {
		for _, p := range f.Include {
			if globMatch(p, rawName) {
				return true
			}
		}
		return false
	}
	for _, p := range f.Exclude {
		if globMatch(p, rawName) {
			return false
		}
	}
	return true
}

func globMatch(pattern, name string) bool {
	ok, _ := filepath.Match(pattern, name)
	return ok
}

func disabled(cfg ServerConfig) bool {
	switch c := cfg.(type) {
	case StdioConfig:
		return c.Disabled
	case HTTPConfig:
		return c.Disabled
	}
	return false
}

func getFilter(cfg ServerConfig) *ToolFilter {
	switch c := cfg.(type) {
	case StdioConfig:
		return c.Filter
	case HTTPConfig:
		return c.Filter
	}
	return nil
}

func getAliases(cfg ServerConfig) map[string]string {
	switch c := cfg.(type) {
	case StdioConfig:
		return c.Aliases
	case HTTPConfig:
		return c.Aliases
	}
	return nil
}

func buildClient(cfg ServerConfig) (Client, error) {
	switch c := cfg.(type) {
	case StdioConfig:
		cmd := exec.Command(c.Command, c.Args...)
		cmd.Env = append(os.Environ(), envSliceFromMap(c.Env)...)
		if c.WorkDir != "" {
			cmd.Dir = c.WorkDir
		}
		return NewStdioClient(cmd)
	case HTTPConfig:
		timeout := c.Timeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		return NewHTTPClient(c.URL, c.Headers, c.Auth, timeout), nil
	default:
		return nil, fmt.Errorf("unknown MCP server config type: %T", cfg)
	}
}

func envSliceFromMap(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// Unregister disconnects and removes the server, cleaning up its tools.
func (r *Registry) Unregister(ctx context.Context, name string) error {
	r.mu.Lock()
	entry, ok := r.servers[name]
	if !ok {
		r.mu.Unlock()
		return ErrServerNotFound
	}
	delete(r.servers, name)
	r.mu.Unlock()

	entry.state.Store(int32(StateDead))
	entry.cancelCtx()

	closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	closeErr := entry.client.Close(closeCtx)

	entry.toolsMu.RLock()
	toolNames := make([]string, 0, len(entry.tools))
	for _, t := range entry.tools {
		toolNames = append(toolNames, t.fullName)
	}
	entry.toolsMu.RUnlock()
	for _, n := range toolNames {
		r.removeTool(n)
	}

	func() {
		defer func() { _ = recover() }()
		r.handler.OnDisconnect(name, nil)
	}()
	r.emit(Event{Type: EventDisconnected, Server: name, Timestamp: time.Now()})
	return closeErr
}

// List returns a snapshot of all registered servers' status.
func (r *Registry) List() []ServerStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]ServerStatus, 0, len(r.servers))
	for name, e := range r.servers {
		var transport string
		switch e.cfg.(type) {
		case StdioConfig:
			transport = "stdio"
		case HTTPConfig:
			transport = "http"
		}
		lastErr := loadMCPError(&e.lastErr)
		e.toolsMu.RLock()
		toolCount := len(e.tools)
		e.toolsMu.RUnlock()
		out = append(out, ServerStatus{
			Name:        name,
			Transport:   transport,
			State:       ServerState(e.state.Load()),
			ToolCount:   toolCount,
			LastError:   lastErr,
			ConnectedAt: time.Unix(0, e.connectAt.Load()),
			Server:      e.info,
		})
	}
	return out
}

// Subscribe returns a channel of lifecycle events. Buffered 64; oldest dropped if full.
func (r *Registry) Subscribe() <-chan Event {
	return r.eventsCh
}

func (e *serverEntry) attemptReconnect() bool {
	newClient, err := buildClient(e.cfg)
	if err != nil {
		storeMCPError(&e.lastErr, err)
		return false
	}
	initCtx, cancel := context.WithTimeout(e.parentCtx, initializeTimeout)
	defer cancel()
	if _, err := newClient.Initialize(initCtx); err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		return false
	}
	list, err := newClient.ListTools(initCtx)
	if err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		return false
	}

	oldClient := e.client
	e.client = newClient

	e.toolsMu.Lock()
	for _, t := range e.tools {
		e.parent.removeTool(t.fullName)
	}
	e.tools = make(map[string]*toolEntry)
	e.toolsMu.Unlock()

	if err := e.parent.registerTools(e, list.Tools); err != nil {
		storeMCPError(&e.lastErr, err)
		_ = newClient.Close(context.Background())
		e.client = oldClient
		return false
	}

	newClient.OnDisconnect(func(disconnectErr error) {
		e.parent.markUnhealthy(e.cfg.serverName(), disconnectErr)
	})
	e.state.Store(int32(StateHealthy))
	e.connectAt.Store(time.Now().UnixNano())
	e.parent.emit(Event{Type: EventConnected, Server: e.cfg.serverName(), Timestamp: time.Now()})
	if oldClient != nil {
		_ = oldClient.Close(context.Background())
	}
	return true
}

func nextBackoff(b *backoffState) time.Duration {
	base := reconnectBaseDelay
	for i := 0; i < b.attempts; i++ {
		base *= 2
		if base > reconnectMaxDelay {
			base = reconnectMaxDelay
			break
		}
	}
	jitterRange := int64(base) / 2
	var jitter int64
	if jitterRange > 0 {
		jitter = rand.Int63n(jitterRange) - jitterRange/2
	}
	d := time.Duration(int64(base) + jitter)
	if d < 0 {
		d = 0
	}
	return d
}

// Reconnect manually triggers a reconnect attempt on a server that may be Dead.
func (r *Registry) Reconnect(ctx context.Context, name string) error {
	r.mu.RLock()
	entry, ok := r.servers[name]
	r.mu.RUnlock()
	if !ok {
		return ErrServerNotFound
	}
	entry.state.Store(int32(StateReconnecting))
	entry.backoff.attempts = 0
	go entry.reconnectLoop()
	return nil
}

// Reload updates a server's config in place by Unregister + Register.
func (r *Registry) Reload(ctx context.Context, name string, cfg ServerConfig) error {
	if err := r.Unregister(ctx, name); err != nil && !errors.Is(err, ErrServerNotFound) {
		return fmt.Errorf("unregister: %w", err)
	}
	return r.Register(ctx, cfg)
}

// GetTool returns the wrapped oasis.AnyTool for direct invocation. The tool
// parameter is the short name (after alias, before mcp__ prefix).
func (r *Registry) GetTool(server, tool string) (oasis.AnyTool, bool) {
	r.mu.RLock()
	entry, ok := r.servers[server]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	entry.toolsMu.RLock()
	defer entry.toolsMu.RUnlock()
	te, ok := entry.tools[tool]
	if !ok {
		return nil, false
	}
	return &toolWrapper{entry: te, server: entry, parent: r}, true
}
```

- [ ] **Step 6: Create mcp/tool_wrapper.go**

Move `mcp_tool_wrapper.go` into `mcp/tool_wrapper.go`. Rename `mcpToolWrapper` → `toolWrapper`, `mcpToolEntry` → `toolEntry`, `mcpServerEntry` → `serverEntry`. Return types switch from `oasis.ToolResult`/`oasis.ToolDefinition` (formerly bare in root) to qualified ones.

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	oasis "github.com/nevindra/oasis"
)

const defaultToolCallTimeout = 60 * time.Second

// toolWrapper implements oasis.AnyTool. Forwards calls to the underlying
// Client, translating between Oasis types and MCP wire types. Each wrapper
// represents exactly one MCP tool.
type toolWrapper struct {
	entry  *toolEntry
	server *serverEntry
	parent *Registry
}

// Name implements oasis.AnyTool. Returns the MCP tool's full registry name.
func (w *toolWrapper) Name() string {
	if d := w.entry.def.Load(); d != nil {
		return d.Name
	}
	return w.entry.fullName
}

// Definition implements oasis.AnyTool.
func (w *toolWrapper) Definition() oasis.ToolDefinition {
	if d := w.entry.def.Load(); d != nil {
		return *d
	}
	return oasis.ToolDefinition{}
}

// EnsureSchema implements oasis.SchemaEnsurer. Loads the cached schema (or
// re-fetches from the server on cache miss) into the tool's ToolDefinition.
func (w *toolWrapper) EnsureSchema(ctx context.Context) error {
	w.entry.schemaMu.Lock()
	defer w.entry.schemaMu.Unlock()

	cur := w.entry.def.Load()
	if cur != nil && len(cur.Parameters) > 0 {
		return nil
	}

	var newSchema json.RawMessage
	if raw, ok := w.entry.schema.Load().(json.RawMessage); ok && len(raw) > 0 {
		newSchema = raw
	} else {
		state := ServerState(w.server.state.Load())
		if state != StateHealthy {
			return fmt.Errorf("cannot ensure schema for %q: server not healthy (%s)",
				w.entry.fullName, state)
		}
		list, err := w.server.client.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("list tools: %w", err)
		}
		for _, t := range list.Tools {
			if t.Name != w.entry.rawName {
				continue
			}
			if raw, ok := t.InputSchema.(json.RawMessage); ok {
				newSchema = raw
			} else if b, merr := json.Marshal(t.InputSchema); merr == nil {
				newSchema = b
			}
			break
		}
		if len(newSchema) == 0 {
			return fmt.Errorf("tool %q not found on server after refetch", w.entry.rawName)
		}
	}

	newDef := oasis.ToolDefinition{
		Name:        cur.Name,
		Description: cur.Description,
		Parameters:  newSchema,
	}
	w.entry.def.Store(&newDef)
	return nil
}

// ExecuteRaw implements oasis.AnyTool.
func (w *toolWrapper) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, defaultToolCallTimeout)
	defer cancel()

	state := ServerState(w.server.state.Load())
	if state != StateHealthy {
		return oasis.ToolResult{
			Error: fmt.Sprintf("MCP server %q not healthy (%s)", w.server.cfg.serverName(), state),
		}, nil
	}

	if w.parent != nil {
		w.parent.fireOnToolCall(w.server.cfg.serverName(), w.entry.rawName, args)
	}

	w.server.callMu.Lock()
	res, err := w.server.client.CallTool(callCtx, w.entry.rawName, args)
	w.server.callMu.Unlock()

	if err != nil {
		if w.parent != nil {
			w.parent.markUnhealthy(w.server.cfg.serverName(), err)
		}
		return oasis.ToolResult{
			Error: fmt.Sprintf("MCP call to %s failed: %v", w.entry.fullName, err),
		}, nil
	}

	out := mapMCPResult(res)
	if w.parent != nil {
		w.parent.fireOnToolResult(w.server.cfg.serverName(), w.entry.rawName, res, nil)
	}
	return *out, nil
}
```

Note the simplification at the bottom: we pass `res` (`*CallToolResult`) straight into `fireOnToolResult` instead of going through `mapMCPResultToPublic` (which converted to the now-deleted `MCPToolResult`).

- [ ] **Step 7: Create mcp/toolsearch.go**

Move `mcp_toolsearch.go` into `mcp/toolsearch.go`. Rebind the tool to a `*Registry` (instead of `*ToolRegistry`). Searches the registry's internal tool list for deferred definitions.

```go
package mcp

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"unicode"

	oasis "github.com/nevindra/oasis"
)

const ToolSearchName = "ToolSearch"

const toolSearchDescription = `Find available tools by keyword search. Many MCP tools are loaded on-demand to save context — their input schemas are NOT visible until you query them.

WHEN TO USE:
- You see a tool name prefixed with "mcp__" but want to call it (you need the schema first)
- You're not sure which tool fits the user's request — search by capability ("read pdf", "send email")
- You see a tool description that's promising but want full input details

HOW TO USE:
1. Call ToolSearch with a keyword query (2-5 words)
2. Inspect returned tool schemas
3. Then call the actual tool with correctly-formed arguments

EXAMPLE:
User: "Create a GitHub issue about the login bug"
You: ToolSearch(query="create github issue") → returns mcp__github__create_issue with schema → call it`

const toolSearchInputSchema = `{
    "type": "object",
    "properties": {
        "query": {
            "type": "string",
            "description": "Keywords to match against tool names and descriptions. Use 2-5 specific words like 'create issue github' or 'read pdf file'."
        },
        "max_results": {
            "type": "integer",
            "description": "Maximum tools to return (default 10, max 25)"
        }
    },
    "required": ["query"]
}`

const (
	toolSearchDefaultMax = 10
	toolSearchHardMax    = 25
)

// toolSearchTool is an AnyTool that searches the registry's tools for
// deferred (schema-empty) entries matching a keyword query, lazy-loading
// their schemas via SchemaEnsurer when a match is selected.
type toolSearchTool struct {
	reg *Registry
}

func newToolSearchTool(r *Registry) *toolSearchTool { return &toolSearchTool{reg: r} }

func (t *toolSearchTool) Name() string { return ToolSearchName }

func (t *toolSearchTool) Definition() oasis.ToolDefinition {
	return oasis.ToolDefinition{
		Name:        ToolSearchName,
		Description: toolSearchDescription,
		Parameters:  json.RawMessage(toolSearchInputSchema),
	}
}

type toolSearchInput struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type toolSearchMatch struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	LoadError   string          `json:"loadError,omitempty"`
}

type toolSearchOutput struct {
	Tools []toolSearchMatch `json:"tools"`
	Note  string            `json:"note,omitempty"`
}

func (t *toolSearchTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (oasis.ToolResult, error) {
	var in toolSearchInput
	if err := json.Unmarshal(args, &in); err != nil {
		return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
	}
	if in.Query == "" {
		return oasis.ToolResult{Error: "query must not be empty"}, nil
	}

	n := in.MaxResults
	if n <= 0 {
		n = toolSearchDefaultMax
	}
	if n > toolSearchHardMax {
		n = toolSearchHardMax
	}

	qWords := tokenizeQuery(in.Query)
	if len(qWords) == 0 {
		return oasis.ToolResult{Error: "query contained no searchable words"}, nil
	}

	type scored struct {
		def   oasis.ToolDefinition
		tool  oasis.AnyTool
		score float64
	}

	// Snapshot of the registry's tool list under toolMu.
	t.reg.toolMu.RLock()
	candidates := make([]oasis.AnyTool, 0, len(t.reg.toolList))
	candidates = append(candidates, t.reg.toolList...)
	t.reg.toolMu.RUnlock()

	var matches []scored
	for _, tl := range candidates {
		d := tl.Definition()
		// Deferred = no params loaded yet.
		if len(d.Parameters) != 0 {
			continue
		}
		s := scoreToolMatch(qWords, d.Name, d.Description)
		if s > 0 {
			matches = append(matches, scored{def: d, tool: tl, score: s})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})
	if len(matches) > n {
		matches = matches[:n]
	}

	out := toolSearchOutput{Tools: make([]toolSearchMatch, 0, len(matches))}
	for _, m := range matches {
		mj := toolSearchMatch{Name: m.def.Name, Description: m.def.Description}
		if ensurer, ok := m.tool.(oasis.SchemaEnsurer); ok {
			if err := ensurer.EnsureSchema(ctx); err != nil {
				mj.LoadError = err.Error()
			} else {
				mj.InputSchema = m.tool.Definition().Parameters
			}
		}
		out.Tools = append(out.Tools, mj)
	}
	if len(out.Tools) == 0 {
		out.Note = "No tools matched query. Try broader or different keywords."
	}

	content, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return oasis.ToolResult{Error: "format result: " + err.Error()}, nil
	}
	return oasis.ToolResult{Content: string(content)}, nil
}

func tokenizeQuery(query string) []string {
	var words []string
	var cur strings.Builder
	for _, r := range strings.ToLower(query) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		words = append(words, cur.String())
	}
	return words
}

func scoreToolMatch(queryWords []string, toolName, description string) float64 {
	name := strings.ToLower(toolName)
	desc := strings.ToLower(description)
	var score float64
	for _, w := range queryWords {
		if strings.Contains(name, w) {
			score += 3.0
		}
		if strings.Contains(desc, w) {
			score += 1.0
		}
	}
	return score
}

// DeferredToolsPromptSection returns the system-prompt block that explains
// the mcp__ deferral mechanism to the LLM. Prepend it to your prompt when
// using WithDeferredSchemas:
//
//	prompt := mcp.DeferredToolsPromptSection() + "\n\n" + userPrompt
//	agent := oasis.NewLLMAgent("a", "d", p,
//	    oasis.WithPrompt(prompt),
//	    oasis.WithTools(reg.Tools()...),
//	)
func DeferredToolsPromptSection() string {
	return `<deferred-tools>
You have access to additional tools whose schemas are loaded on-demand.
Tools prefixed with "mcp__" appear in your tool list with name and description
but WITHOUT input schemas — these are deferred. Before calling any deferred tool,
use the ToolSearch tool to load its schema:

  ToolSearch(query="<keywords describing what you need>")

This returns the full schema. After receiving the schema, call the tool normally.
Tools NOT prefixed with "mcp__" have full schemas and can be called directly.
</deferred-tools>`
}
```

- [ ] **Step 8: Delete the six moved root files**

```bash
rm mcp_client.go mcp_config_types.go mcp_state.go mcp_defer.go mcp_tool_wrapper.go mcp_toolsearch.go
```

- [ ] **Step 9: Edit root/agent.go — remove MCP options + config fields**

In `agent.go`, delete lines 189-193 (the `// MCP` block of `agentConfig` fields):

```go
	// MCP
	mcpStartupConfigs   []MCPServerConfig   // set by WithMCPServer / WithMCPServers
	sharedMCPRegistry   *MCPRegistry        // set by WithSharedMCPRegistry
	mcpLifecycleHandler MCPLifecycleHandler // set by WithMCPLifecycleHandler
	deferConfig         *deferConfig        // set by WithDeferredSchemas
```

Delete the five MCP option functions (lines 410-469 in current agent.go):

- `WithMCPServer`
- `WithMCPServers`
- `WithSharedMCPRegistry`
- `WithMCPLifecycleHandler`
- `WithDeferredSchemas`

Verify by grep that no `MCP` references remain in `agent.go`:

```bash
grep -n "MCP\|mcp" agent.go
```

Expected: zero matches (or only legitimate non-MCP matches if any sneaked through — re-inspect each).

- [ ] **Step 10: Edit root/agentcore.go — remove MCP initialization**

In `agentcore.go`:

- Delete the `mcpRegistry *MCPRegistry` field from `agentCore` (line 52).
- Delete the entire MCP init block (lines 121-154) — from `// MCP registry: shared if provided, else per-agent.` through the closing `}` of the `for _, mcfg := range cfg.mcpStartupConfigs` loop.

The block to delete:

```go
	// MCP registry: shared if provided, else per-agent.
	if cfg.sharedMCPRegistry != nil {
		c.mcpRegistry = cfg.sharedMCPRegistry
	} else {
		c.mcpRegistry = &MCPRegistry{
			servers:  make(map[string]*mcpServerEntry),
			handler:  NoopMCPLifecycle{},
			eventsCh: make(chan MCPEvent, 64),
			logger:   c.logger,
			toolReg:  c.tools,
		}
	}
	if cfg.mcpLifecycleHandler != nil {
		c.mcpRegistry.SetLifecycleHandler(cfg.mcpLifecycleHandler)
	}

	// Deferred schemas (Plan α-2): set BEFORE startup MCP servers register so
	// new tools respect the mode. Auto-register ToolSearch and prepend the
	// system-prompt block that teaches the model about the mcp__ deferral.
	if cfg.deferConfig != nil && cfg.deferConfig.enabled {
		c.mcpRegistry.SetDeferredMode(cfg.deferConfig)
		c.tools.Add(newToolSearchTool(c.tools))
		c.systemPrompt = deferredToolsPromptSection() + "\n\n" + c.systemPrompt
	}

	// Register startup MCP servers (soft-degrade: log and continue on failure).
	for _, mcfg := range cfg.mcpStartupConfigs {
		if err := c.mcpRegistry.Register(context.Background(), mcfg); err != nil {
			if c.logger != nil {
				c.logger.Warn("MCP startup registration failed (continuing)",
					"server", mcfg.mcpServerName(), "err", err)
			}
		}
	}
```

The `context` import in agentcore.go may become unused after this delete — check, and remove the import line if so. (Other functions like `executeWithSpan` still use `context.Context`, so the import likely stays.)

Verify:

```bash
grep -n "MCP\|mcp\|defer" agentcore.go
```

Expected: zero MCP references. The only matches should be unrelated `deferred`/`defer` patterns (like `defer c.tracer.End()` — those are fine).

- [ ] **Step 11: Edit root/llmagent.go — remove MCP() method**

Delete lines 47-53:

```go
// MCP returns the agent's MCP controller for runtime server management.
// The controller is backed by the agent's MCPRegistry (which may be shared
// across agents when WithSharedMCPRegistry was used at construction).
// The returned value is never nil.
func (a *LLMAgent) MCP() *MCPController {
	return &MCPController{reg: a.mcpRegistry}
}
```

Verify:

```bash
grep -n "MCP\|mcp" llmagent.go
```

Expected: zero matches.

- [ ] **Step 12: Delete obsolete root tests**

```bash
rm agent_mcp_test.go agent_deferred_test.go
```

These tested behaviors that no longer exist in core (`WithMCPServer`, `WithSharedMCPRegistry`, `WithDeferredSchemas`, `MCP()` accessor, auto-registered ToolSearch, auto-prepended prompt section). Equivalent assertions move into the mcp module's own tests (Task 4) — except the auto-wiring tests, which are gone permanently (composition at app layer, no auto-wiring to test).

- [ ] **Step 13: Update mcp/config/load.go**

(Done in detail in Task 3 — for atomicity, also apply the changes here so the build is green when this commit lands. The Task 3 separation is documentation: it has its own commit only if you want to split, but for now bundle it into this big commit.)

See Task 3 for the exact edits needed. Apply them now.

- [ ] **Step 14: Build everything**

From repo root:

```bash
go build ./...
```

Expected: success. If errors:
- `undefined: oasis.MCPServerConfig` etc. anywhere outside `mcp/` → grep, fix imports.
- `mcp_tool_wrapper.go: no such file` from test imports → check no leftover root MCP file references.

Run tests for both modules:

```bash
go test ./...
(cd mcp && go test ./...)
```

The root oasis test suite should pass. The mcp test suite will fail with "no tests" because we haven't moved the tests yet — that's expected; Task 4 moves them. **For this task's commit, only require:**

- `go build ./...` succeeds in root.
- `go build ./...` succeeds in `mcp/`.
- `go vet ./...` succeeds in both.
- All non-MCP tests in root pass: `go test $(go list ./... | grep -v 'mcp_e2e\|mcp_client\|mcp_state\|mcp_defer\|mcp_tool_wrapper\|mcp_toolsearch')` (but those files no longer exist, so just `go test ./...` should pass).

- [ ] **Step 15: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
feat(mcp)!: extract Registry + tool wrapper into oasis/mcp module

Moves the MCP Registry layer (mcp_client.go, mcp_config_types.go,
mcp_state.go, mcp_defer.go, mcp_tool_wrapper.go, mcp_toolsearch.go)
out of the root oasis package and into the existing mcp/ subpackage,
which is now its own Go module. The MCP prefix on types is dropped
since the package name now provides the namespace.

Breaking changes (no external users; pre-v1 migration):
  - Removed oasis.WithMCPServer, WithMCPServers, WithSharedMCPRegistry,
    WithMCPLifecycleHandler, WithDeferredSchemas options.
  - Removed (*oasis.LLMAgent).MCP() accessor.
  - Removed oasis.MCPRegistry, MCPController, MCPServerConfig,
    StdioMCPConfig, HTTPMCPConfig, MCPToolFilter, MCPServerStatus,
    MCPServerState, MCPServerInfo, MCPToolResult, MCPContent,
    MCPLifecycleHandler, NoopMCPLifecycle, MCPEvent, MCPEventType,
    MCPAccessor, and DeferOption family from root.
  - The redundant MCPToolResult/MCPContent public mirrors of
    mcp.CallToolResult/mcp.ContentBlock are dropped — LifecycleHandler
    now uses *CallToolResult directly.

New wiring:
  reg := mcp.NewRegistry(mcp.WithDeferredSchemas())
  reg.Register(ctx, mcp.StdioConfig{...})
  prompt := mcp.DeferredToolsPromptSection() + "\n\n" + userPrompt
  agent := oasis.NewLLMAgent("a", "d", p,
      oasis.WithPrompt(prompt),
      oasis.WithTools(reg.Tools()...),
  )

Tests carry over in the next commit. CHANGELOG entry is in the final
commit of this extraction.

Spec: docs/superpowers/specs/2026-05-17-microkernel-migration-design.md §7.3, §9.1
EOF
)"
```

---

### Task 3: Update mcp/config/ subpackage to new type names

The `mcp/config/` subpackage (package `mcpconfig`) loads JSON files into `oasis.MCPServerConfig` values. After Task 2, those types live in `mcp` (parent), not `oasis`. Re-target the imports.

**Files:**
- Modify: `mcp/config/load.go`
- Modify: `mcp/config/load_test.go`

Note: this work was already done atomically as part of Task 2 step 13 to keep the build green. This task documents the exact edits in isolation. Either keep them in Task 2's commit or split into a separate commit — they're tightly coupled to the rename surface, so bundling is fine.

- [ ] **Step 1: Edit mcp/config/load.go imports**

Replace:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nevindra/oasis"
)
```

With:

```go
import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nevindra/oasis/mcp"
)
```

- [ ] **Step 2: Rewrite type references in mcp/config/load.go**

Apply these substitutions throughout the file (use Edit tool with `replace_all` per identifier):

| Old                            | New                  |
|---|---|
| `oasis.MCPServerConfig`        | `mcp.ServerConfig`   |
| `oasis.StdioMCPConfig`         | `mcp.StdioConfig`    |
| `oasis.HTTPMCPConfig`          | `mcp.HTTPConfig`     |
| `oasis.MCPToolFilter`          | `mcp.ToolFilter`     |
| `oasis.Auth`                   | `mcp.Auth`           |
| `oasis.BearerAuth`             | `mcp.BearerAuth`     |

Return-type signatures change accordingly:

```go
func Load(startDir string) ([]mcp.ServerConfig, error) { ... }
func LoadFile(path string) ([]mcp.ServerConfig, error) { ... }
func parseServer(name string, raw json.RawMessage) (mcp.ServerConfig, error) { ... }
```

Verify:

```bash
grep -n "oasis\." mcp/config/load.go
```

Expected: zero matches.

- [ ] **Step 3: Update mcp/config/load_test.go**

Same substitutions. Run:

```bash
(cd mcp/config && go test ./...)
```

Expected: PASS.

- [ ] **Step 4: Confirm no other importers of mcpconfig outside the new module**

```bash
grep -rn "nevindra/oasis/mcp/config" --include="*.go" .
```

Expected: only files inside `mcp/config/` itself. (The reference app `cmd/bot_example` that previously consumed it was deleted in P0.1.)

(If Task 3 is committed separately from Task 2, the commit message:)

```
refactor(mcp/config): re-target type refs to mcp package

Updates mcp/config/load.go and its test to reference mcp.ServerConfig
et al. instead of the now-removed oasis.MCPServerConfig family. The
mcpconfig subpackage now imports only mcp (parent), no longer oasis.
```

---

### Task 4: Move MCP tests into the mcp module

**Files:**
- Create: `mcp/registry_test.go` (was root `mcp_client_test.go`)
- Create: `mcp/config_types_test.go` (was root `mcp_config_types_test.go`)
- Create: `mcp/state_test.go` (was root `mcp_state_test.go`)
- Create: `mcp/defer_test.go` (was root `mcp_defer_test.go`)
- Create: `mcp/deferred_e2e_test.go` (was root `mcp_deferred_e2e_test.go`)
- Create: `mcp/e2e_test.go` (was root `mcp_e2e_test.go`)
- Create: `mcp/tool_wrapper_test.go` (was root `mcp_tool_wrapper_test.go`)
- Create: `mcp/toolsearch_test.go` (was root `mcp_toolsearch_test.go`)
- Delete: all eight root `mcp_*_test.go` files

- [ ] **Step 1: Move tests verbatim, then apply renames**

For each of the eight test files, run a `git mv` from root to `mcp/`, then:

1. Change `package oasis` → `package mcp` at the top.
2. Apply the canonical rename table to every identifier in the file.
3. Remove the now-redundant `import "github.com/nevindra/oasis/mcp"` (most tests import this for `mcp.ToolDefinition` etc. — now those are bare references in the same package).
4. Most tests still need to import `oasis` for kernel types they reference (`AnyTool`, `ToolDefinition`, etc.). Add `oasis "github.com/nevindra/oasis"` where needed and qualify `AnyTool` → `oasis.AnyTool`, `ToolDefinition` → `oasis.ToolDefinition`, etc.
5. The `newTestRegistry(t *testing.T)` helper (currently in `mcp_client_test.go`) should be retained but updated:

```go
func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r := NewRegistry(WithLogger(slog.New(slog.NewTextHandler(os.Stderr, nil))))
	return r
}
```

Note: `nullProvider` from the deleted `agent_mcp_test.go` is no longer needed in mcp tests (no LLMAgent construction in mcp tests). Drop it.

6. Tests previously accessing `reg.toolReg.AllDefinitions()` need a new surface. Add this unexported same-package helper to `mcp/registry.go` (right after `Tools()` is a natural location):

```go
// toolDefinitionsForTest returns all tool definitions registered with r.
// Same-package internal helper used by mcp/*_test.go to inspect registry
// state without exposing the internal tool list to consumers.
func (r *Registry) toolDefinitionsForTest() []oasis.ToolDefinition {
	r.toolMu.RLock()
	defer r.toolMu.RUnlock()
	defs := make([]oasis.ToolDefinition, 0, len(r.toolList))
	for _, t := range r.toolList {
		defs = append(defs, t.Definition())
	}
	return defs
}
```

(Since the tests are `package mcp` — white-box, same-package — they can call this directly. No `_test.go`-scoped file is needed.)

Update test sites:

```go
// Was:
for _, d := range reg.toolReg.AllDefinitions() { ... }
// Becomes:
for _, d := range reg.toolDefinitionsForTest() { ... }
```

7. Tests that previously did `reg.SetDeferredMode(&deferConfig{enabled: true})` continue to work — `setDeferredMode` (renamed lowercase as a same-package method) is still available. Just rename the call: `reg.setDeferredMode(&deferConfig{enabled: true})`.

8. Tests using `newToolSearchTool(reg.toolReg)` become `newToolSearchTool(reg)` (the new signature takes `*Registry`).

- [ ] **Step 2: Migrate the prompt-section assertions**

The deleted `agent_deferred_test.go` had three tests we keep, restated in mcp module semantics:

```go
// mcp/deferred_e2e_test.go (or a new mcp/prompt_test.go)

func TestDeferredToolsPromptSection_NonEmpty(t *testing.T) {
	got := DeferredToolsPromptSection()
	if !strings.Contains(got, "<deferred-tools>") {
		t.Errorf("missing <deferred-tools> marker: %q", got)
	}
}

func TestRegistry_Tools_IncludesToolSearchWhenDeferred(t *testing.T) {
	reg := NewRegistry(WithDeferredSchemas())
	tools := reg.Tools()
	var found bool
	for _, tl := range tools {
		if tl.Name() == ToolSearchName {
			found = true
			break
		}
	}
	if !found {
		t.Error("Tools() should include ToolSearch when deferred mode is on")
	}
}

func TestRegistry_Tools_NoToolSearchWithoutDeferred(t *testing.T) {
	reg := NewRegistry()
	for _, tl := range reg.Tools() {
		if tl.Name() == ToolSearchName {
			t.Error("Tools() must NOT include ToolSearch when deferred mode is off")
		}
	}
}
```

The tests dropped permanently (replaced by composition at app layer):
- `TestAgent_WithMCPServers_RegistersAtConstruction` — there's no `WithMCPServer` to test.
- `TestAgent_MCP_Accessor_ReturnsController` — there's no `MCP()` accessor.
- `TestAgent_WithSharedMCPRegistry_SharesAcrossAgents` — sharing happens via the app passing the same `*Registry` to `WithTools(reg.Tools()...)` — testing it would be testing `oasis.WithTools`, which is already covered.
- `TestAgent_WithSharedMCPRegistry_IndependentRegistriesByDefault` — same.
- `TestWithDeferredSchemas_PrependsSystemPrompt` — there's no auto-prepend; replaced by `TestDeferredToolsPromptSection_NonEmpty` above.

- [ ] **Step 3: Run all tests in both modules**

```bash
go test ./...
(cd mcp && go test ./...)
```

Both must pass.

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
test(mcp): move MCP tests into the module

Eight root mcp_*_test.go files relocate to mcp/ with package rename
and identifier substitutions per the extraction. Introduces a
toolDefinitionsForTest() same-package helper to replace the old
reg.toolReg.AllDefinitions() introspection (the registry no longer
embeds an oasis.ToolRegistry).

Also restates the three retained deferred-mode assertions
(prompt-section non-empty, ToolSearch present/absent in Tools())
in registry-centric form. Five removed core-coupled tests
(WithMCPServer / WithSharedMCPRegistry / MCP() accessor / auto-prepend
prompt) are intentionally dropped — those behaviors no longer exist
in core.

Spec: docs/superpowers/specs/2026-05-17-microkernel-migration-design.md §9.1
EOF
)"
```

---

### Task 5: Add DX artifacts — example_test.go and doc.go

Pattern mirrors `ratelimit/example_test.go` + `ratelimit/doc.go`.

**Files:**
- Create: `mcp/example_test.go`
- Create: `mcp/doc.go`

- [ ] **Step 1: Write mcp/doc.go**

```go
// Package mcp implements an oasis.AnyTool-compatible client for Model
// Context Protocol (MCP) servers.
//
// Two layers live here:
//
//   - The wire layer (Client, NewStdioClient, NewHTTPClient, ToolDefinition,
//     CallToolResult, …) implements the MCP JSON-RPC protocol over stdio
//     and HTTP+SSE transports. Use it directly when you need raw access.
//
//   - The Registry layer (Registry, NewRegistry, ServerConfig, …) wraps
//     one or more MCP servers, exposes their tools as oasis.AnyTool values,
//     supplies deferred-schema loading for context-tight setups, and
//     handles reconnection on transport failure.
//
// Typical wiring:
//
//	reg := mcp.NewRegistry(
//	    mcp.WithLifecycleHandler(myHandler),
//	)
//	if err := reg.Register(ctx, mcp.StdioConfig{
//	    Name:    "github",
//	    Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-github"},
//	    Env:     map[string]string{"GITHUB_TOKEN": os.Getenv("GITHUB_TOKEN")},
//	}); err != nil {
//	    log.Printf("MCP startup failed (continuing): %v", err)
//	}
//
//	agent := oasis.NewLLMAgent("github-bot", "GitHub helper", provider,
//	    oasis.WithTools(reg.Tools()...),
//	)
//
// For runtime-changing tool sets (e.g., registering more MCP servers after
// agent construction), pass a closure to oasis.WithDynamicTools instead:
//
//	oasis.WithDynamicTools(func(_ context.Context, _ oasis.AgentTask) []oasis.AnyTool {
//	    return reg.Tools()
//	})
//
// Deferred-schema mode is opt-in via WithDeferredSchemas. Tools appear in
// the LLM's tool list by name + description only; their input schemas load
// on demand through the ToolSearch tool (which Tools() includes
// automatically). Worth it past ~20 MCP tools; net loss for fewer than 10.
// When enabled, prepend DeferredToolsPromptSection() to the agent's
// system prompt.
package mcp
```

- [ ] **Step 2: Write mcp/example_test.go**

```go
package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"

	oasis "github.com/nevindra/oasis"
	"github.com/nevindra/oasis/mcp"
	"github.com/nevindra/oasis/mcp/mcptest"
)

// ExampleNewRegistry shows the typical wiring: build a registry, register
// servers, and hand the resulting tools to oasis.WithTools.
func ExampleNewRegistry() {
	fake := mcptest.New()
	fake.Tools = []mcp.ToolDefinition{
		{Name: "greet", Description: "say hello", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	fake.OnToolCall = func(name string, args json.RawMessage) (mcp.CallToolResult, error) {
		return mcp.CallToolResult{
			Content: []mcp.ContentBlock{{Type: "text", Text: "hello from " + name}},
		}, nil
	}
	out, in := fake.Pipes()
	defer fake.Stop()

	reg := mcp.NewRegistry()
	// In production code, the user calls reg.Register(ctx, mcp.StdioConfig{Command: ...})
	// or mcp.HTTPConfig{URL: ...}. Tests use the package-internal seam below for
	// hermetic execution against mcptest.
	if err := registerWithFakeClient(reg, "demo", mcp.NewStdioClientFromPipes(out, in)); err != nil {
		fmt.Println("register failed:", err)
		return
	}

	tools := reg.Tools()
	fmt.Println("tools:", len(tools))
	for _, t := range tools {
		fmt.Println("-", t.Name())
	}
	// Output:
	// tools: 1
	// - mcp__demo__greet
}

// registerWithFakeClient is a thin wrapper around the package-internal
// registerWithClient test seam, kept here so the example can compile against
// mcptest without exposing the seam in the public API.
func registerWithFakeClient(reg *mcp.Registry, name string, c mcp.Client) error {
	return reg.RegisterTestClient(context.Background(), mcp.StdioConfig{Name: name}, c)
}

// Compile-time guard: every tool returned by Tools() satisfies oasis.AnyTool.
var _ = func() oasis.AnyTool {
	var t oasis.AnyTool
	return t
}
```

Note: the example needs a public-but-test-flavored seam to register against a fixture without launching a real subprocess. Add a small public method to `mcp/registry.go`:

```go
// RegisterTestClient is a test-only entry point that bypasses transport
// construction. Production code should use Register, which builds the
// transport from a ServerConfig. Intended for use with mcp/mcptest fixtures
// or other in-process Client implementations.
func (r *Registry) RegisterTestClient(ctx context.Context, cfg ServerConfig, client Client) error {
	return r.registerWithClient(ctx, cfg, client)
}
```

If preferred, scope the seam to `mcptest` only — but this small method keeps `example_test.go` external (`package mcp_test`) which is more idiomatic for examples. Adopt the public method.

- [ ] **Step 3: Run the example**

```bash
(cd mcp && go test -run ExampleNewRegistry -v ./...)
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add mcp/doc.go mcp/example_test.go mcp/registry.go
git commit -m "$(cat <<'EOF'
docs(mcp): add example_test.go and doc.go

Adds Package-level doc.go covering both the wire and Registry layers,
the typical wiring pattern, and the deferred-schema trade-off.

Adds ExampleNewRegistry that wires a fake MCP server (via mcptest)
through a Registry and verifies the Tools() snapshot. To keep the
example external (package mcp_test) without depending on the
package-internal registerWithClient seam, this commit also exports
RegisterTestClient — a thin pass-through documented as test-only.

Spec: docs/superpowers/specs/2026-05-17-microkernel-migration-design.md §4.3
EOF
)"
```

---

### Task 6: Enable the mcp dependency check in CI

**Files:**
- Modify: `scripts/check-module-deps.sh`

- [ ] **Step 1: Uncomment the mcp check**

In `scripts/check-module-deps.sh`, find the satellite module list at the bottom:

```bash
# === Satellite modules (extend this list as extractions land) ===
check_satellite ratelimit
# check_satellite catalog
# check_satellite network
# check_satellite guardrail
# check_satellite compaction
# check_satellite mcp
```

Change `# check_satellite mcp` to `check_satellite mcp`. Final:

```bash
check_satellite ratelimit
check_satellite mcp
```

- [ ] **Step 2: Run the check**

```bash
bash scripts/check-module-deps.sh
```

Expected output:

```
==> Kernel discipline: github.com/nevindra/oasis imports nothing under github.com/nevindra/oasis/*
  OK
==> Module independence: github.com/nevindra/oasis/ratelimit imports only kernel + declared deps
  OK
==> Module independence: github.com/nevindra/oasis/mcp imports only kernel + declared deps
  OK
```

If kernel-discipline fails: there is still a stray MCP reference in root oasis. Grep for it:

```bash
grep -rn "nevindra/oasis/mcp" --include="*.go" . | grep -v "^./mcp/"
```

The only matches should be inside `mcp/`. If any root oasis file still imports `mcp`, fix it.

If mcp module independence fails: the mcp module is importing something other than core. Inspect:

```bash
(cd mcp && go list -deps ./... | grep nevindra/oasis | grep -v '/mcp$\|/mcp/')
```

Should only show `github.com/nevindra/oasis`. Anything else is a bug.

- [ ] **Step 3: Commit**

```bash
git add scripts/check-module-deps.sh
git commit -m "$(cat <<'EOF'
ci(mcp): enable mcp dependency check

Uncomments check_satellite mcp in scripts/check-module-deps.sh. With
the kernel now stripped of MCP wiring and the satellite module
importing only oasis core (plus its own subpackages), both invariants
hold and the check enforces them going forward.

Spec: docs/superpowers/specs/2026-05-17-microkernel-migration-design.md §9.3
EOF
)"
```

---

### Task 7: CHANGELOG entry + final acceptance verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add CHANGELOG entry**

Read current `CHANGELOG.md`:

```bash
head -40 CHANGELOG.md
```

Locate the `[Unreleased]` section (top of the file). Append an MCP entry under the appropriate sub-headings. Use this template:

```markdown
### Changed

- **BREAKING**: `oasis/mcp` is now a separate Go module.
  - Import path: `github.com/nevindra/oasis/mcp`
  - All `MCP` prefixes on types are dropped (the package name now provides the namespace).
  - Migration:
    ```go
    // Before
    import "github.com/nevindra/oasis"

    agent := oasis.NewLLMAgent("a", "d", p,
        oasis.WithMCPServer(oasis.StdioMCPConfig{...}),
        oasis.WithMCPLifecycleHandler(h),
        oasis.WithDeferredSchemas(),
    )
    agent.MCP().Register(ctx, anotherConfig)

    // After
    import (
        "github.com/nevindra/oasis"
        "github.com/nevindra/oasis/mcp"
    )

    reg := mcp.NewRegistry(
        mcp.WithLifecycleHandler(h),
        mcp.WithDeferredSchemas(),
    )
    _ = reg.Register(ctx, mcp.StdioConfig{...})
    _ = reg.Register(ctx, anotherConfig)

    prompt := mcp.DeferredToolsPromptSection() + "\n\n" + userPrompt
    agent := oasis.NewLLMAgent("a", "d", p,
        oasis.WithPrompt(prompt),
        oasis.WithTools(reg.Tools()...),
    )
    ```

### Removed (from root `oasis`)

- `WithMCPServer`, `WithMCPServers`, `WithSharedMCPRegistry`, `WithMCPLifecycleHandler`, `WithDeferredSchemas` options.
- `(*LLMAgent).MCP()` method and the `MCPAccessor` marker interface.
- `MCPRegistry`, `MCPController`, `MCPServerConfig`, `StdioMCPConfig`, `HTTPMCPConfig`, `MCPToolFilter`, `MCPServerStatus`, `MCPServerState`, `MCPServerInfo`, `MCPLifecycleHandler`, `NoopMCPLifecycle`, `MCPEvent`, `MCPEventType` types — all moved (and renamed without the `MCP` prefix) to `github.com/nevindra/oasis/mcp`.
- `MCPToolResult` and `MCPContent` types — replaced by `mcp.CallToolResult` and `mcp.ContentBlock` directly.
- `MCPServerInfo` is renamed `mcp.ServerMetadata` (to avoid collision with the existing wire-level `mcp.ServerInfo`).
- Re-exported `oasis.Auth` and `oasis.BearerAuth` — use `mcp.Auth` and `mcp.BearerAuth`.
- `DeferOption`, `DeferThreshold`, `DeferAlwaysOn`, `DeferExclude` — moved to `mcp` package; names unchanged.
- `deferredToolsPromptSection()` (was unexported) — now `mcp.DeferredToolsPromptSection()` (exported, user-callable).
```

- [ ] **Step 2: Final acceptance verification — all gates**

Run all of these and confirm each passes:

```bash
# 1. Both modules build.
go build ./...
(cd mcp && go build ./...)

# 2. All tests pass in both modules.
go test ./...
(cd mcp && go test ./...)

# 3. Kernel discipline + per-module independence enforced by CI script.
bash scripts/check-module-deps.sh

# 4. No stray MCP references in root oasis (only legitimate import of the mcp module from non-core code is allowed, but core itself must be clean).
grep -rn "MCP\|Mcp" --include="*.go" . | grep -v "^./mcp/" | grep -v "^./go.work" | grep -v "_test.go" | grep -v "//"
# Expected: zero results (or only doc-comment matches that are intentional).

# 5. The mcp module's example runs.
(cd mcp && go test -run ExampleNewRegistry ./...)

# 6. `go vet` clean in both modules.
go vet ./...
(cd mcp && go vet ./...)

# 7. `gofmt` clean.
test -z "$(gofmt -l . mcp/)"
```

If any gate fails, fix and re-run before committing.

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: changelog entry for mcp module extraction

Documents the breaking API changes, the full rename mapping, and a
side-by-side migration example. Per-extraction CHANGELOG note per
spec §9.4 template.

Spec: docs/superpowers/specs/2026-05-17-microkernel-migration-design.md §9.4, §9.5
EOF
)"
```

---

## Acceptance criteria (final)

1. ✅ `mcp/go.mod` exists, lists `replace github.com/nevindra/oasis => ../`, and the module is in `go.work`.
2. ✅ All root-level `mcp_*.go` files are gone; the root oasis package has zero references to MCP (verified by grep).
3. ✅ The `mcp` module compiles, tests pass, and `bash scripts/check-module-deps.sh` is green for both kernel discipline and mcp's independence.
4. ✅ `mcp/example_test.go` contains a runnable `ExampleNewRegistry` that wires an agent against an `mcptest` fake server.
5. ✅ `mcp/doc.go` documents both layers and shows the typical wiring snippet.
6. ✅ `CHANGELOG.md` has an entry under `[Unreleased]` describing the breaking changes and migration path.
7. ✅ All previously-passing tests still pass (the five core-coupled tests in `agent_mcp_test.go` and `agent_deferred_test.go` are deliberately deleted; their semantics either moved into `mcp/` or no longer exist).
8. ✅ The litmus test from spec §6.2 holds: the loop does not call MCP code in its main path; MCP tools reach the loop only via `WithTools` like any other `AnyTool`.
