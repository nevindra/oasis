# API Reference: Types

All types live in the root `oasis` package unless noted otherwise.

## Domain Types

**File:** `types.go`

```go
type Document struct {
    ID        string `json:"id"`
    Title     string `json:"title"`
    Source    string `json:"source"`
    Content   string `json:"content"`
    CreatedAt int64  `json:"created_at"`
}

type Chunk struct {
    ID         string    `json:"id"`
    DocumentID string    `json:"document_id"`
    ParentID   string    `json:"parent_id,omitempty"`
    Content    string    `json:"content"`
    ChunkIndex int       `json:"chunk_index"`
    Embedding  []float32 `json:"-"`
}

type Thread struct {
    ID        string            `json:"id"`
    ChatID    string            `json:"chat_id"`
    Title     string            `json:"title,omitempty"`
    Metadata  map[string]string `json:"metadata,omitempty"`
    CreatedAt int64             `json:"created_at"`
    UpdatedAt int64             `json:"updated_at"`
}

type Message struct {
    ID        string         `json:"id"`
    ThreadID  string         `json:"thread_id"`
    Role      string         `json:"role"`       // "user" or "assistant"
    Content   string         `json:"content"`
    Metadata  map[string]any `json:"metadata,omitempty"`
    Embedding []float32      `json:"-"`
    CreatedAt int64          `json:"created_at"`
}

type Fact struct {
    ID         string    `json:"id"`
    Fact       string    `json:"fact"`
    Category   string    `json:"category"`
    Confidence float64   `json:"confidence"`
    Embedding  []float32 `json:"-"`
    CreatedAt  int64     `json:"created_at"`
    UpdatedAt  int64     `json:"updated_at"`
}

type ScheduledAction struct {
    ID              string `json:"id"`
    Description     string `json:"description"`
    Schedule        string `json:"schedule"`          // "HH:MM <recurrence>"
    ToolCalls       string `json:"tool_calls"`         // JSON array
    SynthesisPrompt string `json:"synthesis_prompt"`
    NextRun         int64  `json:"next_run"`
    Enabled         bool   `json:"enabled"`
    SkillID         string `json:"skill_id,omitempty"`
    CreatedAt       int64  `json:"created_at"`
}

type Skill struct {
    Name          string            `json:"name"`
    Description   string            `json:"description"`
    Instructions  string            `json:"instructions"`
    Tools         []string          `json:"tools,omitempty"`
    Model         string            `json:"model,omitempty"`
    Tags          []string          `json:"tags,omitempty"`
    References    []string          `json:"references,omitempty"`
    Dir           string            `json:"-"`
    Compatibility string            `json:"compatibility,omitempty"`
    License       string            `json:"license,omitempty"`
    Metadata      map[string]string `json:"metadata,omitempty"`
}

type SkillSummary struct {
    Name          string   `json:"name"`
    Description   string   `json:"description"`
    Tags          []string `json:"tags,omitempty"`
    Compatibility string   `json:"compatibility,omitempty"`
}
```

## Chunk Filter Types

**File:** `types.go`

```go
type FilterOp int

const (
    OpEq FilterOp = iota // exact match
    OpIn                  // value in set
    OpGt                  // greater than
    OpLt                  // less than
)

type ChunkFilter struct {
    Field string
    Op    FilterOp
    Value any
}
```

`ChunkFilter` restricts which chunks are considered during vector and keyword search. Pass to `Store.SearchChunks`, `KeywordSearcher.SearchChunksKeyword`, or `HybridRetriever` via `WithFilters`. See [Store: Chunk Filtering](../concepts/store.md#chunk-filtering) for usage examples and backend details.

## Ingest Checkpoint Types

**File:** `ingest_checkpoint.go`

```go
// IngestCheckpoint records the state of an in-progress document ingestion.
// Written after each pipeline stage; deleted on successful completion.
type IngestCheckpoint struct {
    ID              string           `json:"id"`      // UUIDv7
    Type            string           `json:"type"`    // "document", "batch", or "crossdoc"
    Source          string           `json:"source"`  // filename or source URL
    Status          CheckpointStatus `json:"status"`

    // Document ID assigned after StoreDocument succeeds — used on resume to
    // avoid creating orphan duplicates.
    DocumentID      string `json:"document_id,omitempty"`

    // Populated after extraction — persisted so the pipeline can skip re-extraction on resume.
    ContentType     string `json:"content_type,omitempty"`
    ExtractedText   string `json:"extracted_text,omitempty"`

    // Serialised chunks — populated after chunking is complete.
    ChunksJSON      string `json:"chunks_json,omitempty"`

    // Number of embedding batches already written into ChunksJSON.
    // Resume skips the first EmbeddedBatches * batchSize chunks.
    EmbeddedBatches int    `json:"embedded_batches,omitempty"`

    // Serialised []PageMeta from the extractor.
    PageMetaJSON    string `json:"page_meta_json,omitempty"`

    // Type-specific payload (e.g. completed document IDs for batch checkpoints).
    BatchData       string `json:"batch_data,omitempty"`

    CreatedAt int64 `json:"created_at"`
    UpdatedAt int64 `json:"updated_at"`
}

type CheckpointStatus string

const (
    CheckpointExtracting CheckpointStatus = "extracting"
    CheckpointChunking   CheckpointStatus = "chunking"
    CheckpointEnriching  CheckpointStatus = "enriching"
    CheckpointEmbedding  CheckpointStatus = "embedding"
    CheckpointStoring    CheckpointStatus = "storing"
    CheckpointGraphing   CheckpointStatus = "graphing"
)
```

`Type` distinguishes the three checkpoint varieties:

| Type | Created by | Resumed by |
|------|-----------|------------|
| `"document"` | `IngestFile`, `IngestText` | `ResumeIngest` |
| `"batch"` | `IngestBatch` | `ResumeBatch` |
| `"crossdoc"` | `ExtractCrossDocumentEdges` (with `CrossDocWithResume`) | `ResumeCrossDocExtraction` |

## Ingest Batch Types

**Package:** `github.com/nevindra/oasis/ingest`
**File:** `ingest/batch.go`

```go
// BatchItem is a single document submitted for batch ingestion.
type BatchItem struct {
    Data     []byte // raw file content
    Filename string // used for content-type detection and as source
    Title    string // optional; defaults to Filename when empty
}

// BatchResult is the outcome of IngestBatch or ResumeBatch.
type BatchResult struct {
    Succeeded  []IngestResult // documents that completed successfully
    Failed     []BatchError   // documents that failed after all retries
    Checkpoint string         // non-empty when any documents failed or the batch was interrupted; pass to ResumeBatch
}

// BatchError pairs a failed BatchItem with the error that caused the failure.
type BatchError struct {
    Item  BatchItem
    Error error
}
```

## Ingest Types

**Package:** `github.com/nevindra/oasis/ingest`

```go
type EmbedFunc func(ctx context.Context, texts []string) ([][]float32, error)
```

`EmbedFunc` matches the `EmbeddingProvider.Embed` method signature, so `embedding.Embed` can be passed directly to `NewSemanticChunker`.

## Retrieval Types

**File:** `retriever.go`

```go
type RetrievalResult struct {
    Content        string  `json:"content"`
    Score          float32 `json:"score"`
    ChunkID        string  `json:"chunk_id"`
    DocumentID     string  `json:"document_id"`
    DocumentTitle  string  `json:"document_title"`
    DocumentSource string  `json:"document_source"`
}
```

Score is in [0, 1]; higher means more relevant. Exact range depends on scoring method (cosine similarity, RRF, or reranker output).

## Scored Types (search results)

```go
type ScoredMessage struct { Message; Score float32 }
type ScoredChunk struct { Chunk; Score float32 }
type ScoredFact struct { Fact; Score float32 }
```

Score is in [0, 1]. Score 0 means "relevance unknown" (e.g., ANN index).

## LLM Protocol Types

**File:** `types.go`

```go
type ChatMessage struct {
    Role        string          `json:"role"`         // "system", "user", "assistant", "tool"
    Content     string          `json:"content"`
    Attachments []Attachment    `json:"attachments,omitempty"`
    ToolCalls   []ToolCall      `json:"tool_calls,omitempty"`
    ToolCallID  string          `json:"tool_call_id,omitempty"`
    Metadata    json.RawMessage `json:"metadata,omitempty"`
}

type Attachment struct {
    MimeType string `json:"mime_type"`
    URL      string `json:"url,omitempty"`
    Data     []byte `json:"-"`

    // Deprecated: use Data for inline bytes or URL for remote references.
    Base64 string `json:"base64,omitempty"`
}

// InlineData returns raw bytes from whichever inline source is populated.
// Priority: Data > Base64 (decoded). Returns nil if only URL is set.
func (a Attachment) InlineData() []byte

// HasInlineData reports whether inline bytes are available (Data or Base64).
func (a Attachment) HasInlineData() bool

type ToolCall struct {
    ID       string          `json:"id"`
    Name     string          `json:"name"`
    Args     json.RawMessage `json:"args"`
    Metadata json.RawMessage `json:"metadata,omitempty"`
}

type ChatRequest struct {
    Messages         []ChatMessage    `json:"messages"`
    ResponseSchema   *ResponseSchema  `json:"response_schema,omitempty"`
    GenerationParams *GenerationParams `json:"generation_params,omitempty"`
}

// GenerationParams controls LLM generation behavior.
// All fields are pointers — nil means "use provider default".
// A Temperature of 0.0 is valid, so nil (not zero) signals "unset".
type GenerationParams struct {
    Temperature *float64 `json:"temperature,omitempty"`
    TopP        *float64 `json:"top_p,omitempty"`
    TopK        *int     `json:"top_k,omitempty"`
    MaxTokens   *int     `json:"max_tokens,omitempty"`
}

type ResponseSchema struct {
    Name   string          `json:"name"`
    Schema json.RawMessage `json:"schema"`
}

type SchemaObject struct {
    Type        string                   `json:"type"`
    Description string                   `json:"description,omitempty"`
    Properties  map[string]*SchemaObject `json:"properties,omitempty"`
    Items       *SchemaObject            `json:"items,omitempty"`
    Enum        []string                 `json:"enum,omitempty"`
    Required    []string                 `json:"required,omitempty"`
}

func NewResponseSchema(name string, s *SchemaObject) *ResponseSchema

type ChatResponse struct {
    Content     string       `json:"content"`
    Thinking    string       `json:"thinking,omitempty"`
    Attachments []Attachment `json:"attachments,omitempty"`
    ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
    Usage       Usage        `json:"usage"`
}

type Usage struct {
    InputTokens  int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
    CachedTokens int `json:"cached_tokens,omitempty"` // input tokens served from provider cache
}

type ToolDefinition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters"`
}

type ToolResult struct {
    Content     string       `json:"content"`
    Error       string       `json:"error,omitempty"`
    Attachments []Attachment `json:"attachments,omitempty"` // multimodal content (images, PDFs, etc.) passed to the LLM
}
```

## Code Execution Types

**File:** `code.go`

```go
type CodeRequest struct {
    Code    string        `json:"code"`    // source code to execute
    Timeout time.Duration `json:"-"`       // max duration (zero = runner default)
}

type CodeResult struct {
    Output   string `json:"output"`          // structured result via set_result()
    Logs     string `json:"logs,omitempty"`   // print() output and stderr
    ExitCode int    `json:"exit_code"`        // process exit code (0 = success)
    Error    string `json:"error,omitempty"`  // execution failure description
}
```

`CodeRequest.Code` is the source code written by the LLM. `CodeResult.Output` contains the JSON-serialized data passed to `set_result()` in Python. `Logs` captures `print()` output (redirected to stderr by the prelude).

## Sandbox Types

**Package:** `github.com/nevindra/oasis/sandbox`

```go
// EditFileRequest is the input for EditFile.
type EditFileRequest struct {
    Path string // file to edit
    Old  string // exact string to find (must be unique in file)
    New  string // replacement string
}

// GlobRequest is the input for GlobFiles.
type GlobRequest struct {
    Pattern string // glob pattern (e.g., "**/*.py")
    Path    string // base directory; empty uses working directory
}

// GrepRequest is the input for GrepFiles.
type GrepRequest struct {
    Pattern string // regex pattern
    Path    string // base directory or file path
    Glob    string // optional file filter (e.g., "*.py")
}

// GrepMatch is a single search result from GrepFiles.
type GrepMatch struct {
    Path    string // file path
    Line    int    // line number (1-indexed)
    Content string // matching line content
}
```

## Filesystem Mount Types

**Package:** `github.com/nevindra/oasis/sandbox`

```go
// MountMode declares the direction of data flow for a FilesystemMount.
type MountMode int

const (
    MountReadOnly  MountMode = iota // host → sandbox (prefetch only)
    MountWriteOnly                  // sandbox → host (publish only)
    MountReadWrite                  // bidirectional
)

// Predicates used by the framework and tool wrappers.
func (m MountMode) Readable() bool { /* ... */ }
func (m MountMode) Writable() bool { /* ... */ }

// MountSpec attaches a FilesystemMount to a path inside the sandbox and
// declares the lifecycle policy for that mount.
type MountSpec struct {
    Path            string          // absolute path inside sandbox (e.g. "/workspace/inputs")
    Backend         FilesystemMount // implementation that owns the data
    Mode            MountMode       // ReadOnly | WriteOnly | ReadWrite
    PrefetchOnStart bool            // copy backend → sandbox at start
    FlushOnClose    bool            // scan sandbox → publish deltas at close
    MirrorDeletes   bool            // delete backend entries removed locally; default false
    Include         []string        // optional include globs
    Exclude         []string        // glob patterns to skip (e.g. "*.tmp")
}

// MountEntry describes a single file in a FilesystemMount.
type MountEntry struct {
    Key      string    // logical key relative to the mount root
    Size     int64     // bytes
    MimeType string    // best-effort
    Version  string    // backend version token (etag, generation, etc.)
    Modified time.Time // backend modification timestamp
}
```

`Mode` is independent of `PrefetchOnStart` and `FlushOnClose`. The boolean flags are authoritative for lifecycle hook gating; `Mode` is consulted by tool wrappers when deciding whether to publish on write. The two together let apps express asymmetric lifecycle policies (e.g. read at start, write at close, or vice versa).

### Manifest

```go
// Manifest tracks the backend version of every file the framework has
// prefetched into a sandbox. It is used by Layer 2 (tool interception)
// and Layer 3 (lifecycle flush) to send the correct precondition on
// writes back to the backend. Safe for concurrent use.
type Manifest struct{ /* ... */ }

func NewManifest() *Manifest

func (m *Manifest) Record(mountPath, key string, entry MountEntry)
func (m *Manifest) Version(mountPath, key string) (string, bool)
func (m *Manifest) Lookup(mountPath, key string) (MountEntry, bool)
func (m *Manifest) Forget(mountPath, key string)
func (m *Manifest) Keys(mountPath string) []string
```

The same `*Manifest` instance is shared between `PrefetchMounts`, `WithMounts(specs, manifest)`, and `FlushMounts` so that all three layers see the same per-sandbox version state.

### Lifecycle Functions

```go
// PrefetchMounts walks every readable mount with PrefetchOnStart=true and
// copies its backend entries into the sandbox via Sandbox.UploadFile.
// Records each fetched file's version in manifest. Errors are aggregated.
func PrefetchMounts(ctx context.Context, sb Sandbox, specs []MountSpec, manifest *Manifest) error

// FlushMounts walks every writeable mount with FlushOnClose=true, scans
// the sandbox under the mount path via GlobFiles, and publishes any
// deltas to the backend with the manifest version as precondition.
// Conflicts return wrapped ErrVersionMismatch.
func FlushMounts(ctx context.Context, sb Sandbox, specs []MountSpec, manifest *Manifest) error
```

See [Sandbox concept doc](../concepts/sandbox.md#filesystem-mounts) for the layered architecture and the failure-mode table.

## Dynamic Config Function Types

**File:** `agent.go`

```go
// PromptFunc resolves the system prompt per-request.
type PromptFunc func(ctx context.Context, task AgentTask) string

// ModelFunc resolves the LLM provider per-request.
type ModelFunc func(ctx context.Context, task AgentTask) Provider

// ToolsFunc resolves the tool set per-request.
type ToolsFunc func(ctx context.Context, task AgentTask) []Tool
```

Set via `WithDynamicPrompt`, `WithDynamicModel`, `WithDynamicTools`. Called at the start of every `Execute`/`ExecuteStream` call. Dynamic values override their static counterparts.

## Agent Types

**File:** `agent.go`

```go
type AgentTask struct {
    Input       string
    Attachments []Attachment
    Context     map[string]any
}

type AgentResult struct {
    Output      string
    Thinking    string        // last LLM reasoning/chain-of-thought before final response
    Attachments []Attachment
    Usage       Usage
    Steps       []StepTrace   // per-step execution trace, chronological order
}

type StepTrace struct {
    Name     string        `json:"name"`     // tool or agent name
    Type     string        `json:"type"`     // "tool", "agent", or "step" (workflow)
    Input    string        `json:"input"`    // args/task, truncated to 200 chars
    Output   string        `json:"output"`   // result, truncated to 500 chars
    Usage    Usage         `json:"usage"`    // tokens for this step
    Duration time.Duration `json:"duration"` // wall-clock time (see note below)
}
```

`Steps` is populated by LLMAgent (tool calls), Network (tool + agent delegations), and Workflow (step results). Nil when no tools were called.

**StepTrace fields:**

| Field | Type | Description |
|-------|------|-------------|
| `Name` | `string` | Tool or agent name. For agent delegations, the `agent_` prefix is stripped |
| `Type` | `string` | `"tool"` (tool call), `"agent"` (network delegation), or `"step"` (workflow) |
| `Input` | `string` | Tool arguments JSON or agent task text, truncated to 200 characters |
| `Output` | `string` | Result content, truncated to 500 characters |
| `Usage` | `Usage` | Token counts (`InputTokens`, `OutputTokens`) for this individual step |
| `Duration` | `time.Duration` | Wall-clock time. **Serializes as nanoseconds** in JSON (Go `time.Duration` default). Divide by `1_000_000` for milliseconds |

Builder methods: `task.WithThreadID()`, `task.WithUserID()`, `task.WithChatID()`

Typed accessors: `task.TaskThreadID()`, `task.TaskUserID()`, `task.TaskChatID()`

## InputHandler Types

**File:** `input.go`

```go
type InputRequest struct {
    Question string
    Options  []string
    Metadata map[string]string
}

type InputResponse struct {
    Value string
}
```

## Workflow Types

**File:** `workflow.go`

```go
type StepFunc func(ctx context.Context, wCtx *WorkflowContext) error

type StepStatus string  // "pending", "running", "success", "skipped", "failed"

type StepResult struct {
    Name     string
    Status   StepStatus
    Output   string
    Error    error
    Duration time.Duration
}

type WorkflowResult struct {
    Status  StepStatus
    Steps   map[string]StepResult
    Context *WorkflowContext
    Usage   Usage
}

type WorkflowError struct {
    StepName string
    Err      error
    Result   WorkflowResult
}
```

## Runtime Workflow Definition Types

**File:** `types.go`

```go
type NodeType string

const (
    NodeLLM       NodeType = "llm"       // delegates to a registered Agent
    NodeTool      NodeType = "tool"      // calls a registered Tool
    NodeCondition NodeType = "condition" // evaluates expression, routes to branches
    NodeTemplate  NodeType = "template"  // performs string interpolation
)

type WorkflowDefinition struct {
    Name        string           `json:"name"`
    Description string           `json:"description"`
    Nodes       []NodeDefinition `json:"nodes"`
    Edges       [][2]string      `json:"edges"` // [from, to] pairs
}

type NodeDefinition struct {
    ID          string         `json:"id"`
    Type        NodeType       `json:"type"`

    // LLM node
    Agent       string         `json:"agent,omitempty"`
    Input       string         `json:"input,omitempty"`       // template: "Summarize: {{search.output}}"

    // Tool node
    Tool        string         `json:"tool,omitempty"`
    ToolName    string         `json:"tool_name,omitempty"`
    Args        map[string]any `json:"args,omitempty"`        // values may contain {{key}} templates

    // Condition node
    Expression  string         `json:"expression,omitempty"`  // "{{score}} >= 0.8" or registered function name
    TrueBranch  []string       `json:"true_branch,omitempty"`
    FalseBranch []string       `json:"false_branch,omitempty"`

    // Template node
    Template    string         `json:"template,omitempty"`

    // Common
    OutputTo    string         `json:"output_to,omitempty"`   // override default output key
    Retry       int            `json:"retry,omitempty"`
}

type DefinitionRegistry struct {
    Agents     map[string]Agent                         // for "llm" nodes
    Tools      map[string]Tool                          // for "tool" nodes
    Conditions map[string]func(*WorkflowContext) bool   // escape hatch for complex logic
}
```

`WorkflowDefinition` is JSON-serializable. Pass to `FromDefinition` with a `DefinitionRegistry` to produce an executable `*Workflow`.

`DefinitionRegistry` maps string names in the definition to concrete Go objects. The `Conditions` map is optional — use it when condition logic can't be expressed as a simple comparison expression.

## AgentHandle Types

**File:** `handle.go`

```go
type AgentState int32  // StatePending, StateRunning, StateCompleted, StateFailed, StateCancelled
```

## Batch Types

**File:** `batch.go`

```go
type BatchState string

const (
    BatchPending   BatchState = "pending"
    BatchRunning   BatchState = "running"
    BatchSucceeded BatchState = "succeeded"
    BatchFailed    BatchState = "failed"
    BatchCancelled BatchState = "cancelled"
    BatchExpired   BatchState = "expired"
)

type BatchStats struct {
    TotalCount     int `json:"total_count"`
    SucceededCount int `json:"succeeded_count"`
    FailedCount    int `json:"failed_count"`
}

type BatchJob struct {
    ID          string     `json:"id"`
    State       BatchState `json:"state"`
    DisplayName string     `json:"display_name,omitempty"`
    Stats       BatchStats `json:"stats"`
    CreateTime  time.Time  `json:"create_time"`
    UpdateTime  time.Time  `json:"update_time"`
}
```

`BatchJob` is provider-agnostic — each provider maps its native state to `BatchState` constants.

## Memory Types

**File:** `memory.go`

```go
type ExtractedFact struct {
    Fact       string  `json:"fact"`
    Category   string  `json:"category"`
    Supersedes *string `json:"supersedes,omitempty"`
}
```

## Model Catalog Types

**File:** `catalog.go`

### Protocol

```go
type Protocol int

const (
    ProtocolOpenAICompat Protocol = iota // OpenAI-compatible /v1/models + /v1/chat/completions
    ProtocolGemini                        // Google Generative Language API
)
```

### Platform

```go
type Platform struct {
    Name     string   // "OpenAI", "Gemini", "Qwen", etc.
    Protocol Protocol // how to list models and create providers
    BaseURL  string   // default API endpoint
    EnvVars  []string // standard env var names for API key (e.g., ["OPENAI_API_KEY"])
}
```

### ModelInfo

Normalized metadata for a single model, aggregated from static registry data and live provider APIs.

```go
type ModelInfo struct {
    ID          string // "qwen-turbo", "gemini-2.5-flash"
    Provider    string // identifier used in catalog (e.g., "openai", "qwen")
    DisplayName string // human-friendly name (empty if unavailable)

    Family string // model series: "gpt-4", "claude-3", "gemini-2.5" (empty if unknown)

    InputContext  int // max input tokens (0 = unknown)
    OutputContext int // max output tokens (0 = unknown)

    InputModalities  []string // e.g., ["text", "image", "audio", "pdf", "video"]
    OutputModalities []string // e.g., ["text", "image", "audio"]

    Capabilities ModelCapabilities

    OpenWeights     bool   // true for open-source/open-weights models
    KnowledgeCutoff string // "2024-10" format (empty if unknown)
    ReleaseDate     string // "2024-05-13" format (empty if unknown)

    Status         ModelStatus
    Deprecated     bool
    DeprecationMsg string

    Pricing *ModelPricing // nil if provider doesn't expose it
}
```

| Field | Zero value meaning |
|-------|-------------------|
| `InputContext` | 0 = unknown (not "zero tokens") |
| `OutputContext` | 0 = unknown |
| `DisplayName` | Empty = provider API didn't return one; use `ID` as fallback |
| `Family` | Empty = series could not be determined |
| `InputModalities` / `OutputModalities` | nil = unknown (not "text only") |
| `KnowledgeCutoff` / `ReleaseDate` | Empty = unknown |
| `Pricing` | nil = provider doesn't expose pricing (different from "free") |

### ModelCapabilities

```go
type ModelCapabilities struct {
    Chat             bool
    Vision           bool
    ToolUse          bool
    Embedding        bool
    Reasoning        bool // thinking/chain-of-thought (o3, deepseek-r1, claude thinking)
    StructuredOutput bool // JSON mode / structured output support
    Attachment       bool // file/media upload support
}
```

### ModelPricing

Shared with the `observer` package for cost calculation.

```go
type ModelPricing struct {
    InputPerMillion      float64 // USD per 1M input tokens
    OutputPerMillion     float64 // USD per 1M output tokens
    CacheReadPerMillion  float64 // USD per 1M cached input tokens (0 = no cache pricing)
    CacheWritePerMillion float64 // USD per 1M cache write tokens (0 = no cache pricing)
}
```

### ModelStatus

```go
type ModelStatus int

const (
    ModelStatusUnknown     ModelStatus = iota // no live data
    ModelStatusAvailable                       // confirmed by live API
    ModelStatusUnavailable                     // in static but not in live API
)
```

### ParseModelID

```go
func ParseModelID(id string) (provider, model string)
```

Splits a `"provider/model"` string. Returns empty strings if format is invalid.

```go
oasis.ParseModelID("openai/gpt-4o")       // → "openai", "gpt-4o"
oasis.ParseModelID("together/meta-llama/Llama-3.1-70B")  // → "together", "meta-llama/Llama-3.1-70B"
oasis.ParseModelID("invalid")             // → "", ""
```

## Compaction Types

**File:** `compaction.go`

### CompactRequest

Input to a single `Compactor.Compact` call.

```go
type CompactRequest struct {
    Messages           []ChatMessage    // input messages to summarize; caller decides partitioning
    SummarizerProvider Provider         // per-call override; nil → use Compactor's configured default
    FocusHint          string           // optional user directive (e.g., "focus on layout decisions")
    IsRecompact        bool             // input already contains a prior compact; preserve it by reference
    OutputBudget       int              // max_tokens cap for the summary; 0 → implementation default (20_000)
    ExtraSections      []CompactSection // appended after the default 9 sections (e.g., "Active Skills")
}
```

### CompactSection

Domain-specific summary section appended to the default 9.

```go
type CompactSection struct {
    Title        string // section heading (also used as the key in CompactResult.Sections)
    Instructions string // prose directed at the summarizer for what to capture
}
```

### CompactResult

Output of a `Compactor.Compact` call.

```go
type CompactResult struct {
    SummaryText      string            // the full structured summary (<analysis> already stripped)
    Sections         map[string]string // parsed per-section body, keyed by section title
    SourceTokens     int               // estimated input token count
    SummaryTokens    int               // provider-reported output tokens (falls back to heuristic)
    CompressionRatio float64           // SummaryTokens / SourceTokens; 0 when source is empty
    SourceMessageIDs []string          // IDs of summarized messages — currently always nil (see note)
    PersistsTable    []string          // UI transparency: categories the summary preserves
    LostTable        []string          // UI transparency: categories the summary intentionally drops
    Warnings         []string          // non-fatal diagnostics (e.g., "summary_truncated_at_budget", "partial_sections")
}
```

`SourceMessageIDs` is currently always nil — Oasis `ChatMessage` does not yet carry per-message IDs. Future schema changes may populate it.

`Warnings` is non-empty when the result is usable but imperfect. Surface it in UI — the summary is still the source of truth.

See [`Compactor`](interfaces.md#compactor) for the interface and [Compaction Errors](errors.md#compaction-errors) for failure modes.
