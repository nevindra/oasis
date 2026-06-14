# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read [docs/PHILOSOPHY.md](docs/PHILOSOPHY.md) before brainstorming or making architectural decisions** — framework identity, design principles, composability stance, and API strategy.

**Always read [docs/ENGINEERING.md](docs/ENGINEERING.md) before writing code** — coding standards, production engineering, performance rules, and things to never do.

**Before modifying any component, find and read the docs for its topic.** Each topic folder contains the same three files: `index.md` (concept), `api.md` (reference), `examples.md` (recipes).

| Changing...              | Start from                                                  |
|--------------------------|-------------------------------------------------------------|
| LLMAgent / streaming / scheduler | [docs/external/agent/](docs/external/agent/index.md)                  |
| Multi-agent collaboration | [docs/external/network/](docs/external/network/index.md)                     |
| Workflow / DAG           | [docs/external/workflow/](docs/external/workflow/index.md)                    |
| Conversation history / facts / recall | [docs/external/memory/](docs/external/memory/index.md)            |
| RAG / ingestion / retrieval | [docs/external/rag/](docs/external/rag/index.md)                           |
| Skills                   | [docs/external/skills/](docs/external/skills/index.md)                        |
| Tools (built-in / custom) | [docs/external/tools/](docs/external/tools/index.md)                         |
| Sandbox                  | [docs/external/sandbox/](docs/external/sandbox/index.md)                      |
| LLM providers            | [docs/external/providers/](docs/external/providers/index.md)                  |
| Observability / tracing  | [docs/external/observability/](docs/external/observability/index.md)          |
| Processors / guardrails / HITL | [docs/external/processors/](docs/external/processors/index.md)          |
| Storage backends         | [docs/external/store/](docs/external/store/index.md)                          |
| Getting started flow     | [docs/external/getting-started/](docs/external/getting-started/index.md)      |
| A2A protocol (server/client) | [docs/external/a2a/](docs/external/a2a/index.md)                          |
| MCP client integration   | [docs/external/mcp/](docs/external/mcp/index.md)                              |

When your change affects multiple areas, search for all docs referencing the component name and update each one. Keep docs in sync with code.

## What is Oasis

Oasis is a high-performance Go framework for AI agent systems — fast, reliable, and built to scale with AI capabilities. The framework is the product. Reference apps (Telegram bot, etc.) are demos — DO NOT USE BOT CODE AS YOUR GUIDELINE.

## Build & Test

```bash
go build ./...                                # build all packages
go test ./...                                 # run all tests
golangci-lint run ./...                       # enforce depguard rules
```

## Project Structure

The repo is a **single Go module** (`github.com/nevindra/oasis`, one `go.mod`).
A curated root package (`oasis.go`) re-exports protocol types and the most
common APIs from focused subpackages. Optional or heavy-dependency packages live
in their own subdirectories within the same module — consumers import them
directly rather than opting into a separate module.

> Because every subpackage is part of one module, importing any subpackage
> (e.g. `oasis/observer`, `oasis/store/postgres`) pulls in the full module
> dependency set: otel, pgx, modernc-sqlite, pdf, and go-readability.

```
oasis/                              # FRAMEWORK — single module
|-- oasis.go                        # Re-export umbrella (curated public surface)
|-- doc.go                          # Top-level package documentation
|-- batch.go                        # Batch primitives (BatchJob, BatchStats)
|
|-- core/                           # Protocol types + interfaces + Erase helper (leaf package — no internal deps)
|-- agent/                          # LLMAgent + Spawn + functional options
|-- workflow/                       # DAG-based orchestration
|-- network/                        # Multi-agent peer networks
|-- compaction/                     # Compaction processors
|-- guardrail/                      # Guardrail processors
|-- ratelimit/                      # Rate limiter wrapper
|-- memory/                         # Memory orchestration
|-- skills/                         # Skill loader + asset embedding
|-- processor/                      # ProcessorChain helper
|-- provider/{catalog,resolve}/     # Stdlib-only model registry helpers
|
|-- tools/{data,http,...}/          # Tool implementations
|-- cmd/{mcp-docs,modelgen}/        # CLI utilities
|
|-- (optional/heavy packages — same module, import directly)
|   |-- mcp/                        # MCP client integration
|   |-- store/{sqlite,postgres}/    # Storage backends (sqlite driver / pgx)
|   |-- provider/{gemini,openaicompat}/  # LLM provider implementations
|   |-- observer/                   # OTEL observability (full OTEL SDK)
|   |-- ingest/                     # Document ingestion (PDF, DOCX, embeddings)
|   |-- sandbox/                    # Sandbox interface + Tools()
|   |-- rag/                        # Retrieval-augmented generation
|   |-- a2a/                        # A2A protocol (server, client, RemoteAgent)

Sandbox implementations live in their own repos:
  - github.com/nevindra/oasis-sandbox-ix — Docker-backed ix sandbox
```

Adding a re-export to `oasis.go` is a deliberate decision — do not auto-mirror
every new export from a subpackage. Niche or power-user APIs stay in their
subpackage and callers import that subpackage directly.

## Releasing

- **Changelog**: update [CHANGELOG.md](CHANGELOG.md) using [Keep a Changelog](https://keepachangelog.com/) format. New changes under `[Unreleased]`. When tagging, rename to `[x.y.z] - date` and add fresh `[Unreleased]`.
- **Versioning** (semver, v0.x.x): patch = bug fix, minor = new features or breaking changes, major = reserved for v1.0.0+. Strict rule: patch releases must NEVER introduce new types, interfaces, or exported functions — only bug fixes. New interfaces/types always require a minor bump.
- **Tagging**: `git tag vX.Y.Z && git push origin master vX.Y.Z`. Go proxy indexes automatically.
- **Immutable**: once tagged on `proxy.golang.org`, never re-tag — always bump version.
- **Minimum Go**: 1.25 (dependency-constrained — otel, pgx, `golang.org/x/*`, and modernc-sqlite all require go 1.25).
