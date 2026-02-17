# API Reference

Complete reference for all Oasis framework interfaces, types, and constructors.

All types and interfaces live in the root `oasis` package (`github.com/nevindra/oasis`) unless noted otherwise.

## Interfaces

### Provider

**File:** `provider.go`

```go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
    ChatWithTools(ctx context.Context, req ChatRequest, tools []ToolDefinition) (ChatResponse, error)
    ChatStream(ctx context.Context, req ChatRequest, ch chan<- string) (ChatResponse, error)
    Name() string
}
```

| Method | Description |
|--------|-------------|
| `Chat` | Send a request, receive a complete response |
| `ChatWithTools` | Send a request with tool definitions. Response may contain `ToolCalls` instead of or alongside `Content` |
| `ChatStream` | Stream tokens into the provided channel. Returns final aggregated response when done. **Must close `ch` when finished.** |
| `Name` | Provider identifier string (e.g. `"gemini"`, `"openai"`) |

**Implementations:**

| Package | Constructor | Notes |
|---------|------------|-------|
| `provider/gemini` | `gemini.New(apiKey, model string) *Gemini` | Also implements `EmbeddingProvider` |
| `provider/openaicompat` | `openaicompat.New(apiKey, model, baseURL string) *Client` | Works with OpenAI, Ollama, and any compatible API |

---

### EmbeddingProvider

**File:** `provider.go`

```go
type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
    Name() string
}
```

| Method | Description |
|--------|-------------|
| `Embed` | Convert texts to embedding vectors. Returns one vector per input text. |
| `Dimensions` | Vector dimensionality (e.g. `1536`) |
| `Name` | Provider identifier |

**Implementations:**

| Package | Constructor |
|---------|------------|
| `provider/gemini` | `gemini.NewEmbedding(apiKey, model string, dimensions int) *GeminiEmbedding` |

---

### Frontend

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

| Method | Description |
|--------|-------------|
| `Poll` | Start long-polling. Returns a channel that emits `IncomingMessage` until `ctx` is cancelled. |
| `Send` | Send a new message. Returns the message ID for later editing. |
| `Edit` | Replace message content with plain text |
| `EditFormatted` | Replace message content with HTML-formatted text |
| `SendTyping` | Show typing/processing indicator |
| `DownloadFile` | Download a file attachment. Returns raw bytes and filename. |

**Implementations:**

| Package | Constructor |
|---------|------------|
| `frontend/telegram` | `telegram.New(token string) *Bot` |

---

### Store

**File:** `store.go`

```go
type Store interface {
    // Messages
    StoreMessage(ctx context.Context, msg Message) error
    GetMessages(ctx context.Context, threadID string, limit int) ([]Message, error)
    SearchMessages(ctx context.Context, embedding []float32, topK int) ([]Message, error)

    // Documents + Chunks
    StoreDocument(ctx context.Context, doc Document, chunks []Chunk) error
    SearchChunks(ctx context.Context, embedding []float32, topK int) ([]Chunk, error)

    // Threads
    CreateThread(ctx context.Context, thread Thread) error
    GetThread(ctx context.Context, id string) (Thread, error)
    ListThreads(ctx context.Context, chatID string, limit int) ([]Thread, error)
    UpdateThread(ctx context.Context, thread Thread) error
    DeleteThread(ctx context.Context, id string) error

    // Key-value config
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
    SearchSkills(ctx context.Context, embedding []float32, topK int) ([]Skill, error)

    // Lifecycle
    Init(ctx context.Context) error
    Close() error
}
```

**Implementations:**

| Package | Constructor | Notes |
|---------|------------|-------|
| `store/sqlite` | `sqlite.New(path string) *Store` | Local SQLite, pure-Go |
| `store/libsql` | `libsql.New(url, token string) *Store` | Remote Turso/libSQL |

---

### MemoryStore

**File:** `memory.go`

```go
type MemoryStore interface {
    UpsertFact(ctx context.Context, fact, category string, embedding []float32) error
    SearchFacts(ctx context.Context, embedding []float32, topK int) ([]Fact, error)
    BuildContext(ctx context.Context, queryEmbedding []float32) (string, error)
    DeleteMatchingFacts(ctx context.Context, pattern string) error
    DecayOldFacts(ctx context.Context) error
    Init(ctx context.Context) error
}
```

| Method | Description |
|--------|-------------|
| `UpsertFact` | Insert a new fact or merge with semantically similar existing fact (cosine > 0.85) |
| `SearchFacts` | Vector search over stored facts |
| `BuildContext` | Build formatted memory context string from top facts (by confidence + recency) |
| `DeleteMatchingFacts` | Delete facts whose text matches a pattern (SQL LIKE) |
| `DecayOldFacts` | Multiply confidence by 0.95 for facts not updated in 7+ days; prune if < 0.3 and > 30 days old |
| `Init` | Create `user_facts` table |

**Implementations:**

| Package | Constructor |
|---------|------------|
| `memory/sqlite` | `memsqlite.New(path string) *MemoryStore` |

---

### Tool

**File:** `tool.go`

```go
type Tool interface {
    Definitions() []ToolDefinition
    Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
}
```

| Method | Description |
|--------|-------------|
| `Definitions` | Return one or more tool schemas for the LLM |
| `Execute` | Handle a tool call. `name` identifies which function (when a Tool exposes multiple). `args` is the LLM-generated JSON arguments. |

**Implementations:**

| Package | Tool Functions | Constructor |
|---------|---------------|------------|
| `tools/knowledge` | `knowledge_search` | `knowledge.New(store, embedding)` |
| `tools/remember` | `remember` | `remember.New(store, embedding)` |
| `tools/search` | `web_search` | `search.New(embedding, braveAPIKey)` |
| `tools/schedule` | `schedule_create`, `schedule_list`, `schedule_update`, `schedule_delete` | `schedule.New(store, tzOffset)` |
| `tools/shell` | `shell_exec` | `shell.New(workspacePath, timeoutSecs)` |
| `tools/file` | `file_read`, `file_write`, `file_list` | `file.New(workspacePath)` |
| `tools/http` | `http_fetch` | `http.New()` |

---

### Agent

**File:** `agent.go`

```go
type Agent interface {
    Name() string
    Description() string
    Execute(ctx context.Context, task AgentTask) (AgentResult, error)
}
```

| Method | Description |
|--------|-------------|
| `Name` | Agent identifier string (e.g. `"researcher"`) |
| `Description` | Human-readable description. Used by Network to generate tool definitions for the routing LLM. |
| `Execute` | Run the agent on a task and return a result. |

**Implementations:**

| Type | Constructor | Notes |
|------|------------|-------|
| `LLMAgent` | `NewLLMAgent(name, desc, provider, ...AgentOption)` | Tool-calling loop with a single Provider |
| `Network` | `NewNetwork(name, desc, router, ...AgentOption)` | Multi-agent coordinator via LLM router |

---

### Processors

**File:** `processor.go`

Three separate interfaces for hooking into the agent execution pipeline. A processor implements whichever phases it needs.

```go
// PreProcessor runs before messages are sent to the LLM.
type PreProcessor interface {
    PreLLM(ctx context.Context, req *ChatRequest) error
}

// PostProcessor runs after the LLM responds, before tool execution.
type PostProcessor interface {
    PostLLM(ctx context.Context, resp *ChatResponse) error
}

// PostToolProcessor runs after each tool execution.
type PostToolProcessor interface {
    PostTool(ctx context.Context, call ToolCall, result *ToolResult) error
}
```

| Interface | Hook Point | Receives |
|-----------|------------|----------|
| `PreProcessor` | Before each LLM call in the tool-calling loop | Mutable `*ChatRequest` (full message history) |
| `PostProcessor` | After LLM response, before tool dispatch | Mutable `*ChatResponse` (content + tool calls) |
| `PostToolProcessor` | After each tool call, before result is appended to history | `ToolCall` (read-only) + mutable `*ToolResult` |

**Halt mechanism:** Return `ErrHalt` to short-circuit execution with a canned response. Other errors propagate as infrastructure failures.

```go
type ErrHalt struct {
    Response string
}
```

**ProcessorChain:**

```go
chain := oasis.NewProcessorChain()
chain.Add(processor)                              // Must implement at least one of the 3 interfaces
err := chain.RunPreLLM(ctx, &req)                 // Run PreProcessor hooks
err := chain.RunPostLLM(ctx, &resp)               // Run PostProcessor hooks
err := chain.RunPostTool(ctx, toolCall, &result)   // Run PostToolProcessor hooks
chain.Len()                                        // Number of registered processors
```

**Registration via AgentOption:**

```go
oasis.WithProcessors(processors ...any)  // Add to LLMAgent or Network
```

---

## Types

### Domain Types

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
    Role      string    `json:"role"`     // "user" or "assistant"
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
    Schedule        string `json:"schedule"`        // "HH:MM <recurrence>"
    ToolCalls       string `json:"tool_calls"`       // JSON array of ScheduledToolCall
    SynthesisPrompt string `json:"synthesis_prompt"`
    NextRun         int64  `json:"next_run"`
    Enabled         bool   `json:"enabled"`
    SkillID         string `json:"skill_id,omitempty"` // Optional: run under this skill's context
    CreatedAt       int64  `json:"created_at"`
}

// Skill is a stored instruction package for specializing the action agent.
type Skill struct {
    ID           string    `json:"id"`
    Name         string    `json:"name"`
    Description  string    `json:"description"`
    Instructions string    `json:"instructions"`
    Tools        []string  `json:"tools,omitempty"`  // Restrict available tools (empty = all)
    Model        string    `json:"model,omitempty"`  // Override default model (empty = default)
    Embedding    []float32 `json:"-"`                // For semantic search
    CreatedAt    int64     `json:"created_at"`
    UpdatedAt    int64     `json:"updated_at"`
}

type ScheduledToolCall struct {
    Tool   string          `json:"tool"`
    Params json.RawMessage `json:"params"`
}

type Intent int

const (
    IntentChat   Intent = iota
    IntentAction
)
```

### LLM Protocol Types

**File:** `types.go`

```go
type ChatMessage struct {
    Role       string          `json:"role"`        // "system", "user", "assistant", "tool"
    Content    string          `json:"content"`
    Images     []ImageData     `json:"images,omitempty"`
    ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
    ToolCallID string          `json:"tool_call_id,omitempty"`
    Metadata   json.RawMessage `json:"metadata,omitempty"`
}

type ImageData struct {
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
    Messages []ChatMessage `json:"messages"`
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
```

### Tool Types

**File:** `tool.go`

```go
type ToolDefinition struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  json.RawMessage `json:"parameters"`  // JSON Schema
}

type ToolResult struct {
    Content string `json:"content"`
    Error   string `json:"error,omitempty"`
}
```

### Frontend Types

**File:** `types.go`

```go
type IncomingMessage struct {
    ID           string
    ChatID       string
    UserID       string
    Text         string
    ReplyToMsgID string
    Document     *FileInfo
    Photos       []FileInfo
    Caption      string
}

type FileInfo struct {
    FileID   string
    FileName string
    MimeType string
    FileSize int64
}
```

### Agent Types

**File:** `agent.go`

```go
type AgentTask struct {
    Input   string            // Natural language task description
    Context map[string]string // Optional metadata (thread ID, user ID, etc.)
}

type AgentResult struct {
    Output string // Agent's final response text
    Usage  Usage  // Aggregate token usage across all LLM calls
}

type AgentOption func(*agentConfig) // Shared option type for LLMAgent and Network
```

---

## Constructors and Helpers

### ChatMessage Constructors

**File:** `types.go`

```go
oasis.UserMessage(text string) ChatMessage       // Role: "user"
oasis.SystemMessage(text string) ChatMessage      // Role: "system"
oasis.AssistantMessage(text string) ChatMessage   // Role: "assistant"
oasis.ToolResultMessage(callID, content string) ChatMessage  // Role: "tool"
```

### ToolRegistry

**File:** `tool.go`

```go
registry := oasis.NewToolRegistry()
registry.Add(tool)                                     // Register a tool
defs := registry.AllDefinitions()                      // Get all tool schemas
result, err := registry.Execute(ctx, name, argsJSON)   // Dispatch by name
```

### LLMAgent

**File:** `llmagent.go`

```go
// Create an LLMAgent (tool-calling loop with a single Provider)
agent := oasis.NewLLMAgent(name, description string, provider oasis.Provider, opts ...oasis.AgentOption)

// AgentOption functions (shared with Network)
oasis.WithTools(tools ...oasis.Tool)          // Add tools
oasis.WithPrompt(s string)                   // Set system prompt
oasis.WithMaxIter(n int)                     // Max tool-calling iterations (default 10)
oasis.WithAgents(agents ...oasis.Agent)       // Ignored by LLMAgent
oasis.WithProcessors(processors ...any)       // Add processors to execution pipeline
```

### Network

**File:** `network.go`

```go
// Create a Network (multi-agent coordinator via LLM router)
network := oasis.NewNetwork(name, description string, router oasis.Provider, opts ...oasis.AgentOption)

// AgentOption functions (shared with LLMAgent)
oasis.WithAgents(agents ...oasis.Agent)       // Add subagents (exposed as "agent_<name>" tools)
oasis.WithTools(tools ...oasis.Tool)          // Add direct tools
oasis.WithPrompt(s string)                   // Set router system prompt
oasis.WithMaxIter(n int)                     // Max routing iterations (default 10)
oasis.WithProcessors(processors ...any)       // Add processors to execution pipeline
```

### ID and Time

**File:** `id.go`

```go
oasis.NewID() string    // Time-sortable 20-char xid (base32)
oasis.NowUnix() int64   // Current Unix timestamp (seconds)
```

### Error Types

**File:** `errors.go`

```go
type ErrLLM struct {
    Provider string
    Message  string
}

type ErrHTTP struct {
    Status int
    Body   string
}
```

Both implement the `error` interface.

---

## Ingest Pipeline

**Package:** `github.com/nevindra/oasis/ingest`

### Pipeline

```go
pipeline := ingest.NewPipeline(maxTokens, overlapTokens int) *Pipeline

result := pipeline.IngestText(content, source, title string) IngestResult
result := pipeline.IngestHTML(html, sourceURL string) IngestResult
result := pipeline.IngestFile(content, filename string) IngestResult
```

### IngestResult

```go
type IngestResult struct {
    Document oasis.Document
    Chunks   []oasis.Chunk  // Embedding field is empty -- caller must embed
}
```

### Text Extraction

```go
ingest.StripHTML(content string) string
ingest.ExtractText(content string, ct ContentType) string
ingest.ContentTypeFromExtension(ext string) ContentType
```

### Chunking

```go
ingest.ChunkText(text string, cfg ChunkerConfig) []string
ingest.DefaultChunkerConfig() ChunkerConfig

type ChunkerConfig struct {
    MaxChars     int  // Default 2048 (~512 tokens)
    OverlapChars int  // Default 200 (~50 tokens)
}
```

---

## Configuration

**Package:** `github.com/nevindra/oasis/internal/config`

```go
cfg := config.Load(path string) Config    // Load from file (empty = "oasis.toml")
cfg := config.Default() Config            // Defaults only
```

### Config Struct

```go
type Config struct {
    Telegram  TelegramConfig   // token, allowed_user_id
    LLM       LLMConfig        // provider, model, api_key
    Embedding EmbeddingConfig  // provider, model, dimensions, api_key
    Database  DatabaseConfig   // path, turso_url, turso_token
    Brain     BrainConfig      // context_window, vector_top_k, timezone_offset, workspace_path
    Intent    IntentConfig     // provider, model, api_key
    Action    ActionConfig     // provider, model, api_key
    Search    SearchConfig     // brave_api_key
    Observer  ObserverConfig   // enabled, pricing
}
```

See [Configuration](configuration.md) for all fields, defaults, and environment variable mappings.

---

## Observer

**Package:** `github.com/nevindra/oasis/observer`

OTEL-based observability middleware. Wraps `Provider`, `EmbeddingProvider`, and `Tool` with instrumented versions that emit traces, metrics, and structured logs via OpenTelemetry.

### Setup

```go
inst, shutdown, err := observer.Init(ctx, pricingOverrides)
defer shutdown(ctx)
```

### Wrappers

```go
// Wrap a Provider (emits llm.chat, llm.chat_with_tools, llm.chat_stream spans)
observed := observer.WrapProvider(provider, modelName, inst)

// Wrap an EmbeddingProvider (emits llm.embed spans)
observed := observer.WrapEmbedding(embedding, modelName, inst)

// Wrap a Tool (emits tool.execute spans)
observed := observer.WrapTool(tool, inst)
```

All wrappers implement their respective interfaces (`oasis.Provider`, `oasis.EmbeddingProvider`, `oasis.Tool`), so they plug into existing code with no changes.

### Traces

| Span Name | Attributes |
|-----------|-----------|
| `llm.chat` | model, provider, input_tokens, output_tokens, cost_usd |
| `llm.chat_with_tools` | model, provider, input_tokens, output_tokens, cost_usd, tool_count, tool_names |
| `llm.chat_stream` | model, provider, input_tokens, output_tokens, cost_usd, stream_chunks |
| `llm.embed` | model, provider, text_count, dimensions |
| `tool.execute` | tool_name, status, result_length |

### Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `llm.token.usage` | Counter | Tokens consumed (by model, provider, direction) |
| `llm.cost.total` | Counter | Cumulative cost in USD |
| `llm.requests` | Counter | Request count (by model, method, status) |
| `llm.duration` | Histogram | Call latency in ms |
| `tool.executions` | Counter | Tool call count (by name, status) |
| `tool.duration` | Histogram | Tool latency in ms |
| `embedding.requests` | Counter | Embedding call count |
| `embedding.duration` | Histogram | Embedding latency in ms |

### Cost Calculation

```go
calc := observer.NewCostCalculator(overrides) // merges with built-in defaults
cost := calc.Calculate("gemini-2.5-flash", inputTokens, outputTokens)
```

Built-in pricing for common Gemini, OpenAI, and Anthropic models. Unknown models return `0.0`.

---

## Schedule Format Reference

Used by `ScheduledAction.Schedule` and `tools/schedule`:

```
Format: "HH:MM <recurrence>"

Recurrence types:
  once                     -- Run once at HH:MM
  daily                    -- Every day at HH:MM
  weekly(monday)           -- Every week on the specified day
  custom(mon,wed,fri)      -- On specific days of the week
  monthly(15)              -- On the 15th of every month

Day names (English and Indonesian):
  monday/mon/senin, tuesday/tue/selasa, wednesday/wed/rabu,
  thursday/thu/kamis, friday/fri/jumat, saturday/sat/sabtu,
  sunday/sun/minggu

Examples:
  "08:00 daily"
  "09:30 weekly(monday)"
  "07:00 custom(mon,wed,fri)"
  "10:00 monthly(1)"
  "14:00 once"
```

Times are in the user's local timezone (determined by `timezone_offset` config).
