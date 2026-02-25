# Oasis

Build AI agents in Go that actually compose. Single agents, multi-agent networks, DAG workflows, graph-powered RAG, code execution — all as recursive primitives. No LLM SDKs. No vendor lock-in. Just interfaces.

```go
import oasis "github.com/nevindra/oasis"
```

## Why Oasis?

Most agent frameworks are wrappers around LLM SDKs with hardcoded abstractions. Oasis is different:

- **Everything is an interface.** LLM providers, storage, tools, memory — swap any component without touching the rest. Write your own in 20 lines.
- **Agents compose recursively.** An `LLMAgent` is an `Agent`. A `Network` of agents is an `Agent`. A `Workflow` containing both is an `Agent`. Nest them arbitrarily.
- **No LLM SDKs.** Every provider uses raw `net/http`. You control the bytes. Zero vendor lock-in, minimal dependencies.
- **Go-native concurrency.** Parallel tool dispatch, background agents via `Spawn()`, DAG workflows with automatic wave execution — all using goroutines and channels.
- **Production primitives, not demos.** Rate limiting, retry with backoff, batch processing, persistent Graph RAG, semantic memory with decay, suspend/resume, code execution with tool bridge.

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
    // Use any provider — Gemini, OpenAI, Groq, Ollama, DeepSeek, Mistral, vLLM, etc.
    llm := gemini.New(apiKey, "gemini-2.5-flash")
    // Or: llm := openaicompat.NewProvider("sk-xxx", "gpt-4o", "https://api.openai.com/v1")
    // Or: llm := openaicompat.NewProvider("", "llama3", "http://localhost:11434/v1")
    embedding := gemini.NewEmbedding(apiKey, "text-embedding-004", 768)

    agent := oasis.NewLLMAgent("assistant", "Helpful research assistant", llm,
        oasis.WithTools(
            knowledge.New(store, embedding),
            search.New(embedding, braveKey),
        ),
        oasis.WithPrompt("You are a helpful research assistant."),
        oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding)),
        oasis.WithUserMemory(memoryStore, embedding),
    )

    result, err := agent.Execute(ctx, oasis.AgentTask{Input: "What is quantum computing?"})
}
```

## Features

### Agent Primitives

- **LLMAgent** — single LLM with tools. Runs a tool-calling loop until the model produces a final response. Multiple tool calls execute in parallel automatically.
- **Network** — coordinates multiple agents via an LLM router. Subagents appear as callable tools (`agent_<name>`). Networks nest recursively.
- **Workflow** — deterministic DAG-based orchestration with `Step`, `AgentStep`, `ToolStep`, `ForEach`, `DoUntil`/`DoWhile`. Steps without dependencies run concurrently. Compile-time validation (cycles, missing deps, duplicates).
- **Background agents** — `Spawn()` launches agents in goroutines with `AgentHandle` for lifecycle tracking, cancellation, and `select`-based multiplexing.

### Intelligence

- **Code execution** — LLM writes and runs Python code in a sandboxed subprocess with full tool bridge access (`call_tool`, `call_tools_parallel`, `set_result`). Complex logic, loops, conditionals, error handling via try/except.
- **Plan execution** — LLM batches multiple tool calls in a single turn via `execute_plan`. All steps run in parallel without re-sampling. Reduces latency and tokens for fan-out patterns.
- **Dynamic configuration** — per-request resolution of prompt, model, and tool set via `WithDynamicPrompt`, `WithDynamicModel`, `WithDynamicTools`. Multi-tenant personalization, tier-based model selection, role-based tool gating.
- **Structured output** — `WithResponseSchema` enforces JSON output at the agent level. `SchemaObject` typed builder for compile-time safety.
- **Suspend/Resume** — pause agent or workflow execution to await external input, then continue from where it left off.

### Memory & RAG

- **Conversation memory** — load/persist history per thread with `MaxHistory` and `MaxTokens` trimming.
- **Cross-thread recall** — semantic search across all threads with cosine similarity filtering.
- **User memory** — LLM-extracted facts with semantic deduplication, confidence decay, and contradiction supersession. Runs automatically after each turn.
- **Graph RAG** — LLM-based graph extraction during ingestion discovers 8 relationship types between chunks. `GraphRetriever` combines vector search with multi-hop BFS traversal. Persistent `GraphStore` in all three backends.
- **Hybrid retrieval** — `HybridRetriever` fuses vector search + FTS keyword search with Reciprocal Rank Fusion, parent-child chunk resolution, and optional LLM re-ranking.
- **Semantic chunking** — embedding-based topic boundary detection alongside recursive and markdown-aware chunkers.
- **Skills** — database-persisted instruction packages with semantic search. Agents discover and create skills for each other.

### Streaming & Events

- **Structured streaming** — `StreamEvent` with 5 typed events: `TextDelta`, `ToolCallStart`, `ToolCallResult`, `AgentStart`, `AgentFinish`. Full visibility into agent execution.
- **Execution traces** — every `AgentResult` includes `Steps []StepTrace` with per-tool timing, token usage, and input/output. No OTEL setup required.
- **SSE helper** — `ServeSSE` streams agent responses as Server-Sent Events with zero boilerplate.

### Resilience

- **Retry** — `WithRetry` wraps any provider with exponential backoff on 429/503.
- **Rate limiting** — `WithRateLimit` with sliding-window RPM and TPM accounting. Blocks requests until budget allows.
- **Batch processing** — `BatchProvider` and `BatchEmbeddingProvider` for async offline jobs at reduced cost.
- **Processor pipeline** — `PreProcessor`, `PostProcessor`, `PostToolProcessor` hooks for guardrails, PII redaction, logging. `ErrHalt` short-circuits execution.
- **Human-in-the-loop** — `InputHandler` lets agents pause and ask humans for input, both LLM-driven (`ask_user` tool) and programmatic.

### Observability

- **Deep tracing** — `Tracer` and `Span` interfaces in the root package. Span hierarchy: `agent.execute` → `agent.memory.load` / `agent.loop.iteration` → `agent.memory.persist`. Zero overhead when no tracer configured.
- **Structured logging** — all framework logging uses `slog`. Pass `WithLogger(*slog.Logger)` to any agent.
- **OTEL integration** — `observer.NewTracer()` backed by the global `TracerProvider`.

## Agents in Depth

### LLMAgent

```go
researcher := oasis.NewLLMAgent("researcher", "Searches the web", llm,
    oasis.WithTools(searchTool, knowledgeTool),
    oasis.WithPrompt("You are a research specialist."),
    oasis.WithMaxIter(5),
    oasis.WithCodeExecution(runner),     // let the LLM write and run code
    oasis.WithPlanExecution(),           // let the LLM batch tool calls
    oasis.WithTracer(observer.NewTracer()),
)
```

### Network

```go
researcher := oasis.NewLLMAgent("researcher", "Searches for information", llm,
    oasis.WithTools(searchTool),
)
writer := oasis.NewLLMAgent("writer", "Writes polished content", llm)

team := oasis.NewNetwork("team", "Research and writing team", router,
    oasis.WithAgents(researcher, writer),
    oasis.WithTools(knowledgeTool),
)

// Networks compose recursively — a Network is just another Agent
org := oasis.NewNetwork("org", "Full organization", ceo,
    oasis.WithAgents(team, opsTeam),
)
```

### Workflow

```go
pipeline, err := oasis.NewWorkflow("research-pipeline", "Research and write",
    oasis.Step("prepare", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        wCtx.Set("query", "Research: "+wCtx.Input())
        return nil
    }),
    oasis.AgentStep("research", researcher, oasis.InputFrom("query"), oasis.After("prepare")),
    oasis.AgentStep("write", writer, oasis.InputFrom("research.output"), oasis.After("research")),
    oasis.WithOnError(func(step string, err error) { log.Printf("%s failed: %v", step, err) }),
)

result, err := pipeline.Execute(ctx, oasis.AgentTask{Input: "Go error handling"})
```

Step types: `Step` (function), `AgentStep` (delegate to Agent), `ToolStep` (call a tool), `ForEach` (iterate with concurrency), `DoUntil`/`DoWhile` (loop). Workflows can also be defined from JSON at runtime via `FromDefinition` for visual workflow builders.

### Streaming

```go
if sa, ok := agent.(oasis.StreamingAgent); ok {
    ch := make(chan oasis.StreamEvent)
    go func() {
        for event := range ch {
            switch event.Type {
            case oasis.EventTextDelta:
                fmt.Print(event.Content)
            case oasis.EventToolCallStart:
                fmt.Printf("\n[calling %s]\n", event.Name)
            case oasis.EventToolCallResult:
                fmt.Printf("[%s returned]\n", event.Name)
            }
        }
    }()
    result, err := sa.ExecuteStream(ctx, task, ch)
}
```

### Background Agents

```go
h := oasis.Spawn(ctx, agent, task)

fmt.Println(h.State()) // Running, Completed, Failed, Cancelled
result, err := h.Wait()
h.Cancel()
```

## Core Interfaces

| Interface | Purpose |
| --------- | ------- |
| `Provider` | LLM backend — `Chat`, `ChatStream` |
| `EmbeddingProvider` | Text-to-vector embedding |
| `Store` | Persistence with vector search, keyword search, graph storage |
| `MemoryStore` | Long-term semantic memory (facts, confidence, decay) |
| `Tool` | Pluggable capability for LLM function calling |
| `Agent` | Composable work unit — `LLMAgent`, `Network`, `Workflow`, or custom |
| `StreamingAgent` | Token streaming with structured events |
| `InputHandler` | Human-in-the-loop — pause and request human input |
| `Tracer` / `Span` | Tracing abstraction (zero OTEL imports in your code) |
| `Retriever` | Composable retrieval with re-ranking |
| `CodeRunner` | Sandboxed code execution with tool bridge |

## Included Implementations

| Component | Packages |
| --------- | -------- |
| **Providers** | `provider/gemini` (Google Gemini), `provider/openaicompat` (OpenAI, Groq, Together, DeepSeek, Mistral, Ollama, vLLM, LM Studio, OpenRouter, Azure, and any OpenAI-compatible API) |
| **Storage** | `store/sqlite` (local, pure-Go), `store/libsql` (Turso/remote), `store/postgres` (PostgreSQL + pgvector). All three support `Store`, `MemoryStore`, `GraphStore`, and `KeywordSearcher` |
| **Tools** | `tools/knowledge` (RAG), `tools/remember`, `tools/search` (web), `tools/schedule`, `tools/shell`, `tools/file`, `tools/http`, `tools/data` (CSV/JSON transform), `tools/skill` (agent skill management) |
| **Code** | `code` (sandboxed Python subprocess with tool bridge) |
| **Retrieval** | `HybridRetriever` (vector + FTS + RRF), `GraphRetriever` (multi-hop BFS), `ScoreReranker`, `LLMReranker` |
| **Ingestion** | `ingest` (HTML, Markdown, CSV, JSON, DOCX, PDF extractors; recursive, markdown, semantic chunkers; parent-child strategy) |
| **Observability** | `observer` (OpenTelemetry-backed `Tracer` implementation) |

## Installation

```bash
go get github.com/nevindra/oasis
```

Requires Go 1.24+.

## Project Structure

```text
oasis/
|-- types.go, provider.go, tool.go     # Core interfaces and domain types
|-- store.go, memory.go
|-- agent.go, llmagent.go, network.go   # Agent primitives
|-- workflow.go                         # DAG orchestration
|-- processor.go                        # Processor pipeline
|-- input.go                            # Human-in-the-loop
|-- retriever.go                        # Retrieval pipeline
|-- handle.go                           # Spawn() + AgentHandle
|
|-- provider/gemini/                    # Google Gemini provider
|-- provider/openaicompat/              # OpenAI-compatible provider
|-- store/sqlite/                       # Local SQLite (pure-Go, no CGO)
|-- store/libsql/                       # Remote Turso store
|-- store/postgres/                     # PostgreSQL + pgvector
|-- code/                              # Sandboxed code execution
|-- observer/                           # OTEL observability
|-- ingest/                             # Document chunking pipeline
|-- tools/                              # Built-in tools
|
|-- cmd/bot_example/                    # Reference application
```

## Configuration

Config loading order: **defaults -> `oasis.toml` -> environment variables** (env vars win).

See [docs/configuration/reference.md](docs/configuration/reference.md) for the full reference.

## Documentation

- [Getting Started](docs/getting-started/) — installation, quick start, reference app
- [Concepts](docs/concepts/) — architecture, interfaces, and primitives
- [Guides](docs/guides/) — how-to guides for building custom components
- [Configuration](docs/configuration/reference.md) — all config options and environment variables
- [API Reference](docs/api/) — complete interface definitions, types, and options
- [Contributing](docs/contributing.md) — engineering principles and coding conventions
- [Deployment](cmd/bot_example/DEPLOYMENT.md) — Docker, cloud deployment for the reference bot

## MCP Docs Server

Oasis ships an MCP (Model Context Protocol) server that exposes framework documentation to AI assistants. Connect it to Claude Code, Cursor, Windsurf, or any MCP-compatible tool.

```json
{
  "mcpServers": {
    "oasis": {
      "type": "stdio",
      "command": "go",
      "args": ["run", "github.com/nevindra/oasis/cmd/mcp-docs@latest"]
    }
  }
}
```

All docs are embedded at build time via `//go:embed`. No network access, no API keys — runs as a local subprocess.

## License

[AGPL-3.0](LICENSE) — commercial licensing available, contact nevindra for details
