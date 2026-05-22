# Mastra vs Oasis — Framework Comparison

> Last updated: 2026-05-21
> Method: codedb-driven static analysis of both repositories (mastra @ TypeScript monorepo, oasis @ Go multi-module). No runtime benchmarks — all numbers are architectural unless stated.

## Language & Philosophy

|                  | Mastra                                                          | Oasis                                                                  |
|------------------|-----------------------------------------------------------------|------------------------------------------------------------------------|
| **Language**     | TypeScript / Node.js                                            | Go                                                                     |
| **Runtime**      | V8 — single-threaded event loop, async/await, JIT               | Goroutines — M:N scheduler, true OS-thread parallelism, AOT-compiled   |
| **Dependencies** | Heavy — Vercel AI SDK, AI SDK provider packages, Zod, lru-cache | Minimal — raw `net/http`, no LLM SDKs in core (`provider/openaicompat/provider.go:46`) |
| **Architecture** | Class-based, npm/pnpm monorepo + Turborepo                      | Interface-driven, root module + opt-in satellite `go.mod`s             |
| **Posture**      | Ecosystem expansion — voice, deployers, 30+ stores, Studio UI   | Kernel hardening — batch, rate-limit, compaction, sandbox, graph RAG   |
| **Footprint**    | ~50–150MB baseline; `node_modules` deployment                   | ~5–20MB baseline; single static binary                                 |
| **Cold start**   | 200ms–2s (V8 + module resolution)                               | <20ms (static binary)                                                  |

The two frameworks have **near-identical primitive vocabularies** (agents, tools, workflows, memory, RAG, MCP, networks) but make opposite tradeoffs at the runtime and packaging layer. The decision is rarely "which is more capable" — it is "which runtime matches the team and the deployment target."

---

## Agent Primitives

| Feature                  | Mastra                                                                                        | Oasis                                                                                                | Winner |
|--------------------------|-----------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------|--------|
| **Single Agent**         | `Agent` class — typed object literal `new Agent(config)` with ~30 fields (`packages/core/src/agent/agent.ts`, ~7,000 LOC; `types.ts:237`) | `LLMAgent` via `agent.NewLLMAgent(name, desc, provider, ...opts)` — 3 positional + variadic functional options (`agent/agent.go:445`) | Tie |
| **Generic type params**  | `Agent<TAgentId, TTools, TOutput, TRequestContext>` — propagates to all methods               | Go generics on `Tool[In, Out]`; agent itself is monomorphic                                          | Mastra |
| **SDK versioning**       | `generate()` / `stream()` require AI SDK v5+; `generateLegacy()` / `streamLegacy()` for v4 (`agent.ts:6920,6968`) | Single `Execute()` / `ExecuteStream()`; any `core.Provider` works regardless of SDK                | Oasis  |
| **Multi-Agent**          | `agent.network()` builds a workflow with routing → dispatch → completion-check loop (`loop/network/index.ts:2067`); also `agents` field for sub-agent-as-tool | `Network` — LLM router exposes subagents as `agent_*` tools (`network/network.go:26`)             | Mastra |
| **Sub-agent dispatch**   | `DelegationConfig` with `onDelegationStart` (modify/reject dispatch), `onDelegationComplete` (with `bail()`), `messageFilter` for message forwarding | `WithSubAgentSpawning`, `MaxSpawnDepth`, `DenySpawnTools` — ephemeral spawn inherits parent provider + tools, no memory | Tie |
| **Dynamic config**       | `DynamicArgument<T>` — most major fields (model, tools, agents, memory, instructions, scorers, processors, defaultOptions, workspace, metadata) accept `({requestContext}) => T \| Promise<T>` | `WithDynamicPrompt` / `WithDynamicModel` / `WithDynamicTools` (resolvers) + `RunOptions` struct via `ExecuteWith` for ~20 per-call overrides (model, memory, generation, processors, hooks, etc.); compile-time field safety via distinct struct type | Tie |
| **Runtime Context/DI**   | `runtimeContext` typed via `requestContextSchema` (validated at execution entry)              | `TaskFromContext(ctx)` — `core.AgentTask` propagated via `context.Context`                           | Mastra |
| **Structured Output**    | Zod schema validation with strict/warn/fallback strategies                                    | `WithResponseSchema(ResponseSchema)` — typed builder                                                 | Tie    |
| **Code Execution**       | `workspace` field — wrapper around external sandbox orchestration (E2B Workspaces)            | `core.Sandbox` 1-method interface in core; richer `sandbox.Sandbox` + `Tools()` in `sandbox/` satellite; concrete implementations external (e.g. `oasis-sandbox-ix`) | Tie |
| **Browser control**      | `browser` field — `MastraBrowser` (Playwright)                                                | N/A in core (delegated to sandbox implementation)                                                    | Mastra |
| **Plan Execution**       | N/A                                                                                           | `WithPlanExecution()` — LLM batches tool calls via `execute_plan`                                    | Oasis  |
| **Background Agents**    | `backgroundTasks` per tool + `streamUntilIdle()` auto-continuation; `OrchestrationWorker` / `BackgroundTaskWorker` offload | `oasis.Spawn` → `AgentHandle` with atomic state machine, `State()` / `Done()` / `Await()` / `Sync()` / `Cancel()` (`agent/handle.go:80`) | Tie |
| **Per-call callbacks**   | `onStepFinish`, `onFinish`, `onChunk`, `onError`, `onAbort`, `onIterationComplete` (inject feedback or stop), `prepareStep` | `PrepareStep` (per-iteration request/model/tools mutation), `OnIterationComplete` (Continue/Stop/InjectFeedback), `OnError` (Propagate/Retry/RetryWithFeedback/HaltDecision); opaque typed decision structs prevent invalid construction | Tie |
| **Sampling controls**    | Via `defaultOptions` (temperature, topP, etc.)                                                | `WithGeneration(Generation{Temperature, TopP, TopK, MaxTokens})` (`agent/agent.go:199`)              | Tie    |
| **Suspend Budget**       | N/A — suspend persists to storage instead                                                     | `WithSuspendBudget(maxSnapshots, maxBytes)` — explicit memory caps (`agent/agent.go:131`)            | Oasis  |
| **Attachment budget**    | Implicit                                                                                      | `WithMaxAttachmentBytes` — default 50MB (`agent/agent.go:122`)                                       | Oasis  |
| **Tool result paging**   | Implicit                                                                                      | `WithMaxToolResultLen` + `WithToolResultStore` for out-of-band paging of large results               | Oasis  |
| **Skills**               | N/A as first-class                                                                            | `WithActiveSkills(...)` (preload) + `WithSkills(provider)` (registers `skill_discover` / `skill_activate` tools) | Oasis |
| **Human-in-the-Loop**    | Via processor + workflow suspend                                                              | `WithInputHandler(h)` — registers built-in `ask_user` tool, LLM autonomously decides when to ask     | Oasis  |
| **Processor hooks**      | `inputProcessors`, `outputProcessors`, `errorProcessors`; can be processors OR processor workflows; `TripWire` halt-with-retry signal emits chunk on stream | `WithPreProcessors` (`PreLLM`), `WithPostProcessors` (`PostLLM`), `WithPostToolProcessors` (`PostTool`); `*ErrHalt` short-circuits to canned response | Mastra |
| **Messaging channels**   | `channels` field — `AgentChannels` (Slack, Discord) with tool approval cards, threads, media | N/A                                                                                                  | Mastra |
| **FGA / RBAC at execute** | `#requireAgentExecutionFGA()` runs before each `generate()` / `stream()` (`agent.ts:5394`)   | N/A                                                                                                  | Mastra |
| **Signal injection**     | `pubsub`, `sendSignal`, `subscribeToThread` for external message injection mid-thread         | N/A                                                                                                  | Mastra |
| **Error taxonomy**       | Structured `MastraError` with `id` / `domain` / `category`; `TripWire` for processor halt    | Typed errors: `ErrLLM`, `*ErrHalt`, `*ErrSuspended`, `WorkflowError` — `errors.Is` / `errors.As` compatible | Tie |

**Score: Mastra 8 — Oasis 7 — Tie 9**

> After the agent-primitives hooks + RunOptions work, the gap on per-call callbacks and dynamic config closes. Mastra still leads on volume of callback hooks (e.g., onStepFinish, onChunk read-only), but Oasis's typed decision-returning hooks (PrepareStep, OnIterationComplete, OnError) cover the genuine "steering the loop" cases without the read-only redundancy with `chan StreamEvent` / `StepTrace`. RunOptions plus `ExecuteWith` gives a Go-idiomatic per-call override surface that maps 1:1 with Mastra's `defaultOptions`. Oasis trades configurability breadth for compile-time safety and clarity — typed decision structs prevent invalid construction, and explicit resource budgets (suspend, attachment, tool result paging) for production resource control.

---

## Workflow / Orchestration

| Feature                       | Mastra                                                                                  | Oasis                                                                                | Winner |
|-------------------------------|-----------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------|--------|
| **Engine**                    | `Workflow` class with fluent builder; internally a **linear `stepFlow: StepFlowEntry[]` array** with embedded compound nodes (parallel/branch) traversed sequentially (`workflow.ts`, ~4,500 LOC) | True **DAG executor** with reactive scheduling — each completion immediately unblocks its dependents (no BFS wave) (`workflow/exec.go:246`) | Oasis |
| **Parallel**                  | Explicit `.parallel([...])` grouping — internally `Promise.all` over step array (`control-flow.ts:136`) | Implicit — multiple steps sharing the same `After(...)` predecessor are launched simultaneously as goroutines | Oasis |
| **Branching**                 | `.branch([[cond, step], ...])` — conditional fan-out                                    | `When(fn)` per-step conditional gate                                                 | Tie    |
| **Sequential steps**          | `.then(step)` fluent chain                                                              | `After(dep1, dep2, ...)` explicit dependency edges                                   | Tie    |
| **Loops**                     | `.foreach(step, {concurrency})`, `.dowhile(step, cond)`, `.dountil(step, cond)` — same three loop types, no iteration cap (`workflow.ts:2169–2316`) | `ForEach(IterOver, Concurrency)`, `DoWhile(While, MaxIter)`, `DoUntil(Until, MaxIter)` — default `MaxIter=10` returns `ErrMaxIterExceeded` on runaway | Oasis (MaxIter) |
| **Sleep primitives**          | `.sleep(ms)` and `.sleepUntil(date)` as first-class step types — map to native durable sleep on Inngest/evented engines | N/A                                                                                  | Mastra |
| **Data mapping**              | `.map({field: {step, path}})` — declarative restructuring between steps                 | None — manual `wCtx.Set`/`wCtx.Get` with `"{step}.output"` naming convention         | Mastra |
| **Step exit mechanisms**      | `suspend(payload)`, `bail(result)` (clean exit with result), `abort()` (cancel run)     | `Suspend(payload)` only — step either succeeds or errors                             | Mastra |
| **Type safety between steps** | `TPrevSchema` generic threads through `.then()` chain; Zod-validated `inputData` typed end-to-end (`workflow.ts:1758–1795`) | `wCtx.Get(key)` returns `(any, bool)` — manual cast required; template `{{key}}` resolution | Mastra |
| **Error Handling**            | Per-step `retries`, workflow-level `retryConfig`, `watch` for monitoring                | `Retry(n, delay)` per step or `WithDefaultRetry` workflow-wide; fail-fast cancels in-flight goroutines | Tie    |
| **Suspend/Resume**            | `.suspend()` with `suspendSchema` / `resumeSchema` (Zod typed); full snapshot incl. tracing context | `Suspend(payload)` returns live `*ErrSuspended` with `.Resume(ctx, data)` closure   | Tie    |
| **Snapshot persistence**      | Serialized `WorkflowRunState` to storage — survives process restarts                    | Closure-captured in-memory only — process death = lost state                         | Mastra |
| **Suspend TTL / memory cap**  | N/A (persisted to DB)                                                                   | Agent-level `WithSuspendTTL` (default 30 min) + `WithSuspendBudget`; workflow-level requires manual `Release()` | Oasis (memory caps) / Mastra (durability) |
| **Cross-suspend tracing**     | Tracing context persisted in snapshot — spans link across suspend/resume                | Tracing reset on resume                                                              | Mastra |
| **Time-travel**               | `Run._timeTravel()` re-executes from any prior step with overridden input/resume data (`workflow.ts:4257`) | N/A                                                                                  | Mastra |
| **Evented workflows**         | `EventedWorkflow` + `EventedExecutionEngine` with `schedule: {cron, timezone}` auto-triggering, Inngest integration (`evented/workflow.ts:1514`) | N/A                                                                                  | Mastra |
| **Runtime Definitions**       | `serializedStepFlow` for introspection (not for construction)                           | `FromDefinition(def, registry)` — JSON `{nodes, edges}` compiled to live `*Workflow` at runtime (`workflow/definition.go:21`) | Oasis |
| **Step types**                | Plain, parallel, conditional, foreach, dowhile, dountil, sleep, sleepUntil; agent/tool via `createStep(agent)` | `Step`, `AgentStep`, `ToolStep`, `ForEach`, `DoWhile`, `DoUntil` as first-class options | Tie |
| **Validation**                | Per-step input/resume/requestContext schema validation at execution time (`handlers/step.ts:98`); no graph validation (linear array) | Construction-time graph validation: duplicate names, missing deps, Kahn's-algorithm cycle detection, unreachability warning (`workflow.go:799–851`) | Oasis (graph) / Mastra (data) |
| **Schema enforcement**        | Zod / Standard Schema on every step input + output                                      | None at framework level                                                              | Mastra |
| **Nested workflows**          | `Workflow implements Step` — pass to `.then()`; shared pubsub                           | `Workflow implements Agent` — pass to `AgentStep`; output writes to `"{name}.output"` | Tie |

**Score: Mastra 10 — Oasis 5 — Tie 5**

> The previous version of this doc said Oasis had "more loop types" than Mastra — that was wrong. Both have `forEach` / `doWhile` / `doUntil`. What's genuinely unique to Oasis is `MaxIter` as a hard safety cap (default 10) preventing runaway loops; Mastra loops have no built-in iteration cap. The previous version also called Mastra's engine "graph-based" — internally it's a linear `stepFlow` array with embedded compound parallel/branch nodes. Oasis is the only one of the two with a real DAG scheduler.

---

## Tool System

| Feature                  | Mastra                                                                                  | Oasis                                                                                | Winner |
|--------------------------|-----------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------|--------|
| **Definition**           | `createTool({ id, inputSchema, outputSchema, suspendSchema, resumeSchema, requestContextSchema, execute })` — config object via factory (`tool.ts:540`) | Interface (`Tool`) or typed generics `Tool[In, Out]` with `Erase` reflection (`core/erase.go:12`) | Tie    |
| **Authoring pattern**    | Plain config object — close over external clients for state                             | Go struct with constructor + injected dependencies (naturally stateful)              | Tie    |
| **Output schema**        | `outputSchema` validated; can declare `toModelOutput` transform and `transform` policy  | `ToolDefinition.OutputSchema` auto-derived from `Out` via reflection at `Erase` time; opt-in `OutSchemaProvider.OutSchema()` override for richer constraints (`core/erase.go`); no runtime validation — codegen-friendly | Tie |
| **Input validation**     | **6-step coercion pipeline** (`tools/validation.ts:444`) — silently handles LLM quirks: null→{}, stringified JSON args (GLM4.7), null-for-optional (Gemini), `query`/`message`/`input`→`prompt` (Claude Sonnet drift). Pipeline IS the fallback strategy; no strict/warn/fallback mode enum | **2-step structural coercion pipeline** (`core/coerce.go`) — null/empty/whitespace → `{}`, stringified-JSON object/array unwrap one level; pure, zero-alloc on happy path, default-on, no opt-out. Deliberately rejects null-as-missing-key (surprises Go `*T` pointer fields) and field aliasing (opinionated rename damages literal-named fields) | Tie |
| **Error handling**       | Runtime errors throw and propagate as `MastraError`; validation errors return `ValidationError` value to LLM | All errors (including panics via `safeDispatch`) silently converted to `ToolResult{Error}` and returned to LLM | Tie    |
| **Parallel Dispatch**    | AI-SDK internal — `Promise.all` style I/O parallelism                                   | `dispatchParallel` bounded worker pool (cap `maxParallelDispatch=10`, `agent/dispatch.go:172`); panic recovery via `safeDispatch` (`dispatch.go:133`) | Oasis |
| **Tool Approval gates**  | `requireApproval` (bool/fn) at tool level + `requireToolApproval` at call-site; emits `tool-call-approval` stream chunk; resumed via `agent.approveToolCall()` / `declineToolCall()` (`builder.ts:797–818`) | No tool-level approval — use `WithInputHandler` + `ask_user` instead                 | Mastra |
| **FGA authorization**    | `requireFGA(TOOLS_EXECUTE)` middleware in `CoreToolBuilder` runs before every tool call (`builder.ts:577`) | N/A                                                                                  | Mastra |
| **Per-tool observability** | `TOOL_CALL` / `MCP_TOOL_CALL` span per tool execution with input/output/success attributes | `OTelSpanMiddleware` auto-wired when a `Tracer` is configured and not already in the user's chain — per-tool OTel span with input/output/success attributes | Tie |
| **Streaming tool output**| `context.writer: WritableStream` for in-execution chunk writes (`tools/stream.ts`); chunks wrapped with `toolCallId`/`toolName`/`runId` | `StreamingTool[In, Out]` adds `ExecuteStream(ctx, in, ch chan<- StreamEvent)` — typed event channel, registered via `EraseStreaming` | Tie |
| **Tool args streaming**  | `tool-call-input-streaming-start` → `tool-call-delta` → `tool-call-input-streaming-end` → `tool-call`; AI SDK v5 toolCallStreaming option | `EventToolCallDelta` for incremental tool arg fragments                              | Mastra |
| **Tool result content**  | Any JS value; AI SDK message format handles multi-part; `MCP CallToolResult.structuredContent` supported | `ToolResult{Content json.RawMessage, Error string, Attachments []Attachment}` — first-class multimodal at tool result level (`core/types.go:95`) | Tie |
| **Large result paging**  | Implicit truncation                                                                     | `WithToolResultStore` + built-in `read_full_result` tool: results > `maxToolResultMessageLen=100_000` runes (`agent/loop.go:56`) stored out-of-band with retrieval ID | Oasis |
| **Deferred schemas**     | N/A                                                                                     | `SchemaEnsurer` + `DeferredDefinitions` — lazy JSON schema resolution at call time   | Oasis  |
| **MCP wrapping**         | Tools become first-class `Tool` objects; AI SDK reconnect; `mcpMetadata` triggers `MCP_TOOL_CALL` span | Per-server call mutex serializes concurrent MCP calls (`mcp/tool_wrapper.go`); server health checks; `mcp__<server>__<tool>` namespacing | Tie |
| **MCP semantic discovery** | N/A                                                                                   | `ToolSearch` built-in scores MCP tools by keyword match for the LLM to discover lazy-loaded tools | Oasis |
| **Background-eligible tools** | **Full `BackgroundTaskManager`**: `ToolBackgroundConfig` per-tool (`enabled`, `timeoutMs`, `maxRetries`, `waitTimeoutMs`, `onComplete`, `onFailed`); lifecycle `pending→running→suspended→completed/failed/cancelled/timed_out`; tasks can self-suspend and be resumed; `globalConcurrency=10` / `perAgentConcurrency=5` with three backpressure modes (queue/reject/fallback-sync) | None — synchronous within loop                                                       | Mastra |
| **Per-tool retry / timeout** | Via `ToolBackgroundConfig.timeoutMs` and `maxRetries` (coupled to BackgroundTaskManager) | `core.ToolPolicy{Timeout, Retries, RetryDelay, MaxRetryDelay, RetryOn}` + `agent.WithToolPolicy(name, p)` / `WithToolPolicyMatch(matcher, p)` (ServeMux-style: exact name beats first-match-wins matcher); exponential backoff with cap; opt-in `core.Retryable` interface + `core.RetryableError(err)` + composable `core.DefaultRetryOn` predicate (honors `context.DeadlineExceeded`, `net.Error.Timeout()`, the `Retryable` mark); streaming tools bypass with one-shot warn | Tie |
| **Dynamic tools**        | `agent.tools` may be a `DynamicArgument` function                                       | `WithDynamicTools(fn)` resolves per loop iteration                                   | Tie    |
| **Built-in / framework tools** | Agent-as-tool (`agent-<name>`), Workflow-as-tool, MCP tools — auto-injected; no separate "built-in tool" inventory | `ask_user`, `execute_plan`, `spawn_agent`, `read_full_result`, `ToolSearch` (when MCP) — explicit framework tools | Oasis |
| **AI SDK Compat**        | Vercel AI SDK `tool()` format + `ProviderDefinedTool` (e.g. `google.tools.googleSearch()`) passthrough | N/A (Go)                                                                             | N/A    |

**Score: Mastra 5 — Oasis 5 — Tie 10 — N/A 1**

> The previous version of this doc listed "strict/warn/fallback validation strategies" for mastra — that's wrong, no such mode enum exists; the actual validation is a single 6-step coercion pipeline tuned for LLM input quirks. The previous version also missed Mastra's `BackgroundTaskManager` entirely (a complete async-task subsystem with retries, timeouts, lifecycle, and three backpressure modes) and Mastra's FGA per-tool authorization middleware. Conversely, Oasis's `ToolResult.Attachments` multimodal channel and its `ToolResultStore` + `read_full_result` paging mechanism for large outputs were underdocumented.
>
> **2026-05-21 (post tool robustness layer):** three rows flipped Mastra → Tie. Oasis shipped the [tool robustness layer](../superpowers/specs/2026-05-21-tool-robustness-layer-design.md): (a) `ToolDefinition.OutputSchema` auto-derived from `Out` at registration + opt-in `OutSchemaProvider` override — codegen-friendly schema publication without runtime validation; (b) structural input coercion (null→{}, stringified-JSON unwrap) — deliberately narrower than Mastra's 6-step pipeline, rejecting null-as-missing and field aliasing as opinionated transforms that misbehave on Go types; (c) `ToolPolicy` per-tool timeout/retry with exponential backoff and opt-in `Retryable` convention — exact-name + matcher precedence, streaming-tool bypass. Mastra still leads on `BackgroundTaskManager` (durable async lifecycle, three backpressure modes), `requireApproval` gates, FGA, per-tool OTel spans, `transform`/`toModelOutput` runtime validation hooks, and tool args streaming.

---

## Memory

| Feature                         | Mastra                                                                                              | Oasis                                                                                  | Winner |
|---------------------------------|-----------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------|--------|
| **Architecture**                | 3 named processors (`MessageHistory`, `WorkingMemory`, `SemanticRecall`) + `ObservationalMemory` engine (replaces `MessageHistory` rather than composing); `Memory` class 2,873 LOC + ObservationalMemory 3,633 LOC | Single `AgentMemory` struct in `memory/memory_orchestration.go` (841 lines) + `history/history.go` options (129 lines) = ~970 LOC total | Tie |
| **Message History**             | `MessageHistory` processor; `lastMessages` window with ID-based dedup against existing messages     | `GetMessages` + `MaxHistory` + `MaxTokens` (`memory/memory_orchestration.go:163`)      | Tie    |
| **Working Memory**              | LLM-writable scratchpad — Markdown template or Zod schema; resource- or thread-scoped; mutex-protected writes via `async-mutex` keyed `resource-${id}` / `thread-${id}`; LLM updates via `updateWorkingMemory` tool | N/A                                                                                    | Mastra |
| **Observational Memory**        | Two-stage observe→reflect LLM pipeline; thread-aware (XML-wrapped per-thread observations); multi-thread observation via `getOtherThreadsContext`; activation triggers: token threshold, `activateAfterIdle` TTL, provider change | N/A                                                                                    | Mastra |
| **Semantic Recall**             | **Dedicated external vector DB** (Pinecone/pgvector/Qdrant/Chroma/Lance/+10) with native ANN; index name pattern `memory_messages_${model}_${dimension}`; resource- or thread-scoped vector filter | **Inline `embedding TEXT` column** in messages table; **brute-force O(N) cosine** scan in process; `CrossThreadSearch()` with embedder configured via agent-level `WithEmbedding`, `chatID` filtered after SQL returns | Mastra |
| **Semantic Trimming**           | Via `SemanticRecall` topK + range window                                                            | `SemanticTrim` with `KeepRecent` anchor; **fresh embeddings each call** (no in-memory embedding cache) | Tie |
| **Token-budget Trimming**       | `lastMessages` count window only; no token-budget trim in core processors                           | `MaxTokens` + oldest-first fallback; estimate `runeCount/4 + 4`                        | Oasis  |
| **Token Counter sophistication**| `tokenx` library + per-provider image/file token formulas (OpenAI tile, Anthropic pixels/750, Gemini tiles vs resolution map); remote image dimension probing; SHA-1 part-level cache; live API fallback for exact counts | Single formula: `utf8.RuneCountInString(content)/4 + 4`; no image/multimodal token accounting | Mastra |
| **Per-turn compression**        | N/A (handled by Observational Memory)                                                               | `history.Compress(fn, threshold)` — LLM-driven in-flight summarization without persistence dependency | Oasis |
| **User Memory (Facts)**         | Subsumed by Working / Observational Memory                                                          | First-class `MemoryStore`: LLM extraction (`extractAndPersistFacts`), semantic dedup ≥0.85, supersession ≥0.80, probabilistic decay 5%/turn (`rand.IntN(20)==0`), 7-day×0.95 fade, 30-day deletion at <0.3 confidence | Oasis |
| **Compaction**                  | Via Observational Memory reflection only                                                            | Three mechanisms: inline tool-result truncation, per-turn `Compress`, structured 9-section `StructuredCompactor` | Oasis |
| **Injection Guard at memory**   | N/A                                                                                                 | `sanitizeFacts()` + `containsInjectionPattern()` — **11-entry** blocklist (`[system`, `[assistant`, `<|im_start|>`, `ignore previous`, etc.) + LLM-prompt-level guard; deliberately narrow to avoid false positives | Oasis |
| **Persist Backpressure**        | `SaveQueueManager` debounces async persistence (100ms window, immediate flush at 1s stale)          | Bounded semaphore `maxPersistGoroutines=16` + 2-second wait + drop or "lightweight persist" fallback (skip embedding/extraction, still write messages); `context.WithoutCancel` detaches from request | Oasis |
| **Thread Cloning**              | `memory.cloneThread()` — copies messages + working memory + observational memory + re-indexes vectors; ancestry tracked in metadata; rollback on OM clone failure | N/A                                                                                    | Mastra |
| **Multi-modal persistence**     | Full multi-modal: image, file, reasoning, tool-invocation parts persisted with AI SDK v1/v4/v5/v6 compat | Attachments on `AgentTask` forwarded to LLM but `messages` table has only `content TEXT` — not persisted/embedded | Mastra |
| **Memory pagination**           | Full pagination in `recall()`: `perPage`, `page`, `orderBy`, date-range filtering, context-window `include` around target messages | `GetMessages(limit)` — most-recent N only, no offset/cursor                            | Mastra |
| **Message format abstraction**  | `MessageList` normalization layer handles AI SDK v1/v4/v5/v6 message format variations              | Flat `core.Message{Role, Content, Embedding, Metadata, CreatedAt}` — single format     | Mastra |
| **REST API**                    | 19 routes in `MEMORY_ROUTES`: threads CRUD, messages, working memory, clone, search, OM status/history, config | N/A                                                                                    | Mastra |
| **Runtime per-call override**   | `RequestContext.set('MastraMemory', { memoryConfig: { lastMessages: 5 } })` for per-request overrides | N/A                                                                                    | Mastra |
| **Embedding dimension handling**| Auto-detected once and cached; dimension-aware index names                                          | Caller chooses embedding provider; brute-force scan tolerant to dimension              | Mastra |
| **RBAC / FGA on memory**        | `checkThreadFGA` — `MEMORY_READ` and write permission gates; `filterAccessibleThreads` in REST handlers | N/A                                                                                    | Mastra |
| **Schema migrations**           | `CREATE TABLE IF NOT EXISTS` per adapter; no in-code migration framework                            | Idempotent `ALTER TABLE` run-and-ignore in `Init`; no version tracking                 | Tie    |
| **Scoping**                     | `threadId` + `resourceId` + cross-resource (admin)                                                  | `threadID` + `chatID` (multi-tenant) — filtering happens after `SearchMessages` returns, not in SQL | Tie |
| **Observability**               | OTel spans per memory op (`recall`/`save`/`update`/`delete`); REST `GET /memory/status` exposes OM buffer state | `agent.memory.load` / `agent.memory.persist` spans via `core.Tracer`; structured `slog.Logger`; no REST inspection | Mastra |

**Score: Mastra 13 — Oasis 7 — Tie 4**

> Two corrections to the previous version of this doc:
> 1. Oasis's injection-guard list is **11 phrases, not 80+** — the original number confused LLM-prompt-level guardrails with the code-level blocklist (`memory/memory_orchestration.go:748–760`).
> 2. Oasis has **no embedding cache** for semantic trimming — embeddings are computed fresh each call. The previous claim about `memory/embedding_cache.go` was incorrect.
>
> The architectural contrast is also sharper than the previous version showed: **Mastra delegates semantic recall to a dedicated external ANN-indexed vector DB; Oasis stores embeddings inline in the messages table and brute-force scans in process**. The Mastra path scales to millions of messages with sub-linear queries; the Oasis path keeps deployments single-binary at the cost of O(N) recall queries. Mastra's memory is *LLM-assisted at retrieval time* (Working Memory tool, Observational Memory reflection); Oasis's is *pre-processed before the LLM call* (facts pre-stored, recall pre-computed) — cheaper per turn but less adaptive.

---

## RAG Pipeline

| Feature                    | Mastra                                                                          | Oasis                                                                                | Winner |
|----------------------------|---------------------------------------------------------------------------------|--------------------------------------------------------------------------------------|--------|
| **Chunking Strategies**    | 9 (recursive, character, token, markdown, HTML, JSON, code, sentence, HTML)     | 4 core (Recursive, Markdown, Semantic, ParentChild) + Flat hierarchy                 | Mastra |
| **Contextual Chunking**    | N/A                                                                             | `ingest/contextual.go` — LLM enriches each chunk with document context pre-embedding | Oasis  |
| **Cross-document Graph**   | N/A                                                                             | `ingest/crossdoc.go` — LLM extracts semantic edges across document boundaries        | Oasis  |
| **Parent-Child Retrieval** | N/A                                                                             | `StrategyParentChild` — match small chunks, return large parent context              | Oasis  |
| **Extractors**             | Text, HTML, Markdown, JSON                                                      | Text, HTML, Markdown, CSV, JSON, DOCX, PDF                                           | Oasis  |
| **Hybrid Retrieval**       | Depends on store (some support BM25 + vector natively)                          | `HybridRetriever` — vector + FTS with Reciprocal Rank Fusion (`rag/retriever.go:146`) | Oasis  |
| **GraphRAG**               | Query-time graph from retrieved chunks; no persistence (see mastra issue #3926) | `GraphRetriever` — ingestion-time edges, 8 typed relations, multi-hop BFS with hop decay (`rag/retriever.go:688`) | Oasis |
| **Reranking**              | Weighted scoring, Cohere, ZeroEntropy, custom                                   | `ScoreReranker`, `LLMReranker` (`rag/retriever.go:486`)                              | Mastra |
| **Metadata Filtering**     | MongoDB/Sift syntax, translated per store                                       | `ChunkFilter` with operators (eq, in, gt, lt)                                        | Tie    |

**Score: Mastra 2 — Oasis 6 — Tie 1**

---

## LLM Providers

| Feature                | Mastra                                                                                  | Oasis                                                                                  | Winner |
|------------------------|-----------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------|--------|
| **Provider Breadth**   | Vercel AI SDK ecosystem — 2,436 models / 81 providers via Model Router string           | Native Gemini + OpenAI-compat (covers OpenAI, Anthropic, Groq, Together, Azure, etc.); live catalog via `provider/catalog/` fetches 87 platforms from models.dev | Tie    |
| **Integration Style**  | `model: "openai/gpt-4o"` string with auto-key detection                                 | Explicit constructor with base URL; `resolve.Provider` for config-driven setup         | Mastra |
| **Live model catalog** | N/A                                                                                     | Dynamic — TTL-cached fetch of 87 platforms + model lists from models.dev               | Oasis  |
| **Fallbacks**          | `ModelFallbacks` + `ModelRouter` — automatic failover on 500/429/timeout                | `WithRetry` decorator (429/503, exponential backoff)                                   | Tie    |
| **Rate Limiting**      | Not built-in — relies on provider-side limits                                           | `WithRateLimit(RPM, TPM)` — proactive sliding window (`ratelimit/ratelimit.go:76`)     | Oasis  |
| **Batch Processing**   | N/A                                                                                     | `BatchProvider` + `BatchEmbeddingProvider`; native Gemini batch (`provider/gemini/batch.go:72`) | Oasis |
| **Provider Caching**   | `ResponseCache` processor with SHA-256 key + TTL; embedding LRU (1000 entries)          | Gemini `CreateCachedContent` server-side caching only (`provider/gemini/cache.go`)     | Mastra |
| **Multimodal**         | Multi-part content with image / file parts                                              | `Attachment{MimeType, Data, URL}` forwarded to LLM; multimodal embedding via `ingest`  | Tie    |
| **SDK Footprint**      | Uses Vercel AI SDK + provider packages — heavy dependency graph                         | Raw `net/http` only — no SDK in core                                                   | Oasis  |

**Score: Mastra 2 — Oasis 4 — Tie 3**

---

## Streaming

| Feature                       | Mastra                                                                                              | Oasis                                                                                       | Winner |
|-------------------------------|-----------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------|--------|
| **API**                       | `.stream()` returns `MastraModelOutput<OUTPUT>` — async-iterable `fullStream` (WHATWG `ReadableStream` from `node:stream/web`); `streamLegacy()` for AI SDK `specificationVersion: 'v1'` models | `ExecuteStream(ctx, task, ch chan<- StreamEvent) (AgentResult, error)` — caller pre-allocates buffered Go channel | Tie |
| **Lifecycle envelope**        | Implicit via `start` / `finish` chunks; `finishReason` is a free-form string on the finish chunk                                       | Explicit envelope: `EventRunStart` first, `EventRunFinish` last, `EventIterationStart`/`Finish` around every LLM call. Typed `FinishReason` enum with 8 constants (`FinishStop` / `FinishToolCalls` / `FinishLength` / `FinishContentFilter` / `FinishHalted` / `FinishSuspended` / `FinishMaxIter` / `FinishError`) on the finish event + `Warnings` + `ProviderMeta`. | Tie |
| **Event type count**          | **~70 distinct chunk types** across `AgentChunkType` (~35), `NetworkChunkType` (~20 network-specific), `WorkflowStreamEvent` (~11)                | **23 event types** in a single `StreamEvent` struct (was 16; +7 from lifecycle envelope + structured object stream)                                         | Mastra |
| **Reasoning chunks**          | First-class: `reasoning-start` / `reasoning-delta` / `reasoning-end` / `reasoning-signature` (cryptographic) / `redacted-reasoning` | `EventReasoningStart` / `EventReasoningDelta` / `EventReasoningEnd` triplet; `EventThinking` remains (deprecated once providers port to the triplet) | Tie |
| **Tool args streaming**       | `tool-call-input-streaming-start` → `tool-call-delta` × N → `tool-call-input-streaming-end` → `tool-call` → `tool-result` | `EventToolCallStart` + `EventToolCallDelta` + `EventToolCallResult`                         | Mastra |
| **Tool progress mid-execution** | No first-class tool-progress interface — tools write to `context.writer: WritableStream`         | `EventToolProgress` — tools implementing `StreamingTool[In, Out]` emit arbitrary JSON progress via `ExecuteStream(ctx, in, ch)` | Oasis |
| **Structured object streaming** | `objectStream: ReadableStream<Partial<OUTPUT>>` + `elementStream` for array outputs               | `EventObjectDelta` snapshots backed by a forgiving partial-JSON parser (`core.PartialJSON`) + `EventObjectFinish` with validated bytes + `EventElementDelta` per completed array element for top-level array schemas. Typed adapters via free generics: `oasis.StreamObjectAs[T](stream) <-chan T` and `oasis.ResultObjectAs[T](result) (T, error)` — no generic contagion through `Agent` / `Network` / `Workflow`. | Tie |
| **Background-task streaming** | 10 dedicated chunk types: `background-task-started/-completed/-failed/-progress/-running/-cancelled/-output/-suspended/-resumed` | N/A                                                                                         | Mastra |
| **Network / nested events**   | `routing-agent-*`, `agent-execution-*`, `workflow-execution-*`, `tool-execution-*` envelope chunks; `from: ChunkFrom` + `runId` for origin tracking | Subagent events appear inline on parent channel between `EventAgentStart` / `EventAgentFinish`; `Name` field identifies emitter | Tie |
| **Output convenience accessors** | `MastraModelOutput` exposes ~25 promise-based accessors: `.text`, `.reasoning`, `.reasoningText`, `.sources`, `.files`, `.steps`, `.toolCalls`, `.toolResults`, `.usage`, `.totalUsage`, `.warnings`, `.finishReason`, `.providerMetadata`, `.object`, `.suspendPayload`, `.resumeSchema`, `.textStream`, `.objectStream`, `.elementStream`, `.tripwire`, `.error`, `.getFullOutput()` — all lazy | `AgentResult` ships typed accessors `Text()`, `Reasoning()`, `ToolCalls()`, `ToolResults()`, `Sources()`, `Files()`, `Warnings()`, `FinishReason()`, `ProviderMeta()`, `SuspendPayload()`, `Object()`, `Iterations()`, `LastStep()`, `StepByTool(name)` — synchronous. `Stream` exposes the same methods (blocking on completion) so streaming and sync code share the surface. | Tie |
| **Multi-reader / fan-out**    | `#bufferedChunks` replay buffer: every new `fullStream` reader gets **full chunk history** from the start (broadcast/replay, not queue) | `Stream.Events()` fan-out with bounded ring-buffer replay (default 256 events, `RunOptions.StreamReplayLimit`); slow subscribers are dropped with a `subscriber-dropped` warning — they cannot stall the agent | Tie |
| **SSE / HTTP integration**    | No core helper; legacy `.toDataStreamResponse()` (AI SDK protocol); v5 server handler in `packages/server` | Built-in `ServeSSE(ctx, w, agent, task)` one-liner: sets headers, allocates 64-slot channel, writes `event: <type>\ndata: <json>\n\n` (`agent/stream.go:30`) | Oasis |
| **Buffer Tuning**             | Internal WHATWG `ReadableStream` queue — unbounded by default; relies on consumer pull              | Explicit `defaultIterChBufSize = 64` events (`agent/stream_fwd.go:15`); regression-benchmarked, held pending real-workload telemetry | Oasis |
| **Backpressure**              | Unbounded JS heap queue under slow consumer (no flow-control back to LLM); plus `#bufferedChunks` grows unbounded for late subscribers | Producer goroutine blocks on `ch <- ev` when buffer full → propagates back to LLM read loop end-to-end | Oasis |
| **Cancellation**              | `abortSignal: controller.signal` threaded through to provider fetch; emits `abort` chunk on stream | `context.Context` cancellation; `select { case ch <- ev: case <-ctx.Done() }` guards; `newStreamForwarder` drains `iterCh` on cancel before exit (`stream_fwd.go:38–41`); channel just closes — no explicit abort event | Tie |
| **WebSocket transport**       | `StreamTransport` with `type: 'openai-websocket'` for OpenAI Realtime API                          | N/A — SSE-over-HTTP only                                                                    | Mastra |
| **`tripwire` chunk**          | Emitted when processor halts mid-stream with retry metadata + `processorId`                        | N/A as distinct chunk type — `ErrHalt` short-circuits to canned response                    | Mastra |
| **Per-stream observability**  | Provider span (`llm: '<modelId>'`, `SpanType.MODEL_GENERATION`) per LLM call with model/provider/temp/maxTokens attrs; ended in `onFinish` with `finishReason` + `usage` | Three-level span hierarchy under the existing `agent.execute` root: `agent.iteration[N]` per loop iteration → `llm.generate` per provider call (attrs: provider, input_tokens, output_tokens, finish_reason) → `tool.execute` per tool. Plus `AgentResult.Iterations` exposes the same data as `[]IterationTrace` for consumers that don't run OTel. Zero overhead when no tracer configured (nil-check skip). | Tie |

**Score: Mastra 5 — Oasis 4 — Tie 9**

> **2026-05-21 (post streaming-world-class overhaul):** three rows flipped Mastra → Tie and one row was added (Lifecycle envelope). Oasis shipped the [streaming world-class overhaul](../superpowers/specs/2026-05-21-streaming-world-class-design.md) in seven commits on `migration/microkernel`:
> - **Structured object streaming** (was Mastra): `EventObjectDelta` snapshots via the new `core.PartialJSON` forgiving parser + `EventObjectFinish` + `EventElementDelta` for top-level array schemas, plus `oasis.StreamObjectAs[T]` / `oasis.ResultObjectAs[T]` typed adapters as free generic functions (no contagion of generics through `Agent` / `Network` / `Workflow`).
> - **Per-stream observability** (was Mastra): `agent.iteration` per LLM-call iteration + `llm.generate` per provider call, both attribute-rich; plus `AgentResult.Iterations` for non-OTel consumers.
> - **Lifecycle envelope** (new row): `EventRunStart` / `EventRunFinish` / `EventIterationStart` / `EventIterationFinish` with typed `FinishReason` enum (8 constants), `Warnings`, `ProviderMeta` carried on the finish event. Deprecates `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt` — all collapsed into the envelope's `FinishReason`.
> - **Output convenience accessors** (already Tie, now richer parity): `AgentResult` and `Stream` both expose `Sources()`, `Files()`, `Warnings()`, `FinishReason()`, `ProviderMeta()`, `SuspendPayload()`, `Object()`, `Iterations()` alongside the previously-existing accessors. Same method names on both paths.
>
> Mastra still leads on: raw event type count (~70 vs 23), tool-args streaming envelope (`tool-call-input-streaming-start` → `-delta` → `-end` envelope, vs Oasis's single `EventToolCallDelta`), `BackgroundTaskManager` event family, WebSocket transport for OpenAI Realtime, and the `tripwire` chunk with `processorId` + automatic retry semantics. Oasis still leads on built-in `ServeSSE`, explicit buffer tuning, end-to-end backpressure that propagates to the LLM read loop, and the `EventToolProgress` interface for long-running tools.
>
> Architecturally: Mastra streaming is *promise-rich* — you can `await stream.text` without iterating; new readers get full chunk history replayed; tool args stream in fragments. Oasis streaming is *channel-flat with a typed envelope* — single reader, single struct type, but the producer naturally pauses when the consumer slows, every run now has explicit start/finish/iteration bookends with a typed finish reason, and structured-output consumers get partial-object snapshots typed through a free generic adapter.

---

## Human-in-the-Loop

| Feature                              | Mastra                                                                                                                              | Oasis                                                                                  | Winner |
|--------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------|--------|
| **HITL mechanisms in framework**     | **Four distinct paths**: (1) workflow step suspend; (2) in-execution tool suspend via `context.agent.suspend(payload)`; (3) tool approval gate (`requireApproval` / `requireToolApproval`); (4) harness approval policy (deny/allow/ask) | **Three paths**: (1) `WithInputHandler` + `ask_user` tool; (2) agent-level `Suspend(payload)`; (3) workflow-level `Suspend(payload)` | Mastra (breadth) |
| **LLM-Initiated pausing**            | N/A — agent loop doesn't autonomously call for human input mid-stream                                                               | First-class: LLM calls `ask_user` tool when it decides clarification is needed (`agent/llm.go:230`)  | Oasis  |
| **Programmer-initiated suspend**     | `context.workflow.suspend(payload)` from step; `context.agent.suspend()` from tool; return type `void`                              | `return agent.Suspend(payload)` or `workflow.Suspend(payload)` — returns Go `error` for normal propagation | Tie    |
| **Framework-initiated suspend**      | `requireApproval: true` on tool def → framework pauses before execution; emits `tool-call-approval` stream chunk (`tool-call.ts:222`) | `WithToolApproval(name, opts...)` — framework-enforced approval gate built on the `ToolMiddleware` chain; emits `EventToolApprovalPending` on the stream before prompting | Tie |
| **Approval API**                     | First-class agent methods: `agent.approveToolCall({runId})` / `declineToolCall()` / `approveToolCallGenerate()` / `declineToolCallGenerate()` (`agent.ts:6835–6861`) | `InputResponse.Value` approves/denies; `DenyAskLLMToRevise` (default) returns error `ToolResult` so the LLM can adapt; `DenyHalt` halts with `*core.ErrHalt`; outermost middleware layer — retries do not re-prompt | Tie |
| **Resume data typing**               | Zod `resumeSchema` per step/tool; validated at `run.resume()` via `_validateResumeData` (`workflow.ts:3241`); serialized to JSON Schema in `tool-call-suspended` chunk | Typed via `SuspendProtocol[Req, Resp].Resume(suspended, ctx, data)` — compile-time `Resp` check; `WithRenderResume(func(Resp) string)` shapes the LLM-visible message; untyped `(*ErrSuspended).Resume` remains as escape hatch | Tie |
| **Suspend payload typing**           | Zod `suspendSchema` (optional)                                                                                                      | Typed via `SuspendProtocol[Req, Resp].Suspend(payload)` — compile-time `Req` check; untyped `Suspend(json.RawMessage)` remains as escape hatch | Tie    |
| **Persistence model**                | Full `WorkflowRunState` snapshot persisted to configurable storage (pg, libsql, redis, DynamoDB, Cloudflare KV, etc.) including step results, `serializedStepGraph`, `suspendedPaths`, `resumeLabels`, `requestContext`, `tracingContext` | In-process closure capturing `snapshotResults` map and `snapshotValues` map (workflow) or deep-copied `[]ChatMessage` (agent) | Mastra |
| **Cross-process resume**             | Native — load snapshot from storage in any process with `run.resume({runId})`                                                       | Not native — `ErrSuspended` closure lives in caller's heap; caller must serialize externally to bridge processes | Mastra |
| **TTL / auto-cleanup**               | N/A — storage record persists with `status: 'suspended'` until resumed or manually deleted                                          | `defaultSuspendTTL = 30 * time.Minute` (`agent/suspend.go:24`); `WithSuspendTTL(d)` overrides; timer nils closures and decrements budget | Oasis |
| **Memory budget**                    | N/A (storage-backed)                                                                                                                | `WithSuspendBudget(maxSnapshots, maxBytes)` defaults 20 / 256 MB; concurrent suspensions share atomic counters | Oasis  |
| **Manual release**                   | Delete snapshot from storage                                                                                                        | `suspended.Release()` — nils closures, decrements budget, cancels TTL timer            | Oasis  |
| **Multiple concurrent suspends**     | `suspendedPaths` tracks multiple suspended steps per run; nested workflow suspensions bubble through `__workflow_meta.path`        | Workflow: only the first suspended step per `Execute()` is surfaced; agent: budget-governed concurrent suspensions | Mastra |
| **Approval UI / Clients**            | **First-class UI surface**: Studio approval cards (`tool-approval-buttons.tsx`); React hooks `useAgent()` with approve/decline; REST `POST /agents/:id/approve-tool-call`; client SDKs (JS, React); mastracode TUI dialog | DIY — implement `InputHandler` for your channel (Telegram, Slack, CLI readline, HTTP long-poll)                              | Mastra |
| **Harness approval policy**          | Per-tool `deny` / `allow` / `ask`; session-grant / category-grant escalation; auto-resolution before prompting (`harness.ts:2371`) | N/A — caller implements policy                                                         | Mastra |
| **Stream chunk integration**         | `tool-call-approval`, `tool-call-suspended` chunk types surface in stream; `output.status = 'suspended'`, `finishReason = 'suspended'` | `EventToolCallSuspended`, `EventStepSuspended`, `EventProcessorSuspended` emitted mid-stream before `EventIterationFinish`; `EventRunFinish` carries `Protocol` + `SuspendPayload` when `FinishReason == FinishSuspended`; `Stream.Suspended()` / `Stream.SuspendedProtocol()` accessors block on completion | Tie    |
| **Constraints on LLM-initiated ask** | N/A                                                                                                                                 | `ask_user` blocked inside `execute_plan` steps and always blocked in sub-agents (`llm.go:177`, `agentcore.go:510`) | Oasis  |
| **Resume data injection**            | Validated `resumeData` passed to step `execute()` as `params.resumeData`; tool execute receives `agent.resumeData`                  | Appended to message list as `UserMessage("Human input: "+string(data))` then loop re-runs | Mastra |
| **Cross-suspend tracing**            | Tracing context persisted in snapshot — spans link across process restarts                                                          | Tracing resets on resume                                                               | Mastra |
| **Observability of suspended state** | Studio shows suspended steps with `resumeSchema`; `workflow.listWorkflowRuns({status: 'suspended'})`                                | Span sets `workflow.status = "suspended"`; `IterationTrace.FinishReason` lets callers walk `AgentResult.Iterations` to identify the suspending iteration; `AgentResult.SuspendProtocol` carries the typed-protocol tag | Tie    |

**Score: Mastra 11 — Oasis 5 — Tie 5**

> The previous version of this doc said "Mastra: Workflow suspend/resume only" — that was significantly incomplete. Mastra has **four distinct HITL mechanisms**, the most important of which (tool approval gate with `requireApproval`/`requireToolApproval`, agent-level `approveToolCall()`/`declineToolCall()` API, and Studio approval cards UI) constitute a full second HITL path independent of workflow suspend. Conversely, **LLM-initiated pausing via `ask_user` is genuinely Oasis-unique** — Mastra's agent loop does not autonomously pause for human input; pausing in Mastra is always programmer- or framework-initiated.
>
> Architectural contrast: Mastra HITL is *durable* (state in DB, cross-process resume, rich UI surface, typed resume data); Oasis HITL is *ephemeral* (in-memory closures, TTL + budget caps, channel/handler-based UX, LLM-driven `ask_user` as the everyday path).

> **2026-05-22 (post typed HITL contracts):** the Suspend/Resume payload-typing rows flipped Mastra → Tie via [`docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md`](../superpowers/specs/2026-05-22-typed-hitl-contracts-design.md): `SuspendProtocol[Req, Resp]` declared once and referenced by both the suspending site and the caller that resumes; compile-time `Req`/`Resp` enforcement on `Suspend`, `PayloadFrom`, `Resume`, `ResumeStream`; runtime string-tag check as a safety net; per-protocol `WithRenderResume` formatter for the LLM-visible resume message. `Agent`/`Workflow`/`Network` stay monomorphic — generics live only on the protocol value and its methods. Mastra still leads on durable cross-process snapshot persistence, multiple concurrent suspended paths surfaced per workflow run, the synchronous-to-async tool approval gate, Studio UI for suspended runs, and cross-suspend tracing context propagation. Those gaps are the targets of HITL specs #2–#6 on the same date.

---

## Processor Pipeline / Guardrails

| Feature                  | Mastra                                                                              | Oasis                                                                                   | Winner |
|--------------------------|-------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------|--------|
| **Input Processing**     | Input processors (security, moderation, normalization)                              | `PreProcessor` — modify `ChatRequest` before LLM                                        | Tie    |
| **Output Processing**    | Output processors — per-step + final hooks                                          | `PostProcessor` — modify `ChatResponse` after LLM                                       | Mastra |
| **Post-Tool**            | N/A                                                                                 | `PostToolProcessor` — modify tool results before history                                | Oasis  |
| **Stream Tripwire**      | `TripWire` exception aborts the stream with `processorId` in the chunk              | N/A as distinct chunk type                                                              | Mastra |
| **Built-in Guards**      | PII detector, prompt injection, moderation, unicode normalizer                      | `InjectionGuard` (80+ phrases, ZW char strip, NFKC normalize), `ContentGuard`, `KeywordGuard`, `MaxToolCallsGuard` (`guardrail/guardrail.go`) | Tie |
| **Halt**                 | Throw `MastraError`                                                                 | `ErrHalt` — graceful early stop from any processor                                      | Oasis  |
| **Suspend**              | N/A from processor layer                                                            | Processors can trigger `Suspend(payload)`                                               | Oasis  |
| **Response Cache**       | `ResponseCache` processor — full prompt-completion cache                            | N/A                                                                                     | Mastra |

**Score: Mastra 3 — Oasis 3 — Tie 2**

---

## MCP Support

| Feature                    | Mastra                                                                                       | Oasis                                                                                          | Winner |
|----------------------------|----------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------|--------|
| **Client transports**      | stdio + SSE + StreamableHTTP with automatic SSE fallback (`packages/mcp/src/client/client.ts:188`) | `HTTPClient` + `StdioClient` (`mcp/client_http.go`, `mcp/client_stdio.go`)                | Mastra |
| **Server**                 | `MCPServerBase` exposes tools + agents as MCP                                                | stdio JSON-RPC server with tools + resources (`mcp/server.go`)                                 | Tie    |
| **Resources / Prompts**    | Resource listing/reading, prompt listing/getting, elicitation actions                        | Tool + resource registration; auth via `mcp/auth.go`                                           | Mastra |
| **Progress / Reconnect**   | Built-in progress tracking + reconnection logic                                              | N/A                                                                                            | Mastra |
| **Semantic tool discovery**| N/A                                                                                          | `ToolSearch` over MCP tools (`mcp/toolsearch.go`)                                              | Oasis  |
| **Deferred tool schemas**  | N/A                                                                                          | `mcp/defer.go` — lazy schema resolution at call time                                           | Oasis  |
| **Agents as Tools**        | Auto-converts agents to `ask_<agent>` tools                                                  | Network exposes subagents as tools; no auto MCP wrap                                           | Mastra |
| **MCP docs server**        | N/A                                                                                          | `cmd/mcp-docs` — embedded framework docs served to AI assistants                               | Oasis  |

**Score: Mastra 4 — Oasis 3 — Tie 1**

> The original 2026-02-21 version of this doc listed oasis as `N/A` on MCP client. Oasis has shipped both HTTP and stdio MCP clients since.

---

## Storage

| Feature              | Mastra                                                                                                 | Oasis                                                              | Winner |
|----------------------|--------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------|--------|
| **Relational**       | LibSQL, PostgreSQL, MongoDB, DynamoDB, MSSQL, Cloudflare D1, Convex, DSQL, ClickHouse, Couchbase, DuckDB | SQLite (pure-Go), PostgreSQL (pgx)                                 | Mastra |
| **Vector**           | 14+ stores (Pinecone, Qdrant, Chroma, pgvector, Lance, Astra, ES, OS, Vectorize, S3 Vectors, Upstash, Turbopuffer) | Integrated into `core.Store` (SQLite cosine in Go, Postgres pgvector HNSW) | Mastra |
| **Edge backends**    | Cloudflare KV / D1 / DO / Vectorize, Upstash                                                           | N/A                                                                | Mastra |
| **Architecture**     | Domain-partitioned (`memory`, `workflows`, `vectors`, `traces`, `evals`, `logs`, `datasets`, `editor`) | Unified `core.Store` interface (relational + vector in one)        | Tie    |
| **Test matrix**      | Per-backend correctness validated via `stores/_test-utils/`                                            | Backend tests live alongside satellites                            | Mastra |
| **FTS for hybrid**   | Depends on store                                                                                       | SQLite FTS5 for keyword search + manual cosine for vectors         | Tie    |

**Score: Mastra 4 — Oasis 0 — Tie 2**

> This is mastra's single largest moat — 30+ backends vs 2. Oasis's two backends are unusually complete (SQLite ships FTS5-based hybrid retrieval; Postgres ships pgvector HNSW), but the integration count gap is real and unlikely to close soon.

---

## Observability

| Feature                       | Mastra                                                                                              | Oasis                                                                                         | Winner |
|-------------------------------|-----------------------------------------------------------------------------------------------------|-----------------------------------------------------------------------------------------------|--------|
| **Tracing**                   | OpenTelemetry integration + 12 external adapters (Arize, Arthur, Braintrust, Datadog, Laminar, Langfuse, LangSmith, PostHog, Sentry, OTel Bridge, OTel Exporter, Mastra Storage) | `Tracer` / `Span` interfaces in root package (zero OTEL imports); `observer.NewTracer()` OTEL backend in satellite | Tie    |
| **Structured Logging**        | Console + custom logger support                                                                     | `slog`-based structured logging throughout core                                               | Tie    |
| **Execution Traces**          | Step-by-step visibility in Studio playground                                                        | `StepTrace` on every `AgentResult` — per-tool name, input, output, tokens, duration (no OTEL required) | Oasis |
| **Span Hierarchy**            | Per-agent + per-workflow spans; MCP tool calls individually spanned                                 | `agent.execute → agent.loop.iteration → llm.chat_with_tools → tool.execute`; `workflow.execute → workflow.step` | Tie |
| **Zero Overhead**             | Always-on instrumentation                                                                           | Nil-check skip — when no tracer configured, all span creation is skipped                      | Oasis  |
| **Cost Tracking**             | Via external adapters (Langfuse, Braintrust)                                                        | Built-in `observer/cost.go` per-model pricing map; auto-emitted as metric                     | Oasis  |
| **UI for traces**             | Studio trace viewer with per-span timing                                                            | None — pipe to Jaeger / Tempo / Grafana via OTEL                                              | Mastra |

**Score: Mastra 2 — Oasis 3 — Tie 2**

---

## Evals / Testing

| Feature                | Mastra                                                                       | Oasis                                                            | Winner |
|------------------------|------------------------------------------------------------------------------|------------------------------------------------------------------|--------|
| **Eval framework**     | `packages/evals` with scorers, score tracing, datasets, experiments          | None                                                             | Mastra |
| **Scorers**            | LLM-based, code-based, prebuilt                                              | N/A                                                              | Mastra |
| **Test utilities**     | No exported mock LLM (uses real eval pipeline)                               | Internal helpers (`callbackProvider`, `nopStore`) — not exported | Tie    |
| **Studio UI for evals**| Built-in                                                                     | N/A                                                              | Mastra |

**Score: Mastra 3 — Oasis 0 — Tie 1**

---

## Voice / Audio

| Feature              | Mastra                                                                                          | Oasis | Winner |
|----------------------|-------------------------------------------------------------------------------------------------|-------|--------|
| **TTS/STT/Realtime** | 17 providers — OpenAI Realtime, Azure, AWS Nova Sonic, Cloudflare, Deepgram, ElevenLabs, Gladia, Google, Gemini Live, Inworld, ModelsLab, Murf, PlayAI, Sarvam, Speechify, xAI Realtime | N/A   | Mastra |

**Score: Mastra 1 — Oasis 0**

---

## Auth & Channels

| Feature                          | Mastra                                                                                              | Oasis                                              | Winner |
|----------------------------------|-----------------------------------------------------------------------------------------------------|----------------------------------------------------|--------|
| **FGA / RBAC**                   | OpenFGA per-resource permission checks in `agent.generate()`, workflow `start()`, tool `execute()`, MCP tool calls (EE) | MCP bearer token only (`mcp/auth.go`)              | Mastra |
| **OAuth / JWT**                  | `auth/cloud` with OAuth + JWT middleware                                                            | N/A                                                | Mastra |
| **Messaging platform channels**  | `AgentChannels` — Slack, Discord with tool approval cards, thread history, inline media             | N/A                                                | Mastra |

**Score: Mastra 3 — Oasis 0**

---

## Deployment

| Feature              | Mastra                                                                              | Oasis                                                                     | Winner |
|----------------------|-------------------------------------------------------------------------------------|---------------------------------------------------------------------------|--------|
| **Self-hosted**      | Node.js HTTP server with auto-generated REST API + 5 server adapters (Express, Fastify, Hono, Koa, NestJS) | Standard Go binary; bring your own HTTP framework                | Tie    |
| **Serverless**       | Cloudflare Workers, Vercel, Netlify deployers                                       | N/A (Go binary, not V8-isolate compatible)                                | Mastra |
| **Managed**          | Mastra Cloud (beta)                                                                 | N/A                                                                       | Mastra |
| **Dev Playground**   | Built-in Studio at `localhost:4111` — chat, traces, workflow viz, evals             | N/A                                                                       | Mastra |
| **API Generation**   | OpenAPI spec + Swagger UI                                                           | N/A                                                                       | Mastra |
| **Container image size** | `node_modules` + runtime (typically 100MB+)                                     | Single static binary (~15–30MB); scratch Docker image possible            | Oasis  |
| **Cold start**       | 200ms–2s (V8 + module load)                                                         | <20ms (static binary)                                                     | Oasis  |

**Score: Mastra 4 — Oasis 2 — Tie 1**

---

## Developer Experience

| Aspect                    | Mastra                                                                                  | Oasis                                                                                  | Winner |
|---------------------------|-----------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------|--------|
| **Getting Started**       | `npm create mastra@latest` — interactive scaffold + `pnpm mastra dev` for Studio at localhost:4111 | `go get github.com/nevindra/oasis` + ~15 LOC in `main.go` + `go run .`             | Mastra |
| **Provider Setup**        | `"openai/gpt-4o"` string — zero config, auto key detection                              | Explicit constructor with base URL; `resolve.Provider` for config-driven setup         | Mastra |
| **Type Safety (tools)**   | Zod schemas — runtime validation + TS inference on `input.*`                            | Go generics `Tool[In, Out]` — compile-time safety + schema derived via `Erase`         | Tie    |
| **Type Safety (workflows)**| Zod `inputData` flows through `.then()` chain — strongly typed                          | `wCtx.Get(key)` returns `any` — requires cast                                          | Mastra |
| **Error Handling**        | Exceptions + `MastraError` with `ErrorCategory` / `ErrorDomain`                         | Typed `ErrLLM`, `ErrHalt`, `ErrSuspended`, `WorkflowError` — `errors.Is` / `errors.As` | Oasis  |
| **Config File**           | None — code + env only                                                                  | `oasis.toml` → env vars → defaults                                                     | Oasis  |
| **Debugging**             | Interactive Studio playground, `streamVNext` with step-by-step visibility               | Channel events + `StepTrace` timing, no UI                                             | Mastra |
| **Documentation**         | Polished external site (`mastra.ai`), examples, blog posts, community                   | Self-contained `docs/` (80+ files); `PHILOSOPHY.md` + `ENGINEERING.md` as first-class design docs; `cmd/mcp-docs` AI-assistant server | Tie |
| **Composability**         | Networks wrap agents, workflows separate                                                | Recursive — Networks contain Workflows, Workflows contain Networks; everything is `Agent` | Oasis |
| **Boilerplate**           | More — Zod schemas per tool, class instantiation, monorepo build setup                  | Less — interfaces are small, tools are functions                                       | Oasis  |
| **Hot reload**            | tsup+Vite ~2–5s rebuild + Node restart                                                  | None, but `go build` is ~0.3–1s cold                                                   | Mastra |
| **Versioning**            | `.changeset/` per-package (monorepo-scale)                                              | Single `CHANGELOG.md` with semver rules in `CLAUDE.md`                                 | Tie    |
| **MCP for AI assistants** | `--mcp cursor/windsurf/vscode` flag in `create-mastra`                                  | `cmd/mcp-docs` server with embedded docs                                               | Tie    |
| **License**               | Apache-2.0 (+ EE for RBAC, Cloud, Studio cloud)                                         | AGPL-3.0 + commercial; CLA required for contributors                                   | Mastra |
| **Learning Curve**        | Lower if you know TS/React                                                              | Lower if you know Go, higher overall                                                   | Mastra |

**DX verdict: Mastra 7 — Oasis 4 — Tie 4. Mastra wins decisively on onboarding, tooling, and type safety in workflows. Oasis wins on composability, structured errors, and minimal boilerplate.**

---

## Performance

| Aspect                    | Mastra                                                                                                | Oasis                                                                                          | Winner |
|---------------------------|-------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------|--------|
| **Concurrency Model**     | Node.js event loop — single-threaded compute, async I/O                                               | Goroutines — true OS-thread parallelism via M:N scheduler                                       | Oasis  |
| **Parallel Tool Calls**   | `Promise.all` — concurrent I/O but single-threaded compute on event loop                              | `dispatchParallel` — bounded worker pool (cap 10) with panic recovery (`agent/dispatch.go:151`) | Oasis  |
| **GC pause profile**      | V8 generational, stop-the-world ~5–50ms                                                               | Go concurrent tri-color, typically <1ms                                                         | Oasis  |
| **Memory Footprint**      | ~50–150MB baseline                                                                                    | ~5–20MB baseline                                                                                | Oasis  |
| **Cold Start**            | Slow (V8 + module resolution) — painful for serverless                                                | Sub-second startup, single static binary                                                        | Oasis  |
| **Streaming Overhead**    | WHATWG `ReadableStream` + AI SDK abstraction (v4/v5)                                                  | Direct channel writes, tuned 64-event buffer (`agent/stream_fwd.go:29`)                         | Oasis  |
| **Workflow Execution**    | Step-by-step with explicit `parallel()` groupings                                                     | DAG with automatic concurrent execution of independent steps (`workflow/exec.go:246`)           | Oasis  |
| **Rate Limiting**         | Not built-in — relies on provider-side limits                                                         | Proactive sliding-window RPM/TPM — prevents 429s (`ratelimit/ratelimit.go:76`)                  | Oasis  |
| **Batch Processing**      | N/A                                                                                                   | `BatchProvider` — ~50% cost reduction via Gemini batch                                          | Oasis  |
| **Response Caching**      | `ResponseCache` processor — SHA-256 keyed prompt→completion cache with TTL                            | N/A (provider-side Gemini cache only)                                                           | Mastra |
| **Embedding Caching**     | Global LRU cache (1000 entries) for repeated text                                                     | `embeddingCache` for semantic-trim hot path only                                                | Mastra |
| **Background Agents**     | Worker thread offload (`OrchestrationWorker`, `BackgroundTaskWorker`)                                 | `Spawn()` — goroutine-based, near-zero overhead                                                 | Oasis  |
| **Context compaction**    | Via Observational Memory reflection                                                                   | Automatic at rune threshold; three mechanisms (inline, per-turn, structured)                    | Oasis  |
| **Deployment Size**       | `node_modules` + runtime                                                                              | Single binary, ~15–30MB                                                                         | Oasis  |
| **Connection Pooling**    | Default `http.globalAgent` keep-alive                                                                 | Default `http.Client` keep-alive (`MaxIdleConns=100`)                                           | Tie    |

**Performance verdict: Mastra 2 — Oasis 12 — Tie 1.**

> Real benchmark numbers were not gathered for this comparison. The verdict is architectural: at scale (high concurrency, cold-start sensitive, streaming-heavy), the Go runtime advantages compound. For single low-latency requests where LLM API latency dominates, the gap shrinks toward zero.

---

## Overall Scorecard

| Category                      | Mastra  | Oasis | Tie |
|-------------------------------|---------|-------|-----|
| Agent Primitives              | 8       | 7     | 9   |
| Workflow / Orchestration      | 10      | 5     | 5   |
| Tool System                   | 5       | 5     | 10  |
| Memory                        | 13      | 7     | 4   |
| RAG Pipeline                  | 2       | 6     | 1   |
| LLM Providers                 | 2       | 4     | 3   |
| Streaming                     | 5       | 4     | 9   |
| Human-in-the-Loop             | 11      | 5     | 5   |
| Processor / Guardrails        | 3       | 3     | 2   |
| MCP Support                   | 4       | 3     | 1   |
| Storage                       | 4       | 0     | 2   |
| Observability                 | 2       | 3     | 2   |
| Evals / Testing               | 3       | 0     | 1   |
| Voice / Audio                 | 1       | 0     | 0   |
| Auth & Channels               | 3       | 0     | 0   |
| Deployment                    | 4       | 2     | 1   |
| Developer Experience          | 7       | 4     | 4   |
| Performance                   | 2       | 12    | 1   |
| **Total**                     | **89**  | **70**| **60** |

> Scorecard history:
> - **2026-02-21** (original): Mastra 23 / Oasis 48 / Tie 23 — understated mastra strengths.
> - **2026-05-21 (rev 1)**: Mastra 52 / Oasis 61 / Tie 35 — honest accounting of storage, memory, voice, channels, auth, evals.
> - **2026-05-21 (rev 2)**: Mastra 70 / Oasis 62 / Tie 38 — corrected Agent Primitives + Workflow after deep re-examination (mastra has `forEach`/`dowhile`/`dountil`, `.sleep()`, `bail()`, `.map()`, per-call callbacks, `DelegationConfig`, ~30-field `AgentConfig`).
> - **2026-05-21 (rev 3)**: Mastra 106 / Oasis 68 / Tie 43 — corrected Tool System, Memory, Streaming, Human-in-the-Loop after deeper code re-examination found significant under-counts on mastra: (a) full `BackgroundTaskManager` with three backpressure modes, FGA per-tool authorization, 6-step input coercion pipeline; (b) ~70 streaming chunk types (vs 16 in oasis), `MastraModelOutput` with ~25 promise accessors, multi-reader fan-out via `#bufferedChunks`; (c) four distinct HITL mechanisms including `requireApproval` tool approval gate, `agent.approveToolCall()`/`declineToolCall()` API, Studio approval cards UI, harness policy; (d) memory: 3-processor + ObservationalMemory engine architecture (not "5 composable processors"), dedicated ANN vector DB for semantic recall, MessageList AI-SDK-version normalization, runtime per-call memory override, ~25-promise accessor surface. Also corrected two factual errors that had previously credited Oasis: (i) injection-guard list is 11 phrases, not 80+; (ii) no embedding cache for semantic trim. Oasis retains genuine wins on `ask_user` (LLM-initiated pausing — only path of its kind), suspend TTL/budget caps, `ToolResultStore`/`read_full_result` paging, `ToolResult.Attachments` multimodal at tool level, MCP `ToolSearch` semantic discovery, per-turn `Compress`, three-layer compaction.
> - **2026-05-21 (post hooks+runoptions)**: Mastra 103 / Oasis 68 / Tie 45 — Oasis closed the "per-call callbacks" and "dynamic config" gaps via typed loop-control hooks (PrepareStep/OnIterationComplete/OnError) and the `RunOptions` struct passed via `ExecuteWith`. Both rows flipped from Mastra → Tie.
> - **2026-05-21 (post tool robustness)**: Mastra 99 / Oasis 69 / Tie 49 — Tool System row flipped three Mastra wins to Tie: (a) `ToolDefinition.OutputSchema` auto-derived via `DeriveSchema[Out]()` + opt-in `OutSchemaProvider` override; (b) structural input coercion in `core/coerce.go` (null→{}, stringified-JSON unwrap) — deliberately narrower than Mastra's 6-step pipeline; (c) `core.ToolPolicy` + `agent.WithToolPolicy` / `WithToolPolicyMatch` for per-tool timeout, retries with exponential backoff, and an opt-in `Retryable` convention. Also corrected a long-standing Tool System miscount: the table is 5/5/10/1 across 21 rows, not the previously reported 9/4/6/1 across 20 (the prior tally over-counted Mastra by 1 and under-counted Oasis and Tie by 1 each, with a missing N/A row reconciliation). Mastra still leads on `BackgroundTaskManager` durable async, `requireApproval` gates, FGA, per-tool OTel spans, runtime output validation/transform, and tool args streaming.
> - **2026-05-21 (post streaming+tools DX layer)**: Six rows flipped Mastra → Tie:
>   (a) Streaming convenience accessors via `Stream.Text()`/`ToolCalls()` and
>   `AgentResult.Text()`/`ToolCalls()`/`ToolResults()`/`Reasoning()` — synchronous
>   and streaming code use the same method names.
>   (b) Reasoning chunks via `EventReasoningStart`/`Delta`/`End`.
>   (c) Multi-reader fan-out via `Stream.Events()` + bounded replay (default 256)
>   with slow-subscriber drop policy.
>   (d) Framework-initiated suspend / (e) approval API via
>   `WithToolApproval(name, opts...)` — built on the new `ToolMiddleware` chain,
>   `EventToolApprovalPending` on the stream before prompting,
>   `DenyAskLLMToRevise` (default) and `DenyHalt` policies.
>   (f) Per-tool observability via auto-wired `OTelSpanMiddleware`
>   (applied when a `Tracer` is configured and not already in the user's chain).
> - **2026-05-21 (post streaming world-class overhaul)**: Mastra 93 / Oasis 70 / Tie 56 — Streaming category dropped from Mastra 11 / Oasis 3 / Tie 2 to Mastra 5 / Oasis 4 / Tie 9 across the seven commits on `migration/microkernel`:
>   (a) **Structured object streaming** flipped Mastra → Tie: `EventObjectDelta` snapshots backed by `core.PartialJSON` (a stdlib-only forgiving partial-JSON parser, property-tested across every byte prefix of well-formed JSON), `EventObjectFinish` with validated bytes, `EventElementDelta` per completed element for top-level array schemas, plus `oasis.StreamObjectAs[T]` / `oasis.ResultObjectAs[T]` free generic adapters that keep `Agent` / `Network` / `Workflow` monomorphic.
>   (b) **Per-stream observability** flipped Mastra → Tie: three-level span hierarchy `agent.execute → agent.iteration[N] → llm.generate` with attribute-rich spans (model, provider, input_tokens, output_tokens, finish_reason), plus `AgentResult.Iterations []IterationTrace` as a non-OTel surface.
>   (c) New row added — **Lifecycle envelope (Tie)**: every run now bracketed by `EventRunStart` / `EventRunFinish` with typed `FinishReason` enum (8 constants), `Warnings`, `ProviderMeta` on the finish event. Deprecates `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt` (all four collapsed into the envelope's `FinishReason`).
>   (d) **Output convenience accessors** row (already Tie) updated to reflect full parity — `AgentResult` and `Stream` both expose `Sources()` / `Files()` / `Warnings()` / `FinishReason()` / `ProviderMeta()` / `SuspendPayload()` / `Object()` / `Iterations()` alongside the previously-existing accessors. Same method names on both sync and streaming paths.
>   Cross-cutting infrastructure shipped alongside: native Gemini and OpenAI-compat providers populate `ChatResponse.FinishReason` and `ChatResponse.ProviderMeta`; `core.Sourced` opt-in interface (implemented by `HybridRetriever` and `GraphRetriever`) aggregates citations onto `AgentResult.Sources` automatically; `core.Warner` opt-in for providers and decorators.
>   Mastra still leads on: raw event-type count (~70 vs 23), tool-args streaming envelope, `BackgroundTaskManager` event family, WebSocket transport (OpenAI Realtime), and the `tripwire` chunk with `processorId` + automatic retry semantics.
> - **2026-05-22 (post typed HITL contracts)**: Mastra 91 / Oasis 70 / Tie 58 — HITL category dropped from Mastra 15/5/1 to Mastra 13/5/3 across two rows (Suspend payload typing, Resume data typing) via the typed `SuspendProtocol[Req, Resp]` shipped in spec #1 of the 6-spec HITL parity roadmap. The protocol value pins `Req`/`Resp` in one declaration; both the suspending site and the caller that resumes reference it; the compiler refuses to let either side disagree. Spec also adds a per-protocol `WithRenderResume` formatter so the LLM-visible resume message is natural language instead of raw JSON. `Agent`/`Workflow`/`Network` stay monomorphic. Untyped `Suspend(json.RawMessage)` and `(*ErrSuspended).Resume` are preserved as the escape hatch.
> - **2026-05-22 (post HITL stream event parity)**: Mastra 89 / Oasis 70 / Tie 60 — HITL category dropped from Mastra 13/5/3 to Mastra 11/5/5 across two rows ("Stream chunk integration", "Observability of suspended state") via spec #2 of the 6-spec HITL parity roadmap. Oasis now emits `EventToolCallSuspended`, `EventStepSuspended`, and `EventProcessorSuspended` mid-stream (before `EventIterationFinish`) so UIs can render a "human, please decide" surface without waiting for the run to finish. `EventRunFinish` also carries `Protocol` and `SuspendPayload` when `FinishReason == FinishSuspended`. `IterationTrace.FinishReason` lets callers walk `AgentResult.Iterations` to identify the suspending iteration; `AgentResult.SuspendProtocol` carries the typed-protocol tag; `Stream.Suspended()` / `Stream.SuspendedProtocol()` and `AgentResult.Suspended()` / `AgentResult.SuspendedProtocol()` are convenience shorthands on both sync and streaming paths.

---

## Unique to Oasis

- **`MaxIter` loop safety cap** — default 10 iterations, returns `ErrMaxIterExceeded`; Mastra loops have no built-in cap
- **True DAG workflow scheduling** — reactive (not BFS-wave) — each completion immediately unblocks dependents; Mastra is a linear `stepFlow` array with embedded compound nodes
- **Construction-time workflow validation** — duplicate names, missing deps, Kahn-algorithm cycle detection, unreachability warning (`workflow/workflow.go:799–851`)
- **`FromDefinition`** — JSON `{nodes, edges}` compiled to live `*Workflow` at runtime (`workflow/definition.go:21`)
- **Resource budgets at agent level** — `WithSuspendBudget(maxSnapshots, maxBytes)`, `WithMaxAttachmentBytes`, `WithMaxToolResultLen`, `WithMaxPlanSteps`, `WithMaxSteps`
- **Tool result paging** — `WithToolResultStore` offloads large tool results out-of-band
- **Three-layer compaction** — inline tool-result truncation, per-turn `Compress`, and `StructuredCompactor` with 9-section summary format
- **Auto context-management** — runs at rune threshold in the agent loop, no caller action needed (`agent/compress.go:41`)
- **Rate limiting decorator** — proactive sliding-window RPM + TPM
- **Batch processing** — `BatchProvider` / `BatchEmbeddingProvider` with native Gemini batch (~50% cost reduction)
- **Persistent Graph RAG** — ingestion-time LLM edge extraction, 8 typed relations, multi-hop BFS, hop decay (`rag/retriever.go:688`)
- **Contextual chunking** — LLM augments each chunk with document context before embedding
- **Cross-document graph edges** — LLM extracts semantic relations across document boundaries
- **Hybrid retrieval with RRF** — vector + FTS5 Reciprocal Rank Fusion
- **Parent-child chunking** — match small chunks, return parent context
- **Code execution sandbox** — `core.Sandbox` 1-method interface in core, richer `sandbox.Sandbox` + `Tools()` in `sandbox/` satellite, implementations external (e.g. `oasis-sandbox-ix`)
- **Live model catalog** — 87 platforms auto-fetched from models.dev with TTL caching
- **Background agents** — `Spawn()` / `AgentHandle` with atomic state machine (`State()` / `Done()` / `Await()` / `Sync()` / `Cancel()`)
- **`ask_user` tool** — `WithInputHandler(h)` lets the LLM autonomously decide when to request human input
- **Plan execution mode** — `WithPlanExecution()` lets the LLM batch parallel tool calls in one turn
- **Dynamic model/prompt/tools switching** — `WithDynamicModel(fn)` / `WithDynamicPrompt(fn)` / `WithDynamicTools(fn)` for per-invocation resolution
- **Typed loop-control hooks** — `PrepareStep`, `OnIterationComplete`, `OnError` with opaque decision structs (`Continue` / `Stop` / `InjectFeedback` / `Retry` / `RetryWithFeedback` / `HaltDecision`); compile-safe decision space
- **`RunOptions` per-call override struct** — distinct type for runtime overrides (model, memory, generation, hooks, processors, metadata); shallow-merge metadata; multi-tenant memory swap
- **`ExecuteWith` / `ExecuteStreamWith`** — per-call override entry points on `Agent` / `StreamingAgent`; `Execute(ctx, task)` ≡ `ExecuteWith(ctx, task, nil)`; back-compat via sibling interfaces `AgentWithOptions` / `StreamingAgentWithOptions`
- **Skills system** — `WithActiveSkills(...)` preload + `WithSkills(provider)` registers `skill_discover` / `skill_activate` tools
- **Deferred tool schemas** — `SchemaEnsurer` and `DeferredDefinitions` for lazy schema resolution
- **Semantic tool search over MCP** — `mcp/toolsearch.go`
- **User memory fact store** — LLM extraction, semantic dedup ≥0.85, confidence decay, supersession
- **Injection guard at memory layer** — `sanitizeFacts()` + `containsInjectionPattern()` blocklist (11 phrases, deliberately narrow to avoid false positives) (`memory/memory_orchestration.go:748–760`)
- **Persist backpressure** — bounded semaphore, lightweight fallback, 2-second drop
- **`PostToolProcessor`** — hook after each tool execution
- **Provider decorators** — composable `WithRetry` + `WithRateLimit`
- **Zero-overhead observability** — `Tracer`/`Span` interfaces in root package with no OTEL imports; `StepTrace` / `IterationTrace` / `LLMCallTrace` on every `AgentResult`
- **DOCX / PDF extraction** — built-in, no external services
- **MCP docs server** — `cmd/mcp-docs` serves framework docs to AI assistants
- **Typed structured-output adapters** — `oasis.StreamObjectAs[T](stream) <-chan T` and `oasis.ResultObjectAs[T](result) (T, error)` give compile-time-typed access to partial-object snapshots and final structured output without forcing `Agent` / `Network` / `Workflow` to be parameterized by output type
- **Forgiving partial-JSON parser** — `core.PartialJSON` accepts any byte prefix of a well-formed JSON document and returns the most-complete valid snapshot (closes open strings, drops incomplete tail values, terminates open containers); stdlib-only; property-tested
- **Streaming lifecycle envelope with typed `FinishReason`** — explicit `EventRunStart` / `EventRunFinish` / `EventIterationStart` / `EventIterationFinish` framing every run, with a typed `FinishReason` enum (`FinishStop` / `FinishToolCalls` / `FinishLength` / `FinishContentFilter` / `FinishHalted` / `FinishSuspended` / `FinishMaxIter` / `FinishError`) carried on the finish event alongside `Warnings` and `ProviderMeta`
- **Automatic citation aggregation** — `core.Sourced` opt-in interface; `HybridRetriever` and `GraphRetriever` implement it; the agent loop runtime-checks every tool dispatch and aggregates onto `AgentResult.Sources` without per-tool wiring
- **`IterationTrace` surface for non-OTel consumers** — per-iteration model / duration / LLMCallTrace / ToolCalls / Usage exposed on `AgentResult.Iterations`, matching what the `agent.iteration` and `llm.generate` OTel spans record
- **Typed HITL contracts** — `SuspendProtocol[Req, Resp]` value declared once and referenced by both suspend and resume sites; compile-time `Req`/`Resp` enforcement without making `Agent`/`Workflow`/`Network` generic; opt-in `WithRenderResume(func(Resp) string)` shapes the LLM-visible resume message; runtime string-tag check as a safety net for "wrong protocol" mistakes

## Unique to Mastra

- **Working Memory** — LLM-writable structured scratchpad (Markdown template or Zod schema), resource- or thread-scoped, with mutex-protected writes
- **Observational Memory** — 3,600-line observe→reflect engine with token-aware buffering, async compaction, idle/TTL triggers; the most sophisticated long-horizon memory primitive in either framework
- **`.sleep(ms)` and `.sleepUntil(date)` workflow primitives** — first-class durable sleep, maps to native sleep on Inngest/evented engines
- **`.map()` declarative data restructuring** — workflow step that restructures inter-step data without writing a custom step
- **`bail(result)` / `abort()` step exit mechanisms** — clean workflow exit with result OR explicit run cancellation; Oasis has only `Suspend`
- **End-to-end workflow type inference** — `TPrevSchema` generic threads through `.then()` chain; Zod-typed `inputData` flows without manual casts
- **`DelegationConfig` for sub-agent control** — `onDelegationStart` (modify/reject dispatch), `onDelegationComplete` (with `bail()`), `messageFilter` for parent→child message forwarding
- **`requestContextSchema` typed DI** — schema-validated runtime context
- **`generateLegacy()` / `streamLegacy()`** — explicit AI SDK v4 ↔ v5 migration boundary on a runtime version check
- **Persistent tracing context across suspend/resume** — spans link seamlessly across pauses
- **`streamUntilIdle()`** — keeps the outer stream open across all background task continuations
- **`sendSignal` / `subscribeToThread` / `pubsub`** — external message injection into a running thread stream
- **30+ storage backends** — Pinecone, Qdrant, Chroma, pgvector, Lance, Astra, Elasticsearch, OpenSearch, Vectorize, S3 Vectors, Upstash, Turbopuffer, Redis, MongoDB, DynamoDB, ClickHouse, Couchbase, DuckDB, MSSQL, libSQL, Cloudflare D1/KV/DO, Convex, DSQL
- **17 voice/audio providers** — OpenAI Realtime, Azure, AWS Nova Sonic, Cloudflare, Deepgram, ElevenLabs, Gladia, Google, Gemini Live, Inworld, ModelsLab, Murf, PlayAI, Sarvam, Speechify, xAI Realtime
- **MCP Client richness** — StreamableHTTP transport with SSE fallback, progress tracking, elicitation actions, resource/prompt listing, reconnection logic
- **Dev Playground / Studio** — full SPA: agent chat, trace viewer, workflow visualizer, evals, datasets, scorers, MCP browser
- **Evals framework** — `packages/evals` with scorers (LLM-based, code-based, prebuilt), score tracing, datasets, experiments
- **Cloud deployers** — Mastra Cloud, Vercel, Netlify, Cloudflare Workers
- **Server adapters** — Express, Fastify, Hono, Koa, NestJS
- **Model Router** — 2,436 models / 81 providers via single `"provider/model"` string with auto-key detection
- **Response Cache processor** — SHA-256 keyed prompt→completion cache with TTL
- **Embedding LRU cache** — global, 1000 entries
- **OpenFGA RBAC (EE)** — per-resource permission checks in agent/workflow/tool/MCP layers
- **AgentChannels** — Slack / Discord integration with tool approval cards, thread history, inline media
- **Browser control** — `browser` field with `MastraBrowser` (Playwright)
- **A2A / ACP protocol** — `packages/acp` for agent-to-agent communication protocol
- **Time-travel workflow execution** — `Run._timeTravel()` re-runs from any prior step with modified inputs (`workflow.ts:4257`)
- **Evented workflows** — `EventedExecutionEngine` with `schedule: {cron, timezone}` auto-triggering; Inngest integration
- **Thread cloning** — deep-copy threads with resource remapping
- **REST API for memory** — `packages/server/src/server/handlers/memory.ts`
- **Multi-part message content** — tool-invocation, tool-result, reasoning, file parts with AI SDK v4/v5/v6 compatible serialization
- **`create-mastra` CLI** — interactive project scaffold with LLM choice, components, IDE MCP setup
- **Processor workflows** — input/output/error processors can be full processor workflows (chains of steps), not just individual processors
- **`TripWire` halt-with-retry** — processor signal that aborts the stream with a `tripwire` chunk and `processorId`, supports automatic retry up to `maxProcessorRetries`

---

## Persona-Based Picks

| You are…                                          | Pick    | Why                                                                                    |
|---------------------------------------------------|---------|----------------------------------------------------------------------------------------|
| TypeScript developer prototyping                  | Mastra  | Scaffold + Studio + Zod inference — fastest path to a running agent with visual debug. |
| Go backend team building production microservices | Oasis   | Single static binary, no Node runtime, structured errors, zero SDK deps in core.       |
| Polyglot evaluating both                          | Either  | Concepts map 1:1 — match the language to the team. Don't fight the language tide.      |
| Researcher iterating on agent design              | Mastra  | Studio replay + evals/scorers + visual trace inspection.                               |
| Edge-deployed agent (Cloudflare Workers, Vercel)  | Mastra  | Oasis cannot run on V8 isolates. Hard constraint.                                      |
| Cost-sensitive batch workload                     | Oasis   | `BatchProvider` + Gemini batch — ~50% LLM cost reduction.                              |
| High-concurrency (1000+ simultaneous agents)      | Oasis   | Goroutines = ~8KB each across cores vs. one event loop in mastra.                      |
| Privacy-sensitive / on-prem                       | Oasis   | Fewer dependencies, AGPL forces openness, no telemetry in CLI.                         |
| Compliance / RBAC requirements                    | Mastra  | OpenFGA per-resource gating is built in (EE).                                          |
| Voice / audio agents                              | Mastra  | 17 providers vs. zero.                                                                 |

---

## Summary

**Mastra** is a batteries-included TypeScript ecosystem optimized for breadth and visual DX. The Studio playground, 30+ storage backends, 17 voice providers, evals framework, and cloud deployers make it the obvious choice for TS shops that want to move fast with maximum out-of-the-box integration. The cost is operational complexity (pnpm + Turborepo + tsup + Vite + monorepo build) and a 50–150MB runtime footprint with painful cold starts on serverless.

**Oasis** is a kernel-focused Go framework optimized for runtime efficiency and deployment simplicity. The performance verdict is decisive — true parallelism, sub-second cold starts, channel-based streaming, automatic DAG concurrency, proactive rate limiting, three-layer compaction, batch processing. On primitives where both frameworks ship the feature, oasis often has the deeper engineering: graph RAG with persistence, hybrid retrieval with RRF, contextual chunking, sandbox isolation, injection guards in the memory layer. The cost is a missing Studio UI, fewer storage backends, no voice, and a steeper Go-idiom learning curve for non-Go teams.

The frameworks have converged on near-identical primitive vocabularies. **The choice is almost entirely about your team's language and your runtime constraints, not feature gaps.**

---

## Gap Assessment for Oasis

Addressable DX and capability gaps, in priority order:

1. **Dev playground / Studio UI** — the single largest DX gap. Even a minimal trace + chat viewer would close most of the iteration-speed delta with Mastra. (Could be a separate satellite serving over HTTP from `StepTrace` data.)
2. **Working memory primitive** — LLM-writable structured scratchpad (Markdown template or Go struct schema) is becoming table stakes for long-horizon agents.
3. **Workflow type safety** — replace `wCtx.Get(key) any` with a generic `wCtx.Get[T](key)`; consider Zod-equivalent schema validation between steps so step output → next step input is type-checked end-to-end.
4. **`Sleep` / `SleepUntil` workflow primitives** — durable sleep as a first-class step type unlocks scheduled workflows and pairs naturally with the existing `ratelimit/` budget logic.
5. **`Bail(result)` clean step exit** — currently a step can succeed, error, or suspend. Adding `Bail(result)` for "stop the workflow with this final result" matches a common pattern.
6. **Declarative data mapping** — a `Map(...)` step option that restructures inter-step data without writing a closure (analogous to mastra's `.map()`).
7. **More provider packages** — first-class Anthropic, OpenAI, Groq packages (alongside `openaicompat`) would reduce friction; the live catalog in `provider/catalog` is a great foundation.
8. **Response cache processor** — SHA-256 keyed prompt→completion cache analogous to mastra's `ResponseCache` would benefit repetitive workloads.
9. **Evals framework** — even a minimal scorer/dataset harness would unlock systematic quality measurement.
10. **More storage backends** — Pinecone or Qdrant adapter would cover the most common managed vector store ask.
11. **Project scaffold CLI** — `oasis init` to generate `main.go`, `oasis.toml`, and example tool/workflow.
12. **Durable suspend persistence** — workflow-level suspend is in-process only; a `Store`-backed snapshot option would close the gap with mastra's process-restart-safe pause/resume.
13. **Persistent tracing context across suspend/resume** — tracing currently resets on resume; carrying spans across would help long-running workflows.

> ✅ **Closed in 2026-05-21 (post hooks+runoptions):** Per-call agent callbacks (PrepareStep/OnIterationComplete/OnError) shipped with typed decision returns; per-call dynamic options via `RunOptions` + `ExecuteWith` covers the DynamicArgument<T> use case for ~20 fields.
