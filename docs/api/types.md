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
    ID           string    `json:"id"`
    Name         string    `json:"name"`
    Description  string    `json:"description"`
    Instructions string    `json:"instructions"`
    Tools        []string  `json:"tools,omitempty"`
    Model        string    `json:"model,omitempty"`
    Embedding    []float32 `json:"-"`
    CreatedAt    int64     `json:"created_at"`
    UpdatedAt    int64     `json:"updated_at"`
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
type ScoredSkill struct { Skill; Score float32 }
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
    Messages       []ChatMessage   `json:"messages"`
    ResponseSchema *ResponseSchema `json:"response_schema,omitempty"`
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
    Attachments []Attachment `json:"attachments,omitempty"`
    ToolCalls   []ToolCall   `json:"tool_calls,omitempty"`
    Usage       Usage        `json:"usage"`
}

type Usage struct {
    InputTokens  int `json:"input_tokens"`
    OutputTokens int `json:"output_tokens"`
}

type ToolDefinition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters"`
}

type ToolResult struct {
    Content string `json:"content"`
    Error   string `json:"error,omitempty"`
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
    Duration time.Duration `json:"duration"` // wall-clock time
}
```

`Steps` is populated by LLMAgent (tool calls), Network (tool + agent delegations), and Workflow (step results). Nil when no tools were called.

Context key constants: `ContextThreadID`, `ContextUserID`, `ContextChatID`

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
