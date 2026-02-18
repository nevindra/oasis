# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- `Network.Execute` and `Network.ExecuteStream` now fall back to the last sub-agent output when the router LLM returns an empty final response — fixes messages getting stuck on "Thinking..." when using a pure-routing LLM that doesn't synthesize a reply after delegating
- `store/sqlite`: eliminated `SQLITE_BUSY` (error 5) under concurrent writes — `Store` now keeps a single shared `*sql.DB` with `SetMaxOpenConns(1)` instead of opening a new connection per operation; `Close()` now properly closes the underlying connection
- `Network` sub-agent routing: task parameter description changed to "copied verbatim" — prevents the router LLM from paraphrasing or translating the user's message before delegating, which caused language switching and hallucinations
- `observer`: fixed data race in `ObservedProvider.ChatStream` — the chunk counter goroutine now signals completion via a done channel before the counter is read
- Semantic recall: cross-thread messages are now filtered by cosine similarity score before injection into LLM context, preventing irrelevant messages from polluting context (previously all top-K results were injected regardless of score)
- Semantic recall: messages from the current conversation thread are now excluded from cross-thread recall (they are already present in conversation history — previously they were double-injected)

### Added

- `Attachment` type (`MimeType string`, `Base64 string`) for multimodal content (images, PDFs, documents)
- `AgentTask.Attachments []Attachment` — attach binary content to a task; automatically wired into the user `ChatMessage` by the agent memory layer
- `Network` now forwards `AgentTask.Attachments` to sub-agents when routing — multimodal content is available at every level of a multi-agent hierarchy
- `ingest/pdf` extractor implemented using `ledongthuc/pdf` (pure Go, no CGO) — previously a stub; register with `ingest.WithExtractor(ingestpdf.TypePDF, ingestpdf.NewExtractor())`
- `WithRetry(p Provider, opts ...RetryOption) Provider` — wraps any Provider with automatic retry on transient HTTP errors (429, 503) using exponential backoff with jitter
- `ScoredMessage`, `ScoredChunk`, `ScoredSkill`, `ScoredFact` — scored result types that embed the original type with a `Score float32` field; returned by all semantic search methods so callers can make relevance decisions; `Score == 0` means the store did not compute similarity (e.g. libsql ANN index) — callers should treat it as "relevance unknown"
- `ConversationOption` type and `CrossThreadSearch(opts ...SemanticOption) ConversationOption` — opt-in cross-thread semantic recall as a sub-option of `WithConversationMemory`; e.g. `WithConversationMemory(store, CrossThreadSearch(MinScore(0.7)))`
- `SemanticOption` type and `MinScore(score float32) SemanticOption` — tune cross-thread search threshold (default 0.60); passed to `CrossThreadSearch`
- `WithEmbedding(e EmbeddingProvider)` — shared embedding provider used by `CrossThreadSearch` and `WithUserMemory`
- `ExtractedFact` type (`Fact string`, `Category string`) — represents a user fact extracted from a conversation turn
- **Auto fact extraction** — `WithUserMemory` now completes the full read+write cycle: after each conversation turn, the agent uses its own LLM to extract durable user facts from the exchange and persists them to `MemoryStore` via `UpsertFact`; no new option required; extraction runs in the background goroutine alongside message persistence

### Changed

- **Breaking:** `ChatMessage.Images []ImageData` renamed to `ChatMessage.Attachments []Attachment`; `ImageData` type renamed to `Attachment` — broader name reflects that PDFs and documents are also supported, not just images
- **Breaking:** `Store.SearchMessages`, `Store.SearchChunks`, `Store.SearchSkills` now return `[]ScoredMessage`, `[]ScoredChunk`, `[]ScoredSkill` respectively — `store/sqlite` computes brute-force cosine similarity; `store/libsql` returns `Score: 0` (ANN index does not expose scores)
- **Breaking:** `MemoryStore.SearchFacts` now returns `[]ScoredFact` instead of `[]Fact`
- `Scheduler` primitive — polls the store for due actions and executes them via any `Agent` (`NewScheduler`, `Start`, `WithSchedulerInterval`, `WithSchedulerTZOffset`, `WithOnRun`)
- Parallel tool execution — `LLMAgent` and `Network` now execute multiple tool calls concurrently using goroutines
  - When the LLM returns multiple tool calls in a single response, they run in parallel instead of sequentially
  - Results are collected in order; `PostToolProcessor` hooks still run sequentially after all calls complete
  - Applies to both `Execute()` and `ExecuteStream()` methods
- `StreamingAgent` interface — optional capability for agents that support token streaming
  - `ExecuteStream(ctx, task, chan<-string)` streams the final response tokens; tool-calling iterations remain blocking
  - Both `LLMAgent` and `Network` implement `StreamingAgent`; check via type assertion
- `AgentTask.Context` context key constants and typed accessors
  - Constants: `ContextThreadID`, `ContextUserID`, `ContextChatID`
  - Accessors: `task.TaskThreadID()`, `task.TaskUserID()`, `task.TaskChatID()`
- Memory wiring for agent primitives — `LLMAgent` and `Network` now support built-in conversation memory, user memory, and cross-thread recall
  - `WithConversationMemory(Store, ...ConversationOption)` — load/persist conversation history per thread; pass `CrossThreadSearch()` to enable cross-thread semantic recall
  - `WithEmbedding(EmbeddingProvider)` — shared embedding provider for cross-thread search and user memory
  - `WithUserMemory(MemoryStore)` — inject user facts into system prompt **and** auto-extract new facts after each turn (requires `WithEmbedding` and `WithConversationMemory`)

### Changed

- **Breaking:** `AgentTask.Context` type changed from `map[string]string` to `map[string]any` — enables rich metadata (attachments, typed values). Use typed accessors for standard keys.
- `LLMAgent` ask_user handler errors are now soft errors (returned as tool result strings) instead of hard errors — consistent with `Network.dispatch` behavior

## [0.2.0] - 2026-02-18

### Added

- `InputHandler` interface — human-in-the-loop for agents (built-in `ask_user` tool + programmatic gates via `InputHandlerFromContext`)
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
- `ObservedAgent` in `observer/` — OTEL lifecycle spans, metrics, and logs for any Agent execution
- `Spawn()` + `AgentHandle` — background agent execution with state tracking, cancellation, and select-based multiplexing
- ~110 new test cases across framework packages

### Changed

- Switch ID generation from xid to UUIDv7 (RFC 9562) — standard format, native Postgres `UUID` type support
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

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/nevindra/oasis/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/nevindra/oasis/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/nevindra/oasis/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nevindra/oasis/releases/tag/v0.1.0
