# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read [docs/ENGINEERING.md](docs/ENGINEERING.md) before making changes or in brainstorming session** — engineering principles, production engineering, extensibility, and things to never do.

**Read the relevant doc before modifying a component:**

- **[docs/concepts/](docs/concepts/index.md)** — Architecture + every primitive:
  [provider](docs/concepts/provider.md), [agent](docs/concepts/agent.md), [network](docs/concepts/network.md), [workflow](docs/concepts/workflow.md), [tool](docs/concepts/tool.md), [store](docs/concepts/store.md), [memory](docs/concepts/memory.md), [processor](docs/concepts/processor.md), [input-handler](docs/concepts/input-handler.md), [scheduler](docs/concepts/scheduler.md), [ingest](docs/concepts/ingest.md), [retrieval](docs/concepts/retrieval.md), [graph-rag](docs/concepts/graph-rag.md), [observability](docs/concepts/observability.md), [code-execution](docs/concepts/code-execution.md)
- **[docs/guides/](docs/guides/)** — How-to guides:
  [custom-tool](docs/guides/custom-tool.md), [custom-provider](docs/guides/custom-provider.md), [custom-store](docs/guides/custom-store.md), [custom-agent](docs/guides/custom-agent.md), [processors-and-guardrails](docs/guides/processors-and-guardrails.md), [human-in-the-loop](docs/guides/human-in-the-loop.md), [memory-and-recall](docs/guides/memory-and-recall.md), [streaming](docs/guides/streaming.md), [background-agents](docs/guides/background-agents.md), [skills](docs/guides/skills.md), [ingesting-documents](docs/guides/ingesting-documents.md), [rag-pipeline](docs/guides/rag-pipeline.md), [execution-plans](docs/guides/execution-plans.md), [code-execution](docs/guides/code-execution.md), [image-generation](docs/guides/image-generation.md)
- **[docs/api/](docs/api/)** — [interfaces](docs/api/interfaces.md), [types](docs/api/types.md), [constructors](docs/api/constructors.md), [options](docs/api/options.md), [errors](docs/api/errors.md)
- **[docs/configuration/](docs/configuration/index.md)** — Config overview + [full reference](docs/configuration/reference.md)
- **[docs/getting-started/](docs/getting-started/index.md)** — Installation, quick start, reference app

**When you make architectural or convention changes, update the corresponding doc file to keep them in sync.**

## What is Oasis

Oasis is an AI agent framework in Go — built to evolve as AI capabilities grow toward AGI. The framework is the product. Reference apps (Telegram bot, etc.) are demos — DO NOT USE BOT CODE AS YOUR GUIDELINE.

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
|-- agentmemory.go                 # Shared memory wiring
|-- workflow.go                     # DAG-based orchestration
|-- input.go                       # InputHandler (human-in-the-loop)
|-- handle.go                      # Spawn() + AgentHandle
|
|-- provider/{gemini,openaicompat}/ # LLM providers (raw HTTP, no SDKs)
|-- store/{sqlite,libsql,postgres}/ # Storage implementations
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
