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
| `Workflow` | `NewWorkflow(name, desc, ...WorkflowOption) (*Workflow, error)` | Deterministic DAG-based step orchestration |

---

### InputHandler

**File:** `input.go`

```go
type InputHandler interface {
    RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}
```

| Method | Description |
|--------|-------------|
| `RequestInput` | Deliver a question to a human and block until a response is received or `ctx` is cancelled. |

Human-in-the-loop mechanism for agents. When set via `WithInputHandler`, the agent gains a built-in `ask_user` tool (LLM-driven) and processors can access the handler via `InputHandlerFromContext(ctx)` (programmatic gates).

**Types:**

```go
type InputRequest struct {
    Question string            // The question to show the human
    Options  []string          // Suggested choices (empty = free-form)
    Metadata map[string]string // Context: agent name, source ("llm" or "gate"), tool name, etc.
}

type InputResponse struct {
    Value string // The human's text response
}
```

**Context helpers:**

```go
oasis.WithInputHandlerContext(ctx, handler) context.Context  // Inject handler into context
oasis.InputHandlerFromContext(ctx) (InputHandler, bool)       // Retrieve handler from context
```

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

### Workflow Types

**File:** `workflow.go`

```go
// StepFunc is the function signature for custom workflow steps.
type StepFunc func(ctx context.Context, wCtx *WorkflowContext) error

// StepStatus represents the execution state of a workflow step.
type StepStatus string

const (
    StepPending StepStatus = "pending"
    StepRunning StepStatus = "running"
    StepSuccess StepStatus = "success"
    StepSkipped StepStatus = "skipped"
    StepFailed  StepStatus = "failed"
)

// StepResult holds the outcome of a single step execution.
type StepResult struct {
    Name     string
    Status   StepStatus
    Output   string
    Error    error
    Duration time.Duration
}

// WorkflowResult is the aggregate outcome of a full workflow execution.
type WorkflowResult struct {
    Status  StepStatus
    Steps   map[string]StepResult
    Context *WorkflowContext
    Usage   Usage
}

// StepOption configures an individual workflow step.
type StepOption func(*stepConfig)

// WorkflowOption configures a Workflow. Step definitions and workflow-level
// settings both implement this type.
type WorkflowOption func(*workflowConfig)
```

**WorkflowContext** — shared state map that flows between steps. All methods are safe for concurrent use.

```go
wCtx.Get(key string) (any, bool)    // Read a named value
wCtx.Set(key string, value any)     // Write a named value
wCtx.Input() string                 // Original AgentTask.Input
```

**ForEach iteration helpers** — retrieve per-iteration data inside a ForEach step function:

```go
oasis.ForEachItem(ctx context.Context) (any, bool)  // Current element
oasis.ForEachIndex(ctx context.Context) (int, bool)  // 0-based index
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
oasis.WithTools(tools ...oasis.Tool)                    // Add tools
oasis.WithPrompt(s string)                             // Set system prompt
oasis.WithMaxIter(n int)                               // Max tool-calling iterations (default 10)
oasis.WithAgents(agents ...oasis.Agent)                 // Ignored by LLMAgent
oasis.WithProcessors(processors ...any)                 // Add processors to execution pipeline
oasis.WithInputHandler(h oasis.InputHandler)            // Enable human-in-the-loop (ask_user tool + context)
oasis.WithConversationMemory(s oasis.Store)             // Enable history load/persist per thread
oasis.WithSemanticSearch(e oasis.EmbeddingProvider)     // Enable semantic search across threads + user memory
oasis.WithUserMemory(m oasis.MemoryStore)               // Inject user facts into system prompt (requires WithSemanticSearch)
```

**Memory wiring example:**

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", provider,
    oasis.WithConversationMemory(store),
    oasis.WithSemanticSearch(embedding),
    oasis.WithUserMemory(memoryStore),
    oasis.WithPrompt("You are a helpful assistant."),
    oasis.WithTools(searchTool),
)

// Pass thread_id to enable history. Without it, agent runs stateless.
result, err := agent.Execute(ctx, oasis.AgentTask{
    Input:   "What did we discuss yesterday?",
    Context: map[string]string{"thread_id": "thread-123"},
})
```

### Network

**File:** `network.go`

```go
// Create a Network (multi-agent coordinator via LLM router)
network := oasis.NewNetwork(name, description string, router oasis.Provider, opts ...oasis.AgentOption)

// AgentOption functions (shared with LLMAgent)
oasis.WithAgents(agents ...oasis.Agent)                 // Add subagents (exposed as "agent_<name>" tools)
oasis.WithTools(tools ...oasis.Tool)                    // Add direct tools
oasis.WithPrompt(s string)                             // Set router system prompt
oasis.WithMaxIter(n int)                               // Max routing iterations (default 10)
oasis.WithProcessors(processors ...any)                 // Add processors to execution pipeline
oasis.WithInputHandler(h oasis.InputHandler)            // Enable human-in-the-loop (propagated to subagents)
oasis.WithConversationMemory(s oasis.Store)             // Enable history load/persist per thread
oasis.WithSemanticSearch(e oasis.EmbeddingProvider)     // Enable semantic search across threads + user memory
oasis.WithUserMemory(m oasis.MemoryStore)               // Inject user facts into system prompt (requires WithSemanticSearch)
```

### Workflow

**File:** `workflow.go`

```go
// Create a Workflow (deterministic DAG-based step orchestration).
// Returns an error if the step graph is invalid (duplicate names, unknown deps, cycles).
wf, err := oasis.NewWorkflow(name, description string, opts ...oasis.WorkflowOption)
```

**Step definitions** (each returns a `WorkflowOption`):

```go
oasis.Step(name string, fn StepFunc, opts ...StepOption)                    // Custom function step
oasis.AgentStep(name string, agent Agent, opts ...StepOption)               // Delegate to an Agent
oasis.ToolStep(name string, tool Tool, toolName string, opts ...StepOption) // Call a tool function
oasis.ForEach(name string, fn StepFunc, opts ...StepOption)                 // Iterate over collection
oasis.DoUntil(name string, fn StepFunc, opts ...StepOption)                 // Loop until condition true
oasis.DoWhile(name string, fn StepFunc, opts ...StepOption)                 // Loop while condition true
```

**Step options** (`StepOption`):

| Option | Description |
|--------|-------------|
| `After(steps ...string)` | Dependency edges: run after named steps complete |
| `When(fn func(*WorkflowContext) bool)` | Condition gate: skip if false |
| `InputFrom(key string)` | AgentStep: context key for input |
| `ArgsFrom(key string)` | ToolStep: context key for JSON args |
| `OutputTo(key string)` | Override default output key |
| `Retry(n int, delay time.Duration)` | Retry up to n times with delay |
| `IterOver(key string)` | ForEach: context key with `[]any` |
| `Concurrency(n int)` | ForEach: max parallel iterations (default 1) |
| `Until(fn func(*WorkflowContext) bool)` | DoUntil: exit when true |
| `While(fn func(*WorkflowContext) bool)` | DoWhile: continue while true |
| `MaxIter(n int)` | DoUntil/DoWhile: safety cap (default 10) |

**Workflow options** (`WorkflowOption`):

| Option | Description |
|--------|-------------|
| `WithOnFinish(fn func(WorkflowResult))` | Callback after workflow completes |
| `WithOnError(fn func(string, error))` | Callback when a step fails |
| `WithDefaultRetry(n int, delay time.Duration)` | Default retry for all steps |

### AgentHandle

**File:** `handle.go`

Background agent execution with state tracking, result delivery, and cancellation.

```go
// AgentState represents the execution state of a spawned agent.
type AgentState int32

const (
    StatePending   AgentState = iota // created, not yet running
    StateRunning                     // Execute() in progress
    StateCompleted                   // finished successfully
    StateFailed                      // finished with error
    StateCancelled                   // cancelled by caller or parent context
)

func (s AgentState) String() string    // "pending", "running", etc.
func (s AgentState) IsTerminal() bool  // true for completed, failed, cancelled
```

**Spawn and Handle:**

```go
// Launch an agent in a background goroutine.
handle := oasis.Spawn(ctx, agent, task)

handle.ID() string                                         // Unique execution ID (xid-based)
handle.Agent() Agent                                       // The agent being executed
handle.State() AgentState                                  // Current state
handle.Done() <-chan struct{}                               // Closed when execution finishes
handle.Await(ctx context.Context) (AgentResult, error)     // Block until done or ctx cancelled
handle.Result() (AgentResult, error)                       // Non-blocking; zero value if not done
handle.Cancel()                                            // Request cancellation (non-blocking)
```

**Usage patterns:**

```go
// Simple: spawn and await
handle := oasis.Spawn(ctx, researcher, task)
result, err := handle.Await(ctx)

// Multiplex: race two agents
h1 := oasis.Spawn(ctx, fast, task)
h2 := oasis.Spawn(ctx, thorough, task)
select {
case <-h1.Done():
    h2.Cancel()
    result, _ = h1.Result()
case <-h2.Done():
    h1.Cancel()
    result, _ = h2.Result()
}

// Fire-and-forget with later check
handle := oasis.Spawn(ctx, bg, task)
// ... later ...
if handle.State() == oasis.StateCompleted {
    result, _ := handle.Result()
}
```

---

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

### Ingestor (primary API)

End-to-end ingestion: extract → chunk → embed → store.

```go
// Simple — flat recursive, just works
ingestor := ingest.NewIngestor(store, embedding)
result, err := ingestor.IngestText(ctx, content, "message", "")

// Large docs — parent-child strategy
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
)

// Full control
ingestor := ingest.NewIngestor(store, embedding,
    ingest.WithStrategy(ingest.StrategyParentChild),
    ingest.WithParentChunker(ingest.NewMarkdownChunker(ingest.WithMaxTokens(1024))),
    ingest.WithChildChunker(ingest.NewRecursiveChunker(ingest.WithMaxTokens(256))),
    ingest.WithBatchSize(32),
)

// Entry points
result, err := ingestor.IngestText(ctx, text, source, title)
result, err := ingestor.IngestFile(ctx, content []byte, filename)
result, err := ingestor.IngestReader(ctx, r io.Reader, filename)
```

### IngestResult

```go
type IngestResult struct {
    DocumentID string
    Document   oasis.Document
    ChunkCount int
}
```

### Ingestor Options

```go
ingest.WithChunker(c Chunker)               // custom chunker for flat strategy
ingest.WithParentChunker(c Chunker)          // parent-level chunker
ingest.WithChildChunker(c Chunker)           // child-level chunker
ingest.WithStrategy(s ChunkStrategy)         // StrategyFlat (default) or StrategyParentChild
ingest.WithParentTokens(n int)               // default 1024
ingest.WithChildTokens(n int)                // default 256
ingest.WithBatchSize(n int)                  // chunks per Embed() call, default 64
ingest.WithExtractor(ct ContentType, e Extractor)  // register custom extractor
```

### Interfaces

```go
// Extractor converts raw content to plain text.
type Extractor interface {
    Extract(content []byte) (string, error)
}

// Chunker splits text into chunks suitable for embedding.
type Chunker interface {
    Chunk(text string) []string
}
```

Built-in extractors: `PlainTextExtractor`, `HTMLExtractor`, `MarkdownExtractor`.

### Chunkers

```go
// RecursiveChunker — paragraphs → sentences → words (default)
ingest.NewRecursiveChunker(opts ...ChunkerOption) *RecursiveChunker

// MarkdownChunker — heading-aware, preserves headings in chunks
ingest.NewMarkdownChunker(opts ...ChunkerOption) *MarkdownChunker

// ChunkerOptions
ingest.WithMaxTokens(n int)      // default 512
ingest.WithOverlapTokens(n int)  // default 50
```

### Content Types

```go
type ContentType string

const (
    TypePlainText ContentType = "text/plain"
    TypeHTML      ContentType = "text/html"
    TypeMarkdown  ContentType = "text/markdown"
)

ingest.ContentTypeFromExtension(ext string) ContentType
```

### Chunking Strategies

```go
type ChunkStrategy int

const (
    StrategyFlat        ChunkStrategy = iota  // single-level (default)
    StrategyParentChild                        // two-level hierarchical
)
```

### Utility Functions

```go
ingest.StripHTML(content string) string                      // Strip HTML tags, scripts, styles
ingest.ContentTypeFromExtension(ext string) ContentType      // Map file extension to ContentType
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

OTEL-based observability middleware. Wraps `Provider`, `EmbeddingProvider`, `Tool`, and `Agent` with instrumented versions that emit traces, metrics, and structured logs via OpenTelemetry.

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

// Wrap an Agent (emits agent.execute parent span + lifecycle events)
observed := observer.WrapAgent(agent, inst)
```

All wrappers implement their respective interfaces (`oasis.Provider`, `oasis.EmbeddingProvider`, `oasis.Tool`, `oasis.Agent`), so they plug into existing code with no changes.

### Traces

| Span Name | Attributes |
|-----------|-----------|
| `llm.chat` | model, provider, input_tokens, output_tokens, cost_usd |
| `llm.chat_with_tools` | model, provider, input_tokens, output_tokens, cost_usd, tool_count, tool_names |
| `llm.chat_stream` | model, provider, input_tokens, output_tokens, cost_usd, stream_chunks |
| `llm.embed` | model, provider, text_count, dimensions |
| `tool.execute` | tool_name, status, result_length |
| `agent.execute` | agent_name, agent_type, agent_status, tokens_input, tokens_output |

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
| `agent.executions` | Counter | Agent execution count (by name, status) |
| `agent.duration` | Histogram | Agent execution latency in ms |

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
