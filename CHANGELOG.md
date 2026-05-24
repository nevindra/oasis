# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

(planned 0.17.0)

### Added

#### Architecture

- Restructured the repository into a hybrid architecture. The
  user-facing import `github.com/nevindra/oasis` remains a curated umbrella
  that re-exports protocol types and the most common constructors as type
  aliases and `var`-bound functions; the implementation is split across
  focused subpackages.
  - New leaf package `github.com/nevindra/oasis/core` holds protocol types
    and interfaces. It depends on nothing else inside `oasis` — enforced
    by `core/leaf_test.go`, which walks `core/*.go` and asserts no file
    imports another `github.com/nevindra/oasis/*` package.
  - Primitives reorganised into focused public subpackages: `agent`,
    `workflow`, `network`, `compaction`, `guardrail`, `ratelimit`, `memory`,
    `skills`, `processor`, `provider/{catalog,resolve}`.
  - Heavy or optional-dep code lives in subpackages: `mcp`, `store/sqlite`,
    `store/postgres`, `provider/gemini`, `provider/openaicompat`,
    `observer`, `ingest`, `sandbox`, `rag`. All ship in a single root
    `go.mod` — Go 1.17+ lazy module loading keeps pgx/OTEL/PDF/Docker out
    of downstream builds that only import the umbrella.
  - Store-capability interfaces (`KeywordSearcher`, `GraphStore`,
    `BidirectionalGraphStore`, `DocumentGetter`, `DocumentMetaLister`) and
    `CheckpointStore` / `IngestCheckpoint` moved to `core/` so subpackages
    can implement them without cross-package dependencies.

#### Resource-budget API (replaces 7 WithMax* options)

- **`agent.Limits` struct + `WithLimits(Limits) AgentOption`.** One typed
  sub-config replaces the seven `WithMax*` knobs (`MaxIter`, `MaxSteps`,
  `MaxPlanSteps`, `MaxParallelDispatch`, `MaxAttachmentBytes`,
  `MaxToolResultLen`, `MaxSuspendSnapshots`, `MaxSuspendBytes`). All
  fields are optional — zero values keep defaults; calling `WithLimits`
  multiple times merges non-zero fields. Re-exported as `oasis.Limits` /
  `oasis.WithLimits`.
- **`agent.Unbounded` sentinel** (value `-1`). Preserves the old
  `WithMaxSteps(0) = unbounded` semantics now that `0` means "use the
  default 100". Re-exported as `oasis.Unbounded`.
- **`RunOptions.Limits *Limits`** — per-call mirror of the
  construction-time option. Also exposes `MaxParallelDispatch`,
  `MaxSuspendSnapshots`, `MaxSuspendBytes` per-call (previously
  construction-only). Negative values rejected with typed
  `RunOptionsError`; `MaxSteps == Unbounded` is the sentinel for "no
  cap" and is valid.
- **`(*LLMAgent).Limits() Limits`** (promoted from the embedded
  `internal/runtime.Runtime`) — getter for the agent's current budget,
  intended for partial per-call overrides:
  ```go
  lim := ag.Limits()
  lim.MaxIter = 5
  ag.Execute(ctx, task, agent.WithOverrides(&agent.RunOptions{Limits: &lim}))
  ```

#### HITL stream event parity

- New `StreamEventType` constants for mid-stream suspension:
  `EventToolCallSuspended`, `EventStepSuspended`, `EventProcessorSuspended`.
  Emitted before the iteration finish event so UIs can render a "human,
  please decide" card in real time instead of waiting for `EventRunFinish`.
  Re-exported from `oasis.go`.
- New `StreamEvent` fields `Protocol string` and `SuspendPayload
  json.RawMessage`. Populated on the three new mid-stream events, on
  `EventRunFinish` when `FinishReason == FinishSuspended`, and reserved
  for future use on `EventToolApprovalPending`. Both use `omitempty` so
  existing JSON consumers see no shape change for non-suspend events.
- New `IterationTrace.FinishReason FinishReason` field. Lets callers
  walking `AgentResult.Iterations` identify the suspending iteration (or
  any other terminal reason) without external bookkeeping.
- New `AgentResult.SuspendProtocol string` field. Carries the typed
  protocol's tag for suspended runs; empty for untyped
  `Suspend(json.RawMessage)` callers.
- New convenience methods: `AgentResult.Suspended() bool`,
  `AgentResult.SuspendedProtocol() string`, `Stream.Suspended() bool`,
  `Stream.SuspendedProtocol() string`. The `Stream` accessors block on
  completion (same semantics as the existing `SuspendPayload()`
  accessor).

#### Typed HITL contracts

- New `agent.SuspendProtocol[Req, Resp]` value (re-exported as
  `oasis.SuspendProtocol`) with constructor
  `NewSuspendProtocol[Req, Resp](name)` and methods `Suspend(Req)`,
  `PayloadFrom(*ErrSuspended) (Req, error)`, `Resume(*ErrSuspended, ctx,
  Resp)`, `ResumeStream(*ErrSuspended, ctx, Resp, ch)`,
  `WithRenderResume(func(Resp) string)`, and `Name()`. Compile-time
  contract between the suspending site and the caller that resumes —
  wrong payload or response type fails the build.
- Untyped `Suspend(json.RawMessage)` and `(*ErrSuspended).Resume` remain
  as the escape hatch. `Suspend` and `ErrSuspended` are now re-exported
  on the umbrella package (long-standing gap fixed).

#### Streaming v1

- **Lifecycle envelope:** every run now starts with `EventRunStart` and
  ends with `EventRunFinish` carrying `FinishReason`, `Warnings`, and
  `ProviderMeta`. Iterations are bracketed by
  `EventIterationStart`/`Finish`.
- **Structured object streaming:** when `WithResponseSchema` is
  configured, the loop emits `EventObjectDelta` snapshots of partial
  JSON and `EventObjectFinish` with the final validated bytes. Top-level
  array schemas additionally emit one `EventElementDelta` per completed
  element.
- **Typed adapters:** `oasis.StreamObjectAs[T](stream)` returns a typed
  channel of partial-object snapshots; `oasis.ResultObjectAs[T](result)`
  decodes the final object. Generic free functions — no contagion of
  generics through `Agent` / `Network` / `Workflow`.
- **Result-accessor parity:** `AgentResult` and `Stream` gain
  `FinishReason`, `Sources`, `Files`, `Warnings`, `ProviderMeta`,
  `SuspendPayload`, `Object`, `Iterations`. Same method names on both
  paths, so synchronous and streaming code share signatures.
- **Per-stream observability:** new `agent.iteration` and `llm.generate`
  OTel spans under the existing `agent.execute` root, populated with
  model / temperature / max-tokens / input-tokens / output-tokens /
  finish-reason attributes. `AgentResult.Iterations` exposes the same
  data without OTel.
- **`core.Sourced` / `core.Warner`:** opt-in interfaces for tools,
  retrievers, and providers to declare citations and non-fatal warnings.

#### Stream wrapper

- **`oasis.StartStream(ctx, agent, task)`** — multi-reader stream
  with blocking accessors (`Text()`, `ToolCalls()`, `ToolResults()`,
  `Reasoning()`, `Usage()`, `Result()`), live subscription via
  `Events()`, and filtered callbacks (`OnTextDelta`, `OnReasoningDelta`,
  `OnToolCall`, `OnToolResult`, `OnEvent`). Bounded ring-buffer replay
  (default 256 events, configurable via `RunOptions.StreamReplayLimit`).
  Slow subscribers receive a `subscriber-dropped` warning and are
  dropped — they cannot stall the agent. The single-reader channel
  kernel (`ExecuteStream`) is unchanged.
- **`AgentResult` convenience accessors.** `Text()`, `Reasoning()`,
  `ToolCalls()`, `ToolResults()`, `LastStep()`, `StepByTool(name)` —
  pure functions over existing fields; identical shapes to the `Stream`
  accessors.
- **Stream event types.** `EventReasoningStart`/`Delta`/`End` (provider
  incremental reasoning), `EventHalt` (processor halts), `EventError`
  (terminal failures), `EventStreamWarning` (replay-truncated /
  subscriber-dropped), `EventToolApprovalPending` (approval gate).
  `EventThinking` remains; deprecated when providers port to the
  triplet.

#### Tool subsystem config (`ToolConfig`)

- **`agent.ToolConfig` + `agent.WithToolConfig(ToolConfig) AgentOption`.**
  One typed sub-config replaces the four previous tool-subsystem knobs
  (`WithToolPolicy`, `WithToolPolicyMatch`, `WithToolApproval`,
  `WithToolMiddleware`). Fields: `Policies map[string]core.ToolPolicy`,
  `PolicyMatchers []ToolPolicyMatcher`, `Approvals []ApprovalConfig`,
  `Middleware []core.ToolMiddleware`. Calling `WithToolConfig` multiple
  times merges field-by-field.
- **`agent.Approval(toolName, opts...) ApprovalConfig`** helper for
  populating `ToolConfig.Approvals`. Per-tool prompt customization via
  `agent.ApprovalPrompt(fn)`.
- **Tool middleware chain.** Built-in middlewares:
  `LoggingMiddleware`, `TimingMiddleware`, `TransformMiddleware`,
  `OTelSpanMiddleware` (auto-applied when a `Tracer` is configured and
  not already in the user's chain). Innermost-first ordering matches
  `net/http`. `core.ChainToolMiddleware(mws...)` composes a slice into
  a single middleware.
- **Framework-enforced tool approval** pauses tool execution for human
  approval via the configured `InputHandler`. Composes with logging,
  tracing, policy, and any custom middleware. Approve/deny decisions
  via `InputResponse.Value`; `DenyAskLLMToRevise` (default) returns an
  error `ToolResult` so the LLM can adapt, `DenyHalt` halts the run
  with `*core.ErrHalt`. Outermost layer of the chain — retries do not
  re-prompt. Emits `EventToolApprovalPending` on the stream before
  prompting.
- Re-exports on the umbrella: `oasis.WithToolConfig`, `oasis.ToolConfig`,
  `oasis.Approval`, `oasis.ApprovalPrompt`.

#### Tool robustness layer

- **`core.ToolPolicy`** (per-tool `Timeout`, `Retries`, `RetryDelay`,
  `MaxRetryDelay`, `RetryOn`).
- **`core.Retryable` interface, `core.RetryableError(err)` wrapper,
  `core.DefaultRetryOn(err)` predicate, `core.BackoffDelay(base, max,
  attempt)` helper.**
- **`core.OutSchemaProvider`** opt-in interface — tools may publish a
  custom output JSON Schema that overrides the schema derived from
  `Out` by reflection.
- **`core.ToolDefinition.OutputSchema json.RawMessage`** field,
  populated by `core.Erase` / `core.EraseStreaming` via
  `DeriveSchema[Out]()` (or the override). Provider implementations
  decide whether to forward this to the LLM.
- **`core.ToolRegistry.IsStreamingTool(name) bool`** lookup.
- **Per-tool policies via `ToolConfig.Policies` (exact name) and
  `ToolConfig.PolicyMatchers` (prefix/glob).** ServeMux-style
  precedence: exact name first, then matchers in registration order.
  Streaming tools bypass the policy wrapper entirely (with a one-shot
  `slog.Warn` if a policy was registered for one).
- Umbrella re-exports: `oasis.ToolPolicy`, `oasis.Retryable`,
  `oasis.RetryableError`, `oasis.DefaultRetryOn`,
  `oasis.OutSchemaProvider`.

#### Typed tool schemas

- **`core.ToolMeta` struct** — `Name` + `Description` fields, returned
  by `Tool.Definition()`.
- **`core.SchemaProvider` interface** — implement `JSONSchema()
  json.RawMessage` on an input type to bypass reflection (recursive
  shapes, `oneOf`, provider-specific schemas).
- **`core.DeriveSchema[T any]() json.RawMessage`** — exported helper
  that builds a JSON Schema from any Go type by reflection.
- Struct-tag vocabulary recognised by the reflector: `json:"name,omitempty"`
  (stdlib), `describe:"..."`, `enum:"a,b,c"`.
- Umbrella re-exports: `oasis.ToolMeta`, `oasis.SchemaProvider`,
  `oasis.DeriveSchema`.

#### Other additions

- **`core.ToolResultStore` interface** + default in-memory implementation
  (`core.NewInMemoryToolResultStore`) for paging large tool results.
  Auto-enabled with 10 MiB total cap, 5-minute TTL per entry, and
  per-store entry cap (`core.WithToolResultMaxEntries`); opt out with
  `WithToolResultStore(nil)`.
- **Transparent tool-result chunking.** Oversized tool results are now
  split into sequential `tool`-role messages by the loop itself —
  callers no longer need a separate retrieval tool. (Replaces the old
  `read_full_result` built-in.)
- **`core.Sandbox` interface** — `Close() error` contract; replaces the
  old `WithSandbox(any)` signature.
- **`core.CompactRequest.Scope`** field with `core.ScopeFull` and
  `core.ScopeToolResultsOnly` constants.
- **`AgentHandle.Sync()`** — explicit drain for callers that previously
  relied on `State()` to block until completion.
- **`core.EventMaxIterReached`** stream event emitted before forced
  synthesis.
- New options: `WithToolResultStore`, `WithToolResultMaxBytes`,
  `WithToolResultTTL`.
- `StreamingTool[In, Out]` generic interface for type-safe streaming
  tool authoring. Bridge via `EraseStreaming[In, Out]` to register as a
  `StreamingAnyTool`.
- `NewAttachment`, `NewAttachmentFromURL`, `NewAttachmentFromBase64`
  constructors.
- `Role` type with `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool`
  constants.

#### Agent foundation redesign

- **`core.Agent.Execute(ctx, task, ...core.RunOption)`** — the single
  entry point for every `core.Agent` implementation (LLMAgent,
  Network, Workflow, custom). Streaming, deadlines, and per-call
  overrides are passed as variadic `RunOption` values instead of a
  bag of `ExecuteStream` / `ExecuteWith` / `ExecuteStreamWith` /
  `StartStream` / `StartStreamWith` method variants.
- **`core.RunOption` type + helpers.** `core.WithStream(ch chan<-
  core.StreamEvent)`, `core.WithDeadline(t time.Time)`, plus
  `agent.WithOverrides(*agent.RunOptions)` for the existing struct of
  per-call overrides. `core.ApplyRunOptions(opts...)` returns the
  resolved `core.RunConfig`; agent implementations call this once at
  the top of `Execute`.
- **`agent.Subscribe(ctx, ag, task, opts...) *Stream`** — multi-reader
  stream wrapper accepting any `core.Agent`. Replaces both
  `oasis.StartStream` and the older `StartStreamWith`; the previous
  symbols are kept as aliases on the umbrella.
- **Constructor renames** (umbrella aliases preserved):
  - `agent.NewLLMAgent` → `agent.New` (umbrella keeps `oasis.NewLLMAgent`
    and adds `oasis.NewAgent`).
  - `network.NewNetwork` → `network.New` (umbrella keeps
    `oasis.NewNetwork`).
  - `workflow.NewWorkflow` → `workflow.New` (umbrella keeps
    `oasis.NewWorkflow`).

#### Network orchestration primitive

- **`network.New(name, desc, router, opts ...network.Option)`** — the
  Network is now the home for multi-agent coordination. Children are
  declared via `network.WithChildren(agents ...core.Agent)`; the
  legacy `agent.WithAgents` option is removed.
- **Supervision policies.** `network.WithSupervisor(policy)` and
  `network.WithSupervisorFor(name, policy)` attach restart / fallback
  / quorum / circuit-breaker / chain behavior. Built-ins:
  `network.RestartOnFail(n)`, `network.Fallback(primary, fallback)`,
  `network.Quorum(threshold, agents...)`,
  `network.CircuitBreaker(threshold, window)`, `network.Chain(agents...)`.
  Plus the `network.ErrCircuitOpen` sentinel.
- **Runtime membership.** `(*Network).AddAgent(a)` and
  `(*Network).RemoveAgent(name)` for thread-safe live mutation; new
  `(*Network).Topology()` returns a read-only graph snapshot.
- **LLM-driven sub-agent spawning moves to the Network layer.**
  `network.WithDynamicSpawning(SpawnPolicy)` replaces the agent-level
  `WithSubAgentSpawning` / `MaxSpawnDepth` / `DenySpawnTools` options.

#### Workflow Plan C cleanup

- **Step output access via `result.Steps[name].Output`.** The mutable
  `WorkflowContext` bag is gone from the public API; the framework
  owns step lifecycle.
- **`workflow.ToolStep` removed.** Compose a single-tool `LLMAgent`
  and wrap it with `workflow.AgentStep` instead — same behavior, one
  fewer step kind to learn.

#### Plan D cleanup

- **Massive umbrella trim** — `oasis.go` shrank from 845 LOC to 137.
  The umbrella now re-exports only the most common types and
  constructors. Hook types (`OnError`, `OnIterationComplete`,
  `PrepareStep`), event/finish-reason constants, workflow step
  helpers (`Step`, `AgentStep`, `ForEach`, `When`, `After`,
  `InputFrom`, `OutputTo`, `IterOver`), guardrail constructors,
  compaction symbols, store-capability interfaces, processor types,
  and many more must be imported from their subpackages (`oasis/agent`,
  `oasis/core`, `oasis/workflow`, `oasis/guardrail`, `oasis/compaction`,
  `oasis/skills`, etc.) directly. Subpackage import paths are
  unchanged.
- **Skills constructors return the interface.** `skills.FromDir(dirs
  ...string) SkillProvider` (replaces `NewFileSkillProvider`),
  `skills.Chain(providers ...SkillProvider) SkillProvider` (replaces
  `ChainSkillProviders`), `skills.Builtin() SkillProvider` (replaces
  `NewBuiltinSkillProvider`). The concrete types
  (`FileSkillProvider`, `ChainedSkillProvider`,
  `BuiltinSkillProvider`) are no longer exported.
- **Provider middleware.** `provider.Middleware = func(core.Provider)
  core.Provider` + `provider.Chain(mws ...Middleware) Middleware`.
  Used to compose retry, rate-limit, caching, etc. into a single
  provider stack. `agent.Middleware`, `agent.Chain`, and
  `agent.WithMiddleware(mws ...)` are the agent-facing wiring;
  built-in values `agent.RetryMiddleware` and
  `ratelimit.RateLimitMiddleware` replace the older
  `agent.WithRetry` / `ratelimit.WithRateLimit` wrappers (kept as
  deprecated convenience constructors).
- **`core.MemoryItemStore` interface** — canonical location for the
  store contract; `memory.ItemStore` is kept as a deprecated alias.

#### Surface consolidation (Processors & Hooks)

- **`agent.Processors` struct + `agent.WithProcessors(Processors)
  AgentOption`.** One typed sub-config replaces `WithPreProcessors`,
  `WithPostProcessors`, and `WithPostToolProcessors`. Fields are
  optional; multiple calls merge.
- **`agent.Hooks` struct + `agent.WithHooks(Hooks) AgentOption`.**
  One typed sub-config replaces `WithPrepareStep`,
  `WithOnIterationComplete`, and `WithOnError`. Fields are optional;
  multiple calls merge.
- Re-exported: `oasis.Processors`, `oasis.Hooks`,
  `oasis.WithProcessors`, `oasis.WithHooks`.

#### Prompt caching (default-on)

- **Anthropic & OpenAI-compat prompt caching wired by default.** The
  loop stamps ephemeral cache breakpoints on the system prompt and on
  the most recent user/tool message each iteration; cache hits flow
  through to provider requests automatically. No code change required
  to benefit.
- **New `core.Usage` fields:**
  - `CachedTokens int` — tokens served from the provider's prompt
    cache (read hit). Populated by Anthropic native and
    OpenAI-compatible providers when the upstream reports it.
  - `CacheCreationTokens int` — tokens written into the cache
    (warming cost). Anthropic-only.
- **`core.ChatMessage.CacheCheckpoint bool`** — instructs cache-aware
  providers to stamp an ephemeral-cache breakpoint at this message.
  Ignored by providers without cache support.
- **`agent.WithoutPromptCaching() AgentOption`** — opt-out for cost
  or debugging. Re-exported as `oasis.WithoutPromptCaching`.

### Changed

- **BREAKING — `Tool` interface reshaped from bundle to atomic.** One
  implementation now describes exactly one operation. New types:
  - `AnyTool`: type-erased atomic interface (`Definition() / ExecuteRaw(ctx, args)`).
    Consumed by the loop and the registry.
  - `Tool[In, Out any]`: type-safe generic authoring interface.
  - `Erase[In, Out](Tool[In, Out]) AnyTool`: adapter for registration.
  - `StreamingAnyTool`: optional streaming capability replacing the old
    `StreamingTool`.

  `WithTools` now takes `...AnyTool`. `ToolRegistry.Add` now takes
  `AnyTool`. Bundle-style tools (one impl exposing N definitions) must
  be split into N atomic implementations. Built-in tools migrated:
  `tools/http` (now `oasis.Tool[FetchInput, string]`), `tools/data`
  (split into 4 atomic tools), skill tools (split into 4), sandbox
  tools, MCP wrappers.

- **BREAKING — `Tool` interface shrunk (typed tool schemas).**
  - Removed `Name() string`. The tool's name now lives in the
    `ToolMeta` returned by `Definition()`.
  - `Definition() ToolDefinition` → `Definition() ToolMeta`. Authors
    return name + description only; the JSON Schema for `In` is derived
    from the Go type by reflection inside `Erase`.

- **BREAKING — Schema-shape errors now panic at registration.** Previously
  failed silently at LLM-call time. They now **panic** at
  `Erase[In, Out]()` with a descriptive message (field path, offending
  Go type, supported alternatives).

- **BREAKING — `Tool.Execute` errors now propagate as Go errors from the
  erased adapters.** Previously `core.Erase` swallowed the Go error from
  `tool.Execute(...)` into `ToolResult.Error` and returned `(result, nil)`.
  It now returns `(result, err)` so the new dispatch policy wrapper can
  inspect typed errors (`Retryable`, `net.Error.Timeout()`,
  `context.DeadlineExceeded`). The LLM-visible result is unchanged
  because `agent.toolResultToDispatch` already prioritizes the Go error
  path. External `AnyTool` implementers that read `ToolResult.Error` are
  unaffected. Implementers that re-wrap erased tools and previously
  assumed a nil error return from `ExecuteRaw` must now propagate or
  absorb the typed error. Argument-unmarshal errors and result-marshal
  errors continue to return `(result, nil)`.

- **BREAKING — `AgentHandle.State()` no longer blocks.** Callers that
  read `Result()` after `State().IsTerminal()` must insert `h.Sync()`
  between the two. Migration hint: `grep -n 'State().IsTerminal'
  your-project/` and add `Sync()` calls.

- **BREAKING — `oasis.WithHistory` and the `github.com/nevindra/oasis/history`
  subpackage are removed.** All history, recall, compaction, compression, and
  semantic-trim options now live on `memory.Option` and are passed through the
  single `oasis.WithMemory(...)` entry point. See the "Memory system redesign"
  entry below for the full migration.

- **BREAKING — `WithSandbox(any)` is now `WithSandbox(core.Sandbox)`.**
  The `sandbox/` subpackage's existing type already implements the new
  `core.Sandbox` interface — no changes needed. Custom sandbox types
  must implement `Close() error`.

- **BREAKING — `AgentTask.Context map[string]any` removed.** Use the
  typed `ThreadID`/`UserID`/`ChatID` fields. App-defined metadata moves
  to `AgentTask.Extra`. The `ContextThreadID` / `ContextUserID` /
  `ContextChatID` constants and `TaskThreadID()` / `TaskUserID()` /
  `TaskChatID()` accessors are deleted.

- **BREAKING — `Attachment.Base64` field removed.** Construct via
  `NewAttachment` / `NewAttachmentFromURL` / `NewAttachmentFromBase64`.
  `InlineData()` is now infallible and returns `Data` directly.

- **BREAKING — `ChatMessage.Role` switches from `string` to typed
  `Role`.** String literals still compile for comparisons; direct
  assignments of `msg.Role` to a `string` variable need an explicit
  `string()` conversion. New code should use `RoleSystem` / `RoleUser`
  / `RoleAssistant` / `RoleTool`.

- **BREAKING — `AgentCore.Drain()` and `AgentMemory.Drain()` renamed to
  `Close() error`.** Returns nil today; the error return is reserved
  for future flush failures.

- **BREAKING — `Erase` moved from `github.com/nevindra/oasis/tool` to
  `github.com/nevindra/oasis/core`** next to the `Tool` and `AnyTool`
  types it bridges. The `tool/` subpackage has been deleted. The
  umbrella API `oasis.Erase` is unchanged — anyone using the curated
  surface sees no break. Only direct importers of `oasis/tool` need to
  switch to `oasis/core` or `oasis.Erase`.

- **BREAKING — Compaction implementation moved to subpackage
  `github.com/nevindra/oasis/compaction`.** The `Compactor` interface
  and `CompactRequest` / `CompactSection` / `CompactResult` types
  remain in the root `oasis` package — they are the kernel contract
  that `oasis.WithCompaction` consumes.
  - Symbols moved: `StructuredCompactor`, `NewStructuredCompactor`,
    `BuildCompactPrompt`, `EstimateContextTokens`, `StripMediaBlocks`,
    `CompactableToolNames`, `ErrEmptyMessages`, `ErrNoProvider`,
    `ErrSummaryParseFailed`.
  - Migration:
    ```go
    // Before
    c := oasis.NewStructuredCompactor(provider)
    // After
    import "github.com/nevindra/oasis/compaction"
    c := compaction.NewStructuredCompactor(provider)
    // oasis.CompactRequest, oasis.CompactResult, oasis.WithCompaction still in root.
    ```

- **BREAKING — Guardrails moved to subpackage
  `github.com/nevindra/oasis/guardrail`.** `InjectionGuard`,
  `ContentGuard`, `KeywordGuard`, `MaxToolCallsGuard` and their
  constructors/options.
  - Migration:
    ```go
    // Before
    guard := oasis.NewInjectionGuard()
    // After
    import "github.com/nevindra/oasis/guardrail"
    guard := guardrail.NewInjectionGuard()
    ```
  - Symbols moved: `InjectionGuard`, `NewInjectionGuard`,
    `InjectionOption`, `InjectionResponse`, `InjectionPatterns`,
    `InjectionRegex`, `ScanAllMessages`, `InjectionLogger`,
    `SkipLayers`, `ContentGuard`, `NewContentGuard`, `ContentOption`,
    `MaxInputLength`, `MaxOutputLength`, `ContentLogger`,
    `ContentResponse`, `KeywordGuard`, `NewKeywordGuard`, `WithRegex`,
    `WithKeywordLogger`, `WithResponse`, `MaxToolCallsGuard`,
    `NewMaxToolCallsGuard`.

- **BREAKING — Rate limiting moved to subpackage
  `github.com/nevindra/oasis/ratelimit`.** `RateLimitOption`, `RPM`,
  `TPM`, `WithRateLimit`.
  - Migration:
    ```go
    // Before
    limited := oasis.WithRateLimit(provider, oasis.RPM(60), oasis.TPM(100_000))
    // After
    import "github.com/nevindra/oasis/ratelimit"
    limited := ratelimit.WithRateLimit(provider, ratelimit.RPM(60), ratelimit.TPM(100_000))
    ```

- **BREAKING — `agent.AgentCore` is deleted.** Its replacement,
  `internal/runtime.Runtime`, is embedded inside `LLMAgent`,
  `Network`, and `Workflow`; the promoted methods (`Name()`,
  `Tools()`, `Logger()`, `Limits()`, `HasDynamicTools()`,
  `CachedToolDefs()`, `SetCachedToolDefs()`,
  `ActiveSkillInstructions()`, `ExecuteSpawn`, `DispatchBuiltins`,
  `ExecuteWithSpan`, `ApplyRunOptions`) remain reachable through the
  agent value. `internal/runtime` is not part of the public API —
  third-party agent implementations no longer need to embed an
  oasis-internal type to participate.

- **BREAKING — `core.Agent.Execute` signature is now
  `Execute(ctx context.Context, task AgentTask, opts ...core.RunOption)
  error`.** Every third-party `core.Agent` implementation must add the
  variadic `opts` parameter (it is fine to ignore them initially) and
  delete any companion `ExecuteStream` / `ExecuteWith` /
  `ExecuteStreamWith` / `StartStream` / `StartStreamWith` methods.

- **BREAKING — `network.New` signature change.** From
  `New(name, desc, router, children ...core.Agent)` to
  `New(name, desc, router, opts ...network.Option)`. Children move
  into `network.WithChildren(a, b, ...)`; the previous variadic
  positional form (and the `network.NewWithOptions` helper) is gone.

- **BREAKING — `agent.WithMetadata` value type narrowed.** From
  `map[string]any` to `map[string]string`. JSON-encode structured
  metadata before passing if you need richer values.

- **BREAKING — `agent.BuildConfig` now returns `*agent.Config` instead
  of `agent.agentConfig` (by value).** The returned type's fields are
  no longer exported; access via methods (and same-package reads in
  `agent/`).

- `core/` package documentation no longer says "do not import directly."
  Importing `core/` is supported for power users and subpackage authors;
  the umbrella `github.com/nevindra/oasis` remains the recommended path
  for most consumers.

- `StepTrace` is now an alias for `ToolCallTrace` (rename for naming
  consistency with `IterationTrace` and `LLMCallTrace`). The old name is
  kept; rename your variables at convenience.

- `HybridRetriever` and `GraphRetriever` implement `core.Sourced`.

- Native Gemini and OpenAI-compat providers populate
  `ChatResponse.FinishReason` and `ChatResponse.ProviderMeta`.

- **`core.Erase` now applies structural input coercion** (`null`/empty →
  `{}`, stringified-JSON object/array unwrap one level) before
  `json.Unmarshal`. Coercion is pure-function, zero-alloc on the happy
  path, and never errors — malformed inputs that don't match either
  pattern pass through unchanged so the existing `json.Unmarshal`
  failure path reports the real problem. Default-on, no opt-out.

- **Default `MaxIter` raised 10 → 25.** Real tool-using workflows
  commonly need 15-20 iterations. Set `WithLimits(Limits{MaxIter: 10})`
  to restore the old default.

- **`compressMessages` now routes through the `Compactor` interface**
  instead of an inline English prompt. Users with custom `Compactor`
  implementations should handle both `ScopeFull` and
  `ScopeToolResultsOnly` (default `inlineCompactor` does both).

- `StreamingTool[In, Out]` inherits the shrunken `Tool` interface
  automatically.

### Deprecated

- `EventMaxIterReached` and `EventHalt` are no longer emitted by the
  loop but the constants remain exported for one minor release.
  Replace with `EventRunFinish{FinishReason: ...}`.
- `agent.WithRetry(provider, attempts, base)` and
  `ratelimit.WithRateLimit(provider, ...)` are kept as convenience
  wrappers. Prefer composing `agent.RetryMiddleware` /
  `ratelimit.RateLimitMiddleware` via `provider.Chain` or
  `agent.WithMiddleware` for stacks of more than one wrapper.

### Removed

- **BREAKING — Per-knob budget options removed.** `WithMaxIter`,
  `WithMaxSteps`, `WithMaxPlanSteps`, `WithMaxParallelDispatch`,
  `WithMaxAttachmentBytes`, `WithMaxToolResultLen`, `WithSuspendBudget`.
  Use `WithLimits(Limits{...})` instead.
- **BREAKING — Per-call budget pointer fields on `RunOptions` removed.**
  `RunOptions.MaxIter`, `MaxSteps`, `MaxPlanSteps`, `MaxAttachmentBytes`,
  `MaxToolResultLen`. Use `RunOptions.Limits *Limits` instead.
  ```go
  // Before
  &RunOptions{MaxIter: ptr(5)}
  // After
  &RunOptions{Limits: &Limits{MaxIter: 5}}
  ```
- **BREAKING — `LLMAgent.ExecuteStream`, `ExecuteWith`,
  `ExecuteStreamWith`, `StartStream`, `StartStreamWith` methods
  removed.** Use `Execute(ctx, task, opts...)` with
  `core.WithStream(ch)` and/or `agent.WithOverrides(opts)`. For
  multi-reader streaming, use `agent.Subscribe(ctx, ag, task,
  opts...)` (re-exported as `oasis.StartStream` for back-compat).
- **BREAKING — Streaming interfaces removed.** `agent.StreamingAgent`,
  `agent.AgentWithOptions`, `agent.StreamingAgentWithOptions`. Streaming
  is now a `RunOption`, not a separate method or interface.
- **BREAKING — Per-knob tool-subsystem options removed.**
  `agent.WithToolPolicy`, `agent.WithToolPolicyMatch`,
  `agent.WithToolApproval`, `agent.WithToolMiddleware` (and their
  `oasis.*` re-exports). Use `agent.WithToolConfig(ToolConfig{...})`
  with `Policies`, `PolicyMatchers`, `Approvals`, `Middleware`
  fields.
- **BREAKING — Per-knob processor & hook options removed.**
  `WithPreProcessors`, `WithPostProcessors`, `WithPostToolProcessors`,
  `WithPrepareStep`, `WithOnIterationComplete`, `WithOnError`. Use
  `WithProcessors(Processors{...})` and `WithHooks(Hooks{...})`.
- **BREAKING — Sub-agent spawning moved out of `agent` package.**
  `agent.WithAgents`, `agent.WithSubAgentSpawning`,
  `agent.MaxSpawnDepth`, `agent.DenySpawnTools`, `agent.SubAgentOption`,
  `agent.SubAgentConfig`, `agent.ExecuteSpawnAgent`,
  `agent.DispatchBuiltins`, and the `agent.Config` spawn fields
  (`SpawnEnabled`, `SpawnDepthLimit`, `DeniedSpawnTools`, `Agents`,
  `GetAgents()`), plus `RunOptions.Agents` — all removed. Use
  `network.WithChildren(...)` for static membership and
  `network.WithDynamicSpawning(SpawnPolicy)` for LLM-driven spawning.
  Umbrella names dropped: `oasis.WithAgents`,
  `oasis.WithSubAgentSpawning`, `oasis.MaxSpawnDepth`,
  `oasis.DenySpawnTools`.
- **BREAKING — `workflow.ToolStep` and `oasis.ToolStep` removed.** Use
  `workflow.AgentStep` wrapping a single-tool `LLMAgent`.
- **BREAKING — `workflow.NewWorkflowContext` and
  `workflow.WorkflowResult.Context` removed.** Read step outputs via
  `result.Steps[name].Output`.
- **BREAKING — `agent.Suspend(json.RawMessage)` removed.** Use
  `SuspendProtocol[Req, Resp].Suspend(req)` — the untyped escape
  hatch is gone; every suspending site must declare a typed protocol.
- **BREAKING — `read_full_result` built-in tool and its
  auto-registration removed.** Oversized results now chunk
  transparently into sequential tool-result messages.
- **BREAKING — `network.ParallelDispatch` mode, the
  `network.ParallelDefault` / `network.ParallelDisabled` constants,
  and `network.WithParallelDispatch(mode)` removed.** Set the per-
  router parallelism via `network.WithRouter(agent.WithLimits(
  agent.Limits{MaxParallelDispatch: N}))` — `N = 1` is sequential.
- **BREAKING — Skill provider concrete types unexported.**
  `skills.FileSkillProvider`, `skills.ChainedSkillProvider`,
  `skills.BuiltinSkillProvider` removed; use the interface-returning
  constructors `skills.FromDir`, `skills.Chain`, `skills.Builtin`.
  `skills.NewFileSkillProvider`, `skills.NewBuiltinSkillProvider`,
  `skills.ChainSkillProviders` are also gone.
- **BREAKING — Umbrella surface massively trimmed** (`oasis.go`
  845 → 137 LOC). Most niche re-exports moved to their subpackages;
  imports `oasis.OnError`, `oasis.PrepareStep`, `oasis.Step`,
  `oasis.AgentStep`, `oasis.ForEach`, `oasis.When`, `oasis.After`,
  `oasis.InputFrom`, `oasis.OutputTo`, `oasis.IterOver`,
  `oasis.NewContentGuard`, `oasis.NewKeywordGuard`,
  `oasis.NewInjectionGuard`, `oasis.NewStructuredCompactor`,
  `oasis.LLMAgent`, `oasis.AgentHandle`, `oasis.AgentOption`, and
  many more must now import their owning subpackage directly. See
  `oasis.go` for the curated remaining surface.
- **BREAKING — `core.ScheduledToolCall` removed.** Was a
  workflow-internal type that leaked into the public surface.
- **BREAKING — `EventInputReceived` and `EventProcessingStart`
  constants removed** (were deprecated). Use `EventRunStart`.
- **Satellite `go.mod` files collapsed back into the root module.**
  During the microkernel migration, 8 directories (`ingest`, `mcp`,
  `observer`, `rag`, `sandbox`, `provider/gemini`,
  `provider/openaicompat`, `store/sqlite`, `store/postgres`) each had
  their own `go.mod`. They are now plain subdirectories of the root
  module. Releases now require one tag instead of eight; the `go.work`
  workspace file and inter-module `replace` directives are gone. Go
  1.17+ lazy module loading still keeps heavy deps out of downstream
  builds that only import the umbrella, so user-facing behavior is
  unchanged.
- **Reference app `cmd/bot_example/`** — no longer the integration gate.
- **Out-of-scope tool packages** — `tools/knowledge`, `tools/remember`,
  `tools/skill`, `tools/shell`, `tools/file`, `tools/search`,
  `tools/schedule`, `tools/todo`. Will be re-implemented inside their
  owner modules during the harness layer.
- Dead `subAgentConfig` alias in `agent/llm.go`.
- Root-package `scheduler.go` (`Scheduler`, `NewScheduler`,
  `ComputeNextRun`, `FormatLocalTime`, `RunHook`,
  `WithSchedulerInterval`, `WithSchedulerTZOffset`, `WithOnRun`).
  Re-add separately if needed.
- Transitional alias files (`types_aliases.go`, `processor_aliases.go`,
  `tool_aliases.go`, `types.go`, `skill.go`, `skill_builtin.go`,
  `skill_scan.go`, `skill_tool.go`). The aliases now live in
  `oasis.go`.
- Inline English compression prompt in `agent/loop.go` (replaced by
  `inlineCompactor`).

### Fixed

- **`forwardSubagentStream` double-close** routed through a single
  `sync.Once` (the actual bypass sites were the no-tools streaming path
  and synthesis path in `agent/loop.go`, plus `agent/suspend.go`'s
  resume path). The `recover()` in `onceClose` is removed; the real
  bypass paths are fixed.
- `Provider.ChatStream` doc no longer claims providers leave the channel
  open — every implementation closes it, matching the actual contract
  used by the agent loop.
- `ErrHalt` doc now clarifies that processors must return `&ErrHalt{...}`
  (pointer), not a value, to satisfy the `error` interface.
- Silent base64-decode swallow in `Attachment.InlineData()` — moved to
  construction time via `NewAttachmentFromBase64`.
- **MCP / sandbox: repaired `ToolResult.Content` test rot + `ToolSearch`
  double-encoding.** The `ToolSearch` tool was JSON-encoding its result
  twice; downstream tests against `ToolResult.Content` had drifted to
  match the broken shape. The wrapper now encodes once and the tests
  assert the correct shape.

### Migration notes

- Consumers iterating events should expect `EventRunStart` as the first
  event and `EventRunFinish` as the last. Code that triggered on
  `EventMaxIterReached` or `EventHalt` should switch on
  `EventRunFinish.FinishReason`.
- `result.Output` continues to work; `result.Text()` is identical.
- New `AgentResult` fields are zero-value by default; existing reads are
  unaffected.
- The umbrella `oasis.*` surface was trimmed in Plan D. The most-used
  symbols stay re-exported (`oasis.Provider`, `oasis.Agent`,
  `oasis.NewAgent`, `oasis.NewLLMAgent`, `oasis.NewNetwork`,
  `oasis.NewWorkflow`, `oasis.WithMemory`, `oasis.WithLimits`,
  `oasis.WithToolConfig`, `oasis.WithProcessors`, `oasis.WithHooks`,
  `oasis.SuspendProtocol`, `oasis.Stream`, `oasis.StartStream`,
  `oasis.CosineSimilarity`, `oasis.WithCompaction`, …). Niche
  exports (hook types, event constants, workflow step constructors,
  guardrail/compaction constructors, store-capability interfaces,
  processor types, `oasis.LLMAgent`, `oasis.AgentHandle`,
  `oasis.AgentOption`, etc.) now require importing the owning
  subpackage directly — e.g. `agent.LLMAgent`, `agent.AgentOption`,
  `workflow.Step`, `guardrail.NewInjectionGuard`.
- Direct imports of subpackages (`oasis/store/sqlite`,
  `oasis/provider/gemini`, etc.) keep working — they are now regular
  subpackages of the root module rather than separate go modules, but
  the import paths are unchanged.
- Every external `Tool[In, Out]` implementation must: (1) delete the
  `Name()` method; (2) change `Definition() ToolDefinition` to
  `Definition() ToolMeta` and return only `{Name, Description}` (no
  `Parameters` field); (3) add `describe:"..."` and (where applicable)
  `enum:"..."` tags to the `In` struct fields; (4) delete the
  hand-written `Parameters: json.RawMessage(...)` block. For schemas
  reflection cannot express, implement `SchemaProvider.JSONSchema()
  json.RawMessage` on the input type.
- Every external `core.Agent` implementation must change `Execute(ctx,
  task) error` to `Execute(ctx, task, opts ...core.RunOption) error`.
  Inside the body, call `cfg := core.ApplyRunOptions(opts...)` once
  and read `cfg.Stream` / `cfg.Deadline` / `cfg.Overrides` as needed.
  Delete any `ExecuteStream` / `ExecuteWith` / `ExecuteStreamWith` /
  `StartStream` / `StartStreamWith` companion methods — the loop now
  dispatches through `Execute` with `core.WithStream(ch)` instead.
- Streaming/per-call cheat-sheet:
  ```go
  // Before
  ag.ExecuteStream(ctx, task, ch)
  ag.ExecuteWith(ctx, task, &RunOptions{Limits: &Limits{MaxIter: 3}})
  s := oasis.StartStream(ctx, ag, task)

  // After
  ag.Execute(ctx, task, core.WithStream(ch))
  ag.Execute(ctx, task,
      agent.WithOverrides(&agent.RunOptions{Limits: &agent.Limits{MaxIter: 3}}),
  )
  s := agent.Subscribe(ctx, ag, task) // or the kept oasis.StartStream alias
  ```
- Tool-subsystem cheat-sheet:
  ```go
  // Before
  oasis.WithToolPolicy("search", core.ToolPolicy{Timeout: 5*time.Second})
  oasis.WithToolPolicyMatch(matcher, policy)
  oasis.WithToolApproval("delete_file", agent.DenyHalt)
  oasis.WithToolMiddleware(LoggingMiddleware, TimingMiddleware)

  // After (one option, merges across calls)
  oasis.WithToolConfig(agent.ToolConfig{
      Policies: map[string]core.ToolPolicy{
          "search": {Timeout: 5 * time.Second},
      },
      PolicyMatchers: []agent.ToolPolicyMatcher{{Match: matcher, Policy: policy}},
      Approvals: []agent.ApprovalConfig{agent.Approval("delete_file", agent.DenyHalt)},
      Middleware: []core.ToolMiddleware{LoggingMiddleware, TimingMiddleware},
  })
  ```
- Processors / Hooks cheat-sheet:
  ```go
  // Before
  agent.WithPreProcessors(pre)
  agent.WithPostProcessors(post)
  agent.WithPostToolProcessors(postTool)
  agent.WithPrepareStep(fn)
  agent.WithOnIterationComplete(cb)
  agent.WithOnError(eh)

  // After
  agent.WithProcessors(agent.Processors{Pre: []core.PreProcessor{pre},
      Post: []core.PostProcessor{post},
      PostTool: []core.PostToolProcessor{postTool}})
  agent.WithHooks(agent.Hooks{PrepareStep: fn,
      OnIterationComplete: cb, OnError: eh})
  ```
- Sub-agent spawning cheat-sheet:
  ```go
  // Before — spawning lived on the agent
  ag := agent.NewLLMAgent("router", "...", provider,
      agent.WithAgents(child1, child2),
      agent.WithSubAgentSpawning(agent.MaxSpawnDepth(2)),
  )

  // After — spawning lives on the Network
  net := network.New("team", "...", provider,
      network.WithChildren(child1, child2),
      network.WithDynamicSpawning(network.SpawnPolicy{MaxDepth: 2}),
  )
  ```
- Workflow `ToolStep` → `AgentStep` cheat-sheet:
  ```go
  // Before
  workflow.ToolStep("lookup", myTool, inputSpec)
  // After
  workflow.AgentStep("lookup", agent.New("lookup", "", provider,
      agent.WithTools(myTool)), inputSpec)
  ```
- Budget migration cheat-sheet:
  ```go
  // Before
  agent := oasis.NewLLMAgent(
      oasis.WithMaxIter(20),
      oasis.WithMaxSteps(0),       // 0 meant unbounded
      oasis.WithMaxToolResultLen(50_000),
  )
  // After
  agent := oasis.NewLLMAgent(
      oasis.WithLimits(oasis.Limits{
          MaxIter:          20,
          MaxSteps:         oasis.Unbounded, // 0 now means "default 100"
          MaxToolResultLen: 50_000,
      }),
  )
  ```

- **Memory system redesign (BREAKING).** Replaced both `WithUserMemory(...)` and `WithHistory(...)` with a single `oasis.WithMemory(...)` entry point. All persistent memory now uses a unified `MemoryItem` type (facts, working memory, events, playbooks, reflections, summaries — discriminated by `Kind`), and all history/compaction/compression knobs are `memory.Option` values.
  - Removed: `oasis.WithUserMemory`, `oasis.MemoryStore`, `core.Fact`, `core.ScoredFact`.
  - Removed: `oasis.WithHistory`, `oasis.HistoryOption`, and the entire `github.com/nevindra/oasis/history` subpackage (`history.Store`, `history.MaxHistory`, `history.MaxTokens`, `history.AutoTitle`, `history.CrossThreadSearch`, `history.MinScore`, `history.Compaction`, `history.SemanticTrim`, `history.KeepRecent`, `history.Compress`, `history.Config`, `history.Option`, `history.Build`).
  - Added: `oasis.WithMemory` + `memory` package with `MemoryItem`, `ItemStore`, `Filter`, ingest/retrieve pipelines, `Remember`/`Recall`/`Forget`/`Pin` developer methods, and opt-in agent-callable tools via `memory.WithTools(...)`.
  - Added: `memory.WithCompaction(compactor, threshold)`, `memory.WithCompress(modelFunc, threshold)`, `memory.WithSemanticTrimEmbedding(e)`, `memory.WithKeepRecent(n)` — the former `history.*` options as `memory.Option` values.
  - **Migration:** drop your existing `user_facts` table. Combine both old option families into a single `WithMemory` call:
    ```go
    // Before
    oasis.WithUserMemory(store, emb)
    oasis.WithHistory(
        history.Store(store),
        history.MaxHistory(30),
        history.CrossThreadSearch(),
        history.MinScore(0.6),
        history.Compaction(compactor, 0.8),
        history.Compress(modelFunc, 200_000),
        history.SemanticTrim(emb),
    )

    // After
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithEmbedding(emb),
        memory.WithMaxHistory(30),
        memory.WithSemanticRecall(),
        memory.WithSemanticRecallMinScore(0.6),
        memory.WithCompaction(compactor, 0.8),
        memory.WithCompress(modelFunc, 200_000),
        memory.WithSemanticTrimming(),
        memory.WithSemanticTrimEmbedding(emb), // optional: smaller model for trimming
    )
    ```
- Satellite stores (`store/sqlite`, `store/postgres`): dropped `user_facts` table; added `memory_items` table. Existing fact data is **not** auto-migrated (pre-v1 hard cut).

## [0.16.0] - 2026-04-19

### Added

- `WithGenerationParams(*GenerationParams)` agent option — sets the full
  `GenerationParams` struct in one call. The params are deep-copied (struct +
  each inner pointer) so later mutations to the caller's values do not affect
  the agent. Companion to the existing `WithTemperature` / `WithTopP` /
  `WithTopK` / `WithMaxTokens` setters; useful when forwarding a pre-built
  `GenerationParams` to a sub-agent so new fields added to `GenerationParams`
  propagate automatically.
- **Deferred MCP tool schemas** (opt-in via `WithDeferredSchemas`): advertise
  MCP tool names + descriptions without their input schemas; load schemas on
  demand via an auto-registered `ToolSearch` tool. Saves ~600 tokens per
  unloaded tool schema for setups with many MCP servers. Auto-prepends a
  system-prompt block teaching the model the deferral mechanism. New options
  `WithDeferredSchemas`, `DeferOption`, `DeferThreshold`, `DeferAlwaysOn`,
  `DeferExclude`. New methods `ToolRegistry.EnsureSchema`,
  `ToolRegistry.DeferredDefinitions`, `MCPRegistry.SetDeferredMode`. New
  capability interface `SchemaEnsurer` (tools may implement to participate in
  deferred-schema loading).
- **MCP client** — connect agents to external Model Context Protocol servers over
  stdio and HTTP transports. Tools from MCP servers register into the existing
  `ToolRegistry` under `mcp__<server>__<tool>` namespacing and are callable like
  any other tool. Reconnect loop uses exponential backoff (500ms → 30s cap,
  10 attempts, ±25% jitter). New options `WithMCPServer`, `WithMCPServers`,
  `WithSharedMCPRegistry`, `WithMCPLifecycleHandler`; runtime management via
  `(*LLMAgent).MCP()` controller. File-based config loader at `mcp/config`
  (Claude Desktop compatible schema, `${ENV_VAR}` interpolation).
- New root types: `MCPServerConfig`, `StdioMCPConfig`, `HTTPMCPConfig`, `Auth`,
  `BearerAuth`, `MCPToolFilter`, `MCPServerStatus`, `MCPServerInfo`,
  `MCPServerState`, `MCPLifecycleHandler`, `NoopMCPLifecycle`, `MCPController`,
  `MCPRegistry`, `MCPEvent`, `MCPEventType`, `MCPAccessor`.
- New `mcp` package client types: `Client`, `StdioClient`, `HTTPClient`, `Auth`,
  `BearerAuth`, `InitializeResult`, `ListToolsResult`, `CallToolResult`,
  `ContentBlock`, `ServerInfo`. Test fixture at `mcp/mcptest`.
- `ToolRegistry.Remove(name string) error` method — required for removing MCP
  tools on server unregister; also usable by any caller that needs dynamic
  tool removal.
- **`tools/todo` package** — Claude-Code-style `todo_write` tool for agent task
  tracking. Exposes a single tool function (`todo_write`) that accepts a list
  of `{content, activeForm, status}` items (status ∈ `pending` /
  `in_progress` / `completed`). Validates length (max 50 items, 1000-char
  content, 200-char activeForm) and auto-clears the stored list when every
  item is `completed` so downstream UIs can hide the panel.
- **`todo.Backend` interface** — storage adapter (`Get`/`Set` by key) so
  embedders can persist task lists to whatever fits (in-memory, JSONB column,
  file, etc.). Implementations must serialize concurrent `Set` on the same
  key.
- **`todo.New(backend, keyFn)` constructor** — `keyFn(ctx)` extracts the
  scoping identifier (conversation ID, session ID, …) from the agent's
  execution context, letting a single tool instance serve many concurrent
  conversations.
- **`todo.ToolDescription` constant** — full prompt ported from Claude
  Code's `TodoWriteTool/prompt.ts` so the LLM actually uses the tool. The
  port replaces the `${FILE_EDIT_TOOL_NAME}` template with a literal
  "file edit tool"; the verification-agent nudge logic is not part of the
  prompt text and is not ported.

### Fixed

- Memory: `buildMessages` now merges adjacent `role:"system"` messages before
  returning. When a caller combined `WithPrompt`, `WithCompaction`, and
  `CrossThreadSearch`, the LLM request previously contained up to three
  consecutive system messages (base prompt + `[Prior conversation summary]`
  + cross-thread recall block). Anthropic and some OpenAI-compatible servers
  reject consecutive system messages outright; merging into a single block
  keeps wire format valid regardless of which features are enabled.
- Memory: when the conversation store's `GetMessages` fails, compaction and
  cross-thread recall are now skipped for that turn. Previously the error
  was logged and the agent continued — running compaction on empty history
  is a no-op, but cross-thread recall still fired, injecting a "recalled
  from past conversations" block without any local history to anchor it.
  The turn now degrades to a plain system+user request.
- Memory: persist-backpressure timeout bumped from 2s to 30s
  (`persistBackpressureTimeout`). The old value silently dropped user and
  assistant messages when the lightweight-persist path queued behind
  full-persist goroutines running slow embedding calls (5-15s typical).
- `WithDynamicTools` path now honors `StreamingTool` — tools implementing
  `StreamingTool` emit `EventToolProgress` events during `ExecuteStream` even
  when resolved dynamically per request. Previously the dynamic path only
  built a non-streaming executor, silently dropping progress events.
- `spawn_agent` now forwards the child's stream events through the parent's
  channel (text deltas, tool-call start/result, thinking, routing decisions).
  Previously `executeSpawnAgent` always called `child.Execute`, so callers of
  `ExecuteStream` saw only the final `EventToolCallResult` from the spawn.
  Child's `EventInputReceived` is filtered so it does not duplicate the
  parent's input event. Tool-level progress events from `StreamingTool` also
  propagate through spawned children via a `funcTool.ExecuteStream` method.
- `spawn_agent` now reuses the parent's `MCPRegistry` via
  `WithSharedMCPRegistry` instead of allocating a fresh registry (with 64-cap
  events channel + maps) per spawn. Relevant for fan-out workloads that call
  `spawn_agent` in parallel.
- `spawn_agent` now inherits the parent's `Tracer`. Previously the child's
  iterations, LLM calls, and tool dispatches were untraced when the parent
  was configured with `WithTracer`.
- `spawn_agent` now forwards `GenerationParams` via `WithGenerationParams`
  instead of hand-copying four fields. Future fields added to
  `GenerationParams` now propagate to sub-agents automatically.
- `spawn_agent` in a `Network` no longer leaks the router's `agent_*`
  delegation tools into the child's tool definitions. Previously the child
  inherited every `agent_<name>` entry from the parent's tool list but could
  not call them — the child is an `LLMAgent` whose dispatch does not route
  the `agent_` prefix, so every call produced `unknown tool: agent_<name>`
  while still costing tokens on the request. `agent_*` defs are now stripped
  alongside `ask_user`.
- `WithCompaction` auto-trigger is now actually wired. The 0.15.0 option
  stored the `Compactor` and `threshold` on `agentConfig` but nothing read
  them at runtime, so consumers got a silent no-op despite docs promising
  auto-trigger during `buildMessages`. The wiring now: when the loaded
  conversation history's estimated tokens exceed
  `compactThreshold × MaxTokens`, the Compactor is invoked and the history
  is replaced in-memory for this turn with a single
  `[Prior conversation summary]` system message. Transient per-load — the
  store is not rewritten. On Compactor error, the option logs a warning
  and falls through to the existing token-based trim path. If `MaxTokens`
  is unset (0), auto-compaction is a noop since there is no budget to
  scale the threshold against.
- `StructuredCompactor` `partial_sections` warning now accounts for
  `ExtraSections` — previously it only tripped when fewer than 9 total
  sections parsed, silently hiding cases where user-supplied extras went
  missing. Threshold is now `9 + len(req.ExtraSections)`.
- `StructuredCompactor` `summary_truncated_at_budget` warning now uses
  `OutputTokens >= budget` instead of exact equality, catching truncation
  when providers report slightly over-budget token counts.

### Changed

- `EstimateContextTokens` dropped no-op per-family multiplication branches
  for `anthropic` / `openai` / `openaicompat` (all were `* 100 / 100`).
  Only `gemini` has a non-identity adjustment (~5% tighter); others use
  the base estimate. No behavior change.
- `StructuredCompactor` dropped the unused internal `logger` field. The
  constructor no longer allocates an unused `slog.Logger`.

## [0.15.0] - 2026-04-16

### Added
- `Compactor` interface and `StructuredCompactor` default implementation for
  per-thread conversation compaction with a 9-section structured summary
  format (primary intent, technical concepts, files, errors, problem solving,
  all user messages, pending tasks, current work, next step).
- `CompactRequest`, `CompactResult`, `CompactSection` types for compaction.
- `EstimateContextTokens(messages, model)` helper for token estimation.
- `StripMediaBlocks(messages)` helper to remove image/document attachments
  before compaction LLM calls.
- `CompactableToolNames()` helper returning the default whitelist of tool
  names whose results are safe to compact (callers extend this list).
- `BuildCompactPrompt(extras, focusHint, isRecompact)` prompt template builder.
- `WithCompaction(Compactor, threshold)` ConversationOption for opt-in
  auto-trigger during `buildMessages`.
- `provider/catalog.StaticContextWindow(modelID)` — cross-provider static
  InputContext lookup. Returns 0 when the model ID isn't in the registry.
  Useful for `threshold × effectiveWindow` math when the caller's provider
  key doesn't match the static data's provider identifier.

### Changed
- `WithCompressThreshold` default changed from 200_000 (enabled) to 0
  (disabled). Per-turn LLM compression must now be opted into explicitly.
  Per-thread compaction is the preferred strategy.
- Updated docstrings on `WithCompressModel` and `WithCompressThreshold` to
  cross-reference the new compaction primitives.

## [0.14.0] - 2026-04-10

### Added
- **Sandbox filesystem mounts** — new `FilesystemMount` interface in `sandbox/` lets apps back specific sandbox paths with external storage. `MountSpec` declares the path, mode (read-only, write-only, read-write), and lifecycle policy (`PrefetchOnStart`, `FlushOnClose`, `MirrorDeletes`, `Include`/`Exclude` globs). `PrefetchMounts` copies backend files into the sandbox at start; `FlushMounts` scans the sandbox at close and publishes deltas. Tool-level interception in `file_write`, `file_edit`, and `deliver_file` publishes writes to the backend immediately with optimistic version checks. Conflicts surface as tool errors via `ErrVersionMismatch` so the agent can re-read and retry.
- **`WithMounts(specs, manifest)` ToolsOption** — wires a slice of `MountSpec` and a shared `Manifest` into the tool layer.
- **`Manifest` type** — concurrent-safe per-sandbox tracking of `(mountPath, key) → MountEntry` so Layer 2 publishes and Layer 3 flush can send the correct precondition.
- **`FilesystemMounter` capability stub** (`sandbox/mounter.go`) — optional interface for sandbox runtimes to opt into live FUSE/virtio-fs mounting. No implementation ships today.
- **`ErrKeyNotFound` sentinel** — distinct from `sandbox.ErrNotFound` (sandbox-session-level), used by `FilesystemMount.Stat`/`Open` for missing keys.
- `Compatibility`, `License`, `Metadata map[string]string` fields on `Skill` and `SkillSummary` — aligns with the [AgentSkills open specification](https://agentskills.io).
- `ActivateWithReferences()` function — resolves skill references at activation time, prepending referenced skill instructions (one level deep, missing refs silently skipped).
- `WithActiveSkills(skills ...Skill)` agent option — pre-activates skills at init time, injecting their instructions into the system prompt on every LLM call.
- `WithSkills(p SkillProvider)` agent option — registers a `SkillProvider` and auto-adds `skill_discover`/`skill_activate` tools (plus `skill_create`/`skill_update` if the provider implements `SkillWriter`).
- `DefaultSkillDirs()` — returns AgentSkills-compatible scan paths (`<cwd>/.agents/skills/`, `~/.agents/skills/`).
- `{dir}` placeholder in skill instructions resolved to absolute skill directory path at activation time.
- Frontmatter parser supports indented metadata blocks (for `metadata:` with sub-keys).
- Prescriptive built-in skills: `oasis-pdf` (HTML/CSS + Playwright), `oasis-docx` (python-docx), `oasis-xlsx` (openpyxl), `oasis-pptx` (PptxGenJS). Agents use underlying libraries directly with full creative freedom and API access.
- **`Attachments` field on `ToolResult`** — tools can return binary attachments (images, PDFs, etc.) alongside text content. Attachments flow through `DispatchResult` into the agent's accumulated attachments and are passed to the LLM as multimodal input.
- **Tool-loop streaming for single agents** — `LLMAgent` now uses `ChatStream` during tool-loop iterations, providing real-time `EventToolCallDelta` events as arguments arrive. Networks continue using non-streaming `Chat()` to preserve text-delta deduplication with sub-agent streaming.
- **Embedding provider fallback** — unknown embedding provider names in `resolve.EmbeddingProvider` now fall back to OpenAI-compatible when `BaseURL` is provided, matching the existing chat provider behavior.

### Fixed
- **Sandbox and skill tools on Network** — `NewNetwork` was missing the sandbox tool and skill provider registration that `NewLLMAgent` performs, causing "unknown tool" errors for `execute_code`, `shell`, and other sandbox tools when `WithSandbox` was passed to a Network. Also wires `activeSkillInstructions` into the Network's loop config.
- **Router text-delta after child delegation** — the router's final `text-delta` was incorrectly suppressed when a child agent had already streamed, preventing the router from synthesizing or contextualizing the child's output.
- **Qwen provider resolver** — `qwen` and `qwen-cn` were defined in the model catalog but missing from the resolver's known-provider list, causing "embedding provider not supported" errors when configured without an explicit `BaseURL`.
- **HNSW index for high-dimension embeddings** — pgvector HNSW and IVFFlat indexes max out at 2000 dimensions. The Postgres store now skips index creation and falls back to sequential scan when embedding dimensions exceed this limit, instead of failing on init.

### Changed
- **BREAKING:** Built-in document generation skills now teach agents to use underlying libraries directly instead of routing through `oasis-render`. Agents write code that calls python-docx, openpyxl, Playwright, or PptxGenJS — no intermediate JSON spec format.
- Skill tool `skill_activate` output includes `Compatibility`, `License`, and `Metadata` fields.
- Skill tool `skill_create`/`skill_update` accepts `Compatibility`, `License`, `Metadata` parameters.
- **`deliver_file` tool routing** — now consults the mount table to publish files. Falls back to the legacy `FileDelivery` if no mount covers the path. Errors with a clear message if neither is configured.

### Deprecated
- **`FileDelivery` interface** — superseded by `FilesystemMount` with `MountWriteOnly` mode. Continues to work via the fallback path in `deliver_file`. Will be removed in a future release.

### Removed
- `bin/oasis-render` CLI — replaced by prescriptive skills that teach agents to use libraries directly.
- `renderers/` directory — PDF, DOCX, XLSX, PPTX renderer scripts removed.
- `requirements.txt` — Python deps for renderers (library deps remain in Dockerfile for direct agent use).

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.16.0...HEAD
[0.16.0]: https://github.com/nevindra/oasis/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/nevindra/oasis/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/nevindra/oasis/releases/tag/v0.14.0
