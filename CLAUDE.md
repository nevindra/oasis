# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read [docs/PHILOSOPHY.md](docs/PHILOSOPHY.md) before brainstorming or making architectural decisions** — framework identity, design principles, composability stance, and API strategy.

**Always read [docs/ENGINEERING.md](docs/ENGINEERING.md) before writing code** — coding standards, production engineering, performance rules, and things to never do.

**Before modifying any component, find and read all related docs:**

| Changing...              | Start from                                                  |
|--------------------------|-------------------------------------------------------------|
| Framework primitive      | [docs/concepts/](docs/concepts/index.md)                    |
| How-to / usage pattern   | [docs/guides/](docs/guides/index.md)                        |
| Interface or type        | [docs/api/](docs/api/index.md)                              |
| Config options           | [docs/configuration/](docs/configuration/index.md)          |
| Getting started flow     | [docs/getting-started/](docs/getting-started/index.md)      |

When your change affects multiple areas, search for all docs referencing the component name and update each one. Keep docs in sync with code.

## What is Oasis

Oasis is a high-performance Go framework for AI agent systems — fast, reliable, and built to scale with AI capabilities. The framework is the product. Reference apps (Telegram bot, etc.) are demos — DO NOT USE BOT CODE AS YOUR GUIDELINE.

## Build & Test

```bash
go build ./cmd/bot_example/              # build reference app
go test ./...                            # all tests
go test ./tools/schedule/ -run TestName  # single test
docker build -f cmd/bot_example/Dockerfile -t oasis .  # docker
```

## Project Structure

```
oasis/                              # FRAMEWORK (root package)
|-- types.go, provider.go, tool.go  # Protocol types + core interfaces
|-- store.go, memory.go
|-- processor.go                    # Processor interfaces + ProcessorChain
|-- agent.go, llmagent.go, network.go  # Agent primitives (composable)
|-- loop.go, suspend.go            # Execution engine, suspend/resume
|-- batch.go, stream.go            # Batch primitives, SSE streaming
|-- agentmemory.go                 # Shared memory wiring
|-- workflow.go, workflow_exec.go    # DAG-based orchestration
|-- workflow_steps.go, workflow_definition.go
|-- input.go                       # InputHandler (human-in-the-loop)
|-- handle.go                      # Spawn() + AgentHandle
|
|-- provider/{gemini,openaicompat}/ # LLM providers (raw HTTP, no SDKs)
|-- store/{sqlite,postgres}/        # Storage implementations
|-- memory/                        # Storage-agnostic memory helpers
|-- observer/                       # OTEL observability wrappers
|-- retriever.go                    # Retrieval pipeline (Retriever, Reranker, HybridRetriever)
|-- ingest/                         # Document chunking pipeline
|-- tools/{knowledge,remember,search,schedule,shell,file,http,data}/
|
|-- cmd/bot_example/               # REFERENCE APP (demo, not the product)
|-- internal/{config,bot}/         # App config + orchestration
```

## Releasing

- **Changelog**: update [CHANGELOG.md](CHANGELOG.md) using [Keep a Changelog](https://keepachangelog.com/) format. New changes under `[Unreleased]`. When tagging, rename to `[x.y.z] - date` and add fresh `[Unreleased]`.
- **Versioning** (semver, v0.x.x): patch = bug fix, minor = new features or breaking changes, major = reserved for v1.0.0+. Strict rule: patch releases must NEVER introduce new types, interfaces, or exported functions — only bug fixes. New interfaces/types always require a minor bump.
- **Tagging**: `git tag vX.Y.Z && git push origin master vX.Y.Z`. Go proxy indexes automatically.
- **Immutable**: once tagged on `proxy.golang.org`, never re-tag — always bump version.
- **Minimum Go**: 1.24.
