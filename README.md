# Oasis

An AI agent framework for Go. Build tool-calling agents, multi-agent networks, and conversational assistants with composable, interface-driven primitives.

```go
import oasis "github.com/nevindra/oasis"
```

## Features

- **Composable agents** -- `LLMAgent` for single-provider tool loops, `Network` for multi-agent coordination, `Workflow` for deterministic DAG-based orchestration. All three nest recursively. Multiple tool calls execute in parallel automatically.
- **Streaming** -- `StreamingAgent` interface with channel-based token streaming. Tool-calling iterations run in blocking mode; the final response streams token-by-token. Built-in edit batching for messaging platforms.
- **Memory & recall** -- conversation history (`WithConversationMemory`), cross-thread semantic search (`CrossThreadSearch`), and user fact injection (`WithUserMemory`). Built into `LLMAgent` and `Network`.
- **Processor pipeline** -- `PreProcessor`, `PostProcessor`, `PostToolProcessor` hooks for guardrails, PII redaction, logging, and custom middleware.
- **Human-in-the-loop** -- `InputHandler` interface for agents to pause and request human input, both LLM-driven (`ask_user` tool) and programmatic (processor gates).
- **Background agents** -- `Spawn()` launches agents in background goroutines with `AgentHandle` for state tracking, cancellation, and `select`-based multiplexing.
- **Interface-driven** -- every component (LLM, storage, tools, frontends, memory) is a Go interface. Swap implementations without touching the rest of the system.
- **Built-in tools** -- knowledge search (RAG), web search, scheduled actions, shell execution, file I/O, HTTP requests.
- **Observability** -- OpenTelemetry wrappers for providers, tools, embeddings, and agent executions with cost tracking.
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

### Workflow

Deterministic, DAG-based task orchestration. Steps run in dependency order with automatic parallelism. Use it when you know the execution order at build time.

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

Step types: `Step` (function), `AgentStep` (delegate to Agent), `ToolStep` (call a tool), `ForEach` (iterate with concurrency), `DoUntil`/`DoWhile` (loop). See [docs/framework/workflows.md](docs/framework/workflows.md) for the full guide.

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

### Human-in-the-Loop

The `InputHandler` interface lets agents pause execution and ask a human for input. Two patterns:

- **LLM-driven** -- the LLM calls a built-in `ask_user` tool when it decides it needs clarification.
- **Programmatic** -- processors or workflow steps retrieve the handler from context via `InputHandlerFromContext(ctx)` for approval gates, review steps, etc.

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(searchTool),
    oasis.WithInputHandler(myHandler), // enables ask_user tool + context propagation
)
```

Networks propagate the handler to all subagents automatically.

### Memory & Recall

Agents can load conversation history, recall relevant context from past threads, and inject user facts into the system prompt -- all via options.

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(searchTool),
    oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding)), // load/persist history + cross-thread recall
    oasis.WithUserMemory(memoryStore, embedding),                           // inject user facts into system prompt
)

result, err := agent.Execute(ctx, oasis.AgentTask{
    Input: "What did we discuss yesterday?",
    Context: map[string]any{
        oasis.ContextThreadID: "thread-123",
        oasis.ContextUserID:   "user-42",
    },
})
```

### Streaming

Both `LLMAgent` and `Network` implement the `StreamingAgent` interface. Tool-calling iterations run in blocking mode; only the final response streams token-by-token.

```go
if sa, ok := agent.(oasis.StreamingAgent); ok {
    ch := make(chan string)
    go func() {
        for token := range ch {
            fmt.Print(token)
        }
    }()
    result, err := sa.ExecuteStream(ctx, task, ch)
}
```

### Background Agents

`Spawn()` launches an agent in a background goroutine and returns an `AgentHandle` for tracking, awaiting, and cancelling.

```go
h := oasis.Spawn(ctx, agent, task)

// Check state without blocking
fmt.Println(h.State()) // Running, Completed, Failed, Cancelled

// Wait for completion
result, err := h.Wait()

// Or cancel
h.Cancel()
```

## Core Interfaces

| Interface | Purpose |
| --------- | ------- |
| `Provider` | LLM backend -- `Chat`, `ChatWithTools`, `ChatStream` |
| `EmbeddingProvider` | Text-to-vector embedding |
| `Store` | Persistence with vector search |
| `MemoryStore` | Long-term semantic memory (facts, confidence, decay) |
| `Tool` | Pluggable capability for LLM function calling |
| `Frontend` | Messaging platform -- `Poll`, `Send`, `Edit` |
| `Agent` | Composable work unit -- `LLMAgent`, `Network`, `Workflow`, or custom |
| `StreamingAgent` | Optional `Agent` capability -- `ExecuteStream` with channel-based token streaming |
| `InputHandler` | Human-in-the-loop -- pause agent and request human input |

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
|-- workflow.go                        # Workflow primitive (DAG orchestration)
|-- processor.go                        # Processor pipeline
|-- input.go                            # Human-in-the-loop (InputHandler)
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
- [Workflows](docs/framework/workflows.md) -- deterministic DAG-based task orchestration
- [Configuration](docs/framework/configuration.md) -- all config options and environment variables
- [Extending Oasis](docs/framework/extending.md) -- adding custom tools, providers, frontends, and stores
- [API Reference](docs/framework/api-reference.md) -- complete interface definitions and types
- [Deployment](cmd/bot_example/DEPLOYMENT.md) -- Docker, cloud deployment for the reference bot

## License

[Apache-2.0](LICENSE)
