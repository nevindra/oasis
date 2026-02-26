# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed

- **`store/sqlite`, `store/postgres`: implement `DocumentChunkLister`** — add `GetChunksByDocument` method so `ExtractCrossDocumentEdges` no longer fails with "store to implement DocumentChunkLister"

### Added

- **Structured logging across the framework** — comprehensive `slog` debug logging added throughout the agent loop, network, store, and ingest layers. Every operation emits timing, key parameters, and error context. Opt-in via existing logger options (`WithLogger`, `WithMemoryLogger`)

- **Contextual enrichment** — optional LLM-based enrichment step in the ingest pipeline. Each chunk is sent to an LLM alongside the full document text; the LLM returns a 1-2 sentence context prefix prepended to `chunk.Content` before embedding, improving retrieval precision by ~35%
  - `WithContextualEnrichment(provider)` — enable contextual enrichment
  - `WithContextWorkers(n)` — max concurrent LLM calls (default 3)
  - `WithContextMaxDocBytes(n)` — document truncation limit (default 100KB)
  - Bounded worker pool with graceful degradation — individual LLM failures are logged but don't block ingestion
  - Parent-child strategy: only child chunks are enriched
- **RAG strategy guide and recipes** — new Strategy Guide section in `docs/guides/rag-pipeline.md` with decision flowchart, feature reference table, cost spectrum, and 6 named recipes covering FAQ, technical docs, legal corpus, multi-format libraries, research papers, and chatbot use cases
- **`WithEmbeddingRetry`** — retry wrapper for `EmbeddingProvider`, matching the existing `WithRetry` for `Provider`. Retries transient HTTP errors (429, 503) with exponential backoff + jitter. Accepts the same `RetryOption` functions (`RetryMaxAttempts`, `RetryBaseDelay`, `RetryTimeout`)
- **Detailed ingest pipeline logging** — `WithIngestorLogger` now emits structured `slog` logs at Info, Warn, and Error levels throughout the entire pipeline: file/text ingestion, chunking strategy selection, embedding batches, contextual enrichment progress (with enriched/failed/skipped counters), and graph extraction batch results

## [0.9.0] - 2026-02-25

### Added

- **Context caching support** — both providers now support API-level context caching to reduce cost and latency for repeated large prefixes (system instructions, documents)
  - **`Usage.CachedTokens`** — new field on `oasis.Usage` reports input tokens served from provider cache. Zero when no caching is active
  - **Gemini: `WithCachedContent(name)`** — references a pre-created cache resource in requests. Cache CRUD methods on `Gemini`: `CreateCachedContent`, `GetCachedContent`, `ListCachedContents`, `UpdateCachedContent`, `DeleteCachedContent`. Convenience constructor `NewTextCachedContent(model, systemInstruction, ttl)`. Parses `cachedContentTokenCount` from usage metadata
  - **OpenAI-compat: `WithCacheControl(messageIndices ...int)`** — marks messages with `cache_control: {"type": "ephemeral"}` for providers that support the cache_control extension (Anthropic, Qwen). New `CacheControl` and `PromptTokensDetails` types. Parses `cached_tokens` from response usage
- **`AgentTask` builder methods** — `WithThreadID(id)`, `WithUserID(id)`, `WithChatID(id)` set context metadata and return the task for chaining. Replaces raw `map[string]any` construction with exported constants:
  ```go
  // before
  task := oasis.AgentTask{Input: "hi", Context: map[string]any{oasis.ContextThreadID: "t1"}}
  // after
  task := oasis.AgentTask{Input: "hi"}.WithThreadID("t1")
  ```
- **`HTTPRunner` code execution** — new `CodeRunner` implementation (`code/` package) that POSTs code to a remote sandbox service via HTTP. Replaces `SubprocessRunner` with a sidecar container pattern for proper isolation and multi-runtime support
- **Callback server for tool bridge** — shared `/_oasis/dispatch` HTTP endpoint with correlation-ID routing. Sandbox code calls `call_tool()` which POSTs back to this endpoint; the framework dispatches through the agent's tool registry and returns results. Supports concurrent tool calls from parallel code execution
- **`CodeFile` type** — bidirectional file transfer between app and sandbox. Input: `Name` + `Data` (inline bytes) or `Name` + `URL` (future). Output: `Name` + `MIME` + `Data`. `Data` tagged `json:"-"` to avoid double-encoding; wire format uses base64
- **`CodeRequest.Runtime`** — selects execution environment (`"python"` or `"node"`). Empty defaults to `"python"`
- **`CodeRequest.SessionID`** — enables workspace persistence across executions. Same session ID = same workspace directory
- **`CodeRequest.Files` / `CodeResult.Files`** — file input/output on code execution requests and results
- **`execute_code` tool `runtime` parameter** — LLM can now choose between Python and Node.js runtimes
- **File→Attachment mapping** — `executeCode` dispatch handler maps `CodeResult.Files` to `DispatchResult.Attachments`, carrying generated files (charts, CSVs) through to `AgentResult`
- **Reference sandbox service (`cmd/sandbox/`)** — Docker sidecar with `POST /execute`, `GET /health`, `DELETE /workspace/{session_id}`. Python 3.12 and Node.js 22 runtimes with embedded preludes. Session-scoped workspaces with TTL eviction. Semaphore-based concurrency limiting (503 fail-fast). Dockerfile with pre-installed scientific packages (matplotlib, pandas, numpy, seaborn, pillow, scipy)
- **Node.js runtime prelude** — `callTool()`, `callToolsParallel()`, `setResult()`, `installPackage()` for Node.js code execution. All tool functions are async. User code wrapped in async IIFE for top-level await support
- **HTTPRunner options** — `WithCallbackAddr`, `WithCallbackExternal`, `WithMaxFileSize`, `WithMaxRetries`, `WithRetryDelay`
- **Six new stream event types** — `EventToolCallDelta` (incremental tool call arguments from `ChatStream`), `EventToolProgress` (intermediate progress from `StreamingTool`), `EventStepStart`/`EventStepFinish`/`EventStepProgress` (workflow DAG step lifecycle), `EventRoutingDecision` (Network router's agent/tool selections). Total event types: 14
- **`StreamEvent.ID` field** — correlates `EventToolCallDelta`, `EventToolCallStart`, and `EventToolCallResult` events for the same tool call, enabling consumers to track individual tool calls through their lifecycle
- **`StreamingTool` interface** — optional capability for tools that support progress streaming during execution. Tools implementing `ExecuteStream(ctx, name, args, ch)` emit `EventToolProgress` events on the parent agent's stream channel. The framework falls back to `Execute` for tools that don't implement it
- **Workflow streaming (`ExecuteStream`)** — `Workflow` now implements `StreamingAgent`. Emits `EventStepStart` before each step, `EventStepFinish` after completion (with duration), and `EventStepProgress` during ForEach iterations (with completed/total counts)
- **`ErrSuspended.ResumeStream`** — streaming variant of `Resume` for suspended agents and workflows. Emits `StreamEvent` values into a channel throughout the resumed execution. The channel is closed when streaming completes. Returns an error if the suspension was created without streaming support
- **Network routing decision events** — when the router LLM returns `agent_*` tool calls, an `EventRoutingDecision` event is emitted with a JSON summary of the selected agents and direct tools
- **Agent-level generation parameters** — `WithTemperature`, `WithTopP`, `WithTopK`, `WithMaxTokens` agent options set LLM sampling parameters declaratively per agent. Stored as `GenerationParams` (pointer fields — nil means "use provider default") on the agent and injected into every `ChatRequest` via `loopConfig`. Providers read `req.GenerationParams` and map to their native API fields. Agents sharing one provider can now have different temperatures without creating separate provider instances
- **`GenerationParams` type** — new protocol type (`types.go`) with `*float64` Temperature/TopP and `*int` TopK/MaxTokens. Added as optional field on `ChatRequest`. Zero-value backward compatible — nil `GenerationParams` means no override
- **Thinking/reasoning visibility** — `ChatResponse.Thinking` carries the LLM's chain-of-thought content (e.g., Gemini `thought` parts). `AgentResult.Thinking` exposes the last reasoning from the tool-calling loop. `EventThinking` stream event fires after each LLM call when thinking is present. PostProcessors see the full `ChatResponse` including `Thinking` — can inspect reasoning for guardrails, logging, or redaction
- **Semantic context trimming (`WithSemanticTrimming`)** — opt-in relevance-based history trimming inside `WithConversationMemory`. When the conversation exceeds `MaxTokens`, older messages are scored by cosine similarity to the current query instead of being dropped oldest-first. Lowest-scoring messages are dropped first. The most recent N messages (default 3, configurable via `KeepRecent`) are always preserved. Falls back to oldest-first trimming if embedding fails. Reuses the query embedding from `CrossThreadSearch` when both are enabled — no extra API call
- **Provider `WithLogger(*slog.Logger)`** — both `provider/gemini` and `provider/openaicompat` gain a `WithLogger` option for structured logging. Used to emit warnings when `GenerationParams` contains fields unsupported by the provider (e.g., TopK on OpenAI). No logger = no warning
- **Edge descriptions** — `ChunkEdge` now carries a `Description` field with a human-readable explanation of why the relationship exists, as generated by the LLM during graph extraction. Stored in a new `description` column across all backends (SQLite, Postgres, libSQL) with an idempotent `ALTER TABLE` migration
- **`EdgeContext` type + `RetrievalResult.GraphContext`** — graph-discovered chunks now include `[]EdgeContext` explaining which edges led to their discovery (source chunk, relation type, description). Seed chunks have an empty `GraphContext`
- **Sliding window batches (`WithGraphBatchOverlap`)** — graph extraction batches can now overlap, allowing consecutive batches to share chunks and discover cross-boundary relationships. Duplicate edges from overlapping batches are deduplicated by keeping the highest-weight edge per (source, target, relation) key
- **Parallel graph extraction (`WithGraphExtractionWorkers`)** — LLM-based graph extraction now uses a bounded worker pool (default 3 concurrent calls). Respects context cancellation
- **Hybrid seeding for `GraphRetriever` (`WithSeedKeywordWeight`)** — when > 0 and the Store implements `KeywordSearcher`, seed selection combines vector and keyword search via Reciprocal Rank Fusion before graph traversal
- **Cross-document edge extraction (`ExtractCrossDocumentEdges`)** — discovers relationships between chunks from different documents via vector similarity search + LLM extraction. Configurable via `CrossDocOption` functions (`CrossDocWithSimilarityThreshold`, `CrossDocWithMaxPairsPerChunk`, `CrossDocWithBatchSize`, `CrossDocWithDocumentIDs`). Requires `WithGraphExtraction` and a Store implementing both `GraphStore` and `DocumentChunkLister`
- **`DocumentChunkLister` interface** — optional Store capability (`GetChunksByDocument`) for listing chunks belonging to a specific document, discovered via type assertion
- **`ByExcludeDocument` filter + `OpNeq`** — new `ChunkFilter` constructor that excludes chunks from a given document ID. Supported across all three backends
- **`KnowledgeTool` formats `GraphContext`** — knowledge search output now includes edge descriptions for graph-discovered chunks (`↳ Related: "description" (relation)`)
- **Comprehensive agent test coverage** — 43 new tests (322 → 365 total) covering previously untested critical paths: `WithSuspendTTL` auto-release/override/budget decrement, suspend snapshot isolation, `Resume`/`Release` edge cases, `estimateSnapshotSize`, subagent panic recovery (sync + streaming), `execute_code` tool dispatch (success, empty code, invalid args, runner error, runtime error, recursion prevention), plan execution edge cases (max steps cap, `ask_user` blocked), `dispatchParallel` (context cancellation, single-call fast path, tool panic recovery), tool result truncation, `truncateStr`, `buildStepTrace`, `ServeSSE` panic recovery, `WriteSSEEvent` (no flusher, marshal error), stream context cancellation, `Spawn` panic recovery, `Drain()` completion, and all option builders
- **`agentCore` unit tests** — 20 new tests (`agentcore_test.go`) for extracted helpers: `initCore` field wiring (all fields, defaults, memory), shared methods (`cacheBuiltinToolDefs`, `resolvePromptAndProvider`, `resolveDynamicTools`), `executeAgent` (non-streaming, streaming delegation, panic recovery for both paths), `forwardSubagentStream` (EventInputReceived filtering, context cancellation), `onceClose` (idempotent + 100-goroutine concurrent safety), `startDrainTimeout`, `statusStr`, `safeAgentError`, embedded method promotion
- **`WithMaxAttachmentBytes(n int64)`** — configurable byte budget for accumulated attachments per execution (default 50 MB). Attachments that would exceed the budget are silently dropped. Works alongside the existing per-count cap of 50 attachments
- **`WithSuspendBudget(maxSnapshots int, maxBytes int64)`** — per-agent limits on concurrent suspended states (default 20 snapshots, 256 MB). When the budget is exceeded, `checkSuspendLoop` rejects the suspension with an error instead of allocating unbounded memory. Counters are decremented when `Resume()`, `Release()`, or the TTL timer fires
- **`WithCompressModel(fn ModelFunc)` / `WithCompressThreshold(n int)`** — LLM-driven context compression for the `runLoop`. When the message rune count exceeds the threshold (default 200K runes), old tool results are summarized via an LLM call and replaced with a compact summary message. A dedicated provider can be configured via `WithCompressModel`; the main provider is used as fallback. The last 2 iterations are always preserved intact. Successive compressions fold prior summaries into new ones. Degrades gracefully on error
- **`ErrSuspended.WithSuspendTTL`** — opt-in automatic expiry for suspended agent states. Starts a timer that auto-releases the resume closure (and its captured message snapshot) when the TTL elapses without `Resume()` being called. Prevents memory leaks in server environments where callers may not call `Release()` on abandoned suspensions
- **Tool result truncation in `runLoop`** — tool results exceeding 100,000 runes (~25K tokens) are truncated in the message history with an `[output truncated]` marker. Prevents unbounded memory growth from tools returning very large outputs (e.g. web scraping, file reads). Stream events and step traces retain the full content since they are transient

### Removed

- **`SubprocessRunner`** — replaced by `HTTPRunner` + sandbox sidecar. The `CodeRunner` interface is unchanged for pluggability
- **`WithWorkspace`, `WithEnv`, `WithEnvPassthrough` options** — subprocess-specific options removed with `SubprocessRunner`
- **`Intent` type and constants** — `Intent`, `IntentChat`, `IntentAction` were dead code with zero consumers. Deleted
- **`ContextThreadID`, `ContextUserID`, `ContextChatID` exported constants** — replaced by `WithThreadID()`, `WithUserID()`, `WithChatID()` builder methods on `AgentTask` (see Added). The string keys are now internal

### Changed

- **Zero-allocation rune counting in `runLoop`** — replaced `len([]rune(m.Content))` with `utf8.RuneCountInString()` across all hot-path rune counting sites (`runLoop` message tracking, tool result truncation check, `runeCount` helper). Eliminates a `[]rune` heap allocation per message per iteration
- **No JSON marshaling in routing-decision event** — replaced `json.Marshal(map[string][]string{...})` in the `EventRoutingDecision` emit path with `buildRoutingSummary()`, a `strings.Builder`-based serializer. Removes map allocation and reflection on every iteration with agent tool calls
- **No `fmt.Sprintf` in `executePlan` loop** — replaced `fmt.Sprintf("plan_step_%d", i)` with `"plan_step_" + strconv.Itoa(i)` for cheaper string construction in the plan step ID builder
- **Hot-path benchmarks** — added `loop_bench_test.go` with benchmarks for `runeCount` (ASCII/multibyte), `truncateStr` (short/long/multibyte), `buildRoutingSummary`, and `dispatchParallel` (single/multi). All rune counting benchmarks confirm 0 allocs/op
- **Provider interface consolidation** — `ChatWithTools` removed; tools now passed via `ChatRequest.Tools`. `ChatStream` signature changed from `ch chan<- string` to `ch chan<- StreamEvent`, emitting typed events (text deltas, tool call deltas) instead of raw strings. Provider interface reduced from 4 methods to 3 (`Chat`, `ChatStream`, `Name`). All provider implementations and middleware (`WithRetry`, `WithRateLimit`, observer wrappers) updated accordingly
- **Agent file split** — split monolithic `agent.go` (~1,700 lines) into five files by concern: `agent.go` (API surface: interfaces, types, options, constructors), `loop.go` (execution engine: `runLoop`, `dispatchParallel`, `DispatchResult`, `DispatchFunc`), `suspend.go` (suspend/resume: `ErrSuspended`, `checkSuspendLoop`, `ResumeData`), `batch.go` (batch primitives: `BatchProvider`, `BatchState`, `BatchJob`), `stream.go` (streaming: `StreamEvent`, `ServeSSE`, `WriteSSEEvent`). Split `agent_test.go` (~2,700 lines) correspondingly into `agent_test.go`, `loop_test.go`, `suspend_test.go`, `stream_test.go`. No API changes — purely internal reorganization
- **Shared dispatch helpers** — extracted `dispatchBuiltins` and `dispatchTool` from duplicated code in `LLMAgent.makeDispatch` and `Network.makeDispatch`. The `ask_user`, `execute_plan`, `execute_code`, and tool-result conversion logic now lives in a single place
- **`ErrSuspended` default TTL (30 minutes)** — `checkSuspendLoop` now applies a default 30-minute TTL to all `ErrSuspended` values. Previously, abandoned suspensions leaked the entire agent graph (resume closure captures provider, processors, memory, tool registry) indefinitely. Callers can still override with `WithSuspendTTL`
- **Persist backpressure: graceful degradation instead of silent drop** — when the persist semaphore is full, `persistMessages` now falls back to a lightweight persist (DB write only, no embedding, no fact extraction, no title generation) instead of dropping the message entirely. Conversation history is preserved; only cross-thread search quality degrades. If the store is truly unresponsive (no slot within 2 seconds), the persist is dropped with an Error-level log
- **Unified `safeCloseCh` in `runLoop`** — replaced 6 raw `close(ch)` calls and 2 local `sync.Once`-based `safeCloseCh` functions with a single `safeCloseCh` created at the top of `runLoop`. All exit paths now use the same double-close-safe mechanism, eliminating a fragile implicit invariant about which code paths had passed `ch` to a provider
- **Workflow file split** — split monolithic `workflow.go` (1,930 lines) into four files by concern: `workflow.go` (types, options, constructor, graph validation), `workflow_exec.go` (DAG runner, step lifecycle, result building), `workflow_steps.go` (ForEach/agent/tool wrappers, loop executors), `workflow_definition.go` (FromDefinition, node builders, expression evaluator). Applied `maps.Copy`, `strings.Cut`, and `strconv.FormatBool` optimizations during the split
- **Embedded `agentCore` struct** — extracted ~24 shared fields from `LLMAgent` and `Network` into a single embedded `agentCore` struct (`agentcore.go`). Both types now embed `agentCore`, eliminating structural duplication and drift bugs when new agent-level options are added. Shared constructor `initCore()` uses field-by-field assignment to avoid copying sync primitives in `agentMemory`. Shared methods: `Name()`, `Description()`, `Drain()`, `cacheBuiltinToolDefs()`, `resolvePromptAndProvider()`, `resolveDynamicTools()`, `baseLoopConfig()`, `executeWithSpan()`. No public API changes
- **Simplified Network subagent dispatch** — extracted 160-line `Network.makeDispatch` into focused helpers: `dispatchAgent()` (agent routing + event emission), `executeAgent()` (panic recovery + streaming delegation), `forwardSubagentStream()` (channel forwarding + drain timeout), `startDrainTimeout()`, and generic `onceClose[T]()`. Three near-identical panic-recovery blocks collapsed into two reusable functions

### Fixed

- **Gemini batch JSON tag mismatches** — `batchStatsJSON.SucceededRequestCount` used incorrect JSON tag `"successfulRequestCount"` (should be `"succeededRequestCount"`), causing batch stats to always report 0 succeeded. `batchMetadata.Output` used `"output"` instead of `"dest"`, causing batch results to always fail with "no results". Removed unnecessary `batchInlinedResponseList` wrapper type
- **memory**: harden fact extraction against prompt injection — extraction prompt guardrail, injection pattern filter in `sanitizeFacts`, trust framing in `BuildContext`
- **memory**: harden cross-thread recall against injection — trust framing header, content truncation (500 runes), ChatID-scoped filtering
- **Network subagent drain timeout goroutine leak** — when a streaming subagent ignored context cancellation and the 60-second drain timeout elapsed, `subCh` remained open. The subagent's `ExecuteStream` goroutine blocked indefinitely on sends to the unread channel, leaking permanently. The drain goroutine now calls `safeCloseSubCh()` after timeout, causing the subagent's next send to panic and get caught by the existing `recover` wrapper — converting the leak into a clean error
- **Network subagent panic recovery double-close on `subCh`** — if `ExecuteStream` (via `runLoop`) closed the streaming channel and then panicked in a deferred function, the panic recovery handler called `close(subCh)` on the already-closed channel, causing an unrecoverable double-close panic. Wrapped with `sync.Once` + `recover()` guard, matching the existing pattern in `runLoop`'s synthesis block
- **`ErrSuspended` resume closure data race with TTL timer** — `WithSuspendTTL`'s timer callback wrote `e.resume = nil` from a timer goroutine while `Resume()` read `e.resume` from the caller's goroutine without synchronization. Added `sync.Mutex` to guard all `resume` field access; `Resume()` extracts the closure under lock and calls it outside the lock to avoid holding the mutex during `runLoop` re-entry
- **`dispatchParallel` tool panic crashes entire process** — worker goroutines in the parallel tool dispatch pool had no `recover()` guard. A panicking `Tool.Execute` implementation (user-supplied or built-in) terminated the entire process instead of returning an error result. Added `safeDispatch` wrapper with panic recovery, applied to both the single-call fast path and the worker pool path. Matches the recovery pattern already used for subagent dispatch in `Network.makeDispatch`
- **`checkSuspendLoop` deep-copies immutable attachment data** — the suspend snapshot deep-copied every `Attachment.Data` byte slice, temporarily doubling memory usage for conversations with large binary attachments (images, PDFs, audio). `Attachment.Data` is immutable throughout the framework, so the snapshot now shares the backing bytes while still isolating the slice header
- **`sanitizeFacts` allows unbounded fact count per extraction** — a manipulated or hallucinating LLM could return hundreds of facts in a single extraction response, causing expensive embedding API calls and memory store pollution. Now capped at `maxFactsPerTurn` (10) via early break in `sanitizeFacts`
- **`runLoop` tool-call-result blocks agent loop on slow consumers** — the `EventToolCallResult` channel send used a bare `ch <-` without a `select` on `ctx.Done()`, blocking the entire agent loop indefinitely if the streaming consumer disconnected or stopped reading. Now uses `select` with `ctx.Done()`, matching the pattern already used for `EventToolCallStart`, `EventTextDelta`, and `EventProcessingStart`
- **Title generation sends full message to LLM** — `generateTitleNewThread` passed the full user text (up to 50,000 runes) to the LLM for generating an 8-word title, wasting tokens and adding latency on long first messages. Now truncates to 500 runes before the Chat call via `maxTitleInputLen`
- **AutoTitle wiped on subsequent messages** — `ensureThread` called `UpdateThread` with only `ID` and `UpdatedAt` set, leaving `Title` as the zero value (empty string). All store implementations (`sqlite`, `postgres`, `libsql`) unconditionally SET `title=?`, overwriting the title generated by `AutoTitle` on the first message. Now preserves the existing thread fields by passing the full thread from `GetThread` back to `UpdateThread` with only `UpdatedAt` changed
- **`ErrSuspended` retains message snapshot indefinitely** — the resume closure captured a deep copy of the full conversation history (tool arguments, results, attachments). Callers with no timeout or cleanup path (e.g. user abandonment) retained this memory indefinitely. Added `Release()` to eagerly nil the closure, and `Resume()` now also nils it after use (single-use enforcement)
- **`ObservedProvider.ChatStream` deadlock on small/unbuffered channels** — the forwarding goroutine wrote to the caller's `ch` while `ChatStream` synchronously waited on `<-done`, deadlocking when `ch` had insufficient buffer. Now buffers the internal `wrappedCh` to `max(cap(ch), 64)` so the inner provider never blocks on send
- **`ObservedProvider.ChatStream` goroutine ignores context cancellation** — the forwarding goroutine used a bare `ch <- ev` that blocked indefinitely if the consumer stopped reading or the context was cancelled. Now uses `select` with `ctx.Done()` to exit cleanly on cancellation
- **`dispatchParallel` `break` only exits `select`, not `for` loop** — the `break` inside the `select` case for a closed channel only broke out of the `select` statement, not the enclosing `for` loop. If the result channel closed early, subsequent iterations would silently produce zero-value tool results. Replaced with a labeled `break collect` and added post-loop error fill for any unseen results
- **`Spawn` goroutine deadlock on agent panic** — if `agent.Execute` panicked, the goroutine crashed without closing `h.done`, causing all callers of `Await()`, `Done()`, and `State()` to block forever. Added `recover()` that sets `StateFailed` and closes the done channel
- **`runLoop` synthesis double-close panic** — the max-iterations synthesis block's `safeCloseCh` lacked the `recover()` guard present in the no-tools streaming path. If `ChatStream` closed the channel during synthesis, the subsequent `safeCloseCh()` call panicked. Added `recover()` to match the no-tools path
- **`runLoop` bare channel sends block agent loop** — `EventProcessingStart`, `EventTextDelta`, and `EventToolCallStart` sends used bare `ch <-` without context guards, blocking the entire agent loop if the consumer stopped reading. Wrapped all three in `select` with `ctx.Done()`
- **Network subagent panic deadlocks parent** — a panicking subagent's `Execute` or `ExecuteStream` crashed the `dispatchParallel` worker goroutine, preventing `wg.Done()` from firing and deadlocking the result collection loop. Wrapped all subagent calls with `recover()`. For streaming subagents, the recovery also closes `subCh` to unblock the event-forwarding goroutine
- **Inconsistent `agent_` prefix detection** — `runLoop` and `buildStepTrace` used manual `len() > 6 && [:6]` checks while `Network` used `strings.HasPrefix`. Unified on `strings.HasPrefix`/`strings.CutPrefix` for consistency and safety
- **`agentMemory.initSem` data race** — `initSem` used a bare `nil` check to lazily allocate the persist semaphore. Under concurrent `Execute` calls on the same agent, two goroutines could both observe `nil` and allocate separate channels, orphaning one semaphore and breaking the backpressure invariant. Replaced with `sync.Once`
- **`LLMAgent` blocking channel send** — `executeWithSpan` sent the initial `EventInputReceived` event with a bare `ch <-` that could block indefinitely if the consumer stopped reading or the context was cancelled. Now uses `select` with `ctx.Done()`, matching Network's existing pattern
- **`runLoop` double-close panic in streaming no-tools path** — when `ChatStream` succeeded (closing `ch`) and `RunPostLLM` subsequently returned an error, the error handler called `close(ch)` again, panicking with "close of closed channel". Replaced per-path `close(ch)` calls with a unified `sync.Once` + `recover` guard across all exit paths
- **Serial context loading in `buildMessages`** — `Embed` (embedding API call) and `GetMessages` (database query) ran sequentially, adding unnecessary latency when both conversation memory and cross-thread search are enabled. Now runs them concurrently via `sync.WaitGroup` when both are needed; sequential path preserved when only one is active
- **Tool definitions rebuilt on every `Execute` call** — `LLMAgent.buildLoopConfig` and `Network.buildLoopConfig` reconstructed the full tool-definition slice (including agent tools, direct tools, and built-in tools) on every call, even though the set is fixed at construction time for the non-dynamic path. Now pre-computes `cachedToolDefs` in the constructor; dynamic-tools path still rebuilds per-request
- **Unbounded attachment accumulation in `runLoop`** — tool results with attachments were appended to `accumulatedAttachments` without any cap, allowing memory to grow unboundedly in long-running loops with attachment-heavy tools. Added `maxAccumulatedAttachments` (50) guard; once the cap is reached, further attachments are silently dropped
- **Network subagent stream drain goroutine leaks on cancellation** — when a streaming subagent's context was cancelled, the drain goroutine used a bare `for range subCh` that blocked indefinitely if the subagent never closed the channel. Added a 60-second timeout via `time.NewTimer`; logs a warning if the drain times out
- **Shallow copy of `ToolCall.Args` in suspend snapshot** — `checkSuspendLoop` copied the `[]Message` slice and the `[]ToolCall` sub-slices, but `ToolCall.Args` and `ToolCall.Metadata` (`json.RawMessage` = `[]byte`) still shared backing arrays with the live conversation. Concurrent mutation by `runLoop` could corrupt the snapshot. Now deep-copies all inner byte slices (`Args`, `Metadata`, `Attachment.Data`)

## [0.8.0] - 2026-02-24

### Added

- **`provider/resolve` package** — config-driven provider creation via `resolve.Provider(Config)` and `resolve.EmbeddingProvider(EmbeddingConfig)`. Maps provider-agnostic config (provider name, API key, model, optional Temperature/TopP/Thinking) to concrete `gemini` or `openaicompat` instances. Supports Gemini, OpenAI, Groq, DeepSeek, Together, Mistral, and Ollama with auto-filled base URLs
- **`ScanAllMessages()` injection guard option** — opt-in scanning of all user messages in conversation history, not just the last one. Detects injection placed in earlier messages via multi-turn context poisoning
- **`LLMAgent.Drain()` / `Network.Drain()`** — waits for all in-flight background persist goroutines to finish. Call during shutdown to ensure the last messages are written to the store
- **`ingest.WithMaxContentSize(n)` option** — rejects content exceeding the byte limit before extraction (default 50 MB, set to 0 to disable). Prevents memory exhaustion from oversized input

### Changed

- **Reactive DAG engine in Workflow** — replaced wave-based `runDAG` (which waited for an entire wave of steps to finish before launching the next batch) with a channel-based reactive scheduler. Each step completion immediately unblocks its dependents, eliminating latency penalties in heterogeneous DAGs where fast steps previously waited for slow siblings

### Fixed

- **Graph extraction infinite loop on `graphBatchSize <= 0`** — `extractGraphEdges` used the caller-supplied batch size directly as the loop increment. If set to 0 via `WithGraphBatchSize(0)`, the loop never advanced, burning LLM credits indefinitely. Now defaults to 5 when `batchSize <= 0`
- **Graph extraction ignores context cancellation** — `extractGraphEdges` never checked `ctx.Err()` between batches, continuing to issue LLM calls after the context was cancelled (e.g., timeout, client disconnect). Now breaks the batch loop on context cancellation
- **Graph extraction silently swallows errors** — LLM call failures and JSON parse errors in `extractGraphEdges` were silently discarded via `continue`. Now logs warnings via `*slog.Logger` when `WithIngestorLogger` is configured
- **`validRelations` map rebuilt on every `parseEdgeResponse` call** — the constant relation-type lookup map was allocated inside the function body on each invocation. Hoisted to a package-level `var` to avoid repeated allocation
- **`buildSequenceEdges` nested conditional hard to reason about** — the parent-child linking logic used a confusing three-level nested `if` to decide whether two adjacent chunks should be linked. Simplified to a single `ParentID != ParentID` check, which is semantically equivalent: flat chunks (both `""`) link, same-parent children link, everything else skips
- **DOCX zip bomb vulnerability** — `docxReadZipFile` used `io.ReadAll` with no size limit on decompressed zip entries, allowing crafted DOCX files to exhaust memory. Now uses `io.LimitReader` capped at 100 MB per entry
- **JSON extractor stack overflow on deep nesting** — `flatten` recursed without depth limit, allowing deeply nested JSON (e.g., 10,000 levels) to overflow the goroutine stack. Now capped at 100 levels, emitting `<truncated>` beyond that
- **`StripHTML` doubled memory via `[]rune` conversion** — replaced full `[]rune(content)` copy with `utf8.DecodeRuneInString` iteration, eliminating the ~4x memory overhead for large HTML documents
- **DOCX `Extract()` loaded images unnecessarily** — `Extract()` delegated to `ExtractWithMeta()` which eagerly loaded and base64-encoded all embedded images. `Extract()` now uses a text-only path that skips image loading
- **`StripHTML` dead code block** — removed unreachable `collectingTagName` check inside the `>` handler (the `>` rune was already handled by the tag-name collection branch above)
- **`StripHTML` limited entity decoding** — `decodeEntity` only handled 6 named entities and no numeric references. Now supports 25 named entities (`&mdash;`, `&copy;`, `&euro;`, etc.) and numeric entities (`&#123;`, `&#x7B;`)
- **`collapseWhitespace` redundant state** — removed redundant `lastWasEmpty` boolean; `emptyCount > 0` captures the same condition
- **Repeated `MarkdownChunker` allocation in `Ingestor`** — `selectChunker` and `chunkParentChild` created a new `MarkdownChunker` (plus its internal `RecursiveChunker`) on every markdown ingest call. Now cached on the `Ingestor` at construction time
- **Fragile type assertion in `selectChunker`** — `selectChunker` used `(*RecursiveChunker)` type assertion to detect whether a custom chunker was set, which broke if the default chunker was wrapped. Replaced with an explicit `customChunker` flag set by `WithChunker`
- **Misleading field names in chunkers** — `maxChars`/`overlapChars` fields compared against `len(text)` (byte count), not character count. Renamed to `maxBytes`/`overlapBytes` across `RecursiveChunker`, `SemanticChunker`, and `MarkdownChunker`
- **Missing compile-time interface check for `MarkdownChunker`** — added `var _ Chunker = (*MarkdownChunker)(nil)`
- **`splitSentences` naive fallback lost trailing period** — when `findSentenceBoundaries` returned no results, `strings.Split(text, ". ")` consumed the delimiter but only re-appended the period to non-last parts. The last sentence lost its trailing period when the input ended with `". "`. Now checks `strings.HasSuffix(text, ". ")` for the last part
- **`splitOnSentences` deep nesting** — extracted `appendSegment` helper and added algorithm comment to reduce nesting depth and improve readability
- **`executeForEach` goroutine leak on concurrent failures** — error channel was buffered to `concurrency`, but if more goroutines failed simultaneously than the buffer size, `errCh <- err` blocked forever, preventing `wg.Wait()` from completing. Replaced channel-based error collection with `sync.Once` to capture the first error without blocking
- **`Resolve` repeated builder allocations** — `strings.Builder` was not pre-sized, causing multiple re-allocations for templates with many placeholders. Now pre-grows to `len(template)`
- **Unused parameter in `executeResume`** — the suspended step name was passed as `_ string` but never used. Removed from the unexported method signature
- **Redundant `When` gate on tool step in `buildToolNodeStaticArgs`** — the tool step re-applied `When()` even though it only runs after the setter step (which already gates on the condition). Removed the redundant check and cleaned up the unused `when` parameter from the function signature
- **Per-call slice allocation in `readStepOutput`** — suffix lookup iterated over a `[]string` literal allocated on every step completion. Replaced with two explicit checks
- **Workflow resume data retained after execution** — `executeResume` set the `_resume_data` context key to `nil` instead of deleting it, keeping the key (and its payload reference) in the values map indefinitely. Now uses `delete()` to remove the key entirely
- **`Network` blocking channel send** — `executeWithSpan` sent the initial `EventInputReceived` event with a bare channel send that could block indefinitely if the consumer was slow or the context was cancelled. Now uses `select` with `ctx.Done()` to return early on cancellation
- **`retryProvider` context leak** — `withTimeout` discarded the `CancelFunc` from `context.WithDeadline`, leaking the derived context's resources until the deadline expired. All callers now `defer cancel()`
- **`retryProvider` timer leak on cancellation** — replaced `time.After` with `time.NewTimer` in retry backoff loops. Previously, context cancellation during backoff left the timer allocated until its full delay elapsed
- **Unbounded persist goroutines** — `persistMessages` now uses a bounded semaphore (cap 16) with backpressure. When all slots are occupied, new persist requests are dropped with a warning instead of spawning unlimited goroutines
- **Stored prompt injection via fact extraction** — extracted facts are now validated against an allowed category enum (`personal`, `preference`, `work`, `habit`, `relationship`) and truncated to 200 runes. Facts with invalid categories or empty text are dropped
- **Redundant `GetThread` call** — `ensureThread` now returns whether the thread was newly created, and title generation skips the redundant `GetThread` fetch for new threads
- **Sequential supersedes embedding calls** — superseded fact texts are now batch-embedded in a single `Embed()` call instead of one call per superseded fact
- **Byte-count token estimation for non-ASCII** — `estimateTokens` now uses `utf8.RuneCountInString` instead of `len()`, preventing systematic over-trimming of conversation history for CJK, emoji, and other multi-byte content
- **No size limit on persisted messages** — user and assistant messages are now truncated to 50,000 runes before storage, preventing unbounded DB growth
- **`DeleteMatchingFacts` injection surface** — godoc now specifies that implementations must treat the pattern as a plain substring match, never SQL LIKE or regex
- **`chunkParentChild` slice-bounds panic** — when chunk overlap caused `strings.Index` to return `-1`, `parentStart` was left at the previous `parentEnd`, making `parentEnd = parentStart + len(pt)` exceed `len(text)`. The next iteration then panicked with `slice bounds out of range`. Fixed by capping the offset with `min(parentEnd, len(text))`, matching the existing behaviour in `chunkFlat`
- **`InjectionGuard` Unicode homoglyph bypass** — added NFKC normalization before phrase matching. Fullwidth Latin (`ｉｇｎｏｒｅ`), mathematical alphanumerics, and ligatures are now normalized before detection
- **`InjectionGuard` incomplete zero-width char stripping** — added word joiner (U+2060), Mongolian vowel separator (U+180E), and soft hyphen (U+00AD) to the obfuscation character set
- **`InjectionGuard` base64 false positives** — base64 candidates whose length is not a multiple of 4 are now skipped, filtering out false matches from UUIDs, hashes, and other alphanumeric strings
- **`ProcessorChain` per-call type assertions** — processors are now pre-bucketed by interface at `Add()` time, eliminating type assertions in `RunPreLLM`/`RunPostLLM`/`RunPostTool` hot paths

## [0.7.0] - 2026-02-23

### Changed

- **`ToolRegistry.Execute` O(1) lookup** — `ToolRegistry` now maintains a `map[string]Tool` index built during `Add()`, replacing the O(n*m) linear scan in `Execute()` with a single map lookup
- **Dynamic tools skip intermediate `ToolRegistry`** — when `WithDynamicTools` is set, `LLMAgent` and `Network` build tool definitions and a lookup index directly from the returned `[]Tool` slice, avoiding a throwaway `ToolRegistry` allocation on every `Execute` call
- **`dispatchParallel` is context-aware** — replaced `sync.WaitGroup` + `wg.Wait()` with channel-based result collection that `select`s on `ctx.Done()`. If the context is cancelled while tool calls are in-flight, the function returns immediately with error results for incomplete calls instead of blocking indefinitely
- **`dispatchParallel` uses worker pool** — replaced eager goroutine-per-call spawning (which created N goroutines upfront, gated by a semaphore) with a fixed worker pool of `min(len(calls), 10)` goroutines pulling from a shared work channel. Prevents unbounded goroutine creation when `execute_plan` or providers return many tool calls

### Fixed

- **Unbounded `execute_plan` step count** — `executePlan` accepted any number of steps, allowing a misbehaving LLM to trigger mass goroutine creation and potential resource exhaustion. Now capped at 50 steps (`maxPlanSteps`) with a clear error message when exceeded
- **Double-close panic in `ServeSSE` panic recovery** — if `ExecuteStream` already closed `ch` before the panic site, the recovery handler's bare `close(ch)` would panic inside `recover()` (unrecoverable). Now uses `sync.Once`
- **Unconditional channel sends in `Network` dispatch** — `EventAgentStart`/`EventAgentFinish` used bare `ch <- ev` without a context-guarded `select`, blocking the dispatch goroutine forever if the consumer stopped reading. Now wrapped in `select` with `ctx.Done()`
- **Suspension misclassified as error in Workflow tracer** — `*ErrSuspended` (deliberate human-in-the-loop pause) was reported via `span.Error()` with status `"error"`, polluting dashboards with false errors. Now sets status `"suspended"` without marking the span as errored
- **Goroutine leak on agent panic in `ServeSSE`** — the goroutine running `ExecuteStream` now has `recover()`. If the agent panics, `ch` is closed and an error is sent to `resultCh`, preventing the `for ev := range ch` loop from blocking forever
- **Goroutine leak on ctx cancel during sub-agent streaming** — the forwarding goroutine in `Network` now spawns a background drain on context cancellation, releasing references to the parent channel and `done` signal promptly instead of blocking until `ExecuteStream` closes `subCh`
- **`time.After` timer leak in workflow retry delay** — replaced `time.After` with `time.NewTimer` + explicit `Stop()` so the timer is freed immediately when context is cancelled during the retry wait
- **`When` condition dropped from tool step in static-args path** — `buildToolNode` now propagates the `When` condition to the tool step when static (non-template) args are used, matching the template-args path behavior
- **Error detection via `"error: "` string prefix is fragile** — added `IsError bool` to `DispatchResult` and `toolExecResult` for structural error signaling. `executePlan` now uses `.isError` instead of checking string prefixes, preventing misclassification of tools that legitimately return text starting with `"error: "`
- **Parallel `ask_user` in `execute_plan`** — blocked inside plan steps, preventing concurrent `InputHandler.RequestInput` calls
- **Unbounded `errCh` in `executeForEach`** — buffer reduced from `len(items)` to `concurrency`
- **`DoUntil`/`DoWhile` silent success on max-iter** — now returns `ErrMaxIterExceeded` instead of `nil` when the loop cap is hit without the exit condition being met

### Added

- **Ingest: all extractors auto-registered** — `NewIngestor` now registers all seven extractors by default (`PlainTextExtractor`, `HTMLExtractor`, `MarkdownExtractor`, `CSVExtractor`, `JSONExtractor`, `DOCXExtractor`, `PDFExtractor`). No import or `WithExtractor` call required for standard formats
- **Ingest: extractor panic recovery** — panics from extractor calls are caught and returned as errors (`"extractor panicked: …"`), preventing a misbehaving parser from crashing the process
- **Ingest: lifecycle hooks** — two new options: `WithOnSuccess(func(IngestResult))` fires after each successful ingestion; `WithOnError(func(source string, err error))` fires on any failure

### Changed

- **Ingest: extractors merged into `ingest` package** — `CSVExtractor`, `JSONExtractor`, `DOCXExtractor`, and `PDFExtractor` are now defined in the main `ingest` package (constructors `NewCSVExtractor`, `NewJSONExtractor`, `NewDOCXExtractor`, `NewPDFExtractor`). The `ingest/csv`, `ingest/json`, `ingest/docx`, and `ingest/pdf` subpackages are removed

### Removed

- **`ingest/csv`, `ingest/json`, `ingest/docx`, `ingest/pdf` subpackages** — merged into the `ingest` package. Use `ingest.NewCSVExtractor()` etc. instead of the old subpackage constructors

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



[Unreleased]: https://github.com/nevindra/oasis/compare/v0.9.0...HEAD
[0.9.0]: https://github.com/nevindra/oasis/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/nevindra/oasis/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/nevindra/oasis/releases/tag/v0.7.0
