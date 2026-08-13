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

### `FileHasher` (optional capability)

A `Sandbox` implementation MAY additionally implement `FileHasher` to compute
content hashes **inside the guest**, so the host can learn whether a file
changed without moving its bytes. Detected by type assertion, the same shape
`BrowserSandbox` and `TransactionalMount` use — an implementation that does not
provide it degrades to downloading and hashing on the host.

```go
type FileHasher interface {
    HashFiles(ctx context.Context, paths []string) ([]FileHash, error)
}

type FileHash struct {
    Path   string // absolute sandbox path, exactly as passed to HashFiles
    Digest string // lower-case hex sha256, computed in the guest
    Size   int64  // byte length the digest covers
}

func AsFileHasher(sb Sandbox) (FileHasher, bool)
```

`HashFiles` is **best-effort**. A path that was deleted between enumeration and
read, replaced by a directory, or was never readable is **omitted from the
result** — it is not an error and it is not a zero-value entry. Match results by
`Path` rather than by position, and read a missing path as *unknown*, never as
*unchanged*. A returned error means the call as a whole failed and none of it is
usable.

`ErrHashUnsupported` means the call can never succeed on this sandbox — an older
guest, or a runtime that never implemented the operation. It is an error rather
than a missing method because of `Lazy`: a lazy sandbox cannot know what its
inner sandbox supports until something forces it to be created, so it advertises
the capability and answers honestly at call time. `AsFileHasher` returning
`true` therefore means the call is *available*, not that it will succeed, and a
caller that treats `ErrHashUnsupported` exactly like a transport failure — by
falling back to reading the bytes — is correct without ever testing for it.

The digest is the same scheme the framework hashes with on the host, and the
same one a content-addressed backend reports as `MountEntry.Version`, so a guest
digest, a host digest and a stored version are directly comparable.

Two callers inside the package: `NewStatHashDetector` (one round trip per 256
candidate files) and `FlushMounts` (one round trip per mount, and the unchanged
files are then never downloaded).

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
| `GlobRequest` | `Pattern` (e.g., `"**/*.py"`), `Path`, `Exclude`, `Limit` (0=1000) | `GlobResult{Files []string, Truncated bool, Entries []GlobEntry}` |
| `GrepRequest` | `Pattern` (regex), `Path`, `Glob` (file filter), `Context` (lines), `Limit` (0=100) | `GrepResult{Matches []GrepMatch, Truncated bool}` |
| `TreeRequest` | `Path`, `Depth` (0=3), `Exclude` | `TreeResult{Tree string, Files, Dirs int}` |

`GrepMatch`: `{Path string, Line int, Content string, ContextBefore, ContextAfter []string}`.

#### `GlobEntry` — per-file metadata on a glob

```go
type GlobEntry struct {
    Path    string    // absolute sandbox path; how an entry is matched back to Files
    Size    int64     // byte length
    ModTime time.Time // finest resolution the guest could report; zero when it reported none
}
```

`GlobResult.Entries` is **additive**: a guest that predates the metadata
protocol returns `Files` with `Entries` nil, and every caller that only reads
`Files` keeps working. The two slices are **not index-aligned**, and `Entries`
may be shorter — a path the guest could not stat is still a path it found, so it
stays in `Files` with no entry. Match on `Path`, and read a missing entry as
*unknown* rather than *unchanged*.

It exists so a caller can decide a file is unchanged without moving its bytes. A
differing `Size` proves the content changed; nothing here proves it did not.
Stat is a filter that eliminates candidates cheaply — only a hash answers the
question, which is what [`FileHasher`](#filehasher-optional-capability) is for.

**The mtime precision rule.** A **non-zero nanosecond component** on `ModTime`
is proof that the guest's clock and filesystem carried sub-second precision for
that file, so an equal `ModTime` is then strong evidence. A **whole-second**
mtime is ambiguous — a genuine whole-second timestamp is indistinguishable from
a truncated one — and an agent's write-run-rewrite loop puts two versions of a
file inside the same second routinely, so two writes in one second at the same
byte length would read as an "unchanged" file. A caller that cares about
correctness must treat a whole-second mtime as a **hash candidate**, not as
unchanged. `GlobEntry.ModTime`'s doc comment carries the full reasoning.

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

Returns the full set of agent tools backed by `sb`. The 12 tools generated
(when `sb` also satisfies `BrowserSandbox`):
`shell`, `execute_code`, `file_read`, `file_write`, `file_edit`, `file_search`
(one tool with `target: content|files|tree` for grep/glob/tree), `http_fetch`,
`workspace_info`, `browser` (interaction actions plus `eval`/`find`/`wait`),
`browser_read` (one tool with `action: screenshot|snapshot|text|pdf`),
`mcp_call`, `web_search`. `deliver_file` is added automatically when a writable
mount or `FileDelivery` is configured. Browser tools are omitted when `sb` does not
implement `BrowserSandbox`.

`deliver_file` attaches a file to the conversation as a download and **does not
save it**. Persistence is the mount's: Layer 2 for `file_write`/`file_edit`,
`WithToolCallCommits` for what a command wrote, `FlushMounts` at close. It still
requires a destination — a writable mount covering the path, or a
`FileDelivery` — because the url it emits (`<mount root>/<key>`) has to name a
file the host can serve; a path under no mount is an error. The `FileDelivery`
path is the one place it still hands over bytes, because such a host has no
mount to have stored them.

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

### `WithToolCallCommits`

```go
func WithToolCallCommits(d ChangeDetector) ToolsOption
```

Ends every tool call that can write to the sandbox — `shell`, `execute_code`,
`mcp_call` — with a commit of whatever it changed, through
`TransactionalMount`. Without it, anything a *command* writes is invisible to
the backend until `FlushMounts` runs at close, so a VM that dies mid-turn takes
the work with it.

**Pass `NewStatHashDetector`.** Its cost per tool call is one glob round trip
plus one hash round trip per 256 candidate files, and no file bodies move except
for what actually changed. `NewFullScanDetector` is also accepted and downloads
the entire mount on every tool call — correct, and affordable only for a
workspace of a few small documents.

**Off by default**, because a host has to decide that its backends are
transactional and that its workspaces are the shape this suits, and because
committing more often is a change in observable behaviour: every commit is a
version, and a host that surfaces version history will surface more of it. With
the option unset the tool layer runs exactly the code it ran before this existed
— writes reach the backend through Layer 2 tool interception and the close-time
flush, as before. A nil detector leaves it off.

Skipped entirely for mounts that are not writable and for backends that do not
implement `TransactionalMount`; those keep the `Put`/prefetch/flush path.
`FlushOnClose` is not consulted — this is Layer 2 generalised, so `Mode` is
what decides. The file tools are not wrapped: each writes the one path it was
given and publishes it through Layer 2 as it goes, so committing after them
would write the same bytes twice.

Per commit: only files the mount owns (deepest mount wins, `Include`/`Exclude`
applied), one precondition per key — `ExpectVersion` for a key the manifest
tracks, `ExpectAbsent` for a new file on a prefetched mount, `ExpectAny` on a
mount the framework has never read. Deletions are never mirrored mid-turn;
that stays a `FlushMounts` decision under `MirrorDeletes`. A rejected commit
applies nothing and tells the detector nothing, so the same files are reported
again by the next scan; it notes itself in the tool result without failing the
tool call. What the rejection *revealed* is recorded — see [Adopting the
versions a rejection reveals](#adopting-the-versions-a-rejection-reveals).

#### What a rejected commit tells the model

A rejection is appended to the tool result as a note; the tool call itself
still reports success, because a model told its `shell` call failed re-runs it
and re-running a build or a migration is worse than a failed save.

For a **conflict** the note names every conflicted file by its *sandbox path*,
labels it `CHANGED`, `DELETED` or `ALREADY EXISTS`, inlines the current stored
content read back through `FilesystemMount.Open`, and instructs the model to
re-read and re-apply before writing again. The content is in the message
because it is the only place the model can see it: the copy inside the sandbox
is the agent's own uncommitted write, so `file_read` on that path returns what
the agent wrote, not what the other writer stored.

Bounds — a commit can carry a whole workspace and one file can be 50 MB:

| Bound | Value | Beyond it |
|---|---|---|
| Files with content inlined | 3 | named only |
| Files named at all | 10 | reported as a count |
| Content per file | 4 KB | cut, and said to be cut |
| Non-UTF-8 content, or content containing NUL | — | never inlined; the note says it is binary and points at re-reading the file |

The 4 KB bound is applied at the read, so the backend is never asked to ship a
whole large file. A readback that fails degrades to naming the conflict without
the content.

A commit failure that is **not** a conflict — the backend is down, a file the
guest could not serve — gets a different message and no re-read instruction:
nothing changed underneath the model and there is nothing to re-read.

#### Adopting the versions a rejection reveals

A rejected commit updates the manifest with what the rejection reported, so a
retry can succeed. Without that the loop cannot close: the manifest keeps
asserting the version the framework already knows is stale, the model re-reads,
merges and writes again, and the framework sends the same dead precondition,
which the backend rejects for the same reason, forever — the tool result tells
the model to fix something only the framework can fix.

Adopting a version is **not** adopting content. The file inside the sandbox is
still the model's own version; only the framework's belief about what the backend
currently holds moves forward. The next commit therefore says "I know this key is
at v2" and writes the model's bytes over it — an *informed* overwrite, and
informed is the whole promise, because the note above already showed the model
what the file says now, named it, and told it to merge before retrying.

The protection stays live: a third writer landing between the rejection and the
retry moves the key to v3, and the retry — now asserting v2 — is rejected again.
What is given up is that nothing *forces* the model to read what it was shown.
That is the trade every re-read-then-write loop makes, and the alternative is not
a stricter guarantee but a loop that never terminates. The note still tells the
model to stop and escalate if the same file is rejected twice.

Three outcomes, three answers, per conflicted key:

| What the rejection says | What the manifest does |
|---|---|
| `Absent` — the key is gone | forgets it, so the next attempt claims the name is free, which is now true |
| `Want` set — the ordinary case | records that version; no round trip |
| `Want` empty and the key present — a refused `ExpectAbsent` | asks the backend with `Stat`, since that is the only way to learn a version; bounded to 32 such calls per rejection, and a backend that will not answer leaves the key rejected rather than getting an invented version |

An entry adopted from `Want` carries the version and nothing else, on purpose:
the `Size` and `Modified` the manifest held before the conflict describe content
the key no longer holds, and a stale size is worse than an absent one, because
the change detector reads an equal size as evidence of equal content. The `Stat`
case records whatever the backend just reported, which is current by
construction.

### `ChangeDetector` / `NewStatHashDetector` / `NewFullScanDetector`

```go
type ChangeDetector interface {
    Scan(ctx context.Context, sb Sandbox, scan ChangeScan) ([]ChangedFile, error)
    Committed(files []ChangedFile)
    Published(path string, content []byte)
}

type ChangeScan struct {
    Root     string                              // absolute mount root in the sandbox
    Owns     func(path string) bool              // deepest-mount rule + Include/Exclude
    Baseline func(path string) (MountEntry, bool) // what the backend is believed to hold
}

type ChangedFile struct {
    Path    string // absolute sandbox path
    Size    int64
    Digest  string // detector's own content identity; opaque to the caller
    Content []byte // set when the detector had to read the bytes; nil otherwise
}

func NewStatHashDetector() ChangeDetector
func NewFullScanDetector() ChangeDetector
```

The seam between "what changed?" and "commit it". It is an interface because how
expensively that question can be answered depends on what the sandbox runtime
underneath is willing to tell you, and the commit logic must not change when a
runtime gets better at it. Implementations must be safe for concurrent use — a
host may dispatch tool calls in parallel.

**`NewStatHashDetector` is the one to use.** It globs the mount root, discards
every file whose `GlobEntry` proves it was not touched, has the guest hash what
is left, and transfers **no file bodies at all**; the commit path then downloads
only the files it is about to stage. One glob round trip plus one hash round trip
per 256 candidates. The saving is not a constant factor: a tool call that writes
one file in a hundred-file workspace costs one glob and one hash of one file,
where a full scan costs the same glob plus a hundred downloads and the whole
workspace over vsock.

It uses two optional pieces of the guest protocol and degrades cleanly without
either:

| Missing | What happens |
|---|---|
| `GlobResult.Entries` | every owned file becomes a hash candidate — still no downloads, just more hashing |
| `FileHasher` — absent, `ErrHashUnsupported`, or any error at all | that batch is downloaded and hashed on the host, which is exactly what `NewFullScanDetector` does and no worse |

With neither, it behaves as a full scan. It is therefore always safe to use, and
it gets faster as the runtime underneath improves without anything having to be
reconfigured.

**The one thing it cannot see.** A rewrite that preserves *both* the byte length
and the mtime — `cp -p`, `shutil.copy2`, `touch -r`, a tar extraction that
restores timestamps — is indistinguishable from no write at all, and stays
invisible to every scan in the turn. This is the same trade rsync, make and
git's stat cache make, for the same reason: the alternative is hashing every
byte of the workspace after every tool call. It costs **latency, never data** —
the close-time flush compares content, not metadata, so a change hidden from
every scan is still published when the turn ends; it lands late rather than
mid-turn, and nothing downstream treats a late commit differently. The same
reasoning is in `statProvesUnchanged`'s doc comment, alongside the mtime
precision rule described under [`GlobEntry`](#globentry--per-file-metadata-on-a-glob):
a whole-second mtime is never allowed to settle the question on its own.

`NewFullScanDetector` globs the mount root, downloads every file the scan owns
and hashes it on the host: one glob round trip plus one download per owned file,
moving the full byte size of the mount whether or not anything changed. It asks
the guest for nothing beyond glob and download, so it remains the detector for a
runtime that reports neither metadata nor hashes — and it is what the other
degrades into, per batch, when the guest cannot hash.

`ChangedFile.Digest` is the detector's own currency: the commit path never
interprets it, it only carries the value back through `Committed` so the
detector can recognise its own work. Two detectors need not agree on the scheme,
and one must not be swapped for another mid-session.

**Still missing:** there is no way to ask a backend "do you already hold the blob
with this hash?", which is what would remove the last transfer — a file the agent
rewrote to content the backend has seen before is still uploaded. Neither
`FilesystemMount` nor `TransactionalMount` can answer it, and adding a method to
either would break every backend implementing it, so it belongs in a separate
optional capability whenever a backend exists that can answer.

`Published` is how the framework tells a detector about its own Layer 2 writes
(`file_write`, `file_edit`, saved browser captures) so the next scan does not
commit them a second time. `deliver_file` is deliberately **not** on that list:
it publishes nothing, so telling the detector otherwise would make the next
scan skip the file the user was just shown. `Committed` is the only other way a
detector's baseline advances — a scan whose commit was rejected reports the
same files again next call.

### `WithFileDelivery` (deprecated)

```go
func WithFileDelivery(fd FileDelivery) ToolsOption
```

Legacy. Adds a `deliver_file` tool via the `FileDelivery` interface. Prefer
`WithMounts` with `MountWriteOnly`.

It is also the one destination `deliver_file` still writes to. On a mount the
tool is attach-only, because the mount persists what the agent wrote; a
`FileDelivery` host has no mount, so `Deliver` is both the store and the source
of the url. Reached only when no writable mount covers the path — and never for
a path a read-only mount owns.

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

**`Include` / `Exclude` pattern language.** The patterns are
[doublestar](https://github.com/bmatcuk/doublestar) globs, which is the shell's
language: `*` matches within a single path segment and `**` spans segments, so
`node_modules/**` excludes everything below `node_modules/` however deep, and
`**/__pycache__/**` excludes the contents of a `__pycache__` directory at any
depth including the root. Each pattern is tested against the full mount key *and*
against its basename, which is what lets `*.tmp` match `sub/dir/file.tmp` — the
short form means the obvious thing, and mounts in the wild are configured that
way.
`Include` is applied first (empty means all), `Exclude` after it. The same pass
governs prefetch, the close-time flush, and per-tool-call commits.

A malformed pattern matches **nothing**, and specifically never everything. The
dangerous direction is `Exclude`: one that matched everything would swallow the
mount and silently drop every file the caller asked to have published, where
matching nothing costs an over-eager flush of the files the typo meant to skip —
visible in the backend, and recoverable. A malformed `Include` therefore lets
nothing through, so an empty mount points at the pattern instead of looking like
a mount that was never filtered.

### `MountMode`

| Constant | Flow | Prefetch | Publish |
|---|---|---|---|
| `MountReadOnly` | Host → sandbox | Yes (if `PrefetchOnStart`) | Never — writes are **refused** |
| `MountWriteOnly` | Sandbox → host | Never | Yes |
| `MountReadWrite` | Bidirectional | Yes (if `PrefetchOnStart`) | Yes |

A `file_write` or `file_edit` aimed at a path under a `MountReadOnly` mount
returns a refusal and does not touch the guest filesystem, rather than writing
locally and quietly dropping the publish. An agent that believes it saved a
document the user then finds unchanged is worse than a tool call that failed
loudly. Writes the framework cannot intercept — a shell command, a python
script — still land in the guest's own filesystem and are still never published.

`deliver_file` refuses such a path too, though it writes nowhere: a file under
a read-only mount is not the run's own output, so attaching it would present
somebody else's document as what the agent just produced, and the url would
name a key the mount never accepted.

Captures that already produced bytes (`browser_read` with a `path`) keep their
result and carry the refusal as a note, so the model is not sent back to redo
work that a retry cannot fix.

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

### `TransactionalMount` (optional capability)

A backend MAY additionally implement `TransactionalMount` so that a change
touching several files lands as one unit and a stale writer is rejected rather
than silently winning. Detected by type assertion — a backend that does not
implement it keeps today's `Put`/prefetch/flush behaviour exactly.

```go
type TransactionalMount interface {
    // Hand over one file's bytes; get back an opaque handle. The content
    // belongs to no key until a commit gives it one.
    StageContent(ctx context.Context, size int64, data io.Reader) (StagedContent, error)
    // Apply every change or none. Rejected commits return *CommitConflictError.
    Commit(ctx context.Context, changes []MountChange) (CommitResult, error)
}

type MountChange struct {
    Key      string             // logical key, as with Put
    Op       ChangeOp           // OpPut (zero value) | OpDelete
    Content  StagedContent      // handle from StageContent; required for OpPut
    MimeType string
    Expect   VersionExpectation // ExpectAny (zero value) | ExpectVersion | ExpectAbsent
    Have     string             // the version asserted, for ExpectVersion
}

type CommitResult struct {
    Entries []MountEntry // one per OpPut change, carrying the new version
}

func AsTransactional(backend FilesystemMount) (TransactionalMount, bool)
```

Preconditions are **per key**, not per mount: two agents changing different
files in one workspace are not in conflict, and a mount-wide token would
serialise them. The framework only ever knows a version per `(mount, key)` —
that is what `Manifest` records. A host that wants a mount-wide commit chain
(history, undo-a-turn) keeps it as its own bookkeeping; this interface neither
asks for one nor returns a commit id.

A backend that implements this must guarantee: atomicity per `Commit` call and
nothing across calls; preconditions evaluated against the same state the
changes are applied to (no check-then-apply window); staged content invisible
until committed, its handle still valid after a *failed* commit so a retry
re-stages only the conflicted key, and reclaimed by the backend if never
committed (there is no discard call — the caller can die mid-turn); staging is
not idempotent and handles are opaque; changes within a commit are unordered
and no key may appear twice; returned versions live in the same namespace as
`MountEntry.Version`. Implementations must be safe for concurrent use.

One caller: the per-tool-call commit path, and only when the host opts in with
[`WithToolCallCommits`](#withtoolcallcommits). `PrefetchMounts`, `FlushMounts`
and the Layer 2 tool wrappers go through `Put` as before, whether or not a
backend implements this.

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

A file is skipped only when the manifest's version for the key **is** the sha256
of the bytes now on disk. That is a proof rather than a guess, and only a
content-addressed backend can produce it; an empty version, or one in any other
scheme (an ETag, a generation counter, `"v1"`), proves nothing and the file is
published. The rule is deliberately stricter than the per-tool-call detector's,
because flush has no later pass — a same-length rewrite wrongly skipped here is
never published at all, where the detector's mistake only costs a late commit.

Where the sandbox implements [`FileHasher`](#filehasher-optional-capability),
those hashes come from the guest in one round trip per mount and the unchanged
files are never downloaded at all — on a prefetched workspace that is most of
them, and the transfer is the cost, the `Put` it also avoids being the smaller
half. A guest that cannot hash, or a hash call that fails for any reason, falls
back to downloading every candidate and hashing on the host: same outcome, more
bytes. Ownership and `Include`/`Exclude` are applied *before* the hash request,
so the guest is never asked about the `node_modules` tree the excludes just
removed.

A path covered by a more specific, nested mount (e.g. `/workspace/inputs`
underneath `/workspace`) is skipped here — that nested mount publishes it, if it
has `FlushOnClose` of its own — and `MirrorDeletes` leaves such a path alone
rather than deleting it, since this spec no longer owns it.

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
| `ErrCommitConflict` | `TransactionalMount.Commit` | One or more per-key preconditions failed |
| `ErrHashUnsupported` | `FileHasher.HashFiles` | This sandbox has no hashing operation — an older guest, or a runtime that never implemented one |

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

`ErrCommitConflict` is always wrapped in `CommitConflictError`, which also
matches `ErrVersionMismatch` — a commit conflict *is* a version mismatch, only
plural, so callers that already branch on that sentinel keep working when a
backend gains the transactional capability:

```go
var cc *sandbox.CommitConflictError
if errors.As(err, &cc) {
    for _, c := range cc.Conflicts {
        if c.Absent {
            log.Printf("%s was deleted since you read it", c.Key)
            continue
        }
        log.Printf("%s: you had %s, backend has %s — re-read and retry", c.Key, c.Have, c.Want)
    }
}
```

`Conflicts` always names at least one key and should name every key that
failed. Nothing in a rejected commit was applied. `c.Absent` is the split
between "someone changed this" and "someone deleted this" — a merge versus a
decision — and an empty `c.Have` means the caller asserted `ExpectAbsent` and
the name turned out to be taken. All three are surfaced to the model under
`WithToolCallCommits`; see [What a rejected commit tells the
model](#what-a-rejected-commit-tells-the-model).
