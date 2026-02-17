# Oasis

An AI agent framework for Go. Build tool-calling agents, multi-agent networks, and conversational assistants with composable, interface-driven primitives.

```go
import oasis "github.com/nevindra/oasis"
```

## Features

- **Composable agents** -- `LLMAgent` for single-provider tool loops, `Network` for multi-agent coordination. Networks nest recursively.
- **Processor pipeline** -- `PreProcessor`, `PostProcessor`, `PostToolProcessor` hooks for guardrails, PII redaction, logging, and custom middleware.
- **Interface-driven** -- every component (LLM, storage, tools, frontends, memory) is a Go interface. Swap implementations without touching the rest of the system.
- **Streaming** -- channel-based token streaming with built-in edit batching for messaging platforms.
- **Built-in tools** -- knowledge search (RAG), web search, scheduled actions, shell execution, file I/O, HTTP requests.
- **Observability** -- OpenTelemetry wrappers for providers, tools, and embeddings with cost tracking.
- **No LLM SDKs** -- all providers use raw `net/http`. Zero vendor lock-in.
- **Pure-Go SQLite** -- `modernc.org/sqlite`, no CGO required.

## Quick Start

```go
package main

import (
    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/tools/knowledge"
    "github.com/nevindra/oasis/tools/search"
)

func main() {
    llm := gemini.New(apiKey, "gemini-2.5-flash")
    embedding := gemini.NewEmbedding(apiKey, "text-embedding-004", 768)

    // Single agent with tools
    agent := oasis.NewLLMAgent("assistant", "Helpful research assistant", llm,
        oasis.WithTools(
            knowledge.New(store, embedding),
            search.New(embedding, braveKey),
        ),
        oasis.WithPrompt("You are a helpful research assistant."),
    )

    result, err := agent.Execute(ctx, oasis.AgentTask{Input: "What is quantum computing?"})
}
```

## Agents

### LLMAgent

A single LLM with tools. Runs a tool-calling loop until the model produces a final text response.

```go
researcher := oasis.NewLLMAgent("researcher", "Searches the web", llm,
    oasis.WithTools(searchTool, knowledgeTool),
    oasis.WithPrompt("You are a research specialist."),
    oasis.WithMaxIter(5),
)
```

### Network

Coordinates multiple agents and tools via an LLM router. The router sees subagents as callable tools (`agent_<name>`) and decides which to invoke.

```go
researcher := oasis.NewLLMAgent("researcher", "Searches for information", llm,
    oasis.WithTools(searchTool),
)
writer := oasis.NewLLMAgent("writer", "Writes polished content", llm)

team := oasis.NewNetwork("team", "Research and writing team", router,
    oasis.WithAgents(researcher, writer),
    oasis.WithTools(knowledgeTool),
)

// Networks compose recursively
org := oasis.NewNetwork("org", "Full organization", ceo,
    oasis.WithAgents(team, opsTeam),
)
```

### Processors

Middleware hooks that run at specific points in the agent execution pipeline.

```go
agent := oasis.NewLLMAgent("guarded", "Safe agent", llm,
    oasis.WithTools(searchTool),
    oasis.WithProcessors(&guardrail, &piiRedactor, &logger),
)
```

| Interface | Hook Point | Use Cases |
| --------- | ---------- | --------- |
| `PreProcessor` | Before LLM call | Input validation, context injection, rate limiting |
| `PostProcessor` | After LLM response | Output filtering, tool call validation |
| `PostToolProcessor` | After tool execution | Result redaction, audit logging |

Return `ErrHalt` from any processor to short-circuit execution with a canned response.

## Core Interfaces

| Interface | Purpose |
| --------- | ------- |
| `Provider` | LLM backend -- `Chat`, `ChatWithTools`, `ChatStream` |
| `EmbeddingProvider` | Text-to-vector embedding |
| `Store` | Persistence with vector search |
| `MemoryStore` | Long-term semantic memory (facts, confidence, decay) |
| `Tool` | Pluggable capability for LLM function calling |
| `Frontend` | Messaging platform -- `Poll`, `Send`, `Edit` |
| `Agent` | Composable work unit -- `LLMAgent`, `Network`, or custom |

## Included Implementations

| Component | Packages |
| --------- | -------- |
| **Providers** | `provider/gemini` (Google Gemini), `provider/openaicompat` (OpenAI, Anthropic, Ollama) |
| **Storage** | `store/sqlite` (local), `store/libsql` (Turso/remote) |
| **Memory** | `memory/sqlite` |
| **Frontend** | `frontend/telegram` |
| **Tools** | `tools/knowledge`, `tools/remember`, `tools/search`, `tools/schedule`, `tools/shell`, `tools/file`, `tools/http` |
| **Observability** | `observer` (OpenTelemetry wrappers with cost tracking) |
| **Ingestion** | `ingest` (HTML, Markdown, plain text chunking pipeline) |

## Installation

```bash
go get github.com/nevindra/oasis
```

Requires Go 1.24+.

## Project Structure

```text
oasis/
|-- types.go, provider.go, tool.go     # Core interfaces and domain types
|-- store.go, frontend.go, memory.go
|-- agent.go, llmagent.go, network.go   # Agent primitives
|-- processor.go                        # Processor pipeline
|
|-- provider/gemini/                    # Google Gemini provider
|-- provider/openaicompat/              # OpenAI-compatible provider
|-- frontend/telegram/                  # Telegram frontend
|-- store/sqlite/                       # Local SQLite store
|-- store/libsql/                       # Remote Turso store
|-- memory/sqlite/                      # SQLite memory store
|-- observer/                           # OTEL observability wrappers
|-- ingest/                             # Document chunking pipeline
|-- tools/                              # Built-in tools
|
|-- cmd/bot_example/                    # Reference application
```

## Configuration

Config loading order: **defaults -> `oasis.toml` -> environment variables** (env vars win).

See [docs/framework/configuration.md](docs/framework/configuration.md) for the full reference.

## Documentation

- [Getting Started](docs/framework/getting-started.md) -- installation and first run
- [Architecture](docs/framework/architecture.md) -- component design and data flow
- [Configuration](docs/framework/configuration.md) -- all config options and environment variables
- [Extending Oasis](docs/framework/extending.md) -- adding custom tools, providers, frontends, and stores
- [API Reference](docs/framework/api-reference.md) -- complete interface definitions and types
- [Deployment](docs/framework/deployment.md) -- Docker, cloud deployment, database options

## License

[MPL-2.0](LICENSE)
