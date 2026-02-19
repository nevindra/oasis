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
    ID        string    `json:"id"`
    ThreadID  string    `json:"thread_id"`
    Role      string    `json:"role"`       // "user" or "assistant"
    Content   string    `json:"content"`
    Embedding []float32 `json:"-"`
    CreatedAt int64     `json:"created_at"`
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
    Base64   string `json:"base64"`
}

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

type ChatResponse struct {
    Content   string     `json:"content"`
    ToolCalls []ToolCall `json:"tool_calls,omitempty"`
    Usage     Usage      `json:"usage"`
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

## Frontend Types

```go
type IncomingMessage struct {
    ID, ChatID, UserID, Text, ReplyToMsgID string
    Document *FileInfo
    Photos   []FileInfo
    Caption  string
}

type FileInfo struct {
    FileID, FileName, MimeType string
    FileSize int64
}
```

## Agent Types

**File:** `agent.go`

```go
type AgentTask struct {
    Input       string
    Attachments []Attachment
    Context     map[string]any
}

type AgentResult struct {
    Output string
    Usage  Usage
}
```

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
