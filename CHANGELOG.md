# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **`Message.Metadata` field** — `map[string]any` on `Message` for flexible per-message metadata, persisted as JSON (SQLite/libSQL) or JSONB (PostgreSQL). When `WithConversationMemory` is enabled, assistant messages automatically include execution traces (`steps` key) from `AgentResult.Steps`, giving any Oasis app persisted execution traces for free. Schema migration is automatic (best-effort `ALTER TABLE` for existing databases)
- **`AutoTitle()` conversation option** — opt-in automatic thread title generation from the first user message; runs in the background alongside message persistence; skipped when thread already has a title. Usage: `WithConversationMemory(store, AutoTitle())`
- **`WriteSSEEvent` helper** — composable primitive for writing individual Server-Sent Events. Handles JSON marshaling and flushing, letting developers build custom SSE loops with `ExecuteStream` without reimplementing SSE mechanics
- **Image generation support** — added `gemini.WithResponseModalities()` option to enable image output from Gemini models (e.g. `WithResponseModalities("TEXT", "IMAGE")`). Generated images are returned as `Attachment` structs on `AgentResult.Attachments`. See [image generation guide](docs/guides/image-generation.md)
- **`DispatchResult` struct** — replaces the `(string, Usage)` tuple return from `DispatchFunc` with a struct that also carries `Attachments`. Sub-agent attachments (e.g. generated images) now propagate through Network dispatch to the final `AgentResult`. **Breaking:** `DispatchFunc` signature changed from `func(ctx, ToolCall) (string, Usage)` to `func(ctx, ToolCall) DispatchResult`

### Changed

- **`ServeSSE` done event** — the `done` event now sends the full `AgentResult` (output, steps, usage) instead of `[DONE]`, so frontends can access execution metadata without an extra call
- **KnowledgeTool documentation** — expanded godoc on `KnowledgeTool` and the RAG pipeline guide to clarify that retrieval behavior (score threshold, chunk filters, keyword weight, re-ranking) is configured on the `Retriever` via `WithRetriever`, not on the tool itself. Added examples for custom retrieval, vector-only search, and an options summary table
- **Synthesis call now traced** — the forced-response LLM call at max-iterations now emits an `agent.loop.synthesis` span when a tracer is configured
- **Workflow expression operators must be space-bounded** — `evalExpression` now requires operators surrounded by spaces (e.g. `{{x}} == y`). All existing examples already use this format. Prevents false matches when literal values contain operator substrings
- **Gemini `thinkingConfig` no longer sent by default** — previously `thinkingBudget: 0` was always included in `generationConfig` when thinking was disabled, which caused 400 errors on models that don't support it (e.g. image generation models). Now only sent when `WithThinking(true)` is set
- **Gemini `mediaResolution` now opt-in** — previously always sent as `"MEDIA_RESOLUTION_MEDIUM"`. Now only included when explicitly set via `WithMediaResolution()`, avoiding 400 errors on models that don't support this parameter

### Fixed

- **`chunkParentChild` panic on overlapping child chunks** — `RecursiveChunker` adds overlap prefixes from the previous chunk. When such a prefix caused `strings.Index` to return `-1`, `childOffset` advanced by the full chunk length (including overlap), eventually exceeding `len(pt)` and panicking with `slice bounds out of range`. Fixed by clamping `childOffset` to `len(pt)` after each iteration
- **`chunkFlat` offset corruption on overlapping chunks** — same root cause as the `chunkParentChild` panic: `offset` was set to `endByte` unconditionally, accumulating past `len(text)` when overlap-prefixed chunks were not found via `strings.Index`. Fixed by clamping `offset` to `len(text)`
- **`getOverlapSuffix` invalid UTF-8 output** — `text[len(text)-n:]` was a raw byte slice that could land in the middle of a multibyte rune, producing an invalid UTF-8 prefix prepended to the next chunk. Fixed by stepping forward to the next valid rune boundary
- **`splitOnWords` invalid UTF-8 output for long words** — forced word splits stepped by `maxChars` bytes, cutting multibyte runes in half. Fixed with rune-boundary-aware stepping; falls back to stepping one rune at a time when a single rune exceeds `maxChars`
- **`percentileThreshold` panic when `percentile > 100`** — an out-of-range `breakpointPercentile` (e.g. passed via `WithBreakpointPercentile(200)`) caused `lower` to exceed `len(sorted)-1`, resulting in an index out-of-bounds panic. Fixed by clamping `percentile` to `[0, 100]` at the start of the function
- **`TestServeSSE` expected stale done format** — test asserted `data: [DONE]` but `ServeSSE` sends JSON-serialized `AgentResult` since the done event change. Updated to parse and verify the `AgentResult` payload
- **Streaming channel leak in no-tools path** — when `ChatStream` succeeded but `PostProcessor` returned an error (or suspend), `ch` was never closed, leaking any `ServeSSE` consumer blocking on `for ev := range ch`
- **Shallow `Metadata` copy in `checkSuspendLoop`** — `ChatMessage.Metadata` (`json.RawMessage`, a `[]byte`) was not deep-copied in the resume snapshot, sharing the backing array with the original messages. Now deep-copies alongside `ToolCalls`/`Attachments`
- **Expression evaluator matched operators inside literal values** — `evalExpression` used `strings.Index` to find operators, so a value like `not-equal` would match `!=`. Now requires space-bounded operators (`" == "` instead of `"=="`)
- **Streaming channel leaks** — synthesis block (max-iterations) and no-tools streaming path could leave `ch` open on error, leaking goroutines. Now uses `sync.Once`-guarded close and defensive recover guard respectively
- **Network streaming deadlock** — sub-agent event forwarding goroutine blocked on full parent channel. Now context-aware with `select` on `ctx.Done()` and drain on cancellation
- **Duplicate `EventInputReceived` from sub-agents** — forwarding goroutine now filters `EventInputReceived` (Network's `EventAgentStart` is the canonical signal)
- **Workflow expression evaluator false matches** — operators found inside resolved values caused incorrect splits. Now splits on the raw expression before resolving placeholders
- **`FromDefinition` branch target overwrite** — multiple condition nodes routing to the same target silently dropped earlier conditions. Now composes with OR
- **Shallow snapshot in `checkSuspendLoop`** — `ToolCalls`/`Attachments` slices shared backing arrays. Now deep-copies
- **`buildToolNode` slice aliasing** — `append(stepOpts, ...)` could alias the backing array. Now uses explicit `make`+`copy`
- **`execute_code` recursion bypass** — code could call `execute_plan`/`execute_code` via `call_tool`. Dispatch passed to code runner now blocks both
- **`dispatchParallel` ignored context cancellation** — goroutines now check `ctx.Err()` before dispatching
- **ForEach ignored cancelled context** — iterations now check `iterCtx.Err()` before executing the step function
- **Conversation history ordering corruption** — user/assistant messages could share timestamps. Assistant now gets `created_at = now + 1`; added UUIDv7 `id` as secondary sort key
- **`WithConversationMemory` never created thread rows** — `persistMessages` only called `StoreMessage` but never `CreateThread`, leaving the `threads` table empty. Added `ensureThread` to create the thread row on first message and bump `updated_at` on subsequent turns, so `ListThreads`/`GetThread` work correctly for memory-managed threads
- **Network streaming arrived as single chunk** — when a subagent implemented `StreamingAgent`, Network still called `Execute()` (blocking), collecting the entire response before emitting it as one `text-delta`. Network now detects `StreamingAgent` via type assertion and calls `ExecuteStream`, forwarding token-by-token events through the parent channel in real time
- **Network streaming duplicated sub-agent output** — when a Network delegated to a streaming sub-agent, the sub-agent's text-delta events were forwarded correctly, but the router's final response (echo, paraphrase, or empty) emitted a second text-delta, causing consumers to see the response doubled. The router's final text-delta is now suppressed entirely when a sub-agent already streamed; `AgentResult.Output` still carries the router's final text for programmatic use

## [0.6.0] - 2026-02-21

### Added

- **Built-in guardrails** — four guardrail types in the root package, composable via `WithProcessors()`:
  - `InjectionGuard` — multi-layer prompt injection detection (known phrases, role override, delimiter injection, encoding/obfuscation, custom regex). Options: `InjectionPatterns`, `InjectionRegex`, `InjectionResponse`, `SkipLayers`
  - `ContentGuard` — input/output character length enforcement (rune-based). Options: `MaxInputLength`, `MaxOutputLength`, `ContentResponse`
  - `KeywordGuard` — keyword and regex content blocking (case-insensitive substring). Builder methods: `WithRegex`, `WithResponse`
  - `MaxToolCallsGuard` — tool call limiting per LLM response (trims excess, doesn't halt)
- **Deep observability** — tracing and structured logging built into the core framework, replacing external wrapper-based instrumentation
  - `Tracer` and `Span` interfaces in root package (zero OTEL imports) — thin abstraction over any tracing backend
  - `observer.NewTracer()` — OTEL-backed implementation using the global `TracerProvider`
  - `WithTracer(Tracer)` / `WithLogger(*slog.Logger)` — agent-level options for `LLMAgent` and `Network`
  - `WithWorkflowTracer(Tracer)` / `WithWorkflowLogger(*slog.Logger)` — workflow-level options
  - `WithRetrieverTracer(Tracer)` / `WithRetrieverLogger(*slog.Logger)` — HybridRetriever options
  - `WithGraphRetrieverTracer(Tracer)` / `WithGraphRetrieverLogger(*slog.Logger)` — GraphRetriever options
  - `WithIngestorTracer(Tracer)` / `WithIngestorLogger(*slog.Logger)` — Ingestor options (in `ingest` package)
  - Span hierarchy: `agent.execute` → `agent.memory.load` / `agent.loop.iteration` → `agent.memory.persist`; `workflow.execute` → `workflow.step`; `retriever.retrieve`; `ingest.document`
  - All `log.Printf` calls in core framework replaced with structured `slog` (agent, memory, network, workflow)
  - Helper types: `SpanAttr`, `StringAttr()`, `IntAttr()`, `BoolAttr()`, `Float64Attr()`
  - When no tracer is configured, all span creation is skipped (nil check) — zero overhead
- **Skill tool** (`tools/skill`) — agent-facing skill management through the standard `Tool` interface, enabling the self-improvement loop. Three actions: `skill_search` (embed query → semantic search → ranked results), `skill_create` (auto-embed description, auto-set `CreatedBy` from `TaskFromContext`), `skill_update` (partial update with read-modify-write, re-embeds only when description changes). Agents in a shared `Store` automatically discover each other's skills. Governance boundary: agents get search/create/update, humans retain delete via `Store.DeleteSkill()`. New `Skill` fields: `Tags []string` (categorization labels), `CreatedBy string` (origin tracking), `References []string` (skill composability). All three store backends (SQLite, PostgreSQL, libSQL) updated
- **Dynamic agent configuration** — per-request resolution of system prompt, LLM provider, and tool set via `WithDynamicPrompt(PromptFunc)`, `WithDynamicModel(ModelFunc)`, and `WithDynamicTools(ToolsFunc)`. Each accepts a function called at the start of every `Execute`/`ExecuteStream` call, receiving `context.Context` and `AgentTask`. Dynamic values override their static counterparts (`WithPrompt`, constructor provider, `WithTools`). Works with both `LLMAgent` and `Network`. Use cases: multi-tenant prompt personalization, tier-based model selection, role-based tool gating
- **Task context propagation** — `WithTaskContext(ctx, task)` / `TaskFromContext(ctx)` inject and retrieve `AgentTask` via `context.Context`. Called automatically by `LLMAgent` and `Network` at Execute entry points. Tools can now access task metadata (user ID, thread ID, custom context) without changes to the `Tool` interface
- **Graph RAG** — knowledge graph layer for retrieval pipelines. LLM-based graph extraction during ingestion discovers 8 relationship types (`references`, `elaborates`, `depends_on`, `contradicts`, `part_of`, `similar_to`, `sequence`, `caused_by`) between chunks and stores them as weighted edges. `GraphRetriever` combines vector search with multi-hop BFS traversal to discover contextually related content that vector similarity alone would miss. Score blending: `vectorWeight × vectorScore + graphWeight × hopDecay × edgeWeight`. New types: `RelationType`, `ChunkEdge`. New interfaces: `GraphStore` (optional Store capability via type assertion). New options: `WithGraphExtraction(provider)`, `WithMinEdgeWeight`, `WithMaxEdgesPerChunk`, `WithGraphBatchSize`, `WithCrossDocumentEdges`, `WithSequenceEdges` (ingestion); `WithMaxHops`, `WithGraphWeight`, `WithVectorWeight`, `WithHopDecay`, `WithBidirectional`, `WithRelationFilter`, `WithMinTraversalScore`, `WithSeedTopK`, `WithGraphFilters` (retrieval). `GraphStore` implemented in all three store backends (SQLite, PostgreSQL, libSQL) with cascade edge deletion on document removal and orphan edge pruning
- **`SchemaObject` typed builder** — type-safe alternative to raw JSON for `ResponseSchema`. `NewResponseSchema(name, schema)` marshals a `SchemaObject` into `ResponseSchema.Schema`. Covers common JSON Schema keywords: `type`, `description`, `properties`, `items`, `enum`, `required`. Raw `json.RawMessage` remains available for advanced schemas
- **Generic OpenAI-compatible provider** (`provider/openaicompat`) — `NewProvider(apiKey, model, baseURL)` implements `oasis.Provider` for any service that speaks the OpenAI chat completions API. Works with OpenAI, Groq, Together, Fireworks, DeepSeek, Mistral, Ollama, vLLM, LM Studio, OpenRouter, Azure OpenAI, and more. Provider-level options: `WithName` (for logs/observability), `WithHTTPClient`, `WithOptions` (temperature, max tokens, etc. applied to every request). Uses the existing shared helpers (`BuildBody`, `StreamSSE`, `ParseResponse`) — no new dependencies
- **Data transform tool** (`tools/data`) — structured CSV/JSON/JSONL processing without Python subprocess overhead. Four composable functions that chain parse → filter → aggregate → transform. `data_parse` accepts raw CSV/JSON/JSONL text and returns structured records (auto-detects format). `data_filter` filters by conditions (8 operators: `==`, `!=`, `>`, `<`, `>=`, `<=`, `contains`, `in`) with automatic numeric coercion. `data_aggregate` groups records and computes metrics (`sum`, `count`, `avg`, `min`, `max`) with optional `group_by`. `data_transform` selects columns, renames, sorts (numeric-aware), and limits output. All functions exchange data as JSON arrays of objects — the universal interchange format. Pure Go stdlib, no external dependencies. Constructor: `data.New()`. Output capped at 32KB with automatic truncation
- **Attachment redesign** — multi-format support for large media. `Attachment` now supports `URL string` (remote references for large files, pre-uploaded to storage/CDN) and `Data []byte` (raw bytes, encoded at provider boundary — 33% smaller than base64 string). `Base64` field deprecated but still functional for backward compatibility. Helper methods: `InlineData()` (resolves bytes from `Data` or `Base64` with priority), `HasInlineData()`. Provider resolution order: URL > Data > Base64. Gemini provider uses `fileData` for URL-based attachments and encodes `Data` to base64 at serialization boundary. OpenAI-compatible provider routes by MIME type: `image/*` → `image_url` block, everything else (video, audio, PDF) → `file` block. New type in `openaicompat`: `FileData`
- **Code execution** (`WithCodeExecution`, `code` package) — LLM writes and executes Python code in a sandboxed subprocess with full tool bridge access. Complements `WithPlanExecution` (parallel fan-out) with complex logic capabilities: conditionals, loops, data flow between tool calls, error handling via try/except. Architecture: `CodeRunner` interface in root package with `SubprocessRunner` implementation in `code/` package. Embedded Python prelude injects `call_tool(name, args)`, `call_tools_parallel(calls)`, and `set_result(data)` functions. Communication uses a JSON-over-stdin/stdout protocol — `print()` output goes to stderr (captured as logs), structured results go through `set_result()`. Safety: workspace isolation (filesystem ops restricted to configured directory), `os.system`/`subprocess` blocked at Python level, pre-execution regex blocklist, configurable timeout with SIGKILL, recursion prevention (`execute_code` cannot call `execute_code`). New types: `CodeRunner`, `CodeRequest`, `CodeResult`, `DispatchFunc` (exported from `dispatchFunc`). New option: `WithCodeExecution(runner CodeRunner)`. `SubprocessRunner` options: `WithTimeout`, `WithMaxOutput`, `WithWorkspace`, `WithEnv`, `WithEnvPassthrough`. Works with both `LLMAgent` and `Network`. Provider-agnostic — any LLM can use the `execute_code` tool
- **File tool** (`tools/file`) — three new operations: `file_list` (browse directories with type prefix), `file_delete` (remove files or empty directories), `file_stat` (get name, size, type, modification time as JSON). All operations sandboxed within the configured workspace path. Empty path defaults to workspace root for `file_list`

### Fixed

- **SQL injection via metadata filter key** — `buildChunkFilters` (SQLite) and `buildChunkFiltersPg` (PostgreSQL) interpolated user-supplied `meta.*` keys directly into SQL/JSON path expressions. Added `safeMetaKey()` validator that rejects keys containing anything other than `[a-zA-Z0-9_]`
- **`created_at` filter arg/clause mismatch** — `buildChunkFilters` (SQLite) and `buildChunkFiltersPg` (PostgreSQL) always appended args for `created_at` filters even when the `Op` was neither `OpGt` nor `OpLt`, producing arg count mismatches. Args now append inside each branch
- **`source` filter accepted non-equality operators** — `buildChunkFilters` and `buildChunkFiltersPg` generated `source = ?` regardless of `FilterOp`; now skips filters where `Op != OpEq`
- **O(n^2) fact sorting in SQLite MemoryStore** — `SearchFacts` used selection sort; replaced with `sort.Slice` (O(n log n))
- **libSQL connection-per-call overhead** — `store/libsql.Store` opened and closed a `*sql.DB` on every method call (35+ open/close cycles per request). Refactored to a single persistent `*sql.DB` with `SetMaxOpenConns(1)`, matching the `store/sqlite` pattern
- **Missing indexes on frequently queried columns** — added `idx_messages_thread`, `idx_threads_chat`, and `idx_chunks_document` indexes to SQLite and libSQL stores
- **`WithEFSearch` doc inaccuracy** — doc comment said "SET LOCAL" but the code uses `SET` (session-level); updated comment and added explanatory note about connection pool behavior
- **Non-deterministic tool ordering in Network** — `buildToolDefs` iterated a map, causing randomized tool order sent to the LLM router on each call. Agent names are now pre-sorted at construction time for deterministic routing behavior
- **PostProcessor skipped for no-tools streaming path** — `RunPostLLM` was never called when using `ExecuteStream` on a tool-less agent or on the max-iteration synthesis path. Processors now run for side effects (logging, validation) after streaming completes
- **State/Done ordering in AgentHandle** — `State()` could return a terminal value before `Done()` was closed, causing `Result()` to return zero values. `State()` now waits on `Done()` when terminal, guaranteeing `Result()` consistency
- **`dispatchParallel` goroutine hang on context cancellation** — when more than 10 tool calls were dispatched, goroutines waiting to acquire the concurrency semaphore would block indefinitely if the context was cancelled. Semaphore acquisition is now `select`-guarded with `ctx.Done()`, allowing blocked goroutines to exit immediately on cancellation
- **`ForEach` deadlock on concurrent iteration failures** — `executeForEach` error channel was buffered to `concurrency` (max in-flight goroutines), but all `len(items)` goroutines are launched immediately; if more than `concurrency` iterations failed simultaneously, the excess goroutines would block on `errCh <- err`, preventing `wg.Wait()` from completing. Error channel buffer now sized to `len(items)` to guarantee non-blocking sends
- **Double embedding API call in memory pipeline** — when both `WithUserMemory` and `CrossThreadSearch` were enabled, the user's input was embedded twice in separate API calls (once for user fact retrieval, once for cross-thread search). Input is now embedded once at the top of `buildMessages` and the result is reused by both paths
- **`schedParseInt` empty string accepted as valid** — empty string input returned 0 instead of -1, allowing malformed schedules like `":30 daily"` to be silently accepted as `"00:30 daily"`. Now correctly rejects empty components
- **Timer leak in `Scheduler.Start`** — each polling iteration allocated a new `time.After` timer that could not be garbage collected until it fired. Replaced with a reusable `time.NewTimer` with proper `Stop()` on shutdown

### Removed

- **Breaking:** `observer.WrapAgent()` and `ObservedAgent` — agent lifecycle spans are now created by the core framework via `WithTracer`. Remove the `WrapAgent` call and pass `oasis.WithTracer(observer.NewTracer())` to the agent constructor instead
- `agent.executions` and `agent.duration` metrics from `observer.Instruments` — agent metrics are replaced by core tracing spans

### Changed

- **License changed from Apache-2.0 to AGPL-3.0** — copyleft protection for network use; commercial licensing available for proprietary embedding. Versions v0.1.0–v0.5.0 remain Apache-2.0 on the Go module proxy
- **`dispatchFunc` exported as `DispatchFunc`** — the tool dispatch function type is now exported for use by the `code` package's `CodeRunner` interface. All internal references updated
- **Bounded parallel tool dispatch** — `dispatchParallel` now caps concurrency at 10 goroutines to prevent rate limiting and resource exhaustion. Single tool calls run inline without goroutine overhead
- **`truncateStr` fast path** — skips `[]rune` allocation when the byte length already fits within the limit, avoiding unnecessary allocation for short or ASCII-only strings
- **Timeout on background persist goroutine** — `persistMessages` now applies a 30-second timeout to prevent goroutine leaks when store or embedding operations hang
- **Batched fact embedding** — `extractAndPersistFacts` now embeds all extracted facts in a single `Embed` call instead of one per fact, reducing HTTP round-trips
- **`executionState` uses `sync.RWMutex`** — `getResult` and `hasFailedUpstream` now use `RLock` instead of exclusive `Lock`, allowing concurrent reads during DAG wave evaluation

## [0.5.0] - 2026-02-19

### Added

- **Metadata filtering for vector search** — `SearchChunks` now accepts variadic `...ChunkFilter` for scoping search results by document, source, metadata, or time range. Five convenience constructors: `ByDocument(ids...)`, `BySource(source)`, `ByMeta(key, value)`, `CreatedAfter(unix)`, `CreatedBefore(unix)`. Filter types: `ChunkFilter`, `FilterOp` (`OpEq`, `OpIn`, `OpGt`, `OpLt`). Implemented in all three store backends — SQLite and Postgres use SQL-level filtering with conditional JOINs; LibSQL uses an overfetch + in-memory filter strategy (since `vector_top_k()` doesn't support WHERE). `KeywordSearcher.SearchChunksKeyword` also accepts `...ChunkFilter`. `HybridRetriever` gains `WithFilters(...ChunkFilter)` option to pass filters through to both search paths
- **SemanticChunker** (`ingest` package) — embedding-based chunker that detects topic boundaries by computing cosine similarity between consecutive sentence embeddings and splitting at percentile-based breakpoints. `NewSemanticChunker(embed, opts...)` accepts an `EmbedFunc` (matching `EmbeddingProvider.Embed` signature) plus standard `ChunkerOption`s and the new `WithBreakpointPercentile(p)` option (default 25). Falls back to `RecursiveChunker` on embedding errors
- **`ContextChunker` interface** (`ingest` package) — extends `Chunker` with `ChunkContext(ctx, text) ([]string, error)` for chunkers that need a context (e.g., for embedding API calls). The `Ingestor` auto-detects this capability via type assertion and uses `ChunkContext` when available, falling back to `Chunk` otherwise
- **`EmbedFunc` type** (`ingest` package) — `func(context.Context, []string) ([][]float32, error)`, matching `EmbeddingProvider.Embed` so you can pass `provider.Embed` directly to `NewSemanticChunker`
- **Runtime workflow definitions** (`FromDefinition`, `WorkflowDefinition`) — define workflows from JSON at runtime for visual workflow builders (Dify, n8n, Langflow). A JSON definition is parsed and converted into the same `*Workflow` that `NewWorkflow` produces — same DAG engine, same execution semantics. Four node types: `llm` (delegates to Agent), `tool` (calls Tool), `condition` (expression-based branching with `true_branch`/`false_branch`), and `template` (string interpolation). `DefinitionRegistry` maps string names to Go objects. Condition expressions support `==`, `!=`, `>`, `<`, `>=`, `<=`, `contains` with registered Go functions as escape hatch
- **Template resolution on `WorkflowContext`** — `Resolve(template) string` and `ResolveJSON(template) json.RawMessage` methods replace `{{key}}` placeholders with context values. `ResolveJSON` preserves structure for single-placeholder templates (maps, slices, numbers marshalled directly). The `"input"` key is pre-populated with `AgentTask.Input`. Available in both compile-time and runtime workflows
- **`WithResponseSchema` agent option** — enforce structured JSON output at the agent level. Sets `ChatRequest.ResponseSchema` on every LLM call, which providers translate to their native mechanism (e.g. Gemini `responseMimeType` + `responseSchema`). Also fixes Gemini `ChatStream` to forward `ResponseSchema` (previously passed `nil`)
- **Gemini response attachment parsing** — Gemini provider now extracts `inlineData` parts from responses into `ChatResponse.Attachments`, enabling image generation models (e.g. `gemini-2.5-flash-image`) to return generated images through the agent pipeline. Works in both streaming and non-streaming paths
- **Plan execution** (`WithPlanExecution()`) — built-in `execute_plan` tool that batches multiple tool calls in a single LLM turn. The LLM calls it with an array of steps (tool name + args), and the framework executes all steps in parallel without re-sampling between each call. Returns structured per-step JSON results with ok/error status. Reduces latency and token usage for fan-out patterns (e.g., querying 5 regions = 2 samplings instead of 6). Works with both `LLMAgent` and `Network`. Inspired by [Anthropic's programmatic tool calling](https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/programmatic-tool-calling), implemented provider-agnostically.
- **`ServeSSE` HTTP helper** — stream agent responses as Server-Sent Events with zero boilerplate; handles flusher validation, SSE headers, channel management, done/error events, and client disconnection via context cancellation
- **`ListDocuments` and `DeleteDocument`** on Store interface — list documents ordered by creation time, delete with cascade (chunks + FTS). Implemented in all three store backends (SQLite, libSQL, PostgreSQL)
- **PDF extension mapping** — `.pdf` files now correctly map to `TypePDF` content type; `TypePDF` promoted from subpackage to core `ingest` constants
- **Conversation model documentation** — new section in Store docs explaining the ChatID/UserID/ThreadID hierarchy, context key mapping, and common patterns (single-user, multi-user, ownership checks)
- **Execution traces on `AgentResult`** — every `AgentResult` now includes a `Steps []StepTrace` field recording per-tool and per-agent execution metadata (name, type, input, output, token usage, wall-clock duration) in chronological order. Works out of the box with no OTEL setup required. Populated by `LLMAgent` (tool calls), `Network` (tool + agent delegations), and `Workflow` (step results). `StreamEvent` also gains `Usage` and `Duration` fields on `EventToolCallResult` and `EventAgentFinish` events for streaming consumers
- **Token-based history trimming** (`MaxTokens`) — `ConversationOption` that trims conversation history by estimated token count before the LLM call. Messages are dropped oldest-first until the total fits within the budget. Composes with `MaxHistory` — both limits apply, whichever triggers first. Uses a ~4 characters/token heuristic with framing overhead. Pass to `WithConversationMemory(store, MaxTokens(4000))`
- **Proactive rate limiting** (`WithRateLimit`) — Provider decorator with sliding-window RPM and post-request TPM accounting. Blocks requests until budget allows, respecting context cancellation. `RPM(n)` limits requests per minute; `TPM(n)` limits tokens per minute (soft limit — the request that exceeds completes, subsequent ones block). Composes with `WithRetry`: `WithRateLimit(WithRetry(provider), RPM(60), TPM(100000))`. Same decorator pattern as `WithRetry` — wraps any `Provider` transparently

### Fixed

- **Binary file ingestion error** — `IngestFile` now returns a clear error when a binary format (PDF, DOCX) has no registered extractor, instead of silently producing garbage via plaintext fallback

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
- `ScoredMessage`, `ScoredChunk`, `ScoredSkill`, `ScoredFact` — scored result types that embed the original type with a `Score float32` field (cosine similarity in [0, 1]); returned by all semantic search methods so callers can make relevance decisions
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

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.6.0...HEAD
[0.6.0]: https://github.com/nevindra/oasis/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/nevindra/oasis/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/nevindra/oasis/compare/v0.3.2...v0.4.0
[0.3.2]: https://github.com/nevindra/oasis/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/nevindra/oasis/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/nevindra/oasis/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/nevindra/oasis/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nevindra/oasis/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/nevindra/oasis/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/nevindra/oasis/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/nevindra/oasis/releases/tag/v0.1.0
