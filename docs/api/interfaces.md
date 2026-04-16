# API Reference: Interfaces

All interfaces live in the root `oasis` package (`github.com/nevindra/oasis`).

## Provider

**File:** `provider.go`

```go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- StreamEvent) (ChatResponse, error)
    Name() string
}
```

Two methods handle all interaction patterns:

| Method | When it's used |
|--------|---------------|
| `Chat` | Blocking request/response. When `req.Tools` is non-empty, the response may contain `ToolCalls` |
| `ChatStream` | Like Chat but emits `StreamEvent` values into `ch` as content is generated. When `req.Tools` is non-empty, emits `EventToolCallDelta` events. The channel is NOT closed by the provider — the caller owns its lifecycle |

Tools are passed via `ChatRequest.Tools` — no separate method needed.

| Implementation | Constructor |
|----------------|------------|
| `provider/gemini` | `gemini.New(apiKey, model string)` |
| `provider/openaicompat` | `openaicompat.NewProvider(apiKey, model, baseURL string)` |
| `provider/resolve` | `resolve.Provider(cfg resolve.Config)` — config-driven, returns any of the above |

Middleware:
- `oasis.WithRetry(p Provider, opts ...RetryOption) Provider`
- `oasis.WithRateLimit(p Provider, opts ...RateLimitOption) Provider`

---

## EmbeddingProvider

**File:** `provider.go`

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
    Name() string
}
```

| Implementation | Constructor |
|----------------|------------|
| `provider/gemini` | `gemini.NewEmbedding(apiKey, model string, dimensions int)` |
| `provider/openaicompat` | `openaicompat.NewEmbedding(apiKey, model, baseURL string, dims int)` |
| `provider/resolve` | `resolve.EmbeddingProvider(cfg resolve.EmbeddingConfig)` — config-driven |

---

## MultimodalEmbeddingProvider

**File:** `types.go`

Optional capability for embedding multimodal inputs (text + images) into the same vector space. Discovered via type assertion. Enables cross-modal retrieval — text queries finding images.

```go
type MultimodalInput struct {
    Text        string
    Attachments []Attachment
}

type MultimodalEmbeddingProvider interface {
    EmbedMultimodal(ctx context.Context, inputs []MultimodalInput) ([][]float32, error)
}
```

| Implementation | Constructor |
|----------------|------------|
| `provider/openaicompat` | `openaicompat.NewEmbedding(apiKey, model, baseURL string, dims int)` |

---

## Compactor

**File:** `compaction.go`

```go
type Compactor interface {
    Compact(ctx context.Context, req CompactRequest) (CompactResult, error)
}
```

Turns a message list into a structured summary via an LLM call. Implementations
MUST be safe to call concurrently — the agent loop may invoke `Compact` from
background goroutines while a foreground response is being assembled.

| Implementation | Constructor |
|----------------|------------|
| `oasis.StructuredCompactor` | `NewStructuredCompactor(defaultProvider Provider) *StructuredCompactor` — 9-section LLM-based summarizer (default) |

See [`CompactRequest`](types.md#compactrequest) and [`CompactResult`](types.md#compactresult) for the input/output shapes, and [Compactor in concepts](../concepts/compaction.md) for the rationale.

---

## BlobStore

**File:** `types.go`

Interface for external binary object storage. Used by the ingest pipeline to store image bytes externally instead of inline in chunk metadata.

```go
type BlobStore interface {
    StoreBlob(ctx context.Context, key string, data []byte, mimeType string) (ref string, err error)
    GetBlob(ctx context.Context, ref string) (data []byte, mimeType string, err error)
    DeleteBlob(ctx context.Context, ref string) error
}
```

---

## BatchProvider

**File:** `batch.go`

Optional capability for asynchronous batch chat processing. Discovered via type assertion.

```go
type BatchProvider interface {
    BatchChat(ctx context.Context, requests []ChatRequest) (BatchJob, error)
    BatchStatus(ctx context.Context, jobID string) (BatchJob, error)
    BatchChatResults(ctx context.Context, jobID string) ([]ChatResponse, error)
    BatchCancel(ctx context.Context, jobID string) error
}
```

| Implementation    | Constructor                 |
|-------------------|-----------------------------|
| `provider/gemini` | `gemini.New(apiKey, model)` |

---

## BatchEmbeddingProvider

**File:** `batch.go`

Optional capability for asynchronous batch embedding. Discovered via type assertion.

```go
type BatchEmbeddingProvider interface {
    BatchEmbed(ctx context.Context, texts [][]string) (BatchJob, error)
    BatchEmbedStatus(ctx context.Context, jobID string) (BatchJob, error)
    BatchEmbedResults(ctx context.Context, jobID string) ([][]float32, error)
}
```

| Implementation    | Constructor                                           |
|-------------------|-------------------------------------------------------|
| `provider/gemini` | `gemini.NewEmbedding(apiKey, model string, dims int)` |

---

## Store

**File:** `store.go`

```go
type Store interface {
    // Threads
    CreateThread(ctx context.Context, thread Thread) error
    GetThread(ctx context.Context, id string) (Thread, error)
    ListThreads(ctx context.Context, chatID string, limit int) ([]Thread, error)
    UpdateThread(ctx context.Context, thread Thread) error
    DeleteThread(ctx context.Context, id string) error

    // Messages
    StoreMessage(ctx context.Context, msg Message) error
    GetMessages(ctx context.Context, threadID string, limit int) ([]Message, error)
    SearchMessages(ctx context.Context, embedding []float32, topK int) ([]ScoredMessage, error)

    // Documents + Chunks
    StoreDocument(ctx context.Context, doc Document, chunks []Chunk) error
    SearchChunks(ctx context.Context, embedding []float32, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
    GetChunksByIDs(ctx context.Context, ids []string) ([]Chunk, error)

    // Config
    GetConfig(ctx context.Context, key string) (string, error)
    SetConfig(ctx context.Context, key, value string) error

    // Scheduled Actions
    CreateScheduledAction(ctx context.Context, action ScheduledAction) error
    ListScheduledActions(ctx context.Context) ([]ScheduledAction, error)
    GetDueScheduledActions(ctx context.Context, now int64) ([]ScheduledAction, error)
    UpdateScheduledAction(ctx context.Context, action ScheduledAction) error
    UpdateScheduledActionEnabled(ctx context.Context, id string, enabled bool) error
    DeleteScheduledAction(ctx context.Context, id string) error
    DeleteAllScheduledActions(ctx context.Context) (int, error)
    FindScheduledActionsByDescription(ctx context.Context, pattern string) ([]ScheduledAction, error)

    // Lifecycle
    Init(ctx context.Context) error
    Close() error
}
```

| Implementation | Constructor |
|----------------|------------|
| `store/sqlite` | `sqlite.New(path string)` |

---

## SkillProvider

**File:** `skills.go`

Read-only access to the skill library. Used for discovery and activation.

```go
type SkillProvider interface {
    // Discover returns lightweight summaries of all available skills.
    // Returns names and descriptions only — no instruction text loaded.
    Discover(ctx context.Context) ([]SkillSummary, error)

    // Activate loads the full Skill by name, including instructions.
    Activate(ctx context.Context, name string) (Skill, error)
}
```

| Implementation | Constructor |
|----------------|------------|
| `skills.FileSkillProvider` | `skills.NewFileSkillProvider(dir string)` |

---

## SkillWriter

**File:** `skills.go`

Write access to the skill library. Used for agent self-improvement — creating and updating skills at runtime.

```go
type SkillWriter interface {
    // Create writes a new skill to the backing store.
    // Returns an error if a skill with the same name already exists.
    Create(ctx context.Context, skill Skill) error

    // Update replaces an existing skill's fields.
    // The skill must already exist (identified by Name).
    Update(ctx context.Context, skill Skill) error

    // Delete removes a skill by name.
    Delete(ctx context.Context, name string) error
}
```

`FileSkillProvider` implements both `SkillProvider` and `SkillWriter`. The skill tool accepts them separately so you can compose a read-only provider with a restricted writer, or use the same instance for both.

| Implementation | Constructor |
|----------------|------------|
| `skills.FileSkillProvider` | `skills.NewFileSkillProvider(dir string)` |

---

## MemoryStore

**File:** `memory.go`

```go
type MemoryStore interface {
    UpsertFact(ctx context.Context, fact, category string, embedding []float32) error
    SearchFacts(ctx context.Context, embedding []float32, topK int) ([]ScoredFact, error)
    BuildContext(ctx context.Context, queryEmbedding []float32) (string, error)
    DeleteFact(ctx context.Context, factID string) error
    // pattern is a plain substring match — never SQL LIKE or regex.
    DeleteMatchingFacts(ctx context.Context, pattern string) error
    DecayOldFacts(ctx context.Context) error
    Init(ctx context.Context) error
}
```

| Implementation | Constructor |
|----------------|------------|
| `store/sqlite` | `sqlite.NewMemoryStore(db *sql.DB)` |
| `store/postgres` | `postgres.NewMemoryStore(pool *pgxpool.Pool)` |

---

## Tool

**File:** `tool.go`

```go
type Tool interface {
    Definitions() []ToolDefinition
    Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
}
```

---

## StreamingTool

**File:** `types.go`

Optional capability for tools that support progress streaming during execution. Discovered via type assertion.

```go
type StreamingTool interface {
    Tool
    ExecuteStream(ctx context.Context, name string, args json.RawMessage, ch chan<- StreamEvent) (ToolResult, error)
}
```

Tools emit `EventToolProgress` events on `ch` to report intermediate progress. The channel is shared with the parent agent's stream — events appear inline with other agent events. The channel is NOT closed by the tool — the caller owns its lifecycle.

When a tool implements `StreamingTool` and the agent is streaming, `ExecuteStream` is called instead of `Execute`. Tools that only implement `Tool` work unchanged — the framework falls back to `Execute`.

| Implementation | Constructor | Functions |
|----------------|------------|-----------|
| `tools/knowledge` | `knowledge.New(store, embedding)` | `knowledge_search` |
| `tools/remember` | `remember.New(store, embedding)` | `remember` |
| `tools/search` | `search.New(embedding, braveKey)` | `web_search` |
| `tools/schedule` | `schedule.New(store, tzOffset)` | `schedule_create`, `schedule_list`, `schedule_update`, `schedule_delete` |
| `tools/shell` | `shell.New(workspacePath, timeoutSecs)` | `shell_exec` |
| `tools/file` | `file.New(workspacePath)` | `file_read`, `file_write`, `file_list` |
| `tools/http` | `http.New()` | `http_fetch` |

---

## Agent

**File:** `agent.go`

```go
type Agent interface {
    Name() string
    Description() string
    Execute(ctx context.Context, task AgentTask) (AgentResult, error)
}
```

| Implementation | Constructor |
|----------------|------------|
| `LLMAgent` | `NewLLMAgent(name, desc string, provider Provider, opts ...AgentOption)` |
| `Network` | `NewNetwork(name, desc string, router Provider, opts ...AgentOption)` |
| `Workflow` | `NewWorkflow(name, desc string, opts ...WorkflowOption) (*Workflow, error)` |

---

## StreamingAgent

**File:** `agent.go`

```go
type StreamingAgent interface {
    Agent
    ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error)
}
```

Implemented by `LLMAgent`, `Network`, and `Workflow`.

---

## InputHandler

**File:** `input.go`

```go
type InputHandler interface {
    RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}
```

---

## Processors

**File:** `processor.go`

```go
type PreProcessor interface {
    PreLLM(ctx context.Context, req *ChatRequest) error
}

type PostProcessor interface {
    PostLLM(ctx context.Context, resp *ChatResponse) error
}

type PostToolProcessor interface {
    PostTool(ctx context.Context, call ToolCall, result *ToolResult) error
}
```

---

## Retriever

**File:** `retriever.go`

```go
type Retriever interface {
    Retrieve(ctx context.Context, query string, topK int) ([]RetrievalResult, error)
}
```

| Implementation | Constructor |
|----------------|------------|
| `HybridRetriever` | `oasis.NewHybridRetriever(store, embedding, opts ...RetrieverOption)` |

---

## Reranker

**File:** `retriever.go`

```go
type Reranker interface {
    Rerank(ctx context.Context, query string, results []RetrievalResult, topK int) ([]RetrievalResult, error)
}
```

| Implementation | Constructor |
|----------------|------------|
| `ScoreReranker` | `oasis.NewScoreReranker(minScore float32)` |
| `LLMReranker` | `oasis.NewLLMReranker(provider Provider)` |

---

## KeywordSearcher

**File:** `retriever.go`

Optional Store capability for full-text keyword search. Discovered via type assertion.

```go
type KeywordSearcher interface {
    SearchChunksKeyword(ctx context.Context, query string, topK int, filters ...ChunkFilter) ([]ScoredChunk, error)
}
```

Implemented by `store/sqlite` (FTS5) and `store/postgres` (GIN/tsvector).

---

## CheckpointStore

**File:** `retriever.go`

Optional Store capability for ingest pipeline checkpoint persistence. Discovered via type assertion. When a Store implements `CheckpointStore`, the ingest pipeline automatically saves progress after each stage and can resume crashed ingestions via `ResumeIngest`, `ResumeBatch`, and `ResumeCrossDocExtraction`. If the Store does not implement this interface, checkpointing is silently disabled — retry still works, but crashes cannot be resumed.

```go
type CheckpointStore interface {
    SaveCheckpoint(ctx context.Context, cp IngestCheckpoint) error
    LoadCheckpoint(ctx context.Context, id string) (IngestCheckpoint, error)
    DeleteCheckpoint(ctx context.Context, id string) error
    ListCheckpoints(ctx context.Context) ([]IngestCheckpoint, error)
}
```

Checkpoints are stored as JSON blobs in the `ingest_checkpoints` table. The table is created by `Store.Init()` and is non-breaking — existing databases are upgraded automatically on the next `Init()` call.

| Implementation | Supported |
|----------------|-----------|
| `store/sqlite` | Yes |
| `store/postgres` | Yes |

---

## DocumentChunkLister

**Package:** `github.com/nevindra/oasis/ingest`
**File:** `ingest/crossdoc.go`

Optional Store capability required for cross-document edge extraction. Discovered via type assertion by `ExtractCrossDocumentEdges`.

```go
type DocumentChunkLister interface {
    GetChunksByDocument(ctx context.Context, docID string) ([]oasis.Chunk, error)
}
```

---

## Sandbox

**Package:** `github.com/nevindra/oasis/sandbox`

```go
type Sandbox interface {
    Shell(ctx context.Context, req ShellRequest) (ShellResult, error)
    ExecCode(ctx context.Context, req CodeRequest) (CodeResult, error)
    ReadFile(ctx context.Context, path string) (FileContent, error)
    WriteFile(ctx context.Context, req WriteFileRequest) error
    UploadFile(ctx context.Context, path string, r io.Reader) error
    DownloadFile(ctx context.Context, path string) (io.ReadCloser, error)
    BrowserNavigate(ctx context.Context, url string) error
    BrowserScreenshot(ctx context.Context) ([]byte, error)
    BrowserAction(ctx context.Context, action BrowserAction) (BrowserResult, error)
    MCPCall(ctx context.Context, req MCPRequest) (MCPResult, error)
    EditFile(ctx context.Context, req EditFileRequest) error
    GlobFiles(ctx context.Context, req GlobRequest) ([]string, error)
    GrepFiles(ctx context.Context, req GrepRequest) ([]GrepMatch, error)
    Close() error
}
```

| Method | Description |
|--------|-------------|
| `EditFile` | Performs a surgical string replacement in a file. `old` must exist exactly once in the file. Returns an error if `old` is not found or appears more than once |
| `GlobFiles` | Finds files matching a glob pattern relative to a base directory |
| `GrepFiles` | Searches file contents for a regex pattern and returns matches with file path, line number, and matching line content |

| Implementation | Constructor |
|----------------|------------|
| `sandbox/ix` | `ix.NewManager(ctx, ix.ManagerConfig{...})` then `mgr.Create(ctx, sandbox.CreateOpts{...})` |

---

## Manager

**Package:** `github.com/nevindra/oasis/sandbox`

```go
type Manager interface {
    Create(ctx context.Context, opts CreateOpts) (Sandbox, error)
    Get(ctx context.Context, sessionID string) (Sandbox, error)
    Shutdown(ctx context.Context) error
    Close() error
}
```

| Implementation | Constructor |
|----------------|------------|
| `sandbox/ix` | `ix.NewManager(ctx, ix.ManagerConfig{...})` |

---

## FilesystemMount

**Package:** `github.com/nevindra/oasis/sandbox`

```go
type FilesystemMount interface {
    List(ctx context.Context, prefix string) ([]MountEntry, error)
    Open(ctx context.Context, key string) (io.ReadCloser, error)
    Put(ctx context.Context, key, mimeType string, size int64, data io.Reader, ifVersion string) (newVersion string, err error)
    Delete(ctx context.Context, key, ifVersion string) error
    Stat(ctx context.Context, key string) (MountEntry, error)
}
```

Abstracts a key-value-ish file storage backend that can back a path inside a sandbox via a `MountSpec`. All keys are logical (relative to the mount root); the framework joins them with `MountSpec.Path` to form absolute sandbox paths.

| Method | Description |
|--------|-------------|
| `List` | Returns the entries under the given prefix (relative to the mount root). Empty prefix lists everything |
| `Open` | Returns a reader for the file at `key`. Returns an error wrapping `ErrKeyNotFound` if absent |
| `Put` | Writes data with optional `ifVersion` precondition (empty = unconditional). Returns the new version assigned by the backend. Conflicts return wrapped `ErrVersionMismatch` |
| `Delete` | Removes the file. `ifVersion` is honored the same way as `Put` |
| `Stat` | Returns metadata for a single file. Returns an error wrapping `ErrKeyNotFound` if absent |

See [Sandbox concept doc](../concepts/sandbox.md#filesystem-mounts) for the full mechanism (prefetch, tool interception, flush) and [Errors](errors.md) for `ErrVersionMismatch` / `ErrKeyNotFound` semantics.

---

## FilesystemMounter

**Package:** `github.com/nevindra/oasis/sandbox`

```go
type FilesystemMounter interface {
    MountFilesystem(ctx context.Context, spec MountSpec) error
    UnmountFilesystem(ctx context.Context, path string) error
}
```

OPTIONAL capability that a `Sandbox` implementation MAY expose to indicate it can perform live, transparent mounting of a `FilesystemMount` into the running container (e.g. via FUSE, virtio-fs, NFS). When a sandbox satisfies this interface, the framework prefers the mounter over the default Layer 2 + Layer 3 publish/flush path for the specific mount.

No sandbox runtime ships with this capability today. The interface exists so that adding live mounting later does not require changes to the framework or applications using mounts.

---

## FileDelivery (deprecated)

**Package:** `github.com/nevindra/oasis/sandbox`

```go
// Deprecated: Use FilesystemMount with MountWriteOnly mode instead.
type FileDelivery interface {
    Deliver(ctx context.Context, name, mimeType string, size int64, data io.Reader) (url string, err error)
}
```

Original one-shot push interface for sending sandbox files to the host. Still supported for backward compatibility — when passed to `WithFileDelivery`, it acts as a fallback inside `deliver_file` for paths that fall under no mount. New code should use `FilesystemMount`.

---

## DispatchResult

**File:** `loop.go`

```go
type DispatchResult struct {
    Content     string
    Usage       Usage
    Attachments []Attachment
    IsError     bool
}
```

Holds the result of a single tool or agent dispatch. `Content` is the text output, `Usage` tracks token consumption, `Attachments` carries binary data (e.g. images from sub-agent image generation), and `IsError` signals whether `Content` represents an error message rather than successful tool output — enabling structural error detection without string-prefix heuristics.

---

## DispatchFunc

**File:** `loop.go`

```go
type DispatchFunc func(ctx context.Context, tc ToolCall) DispatchResult
```

Bridges code execution back to the agent's tool registry. Provided by `LLMAgent` and `Network` — not constructed directly. Returns a `DispatchResult` containing the output text, token usage, and any attachments generated by the dispatched tool or sub-agent.

---

## Tracer

**File:** `tracer.go`

```go
type Tracer interface {
    Start(ctx context.Context, name string, attrs ...SpanAttr) (context.Context, Span)
}
```

| Implementation | Constructor |
|----------------|------------|
| `observer` (OTEL-backed) | `observer.NewTracer()` |

---

## Span

**File:** `tracer.go`

```go
type Span interface {
    SetAttr(attrs ...SpanAttr)
    Event(name string, attrs ...SpanAttr)
    Error(err error)
    End()
}
```

Attribute helpers:

```go
oasis.StringAttr(k, v string) SpanAttr
oasis.IntAttr(k string, v int) SpanAttr
oasis.BoolAttr(k string, v bool) SpanAttr
oasis.Float64Attr(k string, v float64) SpanAttr
```

---

## Ingest Interfaces

**Package:** `github.com/nevindra/oasis/ingest`

```go
type Extractor interface {
    Extract(content []byte) (string, error)
}

type Chunker interface {
    Chunk(text string) []string
}

type ContextChunker interface {
    Chunker
    ChunkContext(ctx context.Context, text string) ([]string, error)
}
```

`ContextChunker` extends `Chunker` for implementations that call external services (e.g., embedding APIs). The `Ingestor` discovers this via type assertion and calls `ChunkContext` when available. `SemanticChunker` implements both interfaces.

| Implementation | Constructor |
| --- | --- |
| `RecursiveChunker` | `ingest.NewRecursiveChunker(opts ...ChunkerOption)` |
| `MarkdownChunker` | `ingest.NewMarkdownChunker(opts ...ChunkerOption)` |
| `SemanticChunker` | `ingest.NewSemanticChunker(embed EmbedFunc, opts ...ChunkerOption)` |
