# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Documentation

**Always read and follow these docs before making changes:**

- **[docs/ENGINEERING.md](docs/ENGINEERING.md)** — Engineering principles: forward-compatible design, performance, developer experience, dependency philosophy, error handling, testing approach.
- **[docs/CONVENTIONS.md](docs/CONVENTIONS.md)** — Coding conventions: error handling, import ordering, naming, config patterns, database patterns, logging, testing rules, LLM-readability, and things to never do.

**Framework documentation (for users/extenders):**

- **[docs/framework/](docs/framework/README.md)** — Complete framework docs: getting started, configuration reference, architecture overview, extending guide (tools/providers/frontends/stores), deployment guide, and full API reference.

**When you make architectural or convention changes, update the corresponding doc file to keep them in sync.**

## What is Oasis

Oasis is an AI agent framework in Go — built to evolve as AI capabilities grow toward AGI. It provides primitives for building AI systems today (tool-calling agents, multi-agent networks, knowledge stores, long-term memory) that are designed to remain relevant as agents become more autonomous, self-directing, and capable.

The framework is the product. Everything else — the Telegram bot, the reference app — is a demo of what the framework can do. DO NOT USE BOT CODE AS YOUR GUIDELINE.

**When Oasis is mentioned, it means the FRAMEWORK, not any specific application built on it.**

## Design Philosophy (quick reference)

See [docs/ENGINEERING.md](docs/ENGINEERING.md) for the full mental model.

1. **AGI-ready** — every interface and primitive asks: "will this still work when agents get 10x smarter?" Design for capabilities that don't exist yet.
2. **Forward-compatible** — add, don't remove. Extend interfaces via composition, not modification. Deprecate before deleting. Breaking changes are a last resort.
3. **Framework > app** — framework primitives are the product. Reference apps are demos. Invest design time in primitives, ship fast on app code.
4. **Dual-audience code** — code is read by both humans and LLMs. Godoc on every exported symbol. Interface contracts in comments. Names that explain intent. Examples in tests.
5. **Performance with great DX** — optimize what users feel (latency, API calls), not micro-benchmarks. Make the fast path obvious and the correct path easy.

## Build & Test

```bash
go build ./cmd/bot_example/              # build reference app
go test ./...                            # all tests
go test ./tools/schedule/ -run TestName  # single test
docker build -f cmd/bot_example/Dockerfile -t oasis .  # docker
```

Config: defaults -> `oasis.toml` -> env vars (env wins). See [docs/framework/configuration.md](docs/framework/configuration.md).

## Architecture (quick reference)

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for full details.

### Project Structure

```
oasis/                              # FRAMEWORK
|-- types.go, provider.go, tool.go  # Protocol types + core interfaces
|-- store.go, frontend.go, memory.go
|-- processor.go                    # Processor interfaces + ProcessorChain
|-- agent.go, llmagent.go, network.go  # Agent primitives (composable)
|-- workflow.go                     # Workflow primitive (DAG-based orchestration)
|
|-- provider/gemini/               # Google Gemini (Provider + EmbeddingProvider)
|-- provider/openaicompat/         # OpenAI-compatible (Provider)
|-- frontend/telegram/             # Telegram bot (Frontend)
|-- store/sqlite/                  # Local SQLite (Store)
|-- store/libsql/                  # Remote Turso (Store)
|-- memory/sqlite/                 # SQLite MemoryStore
|-- observer/                      # OTEL observability (wraps Provider/Tool/Embedding)
|-- ingest/                        # Document chunking pipeline
|-- tools/{knowledge,remember,search,schedule,shell,file,http}/
|
|-- cmd/bot_example/main.go        # REFERENCE APP (demo, not the product)
|-- internal/config/               # Config loading
|-- internal/bot/                  # Application orchestration
```

### Core Interfaces (root package)

| Interface | File | Purpose |
|-----------|------|---------|
| `Provider` | `provider.go` | LLM backend: Chat, ChatWithTools, ChatStream |
| `EmbeddingProvider` | `provider.go` | Text-to-vector embedding |
| `Store` | `store.go` | Persistence + vector search |
| `MemoryStore` | `memory.go` | Long-term semantic memory |
| `Tool` | `tool.go` | Pluggable tool for LLM function calling |
| `Frontend` | `frontend.go` | Messaging platform: Poll, Send, Edit |
| `Agent` | `agent.go` | Composable work unit: LLMAgent, Network, Workflow, or custom |
| `PreProcessor` | `processor.go` | Transform/validate messages before LLM call |
| `PostProcessor` | `processor.go` | Transform/validate LLM responses before tool execution |
| `PostToolProcessor` | `processor.go` | Transform/validate tool results before appending to history |

### Agent Primitives

Agents compose recursively — Networks contain Agents, Workflows orchestrate Agents, and all three implement Agent:

```go
// Single agent with tools
researcher := oasis.NewLLMAgent("researcher", "Searches info", provider, oasis.WithTools(searchTool))

// Agent with processors (guardrails, PII redaction, etc.)
guarded := oasis.NewLLMAgent("guarded", "Safe agent", provider,
    oasis.WithTools(searchTool),
    oasis.WithProcessors(&guardrail, &piiRedactor),
)

// Multi-agent network (router delegates to subagents)
team := oasis.NewNetwork("team", "Coordinates", router, oasis.WithAgents(researcher, writer))

// Deterministic workflow (explicit DAG, no LLM routing)
pipeline, _ := oasis.NewWorkflow("pipeline", "Research and write",
    oasis.Step("prepare", prepareFn),
    oasis.AgentStep("research", researcher, oasis.After("prepare")),
    oasis.AgentStep("write", writer, oasis.InputFrom("research.output"), oasis.After("research")),
)

// Networks of networks (or workflows)
org := oasis.NewNetwork("org", "Top-level", ceo, oasis.WithAgents(team, pipeline))
```

### Key Design Decisions

- **No LLM SDKs** — all providers use raw HTTP (`net/http`)
- **Interface-driven** — every major component is a Go interface
- **Constructor injection** — no global state, dependencies via structs
- **Forward-compatible** — interfaces grow by addition, never removal
- **Streaming** — channel-based token streaming with 1s edit batching
- **Pure-Go SQLite** — `modernc.org/sqlite`, no CGO required

## Releasing

- **Changelog**: update [CHANGELOG.md](CHANGELOG.md) using [Keep a Changelog](https://keepachangelog.com/) format. New changes go under `[Unreleased]`. When tagging, rename `[Unreleased]` to `[x.y.z] - date` and add a fresh `[Unreleased]` section.
- **Versioning** (semver, currently v0.x.x):
  - Patch (0.1.**x**): bug fixes, no API change
  - Minor (0.**x**.0): new features, new API, or breaking changes (breaking in minor is expected while v0)
  - Major: reserved for v1.0.0+
- **Tagging**: `git tag vX.Y.Z && git push origin master vX.Y.Z`. Go proxy indexes automatically.
- **Immutable**: once a tag hits `proxy.golang.org`, it's cached forever. Never re-tag, always bump version.
- **Minimum Go version**: 1.24 (floor set by `modernc.org/sqlite` and `go.opentelemetry.io/otel`). Run `go mod tidy` after dependency updates to verify.

## Engineering Principles (quick reference)

1. **Design for the future, don't break the past** — primitives should accommodate agents that spawn sub-agents, manage their own memory, and negotiate with peers. Add, don't remove. Extend via composition, not modification.
2. **Earn every abstraction, design for composability** — concrete first for app code, composability-first for framework primitives. Interfaces at natural boundaries, primitives that snap together.
3. **Optimize for the reader** — both human and LLM readers. Names explain intent, godoc on exports, interface contracts in comments, examples in tests.
4. **Make it fast where it matters** — user-perceived latency and API call count, not micro-optimizations.
5. **Fail gracefully** — degrade don't die, distinguish transient vs permanent, never crash on recoverable errors.
6. **Own your dependencies** — hand-roll < 200 lines, no SDKs for external APIs, raw HTTP.
7. **Explicit over magic, respect the user's time** — constructor injection, no hidden side effects, actionable errors, sensible defaults, living documentation.
8. **Ship with confidence** — working > perfect for app code, right > fast for framework primitives. Test behavior not implementation, edge cases > happy path.
