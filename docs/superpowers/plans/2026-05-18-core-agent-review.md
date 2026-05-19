# `core/` + `agent/` Framework Review

**Date:** 2026-05-18
**Status:** Findings report — verified against codebase 2026-05-18 (see "Verification corrections" below). **Phase 1 (type-safety release) shipped 2026-05-18** — see [Phase 1 shipped status](#phase-1-shipped-status-2026-05-18) at the bottom of this doc and per-finding ✅/⏳ markers throughout.
**Scope:** All source files (no tests) in `core/` (~1,500 LOC, 11 files) and `agent/` (~2,800 LOC, 8 files).

**Review axes (user-requested):**
1. Weird or incomplete design on our API
2. API we can simplify
3. DX improvements
4. Memory leaks / performance issues

**Filter:** Real issues only — citations with file:line. No nitpicks about formatting or comment style.

**Legend:** ✅ shipped in Phase 1 · ✅ shipped in Phase 1.5 · ⏳ deferred to a later phase · 🔁 reopened after partial fix

---

## 1. Weird or incomplete design

### 1.1 Bugs (real, not stylistic)

#### 1.1.a ✅ `AgentTask.WithThreadID/WithUserID/WithChatID` mutate a shared map

**Shipped:** Commit `85defbd`. `AgentTask.Context` map replaced with typed `ThreadID`/`UserID`/`ChatID`/`Extra` fields. With\*ID methods now do trivial field assignment — bug structurally impossible.

`core/agent.go:59-83`

```go
func (t AgentTask) WithThreadID(id string) AgentTask {
    if t.Context == nil {
        t.Context = map[string]any{}
    }
    t.Context[ContextThreadID] = id  // ← mutates caller's map
    return t
}
```

Value receiver looks immutable but the inner `map[string]any` is shared by reference. If `t.Context` was non-nil at call time, the caller's original task is mutated.

Bug triggers when a caller does `derived := orig.WithThreadID("x")` expecting `orig` untouched.

**Fix:** deep-copy Context when non-nil, or change to pointer receiver and document the mutation explicitly.

---

#### 1.1.b ✅ `Provider.ChatStream` contract contradicts the framework's own assumptions

**Shipped:** Commit `2da6291`. Doc rewritten to say "Implementations MUST close ch before returning." Aligns with reality.

`core/types.go:33-36`

The doc says:
> The channel is NOT closed by the provider — the caller owns its lifecycle.

But `agent/loop.go:262-273` (streaming with tools) does `for ev := range iterCh` after `provider.ChatStream(...)` returns. That for-range only exits when `iterCh` closes. Same expectation in `agent/retry.go:105-109`.

If a provider implementation followed the documented contract literally, **both paths would deadlock**. Tests pass, so real providers DO close the channel — meaning the doc is wrong, not the code.

Compare to `StreamingAgent.ExecuteStream` (`core/agent.go:32-34`) which correctly says "implementations MUST close ch before returning."

**Fix:** update Provider's doc to match the actual contract.

---

#### 1.1.c ✅ `Attachment.InlineData()` silently swallows base64 errors

**Shipped:** Commit `fadc396`. `Base64` field removed entirely. `InlineData()` is now infallible (returns `a.Data` directly). Decode errors surface at construction via `NewAttachmentFromBase64`.

`core/types.go:246-255`

```go
data, _ := base64.StdEncoding.DecodeString(a.Base64)
return data
```

Bad base64 returns `nil`. Caller can't distinguish "no inline data" from "corrupt inline data."

**Fix:** return `(data, error)` or at minimum log a warning.

---

### 1.2 Design smells (not bugs, but real noise)

#### 1.2.a ⏳ `Store` interface is a kitchen sink

**Deferred:** Phase 3 (structural breaking work). Spec to be written.

`core/store.go` — 49 LOC defining 25 methods across 6 unrelated concerns:
- Threads (5 methods)
- Messages (3 methods)
- Documents + Chunks (5 methods)
- Generic K/V config (2 methods)
- **Scheduled Actions (8 methods!)**
- Lifecycle (Init, Close)

`ScheduledAction` looks application-specific (has a `SkillID` field — `core/persistence.go:189`). The pattern for optional capabilities is already established: `KeywordSearcher`, `GraphStore`, `DocumentGetter`, `CheckpointStore` — all discovered via type assertion (`core/store_capabilities.go`).

**Fix:** apply the same pattern — move ScheduledActions, Config, possibly Documents/Chunks to optional capability interfaces. Mandatory `Store` should be ~Threads + Messages + Init/Close.

---

#### 1.2.b ✅ `Attachment` has three byte-source fields with `Base64` half-deprecated

**Shipped:** Commit `fadc396`. `Base64` field removed. Three constructors (`NewAttachment`, `NewAttachmentFromURL`, `NewAttachmentFromBase64`) replace the ambiguity. Two byte sources only: `URL` for remote, `Data` for inline.

`core/types.go:235-242`

`URL`, `Data`, `Base64`. `Base64` is documented Deprecated but still present in the struct, in JSON tags, and read by `InlineData()`.

**Fix:** decide — deprecate-and-remove in next breaking release, OR recognize it's not actually deprecated and drop the doc. Current half-state is the worst of both worlds.

---

#### 1.2.c ✅ `ChatMessage.Role` is `string` with magic values

**Shipped:** Commit `8f9bd20`. `type Role string` with `RoleSystem`/`RoleUser`/`RoleAssistant`/`RoleTool` constants. JSON wire format preserved. Existing string-literal comparisons still compile (Go permits comparing a defined string type to untyped string literals).

`core/types.go:222` — `"system" | "user" | "assistant" | "tool"`.

The pattern of `type X string` + typed constants already exists in the codebase (`CheckpointStatus`, `core/checkpoint.go:47-54`). Just not applied here.

**Fix:** `type Role string` with `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool` constants. Prevents typos, gives autocomplete.

---

#### 1.2.d ⏳ `Provider` forces every implementer to write streaming code

**Deferred:** candidate for Phase 3 (Provider capability split, alongside 1.2.a). Worth bundling because both follow the same "split into base + optional capability" pattern.

`core/types.go:37-41`

Compare to `Agent`/`StreamingAgent` split where streaming is optional and discovered via type assertion. A provider that only wants Chat must still implement ChatStream (probably by faking it via Chat).

**Fix:** split — `Provider` (Chat only) + `StreamingProvider` (Provider + ChatStream).

---

#### 1.2.e ⏳ `AgentCore` exports fields under awkward renames to dodge name clashes

**Deferred:** Phase 5 (AgentCore + network package unification). Architectural decision — needs its own design doc.

`agent/agentcore.go:23-55`

```go
LLMProvider      Provider        // (avoid clash with Provider interface name)
GenParams        *GenerationParams // (avoid clash with GenerationParams type name)
SpawnDepthLimit  int             // (avoid clash with MaxSpawnDepth option func)
DeniedSpawnTools []string        // (avoid clash with DenySpawnTools option func)
Handler          InputHandler    // (avoid clash with InputHandler interface name)
```

These rename gymnastics signal the package boundary is wrong. They exist only so the `network/` subpackage can read internals via field access.

There's also a large set of "exported for network subpackage access" helpers in `loop.go`/`agentcore.go`: `InitCore`, `BaseLoopConfig`, `CacheBuiltinToolDefs`, `ResolvePromptAndProvider`, `ResolveDynamicTools`, `ExecuteWithSpan`, `ExecuteAgent`, `ExecuteSpawnAgent`, `DispatchBuiltins`, `DispatchTool`. Huge public surface area existing only to support one consumer.

**Fix:** move `AgentCore` to `internal/agentcore/` shared by both, OR move `Network` back into the `agent/` package. This is connected to the audit doc's #4 finding — Network and Agent legitimately share ~30/31 options, so they should probably not be in separate packages.

---

#### 1.2.f ⏳ Three different mechanisms to shrink history with overlapping semantics

**Deferred:** still open. The option-surface portion was already shipped (`WithHistory`); the runtime-mechanism overlap remains and is candidate for Phase 2.

Already flagged by `2026-05-18-dx-improvements-audit.md` #1.

The top-level option functions were already collapsed into `history.*` sub-options consumed by `WithHistory` (`agent/agent.go:141-163`), but the three underlying mechanisms still co-exist as separate fields on `agentConfig`:
- `compressModel` / `compressThreshold` (per-turn rune-count) — agent/agent.go:56-57
- `compactor` / `compactThreshold` (per-thread) — agent/agent.go:58-59
- `semanticTrimming` / `trimmingEmbedding` (relevance-based) — agent/agent.go:61-62

Semantic overlap remains; the option surface is cleaner than first reported, but the runtime still chooses among three independent strategies with no documented precedence.

---

#### 1.2.g ⏳ `agentConfig.embedding` is set by both `WithUserMemory` and `CrossThreadSearch`

**Deferred:** small surgical fix, slot into Phase 2 alongside 1.2.f (both touch memory/history wiring).

`agent/agent.go:367` (WithUserMemory) and `agent/agent.go:150` (WithHistory → `history.CrossThreadSearch`)

Last writer wins. Undocumented. If both options use different embedding providers, silently the wrong one is used.

**Fix:** either reject the combination at `BuildConfig` time with a clear error, OR support separate embedding fields per use case.

---

#### 1.2.h ~~`agentConfig.SkillProvider` is uppercase~~ — RESOLVED (verification 2026-05-18)

Originally flagged as a speculative export. Verification found `network/network.go:42-46` actively reads `cfg.SkillProvider` (`skills.NewSkillTools(...)`). The export is now justified by a real consumer. **No action needed.**

---

#### 1.2.i ✅ `Tool[In, Out]` has no streaming counterpart

**Shipped:** Commit `5f556fb`. `StreamingTool[In, Out]` interface and `EraseStreaming[In, Out]` function added. Re-exported from oasis umbrella in commit `8f9bd20`.

`core/tool.go:38-44`

`AnyTool` has `StreamingAnyTool` (`core/tool.go:33`). `Tool[In, Out]` does not. If you want type-safe authoring AND streaming, you must drop down to AnyTool — losing the type safety.

**Fix:** add `StreamingTool[In, Out]` interface mirroring the AnyTool relationship.

---

#### 1.2.j ✅ `ErrHalt` inconsistent value/pointer usage

**Shipped:** Commit `2da6291`. Doc rewritten to specify `return &core.ErrHalt{...}` (pointer) and explain that only `*ErrHalt` satisfies the `error` interface because `Error()` has a pointer receiver.

`core/processor.go:36-40`

```go
func (e *ErrHalt) Error() string { return "processor halted: " + e.Response }
```

Pointer receiver, so `errors.As(err, &halt)` matches `*ErrHalt` but `errors.As(err, ErrHalt{...})` won't. The doc just says "Return ErrHalt to short-circuit" — should specify `&ErrHalt{...}`.

---

#### 1.2.k ⏳ `Sandbox any` typed field

**Deferred:** needs a small `core.Sandbox` interface design — slot into Phase 2.

`agent/agent.go:42`

Holds "a sandbox.Sandbox" per comment but typed as `any`. Reasonable to avoid the dep on the sandbox/ satellite, but means runtime type assertion.

**Fix:** define a minimal `core.Sandbox` interface that the satellite implements. Then `agent` can hold a typed value.

---

## 2. API simplification opportunities

### 2.1 Already on the radar (audit doc)

- **#1 in audit doc:** Collapse 31 `With*` options into ~8 grouped config structs (`Generation`, `Limits`, `History`, `Resolvers`, `Spawn`, …)
- **#2 in audit doc:** Typed processors instead of `WithProcessors(...any)`

### 2.2 New simplification proposals

#### 2.2.a Split `Store` interface using existing capability pattern

`core/store.go`'s 8 ScheduledAction methods → `ScheduledActionStore` optional capability. Same for Config (2 methods) and possibly Documents (3 methods). Follows the established pattern in `core/store_capabilities.go`.

---

#### 2.2.b Collapse scoped option types after grouping

`agent/agent.go:229, 352, 376, 442`

Four scoped option types (`ConversationOption`, `SemanticOption`, `SemanticTrimmingOption`, `SubAgentOption`) with ~5 functions total. The compile-time-scoping payoff is real, but ratio of types to functions is high. With grouped configs (audit #1), most collapse into struct fields.

---

#### 2.2.c ✅ Replace magic-string `AgentTask.Context` keys with `TaskMeta` struct

**Shipped:** Commit `85defbd`. Chosen approach: flat typed fields on `AgentTask` (not a `TaskMeta` sub-type) plus an `Extra map[string]any` for app-defined keys — matches the codebase's preference for flat structs (e.g. `ChatMessage`). Migration also propagated to `network/network.go` and `workflow/steps.go` (sub-task construction).

`core/agent.go:51-107`

3 constants + 3 setters + 3 getters → one typed `TaskMeta` struct (ThreadID, UserID, ChatID). Loses the "anyone can attach metadata" extensibility — but that's currently undocumented and the keys are reserved internal-only anyway.

---

#### 2.2.d Split `loop.go` (870 LOC)

`runLoop` alone is ~380 lines doing 10+ jobs (blocking vs streaming, with-tools vs no-tools, normal vs synthesis, error/suspend handling, compression, attachment accumulation, tracing).

**Suggested split:**
- `dispatch.go` — `DispatchTool`, `DispatchBuiltins`, `dispatchParallel`, `safeDispatch`
- `compress.go` — `compressMessages`, `runeCount`
- `synthesis.go` — the forced-synthesis tail (lines 491-541)
- `routing.go` — `buildRoutingSummary`
- `strings.go` — `TruncateStr` (currently in loop.go for no clear reason)

---

#### 2.2.e Split `agentcore.go` (419 LOC)

Mixes AgentCore + subagent dispatch helpers + `onceClose` generic + `safeAgentError`. Extract `subagent.go` for the subagent-related helpers.

---

#### 2.2.f ✅ Remove `subAgentConfig = SubAgentConfig` alias

**Shipped:** Commit `5a0992b`. Two-line deletion. No callers remained.

`agent/llm.go:368` — explicitly labeled "for backward compatibility." Internal refactor; delete the alias.

---

#### 2.2.g 🔁 Remove dead `recover()` in `onceClose`

**Reopened.** Initial removal in commit `5a0992b` immediately caused `TestLLMAgentExecuteStreamNoTools` to panic with `close of closed channel`. The recover was masking a real race — `forwardSubagentStream` has another close path that bypasses `onceClose`'s `sync.Once`. The recover was restored in commit `ba3b912` with a comment explaining why; the underlying double-close ownership bug remains visible (but not fixed) and is a candidate for Phase 2. The right fix is to identify the external close path and route it through the same `onceClose` instance, not to keep the recover.

`agent/agentcore.go:410`

```go
once.Do(func() {
    defer func() { recover() }()
    close(ch)
})
```

`sync.Once` already prevents double-close. The recover is defensive dead code. Remove.

---

## 3. DX improvements

### 3.1 ⏳ Biggest single DX win: typed tool schemas

**Deferred:** Phase 1.5 (its own dedicated spec). Design-heavy (reflection rules, supported Go types, escape hatches for raw JSON Schema). The Phase 1 design doc explicitly carved this out because it deserves dedicated brainstorming. **This is the next phase to design.**

`core/types.go:340`

Tool authors today write raw JSON Schema strings:

```go
Parameters: json.RawMessage(`{"type":"object","properties":{...},"required":[...]}`),
```

Bad JSON in registration panics at LLM-call time. `SchemaObject` already exists (`core/types.go:290-304`) for `ResponseSchema` but not for tools.

**Two options:**

**Option A — `NewToolDefinition(name, desc, in *SchemaObject)`** — mirrors the existing `NewResponseSchema`. Low effort.

**Option B — Generic schema derivation from struct types** — `core.Erase(t)` already takes a `Tool[In, Out]`. We can derive the JSON Schema from `In` via reflection (one-time at registration). Then a tool author writes:

```go
type SearchIn struct {
    Query string `json:"query" describe:"the search query"`
    Limit int    `json:"limit,omitempty" describe:"max results, default 10"`
}
type SearchOut struct { ... }

func (t *SearchTool) Name() string { return "search" }
func (t *SearchTool) Execute(ctx context.Context, in SearchIn) (SearchOut, error) { ... }
// Definition() is auto-generated or replaced by a default impl.
```

Order-of-magnitude DX win for the most common authoring path. Option B is the right ambition; Option A is the cheap stepping stone.

---

### 3.2 `core` package documentation says "don't import directly"

`core/doc.go:5`

> User code should not import this package directly. Use the curated re-exports from github.com/nevindra/oasis instead.

But `core` contains the foundational stable types. Forcing users through `oasis.Chunk = core.Chunk` aliases adds indirection without protection.

**Reconsider:** `core` is stable enough to be importable. The umbrella exists for ergonomics, not as a wall.

---

### 3.3 `Provider` interface lacks a `Capabilities()` method

No way to ask a provider "do you support streaming / tools / image input / what's max context?" — must try and see what fails. The `ModelCapabilities` struct exists in `core/catalog.go:58-66` (used by `ModelInfo`) but `Provider` doesn't carry it.

---

### 3.4 `AgentHandle.State()` blocks if state is terminal

`agent/handle.go:149-155`

```go
func (h *AgentHandle) State() AgentState {
    s := AgentState(h.state.Load())
    if s.IsTerminal() {
        <-h.done  // ← BLOCKS
    }
    return s
}
```

Documented as "blocks until Done() is closed (nanoseconds)" — but "nanoseconds" glosses over the surprise. A caller doing `if h.State().IsTerminal() { … }` does not expect to block.

**Fix:** split into `State()` (non-blocking, returns whatever the atomic shows) and `Sync()` (waits for happens-before barrier).

---

### 3.5 ✅ `AgentCore.Drain()` is required at shutdown but easy to forget

**Shipped:** Commit `57ce18f`. Renamed to `Close() error` on both `AgentMemory` and `AgentCore`. Matches the stdlib `io.Closer` convention. Returns nil today; the error return reserves the slot for future flush failures (remote stores, network drains) so a second breaking rename isn't needed later. Finalizer / synchronous-persist alternatives were considered and rejected in the Phase 1 design.

`agent/agentcore.go:137`

If forgotten, last messages are silently lost. No enforcement.

**Options:**
- Rename to `Close() error` (matches stdlib lifecycle convention).
- Add a finalizer with a warning log.
- Run the persist synchronously (defeats the perf win, but is correct by default).

---

### 3.6 `maxIter` defaults to 10 with no rationale

`agent/agentcore.go:16`

Many real workflows need >10 iterations. When the limit is hit, the framework forces synthesis (`loop.go:492-494`) — an extra billed LLM call. The WARN log is the only signal.

**Fix:** surface as a `StreamEvent` (e.g., `EventMaxIterReached`) so UIs can show it. Document the cost loudly. Possibly raise default to 25-30.

---

### 3.7 Tool result truncation at 100K runes silently drops content

`agent/loop.go:120, 470-472`

Truncation marker `[output truncated — original was longer]` is appended, but the LLM has no way to ask for the rest.

**Fix:** store full content out-of-band (e.g., in a `ToolResultStore` keyed by ID), hand back the truncated content + a `result_id`. Add a built-in `read_full_result(result_id, offset, length)` tool. Closes the loop without unbounded memory growth.

---

### 3.8 Hardcoded constants with no `With*` knobs

| Constant | File:line | Tunable? |
|---|---|---|
| `maxToolResultMessageLen = 100_000` | loop.go:120 | No |
| `maxAccumulatedAttachments = 50` | loop.go:125 | No (bytes are: `WithMaxAttachmentBytes`) |
| `maxParallelDispatch = 10` | loop.go:133 | No |
| `maxPlanSteps = 50` | llm.go:172 | No |
| `defaultMaxIter = 10` | agentcore.go:16 | Yes (`WithMaxIter`) |
| `defaultSuspendTTL = 30 * time.Minute` | suspend.go:24 | Yes (`WithSuspendTTL`) |
| `defaultMaxSuspendSnapshots = 20` | suspend.go:26 | Yes (`WithSuspendBudget`) |
| `defaultMaxSuspendBytes = 256MB` | suspend.go:27 | Yes (`WithSuspendBudget`) |

For a "high-performance framework" identity, `maxParallelDispatch` especially deserves a knob.

---

### 3.9 `compressMessages` uses a hardcoded English prompt

`agent/loop.go:635-638`

```go
SystemMessage("Summarize the following tool execution results concisely. Preserve key facts, data values, decisions, and errors. Omit redundant details."),
```

Critical operation, generic prompt, no localization, no per-agent customization. The framework already has a `Compactor` interface (`core/compactor.go`) that does this properly.

**Fix:** route compression through the existing `Compactor` interface rather than the inline prompt. This path predates the Compactor abstraction.

---

### 3.10 ✅ No `core.NewAttachment(mime, data)` constructor

**Shipped:** Commit `fadc396`. Three constructors: `NewAttachment(mime, data)`, `NewAttachmentFromURL(mime, url)`, `NewAttachmentFromBase64(mime, encoded) (Attachment, error)`. Re-exported from `oasis` umbrella.

Users must remember which of `Data`/`URL`/`Base64` to populate. A constructor enforces correct usage.

---

## 4. Memory & performance

### 4.1 Real concerns

#### 4.1.a `iterCh` allocated per main-loop iteration

`agent/loop.go:257`

```go
iterCh := make(chan StreamEvent, 64)
```

64 events × ~150 bytes each ≈ 10 KB per iteration, allocated and abandoned every iteration. With `maxIter=10`, ~100 KB per Execute. Could be reused via `sync.Pool`.

---

#### 4.1.b `messages []ChatMessage` grows via unbounded append

`agent/loop.go:369, 474`

Long runs (lots of tool calls) cause several reallocations. Pre-allocating `make([]ChatMessage, 0, maxIter*4)` would avoid most reallocations. Each `ChatMessage` is ~200 bytes empty, more with content.

---

#### 4.1.c `compressMessages` allocates `compressed` via append

`agent/loop.go:650`

Could pre-allocate `make([]ChatMessage, 0, len(messages)-len(toRemove)+1)`.

---

#### 4.1.d `Message.Embedding` and `Chunk.Embedding` are large `[]float32`

`core/persistence.go:166` (Message), `core/persistence.go:40` (Chunk), `core/persistence.go:175` (Fact) — 384-dim embedding = 1.5 KB. 768-dim = 3 KB.

When `GetMessages(threadID, limit=100)` is called, does the implementation load embeddings? The interface gives no way to ask "without embeddings." Per-call DX/perf footgun for any consumer that doesn't need them.

**Fix:** either a `GetMessagesWithoutEmbeddings` capability, or move embeddings to a separate query / lazy-loader.

---

#### 4.1.e `ToolRegistry.Remove` is O(N)

`core/types.go:123-137` — scans `r.tools` to filter. Acceptable for ~10-50 tools, but inconsistent with the O(1) `index` map used elsewhere.

**Fix:** swap-and-truncate if order doesn't matter, or accept this if Remove is rare (probably is).

---

#### 4.1.f `ParseRetryAfter` uses `fmt.Sscanf`

`core/types.go:391` — `fmt.Sscanf("%d", ...)` is ~100× slower than `strconv.Atoi`. Not hot-path, but a free win.

---

#### 4.1.g `AgentCore.suspendMu sync.Mutex` + atomic counters

`agent/agentcore.go:43-45`, `suspend.go:253-265`

Mixed approach: atomics for the counters, mutex to make check-then-add atomic. Functional but slightly heavy. A single mutex would simplify and the perf delta is noise (suspend is rare).

---

### 4.2 Non-issues I checked (false alarms)

- `dispatchParallel` (`loop.go:761`): bounded worker pool, correct ctx-cancel handling, fast-path single-call. Solid.
- `AgentHandle` synchronization: writes-then-`close(done)` provides happens-before; readers via `<-h.done`. Correct.
- `CosineSimilarity`: single allocation-free pass.
- `ToolRegistry.Execute`: O(1) via index map.
- `safeDispatch`'s `defer recover()`: ~50ns/call overhead — negligible.
- `onceClose` correctness: `sync.Once` prevents double-close. Correct (just has a dead `recover()` defer — see 2.2.g).

---

## Top 10 fixes ranked by impact / cost ratio

| # | Item | Section | Why it matters |
|---|---|---|---|
| 1 | Fix `AgentTask.With*ID` map-sharing bug | 1.1.a | Real, silent correctness bug. ~5-line fix. |
| 2 | Fix `Provider.ChatStream` doc — say "MUST close ch" | 1.1.b | Doc misrepresents the contract every consumer relies on. Either fix doc (cheap) or fix all callers (expensive). |
| 3 | `Attachment.InlineData` should return error or log | 1.1.c | Silent failure. Trivial. |
| 4 | Typed tool schemas (`NewToolDefinition` or generics) | 3.1 | Biggest single DX win for tool authors — most-common authoring path. |
| 5 | Split `Store` interface — move ScheduledActions etc. to capabilities | 1.2.a, 2.2.a | Largest ISP violation in the codebase; pattern already exists. |
| 6 | Move `AgentCore` to internal/ or unify agent+network packages | 1.2.e | Removes ~15 awkward exports and a dozen "exported for network" rename comments. Connects to audit doc #4. |
| 7 | Group history-management options (audit doc #1) | 1.2.f | Already on radar. Confirmed: 3 mechanisms with overlapping semantics. |
| 8 | ~~Typed processors (audit doc #2)~~ | — | **Shipped 2026-05-18 (commit `ba9cbd7`).** |
| 9 | Split `loop.go` into 4-5 focused files | 2.2.d | 870 LOC + 380-line `runLoop` — hard to maintain heart of the framework. |
| 10 | Surface tool-result truncation to the LLM (return a `result_id`) | 3.7 | Silently dropping 80% of a tool's output is a correctness footgun. |

**If forced to pick three to do first:** #1 (correctness bug), #2 (doc/contract clarity blocking refactor confidence), #4 (biggest user-facing DX win).

Items #5 and #6 are the structural wins but each is a small project.

---

## Cross-references to existing audit

- **Audit doc #1** (collapse 31 options into grouped configs) — confirmed; supported by 1.2.f and 2.2.b here.
- **Audit doc #2** (typed processors) — confirmed.
- **Audit doc #3** (example app) — out of scope for this review.
- **Audit doc #4** (split NetworkOption/AgentOption) — already invalidated in the audit doc itself. This review reinforces: the structural fix is "move Network closer to Agent" (1.2.e), not "split the option type."
- **Audit doc #5** (move `cmd/ix`) — out of scope.

---

## Phase 1 shipped status (2026-05-18)

Phase 1 type-safety release shipped on 2026-05-18 as a single breaking minor bump. Source: [docs/superpowers/specs/2026-05-18-phase-1-type-safety-design.md](../specs/2026-05-18-phase-1-type-safety-design.md). Plan: [docs/superpowers/plans/2026-05-18-phase-1-type-safety.md](2026-05-18-phase-1-type-safety.md).

### Shipped (✅)

| Finding | Commit |
|---|---|
| 1.1.a — AgentTask map-sharing bug | `85defbd` |
| 1.1.b — Provider.ChatStream doc | `2da6291` |
| 1.1.c — Attachment.InlineData silent decode | `fadc396` |
| 1.2.b — Attachment Base64 half-deprecated | `fadc396` |
| 1.2.c — ChatMessage.Role magic strings | `8f9bd20` |
| 1.2.i — Missing StreamingTool[In, Out] | `5f556fb`, umbrella in `8f9bd20` |
| 1.2.j — ErrHalt doc | `2da6291` |
| 2.2.c — Magic-string AgentTask.Context keys | `85defbd` |
| 2.2.f — subAgentConfig alias removal | `5a0992b` |
| 3.5 — Drain → Close(error) | `57ce18f` |
| 3.10 — NewAttachment constructor | `fadc396` |

Also shipped in Phase 1 but not from this review: the `WithProcessors(...any)` → typed processors refactor (audit #2, commit `ba9cbd7` on 2026-05-18).

Repo-wide verification at end of Phase 1: **658 root tests pass with `-race`**, all 9 satellites green, six grep checks for stale references clean, CHANGELOG updated (commit `cab29cd`).

### Reopened (🔁)

- **2.2.g** — Dead `recover()` in `onceClose`. Removal in `5a0992b` panicked `TestLLMAgentExecuteStreamNoTools`; restored in `ba3b912`. The double-close ownership bug is real and visible; fix slated for Phase 2.

### Phase 1 sequencing in retrospect

3 waves of parallel subagents:
- **Wave 1** (3 parallel): Track A (AgentTask), C-Doc (Provider + ErrHalt), D1 (StreamingTool)
- **Wave 2** (2 parallel): Track B (Attachment), D-Cleanup (alias + recover)
- **Wave 3** (sequential — shared `memory_orchestration.go`): C-Role (Role + umbrella), D-Close (Drain → Close)
- **Wave 4** (single): final verification + CHANGELOG

Parallelism worked because oasis.go re-exports were deferred to Wave 3 and breaking type changes were grouped into atomic per-track tasks. The one misstep (D5 recover removal) was caught by an existing test, not in production.

### Next phase candidates

Items still open after Phase 1, grouped by candidate phase per the Phase 1 design doc:

| Candidate phase | Theme | Findings |
|---|---|---|
| **Phase 1.5** | Typed tool schemas | ✅ 3.1 (highest-impact remaining single DX item; explicitly carved out of Phase 1 for its own spec) |
| **Phase 2** | Memory/history coherence + small fixes | 1.2.f, 1.2.g, 1.2.k, 2.2.g, 3.4, 3.6, 3.7, 3.8, 3.9 |
| **Phase 3** | Capability splits | 1.2.a (Store), 1.2.d (Provider), 4.1.d (Embedding load opt-out), 4.1.e (Remove ToolRegistry) |
| **Phase 4** | Mechanical file splits + perf | 2.2.d (loop.go), 2.2.e (agentcore.go), 4.1.a–c, 4.1.f, 4.1.g |
| **Phase 5** | Architectural unification | 1.2.e (AgentCore + network packaging), 3.2 (core/ "don't import" gate), 3.3 (Provider.Capabilities) |

The natural next step is **Phase 1.5 — typed tool schemas** per the design doc's explicit handoff. Brainstorming for that phase begins next.

---

## Verification corrections (2026-05-18)

Five parallel agents re-checked every finding against the live codebase. Net result: **bugs and structural findings hold**, with these corrections:

| Finding | Correction |
|---|---|
| 1.2.a | Store has **25** methods, not 24. `ScheduledAction.SkillID` is at `core/persistence.go:189`, not `core/types.go:189`. |
| 1.2.f | The named `With*` options no longer exist as top-level (already collapsed into `history.*` sub-options). The three *runtime mechanisms* still co-exist as `agentConfig` fields — see updated text above. |
| 1.2.g | Second `c.embedding` write is at `agent/agent.go:150` (routed through `WithHistory`), not line 502. |
| **1.2.h** | **RESOLVED.** `network/network.go:42-46` actively reads `cfg.SkillProvider`. Export is justified. No action. |
| 3.3 | Type is `ModelCapabilities` struct (used by `ModelInfo`), not a `ModelInfo.Capabilities` field. |
| 3.5 | `Drain()` is at `agent/agentcore.go:137`, not 131. |
| 4.1.d | Embedding fields live in `core/persistence.go`, not `core/types.go` (lines 40 / 166 / 175). |

All other findings — including the three correctness bugs in §1.1, the AgentCore export pattern (1.2.e), the typed-tool-schema gap (3.1), and every perf claim in §4.1 — verified exactly as written.
