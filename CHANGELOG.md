# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **Plan execution** (`WithPlanExecution()`) — built-in `execute_plan` tool that batches multiple tool calls in a single LLM turn. The LLM calls it with an array of steps (tool name + args), and the framework executes all steps in parallel without re-sampling between each call. Returns structured per-step JSON results with ok/error status. Reduces latency and token usage for fan-out patterns (e.g., querying 5 regions = 2 samplings instead of 6). Works with both `LLMAgent` and `Network`. Inspired by [Anthropic's programmatic tool calling](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/programmatic-tool-calling), implemented provider-agnostically.
- **`ServeSSE` HTTP helper** — stream agent responses as Server-Sent Events with zero boilerplate; handles flusher validation, SSE headers, channel management, done/error events, and client disconnection via context cancellation
- **Conversation model documentation** — new section in Store docs explaining the ChatID/UserID/ThreadID hierarchy, context key mapping, and common patterns (single-user, multi-user, ownership checks)

## [0.4.0] - 2026-02-19

### Added

- **Multimodal output plumbing** — `ChatResponse` and `AgentResult` now carry `Attachments []Attachment`, enabling providers to return images, audio, and other media through the agent pipeline to application code
- **Structured streaming events** — `StreamEvent` type with 5 typed event types for rich UI support
  - `EventTextDelta` — LLM text token (emitted by providers)
  - `EventToolCallStart` — tool invocation begins (emitted by runLoop before dispatch)
  - `EventToolCallResult` — tool invocation completes (emitted by runLoop after dispatch)
  - `EventAgentStart` — sub-agent begins execution (emitted by Network dispatch)
  - `EventAgentFinish` — sub-agent completes execution (emitted by Network dispatch)
  - `StreamEvent` struct carries `Type`, `Name`, `Content`, and `Args` (JSON) with serialization tags for SSE/WebSocket consumers

### Changed

- **Breaking:** `Provider.ChatStream` signature changed from `chan<- string` to `chan<- StreamEvent` — providers now emit typed events instead of raw strings
- **Breaking:** `StreamingAgent.ExecuteStream` signature changed from `chan<- string` to `chan<- StreamEvent` — consumers receive structured events with tool call and agent delegation visibility

### Removed

- **Breaking:** `Frontend` interface, `IncomingMessage`, and `FileInfo` types removed from root package — no framework core code depended on them; messaging platform abstraction is now an application-level concern
- **Breaking:** `frontend/telegram` package removed — Telegram bot implementation moved to reference app

## [0.3.2] - 2026-02-19

### Fixed

- **ScheduledAction `skill_id` never persisted** — `CreateScheduledAction`, `UpdateScheduledAction`, and all read queries now include `skill_id` across postgres, sqlite, and libsql stores
- **`UpdateSkill` keeps stale embedding** — postgres and libsql now set `embedding=NULL` when no new embedding is provided, matching sqlite behavior
- **`DecayOldFacts` silently discards UPDATE error** — postgres and sqlite `MemoryStore` now propagate the decay UPDATE error instead of ignoring it
- **Embedding serialization precision loss** — postgres and libsql `serializeEmbedding` now uses `strconv.FormatFloat` for full float32 precision instead of `fmt.Sprintf("%g")`
- **Batch API support** — framework-level `BatchProvider` and `BatchEmbeddingProvider` interfaces for asynchronous batch processing at reduced cost
  - `BatchProvider` interface — `BatchChat`, `BatchStatus`, `BatchChatResults`, `BatchCancel` for offline batch chat generation
  - `BatchEmbeddingProvider` interface — `BatchEmbed`, `BatchEmbedStatus`, `BatchEmbedResults` for batch embedding
  - `BatchJob`, `BatchState`, `BatchStats` types — provider-agnostic job lifecycle tracking
  - Gemini implementation (`provider/gemini`) — inline batch requests via `batchGenerateContent` and `batchEmbedContent` endpoints; 50% cost reduction vs standard API

### Changed

- **SQLite MemoryStore consolidated** — `memory/sqlite` package removed; use `sqlite.NewMemoryStore(store.DB())` from `store/sqlite` instead (matches `store/postgres` pattern). Shares the same `*sql.DB` connection, fixing the open-close-per-call anti-pattern.

## [0.3.1] - 2026-02-19

### Added

- **MCP docs server** (`mcp/`, `cmd/mcp-docs`) — stdio-based MCP server exposing all framework documentation as resources (`oasis://` URIs) with `search_docs` tool; docs embedded at build time via `//go:embed`

## [0.3.0] - 2026-02-19

### Added

- **PostgreSQL + pgvector storage** (`store/postgres`) — full `Store`, `KeywordSearcher`, and `MemoryStore` implementation backed by PostgreSQL with pgvector
  - Native `vector` columns with HNSW indexes for cosine distance search — no brute-force scanning
  - Full-text keyword search via `tsvector`/`tsquery` with GIN expression index (automatic sync, no FTS5 virtual table)
  - `MemoryStore` with pgvector-powered semantic deduplication (cosine similarity in SQL instead of in-process)
  - Constructor injection: `postgres.New(pool)` and `postgres.NewMemoryStore(pool)` accept an externally-owned `*pgxpool.Pool` — share one pool across Store, MemoryStore, and application code
  - Dependency: `github.com/jackc/pgx/v5` (pure Go, no CGO)

- **Retrieval pipeline overhaul** — composable hybrid search with re-ranking for RAG
  - `Retriever` interface — search a knowledge base and return ranked `RetrievalResult`s
  - `Reranker` interface — re-score retrieval results for improved precision
  - `KeywordSearcher` interface — optional `Store` capability for full-text keyword search (discovered via type assertion)
  - `HybridRetriever` — default implementation combining vector search + FTS5 keyword search with Reciprocal Rank Fusion, parent-child chunk resolution, and optional re-ranking
  - `ScoreReranker` — score-based filtering and re-sorting (zero-dependency baseline)
  - `LLMReranker` — LLM-powered relevance scoring via `Provider`
  - FTS5 keyword search in both SQLite and libsql stores (implements `KeywordSearcher`)
  - `RetrieverOption` functional options: `WithReranker`, `WithMinRetrievalScore`, `WithKeywordWeight`, `WithOverfetchMultiplier`

### Changed

- `KnowledgeTool` now delegates to `Retriever` interface — backward-compatible constructor; existing `New(store, emb)` calls auto-create a `HybridRetriever`. New options: `WithRetriever(r)`, `WithTopK(n)`

- **Ingest extractors and chunk metadata** — new content types with structured metadata flowing through the pipeline
  - `ChunkMeta` type — per-chunk metadata (page number, section heading, source URL, images) stored as JSON in the `metadata` column
  - `Image` type — extracted image representation (MIME type, base64 data, alt text, page)
  - `MetadataExtractor` interface — optional capability for extractors to return `ExtractResult` with `PageMeta` alongside text; ingestor auto-detects via type assertion
  - `csv.Extractor` (`ingest/csv`) — first row as headers, subsequent rows as labeled paragraphs
  - `json.Extractor` (`ingest/json`) — recursive flattening with dotted key paths
  - `docx.Extractor` (`ingest/docx`) — paragraphs, headings, tables, images via `archive/zip` + `encoding/xml` (pure Go, no CGO); implements `MetadataExtractor`
  - PDF extractor upgraded to `MetadataExtractor` — page-by-page extraction with `PageMeta` per page
  - Content type detection for `.csv`, `.json`, `.docx` extensions
  - SQLite and libSQL stores: `metadata TEXT` column on `chunks` table with JSON serialization/deserialization
  - Ingestor metadata wiring: byte-range overlap matching assigns `PageMeta` to chunks via `assignMeta()`

### Fixed

- Parent-child chunk resolution — `StrategyParentChild` now works end-to-end; matched child chunks are resolved to their parent's richer context during retrieval

### Added

- `MaxHistory(n int) ConversationOption` — control how many recent messages are loaded into LLM context per thread; pass to `WithConversationMemory(store, MaxHistory(30))`
- **Suspend/Resume** — pause agent or workflow execution to await external input, then continue from where it left off
  - `Suspend(payload json.RawMessage) error` — call from any step function or processor to signal the engine to pause
  - `ErrSuspended` — returned by `Execute()` when execution is suspended; inspect `Payload` for context, call `Resume(ctx, data)` to continue
  - `ResumeData(wCtx *WorkflowContext) (json.RawMessage, bool)` — retrieve resume data inside a workflow step
  - `StepSuspended` step status — marks the step that triggered the suspension
  - Works in Workflows (DAG state preserved across suspend/resume cycles), LLMAgent, and Network (conversation history preserved)
  - Processors (`PreProcessor`, `PostProcessor`, `PostToolProcessor`) can return `Suspend()` to trigger human-in-the-loop gates

## [0.2.1] - 2026-02-18

### Changed

- **Breaking:** `Workflow.Execute` now returns `*WorkflowError` (non-nil `error`) when a step fails — previously returned `nil` error with the failure description embedded in `AgentResult.Output`. Callers that check `err != nil` will now correctly detect workflow failures. Use `errors.As(err, &wfErr)` to access per-step results via `wfErr.Result`
- **Internal**: extracted shared `runLoop` from `LLMAgent` and `Network` — the core tool-calling loop (Execute + ExecuteStream) now lives in a single function in `agent.go`, eliminating 4-way code duplication. Both types delegate via a `dispatchFunc` callback. No public API changes.

### Added

- `WorkflowError` type — returned by `Workflow.Execute` on step failure; carries `StepName`, `Err` (unwrappable via `errors.Is`/`errors.As`), and the full `WorkflowResult` with per-step outcomes

### Fixed

- `truncateStr` now operates on runes instead of bytes — previously could produce invalid UTF-8 when truncating multibyte strings (emoji, CJK, Arabic)
- `Workflow` `Retry()` option now works on all step types — previously `ForEach`, `DoUntil`, and `DoWhile` steps silently ignored retry configuration; only basic `Step` was retried
- `Workflow` `ForEach` now cancels remaining iterations on first failure — previously all sibling goroutines continued running to completion even after one failed, wasting resources and risking side effects
- `Network.Execute` and `Network.ExecuteStream` now fall back to the last sub-agent output when the router LLM returns an empty final response — fixes messages getting stuck on "Thinking..." when using a pure-routing LLM that doesn't synthesize a reply after delegating
- `store/sqlite`: eliminated `SQLITE_BUSY` (error 5) under concurrent writes — `Store` now keeps a single shared `*sql.DB` with `SetMaxOpenConns(1)` instead of opening a new connection per operation; `Close()` now properly closes the underlying connection
- `Network` sub-agent routing: task parameter description changed to "copied verbatim" — prevents the router LLM from paraphrasing or translating the user's message before delegating, which caused language switching and hallucinations
- `observer`: fixed data race in `ObservedProvider.ChatStream` — the chunk counter goroutine now signals completion via a done channel before the counter is read
- Semantic recall: cross-thread messages are now filtered by cosine similarity score before injection into LLM context, preventing irrelevant messages from polluting context (previously all top-K results were injected regardless of score)
- Semantic recall: messages from the current conversation thread are now excluded from cross-thread recall (they are already present in conversation history — previously they were double-injected)
- Background persist goroutine now uses `context.WithoutCancel` — message persistence and fact extraction complete even when the parent handler context is canceled; context values (trace IDs) are still inherited
- User messages are now embedded before storing (single write) instead of store-then-embed-then-re-store (double write)
- `Workflow` `AgentStep` now propagates `AgentTask.Context` (thread ID, user ID, chat ID) and `Attachments` to sub-agents — previously these were dropped, silently breaking conversation memory, user memory, and cross-thread search for agents running inside workflows

### Added

- `Attachment` type (`MimeType string`, `Base64 string`) for multimodal content (images, PDFs, documents)
- `AgentTask.Attachments []Attachment` — attach binary content to a task; automatically wired into the user `ChatMessage` by the agent memory layer
- `Network` now forwards `AgentTask.Attachments` to sub-agents when routing — multimodal content is available at every level of a multi-agent hierarchy
- `ingest/pdf` extractor implemented using `ledongthuc/pdf` (pure Go, no CGO) — previously a stub; register with `ingest.WithExtractor(ingestpdf.TypePDF, ingestpdf.NewExtractor())`
- `WithRetry(p Provider, opts ...RetryOption) Provider` — wraps any Provider with automatic retry on transient HTTP errors (429, 503) using exponential backoff with jitter
- `ScoredMessage`, `ScoredChunk`, `ScoredSkill`, `ScoredFact` — scored result types that embed the original type with a `Score float32` field; returned by all semantic search methods so callers can make relevance decisions; `Score == 0` means the store did not compute similarity (e.g. libsql ANN index) — callers should treat it as "relevance unknown"
- `ConversationOption` type and `CrossThreadSearch(e EmbeddingProvider, opts ...SemanticOption) ConversationOption` — opt-in cross-thread semantic recall as a sub-option of `WithConversationMemory`; e.g. `WithConversationMemory(store, CrossThreadSearch(embedding, MinScore(0.7)))`
- `SemanticOption` type and `MinScore(score float32) SemanticOption` — tune cross-thread search threshold (default 0.60); passed to `CrossThreadSearch`
- `ExtractedFact` type (`Fact string`, `Category string`, `Supersedes *string`) — represents a user fact extracted from a conversation turn; optional `Supersedes` field names the old fact being replaced
- `MemoryStore.DeleteFact(ctx, factID)` — delete a single fact by ID; used by the supersedes pipeline to remove contradicted facts
- **Auto fact extraction** — `WithUserMemory` now completes the full read+write cycle: after each conversation turn, the agent uses its own LLM to extract durable user facts from the exchange and persists them to `MemoryStore` via `UpsertFact`; no new option required; extraction runs in the background goroutine alongside message persistence
- **Trivial message filter** — extraction is skipped for short or trivial messages ("ok", "thanks", "wkwk", etc.) to avoid wasted LLM calls
- **Semantic supersedes** — when the extraction LLM detects a contradicting fact (e.g. "moved to Bali" superseding "lives in Jakarta"), the pipeline embeds the superseded text, searches for matching facts (cosine >= 0.80), and deletes them before upserting the new fact
- **Probabilistic fact decay** — `DecayOldFacts` is now called automatically with ~5% probability per conversation turn, so stale facts decay without external scheduling

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
  - `WithConversationMemory(Store, ...ConversationOption)` — load/persist conversation history per thread; pass `CrossThreadSearch(embedding)` to enable cross-thread semantic recall
  - `WithUserMemory(MemoryStore, EmbeddingProvider)` — inject user facts into system prompt **and** auto-extract new facts after each turn (requires `WithConversationMemory` for write path)
- **Breaking:** `CrossThreadSearch` now takes `EmbeddingProvider` as required first argument — previously a standalone `WithEmbedding` option was needed; compile-time enforcement prevents silent misconfiguration
- **Breaking:** `WithUserMemory` now takes `EmbeddingProvider` as required second argument — previously required a separate `WithEmbedding` call; compile-time enforcement prevents silent misconfiguration
- **Breaking:** removed `WithEmbedding(EmbeddingProvider)` — embedding provider is now passed directly to `CrossThreadSearch` and `WithUserMemory`

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

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/nevindra/oasis/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nevindra/oasis/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/nevindra/oasis/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nevindra/oasis/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/nevindra/oasis/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/nevindra/oasis/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nevindra/oasis/releases/tag/v0.1.0
