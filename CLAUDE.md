# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read and follow these docs before making changes:**

- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — Full system architecture: crate graph, brain 3-layer structure, message processing flows, LLM dispatch, subsystems (memory, ingestion, search, scheduling), database schema, configuration, and design decisions.
- **[docs/CONVENTIONS.md](docs/CONVENTIONS.md)** — Coding best practices: error handling, module structure, import ordering, trait patterns, how to add tools/providers, async patterns, logging, string/type/config conventions, testing rules, and things to never do.

**When you make architectural or convention changes, update the corresponding doc file to keep them in sync.**

## What is Oasis

Oasis is a personal AI assistant accessed via Telegram. It combines conversational AI, a knowledge base (RAG), task management, scheduled automations, web browsing, and conversational memory. Built in Rust as a single binary with modular library crates, deployed as a Docker image on Zeabur.

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

## Architecture (quick reference)

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for full details.

### Crate Dependency Flow (5 crates, no cycles)

```
src/main.rs → oasis-brain
oasis-brain → oasis-core, oasis-llm, oasis-telegram, oasis-integrations
oasis-telegram → oasis-core
oasis-llm → oasis-core
oasis-integrations → oasis-core
```

### Brain Three-Layer Structure

```
brain/     (L1: Orchestration)  — routing, chat streaming, action dispatch, scheduling
tool/      (L2: Extension point) — Tool trait + ToolRegistry + implementations
service/   (L3: Infrastructure)  — store, memory, LLM dispatch, search, ingest
```

Dependency flows downward only. L3 never calls upward.

### Key Design Decisions

- **No async trait objects for LLM** — match-based dispatch, RPITIT on `LlmProvider`
- **`#[async_trait]` for Tool** — must be object-safe (`Box<dyn Tool>` in `ToolRegistry`)
- **No anyhow/thiserror** — custom `OasisError` enum
- **No bot framework** — hand-rolled Telegram client
- **No LLM SDKs** — all providers use raw HTTP (reqwest)
- **No chrono/time** — hand-rolled date math
- **Telegram HTML, not Markdown** — `pulldown-cmark` converts MD→HTML

## Conventions (quick reference)

See [docs/CONVENTIONS.md](docs/CONVENTIONS.md) for full details, code examples, and the "Things to Never Do" list.

### Database

Uses libSQL (SQLite-compatible) with DiskANN vector extensions. Fresh connections per call (no caching). Tables: `documents`, `chunks` (with `F32_BLOB(1536)` embedding), `conversations`, `messages` (with embedding), `config`, `scheduled_actions`, `user_facts`, `conversation_topics`.
