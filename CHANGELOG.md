# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- Comprehensive test suite: ~110 new test cases across agent, tool registry, app, scheduler, schedule, memory/sqlite, observer, errors, types, and tools (shell, file, schedule)
- Workflow primitive: deterministic DAG-based task orchestration (`workflow.go`)
  - Step types: `Step` (function), `AgentStep` (Agent delegation), `ToolStep` (tool call), `ForEach` (collection iteration), `DoUntil`/`DoWhile` (loops)
  - Step options: `After` (dependencies), `When` (conditions), `InputFrom`/`ArgsFrom`/`OutputTo` (data routing), `Retry`, `IterOver`, `Concurrency`, `Until`, `While`, `MaxIter`
  - Workflow options: `WithOnFinish`, `WithOnError`, `WithDefaultRetry`
  - Shared `WorkflowContext` with concurrent-safe `Get`/`Set`
  - `ForEachItem`/`ForEachIndex` helpers for per-goroutine iteration data
  - DAG validation at construction: duplicate detection, unknown dependency check, cycle detection (Kahn's algorithm)
  - Fail-fast error handling with configurable retries and failure cascade tracking
  - Implements `Agent` interface for recursive composition with Network and other Workflows

### Fixed

- `parseScheduledToolCalls` partial unmarshal contamination causing legacy format to return duplicates

## [0.1.2] - 2026-02-18

### Changed

- Use canonical Apache-2.0 license text from apache.org

## [0.1.1] - 2026-02-18

### Changed

- Switch license from MPL-2.0 to Apache-2.0

## [0.1.0] - 2026-02-17

Initial release of the Oasis AI agent framework.

### Added

- Core interfaces: Provider, Store, Tool, Frontend, Agent, MemoryStore, EmbeddingProvider
- Agent primitives: LLMAgent (single-agent tool loop), Network (multi-agent coordination)
- Processor pipeline: PreProcessor, PostProcessor, PostToolProcessor
- Providers: Gemini, OpenAI-compatible (OpenAI, Anthropic, Ollama)
- Storage: SQLite (local), libSQL/Turso (remote)
- Memory: SQLite-backed semantic memory with confidence and decay
- Frontend: Telegram bot with streaming support
- Tools: knowledge (RAG), remember, search, schedule, shell, file, http
- Observability: OpenTelemetry wrappers with cost tracking
- Document ingestion: HTML, Markdown, plain text chunking pipeline
- Channel-based token streaming with edit batching
- Reference app in cmd/bot_example/

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/nevindra/oasis/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/nevindra/oasis/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nevindra/oasis/releases/tag/v0.1.0
