# Architecture

This document describes the Oasis framework's component design -- the reusable building blocks, how they connect, and how data flows through them.

For application-level orchestration (routing, intent classification, agent management), see `internal/bot/` which is a reference implementation built on top of these components.

## Component Overview

```
+-------------------+      +--------------------+
|    Frontend       |      |    Provider         |
| (Telegram, etc.)  |      | (Gemini, OpenAI)   |
+-------------------+      +--------------------+
         |                          |
         |                  +-------+-------+
         |                  |  Observer     |  (optional OTEL wrapper)
         |                  | traces/metrics|
         |                  +-------+-------+
         |                          |
         v                          v
+--------------------------------------------------+
|              Your Application                     |
|   (wires components together, defines behavior)   |
+--------------------------------------------------+
         |              |              |
         v              v              v
+-------------+  +-------------+  +-----------+
|    Store    |  | MemoryStore |  |   Tool    |
| (sqlite,    |  | (sqlite)    |  | Registry  |
|  libsql)    |  |             |  |           |
+-------------+  +-------------+  +-----------+
                                       |
                              +--------+--------+
                              |     Agent       |
                              | (LLMAgent,      |
                              |  Network,       |
                              |  Workflow)       |
                              +-----------------+
                                       |
                                       v
                                  +-----------+
                                  | Ingest    |
                                  | Pipeline  |
                                  +-----------+
```

Every box above is a Go interface (except Ingest which is a concrete struct). You can swap any implementation without affecting the others.

## Core Interfaces

### Provider (`provider.go`)

Abstracts the LLM backend. Three capabilities:

| Method | Purpose |
|--------|---------|
| `Chat(ctx, req) -> ChatResponse` | Send request, get complete response |
| `ChatWithTools(ctx, req, tools) -> ChatResponse` | Request with function calling; response may contain `ToolCalls` |
| `ChatStream(ctx, req, ch) -> ChatResponse` | Stream tokens into a channel, then return final response with usage stats |
| `Name() -> string` | Provider identifier (e.g. `"gemini"`) |

**Shipped implementations**: `provider/gemini` (Google Gemini), `provider/openaicompat` (OpenAI-compatible endpoints).

Both implementations use raw HTTP with SSE parsing -- no SDK dependencies.

### EmbeddingProvider (`provider.go`)

Abstracts text-to-vector embedding.

| Method | Purpose |
|--------|---------|
| `Embed(ctx, texts) -> [][]float32` | Batch embed multiple texts |
| `Dimensions() -> int` | Vector dimensionality (e.g. `1536`) |
| `Name() -> string` | Provider identifier |

**Shipped implementation**: `provider/gemini` (Gemini embedding-001, 1536 dimensions).

### Frontend (`frontend.go`)

Abstracts the messaging platform. Designed around a poll-send-edit cycle.

| Method | Purpose |
|--------|---------|
| `Poll(ctx) -> <-chan IncomingMessage` | Long-poll for incoming messages. Returns a channel. |
| `Send(ctx, chatID, text) -> msgID` | Send a new message, returns its ID for later editing |
| `Edit(ctx, chatID, msgID, text)` | Update a message with plain text |
| `EditFormatted(ctx, chatID, msgID, text)` | Update a message with rich formatting (HTML) |
| `SendTyping(ctx, chatID)` | Show typing indicator |
| `DownloadFile(ctx, fileID) -> (data, filename)` | Download an uploaded file |

**Shipped implementation**: `frontend/telegram` (Telegram Bot API with long-polling).

The `Poll` -> `Send` -> `Edit` pattern enables streaming: send a placeholder message, then progressively edit it as LLM tokens arrive.

### Store (`store.go`)

Persistence layer with vector search capabilities. Handles messages, documents/chunks, threads, config, and scheduled actions.

**Message operations:**
- `StoreMessage(ctx, msg)` -- persist a message (with optional embedding)
- `GetMessages(ctx, threadID, limit)` -- recent messages for context window
- `SearchMessages(ctx, embedding, topK)` -- vector search over all messages

**Document/chunk operations:**
- `StoreDocument(ctx, doc, chunks)` -- persist a document and its chunks (with embeddings)
- `SearchChunks(ctx, embedding, topK)` -- vector search over document chunks

**Thread management (full CRUD):**
- `CreateThread(ctx, thread)` -- create a new conversation thread
- `GetThread(ctx, id)` -- get a thread by ID
- `ListThreads(ctx, chatID, limit)` -- list threads for a chat, ordered by most recent
- `UpdateThread(ctx, thread)` -- update title/metadata
- `DeleteThread(ctx, id)` -- delete a thread and its messages

**Key-value config:**
- `GetConfig(ctx, key) -> string`
- `SetConfig(ctx, key, value)`

**Scheduled actions (full CRUD):**
- `CreateScheduledAction`, `ListScheduledActions`, `GetDueScheduledActions`
- `UpdateScheduledAction`, `UpdateScheduledActionEnabled`
- `DeleteScheduledAction`, `DeleteAllScheduledActions`
- `FindScheduledActionsByDescription`

**Skills (full CRUD + vector search):**
- `CreateSkill`, `GetSkill`, `ListSkills`, `UpdateSkill`, `DeleteSkill`
- `SearchSkills(ctx, embedding, topK)` -- vector search over skill embeddings

**Lifecycle:**
- `Init(ctx)` -- create tables/indexes
- `Close()` -- clean up connections

**Shipped implementations**: `store/sqlite` (local pure-Go SQLite), `store/libsql` (remote Turso/libSQL).

Both implementations store embeddings as JSON-serialized float32 arrays and perform brute-force cosine similarity for vector search.

### MemoryStore (`memory.go`)

Long-term semantic memory. Stores user facts with confidence scoring, semantic deduplication, and time-based decay. This interface is optional -- applications can run without it.

| Method | Purpose |
|--------|---------|
| `UpsertFact(ctx, fact, category, embedding)` | Insert or merge a fact (deduplicates by cosine similarity > 0.85) |
| `SearchFacts(ctx, embedding, topK)` | Semantic search over stored facts |
| `BuildContext(ctx, queryEmbedding) -> string` | Build a formatted memory context string (top 15 facts by confidence + recency) |
| `DeleteMatchingFacts(ctx, pattern)` | Delete facts matching a text pattern |
| `DecayOldFacts(ctx)` | Reduce confidence of un-reinforced facts (multiply by 0.95 if not updated in 7+ days) |
| `Init(ctx)` | Create tables |

**Shipped implementation**: `memory/sqlite`.

**Confidence system:**
- New facts start at `confidence = 1.0`
- Re-extracted facts get `+0.1` (capped at 1.0)
- Decay: `confidence *= 0.95` for facts not reinforced in 7+ days
- Pruning: facts with `confidence < 0.3` and `age > 30 days` are removed

### Tool + ToolRegistry (`tool.go`)

Pluggable tool system for LLM function calling.

**Tool interface:**
```go
type Tool interface {
    Definitions() []ToolDefinition
    Execute(ctx context.Context, name string, args json.RawMessage) (ToolResult, error)
}
```

A single `Tool` implementation can expose multiple tool functions via `Definitions()`. The `Execute` method receives the tool name and JSON arguments, and returns either content or an error string.

**ToolRegistry:**
```go
registry := oasis.NewToolRegistry()
registry.Add(myTool)

// Get all tool definitions (for passing to LLM)
defs := registry.AllDefinitions()

// Execute a tool call by name
result, err := registry.Execute(ctx, "tool_name", argsJSON)
```

**Shipped tools:**

| Tool | Functions | Dependencies |
|------|-----------|-------------|
| `tools/knowledge` | `knowledge_search` | Store, EmbeddingProvider |
| `tools/remember` | `remember` | Store, EmbeddingProvider |
| `tools/search` | `web_search` | EmbeddingProvider, Brave API key |
| `tools/schedule` | `schedule_create`, `schedule_list`, `schedule_update`, `schedule_delete` | Store |
| `tools/shell` | `shell_exec` | workspace path |
| `tools/file` | `file_read`, `file_write`, `file_list` | workspace path |
| `tools/http` | `http_fetch` | (none) |

### Agent + LLMAgent + Network + Workflow (`agent.go`, `llmagent.go`, `network.go`, `workflow.go`)

Composable agent primitives for building single-agent and multi-agent systems.

**Agent interface:**

```go
type Agent interface {
    Name() string
    Description() string
    Execute(ctx context.Context, task AgentTask) (AgentResult, error)
}
```

Any struct implementing `Agent` can be used standalone or composed into a Network.

**LLMAgent** -- a concrete Agent that runs a `ChatWithTools` loop with a single Provider. It iterates: call LLM -> execute tool calls -> feed results back -> repeat until the LLM produces a final text response.

```go
agent := oasis.NewLLMAgent("researcher", "Searches for information", provider,
    oasis.WithTools(searchTool, knowledgeTool),
    oasis.WithPrompt("You are a research assistant."),
    oasis.WithMaxIter(5),
)
result, err := agent.Execute(ctx, oasis.AgentTask{Input: "Find info about Go interfaces"})
```

**Network** -- a concrete Agent that coordinates subagents and tools via an LLM router. The router sees subagents as callable tools (`agent_<name>`) and decides which to invoke. Networks can contain other Networks (recursive composition).

```go
network := oasis.NewNetwork("coordinator", "Routes tasks to specialists", routerProvider,
    oasis.WithAgents(researcher, writer),
    oasis.WithTools(knowledgeTool),
)
result, err := network.Execute(ctx, oasis.AgentTask{Input: "Research and write about X"})
```

**Workflow** -- a concrete Agent that executes a deterministic DAG of steps. Unlike Network (LLM-routed), Workflow follows explicit dependency edges declared at construction time. Steps can be functions, Agent delegations, or Tool calls. Parallel execution emerges from shared predecessors.

```go
wf, err := oasis.NewWorkflow("pipeline", "Research and write",
    oasis.Step("prepare", prepareFn),
    oasis.AgentStep("research", researcher, oasis.After("prepare")),
    oasis.AgentStep("write", writer, oasis.InputFrom("research.output"), oasis.After("research")),
)
result, err := wf.Execute(ctx, oasis.AgentTask{Input: "Go error handling"})
```

Since Workflow implements Agent, it composes with Networks and other Workflows. See [Workflows](workflows.md) for the full guide.

**AgentOption** -- unified option type shared by both `NewLLMAgent` and `NewNetwork`:

| Option | Description |
|--------|-------------|
| `WithTools(tools ...Tool)` | Add tools to the agent or network |
| `WithPrompt(s string)` | Set the system prompt |
| `WithMaxIter(n int)` | Set the maximum tool-calling iterations (default 10) |
| `WithAgents(agents ...Agent)` | Add subagents to a Network (ignored by LLMAgent) |
| `WithProcessors(processors ...any)` | Add processors to the execution pipeline (see Processors below) |
| `WithInputHandler(h InputHandler)` | Enable human-in-the-loop interactions (see InputHandler below) |

### Processors (`processor.go`)

Processors transform, validate, or control messages as they pass through an agent's execution pipeline. Three separate interfaces, one per hook point -- a processor implements whichever phases it needs:

| Interface | Hook Point | Can Modify |
| -------------------- | ------------------------------------------------ | ------------------------------------------ |
| `PreProcessor` | Before LLM call | `*ChatRequest` (messages, parameters) |
| `PostProcessor` | After LLM response, before tool execution | `*ChatResponse` (content, tool calls) |
| `PostToolProcessor` | After each tool execution | `*ToolResult` (content, error) |

Any processor can return `ErrHalt` to short-circuit execution and return a canned response. This enables guardrails, content moderation, and safety checks.

**ProcessorChain** holds an ordered list of processors and runs them at each hook point. Both `LLMAgent` and `Network` hold a chain, populated via `WithProcessors()`.

```go
agent := oasis.NewLLMAgent("safe-agent", "Agent with guardrails", provider,
    oasis.WithTools(searchTool),
    oasis.WithProcessors(&guardrail, &piiRedactor, &tokenBudget),
)
```

Processors run in registration order. An empty chain is a no-op (zero overhead).

**Use cases:** input validation, guardrails, prompt injection detection, content moderation, PII redaction, token budget enforcement, tool call filtering, message transformation.

### InputHandler (`input.go`)

Human-in-the-loop mechanism that lets agents pause mid-execution to ask a human for input. Frontend-agnostic -- implement `InputHandler` for your communication channel (Telegram, CLI, HTTP, etc.).

```go
type InputHandler interface {
    RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}
```

**Two patterns:**

1. **LLM-driven** -- when `WithInputHandler` is set, the agent gains a built-in `ask_user` tool. The LLM decides when to call it (e.g. ambiguous instructions, dangerous actions). The handler blocks until the human responds or the context is cancelled.

2. **Programmatic** -- developers build approval gates and review steps using existing Processors or Workflow Steps. Processors access the handler via `InputHandlerFromContext(ctx)`.

**Key behaviors:**

- Optional: agents without a handler work identically to before (no `ask_user` tool, processors skip gracefully)
- `ask_user` is a special-case in the execution loop, not a registered tool -- cannot be shadowed by user tools
- Network propagates the handler to subagents via context

## Ingest Pipeline (`ingest/`)

End-to-end ingestion: extract → chunk → embed → store. Built around two core interfaces (`Extractor`, `Chunker`) and one high-level API (`Ingestor`).

```go
// Ingestor handles the full pipeline
ingestor := ingest.NewIngestor(store, embedding)
result, err := ingestor.IngestText(ctx, content, source, title)
result, err := ingestor.IngestFile(ctx, fileBytes, filename)
result, err := ingestor.IngestReader(ctx, reader, filename)
```

**Interfaces:**

- `Extractor` — converts raw `[]byte` to plain text (pluggable: PDF, DOCX, etc.)
- `Chunker` — splits text into `[]string` chunks (pluggable strategies)

**Built-in chunkers:**

- `RecursiveChunker` — paragraphs → sentences → words (default). Improved sentence boundaries: abbreviation-aware (Mr., Dr.), decimal-safe (3.14), CJK punctuation (。！？).
- `MarkdownChunker` — splits at heading boundaries, preserves headings in chunks for LLM context, falls back to RecursiveChunker for oversized sections.

**Chunking strategies:**

- `StrategyFlat` (default) — single-level chunking
- `StrategyParentChild` — two-level hierarchical. Child chunks (small, ~256 tokens) are embedded for matching; parent chunks (large, ~1024 tokens) provide context. On retrieval: match children → resolve `ParentID` → return parent content.

**Batched embedding:** Large documents are embedded in configurable batches (default 64 chunks per `Embed()` call) to respect provider limits.

**Package layout:**

```text
ingest/
├── extractor.go          # Extractor interface + PlainText/HTML/Markdown extractors
├── chunker.go            # Chunker interface + RecursiveChunker
├── chunker_markdown.go   # MarkdownChunker
├── ingestor.go           # Ingestor (extract → chunk → embed → store)
├── strategy.go           # ChunkStrategy + parent-child logic
├── option.go             # Option types
└── pdf/
    └── extractor.go      # PDFExtractor (separate dependency)
```

## Domain Types (`types.go`)

All framework types live in the root `oasis` package:

| Type | Purpose | Key Fields |
|------|---------|-----------|
| `Document` | Ingested content | ID, Title, Source, Content, CreatedAt |
| `Chunk` | Document fragment with embedding | ID, DocumentID, ParentID, Content, ChunkIndex, Embedding |
| `Thread` | Conversation thread | ID, ChatID, Title, Metadata, CreatedAt, UpdatedAt |
| `Message` | Chat message | ID, ThreadID, Role, Content, Embedding, CreatedAt |
| `Fact` | Memory fact | ID, Fact, Category, Confidence, Embedding, CreatedAt, UpdatedAt |
| `ScheduledAction` | Recurring automation | ID, Description, Schedule, ToolCalls (JSON), NextRun, Enabled, SkillID |
| `Skill` | Stored instruction package for specializing agents | ID, Name, Description, Instructions, Tools, Model, Embedding, CreatedAt, UpdatedAt |
| `ChatMessage` | LLM protocol message | Role, Content, Images, ToolCalls, ToolCallID |
| `ChatRequest` | LLM request | Messages |
| `ChatResponse` | LLM response | Content, ToolCalls, Usage |
| `ToolDefinition` | Tool schema | Name, Description, Parameters (JSON Schema) |
| `ToolCall` | LLM tool invocation | ID, Name, Args |
| `ToolResult` | Tool execution result | Content, Error |
| `IncomingMessage` | Frontend message | ID, ChatID, UserID, Text, Document, Photos |
| `AgentTask` | Input to an Agent | Input, Context (metadata map) |
| `AgentResult` | Output of an Agent | Output, Usage (aggregate tokens) |

**Convenience constructors:**
```go
oasis.UserMessage("hello")
oasis.SystemMessage("You are a helpful assistant.")
oasis.AssistantMessage("Hi there!")
oasis.ToolResultMessage(callID, "result content")
```

## ID and Timestamp Utilities (`id.go`)

```go
oasis.NewID()    // Time-sortable 20-char xid (base32)
oasis.NowUnix()  // Current Unix timestamp (seconds)
```

## Error Types (`errors.go`)

```go
&oasis.ErrLLM{Provider: "gemini", Message: "rate limited"}
&oasis.ErrHTTP{Status: 429, Body: "too many requests"}
```

No `anyhow`/`thiserror` equivalents -- errors are kept minimal and specific.

## Configuration (`internal/config/`)

Layered config loading: **defaults** -> **TOML file** -> **environment variables** (env wins).

```go
cfg := config.Load("")            // loads from oasis.toml
cfg := config.Load("/path/to.toml") // loads from specific file
```

See [Configuration](configuration.md) for the full reference.

## Database Schema

The Store implementations create these tables:

```sql
-- Knowledge base
documents (id, title, source, content, created_at)
chunks    (id, document_id, parent_id, content, chunk_index, embedding)

-- Threads
threads  (id, chat_id, title, metadata, created_at, updated_at)
messages (id, thread_id, role, content, embedding, created_at)

-- Config
config (key PRIMARY KEY, value)

-- Scheduling
scheduled_actions (id, description, schedule, tool_calls, synthesis_prompt,
                   next_run, enabled, skill_id, created_at)

-- Skills
skills (id, name, description, instructions, tools, model, embedding,
        created_at, updated_at)
```

The MemoryStore creates:

```sql
user_facts (id, fact, category, confidence, embedding, created_at, updated_at)
```

Embeddings are stored as JSON text. Vector search is brute-force cosine similarity computed in-process.

## Data Flow: Ingest -> Search

```text
Raw content (text/HTML/file/PDF)
         |
   [Ingestor]  (extract → chunk → batch embed → store)
         |
         v
   Stored in SQLite (Document + Chunks with embeddings)

--- later ---

   User query
         |
   [EmbeddingProvider.Embed]
         |
   Query embedding
         |
   [Store.SearchChunks]
         |
   Top-K relevant chunks
         |
   (if parent-child: resolve ParentID via Store.GetChunksByIDs)
```

## Data Flow: Tool Execution

```
   LLM response with ToolCalls
         |
   [ToolRegistry.Execute(name, args)]
         |
   Dispatches to matching Tool
         |
   Tool.Execute(ctx, name, args)
         |
   ToolResult { Content | Error }
         |
   Appended to messages, sent back to LLM
```

## Design Decisions

- **No SDK dependencies** -- All LLM providers use raw HTTP via `net/http`. This avoids version lock-in and keeps the binary small.
- **Pure-Go SQLite** -- Uses `modernc.org/sqlite` (no CGO required for basic builds). CGO is enabled for the production build for performance.
- **Brute-force vector search** -- No vector index (DiskANN/HNSW). Sufficient for personal-scale knowledge bases (thousands of chunks). Keeps dependencies minimal.
- **Embeddings as JSON** -- Stored as JSON text rather than binary blobs. Simpler, portable, easily inspectable. Trade-off: more storage, slower deserialization.
- **Fresh DB connections** -- Each operation opens a fresh connection. Avoids connection pooling complexity and Turso STREAM_EXPIRED errors.
- **Interface-driven** -- Every major component is a Go interface. Concrete implementations are in separate packages. No global state.
- **Minimal error types** -- Two custom error types (`ErrLLM`, `ErrHTTP`). Tool errors use `ToolResult.Error` string field, not Go errors.
- **Observer as middleware** -- The `observer/` package wraps `Provider`, `EmbeddingProvider`, and `Tool` with OTEL instrumentation using the decorator pattern. Zero changes to existing implementations, zero overhead when disabled.
- **Agent as interface** -- `Agent` is a composable primitive like `Tool` or `Provider`. `LLMAgent`, `Network`, and `Workflow` are concrete implementations. All three implement Agent, so they compose recursively: Networks can contain Workflows, Workflows can contain Agents via AgentStep, and Workflows can nest inside other Workflows.
