# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `Workflow` agent primitive — deterministic DAG-based task orchestration
  - Step types: `Step`, `AgentStep`, `ToolStep`, `ForEach`, `DoUntil`/`DoWhile`
  - DAG validation at construction (duplicate, unknown dep, cycle detection)
  - Shared `WorkflowContext` with concurrent-safe `Get`/`Set`
  - Implements `Agent` interface for recursive composition
- `Ingestor` — end-to-end extract → chunk → embed → store API with batched embedding
- `Extractor` interface with built-in `PlainTextExtractor`, `HTMLExtractor`, `MarkdownExtractor`
- `Chunker` interface with `RecursiveChunker` (improved sentence boundaries) and `MarkdownChunker` (heading-aware)
- `StrategyParentChild` — two-level hierarchical chunking with `Chunk.ParentID` linking
- `Store.GetChunksByIDs` for batch chunk retrieval (parent-child resolution)
- PDF extractor stub subpackage (`ingest/pdf/`)
- ~110 new test cases across framework packages

### Changed

- `ContentType` is now `string` (MIME type) instead of `int` enum
- Remember tool uses `Ingestor` instead of manual embed+store boilerplate
- Search tool uses `Chunker` interface instead of `ChunkerConfig`

### Removed

- `Pipeline`, `PipelineResult`, `NewPipeline` — use `Ingestor`
- `ChunkerConfig`, `DefaultChunkerConfig()`, `ChunkText()` — use `NewRecursiveChunker()`
- `ExtractText()` — use `Extractor` implementations

### Fixed

- `parseScheduledToolCalls` partial unmarshal contamination causing duplicates

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
