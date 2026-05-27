# Oasis

A high-performance Go framework for AI agent systems — fast, reliable, and built to scale as models get smarter. Single agents, multi-agent networks, DAG workflows, graph-powered RAG, sandboxed code execution, and long-term memory.

```go
import oasis "github.com/nevindra/oasis"
```

## Why Oasis?

Oasis is shaped by [four constraints](docs/PHILOSOPHY.md) that apply simultaneously: **Fast. Best-in-class DX. Future-Ready. Safe and Recoverable.** Any design that trades one for another is wrong.

- **Opinions where they earn their keep.** The run loop, memory pipeline, suspend/resume, and message assembly are framework-owned and optimized as a unit. Topology, supervision strategy, and app shape are yours. Providers, tools, stores, and processors are open interfaces — swap any implementation.
- **Agents compose recursively.** An `LLMAgent` is an `Agent`. A `Network` of agents is an `Agent`. A `Workflow` containing both is an `Agent`. Nest them arbitrarily — the same primitives carry from today's tool-calling loop into tomorrow's sub-agent spawning and runtime delegation.
- **No LLM SDKs.** Every provider uses raw `net/http`. You control the bytes. Zero vendor lock-in, minimal dependencies.
- **Codegen-friendly by design.** Consistent shapes across every Tool, Provider, and Store. Predictable verbs. No `any` at the boundary. APIs built so an LLM coding assistant with zero Oasis context writes correct code on the first try.
- **Go-native concurrency.** Parallel tool dispatch, background agents via `Spawn`, DAG workflows with automatic wave execution — all using goroutines and channels.
- **Production primitives, built in.** Rate limiting, retry middleware, batch processing, persistent Graph RAG, unified semantic memory, suspend/resume, prompt caching default-on, structured streaming, framework-enforced tool approval.

## Quick Start

```go
package main

import (
    "context"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/memory"
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/store/sqlite"
)

func main() {
    ctx := context.Background()

    // Any provider — Gemini, OpenAI, Groq, Ollama, DeepSeek, Mistral, vLLM, …
    llm := gemini.New(apiKey, "gemini-2.5-flash")
    embed := gemini.NewEmbedding(apiKey, "text-embedding-004", 768)

    store, _ := sqlite.New(ctx, "app.db")

    agent := oasis.NewAgent("assistant", "Helpful research assistant", llm,
        oasis.WithPrompt("You are a helpful research assistant."),
        oasis.WithMemory(
            memory.WithStore(store),
            memory.WithEmbedding(embed),
            memory.WithSemanticRecall(),
        ),
    )

    result, _ := agent.Execute(ctx, oasis.AgentTask{Input: "What is quantum computing?"})
    println(result.Text())
}
```

## Features

### Agent Primitives

- **LLMAgent** — single LLM with tools. Runs a tool-calling loop until the model produces a final response. Multiple tool calls execute in parallel automatically.
- **Network** — coordinates multiple agents via an LLM router. Subagents appear as callable tools (`agent_<name>`). Built-in supervision policies: `RestartOnFail`, `Fallback`, `Quorum`, `CircuitBreaker`, `Chain`. Runtime membership via `AddAgent`/`RemoveAgent`. LLM-driven sub-agent creation via `WithDynamicSpawning`.
- **Workflow** — deterministic DAG-based orchestration with `Step`, `AgentStep`, `ForEach`, `DoUntil`/`DoWhile`. Steps without dependencies run concurrently. Compile-time validation (cycles, missing deps, duplicates).
- **Background agents** — `Spawn` launches agents in goroutines with `AgentHandle` for lifecycle tracking, cancellation, and `select`-based multiplexing.

### Execution

- **Single `Execute` contract** — every `core.Agent` implements `Execute(ctx, task, ...core.RunOption)`. Streaming and per-call overrides flow through `RunOption` values (`core.WithStream(ch)`, `core.WithDeadline(t)`, `agent.WithOverrides(opts)`) — no separate `ExecuteStream` / `ExecuteWith` variants to remember.
- **Prompt caching default-on.** Anthropic and OpenAI-compatible providers automatically stamp ephemeral cache breakpoints on the system prompt and most recent user/tool message; `Usage.CachedTokens` and `CacheCreationTokens` report hit/warm cost. Opt out via `agent.WithoutPromptCaching()`.
- **Transparent tool-result chunking.** Oversized results split into sequential tool-role messages by the loop itself — no separate retrieval tool needed.
- **Dynamic configuration** — per-request resolution of prompt, model, and tool set via `WithDynamicPrompt`, `WithDynamicModel`, `WithDynamicTools`. Multi-tenant personalization, tier-based model selection, role-based tool gating.
- **Structured output** — `WithResponseSchema` enforces JSON output at the agent level. Top-level array schemas additionally emit one element-delta event per completed element.
- **Suspend/Resume** — pause agent or workflow execution to await external input, then continue from where it left off. Typed contracts via `SuspendProtocol[Req, Resp]`.

### Memory & RAG

- **Unified memory** — single `oasis.WithMemory(...)` entry point over a single `MemoryItem` model (facts, working memory, events, playbooks, reflections, summaries — discriminated by `Kind`). Replaces the older `WithUserMemory` + `WithHistory` split.
- **Conversation history** — load/persist per thread with `memory.WithHistory`. Cross-thread semantic recall with `memory.WithSemanticRecall` and `memory.WithSemanticRecallMinScore`.
- **Long-term facts** — semantic deduplication, confidence decay, and contradiction supersession. Auto-extracted from each turn when enabled.
- **Graph RAG** — LLM-based graph extraction during ingestion discovers 8 relationship types between chunks. `GraphRetriever` combines vector search with multi-hop BFS traversal.
- **Hybrid retrieval** — `HybridRetriever` fuses vector search + FTS keyword search with Reciprocal Rank Fusion, parent-child chunk resolution, and optional LLM re-ranking.
- **Semantic chunking** — embedding-based topic boundary detection alongside recursive and markdown-aware chunkers.
- **Skills** — file-based instruction packages (AgentSkills-compatible). Agents discover, activate, and create skills at runtime via `SkillProvider`.

### Streaming & Events

- **Full lifecycle envelope** — every run brackets with `EventRunStart` / `EventRunFinish`. Iterations bracket with `EventIterationStart` / `EventIterationFinish`. 30 typed event kinds cover text deltas, reasoning, tool calls, structured object snapshots, suspend payloads, approval gates, warnings, and errors.
- **Multi-reader stream wrapper** — `oasis.Subscribe(ctx, ag, task, opts...)` returns a `Stream` with blocking accessors (`Text()`, `ToolCalls()`, `Reasoning()`, `Object()`, `Result()`), live subscription via `Events()`, and filtered callbacks (`OnTextDelta`, `OnReasoningDelta`, `OnToolCall`).
- **Typed object adapters** — `oasis.StreamObjectAs[T](stream)` for incremental partial-JSON snapshots; `oasis.ResultObjectAs[T](result)` for the final decoded object.
- **Execution traces** — every `AgentResult` includes `Iterations []IterationTrace` with per-iteration timing, token usage (including cache metrics), finish reason, and inner tool-call traces. No OTel setup required.
- **SSE helper** — `agent.ServeSSE` streams agent responses as Server-Sent Events with zero boilerplate.

### Resilience

- **Provider middleware** — `provider.Middleware` + `provider.Chain` compose retry, rate limiting, caching, and custom wrappers into a single provider stack. Built-in: `agent.RetryMiddleware`, `ratelimit.RateLimitMiddleware`.
- **Per-tool policies** — `core.ToolPolicy` with `Timeout`, `Retries`, `RetryDelay`, `MaxRetryDelay`, `RetryOn`. Attach via `agent.ToolConfig.Policies` (exact name) or `PolicyMatchers` (prefix/glob).
- **Tool middleware** — `LoggingMiddleware`, `TimingMiddleware`, `TransformMiddleware`, `OTelSpanMiddleware`. Innermost-first ordering matches `net/http`.
- **Framework-enforced tool approval** — `agent.Approval(toolName, opts...)` pauses tool execution for human approval via the configured `InputHandler`. Composes with logging, tracing, policy, and any custom middleware. Emits `EventToolApprovalPending` before prompting.
- **Processor pipeline** — `PreProcessor`, `PostProcessor`, `PostToolProcessor` hooks for guardrails, PII redaction, logging. `*ErrHalt` short-circuits execution.
- **Human-in-the-loop** — `InputHandler` lets agents pause and ask humans for input, both LLM-driven (`ask_user` tool) and programmatic.
- **Batch processing** — `BatchProvider` and `BatchEmbeddingProvider` for async offline jobs at reduced cost.

### Observability

- **Deep tracing** — `core.Tracer` and `core.Span` interfaces. Span hierarchy: `agent.execute` → `agent.iteration` → `llm.generate`, plus `agent.memory.load` / `agent.memory.persist` / `tool.dispatch`. Zero overhead when no tracer configured.
- **Structured logging** — all framework logging uses `slog`. Pass `WithLogger(*slog.Logger)` to any agent.
- **OTel integration** — `observer.NewTracer()` backed by the global `TracerProvider`.

## Agents in Depth

### LLMAgent

```go
researcher := oasis.NewAgent("researcher", "Searches the web", llm,
    oasis.WithTools(searchTool, knowledgeTool),
    oasis.WithPrompt("You are a research specialist."),
    oasis.WithLimits(oasis.Limits{MaxIter: 5}),
    oasis.WithTracer(observer.NewTracer()),
)
```

### Network

```go
import "github.com/nevindra/oasis/network"

researcher := oasis.NewAgent("researcher", "Searches for information", llm,
    oasis.WithTools(searchTool),
)
writer := oasis.NewAgent("writer", "Writes polished content", llm)

team := oasis.NewNetwork("team", "Research and writing team", router,
    network.WithChildren(researcher, writer),
    network.WithSupervisorFor("researcher", network.RestartOnFail(2)),
)

// Networks compose recursively — a Network is just another Agent.
org := oasis.NewNetwork("org", "Full organization", ceoRouter,
    network.WithChildren(team, opsTeam),
)
```

### Workflow

```go
import "github.com/nevindra/oasis/workflow"

pipeline, _ := oasis.NewWorkflow("research-pipeline", "Research and write",
    workflow.AgentStep("research", researcher),
    workflow.AgentStep("write", writer, workflow.After("research"),
        workflow.InputFrom("research")),
)

result, _ := pipeline.Execute(ctx, oasis.AgentTask{Input: "Go error handling"})
final := result.Steps["write"].Output  // step outputs are addressable by name
```

Step types: `Step` (function), `AgentStep` (delegate to any `Agent`), `ForEach` (iterate with concurrency), `DoUntil`/`DoWhile` (loop). Workflows can also be defined from JSON at runtime via `FromDefinition` for visual workflow builders.

### Streaming

```go
ch := make(chan oasis.StreamEvent, 64)
go func() {
    for ev := range ch {
        switch ev.Type {
        case core.EventTextDelta:
            fmt.Print(ev.Content)
        case core.EventToolCallStart:
            fmt.Printf("\n[calling %s]\n", ev.Name)
        case core.EventToolCallResult:
            fmt.Printf("[%s returned]\n", ev.Name)
        case core.EventRunFinish:
            fmt.Printf("\n[done: %s]\n", ev.FinishReason)
        }
    }
}()
_ = agent.Execute(ctx, task, oasis.WithStream(ch))
```

Or use the multi-reader wrapper:

```go
s := oasis.Subscribe(ctx, agent, task)
s.OnTextDelta(func(chunk string) { fmt.Print(chunk) })
result, _ := s.Result()
```

### Background Agents

```go
h := oasis.Spawn(ctx, agent, task)

fmt.Println(h.State()) // Pending, Running, Completed, Failed, Cancelled
result, err := h.Wait()
h.Cancel()
```

## Core Interfaces

| Interface | Purpose |
| --------- | ------- |
| `core.Provider` | LLM backend — `ChatStream`, `Name`. `core.Chat` is a convenience function |
| `core.EmbeddingProvider` | Text-to-vector embedding |
| `core.Store` | Persistence with vector search; capability interfaces (`KeywordSearcher`, `GraphStore`, `ScheduledActionStore`, `DocumentGetter`) discovered via type assertion |
| `core.MemoryItemStore` | Unified persistent memory over `MemoryItem` |
| `core.AnyTool` / `core.Tool[In, Out]` | Pluggable capability for LLM function calling — atomic (one tool, one operation) |
| `core.Agent` | Composable work unit — `LLMAgent`, `Network`, `Workflow`, or custom. Single `Execute(ctx, task, ...RunOption)` method |
| `core.InputHandler` | Human-in-the-loop — pause and request human input |
| `core.Tracer` / `core.Span` | Tracing abstraction (zero OTel imports in your code) |
| `core.Retriever` | Composable retrieval with re-ranking |
| `core.Sandbox` | Sandboxed code/shell/file/browser execution (`Close() error`) |
| `core.Compactor` | Per-thread conversation compaction with a structured summary format |

## Included Implementations

| Component | Packages |
| --------- | -------- |
| **Providers** | `provider/gemini` (Google Gemini), `provider/openaicompat` (OpenAI, Groq, Together, DeepSeek, Mistral, Ollama, vLLM, LM Studio, OpenRouter, Azure, and any OpenAI-compatible API). Anthropic native via `provider/openaicompat` with the Messages-API endpoint. |
| **Storage** | `store/sqlite` (local, pure-Go, no CGO), `store/postgres` (PostgreSQL + pgvector). Both implement `Store`, `MemoryItemStore`, `GraphStore`, `KeywordSearcher`, and `ScheduledActionStore`. |
| **Tools** | `tools/data` (CSV/JSON transform), `tools/http` (typed fetch). Sandbox tools (`shell`, `code_execute`, `file_read`, `file_write`, `file_edit`, `browser`, `deliver_file`, MCP wrappers) are auto-registered via `agent.WithSandbox(sb, sandbox.Tools(sb)...)`. |
| **Sandbox** | `sandbox` (interface + `Tools()` + `Lazy()` deferred-init wrapper); implementations live in separate repos (e.g. [`oasis-sandbox-ix`](https://github.com/nevindra/oasis-sandbox-ix) — Docker-backed). |
| **MCP** | `mcp` client over stdio + HTTP. Tools register under `mcp__<server>__<tool>` namespacing. Deferred-schema mode for many-server setups. File-based config loader at `mcp/config` (Claude Desktop schema compatible). |
| **Retrieval** | `rag.HybridRetriever` (vector + FTS + RRF), `rag.GraphRetriever` (multi-hop BFS), `rag.ScoreReranker`, `rag.LLMReranker`. |
| **Ingestion** | `ingest` (HTML, Markdown, CSV, JSON, DOCX, PDF extractors; recursive, markdown, semantic chunkers; parent-child strategy). |
| **Observability** | `observer` (OpenTelemetry-backed `Tracer` implementation). |
| **Guardrails / rate limit / compaction** | `guardrail` (injection, content, keyword, max-tool-calls), `ratelimit` (RPM/TPM sliding-window), `compaction` (`StructuredCompactor` with 9-section summary). |

## Installation

```bash
go get github.com/nevindra/oasis
```

Requires Go 1.24+.

## Project Structure

Hybrid microkernel: a single curated root package (`github.com/nevindra/oasis`) re-exports protocol types and the most common APIs. Implementation lives in focused subpackages.

```text
oasis/
|-- oasis.go                        # Curated public umbrella
|-- doc.go                          # Top-level package documentation
|-- batch.go                        # Batch primitives (BatchJob, BatchStats)
|
|-- core/                           # Protocol types + interfaces + Erase helper
|                                   #   (leaf package — depends on nothing in oasis)
|-- agent/                          # LLMAgent + Spawn + functional options
|-- network/                        # Multi-agent orchestration + supervision
|-- workflow/                       # DAG-based orchestration
|-- memory/                         # Unified MemoryItem + ingest/retrieve pipelines
|-- compaction/                     # Compaction processors
|-- guardrail/                      # Guardrail processors
|-- ratelimit/                      # Rate limiter wrapper
|-- skills/                         # Skill loader + asset embedding
|-- processor/                      # ProcessorChain helper
|-- provider/{catalog,resolve}/     # Stdlib-only model registry helpers
|
|-- tools/{data,http}/              # Built-in tools
|-- cmd/{mcp-docs,modelgen}/        # CLI utilities
|
|-- (heavy / optional-dep subpackages)
|   |-- mcp/                        # MCP client integration
|   |-- store/{sqlite,postgres}/    # Storage backends
|   |-- provider/{gemini,openaicompat}/  # LLM providers
|   |-- observer/                   # OTel observability (full OTel SDK)
|   |-- ingest/                     # Document ingestion (PDF, DOCX, embeddings)
|   |-- sandbox/                    # Sandbox interface + Tools()
|   |-- rag/                        # Retrieval-augmented generation
```

All ship in a single root `go.mod`. Go 1.17+ lazy module loading keeps heavy deps (pgx, OTel, PDF, Docker) out of downstream builds that only import the umbrella.

## Documentation

- [Getting Started](docs/external/getting-started/index.md) — install, first agent, project layout
- [Agent](docs/external/agent/index.md) — LLMAgent, streaming, scheduling, suspend/resume
- [Network](docs/external/network/index.md) — multi-agent orchestration and supervision
- [Workflow](docs/external/workflow/index.md) — DAG orchestration
- [Memory](docs/external/memory/index.md) — unified memory, recall, compaction
- [RAG](docs/external/rag/index.md) — ingestion, retrieval, Graph RAG
- [Skills](docs/external/skills/index.md) — skill providers and runtime activation
- [Tools](docs/external/tools/index.md) — built-in and custom tools
- [Sandbox](docs/external/sandbox/index.md) — code execution, shell, files, browser
- [Providers](docs/external/providers/index.md) — Gemini, OpenAI-compat, Anthropic
- [Store](docs/external/store/index.md) — SQLite, Postgres
- [Processors](docs/external/processors/index.md) — guardrails, HITL, processor chain
- [Observability](docs/external/observability/index.md) — tracing, logging, OTel

Each topic folder contains `index.md` (concept), `api.md` (reference), and `examples.md` (recipes).

- [Philosophy](docs/PHILOSOPHY.md) — the four constraints, API strategy, framework identity.

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
