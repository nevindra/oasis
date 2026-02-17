# Oasis Coding Conventions

Rules and patterns that all contributors (human and LLM) must follow when writing code in this project. Read this before writing any code.

For engineering principles and mindset (performance, DX, dependency philosophy), see [ENGINEERING.md](ENGINEERING.md).

## Philosophy

Oasis is deliberately minimalist. The project avoids large frameworks, SDK dependencies, and module bloat. When a standard library solution or hand-rolled implementation exists and is simple enough, it is preferred over adding a dependency.

**Do not add dependencies unless absolutely necessary.** If you think you need a new module, check whether the project already has a hand-rolled solution first.

## Error Handling

### Custom Error Types

Two error types are defined in `errors.go`:

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

No `pkg/errors`, no error wrapping frameworks. Use `fmt.Errorf("context: %w", err)` for wrapping when needed.

### Rules

- **Error messages are lowercase, no trailing period:** `"invalid schedule format: %s"`, not `"Invalid schedule format: %s."`.
- **Wrap with context:** `fmt.Errorf("store init: %w", err)`, not bare `return err`.
- **Provider-specific errors** use `ErrLLM`:
  ```go
  func (g *Gemini) wrapErr(msg string) error {
      return &oasis.ErrLLM{Provider: "gemini", Message: msg}
  }
  ```

### ToolResult is Not an Error

`ToolResult` is a plain struct. Tool execution always "succeeds" at the Go level — business errors are encoded in `ToolResult.Error`. This is by design: tool errors are communicated back to the LLM, not propagated as Go errors.

```go
// Correct
return oasis.ToolResult{Content: "Task created: buy groceries"}, nil
return oasis.ToolResult{Error: "no tasks found matching 'xyz'"}, nil

// Wrong — don't return Go error for business failures
return oasis.ToolResult{}, fmt.Errorf("no tasks found")
```

### Silent Error Handling

Non-critical operations discard errors with `_ =`:

```go
// Telegram edit during streaming — may fail if content hasn't changed
_ = a.frontend.Edit(ctx, chatID, msgID, accumulated.String())
```

Only use this pattern when failure is expected and non-critical.

## Project Layout

### Interface in Root, Implementation in Subdirectory

Core interfaces live in the root `oasis` package. Implementations live in dedicated packages:

```
oasis/
  provider.go              — Provider, EmbeddingProvider interfaces
  provider/gemini/         — Gemini implementation
  provider/openaicompat/   — OpenAI-compatible implementation

  store.go                 — VectorStore interface
  store/sqlite/            — SQLite implementation
  store/libsql/            — Turso/libSQL implementation

  frontend.go              — Frontend interface
  frontend/telegram/       — Telegram implementation
```

### File = Concern

One file, one concern. Don't mix routing with storage in the same file:

```
internal/bot/
  app.go      — struct, constructor, Run(), RunWithSignal()
  router.go   — message routing dispatch
  chat.go     — chat streaming handler
  action.go   — action agent loop
  intent.go   — intent classification
  store.go    — background persistence (embed + store + facts)
  agents.go   — agent lifecycle management
```

### Package Naming

Package names are short, lowercase, single words:

```go
package gemini       // not oasis_gemini
package sqlite       // not sqliteStore
package schedule     // not scheduleTool
```

## Import Ordering

Imports follow a 3-group pattern separated by blank lines:

1. **Standard library** (`context`, `fmt`, `net/http`, etc.)
2. **External modules + project root** (`github.com/nevindra/oasis`, `github.com/rs/xid`, etc.)
3. **Internal packages** (`github.com/nevindra/oasis/internal/...`)

```go
import (
    "context"
    "encoding/json"
    "fmt"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/ingest"

    "github.com/nevindra/oasis/internal/config"
)
```

### Root Package Import Alias

When importing the root `oasis` package from subpackages, use the alias `oasis`:

```go
import oasis "github.com/nevindra/oasis"
```

## Naming Conventions

### Functions and Methods

- Exported: `PascalCase` — `func New(...)`, `func (t *Tool) Execute(...)`
- Unexported: `camelCase` — `func buildBody(...)`, `func (g *Gemini) wrapErr(...)`
- Constructors: always `New(...)` or `NewXxx(...)`

### Constants

Top of file, after imports. Use `camelCase` for unexported, `PascalCase` for exported:

```go
const (
    maxStreamRetries = 3
    maxMessageLength = 4096
    baseURL          = "https://generativelanguage.googleapis.com/v1beta"
)
```

Use `_` separators for large numbers: `1 << 20` or `1_000_000`.

### Struct Tags

JSON tags use `snake_case`, match the Go field names where possible:

```go
type Document struct {
    ID        string `json:"id"`
    CreatedAt int64  `json:"created_at"`
}
```

Omit from JSON with `json:"-"` for internal-only fields:

```go
Embedding []float32 `json:"-"`
```

## Interface Implementation

### Compile-Time Verification

Every concrete implementation must include a compile-time interface check:

```go
var _ oasis.VectorStore = (*Store)(nil)
var _ oasis.Frontend = (*Bot)(nil)
```

### Package Doc Comments

Every package must have a doc comment on its primary file:

```go
// Package sqlite implements oasis.VectorStore using pure-Go SQLite
// with in-process brute-force vector search. Zero CGO required.
package sqlite
```

## Constructor Patterns

### Dependency Injection

Dependencies are injected through constructors, never globals:

```go
func New(store oasis.VectorStore, emb oasis.EmbeddingProvider) *KnowledgeTool {
    return &KnowledgeTool{store: store, embedding: emb, topK: 5}
}
```

### Deps Struct for Many Parameters

When constructors need 4+ parameters, group them in a `Deps` struct:

```go
type Deps struct {
    Frontend  oasis.Frontend
    ChatLLM   oasis.Provider
    IntentLLM oasis.Provider
    ActionLLM oasis.Provider
    Embedding oasis.EmbeddingProvider
    Store     oasis.VectorStore
    Memory    oasis.MemoryStore
}

func New(cfg *config.Config, deps Deps) *App { ... }
```

## Tool Conventions

### Adding a New Tool

1. Create a package in `tools/` (e.g., `tools/mytool/`).
2. Define a struct with dependencies.
3. Implement the `oasis.Tool` interface.
4. Register it in `cmd/oasis/main.go`.

### Tool Definition Rules

- Tool names use `snake_case`: `knowledge_search`, `schedule_create`
- Description must be clear enough for an LLM to decide when to call it
- Parameters use JSON Schema via `json.RawMessage`
- A single Tool struct can provide multiple tool definitions

### Tool Execute Pattern

Parse args into an anonymous struct, early-return on errors:

```go
func (t *MyTool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    var params struct {
        Query string `json:"query"`
    }
    if err := json.Unmarshal(args, &params); err != nil {
        return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
    }
    // ... business logic ...
    return oasis.ToolResult{Content: result}, nil
}
```

For multi-function tools, dispatch on `name`:

```go
switch name {
case "schedule_create":
    return t.handleCreate(ctx, args)
case "schedule_list":
    return t.handleList(ctx)
default:
    return oasis.ToolResult{Error: "unknown tool: " + name}, nil
}
```

## Database Conventions

### Fresh Connections

Each VectorStore/MemoryStore method opens a fresh database connection via `sql.Open()`. Do not cache or reuse connections — this avoids `STREAM_EXPIRED` errors on Turso.

```go
func (s *Store) openDB() (*sql.DB, error) {
    db, err := sql.Open("sqlite", s.dbPath)
    if err != nil {
        return nil, fmt.Errorf("open database: %w", err)
    }
    return db, nil
}
```

### Table Creation

All `CREATE TABLE` statements use `IF NOT EXISTS`. Table creation happens in `Init()`.

### Embedding Storage

Embeddings are serialized as JSON text (`[]float32` -> `string`). Vector search loads all embeddings and computes cosine similarity in-process.

## LLM Provider Conventions

### Adding a New Provider

1. Create a package in `provider/` (e.g., `provider/anthropic/`).
2. Implement `oasis.Provider` and/or `oasis.EmbeddingProvider`.
3. Use raw HTTP via `net/http`. No SDK dependencies.
4. Add a `wrapErr()` helper for consistent error wrapping.

### Streaming

All streaming implementations use SSE (Server-Sent Events):

1. POST with streaming enabled
2. Read response as buffered reader
3. Parse `data:` lines
4. Send text deltas via `ch <- chunk`
5. Close the channel when done
6. Return final `ChatResponse` with usage stats

```go
func (g *Gemini) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- string) (oasis.ChatResponse, error) {
    defer close(ch)
    // ... SSE parsing, send chunks to ch ...
    return oasis.ChatResponse{Content: full, Usage: usage}, nil
}
```

## Telegram Conventions

### HTML, Not Markdown

Always use `parse_mode: "HTML"` for formatted output. Convert markdown to HTML via goldmark.

### Streaming Edits

- **Intermediate edits**: plain text via `Edit()` (markdown may be incomplete mid-stream)
- **Final edit**: HTML via `EditFormatted()`
- **Edit errors**: silently ignored with `_ =`
- **Edit rate**: max once per second to avoid Telegram rate limits

### Message Length

Telegram has a 4096-character limit. Handle splitting for long messages.

## Configuration Conventions

### Adding a New Config Field

1. Add the field to the appropriate sub-config struct in `internal/config/config.go`
2. Add a default value in `Default()`
3. If it should be overridable via env var, add the override in `Load()`
4. Update `oasis.toml` with the new field

### Env Var Naming

All env vars use the `OASIS_` prefix, uppercase, underscore-separated:

```
OASIS_TELEGRAM_TOKEN
OASIS_LLM_API_KEY
OASIS_BRAVE_API_KEY
```

## Concurrency Patterns

### Background Goroutines

Heavy work that's not on the critical path runs in background goroutines:

```go
go func() {
    a.storeMessagePair(ctx, conv.ID, userText, assistantText)
    a.extractAndStoreFacts(ctx, userText, assistantText)
}()
```

### Channel-Based Streaming

LLM streaming uses buffered channels:

```go
ch := make(chan string, 100)
go func() {
    resp, err := provider.ChatStream(ctx, req, ch)
    resultCh <- streamResult{resp, err}
}()
for chunk := range ch {
    accumulated.WriteString(chunk)
    // edit message periodically
}
```

### Retry with Exponential Backoff

Transient errors (429, 5xx) are retried. Non-transient errors are not.

```go
for attempt := 0; attempt <= maxRetries; attempt++ {
    if attempt > 0 {
        delay := time.Duration(1<<(attempt-1)) * time.Second
        time.Sleep(delay)
    }
    // ... attempt operation ...
}
```

## Logging

### Standard log Package

Use Go's standard `log` package. No structured logging frameworks.

```go
log.Printf(" [recv] from=%s chat=%s", msg.UserID, msg.ChatID)
log.Printf(" [chat] retry %d/%d in %s", attempt, maxRetries, delay)
log.Printf(" [memory] extracted %d fact(s)", len(facts))
```

### Log Tag Convention

Messages start with a **space, then a bracketed tag**:

```
 [recv]    — incoming message
 [auth]    — authentication
 [route]   — message routing
 [chat]    — chat streaming
 [send]    — outgoing message
 [tool]    — tool execution
 [agent]   — action agent lifecycle
 [store]   — persistence
 [memory]  — fact extraction/storage
 [ingest]  — document ingestion
```

## Testing

### Test Files

Tests go in `*_test.go` files alongside the source:

```
ingest/chunker.go
ingest/chunker_test.go
tools/schedule/schedule.go
tools/schedule/schedule_test.go
```

### Test Naming

```go
func TestChunkText(t *testing.T) { ... }
func TestComputeNextRun(t *testing.T) { ... }
func TestStripHTML(t *testing.T) { ... }
```

### Table-Driven Tests

Preferred for functions with multiple input/output cases:

```go
cases := []struct {
    input string
    want  int
}{
    {"monday", 0},
    {"senin", 0},
    {"invalid", -1},
}
for _, tc := range cases {
    got := myFunc(tc.input)
    if got != tc.want {
        t.Errorf("myFunc(%q) = %d, want %d", tc.input, got, tc.want)
    }
}
```

### What to Test

Focus on pure functions and business logic:
- Chunking algorithms
- Schedule parsing and next-run computation
- HTML/Markdown text extraction
- Day name parsing
- Config loading

Do **not** write tests that require external services (LLM API, Telegram, databases with real data).

## Things to Never Do

- **Do not add LLM SDK crates.** All providers use raw HTTP.
- **Do not add bot frameworks.** The Telegram client is hand-rolled.
- **Do not add error wrapping libraries.** Use `fmt.Errorf` with `%w`.
- **Do not add time/date libraries.** Use `time` stdlib + hand-rolled date math where needed.
- **Do not cache database connections.** Fresh connection per operation.
- **Do not return Go `error` from `Tool.Execute` for business failures.** Use `ToolResult.Error`.
- **Do not use Telegram's `parse_mode: "Markdown"`.** Always use HTML.
- **Do not use global state.** Inject dependencies through constructors.
- **Do not add structured logging frameworks.** Use the standard `log` package.
- **Do not add HTTP router libraries.** The Telegram client doesn't need one.
