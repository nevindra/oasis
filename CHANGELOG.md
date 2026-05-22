# Changelog

All notable changes to this project will be documented in this file.

Format based on [Keep a Changelog](https://keepachangelog.com/), adhering to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- **HITL stream event parity** (`docs/superpowers/specs/2026-05-22-hitl-stream-event-parity-design.md`).
  - New `StreamEventType` constants for mid-stream suspension: `EventToolCallSuspended`, `EventStepSuspended`, `EventProcessorSuspended`. Emitted before the iteration finish event so UIs can render a "human, please decide" card in real time instead of waiting for `EventRunFinish`. Re-exported from `oasis.go`.
  - New `StreamEvent` fields `Protocol string` and `SuspendPayload json.RawMessage`. Populated on the three new mid-stream events, on `EventRunFinish` when `FinishReason == FinishSuspended`, and reserved for future use on `EventToolApprovalPending`. Both use `omitempty` so existing JSON consumers see no shape change for non-suspend events.
  - New `IterationTrace.FinishReason FinishReason` field. Lets callers walking `AgentResult.Iterations` identify the suspending iteration (or any other terminal reason) without external bookkeeping.
  - New `AgentResult.SuspendProtocol string` field. Carries the typed protocol's tag for suspended runs; empty for untyped `Suspend(json.RawMessage)` callers.
  - New convenience methods: `AgentResult.Suspended() bool`, `AgentResult.SuspendedProtocol() string`, `Stream.Suspended() bool`, `Stream.SuspendedProtocol() string`. Shorthands for `r.FinishReason == FinishSuspended` and `r.SuspendProtocol`. The `Stream` accessors block on completion (same semantics as the existing `SuspendPayload()` accessor).
  - All additions are additive — no existing event type, field, or signature is removed or renamed. Spec #2 of 6 in the HITL parity roadmap.

### Breaking

- **`AgentHandle.State()` no longer blocks.** Callers that read `Result()` after `State().IsTerminal()` must insert `h.Sync()` between the two. Migration hint: `grep -n 'State().IsTerminal' your-project/` and add `Sync()` calls. (finding 3.4)
- **Conflicting embedding providers panic at build time.** `WithUserMemory(em1, ...)` and `WithHistory(history.CrossThreadSearch(em2, ...))` with non-equal embeddings now panic from `BuildConfig`. Pass the same `EmbeddingProvider` to both, or pick one. (finding 1.2.g)
- **`WithSandbox(any)` is now `WithSandbox(core.Sandbox)`.** The `sandbox/` satellite's existing type already implements the new `core.Sandbox` interface — no satellite changes needed. Custom sandbox types must implement `Close() error`. (finding 1.2.k)
- `AgentTask.Context map[string]any` removed. Use the typed `ThreadID`/`UserID`/`ChatID` fields. App-defined metadata moves to `AgentTask.Extra`. The `ContextThreadID`/`ContextUserID`/`ContextChatID` constants and `TaskThreadID()`/`TaskUserID()`/`TaskChatID()` accessors are deleted.
- `Attachment.Base64` field removed. Construct via `NewAttachment` / `NewAttachmentFromURL` / `NewAttachmentFromBase64`. `InlineData()` is now infallible and returns `Data` directly.
- `ChatMessage.Role` switches from `string` to typed `Role`. String literals still compile for comparisons; direct assignments of `msg.Role` to a `string` variable need an explicit `string()` conversion. New code should use `RoleSystem` / `RoleUser` / `RoleAssistant` / `RoleTool`.
- `AgentCore.Drain()` and `AgentMemory.Drain()` renamed to `Close() error`. Returns nil today; the error return is reserved for future flush failures.
- `Erase` (the `core.Tool[In, Out]` → `core.AnyTool` adapter) moved from the
  one-function `github.com/nevindra/oasis/tool` package into `core/` next to
  the `Tool` and `AnyTool` types it bridges. The `tool/` subpackage has been
  deleted. The umbrella API `oasis.Erase` is unchanged — anyone using the
  curated surface sees no break. Only direct importers of `oasis/tool` (a
  niche subpackage import for a one-function package) need to switch to
  `oasis/core` or `oasis.Erase`.
- **BREAKING**: Restructured the repository into a hybrid architecture per
  `docs/superpowers/specs/2026-05-18-hybrid-architecture-design.md`. Highlights:
  - Protocol types and core interfaces moved to a new leaf package
    `github.com/nevindra/oasis/core`. The `core` package depends on nothing
    inside `oasis` (enforced via depguard) and is safe to import everywhere.
  - Primitives reorganised into focused public subpackages: `agent`, `workflow`,
    `network`, `compaction`, `guardrail`, `ratelimit`, `memory`, `skills`,
    `processor`.
  - Heavy or optional-dep code extracted as satellites with their own
    `go.mod`: `store/sqlite`, `store/postgres`, `provider/gemini`,
    `provider/openaicompat`, `observer`, `ingest`, `sandbox`, `rag` — joining
    the existing `mcp` satellite. Each is opt-in: pulling in the SQLite
    driver, the Docker SDK, the OTEL stack, etc. requires explicitly importing
    the relevant satellite.
  - Store-capability interfaces (`KeywordSearcher`, `GraphStore`,
    `BidirectionalGraphStore`, `DocumentGetter`, `DocumentMetaLister`) and
    `CheckpointStore` / `IngestCheckpoint` moved to `core/` so satellites can
    implement them without cross-satellite dependencies.
  - `oasis.go` is now a curated re-export umbrella. The 80% case stays
    `import "github.com/nevindra/oasis"` — common types and constructors are
    available as type aliases and `var`-bound functions. The transitional
    `types_aliases.go`, `processor_aliases.go`, `tool_aliases.go`, `types.go`,
    and the `skill*.go` re-export shims have been deleted; their content is
    now in `oasis.go`.
  - `scheduler.go` was removed from the root (no external callers).
- **BREAKING**: Compaction *implementation* moved to a separate Go module
  `github.com/nevindra/oasis/compaction`. The `Compactor` interface and
  `CompactRequest`/`CompactSection`/`CompactResult` types remain in the
  root `oasis` package — they are the kernel contract that
  `oasis.WithCompaction` consumes.
  - Symbols moved: `StructuredCompactor`, `NewStructuredCompactor`,
    `BuildCompactPrompt`, `EstimateContextTokens`, `StripMediaBlocks`,
    `CompactableToolNames`, `ErrEmptyMessages`, `ErrNoProvider`,
    `ErrSummaryParseFailed`.
  - Symbols retained in root: `Compactor`, `CompactRequest`, `CompactResult`,
    `CompactSection`, `WithCompaction`.
  - Migration:
    ```go
    // Before
    import "github.com/nevindra/oasis"
    c := oasis.NewStructuredCompactor(provider)

    // After
    import (
        oasis "github.com/nevindra/oasis"
        "github.com/nevindra/oasis/compaction"
    )
    c := compaction.NewStructuredCompactor(provider)
    // oasis.CompactRequest, oasis.CompactResult, oasis.WithCompaction still in root.
    ```
  - Third extraction in the microkernel migration. First one exercising
    the "kernel-consumed interface stays, satellite implementation moves"
    split. See `docs/superpowers/specs/2026-05-17-microkernel-migration-design.md` §6.
- **BREAKING**: `InjectionGuard`, `ContentGuard`, `KeywordGuard`,
  `MaxToolCallsGuard` and their constructors/options moved to a separate
  Go module `github.com/nevindra/oasis/guardrail`.
  - Migration:
    ```go
    // Before
    import "github.com/nevindra/oasis"
    guard := oasis.NewInjectionGuard()

    // After
    import (
        oasis "github.com/nevindra/oasis"
        "github.com/nevindra/oasis/guardrail"
    )
    guard := guardrail.NewInjectionGuard()
    ```
  - Symbols moved: `InjectionGuard`, `NewInjectionGuard`, `InjectionOption`,
    `InjectionResponse`, `InjectionPatterns`, `InjectionRegex`,
    `ScanAllMessages`, `InjectionLogger`, `SkipLayers`, `ContentGuard`,
    `NewContentGuard`, `ContentOption`, `MaxInputLength`, `MaxOutputLength`,
    `ContentLogger`, `ContentResponse`, `KeywordGuard`, `NewKeywordGuard`,
    `WithRegex` (on KeywordGuard), `WithKeywordLogger`, `WithResponse` (on
    KeywordGuard), `MaxToolCallsGuard`, `NewMaxToolCallsGuard`.
  - Second extraction in the microkernel migration. See
    `docs/superpowers/specs/2026-05-17-microkernel-migration-design.md`.
- **BREAKING**: `RateLimitOption`, `RPM`, `TPM`, `WithRateLimit` moved to
  separate Go module `github.com/nevindra/oasis/ratelimit`.
  - Migration:
    ```go
    // Before
    import "github.com/nevindra/oasis"
    limited := oasis.WithRateLimit(provider, oasis.RPM(60), oasis.TPM(100_000))

    // After
    import (
        oasis "github.com/nevindra/oasis"
        "github.com/nevindra/oasis/ratelimit"
    )
    limited := ratelimit.WithRateLimit(provider,
        ratelimit.RPM(60),
        ratelimit.TPM(100_000),
    )
    ```
  - First extraction in the microkernel migration. See
    `docs/superpowers/specs/2026-05-17-microkernel-migration-design.md`.
- **BREAKING — `Tool` interface reshaped from bundle to atomic.** One
  implementation now describes exactly one operation. New types:
  - `AnyTool`: type-erased atomic interface (`Name() / Definition() /
    ExecuteRaw(ctx, args)`). Consumed by the loop and the registry.
  - `Tool[In, Out any]`: type-safe generic authoring interface.
  - `Erase[In, Out](Tool[In, Out]) AnyTool`: adapter for registration.
  - `StreamingAnyTool`: optional streaming capability replacing the old
    `StreamingTool`.

  `WithTools` now takes `...AnyTool`. `ToolRegistry.Add` now takes `AnyTool`.
  Bundle-style tools (one impl exposing N definitions) must be split into N
  atomic implementations. Built-in tools migrated: `tools/http` (now
  `oasis.Tool[FetchInput, string]`), `tools/data` (split into 4 atomic
  tools), skill tools (split into 4), sandbox tools, MCP wrappers.
- **BREAKING — `Tool` interface shrunk (Phase 1.5: typed tool schemas)**:
  - Removed `Name() string`. The tool's name now lives in the `ToolMeta`
    returned by `Definition()`.
  - `Definition() ToolDefinition` → `Definition() ToolMeta`. Authors
    return name + description only; the JSON Schema for `In` is derived
    from the Go type by reflection inside `Erase`.
- **BREAKING — Schema-shape errors now panic (Phase 1.5)**: Schema-shape errors now **panic** at `Erase[In, Out]()` registration time with a descriptive message (field path, offending Go type, supported alternatives). They previously failed silently at LLM-call time.
- **`Tool.Execute` errors now propagate as Go errors from the erased adapters.** Previously `core.Erase` swallowed the Go error from `tool.Execute(...)` into `ToolResult.Error` and returned `(result, nil)`. It now returns `(result, err)` so the new dispatch policy wrapper can inspect typed errors (`Retryable`, `net.Error.Timeout()`, `context.DeadlineExceeded`). The LLM-visible result is unchanged because `agent.toolResultToDispatch` already prioritizes the Go error path. External `AnyTool` implementers that read `ToolResult.Error` are unaffected. Implementers that re-wrap erased tools and previously assumed a nil error return from `ExecuteRaw` must now propagate or absorb the typed error. Argument-unmarshal errors and result-marshal errors continue to return `(result, nil)`.

### Changed (breaking)

- `agent.AgentCore` fields are no longer exported. Access via methods
  (`Name()`, `Tools()`, `Logger()`, `HasDynamicTools()`, `CachedToolDefs()`,
  `SetCachedToolDefs()`, `ActiveSkillInstructions()`) or via methods that
  absorb operations previously requiring field access (`ExecuteSpawn`,
  `DispatchBuiltins`). Internal type — was documented "do not depend on
  stability."
- `agent.BuildConfig` now returns `*agent.Config` instead of
  `agent.agentConfig` (by value). The returned type's fields are no
  longer exported; access via methods (`Agents()` and same-package reads
  in `agent/`).
- Removed `agent.SubAgentConfig` struct. State now lives on `AgentCore`
  and is accessed via the new `ExecuteSpawn` method.
- Removed package-level helpers `agent.ExecuteSpawnAgent` and
  `agent.DispatchBuiltins`. Use methods on `*AgentCore` instead.

### Changed (non-breaking)

- `core/` package documentation no longer says "do not import directly."
  Importing `core/` is supported for power users and satellite authors;
  the umbrella `github.com/nevindra/oasis` remains the recommended path
  for most consumers.

### Changed

- `StepTrace` is now an alias for `ToolCallTrace` (rename for naming consistency with `IterationTrace` and `LLMCallTrace`). The old name is kept; rename your variables at convenience.
- `HybridRetriever` and `GraphRetriever` implement `core.Sourced`.
- Native Gemini and OpenAI-compat providers populate `ChatResponse.FinishReason` and `ChatResponse.ProviderMeta`.
- `core.Erase` now applies structural input coercion (`null`/empty → `{}`, stringified-JSON object/array unwrap one level) before `json.Unmarshal`. Coercion is pure-function, zero-alloc on the happy path, and never errors — malformed inputs that don't match either pattern pass through unchanged so the existing `json.Unmarshal` failure path reports the real problem. This default-on behavior is intentional: no opt-out.
- **Default `maxIter` raised 10 → 25.** Real tool-using workflows commonly need 15-20 iterations. Set `WithMaxIter(10)` to restore the old default. (finding 3.6)
- **`compressMessages` now routes through the `Compactor` interface** instead of an inline English prompt. Users with custom `Compactor` implementations should handle both `ScopeFull` and `ScopeToolResultsOnly` (default `inlineCompactor` does both). (findings 1.2.f, 3.9)
- `StreamingTool[In, Out]` inherits the shrunken `Tool` interface automatically.

### Added

#### Streaming world-class (Phases 1-11)
- **Lifecycle envelope:** every run now starts with `EventRunStart` and ends with `EventRunFinish` carrying `FinishReason`, `Warnings`, and `ProviderMeta`. Iterations are bracketed by `EventIterationStart`/`Finish`. See `docs/superpowers/specs/2026-05-21-streaming-world-class-design.md`.
- **Structured object streaming:** when `WithResponseSchema` is configured, the loop emits `EventObjectDelta` snapshots of partial JSON and `EventObjectFinish` with the final validated bytes. Top-level array schemas additionally emit one `EventElementDelta` per completed element.
- **Typed adapters:** `oasis.StreamObjectAs[T](stream)` returns a typed channel of partial-object snapshots; `oasis.ResultObjectAs[T](result)` decodes the final object. Generic free functions — no contagion of generics through `Agent` / `Network` / `Workflow`.
- **Result accessor parity:** `AgentResult` and `Stream` gain `FinishReason`, `Sources`, `Files`, `Warnings`, `ProviderMeta`, `SuspendPayload`, `Object`, `Iterations`. Same method names on both paths.
- **Per-stream observability:** new `agent.iteration` and `llm.generate` OTel spans under the existing `agent.execute` root, populated with model / temperature / max-tokens / input-tokens / output-tokens / finish-reason attributes. `AgentResult.Iterations` exposes the same data without OTel.
- **`core.Sourced` / `core.Warner`:** opt-in interfaces for tools, retrievers, and providers to declare citations and non-fatal warnings.

#### Other additions
- **`core.ToolResultStore` interface** + default in-memory implementation (`core.NewInMemoryToolResultStore`) for paging large tool results. Auto-enabled with 10 MiB total cap and 5-minute TTL per entry; opt out with `WithToolResultStore(nil)`. (finding 3.7)
- **`read_full_result` built-in tool** for the LLM to retrieve slices of stored results. Auto-registered when a `ToolResultStore` is configured. (finding 3.7)
- **`core.Sandbox` interface** — see breaking note above. (finding 1.2.k)
- **`core.CompactRequest.Scope`** field with `core.ScopeFull` and `core.ScopeToolResultsOnly` constants. (finding 1.2.f)
- **`AgentHandle.Sync()`** — see breaking note above. (finding 3.4)
- **`core.EventMaxIterReached`** stream event emitted before forced synthesis. (finding 3.6)
- **Four new options:** `WithToolResultStore`, `WithMaxToolResultLen`, `WithMaxParallelDispatch`, `WithMaxPlanSteps`. (findings 3.7, 3.8)
- **Helper option functions:** `WithToolResultMaxBytes`, `WithToolResultTTL` for tuning the in-memory store. (finding 3.7)
- `StreamingTool[In, Out]` generic interface for type-safe streaming tool authoring. Bridge via `EraseStreaming[In, Out]` to register as a `StreamingAnyTool`.
- `NewAttachment`, `NewAttachmentFromURL`, `NewAttachmentFromBase64` constructors.
- `Role` type with `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool` constants.
- `go.work` workspace file for local multi-module development.
- `scripts/check-module-deps.sh` enforces microkernel dependency invariants in CI.
- `core.ToolMeta` struct — `Name` + `Description` fields, returned by `Tool.Definition()` (Phase 1.5).
- `core.SchemaProvider` interface — implement `JSONSchema() json.RawMessage` on an input type to bypass reflection (recursive shapes, `oneOf`, provider-specific schemas) (Phase 1.5).
- `core.DeriveSchema[T any]() json.RawMessage` — exported helper that builds a JSON Schema from any Go type by reflection (Phase 1.5).
- Struct-tag vocabulary recognised by the reflector: `json:"name,omitempty"` (stdlib), `describe:"..."`, `enum:"a,b,c"` (Phase 1.5).
- Three umbrella re-exports: `oasis.ToolMeta`, `oasis.SchemaProvider`, `oasis.DeriveSchema` (Phase 1.5).
- **Deferred MCP tool schemas** (opt-in via `WithDeferredSchemas`): advertise MCP tool names + descriptions without their input schemas; load schemas on demand via an auto-registered `ToolSearch` tool. Saves ~600 tokens per unloaded tool schema for setups with many MCP servers. Auto-prepends a system-prompt block teaching the model the deferral mechanism. New options `WithDeferredSchemas`, `DeferOption`, `DeferThreshold`, `DeferAlwaysOn`, `DeferExclude`. New methods `ToolRegistry.EnsureSchema`, `ToolRegistry.DeferredDefinitions`, `MCPRegistry.SetDeferredMode`. New capability interface `SchemaEnsurer` (tools may implement to participate in deferred-schema loading). See [`docs/guides/connecting-mcp-servers.md`](docs/guides/connecting-mcp-servers.md) § "Deferred schemas".
- **MCP client** — connect agents to external Model Context Protocol servers over stdio and HTTP transports. Tools from MCP servers register into the existing `ToolRegistry` under `mcp__<server>__<tool>` namespacing and are callable like any other tool. Reconnect loop uses exponential backoff (500ms → 30s cap, 10 attempts, ±25% jitter). New options `WithMCPServer`, `WithMCPServers`, `WithSharedMCPRegistry`, `WithMCPLifecycleHandler`; runtime management via `(*LLMAgent).MCP()` controller. File-based config loader at `mcp/config` (Claude Desktop compatible schema, `${ENV_VAR}` interpolation). See [`docs/guides/connecting-mcp-servers.md`](docs/guides/connecting-mcp-servers.md).
- New root types: `MCPServerConfig`, `StdioMCPConfig`, `HTTPMCPConfig`, `Auth`, `BearerAuth`, `MCPToolFilter`, `MCPServerStatus`, `MCPServerInfo`, `MCPServerState`, `MCPLifecycleHandler`, `NoopMCPLifecycle`, `MCPController`, `MCPRegistry`, `MCPEvent`, `MCPEventType`, `MCPAccessor`.
- New `mcp` package client types: `Client`, `StdioClient`, `HTTPClient`, `Auth`, `BearerAuth`, `InitializeResult`, `ListToolsResult`, `CallToolResult`, `ContentBlock`, `ServerInfo`. Test fixture at `mcp/mcptest`.
- `ToolRegistry.Remove(name string) error` method — required for removing MCP tools on server unregister; also usable by any caller that needs dynamic tool removal.
- `core.ToolPolicy` (per-tool `Timeout`, `Retries`, `RetryDelay`, `MaxRetryDelay`, `RetryOn`).
- `core.Retryable` interface, `core.RetryableError(err) error` wrapper, `core.DefaultRetryOn(err) bool` predicate, `core.BackoffDelay(base, max, attempt)` helper.
- `core.OutSchemaProvider` opt-in interface — tools may publish a custom output JSON Schema that overrides the schema derived from `Out` by reflection.
- `core.ToolDefinition.OutputSchema json.RawMessage` field, populated by `core.Erase` / `core.EraseStreaming` via `DeriveSchema[Out]()` (or the override). Provider implementations decide whether to forward this to the LLM.
- `core.ToolRegistry.IsStreamingTool(name) bool` lookup.
- `agent.WithToolPolicy(name string, p core.ToolPolicy)` and `agent.WithToolPolicyMatch(matcher func(name string) bool, p core.ToolPolicy)` options. ServeMux-style precedence: exact name first, then matchers in registration order. Streaming tools bypass the policy wrapper entirely (with a one-shot `slog.Warn` if a policy was registered for one).
- Umbrella re-exports: `oasis.ToolPolicy`, `oasis.Retryable`, `oasis.RetryableError`, `oasis.DefaultRetryOn`, `oasis.OutSchemaProvider`.
- **`tools/todo` package** — Claude-Code-style `todo_write` tool for agent task tracking. Exposes a single tool function (`todo_write`) that accepts a list of `{content, activeForm, status}` items (status ∈ `pending` / `in_progress` / `completed`). Validates length (max 50 items, 1000-char content, 200-char activeForm) and auto-clears the stored list when every item is `completed` so downstream UIs can hide the panel.
- **`todo.Backend` interface** — storage adapter (`Get`/`Set` by key) so embedders can persist task lists to whatever fits (in-memory, JSONB column, file, etc.). Implementations must serialize concurrent `Set` on the same key.
- **`todo.New(backend, keyFn)` constructor** — `keyFn(ctx)` extracts the scoping identifier (conversation ID, session ID, …) from the agent's execution context, letting a single tool instance serve many concurrent conversations.
- **`todo.ToolDescription` constant** — full prompt ported from Claude Code's `TodoWriteTool/prompt.ts` so the LLM actually uses the tool. The port replaces the `${FILE_EDIT_TOOL_NAME}` template with a literal "file edit tool"; the verification-agent nudge logic is not part of the prompt text and is not ported.
- **Streaming `Stream` wrapper.** `oasis.StartStream(ctx, agent, task)` returns
  a multi-reader stream with blocking accessors (`Text()`, `ToolCalls()`,
  `ToolResults()`, `Reasoning()`, `Usage()`, `Result()`), live subscription
  via `Events()`, and filtered callbacks (`OnTextDelta`, `OnReasoningDelta`,
  `OnToolCall`, `OnToolResult`, `OnEvent`). Bounded ring-buffer replay
  (default 256 events, configurable via `RunOptions.StreamReplayLimit`).
  Slow subscribers receive a `subscriber-dropped` warning and are dropped —
  they cannot stall the agent. The single-reader channel kernel
  (`ExecuteStream`) is unchanged.
- **`AgentResult` convenience accessors.** `Text()`, `Reasoning()`,
  `ToolCalls()`, `ToolResults()`, `LastStep()`, `StepByTool(name)`. Pure
  functions over existing fields; identical shapes to the `Stream` accessors,
  so synchronous and streaming code use the same method names.
- **Stream event types.** `EventReasoningStart`/`Delta`/`End` (provider
  incremental reasoning), `EventHalt` (processor halts), `EventError`
  (terminal failures), `EventStreamWarning` (replay-truncated /
  subscriber-dropped), `EventToolApprovalPending` (approval gate).
  `EventThinking` remains; deprecated when providers port to the triplet.
- **Tool middleware chain.** `core.ToolMiddleware` + `oasis.WithToolMiddleware`
  with built-in `LoggingMiddleware`, `TimingMiddleware`,
  `TransformMiddleware`, and `OTelSpanMiddleware` (auto-applied when a
  `Tracer` is configured and not already in the user's chain).
  Innermost-first ordering matches `net/http`.
- **Framework-enforced tool approval.** `oasis.WithToolApproval(name, opts...)`
  pauses tool execution for human approval via the configured `InputHandler`.
  Built on the middleware chain — composes with logging, tracing, policy,
  and any custom middleware. Approve/deny decisions via `InputResponse.Value`;
  `DenyAskLLMToRevise` (default) returns an error `ToolResult` so the LLM
  can adapt, `DenyHalt` halts the run with `*core.ErrHalt`. Outermost layer
  of the chain — retries do not re-prompt. Emits `EventToolApprovalPending`
  on the stream before prompting.
- **Typed HITL contracts.** New `agent.SuspendProtocol[Req, Resp]` value (re-exported as `oasis.SuspendProtocol`) with constructor `NewSuspendProtocol[Req, Resp](name)` and methods `Suspend(Req)`, `PayloadFrom(*ErrSuspended) (Req, error)`, `Resume(*ErrSuspended, ctx, Resp)`, `ResumeStream(*ErrSuspended, ctx, Resp, ch)`, `WithRenderResume(func(Resp) string)`, and `Name()`. Compile-time contract between the suspending site and the caller that resumes — wrong payload or response type fails the build. Untyped `Suspend(json.RawMessage)` and `(*ErrSuspended).Resume` remain as the escape hatch. Also re-exports `Suspend` and `ErrSuspended` on the umbrella package (long-standing gap fixed). Spec: [`docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md`](docs/superpowers/specs/2026-05-22-typed-hitl-contracts-design.md).

### Deprecated

- `EventInputReceived`, `EventProcessingStart`, `EventMaxIterReached`, `EventHalt` are no longer emitted. The constants remain exported for one minor release for back-compat with consumers that type-switch on them. Replace with `EventRunStart` (for the first two) and `EventRunFinish{FinishReason: ...}` (for the last two).

### Migration

- Consumers iterating events should expect `EventRunStart` as the first event and `EventRunFinish` as the last. Code that triggered on `EventMaxIterReached` or `EventHalt` should switch on `EventRunFinish.FinishReason`.
- Code calling `result.Output` continues to work; `result.Text()` is identical.
- New `AgentResult` fields are zero-value by default; existing reads are unaffected.

### Removed

- **Reference app `cmd/bot_example/`** — no longer the integration gate.
- **Out-of-scope tool packages** — `tools/knowledge`, `tools/remember`, `tools/skill`, `tools/shell`, `tools/file`, `tools/search`, `tools/schedule`, `tools/todo`. Will be re-implemented inside their owner modules during Phase 2 / harness layer.
- Dead `subAgentConfig` alias in `agent/llm.go`.
- Root-package `scheduler.go` (`Scheduler`, `NewScheduler`, `ComputeNextRun`, `FormatLocalTime`, `RunHook`, `WithSchedulerInterval`, `WithSchedulerTZOffset`, `WithOnRun`). Re-add separately if needed.
- Transitional alias files (`types_aliases.go`, `processor_aliases.go`, `tool_aliases.go`, `types.go`, `skill.go`, `skill_builtin.go`, `skill_scan.go`, `skill_tool.go`). The aliases now live in `oasis.go`.
- Inline English compression prompt in `agent/loop.go` (replaced by `inlineCompactor`).

### Fixed

- **`forwardSubagentStream` double-close** routed through a single `sync.Once` (the actual bypass sites were the no-tools streaming path and synthesis path in `agent/loop.go`, plus `agent/suspend.go`'s resume path). The `recover()` in `onceClose` is removed; the real bypass paths are fixed. (finding 2.2.g)
- `Provider.ChatStream` doc no longer claims providers leave the channel open — every implementation closes it, matching the actual contract used by the agent loop.
- `ErrHalt` doc now clarifies that processors must return `&ErrHalt{...}` (pointer), not a value, to satisfy the `error` interface.
- Silent base64-decode swallow in `Attachment.InlineData()` — moved to construction time via `NewAttachmentFromBase64`.

### Migration notes

- The root `go.mod` no longer requires `modernc.org/sqlite`, `github.com/jackc/pgx/v5`, the OTEL stack, the Docker SDK, or `github.com/ledongthuc/pdf` directly. Apps that previously got those transitively must now explicitly import the matching satellite (e.g. `import _ "github.com/nevindra/oasis/store/sqlite"`).
- All re-exported types and functions from `oasis.*` retain their names. If your code uses `oasis.Provider`, `oasis.LLMAgent`, `oasis.WithCompaction`, `oasis.CosineSimilarity`, etc., no source change is needed.
- Direct imports of the satellites (`oasis/store/sqlite`, `oasis/provider/gemini`, etc.) are unchanged.
- Every external `Tool[In, Out]` implementation must: (1) Delete the `Name()` method. (2) Change `Definition() ToolDefinition` to `Definition() ToolMeta` and return only `{Name, Description}` (no `Parameters` field). (3) Add `describe:"..."` and (where applicable) `enum:"..."` tags to the `In` struct fields. (4) Delete the hand-written `Parameters: json.RawMessage(...)` block. For schemas reflection cannot express, implement `SchemaProvider.JSONSchema() json.RawMessage` on the input type. See `docs/guides/typed-tool-schemas.md` for a worked side-by-side example (Phase 1.5).
- Deferred MCP tool schemas + `ToolSearch` follow in next release (Plan α-2).

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

## [0.13.0] - 2026-03-29

### Added
- **ix — sandbox execution daemon** (`internal/ixd/`, `cmd/ix/`). Go stdlib HTTP daemon that runs inside sandbox containers, replacing gem-server/execd. Provides shell execution (SSE streaming), stateless code execution (Python, JS, Bash), and comprehensive file operations via REST API. Zero external dependencies.
- **Enhanced file operations** — `EditFile`, `GlobFiles`, `GrepFiles` methods on `Sandbox` interface. `file_edit` (surgical string replacement), `file_glob` (pattern search via `fd` with `**` support), `file_grep` (content search via `rg` with regex and context lines) tools. `ReadFile` now uses buffered line-by-line reading with line numbers (`cat -n` format) instead of loading entire files. `GlobFiles` and `GrepFiles` support `Exclude`, `Limit`, and `Context` parameters. 10-50x token savings vs read+rewrite for file edits.
- **Workspace tools for ix sandbox** — 3 new endpoints and 3 fixes to make the sandbox a fast, complete workspace for AI agents:
  - `POST /v1/file/tree` — recursive directory listing with depth control and exclude patterns. Uses `tree` command with native Go fallback.
  - `POST /v1/http/fetch` — URL fetching with readability text extraction (via `go-readability`). Clean text by default, `raw: true` for HTML.
  - `GET /v1/workspace/info` — environment discovery: OS, arch, working directory, installed tools (`rg`, `fd`, `git`, `python3`, `node`, etc.), and browser availability.
  - `file_tree`, `http_fetch`, `workspace_info` sandbox tools registered by default via `sandbox.Tools()`.
- `TreeRequest`, `TreeResult`, `HTTPFetchRequest`, `HTTPFetchResult`, `WorkspaceInfoResult` types on `sandbox` package.
- `Tree`, `HTTPFetch`, `WorkspaceInfo` methods on `Sandbox` interface.
- **File delivery** — `FileDelivery` interface + `deliver_file` tool. Agents can deliver sandbox files to users as downloadable chat attachments. Framework-level capability: apps implement `FileDelivery` to choose storage backend (S3, disk, etc.). Tool conditionally registered via `WithFileDelivery()` option.
- `ToolsOption` functional options for `sandbox.Tools()` — extensible tool registration without breaking the function signature.
- `EventFileAttachment` stream event type for file delivery notifications.
- `SkillProvider` interface for discovering and activating skills.
- `SkillWriter` interface for creating, updating, and deleting skills.
- `FileSkillProvider` — reads skills from directories, hot-reloads without restart.
- `SkillSummary` type for lightweight discovery results.
- **Document generation skills** — `oasis-design-system`, `oasis-pdf`, `oasis-docx`, `oasis-xlsx`, `oasis-pptx` skills in `skills/`. Agents generate PDF (HTML+Tailwind+Playwright), Word (JSON+python-docx), Excel (JSON+openpyxl), and PowerPoint (JSON+PptxGenJS) documents via the `oasis-render` CLI inside the sandbox.
- **`oasis-render` CLI** — unified entry point for document rendering (`bin/oasis-render`). Routes to format-specific renderers: `pdf`, `pdf-fill`, `docx`, `docx-fill`, `xlsx`, `pptx`.
- **Renderer scripts** — `renderers/pdf/render.js` (Playwright HTML->PDF), `renderers/pdf/fill.py` (pypdf form fill), `renderers/docx/generate.py` (python-docx), `renderers/docx/fill.py` (template fill), `renderers/xlsx/generate.py` (openpyxl), `renderers/pptx/compile.js` (PptxGenJS).
- **Sandbox Dockerfile** — single unified `cmd/ix/Dockerfile` with Chrome, Pinchtab, uv, document generation deps (python-docx, openpyxl, pypdf, PptxGenJS, Playwright), and oasis-render CLI.
- **`BuiltinSkillProvider`** — embedded skill provider that serves framework skills (`oasis-pdf`, `oasis-docx`, `oasis-xlsx`, `oasis-pptx`, `oasis-design-system`) from the compiled binary via `//go:embed`. Consumers get document generation skills without filesystem setup.
- **`ChainSkillProviders`** — merges multiple `SkillProvider` implementations. User file-based skills take priority over built-in ones. Typical usage: `ChainSkillProviders(fileProvider, builtinProvider)`.

### Changed
- **Sandbox Dockerfile** — Node.js 22 (nodesource) replaced with Node.js 25 (fnm), npm replaced with pnpm (via corepack), uv pinned to 0.11.2 with `--break-system-packages` for PEP 668 compliance on Ubuntu 24.04.
- **BREAKING:** `sandbox/aio` package renamed to `sandbox/ix`. `AIOSandbox` → `IXSandbox`, `AIOManager` → `IXManager`. Import path: `github.com/nevindra/oasis/sandbox/ix`.
- **BREAKING:** `IXSandbox` now communicates via SSE for shell/code execution (previously synchronous JSON). Client-side change only — `Sandbox` interface unchanged.
- **BREAKING:** `Sandbox.ReadFile` now accepts `ReadFileRequest` instead of a plain path string. `FileContent` gains `TotalLines` field.
- **BREAKING:** `Sandbox.GlobFiles` now returns `GlobResult` (with `Files` and `Truncated`) instead of `[]string`. `GlobRequest` gains `Exclude` and `Limit` fields.
- **BREAKING:** `Sandbox.GrepFiles` now returns `GrepResult` (with `Matches` and `Truncated`) instead of `[]GrepMatch`. `GrepRequest` gains `Context` and `Limit` fields. `GrepMatch` gains `ContextBefore` and `ContextAfter`.
- **BREAKING:** Skills are now file-based (folders with `SKILL.md`) instead of database-stored. Skill CRUD methods removed from `Store` interface. Use `SkillProvider` and `FileSkillProvider` instead.
- Skill tool now exposes `skill_discover` and `skill_activate` instead of `skill_search`. Progressive disclosure: discover returns names only, activate loads full instructions.
- All sandbox tool descriptions updated to guide agents toward dedicated tools and away from shell workarounds (e.g., "Use file_read instead of cat via shell").
- Default sandbox image changed from `ghcr.io/agent-infra/sandbox:latest` to `oasis-ix:latest`.
- Health check endpoint changed from `GET /v1/shell/sessions` to `GET /health`.
- Added GitHub CI workflow (`build-ix.yml`) to build and push ix sandbox Docker image on sandbox-related changes.

### Removed
- `sandbox/aio` package — replaced by `sandbox/ix`.
- `Store.CreateSkill`, `Store.GetSkill`, `Store.ListSkills`, `Store.UpdateSkill`, `Store.DeleteSkill`, `Store.SearchSkills` — replaced by `SkillProvider`.
- `ScoredSkill` type — no longer needed (no embedding-based search).
- `Skill.ID`, `Skill.Embedding`, `Skill.CreatedBy`, `Skill.CreatedAt`, `Skill.UpdatedAt` fields — replaced by filesystem metadata.
- `store/sqlite/skills.go`, `store/postgres/skills.go` — DB skill implementations.
- `cmd/sandbox/` — legacy sandbox service, superseded by `cmd/ix/`.
- `cmd/ix/Dockerfile.browser` — merged into `cmd/ix/Dockerfile` (single image with Chrome + document generation).

## [0.12.1] - 2026-03-19

### Fixed

- **`modelgen` output path** — generated files were written to a nested `provider/catalog/provider/catalog/` path instead of `provider/catalog/`. Default `-out` flag now uses relative paths since `go generate` runs from the package directory
- **Regenerated static registry** — 3836 models from models.dev (was 646 from OpenRouter) with full pricing, capabilities, modalities, and metadata. 82 auto-discovered platforms with base URLs and env vars

## [0.12.0] - 2026-03-19

### Added

- **models.dev integration** — model registry generator (`cmd/modelgen`) now fetches from `models.dev/api.json` instead of OpenRouter. 2600+ models with richer metadata: pricing, capabilities, modalities, cache pricing, model family, knowledge cutoff, and release dates
- **`Reasoning`, `StructuredOutput`, `Attachment` capabilities** — `ModelCapabilities` now tracks whether a model supports chain-of-thought reasoning (o3, DeepSeek-R1, Claude thinking), JSON structured output, and file/media attachments
- **`InputModalities`, `OutputModalities` on `ModelInfo`** — granular modality tracking (text, image, audio, pdf, video) replaces the coarse `Vision` boolean for model selection
- **`Family`, `OpenWeights`, `KnowledgeCutoff`, `ReleaseDate` on `ModelInfo`** — model family grouping, open-source flag, and temporal metadata for informed model selection
- **`CacheReadPerMillion`, `CacheWritePerMillion` on `ModelPricing`** — cache-aware pricing from models.dev. Enables accurate cost tracking for Gemini, Claude, and other providers with prompt caching
- **`EnvVars` on `Platform`** — standard environment variable names for API keys (e.g., `["OPENAI_API_KEY"]`), sourced from models.dev provider data
- **`PricingMap()` in `provider/catalog`** — returns `map[string]ModelPricing` from the static registry for initializing `CostCalculator` without API calls
- **`NewCostCalculatorFromModels`** — creates a `CostCalculator` from `[]ModelInfo` entries, with optional overrides. Bridges the catalog and observer packages
- **`platforms_gen.go` generated output** — `modelgen` now generates a second file with provider platform data (base URLs, env vars) discovered from models.dev, merged with manually curated builtins at catalog construction

### Changed

- **`CostCalculator.Calculate` is now cache-aware** — signature changed from `(model, input, output)` to `(model, input, output, cached)`. When cached tokens > 0 and the model has cache pricing, cached tokens are billed at the lower `CacheReadPerMillion` rate. **Breaking:** callers must add the 4th argument (pass `0` to preserve old behavior)
- **Observer emits `llm.tokens.cached` attribute** — span attributes and structured logs now include cached token counts alongside input/output
- **`enrichLiveWithStatic` handles new fields** — live/static merge now preserves Family, Modalities, KnowledgeCutoff, ReleaseDate, and OpenWeights from static data when live API doesn't provide them
- **CI workflow references models.dev** — `update-models.yml` now checks both `models_gen.go` and `platforms_gen.go` for changes, and PR descriptions reference models.dev

[Unreleased]: https://github.com/nevindra/oasis/compare/v0.14.0...HEAD
[0.14.0]: https://github.com/nevindra/oasis/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/nevindra/oasis/compare/v0.12.1...v0.13.0
[0.12.1]: https://github.com/nevindra/oasis/compare/v0.12.0...v0.12.1
[0.12.0]: https://github.com/nevindra/oasis/releases/tag/v0.12.0
