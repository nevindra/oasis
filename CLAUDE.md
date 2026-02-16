# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Oasis

Oasis is a personal AI assistant accessed via Telegram. It combines a knowledge base (RAG), task management, and conversational memory. Built in Rust as a single binary with modular library crates, deployed as a Docker image on Zeabur.

## Build & Run Commands

```bash
# Build
cargo build
cargo build --release

# Run (requires .env or env vars set)
source .env && cargo run

# Run tests (all crates)
cargo test --workspace

# Run tests for a single crate
cargo test -p oasis-brain

# Run a single test by name
cargo test -p oasis-brain test_ingest_html

# Check without building
cargo check --workspace

# Reset database (drops all tables)
./scripts/reset-db.sh

# Docker build
docker build -t oasis .
```

## Configuration

Config loading order: defaults -> `oasis.toml` -> environment variables (env wins).

Secrets are set via environment variables (see `.env`):
- `OASIS_TELEGRAM_TOKEN` - Telegram bot token
- `OASIS_LLM_API_KEY` - API key for the configured LLM provider
- `OASIS_EMBEDDING_API_KEY` - API key for the configured embedding provider
- `OASIS_TURSO_URL` / `OASIS_TURSO_TOKEN` - optional remote libSQL database
- `OASIS_CONFIG` - path to config file (defaults to `oasis.toml`)

## Architecture

### Crate Dependency Flow (no cycles)

```
src/main.rs → oasis-brain
oasis-brain → oasis-core, oasis-llm, oasis-telegram
oasis-telegram → oasis-core
oasis-llm → oasis-core
```

### Crate Responsibilities (4 crates)

- **oasis-core** — Shared types (`Document`, `Chunk`, `Task`, `Message`, `ChatRequest`/`ChatResponse`), `Config` loading, `OasisError` enum, and utility functions (`new_id`, `now_unix`).
- **oasis-llm** — `LlmProvider` and `EmbeddingProvider` traits with implementations for Anthropic, OpenAI, Gemini, and Ollama. All providers use raw HTTP (reqwest) with no SDK dependencies. Gemini supports streaming via SSE.
- **oasis-telegram** — `TelegramBot` client using long polling. Hand-rolled, no bot framework. Handles message splitting for Telegram's 4096-char limit.
- **oasis-brain** — The orchestration layer (`Brain` struct) plus all business logic modules:
  - `brain.rs` — Message routing, intent dispatch, LLM/embedding dispatch, streaming chat
  - `store.rs` — `VectorStore` wrapping libSQL with DiskANN vector indexes (documents, chunks, conversations, messages, config)
  - `tasks.rs` — `TaskManager` for CRUD on projects and tasks
  - `memory.rs` — `MemoryStore` for user fact extraction and memory
  - `search.rs` — `WebSearch` with headless Chromium for web search and browsing
  - `tools.rs` — Tool definitions for LLM function calling
  - `scheduler.rs` — `Scheduler` for proactive task reminders
  - `ingest/` — `IngestPipeline` for text extraction (plain text, Markdown, HTML) and recursive chunking with overlap

### Key Design Patterns

- **No async trait objects** — LLM/embedding providers are created on-the-fly in dispatch methods (`chat_llm_inner`, `embed_text_inner`) via match on provider name, avoiding `dyn` trait complexity.
- **Custom error type without anyhow/thiserror** — `OasisError` enum with manual `Display` impl. All crates use `oasis_core::error::Result<T>`.
- **ULID-like IDs** — `new_id()` generates time-sortable IDs using timestamp + random bytes from `/dev/urandom`, no external crate.
- **Background message storage** — `spawn_store()` fires a tokio task to embed and persist message pairs after responding, so the user doesn't wait for embedding.
- **LLM-based intent routing** — `Brain::handle_message` classifies user messages via a lightweight intent LLM (Gemini Flash-Lite) into Chat vs Action intents, then dispatches accordingly.

### Database

Uses libSQL (SQLite-compatible) with vector extensions. Tables: `documents`, `chunks` (with `F32_BLOB(1536)` embedding column), `projects`, `tasks`, `conversations`, `messages` (with embedding), `config` (key-value store for runtime state like `telegram_offset` and `owner_user_id`).
