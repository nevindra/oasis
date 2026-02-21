# Oasis

An AI agent framework for Go. Composable primitives for tool-calling agents, multi-agent networks, deterministic workflows, Graph RAG, code execution, and long-term memory — designed to evolve alongside AI capabilities.

```go
import oasis "github.com/nevindra/oasis"
```

## Architecture

```mermaid
graph TB
    subgraph APP["Your Application"]
        WIRE[Wiring & Config]
    end

    subgraph AGENTS["Agent Primitives"]
        direction LR
        LLMA["LLMAgent<br/><small>tool-calling loop</small>"]
        NET["Network<br/><small>multi-agent router</small>"]
        WF["Workflow<br/><small>DAG orchestration</small>"]
        SCHED["Scheduler<br/><small>time-based</small>"]
    end

    subgraph INTELLIGENCE["Intelligence Layer"]
        direction LR
        CODE["Code Execution<br/><small>sandboxed Python</small>"]
        PLAN["Plan Execution<br/><small>parallel tool batch</small>"]
        DYN["Dynamic Config<br/><small>per-request model/prompt/tools</small>"]
    end

    subgraph MEMORY["Memory & RAG"]
        direction LR
        CONV["Conversation<br/><small>history + cross-thread</small>"]
        FACTS["User Memory<br/><small>facts + decay</small>"]
        GRAPH["Graph RAG<br/><small>multi-hop BFS</small>"]
        HYBRID["Hybrid Retrieval<br/><small>vector + FTS + RRF</small>"]
    end

    subgraph PIPELINE["Processing Pipeline"]
        direction LR
        PRE["PreProcessor"]
        POST["PostProcessor"]
        PTOOL["PostToolProcessor"]
        INPUT["InputHandler<br/><small>human-in-the-loop</small>"]
    end

    subgraph PROVIDERS["LLM Providers"]
        direction LR
        GEM["Gemini"]
        OAI["OpenAI-compat<br/><small>OpenAI · Groq · Ollama<br/>DeepSeek · Mistral · vLLM</small>"]
    end

    subgraph STORAGE["Storage Backends"]
        direction LR
        SQL["SQLite<br/><small>pure-Go, no CGO</small>"]
        PG["PostgreSQL<br/><small>pgvector HNSW</small>"]
        LIB["libSQL<br/><small>Turso</small>"]
    end

    subgraph TOOLS["Built-in Tools"]
        direction LR
        KNOW["knowledge"]
        SEARCH["search"]
        SHELL["shell"]
        FILE["file"]
        HTTP2["http"]
        DATA["data"]
        SKILL["skill"]
        REM["remember"]
    end

    subgraph OBS["Observability"]
        direction LR
        TRACE["Tracer / Span"]
        LOG["slog"]
        OTEL["OpenTelemetry"]
    end

    WIRE --> AGENTS
    LLMA --> INTELLIGENCE
    LLMA --> MEMORY
    LLMA --> PIPELINE
    LLMA --> PROVIDERS
    LLMA --> STORAGE
    LLMA --> TOOLS
    LLMA --> OBS
    NET --> LLMA
    WF --> LLMA
    SCHED --> LLMA

    style APP fill:#f5f5f5,stroke:#999
    style AGENTS fill:#e1f5fe,stroke:#0288d1
    style INTELLIGENCE fill:#fff8e1,stroke:#f9a825
    style MEMORY fill:#e8f5e9,stroke:#2e7d32
    style PIPELINE fill:#fce4ec,stroke:#c62828
    style PROVIDERS fill:#ede7f6,stroke:#512da8
    style STORAGE fill:#e0f2f1,stroke:#00695c
    style TOOLS fill:#fff3e0,stroke:#e65100
    style OBS fill:#f3e5f5,stroke:#7b1fa2
```

Everything above is a Go interface (or built on one). Swap any box without touching the others.

---

## How It Works

Oasis agents run a simple loop: receive input → call LLM → execute tools → repeat until done.

```mermaid
sequenceDiagram
    participant App
    participant Agent as LLMAgent
    participant LLM as Provider
    participant T as Tools
    participant S as Store

    App->>Agent: Execute(task)
    Agent->>S: Load conversation history
    Agent->>S: Load user facts
    Agent->>LLM: ChatWithTools(messages)

    loop Tool-calling loop
        LLM-->>Agent: Tool calls
        Agent->>T: Dispatch (parallel)
        T-->>Agent: Results
        Agent->>LLM: ChatWithTools(messages + results)
    end

    LLM-->>Agent: Final response
    Agent->>S: Persist history (background)
    Agent->>S: Extract user facts (background)
    Agent-->>App: AgentResult
```

When an agent is wrapped in a **Network**, the LLM router decides which subagent to invoke. When it's a step in a **Workflow**, the DAG engine schedules it based on dependencies.

---

## Core Primitives

### Agent Primitives

```mermaid
graph LR
    TASK["AgentTask"] --> A["LLMAgent"]
    TASK --> N["Network"]
    TASK --> W["Workflow"]

    A -->|"is an"| IFACE["Agent interface"]
    N -->|"is an"| IFACE
    W -->|"is an"| IFACE

    N -->|"contains"| A
    W -->|"contains"| A
    W -->|"contains"| N
    N -->|"contains"| W

    style IFACE fill:#e1f5fe,stroke:#0288d1
    style A fill:#e1f5fe,stroke:#0288d1
    style N fill:#fff3e0,stroke:#e65100
    style W fill:#e8f5e9,stroke:#2e7d32
```

| Primitive | Routing | Parallelism | Best for |
| --------- | ------- | ----------- | -------- |
| [**LLMAgent**](concepts/agent.md) | N/A — single LLM | Parallel tool dispatch | Tool-calling tasks with a single provider |
| [**Network**](concepts/network.md) | Runtime (LLM decides) | LLM can invoke multiple agents | Open-ended tasks, ambiguous input |
| [**Workflow**](concepts/workflow.md) | Compile-time (you declare) | Steps without deps run concurrently | Pipelines, ETL, multi-step processing |
| [**Scheduler**](concepts/scheduler.md) | Time-based | N/A | Periodic jobs, reminders |

All implement `Agent`, so they compose recursively — a Network can contain Workflows, a Workflow can orchestrate Networks.

### Choosing the Right Primitive

```mermaid
flowchart TD
    START["What are you building?"]
    START -->|"Single LLM + tools"| A["LLMAgent"]
    START -->|"Multiple agents collaborating"| Q1{"Do you know the steps in advance?"}
    START -->|"Periodic / scheduled tasks"| SCHED["Scheduler"]

    Q1 -->|"Yes — fixed pipeline"| WF["Workflow"]
    Q1 -->|"No — depends on input"| NET["Network"]
    Q1 -->|"Mix of both"| BOTH["Workflow with AgentStep → Network"]

    style A fill:#e1f5fe
    style WF fill:#e8f5e9
    style NET fill:#fff3e0
    style SCHED fill:#f3e5f5
    style BOTH fill:#fff9c4
```

---

## Intelligence Capabilities

Beyond basic tool calling, agents can leverage advanced execution patterns:

| Capability | Option | What it does |
| ---------- | ------ | ------------ |
| [**Code Execution**](concepts/code-execution.md) | `WithCodeExecution(runner)` | LLM writes Python, executed in sandbox with `call_tool()` bridge |
| [**Plan Execution**](guides/execution-plans.md) | `WithPlanExecution()` | LLM batches tool calls → all run in parallel → results returned in one turn |
| **Dynamic Config** | `WithDynamicPrompt` / `WithDynamicModel` / `WithDynamicTools` | Per-request prompt, model, and tool resolution |
| **Structured Output** | `WithResponseSchema(schema)` | Enforce JSON output schema on every LLM call |
| **Suspend/Resume** | `Suspend(payload)` / `Resume(ctx, data)` | Pause execution for external input, continue later |

---

## Memory System

```mermaid
graph TB
    subgraph "Per-Thread"
        HIST["Conversation History<br/><small>MaxHistory · MaxTokens</small>"]
    end

    subgraph "Cross-Thread"
        SEM["Semantic Recall<br/><small>cosine similarity filtering</small>"]
    end

    subgraph "Per-User"
        FACTS["User Facts<br/><small>auto-extracted · confidence decay<br/>semantic dedup · supersession</small>"]
    end

    subgraph "Knowledge Base"
        GRAPHRAG["Graph RAG<br/><small>8 relation types · multi-hop BFS<br/>persistent GraphStore</small>"]
        HYBRIDRET["Hybrid Retrieval<br/><small>vector + FTS + RRF<br/>parent-child resolution</small>"]
        SKILLS["Skills<br/><small>agent-created · semantic search</small>"]
    end

    HIST --> LLM["LLM Context"]
    SEM --> LLM
    FACTS --> LLM
    GRAPHRAG --> LLM
    HYBRIDRET --> LLM
    SKILLS --> LLM

    style LLM fill:#e1f5fe,stroke:#0288d1
    style GRAPHRAG fill:#e8f5e9
    style HYBRIDRET fill:#e8f5e9
```

| Feature | Setup | Docs |
| ------- | ----- | ---- |
| **Conversation memory** | `WithConversationMemory(store)` | [Memory guide](guides/memory-and-recall.md) |
| **Cross-thread recall** | `WithConversationMemory(store, CrossThreadSearch(emb))` | [Memory guide](guides/memory-and-recall.md) |
| **User memory** | `WithUserMemory(memStore, emb)` | [Memory guide](guides/memory-and-recall.md) |
| **Graph RAG** | `WithGraphExtraction(provider)` on Ingestor + `GraphRetriever` | [RAG pipeline](guides/rag-pipeline.md) |
| **Hybrid retrieval** | `HybridRetriever` with `WithReranker`, `WithFilters` | [RAG pipeline](guides/rag-pipeline.md) |
| **Skills** | `tools/skill` with `Store` | [Skills guide](guides/skills.md) |

---

## RAG Pipeline

```mermaid
graph LR
    DOC["Document<br/><small>HTML · MD · CSV<br/>JSON · DOCX · PDF</small>"]
    DOC --> EXT["Extract"]
    EXT --> CHUNK["Chunk<br/><small>recursive · markdown<br/>semantic · parent-child</small>"]
    CHUNK --> EMBED["Embed"]
    EMBED --> STORE["Store<br/><small>+ graph edges</small>"]

    QUERY["Query"] --> VEC["Vector Search"]
    QUERY --> KW["Keyword Search"]
    VEC --> RRF["RRF Fusion"]
    KW --> RRF
    RRF --> RERANK["Rerank<br/><small>score · LLM</small>"]
    RERANK --> RESULT["Results"]

    STORE -.-> VEC
    STORE -.-> KW

    style DOC fill:#fff3e0
    style STORE fill:#e0f2f1
    style RESULT fill:#e1f5fe
```

Ingestion and retrieval are fully composable — swap chunkers, add extractors, chain rerankers. See [Ingesting Documents](guides/ingesting-documents.md) and [RAG Pipeline](guides/rag-pipeline.md).

---

## Streaming & Events

Agents emit structured events during execution — not just text tokens:

```mermaid
sequenceDiagram
    participant App
    participant Agent
    participant LLM
    participant Tool

    App->>Agent: ExecuteStream(task, ch)

    Agent->>LLM: ChatStream
    LLM-->>App: EventTextDelta ("Searching...")

    LLM-->>Agent: Tool call
    Agent-->>App: EventToolCallStart (web_search)
    Agent->>Tool: Execute
    Tool-->>Agent: Result
    Agent-->>App: EventToolCallResult (web_search)

    Agent->>LLM: ChatStream (with results)
    LLM-->>App: EventTextDelta ("Based on...")
    LLM-->>App: EventTextDelta ("the results...")

    Agent-->>App: Done
```

Five event types: `EventTextDelta`, `EventToolCallStart`, `EventToolCallResult`, `EventAgentStart`, `EventAgentFinish`. Plus `ServeSSE` for zero-boilerplate Server-Sent Events.

Every `AgentResult` also includes `Steps []StepTrace` — per-tool timing, token usage, and I/O — with no OTEL setup required.

---

## Processing Pipeline

```mermaid
graph LR
    IN["Input"] --> PRE["PreProcessor<br/><small>validation · injection<br/>rate limiting</small>"]
    PRE --> LLM["LLM Call"]
    LLM --> POST["PostProcessor<br/><small>output filtering<br/>tool call validation</small>"]
    POST --> TOOL["Tool Execution"]
    TOOL --> PTOOL["PostToolProcessor<br/><small>result redaction<br/>audit logging</small>"]
    PTOOL --> LLM

    PRE -.->|"ErrHalt"| OUT["Short-circuit"]
    POST -.->|"ErrHalt"| OUT
    PRE -.->|"Suspend"| PAUSE["Pause execution"]

    style OUT fill:#ffcdd2
    style PAUSE fill:#fff9c4
```

See [Processors & Guardrails](guides/processors-and-guardrails.md) and [Human-in-the-Loop](guides/human-in-the-loop.md).

---

## Provider Architecture

```mermaid
graph LR
    P["Any Provider"] --> RETRY["WithRetry<br/><small>429/503 backoff</small>"]
    RETRY --> RATE["WithRateLimit<br/><small>RPM + TPM</small>"]
    RATE --> OBS2["WithTracer<br/><small>span per call</small>"]

    GEM["gemini.New()"] --> P
    OAI["openaicompat.NewProvider()"] --> P

    style P fill:#ede7f6,stroke:#512da8
    style RETRY fill:#fff3e0
    style RATE fill:#fff3e0
    style OBS2 fill:#f3e5f5
```

Provider decorators compose — `WithRateLimit(WithRetry(provider), RPM(60), TPM(100000))`. All providers use raw `net/http`, no LLM SDKs.

Optional: `BatchProvider` and `BatchEmbeddingProvider` for async batch processing at reduced cost.

---

## At a Glance

```go
// Single agent with memory, code execution, and streaming
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(searchTool, knowledgeTool),
    oasis.WithPrompt("You are a helpful assistant."),
    oasis.WithConversationMemory(store, oasis.CrossThreadSearch(embedding)),
    oasis.WithUserMemory(memoryStore, embedding),
    oasis.WithCodeExecution(runner),
    oasis.WithPlanExecution(),
    oasis.WithTracer(observer.NewTracer()),
)

// Multi-agent network
team := oasis.NewNetwork("team", "Research and writing", router,
    oasis.WithAgents(researcher, writer),
)

// Deterministic workflow
pipeline, _ := oasis.NewWorkflow("pipeline", "Research then write",
    oasis.AgentStep("research", researcher),
    oasis.AgentStep("write", writer,
        oasis.InputFrom("research.output"),
        oasis.After("research"),
    ),
)
```

---

## Documentation

| Section | What you'll learn |
| ------- | ----------------- |
| [**Getting Started**](getting-started/index.md) | Installation, first agent, reference app |
| [**Concepts**](concepts/index.md) | How the framework works — one page per primitive |
| [**Guides**](guides/) | Step-by-step how-tos |
| [**Configuration**](configuration/index.md) | All config options and environment variables |
| [**API Reference**](api/interfaces.md) | Complete interface definitions and types |
| [**Contributing**](contributing.md) | Engineering principles and coding conventions |

### Guides

| Guide | Topic |
| ----- | ----- |
| [Custom Tool](guides/custom-tool.md) | Build your own tools |
| [Custom Provider](guides/custom-provider.md) | Implement a new LLM provider |
| [Custom Store](guides/custom-store.md) | Write a storage backend |
| [Custom Agent](guides/custom-agent.md) | Create a custom agent type |
| [Memory & Recall](guides/memory-and-recall.md) | Conversation history, cross-thread, user facts |
| [RAG Pipeline](guides/rag-pipeline.md) | Ingestion, retrieval, Graph RAG |
| [Ingesting Documents](guides/ingesting-documents.md) | Extract, chunk, embed, store |
| [Streaming](guides/streaming.md) | Token streaming and SSE |
| [Processors & Guardrails](guides/processors-and-guardrails.md) | Input/output middleware |
| [Human-in-the-Loop](guides/human-in-the-loop.md) | InputHandler and suspend/resume |
| [Background Agents](guides/background-agents.md) | Spawn, cancel, select |
| [Code Execution](guides/code-execution.md) | Sandboxed Python with tool bridge |
| [Execution Plans](guides/execution-plans.md) | Parallel tool batching |
| [Skills](guides/skills.md) | Agent-created instruction packages |

---

## Requirements

- **Go 1.24+**
- No CGO required (pure-Go SQLite via `modernc.org/sqlite`)

## Installation

```bash
go get github.com/nevindra/oasis
```

This pulls the core framework. Provider, store, and tool packages are imported individually as needed:

```go
import (
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/store/sqlite"
    "github.com/nevindra/oasis/tools/knowledge"
)
```
