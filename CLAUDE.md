# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read and follow these docs before making changes:**

- **[docs/ENGINEERING.md](docs/ENGINEERING.md)** — Engineering principles and mindset: performance patterns, developer experience, dependency philosophy, error handling philosophy, testing approach, and code organization rules.
- **[docs/CONVENTIONS.md](docs/CONVENTIONS.md)** — Coding conventions: error handling, import ordering, naming, config patterns, database patterns, logging, testing rules, and things to never do.

**Framework documentation (for users/extenders):**

- **[docs/framework/](docs/framework/README.md)** — Complete framework docs: getting started, configuration reference, architecture overview, extending guide (tools/providers/frontends/stores), deployment guide, and full API reference.

**When you make architectural or convention changes, update the corresponding doc file to keep them in sync.**

## What is Oasis

Oasis is a personal AI assistant framework built in Go. It combines conversational AI (streaming), a knowledge base (RAG with vector search), long-term semantic memory, a pluggable tool execution system, scheduled automations, and web browsing. When Oasis is mentioned it means the FRAMEWORK not the TELEGRAM BOT.

## Build & Run Commands

```bash
# Build
go build ./cmd/bot_example/

# Build with CGO (better SQLite performance)
CGO_ENABLED=1 go build -o oasis ./cmd/bot_example/

# Run (requires .env or env vars set)
source .env && go run ./cmd/bot_example/

# Run tests
go test ./...

# Run tests for a specific package
go test ./ingest/
go test ./store/sqlite/
go test ./tools/schedule/

# Run a single test by name
go test ./ingest/ -run TestChunkText

# Docker build
docker build -t oasis .
```

## Configuration

Config loading order: defaults -> `oasis.toml` -> environment variables (env wins).

Secrets are set via environment variables:
- `OASIS_TELEGRAM_TOKEN` — Telegram bot token
- `OASIS_LLM_API_KEY` — API key for the chat LLM provider
- `OASIS_EMBEDDING_API_KEY` — API key for the embedding provider
- `OASIS_INTENT_API_KEY` — intent LLM (falls back to `OASIS_LLM_API_KEY`)
- `OASIS_ACTION_API_KEY` — action LLM (falls back to `OASIS_LLM_API_KEY`)
- `OASIS_TURSO_URL` / `OASIS_TURSO_TOKEN` — optional remote libSQL database
- `OASIS_BRAVE_API_KEY` — Brave Search API (enables `web_search` tool)
- `OASIS_OBSERVER_ENABLED` — enable OTEL observability (`true` or `1`)
- `OASIS_CONFIG` — path to config file (defaults to `oasis.toml`)

Full config reference: [docs/framework/configuration.md](docs/framework/configuration.md)

## Architecture (quick reference)

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for full details.

### Project Structure

```
oasis/
|-- types.go, provider.go, tool.go, store.go, frontend.go, memory.go
|   (root package: domain types + interfaces)
|
|-- cmd/bot_example/main.go         # Reference application entry point
|-- internal/config/               # Config loading
|-- internal/bot/                  # Application orchestration (routing, chat, agents)
|-- internal/scheduling/           # Background scheduler
|
|-- provider/gemini/               # Google Gemini (Provider + EmbeddingProvider)
|-- provider/openaicompat/         # OpenAI-compatible (Provider)
|-- frontend/telegram/             # Telegram bot (Frontend)
|-- store/sqlite/                  # Local SQLite (Store)
|-- store/libsql/                  # Remote Turso (Store)
|-- memory/sqlite/                 # SQLite MemoryStore
|-- observer/                      # OTEL observability (wraps Provider/Tool/Embedding)
|-- ingest/                        # Document chunking pipeline
|-- tools/{knowledge,remember,search,schedule,shell,file,http}/
```

### Core Interfaces (root package)

| Interface | File | Purpose |
|-----------|------|---------|
| `Provider` | `provider.go` | LLM: Chat, ChatWithTools, ChatStream |
| `EmbeddingProvider` | `provider.go` | Text-to-vector embedding |
| `Frontend` | `frontend.go` | Messaging platform: Poll, Send, Edit |
| `Store` | `store.go` | Persistence + vector search |
| `MemoryStore` | `memory.go` | Long-term semantic memory |
| `Tool` | `tool.go` | Pluggable tool for LLM function calling |

### Key Design Decisions

- **No LLM SDKs** — all providers use raw HTTP (`net/http`)
- **No bot framework** — hand-rolled Telegram client
- **No error framework** — 2 custom error types (`ErrLLM`, `ErrHTTP`)
- **No chrono/time library** — hand-rolled date math
- **Pure-Go SQLite** — `modernc.org/sqlite`, no CGO required
- **Embeddings as JSON** — brute-force cosine similarity in-process
- **Interface-driven** — every major component is a Go interface
- **Constructor injection** — no global state, dependencies via `Deps` struct
- **Background storage** — embed + store + fact extraction after response
- **Streaming** — channel-based token streaming with 1s edit batching

## Conventions (quick reference)

See [docs/CONVENTIONS.md](docs/CONVENTIONS.md) for full details.

### Import Ordering (3 groups)

```go
import (
    "context"           // 1. stdlib
    "fmt"

    oasis "github.com/nevindra/oasis"  // 2. external + project root
    "github.com/nevindra/oasis/ingest"

    "github.com/nevindra/oasis/internal/config"  // 3. internal
)
```

### Error Handling

- Tool errors go in `ToolResult.Error`, not Go `error`
- Error messages: lowercase, no trailing period
- Graceful degradation: log and continue, don't crash
- Transient errors (429, 5xx): retry with exponential backoff
- Non-critical ops: `let _ =` pattern (e.g. edit during streaming)

### Tool Pattern

```go
func (t *MyTool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    var params struct { Query string `json:"query"` }
    if err := json.Unmarshal(args, &params); err != nil {
        return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
    }
    // ... business logic ...
    return oasis.ToolResult{Content: result}, nil
}
```

### Database

Uses pure-Go SQLite with brute-force vector search. Fresh connections per call (no pooling). Tables: `documents`, `chunks`, `threads`, `messages`, `config`, `scheduled_actions`, `skills`, `user_facts`.

## Engineering Principles (quick reference)

See [docs/ENGINEERING.md](docs/ENGINEERING.md) for the full mental model.

**Key distinction:** Framework primitives (core interfaces, agent model, protocol types) are designed for **composability and expressiveness** — invest in getting the design right. Application code (tool impls, provider adapters) is designed for **simplicity and pragmatism** — concrete first, refactor later.

1. **Earn every abstraction** — concrete first, extract only when pattern repeats 3x. *Exception: framework primitives are the abstraction — design for composability from the start.*
2. **Optimize for the reader** — names explain intent, comments explain why, top-to-bottom flow
3. **Make it fast where it matters** — user-perceived latency and API call count, not micro-optimizations
4. **Fail gracefully** — degrade don't die, distinguish transient vs permanent, never crash on recoverable errors
5. **Own your dependencies** — hand-roll < 200 lines, no SDKs for external APIs, raw HTTP
6. **Design for composability** — interfaces at natural boundaries, primitives that snap together, expressiveness over simplicity at the framework layer
7. **Explicit over magic** — constructor injection, no hidden side effects, predictable config cascade
8. **Ship incrementally** — working > perfect for app code. *Right > fast for framework primitives — breaking changes cascade.*
9. **Test what matters** — behavior not implementation, pure functions first, edge cases > happy path
10. **Respect the user's time** — actionable errors, sensible defaults, living documentation
