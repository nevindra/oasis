# Oasis — Personal Assistant Design

**Date:** 2026-02-16
**Status:** Approved

## Overview

Oasis (Open Assistant) is a personal AI assistant that combines a knowledge base, task management, and conversational memory — accessed via Telegram. Built in Rust with minimal dependencies, deployed as a small Docker image on Zeabur.

## Architecture

**Approach:** Modular library crates compiling to a single binary.

```
oasis/
├── Cargo.toml              # workspace root
├── Dockerfile
├── oasis.toml
├── crates/
│   ├── oasis-core/         # shared types, config, error handling
│   ├── oasis-llm/          # LLM provider trait + implementations
│   ├── oasis-vector/       # libSQL vector storage abstraction
│   ├── oasis-tasks/        # project/task/subtask management
│   ├── oasis-ingest/       # ingestion pipeline
│   ├── oasis-telegram/     # Telegram Bot API client
│   └── oasis-brain/        # orchestration layer
└── src/
    └── main.rs             # entrypoint
```

### Dependency Flow (no cycles)

```
main.rs → oasis-brain
oasis-brain → oasis-llm, oasis-vector, oasis-tasks, oasis-ingest, oasis-telegram
oasis-telegram → oasis-core
oasis-llm → oasis-core
oasis-vector → oasis-core
oasis-tasks → oasis-core
oasis-ingest → oasis-core, oasis-llm
```

### External Dependencies

| Crate | Purpose |
|---|---|
| `libsql` | Database + DiskANN vector search |
| `reqwest` (rustls, minimal features) | Async HTTP client |
| `tokio` (rt-multi-thread, macros) | Async runtime |
| `serde` + `serde_json` | JSON serialization |
| `toml` | Config parsing |

## Data Model (libSQL)

### Knowledge Base

```sql
CREATE TABLE documents (
    id TEXT PRIMARY KEY,
    source_type TEXT NOT NULL,      -- 'message', 'file', 'url', 'api'
    source_ref TEXT,                -- original URL, filename, or API ref
    title TEXT,
    raw_content TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE chunks (
    id TEXT PRIMARY KEY,
    document_id TEXT NOT NULL REFERENCES documents(id),
    content TEXT NOT NULL,
    embedding F32_BLOB(1536),
    chunk_index INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX chunks_vector_idx ON chunks(
    libsql_vector_idx(embedding, 'metric=cosine', 'compress_neighbors=float8', 'max_neighbors=64')
);
```

### Task Management

```sql
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'active',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE tasks (
    id TEXT PRIMARY KEY,
    project_id TEXT REFERENCES projects(id),
    parent_task_id TEXT REFERENCES tasks(id),
    title TEXT NOT NULL,
    description TEXT,
    status TEXT NOT NULL DEFAULT 'todo',
    priority INTEGER DEFAULT 0,
    due_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
```

### Conversation Memory

```sql
CREATE TABLE conversations (
    id TEXT PRIMARY KEY,
    telegram_chat_id INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations(id),
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    embedding F32_BLOB(1536),
    created_at INTEGER NOT NULL
);

CREATE INDEX messages_vector_idx ON messages(
    libsql_vector_idx(embedding, 'metric=cosine')
);
```

### Configuration Store

```sql
CREATE TABLE config (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

**IDs:** ULIDs (time-sortable, no coordination needed).
**Timestamps:** Unix epoch integers.
**Files:** Original files are discarded after text extraction. Only extracted text and chunks are stored.

## LLM & Embedding Providers

Pluggable via Rust traits:

```rust
pub trait LlmProvider {
    async fn chat(&self, request: ChatRequest) -> Result<ChatResponse>;
    fn name(&self) -> &str;
}

pub trait EmbeddingProvider {
    async fn embed(&self, texts: &[&str]) -> Result<Vec<Vec<f32>>>;
    fn dimensions(&self) -> usize;
    fn name(&self) -> &str;
}
```

### Supported Providers

| Provider | LLM | Embeddings |
|---|---|---|
| Anthropic | Claude | — |
| OpenAI | GPT-4o | text-embedding-3-small/large |
| Google Gemini | Gemini 2.0 Flash/Pro | text-embedding-004 |
| Ollama | Local models | Local embedding models |

LLM and embedding providers are independent — can mix (e.g. Claude for chat, OpenAI for embeddings). All providers are raw HTTPS + JSON, no SDK dependencies.

## Ingestion Pipeline

```
Source → Extractor → Chunker → Embedder → Store
```

### Sources

- **Telegram messages** — direct text input
- **Files** — uploaded via Telegram (PDF, markdown, plain text)
- **URLs** — user sends a link, we fetch and extract
- **3rd party APIs** — pluggable via `ExternalSource` trait (future)

### Text Extraction

| Format | Approach | External dep? |
|---|---|---|
| Plain text | Pass through | No |
| Markdown | Strip formatting | No |
| HTML | Simple tag-stripping parser | No (~200 lines) |
| PDF | Basic text extraction | Minimal (evaluate at impl time) |

### Chunking Strategy

Recursive character splitting with overlap:

1. Split on paragraph boundaries (`\n\n`)
2. If too large, split on sentence boundaries (`. `)
3. If still too large, split on word boundaries
4. Target: ~512 tokens per chunk, ~50 token overlap

### Ingestion Behavior

| Trigger | Action |
|---|---|
| Text message | Store as message + embed. If "remember this", also store as document. |
| File upload | Extract text → chunk → embed → store. Discard original. |
| URL sent | Fetch HTML → strip tags → chunk → embed → store. |
| 3rd party API | Periodic/on-demand fetch → extract → chunk → embed → store. |

## Conversation Engine (oasis-brain)

### Intent Detection

LLM classifies user intent into structured actions:

| Intent | Example | Action |
|---|---|---|
| `question` | "What did I save about Rust?" | Vector search → RAG → answer |
| `task_create` | "Remind me to deploy by Friday" | Create task |
| `task_query` | "What's on my plate today?" | Query tasks → summarize |
| `task_update` | "Mark deploy as done" | Find + update task |
| `ingest` | "Remember this: ..." / file / URL | Route to ingestion |
| `chat` | "How do errors work in Rust?" | Direct LLM chat |

### RAG Flow

```
Question → Embed → Vector search (top-k=10) → Build prompt → LLM → Response
```

### Conversation Memory

- Last 20 messages kept in context window
- Older messages embedded and searchable via vector search
- Brain pulls relevant past context when needed

### System Prompt (composed dynamically)

```
You are Oasis, a personal assistant for {user_name}.
Current date: {date}
Active tasks: {task_summary}

## Relevant knowledge
{vector_search_results}

## Recent conversation
{last_20_messages}
```

## Telegram Bot

Hand-rolled client, no bot framework. Long polling.

### API Methods Used

| Method | Purpose |
|---|---|
| `getMe` | Verify bot token on startup |
| `getUpdates` | Long poll for messages |
| `sendMessage` | Send response (Markdown) |
| `getFile` + download | Receive uploaded files |
| `sendChatAction` | Show "typing..." indicator |

### Security

- `allowed_user_id` in config — only your Telegram ID can interact
- All other messages silently ignored
- Bot token via environment variable

### Message Flow

```
Telegram → poll() → auth check → send_typing() → brain::handle() → send_text()
```

## Configuration

```toml
# oasis.toml

[telegram]
allowed_user_id = 123456789

[llm]
provider = "anthropic"
model = "claude-sonnet-4-5-20250929"

[embedding]
provider = "openai"
model = "text-embedding-3-small"
dimensions = 1536

[ollama]
base_url = "http://localhost:11434"

[database]
path = "oasis.db"

[chunking]
max_tokens = 512
overlap_tokens = 50

[brain]
context_window = 20
vector_top_k = 10
```

**Loading order:** defaults → `oasis.toml` → environment variables (env wins).

**Secrets via env:**
- `OASIS_TELEGRAM_TOKEN`
- `OASIS_LLM_API_KEY`
- `OASIS_EMBEDDING_API_KEY`
- `OASIS_TURSO_URL` (optional)
- `OASIS_TURSO_TOKEN` (optional)

## Error Handling

Custom error type, no `anyhow`/`thiserror`:

```rust
pub enum OasisError {
    Telegram(String),
    Llm { provider: String, message: String },
    Embedding(String),
    Database(String),
    Ingest(String),
    Config(String),
    Http { status: u16, body: String },
}
```

**Strategy:** Never crash on recoverable errors. Report failures to user via Telegram and continue. Only panic on startup config errors.

## Deployment

### Docker

```dockerfile
FROM rust:1.84-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY . .
RUN cargo build --release

FROM alpine:3.21
COPY --from=builder /app/target/release/oasis /usr/local/bin/oasis
COPY oasis.toml /etc/oasis/oasis.toml
ENTRYPOINT ["oasis"]
```

**Binary size:** ~5-10MB (statically linked, musl).
**Image size:** ~15MB total with Alpine.

### Zeabur

- GitHub repo → Zeabur auto-deploys via Dockerfile
- Env vars set in Zeabur dashboard
- Optional: Turso for managed DB (data survives container restarts)
- Alternative: Zeabur persistent volume for local `oasis.db`
