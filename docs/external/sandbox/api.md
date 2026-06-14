# Sandbox API

Import path: `github.com/nevindra/oasis/sandbox`

---

## Types

### `Sandbox` interface

The main interface your agent works with. Every method maps to a single API call
to the underlying container runtime. All methods are safe for concurrent use.

```go
type Sandbox interface {
    // Shell and code
    Shell(ctx context.Context, req ShellRequest) (ShellResult, error)
    ExecCode(ctx context.Context, req CodeRequest) (CodeResult, error)

    // Files
    ReadFile(ctx context.Context, req ReadFileRequest) (FileContent, error)
    WriteFile(ctx context.Context, req WriteFileRequest) error
    EditFile(ctx context.Context, req EditFileRequest) error
    UploadFile(ctx context.Context, path string, data io.Reader) error
    DownloadFile(ctx context.Context, path string) (io.ReadCloser, error)
    GlobFiles(ctx context.Context, req GlobRequest) (GlobResult, error)
    GrepFiles(ctx context.Context, req GrepRequest) (GrepResult, error)
    Tree(ctx context.Context, req TreeRequest) (TreeResult, error)

    // Web and MCP
    HTTPFetch(ctx context.Context, req HTTPFetchRequest) (HTTPFetchResult, error)
    WebSearch(ctx context.Context, req WebSearchRequest) (WebSearchResult, error)
    MCPCall(ctx context.Context, req MCPRequest) (MCPResult, error)
    WorkspaceInfo(ctx context.Context) (WorkspaceInfoResult, error)

    // Lifecycle
    Close() error
}
```

The nine browser methods (`BrowserNavigate`, `BrowserScreenshot`, `BrowserAction`,
`BrowserSnapshot`, `BrowserText`, `BrowserPDF`, `BrowserEval`, `BrowserFind`,
`BrowserWait`) are declared on the **optional** `BrowserSandbox` interface, not on
`Sandbox`. `Tools()` checks for `BrowserSandbox` via a type assertion; if the
implementation satisfies it, the `browser_*` tools are registered. Headless or
lightweight implementations can satisfy `Sandbox` alone without stubbing browser
methods.

```go
type BrowserSandbox interface {
    BrowserNavigate(ctx context.Context, url string) error
    BrowserScreenshot(ctx context.Context) ([]byte, error)
    BrowserAction(ctx context.Context, action BrowserAction) (BrowserResult, error)
    BrowserSnapshot(ctx context.Context, opts SnapshotOpts) (PageSnapshot, error)
    BrowserText(ctx context.Context, opts TextOpts) (BrowserTextResult, error)
    BrowserPDF(ctx context.Context) ([]byte, error)
    BrowserEval(ctx context.Context, expression string) (string, error)
    BrowserFind(ctx context.Context, query string) (BrowserFindResult, error)
    BrowserWait(ctx context.Context, opts BrowserWaitOpts) (BrowserWaitResult, error)
}
```

`Close` releases resources held by this instance. Container stop/remove is managed
by `Manager`. Safe to call multiple times.

---

### `Manager` interface

Manages container lifecycles. Owned by the platform layer; agents receive `Sandbox`
via dependency injection.

```go
type Manager interface {
    Create(ctx context.Context, opts CreateOpts) (Sandbox, error)
    Get(sessionID string) (Sandbox, error)
    Destroy(ctx context.Context, sessionID string) error
    Shutdown(ctx context.Context) error
    Close() error
}
```

| Method | Contract |
|---|---|
| `Create` | Provisions a container. Blocks until health check passes. Returns `ErrCapacityFull` at the concurrency limit. |
| `Get` | Returns an existing sandbox by session ID. Returns `ErrNotFound` if gone. |
| `Destroy` | Stops and removes a container. Returns `ErrNotFound` if already gone. |
| `Shutdown` | Stops accepting new sandboxes; drains in-flight work; keeps containers for recovery. |
| `Close` | Force-destroys all managed sandboxes and networks. |

---

### `CreateOpts` / `ResourceSpec`

```go
type CreateOpts struct {
    SessionID string            // required — conversation/session identifier
    Image     string            // container image; empty = manager default
    TTL       time.Duration     // sandbox lifetime; 0 = manager default
    Resources ResourceSpec      // resource limits; zero values use defaults
    Env       map[string]string // extra env vars injected into the container
}

type ResourceSpec struct {
    CPU    int   // cores; 0 = 1
    Memory int64 // bytes; 0 = 2 GB
    Disk   int64 // bytes; 0 = 10 GB
}
```

---

## Request and result types

### Shell and code

```go
type ShellRequest struct {
    Command string // required
    Cwd     string // optional working directory
    Timeout int    // seconds; 0 = sandbox default
}
type ShellResult struct {
    Output   string
    ExitCode int
}

type CodeRequest struct {
    Language string // "python", "javascript", "bash", etc.
    Code     string // required
    Timeout  int    // seconds; 0 = sandbox default
}
type CodeResult struct {
    Status string // "ok" or "error"
    Stdout string
    Stderr string
}
```

### Files

| Request type | Key fields | Result type |
|---|---|---|
| `ReadFileRequest` | `Path` (req), `Offset` (line, 0-based), `Limit` (0=2000) | `FileContent{Content, Path, TotalLines}` |
| `WriteFileRequest` | `Path`, `Content` (both required) | `error` |
| `EditFileRequest` | `Path`, `Old` (must appear exactly once), `New` | `error` |
| `GlobRequest` | `Pattern` (e.g., `"**/*.py"`), `Path`, `Exclude`, `Limit` (0=1000) | `GlobResult{Files []string, Truncated bool}` |
| `GrepRequest` | `Pattern` (regex), `Path`, `Glob` (file filter), `Context` (lines), `Limit` (0=100) | `GrepResult{Matches []GrepMatch, Truncated bool}` |
| `TreeRequest` | `Path`, `Depth` (0=3), `Exclude` | `TreeResult{Tree string, Files, Dirs int}` |

`GrepMatch`: `{Path string, Line int, Content string, ContextBefore, ContextAfter []string}`.

### Browser

```go
type BrowserAction struct {
    Type      string // "click","type","scroll","navigate","key","hover","fill","press","select","focus"
    Ref       string // element ref from BrowserSnapshot — preferred over coordinates
    X, Y      int    // pixel coords (fallback for canvas/maps)
    Text      string // text for type/fill; URL for navigate
    Key       string // key name for key/press
    Direction string // scroll: "up","down","left","right"
    Value     string // option value for select
}
type BrowserResult struct{ Success bool; Message string }

type SnapshotOpts struct {
    Filter   string // "interactive" limits to actionable elements only
    Selector string // CSS selector to scope subtree
    Depth    int    // 0 = unlimited
}
type PageSnapshot struct {
    URL   string
    Title string
    Nodes []SnapshotNode
}
type SnapshotNode struct {
    Ref  string // e.g., "e0" — use in BrowserAction.Ref
    Role string // link, button, textbox, heading, …
    Name string // accessible name / visible text
}

type TextOpts struct {
    Raw      bool // true = innerText; false = readability extraction (default)
    MaxChars int  // 0 = unlimited
}
type BrowserTextResult struct {
    URL, Title, Text string
    Truncated        bool
}

type BrowserFindResult struct {
    Ref        string  `json:"best_ref"`
    Confidence string  `json:"confidence"` // "high","medium","low"
    Score      float64 `json:"score"`
}
```

### Web and MCP

| Request type | Key fields | Result type |
|---|---|---|
| `HTTPFetchRequest` | `URL` (req), `Raw` (false=readability), `MaxChars` (0=8000) | `HTTPFetchResult{URL, Title, Content string}` |
| `WebSearchRequest` | `Query` (req), `MaxResults` (0=10) | `WebSearchResult{Query, Results []WebSearchResultItem}` |
| `MCPRequest` | `Server` (MCP server name in container), `Tool`, `Args json.RawMessage` | `MCPResult{Content string, IsError bool}` |

`WebSearchResultItem`: `{Title, URL, Snippet string}`.

`WorkspaceInfoResult`: `{OS, Arch, WorkingDir string; Tools map[string]bool; Browser bool}`.

Note: `HTTPFetch` is a plain GET. Sites with WAF/Cloudflare will block it.
Use `BrowserNavigate` + `BrowserText` as fallback.

---

## Constructors

### `Tools`

```go
func Tools(sb Sandbox, opts ...ToolsOption) []oasis.AnyTool
```

Returns the full set of agent tools backed by `sb`. The 20 tools generated
(when `sb` also satisfies `BrowserSandbox`):
`shell`, `execute_code`, `file_read`, `file_write`, `file_edit`, `file_glob`,
`file_grep`, `file_tree`, `http_fetch`, `workspace_info`, `browser`, `screenshot`,
`mcp_call`, `snapshot`, `page_text`, `export_pdf`, `browser_eval`, `browser_find`,
`browser_wait`, `web_search`. `deliver_file` is added automatically when a writable
mount or `FileDelivery` is configured. Browser tools are omitted when `sb` does not
implement `BrowserSandbox`.

```go
oasis.WithSandbox(sb, sandbox.Tools(sb)...)
```

---

## Options

### `WithMounts`

```go
func WithMounts(specs []MountSpec, manifest *Manifest) ToolsOption
```

Attaches filesystem mount specs. Tool wrappers (`file_write`, `file_edit`) publish
writes to the backend automatically when the path falls under a writable mount.

### `WithFileDelivery` (deprecated)

```go
func WithFileDelivery(fd FileDelivery) ToolsOption
```

Legacy. Adds a `deliver_file` tool via the `FileDelivery` interface. Prefer
`WithMounts` with `MountWriteOnly`.

---

## Mounts

### `MountSpec`

```go
type MountSpec struct {
    Path            string          // absolute sandbox path, e.g., "/workspace/inputs"
    Backend         FilesystemMount
    Mode            MountMode
    PrefetchOnStart bool // copy backend files into sandbox at start (readable modes)
    FlushOnClose    bool // scan and publish at session end (writable modes)
    MirrorDeletes   bool // delete backend entries absent locally (default: false)
    Include         []string // glob patterns; empty = all
    Exclude         []string
}
```

### `MountMode`

| Constant | Flow | Prefetch | Publish |
|---|---|---|---|
| `MountReadOnly` | Host → sandbox | Yes (if `PrefetchOnStart`) | Never |
| `MountWriteOnly` | Sandbox → host | Never | Yes |
| `MountReadWrite` | Bidirectional | Yes (if `PrefetchOnStart`) | Yes |

### `FilesystemMount` interface

Implement to back a mount with any storage system (S3, GCS, local disk, etc.):

```go
type FilesystemMount interface {
    List(ctx context.Context, prefix string) ([]MountEntry, error)
    Open(ctx context.Context, key string) (io.ReadCloser, error)
    // ifVersion: empty = unconditional; non-empty = optimistic precondition.
    // Returns (newVersion, ErrVersionMismatch) on conflict.
    Put(ctx context.Context, key, mimeType string, size int64, data io.Reader, ifVersion string) (string, error)
    Delete(ctx context.Context, key string, ifVersion string) error
    Stat(ctx context.Context, key string) (MountEntry, error)
}

type MountEntry struct {
    Key      string
    Size     int64
    MimeType string
    Version  string    // etag / generation / etc.
    Modified time.Time
}
```

### `Manifest`

```go
func NewManifest() *Manifest

func (m *Manifest) Record(mountPath, key string, entry MountEntry)
func (m *Manifest) Version(mountPath, key string) (string, bool)
func (m *Manifest) Lookup(mountPath, key string) (MountEntry, bool)
func (m *Manifest) Forget(mountPath, key string)
func (m *Manifest) Keys(mountPath string) []string
```

Safe for concurrent use. Tracks the backend version of each prefetched file
so writes use the correct precondition.

---

## Lifecycle helpers

### `PrefetchMounts`

```go
func PrefetchMounts(ctx context.Context, sb Sandbox, specs []MountSpec, manifest *Manifest) error
```

Walks every spec with `PrefetchOnStart: true` and copies backend files into the
sandbox. Call once after `Manager.Create`, before the agent runs.
Errors are aggregated; all files are attempted before returning.

### `FlushMounts`

```go
func FlushMounts(ctx context.Context, sb Sandbox, specs []MountSpec, manifest *Manifest) error
```

Walks every spec with `FlushOnClose: true`, scans the sandbox, and publishes
changes. Call before `sb.Close()`. Returns `ErrVersionMismatch` (wrapped) on
optimistic concurrency conflict.

---

## Agent wiring

### `oasis.WithSandbox`

```go
// Re-exported from agent.WithSandbox:
var WithSandbox = agent.WithSandbox

func WithSandbox(sb core.Sandbox, tools ...AnyTool) AgentOption
```

Registers the sandbox and its tools on an agent. Tools are included in every
LLM call for the lifetime of that agent instance.

---

## Errors

| Sentinel | Returned by | Meaning |
|---|---|---|
| `ErrNotFound` | `Manager.Get`, `Manager.Destroy` | No sandbox with that session ID |
| `ErrCapacityFull` | `Manager.Create` | Concurrency limit reached |
| `ErrUnhealthy` | `Manager.Create` | Container failed health check |
| `ErrShuttingDown` | `Manager.Create` | Manager is shutting down |
| `ErrVersionMismatch` | `FilesystemMount.Put`, `Delete` | Optimistic concurrency conflict |
| `ErrKeyNotFound` | `FilesystemMount.Open`, `Stat` | Key not in backend |

`ErrVersionMismatch` is always wrapped in `VersionMismatchError`:

```go
if errors.Is(err, sandbox.ErrVersionMismatch) {
    var vme *sandbox.VersionMismatchError
    errors.As(err, &vme)
    log.Printf("conflict on %s: had %s, backend has %s", vme.Key, vme.Have, vme.Want)
}
```

`vme.Have` is the version the framework held; `vme.Want` is what the backend
reports (empty if the backend does not provide it). `vme.Cause` wraps the
underlying backend error.
