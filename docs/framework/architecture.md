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
         v                          v
+--------------------------------------------------+
|              Your Application                     |
|   (wires components together, defines behavior)   |
+--------------------------------------------------+
         |              |              |
         v              v              v
+-------------+  +-------------+  +-----------+
| VectorStore |  | MemoryStore |  |   Tool    |
| (sqlite,    |  | (sqlite)    |  | Registry  |
|  libsql)    |  |             |  |           |
+-------------+  +-------------+  +-----------+
                                       |
                                       v
                                  +-----------+
                                  | Ingest    |
                                  | Pipeline  |
                                  +-----------+
```

Every box above is a Go interface (except Ingest Pipeline which is a concrete struct). You can swap any implementation without affecting the others.

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

### VectorStore (`store.go`)

Persistence layer with vector search capabilities. Handles messages, documents/chunks, conversations, config, and scheduled actions.

**Message operations:**
- `StoreMessage(ctx, msg)` -- persist a message (with optional embedding)
- `GetMessages(ctx, conversationID, limit)` -- recent messages for context window
- `SearchMessages(ctx, embedding, topK)` -- vector search over all messages

**Document/chunk operations:**
- `StoreDocument(ctx, doc, chunks)` -- persist a document and its chunks (with embeddings)
- `SearchChunks(ctx, embedding, topK)` -- vector search over document chunks

**Conversation management:**
- `GetOrCreateConversation(ctx, chatID)` -- find or create a conversation by chat ID

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
| `tools/knowledge` | `knowledge_search` | VectorStore, EmbeddingProvider |
| `tools/remember` | `remember` | VectorStore, EmbeddingProvider |
| `tools/search` | `web_search` | EmbeddingProvider, Brave API key |
| `tools/schedule` | `schedule_create`, `schedule_list`, `schedule_update`, `schedule_delete` | VectorStore |
| `tools/shell` | `shell_exec` | workspace path |
| `tools/file` | `file_read`, `file_write`, `file_list` | workspace path |
| `tools/http` | `http_fetch` | (none) |

## Ingest Pipeline (`ingest/`)

Converts raw content into documents and chunks ready for embedding and storage. This is a concrete struct, not an interface.

```go
pipeline := ingest.NewPipeline(512, 50) // maxTokens, overlapTokens

// From plain text
result := pipeline.IngestText(content, source, title)

// From HTML
result := pipeline.IngestHTML(htmlContent, sourceURL)

// From file (detects type by extension)
result := pipeline.IngestFile(content, filename)
```

Returns `IngestResult` containing a `Document` and `[]Chunk`. **Embedding is NOT done by the pipeline** -- the caller must embed the chunks and store them via VectorStore.

**Chunking strategy:**
1. Split on paragraph boundaries (`\n\n`)
2. If too large, split on sentence boundaries (`. ` followed by uppercase)
3. If still too large, split on word boundaries
4. Target: ~512 tokens per chunk (~2048 chars), ~50 token overlap

**Text extraction:**
- HTML: strips tags, scripts, styles; decodes entities
- Markdown: strips formatting markers, links, code fences
- Plain text: pass through

## Domain Types (`types.go`)

All framework types live in the root `oasis` package:

| Type | Purpose | Key Fields |
|------|---------|-----------|
| `Document` | Ingested content | ID, Title, Source, Content, CreatedAt |
| `Chunk` | Document fragment with embedding | ID, DocumentID, Content, ChunkIndex, Embedding |
| `Conversation` | Chat session | ID, ChatID, CreatedAt |
| `Message` | Chat message | ID, ConversationID, Role, Content, Embedding, CreatedAt |
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

The VectorStore implementations create these tables:

```sql
-- Knowledge base
documents (id, title, source, content, created_at)
chunks    (id, document_id, content, chunk_index, embedding)

-- Conversations
conversations (id, chat_id UNIQUE, created_at)
messages      (id, conversation_id, role, content, embedding, created_at)

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

```
Raw content (text/HTML/file)
         |
   [Ingest Pipeline]
         |
   Document + Chunks (no embeddings yet)
         |
   [EmbeddingProvider.Embed]
         |
   Chunks with embeddings
         |
   [VectorStore.StoreDocument]
         |
         v
   Stored in SQLite

--- later ---

   User query
         |
   [EmbeddingProvider.Embed]
         |
   Query embedding
         |
   [VectorStore.SearchChunks]
         |
   Top-K relevant chunks
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
