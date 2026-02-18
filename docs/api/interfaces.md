# API Reference: Interfaces

All interfaces live in the root `oasis` package (`github.com/nevindra/oasis`).

## Provider

**File:** `provider.go`

```go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ChatWithTools(ctx context.Context, req ChatRequest, tools []ToolDefinition) (ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- string) (ChatResponse, error)
    Name() string
}
```

| Implementation | Constructor |
|----------------|------------|
| `provider/gemini` | `gemini.New(apiKey, model string)` |
| `provider/openaicompat` | `openaicompat.New(apiKey, model, baseURL string)` |

Middleware: `oasis.WithRetry(p Provider, opts ...RetryOption) Provider`

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
    SearchChunks(ctx context.Context, embedding []float32, topK int) ([]ScoredChunk, error)
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

    // Skills
    CreateSkill(ctx context.Context, skill Skill) error
    GetSkill(ctx context.Context, id string) (Skill, error)
    ListSkills(ctx context.Context) ([]Skill, error)
    UpdateSkill(ctx context.Context, skill Skill) error
    DeleteSkill(ctx context.Context, id string) error
    SearchSkills(ctx context.Context, embedding []float32, topK int) ([]ScoredSkill, error)

    // Lifecycle
    Init(ctx context.Context) error
    Close() error
}
```

| Implementation | Constructor |
|----------------|------------|
| `store/sqlite` | `sqlite.New(path string)` |
| `store/libsql` | `libsql.New(url, token string)` |

---

## MemoryStore

**File:** `memory.go`

```go
type MemoryStore interface {
    UpsertFact(ctx context.Context, fact, category string, embedding []float32) error
    SearchFacts(ctx context.Context, embedding []float32, topK int) ([]ScoredFact, error)
    BuildContext(ctx context.Context, queryEmbedding []float32) (string, error)
    DeleteFact(ctx context.Context, factID string) error
    DeleteMatchingFacts(ctx context.Context, pattern string) error
    DecayOldFacts(ctx context.Context) error
    Init(ctx context.Context) error
}
```

| Implementation | Constructor |
|----------------|------------|
| `memory/sqlite` | `memsqlite.New(path string)` |

---

## Frontend

**File:** `frontend.go`

```go
type Frontend interface {
    Poll(ctx context.Context) (<-chan IncomingMessage, error)
    Send(ctx context.Context, chatID string, text string) (string, error)
    Edit(ctx context.Context, chatID string, msgID string, text string) error
    EditFormatted(ctx context.Context, chatID string, msgID string, text string) error
    SendTyping(ctx context.Context, chatID string) error
    DownloadFile(ctx context.Context, fileID string) ([]byte, string, error)
}
```

| Implementation | Constructor |
|----------------|------------|
| `frontend/telegram` | `telegram.New(token string)` |

---

## Tool

**File:** `tool.go`

```go
type Tool interface {
    Definitions() []ToolDefinition
    Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
}
```

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
    ExecuteStream(ctx context.Context, task AgentTask, ch chan<- string) (AgentResult, error)
}
```

Implemented by `LLMAgent` and `Network`.

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

## Ingest Interfaces

**Package:** `github.com/nevindra/oasis/ingest`

```go
type Extractor interface {
    Extract(content []byte) (string, error)
}

type Chunker interface {
    Chunk(text string) []string
}
```
