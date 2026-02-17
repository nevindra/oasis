# Oasis Framework Documentation

Oasis is a personal AI assistant framework built in Go. It provides a set of modular, interface-driven building blocks for constructing AI-powered assistants: LLM providers, embedding providers, vector storage, long-term memory, a tool execution system, a document ingestion pipeline, and messaging frontend abstractions.

Every major component is a Go interface. Swap LLM providers, storage backends, messaging platforms, or add custom tools without touching the rest of the system.

## Documentation Index

| Document | Description |
|----------|-------------|
| [Getting Started](getting-started.md) | Installation, prerequisites, and running Oasis for the first time |
| [Configuration](configuration.md) | All configuration options, environment variables, defaults, and fallback logic |
| [Architecture](architecture.md) | Framework component design, interfaces, data flow, and how pieces connect |
| [Extending Oasis](extending.md) | How to add custom tools, LLM providers, frontends, and storage backends |
| [Deployment](deployment.md) | Docker builds, cloud deployment, production setup, and database options |
| [API Reference](api-reference.md) | Complete interface definitions, domain types, and constructor patterns |

## Framework Components

- **Provider** -- LLM abstraction supporting chat, streaming, and function calling. Ships with Gemini and OpenAI-compatible implementations.
- **EmbeddingProvider** -- Text embedding abstraction for vector search. Ships with Gemini embeddings.
- **Store** -- Persistence layer with vector search over messages, document chunks, threads, and scheduled actions. Ships with SQLite and libSQL (Turso) implementations.
- **MemoryStore** -- Long-term semantic memory with fact storage, confidence scoring, deduplication, and decay. Ships with SQLite implementation.
- **Tool + ToolRegistry** -- Pluggable tool system for LLM function calling. Ships with knowledge search, web search, scheduling, shell, file I/O, and HTTP tools.
- **Frontend** -- Messaging platform abstraction (poll for messages, send/edit responses, download files). Ships with Telegram implementation.
- **Ingest Pipeline** -- Document chunking pipeline: extract text from HTML/Markdown/plain text, split into overlapping chunks ready for embedding.
- **Configuration** -- Layered config system: defaults -> TOML file -> environment variables.

## Project Structure

```
oasis/
|-- types.go                       # Domain types (Message, Document, Chunk, etc.)
|-- provider.go                    # Provider + EmbeddingProvider interfaces
|-- tool.go                        # Tool interface + ToolRegistry
|-- store.go                       # Store interface
|-- frontend.go                    # Frontend interface
|-- memory.go                      # MemoryStore interface
|-- errors.go                      # Custom error types
|-- id.go                          # ID generation (xid) + timestamps
|-- oasis.toml                     # Default configuration
|
|-- internal/config/               # Config loading (TOML + env vars)
|
|-- provider/
|   |-- gemini/                    # Google Gemini (Provider + EmbeddingProvider)
|   |-- openaicompat/              # OpenAI-compatible endpoints (Provider)
|
|-- frontend/telegram/             # Telegram bot (Frontend)
|
|-- store/
|   |-- sqlite/                    # Local SQLite (Store)
|   |-- libsql/                    # Remote Turso/libSQL (Store)
|
|-- memory/sqlite/                 # SQLite-backed MemoryStore
|
|-- ingest/                        # Document chunking pipeline
|
|-- tools/
|   |-- knowledge/                 # knowledge_search -- vector search over KB + messages
|   |-- remember/                  # remember -- save content to knowledge base
|   |-- search/                    # web_search -- Brave Search API
|   |-- schedule/                  # schedule_create/list/update/delete
|   |-- shell/                     # shell_exec -- sandboxed command execution
|   |-- file/                      # file_read/write/list -- sandboxed file I/O
|   |-- http/                      # http_request -- outbound HTTP calls
|
|-- cmd/bot_example/main.go         # Reference application entry point
|-- internal/bot/                  # Reference application (orchestration, routing, agents)
|-- internal/scheduling/           # Reference application (background scheduler)
|
|-- docs/
    |-- framework/                 # This documentation
    |-- ARCHITECTURE.md            # Internal dev reference
    |-- CONVENTIONS.md             # Coding conventions
    |-- systems/                   # Per-subsystem internal docs
```

## Minimum Requirements

- Go 1.24+
- An LLM API key (Gemini, OpenAI, or compatible)
- A messaging platform token (Telegram bot token for the default frontend)

## License

See the project root for license information.
