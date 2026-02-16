# Oasis

A personal AI assistant accessed via Telegram, built in Rust. Combines conversational AI with a knowledge base (RAG), task management, scheduled reminders, and long-term memory — all running as a single binary.

## Features

- **Streaming Chat** — Real-time streamed responses with multi-provider LLM support (Gemini, Claude, OpenAI, Ollama)
- **Knowledge Base (RAG)** — Ingest documents (text, Markdown, HTML), chunk and embed them, then retrieve relevant context via vector search
- **Task Management** — Create, list, update, and delete tasks and projects through natural conversation
- **Web Search & Browsing** — Search the web and browse pages using headless Chromium
- **Long-term Memory** — Automatically extracts and recalls facts about the user across conversations
- **Scheduled Actions** — Set reminders and recurring tasks with proactive notifications
- **Service Integrations** — Linear (issue management), Google Calendar (events), Gmail (search, read, send) — all accessible via natural language
- **Intent Classification** — Routes messages to the right handler (chat vs. tool use) using a lightweight classifier model

## Architecture

```
src/main.rs → oasis-brain
                ├── oasis-integrations  (Linear, Google Calendar, Gmail clients)
                ├── oasis-llm           (LLM & embedding providers)
                ├── oasis-telegram      (Telegram bot client)
                └── oasis-core          (shared types, config, errors)
```

**oasis-brain** is organized into three layers:

| Layer | Directory | Responsibility |
|-------|-----------|----------------|
| L1: Orchestration | `brain/` | Message routing, streaming chat, action dispatch, scheduling |
| L2: Extension | `tool/` | Tool trait, registry, and 8 tool modules (29 tools total) |
| L3: Infrastructure | `service/` | Storage, task management, memory, LLM dispatch, search, ingestion |

The system uses three separate LLM models:

| Model | Default | Purpose |
|-------|---------|---------|
| Chat | Gemini 2.5 Flash | Streaming conversational responses |
| Intent | Gemini Flash-Lite | Lightweight intent classification & fact extraction |
| Action | Gemini 2.5 Flash | Agentic tool-use loop |

## Getting Started

### Prerequisites

- Rust 1.84+
- A Telegram bot token (from [@BotFather](https://t.me/BotFather))
- An API key for at least one LLM provider (Gemini, OpenAI, or Anthropic)
- Chromium (for web search features; included in Docker image)

### Configuration

Create a `.env` file in the project root:

```bash
# Required
OASIS_TELEGRAM_TOKEN=your-telegram-bot-token
OASIS_LLM_API_KEY=your-llm-api-key
OASIS_EMBEDDING_API_KEY=your-embedding-api-key

# Optional — remote database (Turso)
OASIS_TURSO_URL=
OASIS_TURSO_TOKEN=

# Optional — separate keys for intent/action models
OASIS_INTENT_API_KEY=your-intent-api-key
OASIS_ACTION_API_KEY=your-action-api-key

# Optional — service integrations
OASIS_LINEAR_API_KEY=your-linear-api-key
OASIS_GOOGLE_CLIENT_ID=your-google-client-id
OASIS_GOOGLE_CLIENT_SECRET=your-google-client-secret
```

See `.env.development` for a complete template with all available variables.

Adjust `oasis.toml` for model selection, chunking parameters, and other settings. Config loading order: **defaults → `oasis.toml` → environment variables** (env vars win).

### Run Locally

```bash
source .env && cargo run
```

### Docker

```bash
docker build -t oasis .
docker run --env-file .env oasis
```

The Docker image uses a multi-stage Alpine build and includes Chromium for headless browsing.

## Development

```bash
cargo check --workspace        # Type-check all crates
cargo build                    # Debug build
cargo test --workspace         # Run all tests
cargo test -p oasis-brain      # Test a single crate
cargo test test_ingest_html    # Run a specific test
```

### Database

Oasis uses libSQL (SQLite-compatible) with DiskANN vector indexes for embedding search. By default, it creates a local `oasis.db` file. Optionally connect to a remote Turso database via `OASIS_TURSO_URL`.

```bash
./scripts/reset-db.sh          # Drop all tables (local or remote)
```

## Deployment

Deployed as a Docker image on [Zeabur](https://zeabur.com). Set the required environment variables in your deployment platform and the container runs as a single process.

## License

All rights reserved.
