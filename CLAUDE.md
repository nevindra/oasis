# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read [docs/PHILOSOPHY.md](docs/PHILOSOPHY.md) before brainstorming or making architectural decisions** — framework identity, design principles, composability stance, and API strategy.

**Always read [docs/ENGINEERING.md](docs/ENGINEERING.md) before writing code** — coding standards, production engineering, performance rules, and things to never do.

**Before modifying any component, find and read the docs for its topic.** Each topic folder contains the same three files: `index.md` (concept), `api.md` (reference), `examples.md` (recipes).

| Changing...              | Start from                                                  |
|--------------------------|-------------------------------------------------------------|
| LLMAgent / streaming / scheduler | [docs/agent/](docs/agent/index.md)                  |
| Multi-agent collaboration | [docs/network/](docs/network/index.md)                     |
| Workflow / DAG           | [docs/workflow/](docs/workflow/index.md)                    |
| Conversation history / facts / recall | [docs/memory/](docs/memory/index.md)            |
| RAG / ingestion / retrieval | [docs/rag/](docs/rag/index.md)                           |
| Skills                   | [docs/skills/](docs/skills/index.md)                        |
| Tools (built-in / custom) | [docs/tools/](docs/tools/index.md)                         |
| Sandbox                  | [docs/sandbox/](docs/sandbox/index.md)                      |
| LLM providers            | [docs/providers/](docs/providers/index.md)                  |
| Observability / tracing  | [docs/observability/](docs/observability/index.md)          |
| Processors / guardrails / HITL | [docs/processors/](docs/processors/index.md)          |
| Storage backends         | [docs/store/](docs/store/index.md)                          |
| Getting started flow     | [docs/getting-started/](docs/getting-started/index.md)      |

When your change affects multiple areas, search for all docs referencing the component name and update each one. Keep docs in sync with code.

## What is Oasis

Oasis is a high-performance Go framework for AI agent systems — fast, reliable, and built to scale with AI capabilities. The framework is the product. Reference apps (Telegram bot, etc.) are demos — DO NOT USE BOT CODE AS YOUR GUIDELINE.

## Build & Test

```bash
go build ./...                                # build root module
go test ./...                                 # run root tests
cd <satellite> && go test ./...               # run a satellite's tests
golangci-lint run ./...                       # enforce depguard rules
```

## Project Structure

The repo is a hybrid architecture: a single curated root package
(`github.com/nevindra/oasis`) re-exports protocol types and the most common
APIs from focused subpackages. Heavy or optional-dep code lives in
satellite modules with their own `go.mod` — users opt in by importing
the satellite directly.

```
oasis/                              # FRAMEWORK
|-- oasis.go                        # Re-export umbrella (curated public surface)
|-- doc.go                          # Top-level package documentation
|-- batch.go                        # Batch primitives (BatchJob, BatchStats)
|
|-- core/                           # Protocol types + interfaces + Erase helper (leaf package — depends on nothing in oasis)
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
|-- (satellites — each its own go.mod, opt-in via direct import)
|   |-- mcp/                        # MCP client integration
|   |-- store/{sqlite,postgres}/    # Storage backends (sqlite driver / pgx)
|   |-- provider/{gemini,openaicompat}/  # LLM providers (own evolution cadence)
|   |-- observer/                   # OTEL observability (full OTEL SDK)
|   |-- ingest/                     # Document ingestion (PDF, DOCX, embeddings)
|   |-- sandbox/                    # Sandbox interface + Tools() (implementations in separate repos)
|   |-- rag/                        # Retrieval-augmented generation

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
- **Minimum Go**: 1.24.
