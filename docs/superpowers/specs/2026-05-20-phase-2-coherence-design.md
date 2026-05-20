# Phase 2 — Coherence Release Design

**Date:** 2026-05-20
**Status:** Approved design — ready for implementation planning
**Predecessors:**
- [Phase 1 type-safety design](2026-05-18-phase-1-type-safety-design.md) (shipped 2026-05-18)
- [Phase 1.5 typed tool schemas design](2026-05-18-phase-1.5-typed-tool-schemas-design.md) (shipped)

**Source review:** [docs/superpowers/plans/2026-05-18-core-agent-review.md](../plans/2026-05-18-core-agent-review.md) — findings 1.2.f, 1.2.g, 1.2.k, 2.2.g, 3.4, 3.6, 3.7, 3.8, 3.9.

---

## Theme

**Coherence.** Stop silent footguns and unify overlapping mechanisms — without moving package boundaries (those wait for Phase 3).

Phase 1 fixed type safety. Phase 1.5 fixed tool-authoring DX. Phase 2 fixes **silent behavior** — places where the framework drops data, picks the wrong setting, or blocks unexpectedly without telling the user.

---

## Release shape

Single breaking minor bump (v0.next). All 9 findings ship together as one atomic migration. Rationale: each individual change is small; bundling gives users a single coherent migration story instead of three drip-fed releases.

**Three breaking changes** (each small and mechanical):
1. `AgentHandle.State()` becomes non-blocking; new `Sync()` for happens-before barrier.
2. `agentConfig` rejects conflicting embedding providers at build time (was silent last-writer-wins).
3. `Sandbox any` → typed `core.Sandbox` interface.

**One observable behavior change** (non-breaking compile, but visible):
- Default `maxIter` raises from 10 → 25.

---

## Goals

1. **No silent data loss.** Tool results over budget remain accessible via paging instead of vanishing.
2. **No silent misconfiguration.** Conflicting embedding providers fail loudly at construction.
3. **No surprise blocking.** `State()` returns immediately; `Sync()` is the explicit barrier.
4. **One summarization abstraction.** The three history-shrink mechanisms cascade through a single `Compactor` interface where applicable; ordering is documented.
5. **Observable iteration cap.** `EventMaxIterReached` lets UIs surface the forced-synthesis path.
6. **Tunable hot paths.** `maxParallelDispatch`, `maxToolResultLen`, `maxPlanSteps` become `With*` knobs.

## Non-goals

- Package boundary changes (`Store` capability split, `Provider` capability split, AgentCore unification) — Phase 3.
- File splits (`loop.go`, `agentcore.go`) — Phase 4.
- Micro-perf optimizations (sync.Pool for `iterCh`, pre-allocated slices) — Phase 4.
- New embedding semantics or RAG features — out of scope.
- Provider `Capabilities()` method — Phase 5.

---

## 1. Architecture changes

### 1.1 Tool-result paging (finding 3.7)

**Problem:** results over `maxToolResultMessageLen = 100_000` runes are silently truncated. The LLM sees `[output truncated — original was longer]` and has no way to retrieve the rest. This is a correctness issue for any tool returning large payloads — search, file reads, DB queries, log analysis.

**Solution:** new optional `ToolResultStore` capability + auto-registered `read_full_result` built-in tool.

#### Interface (in `core/tool_result_store.go`)

```go
// ToolResultStore holds full tool results when their content exceeds the
// inline budget. The LLM retrieves slices via the read_full_result built-in.
//
// Implementations should be safe for concurrent use. The default in-memory
// implementation is bounded by total bytes and per-entry TTL.
type ToolResultStore interface {
    // Put stores the full content and returns an opaque id. The id is
    // included in the truncation marker handed to the LLM.
    Put(ctx context.Context, content string) (id string, err error)

    // Get returns a slice of the stored content starting at offset runes,
    // up to length runes. total is the full rune count of the stored
    // content (so the LLM can tell whether more remains).
    // Returns an error if the id is unknown or expired.
    // If offset >= total, returns empty content with no error.
    Get(ctx context.Context, id string, offset, length int) (content string, total int, err error)
}
```

#### Default implementation

`core.NewInMemoryToolResultStore(opts ...InMemoryStoreOption)` — bounded LRU:

- Total cap: 10 MiB (10 × 1024 × 1024 bytes) across all entries (configurable via `WithMaxBytes(n int64)`)
- TTL per entry: 5 minutes (configurable via `WithTTL(d time.Duration)`)
- Eviction: LRU when cap exceeded; lazy time-based expiry (expired entries removed on next Get or Put, not via background goroutine)
- Concurrency: `sync.RWMutex` — reads are common (Get), writes rare (Put)

#### Truncation path (`agent/loop.go`)

When a tool result's rune count exceeds `maxToolResultLen`:

- **Store configured:** `id, _ := store.Put(ctx, full)`, then the message handed to the LLM contains the first `maxToolResultLen` runes followed by:
  ```
  [truncated at <N> runes of <M> total. Use read_full_result(id="<id>", offset=<N>, length=<K>) for more]
  ```
- **No store (`WithToolResultStore(nil)`):** today's behavior — append `[output truncated — original was longer]`, full content lost.

#### Built-in tool

`read_full_result(id, offset, length)` — auto-registered on every Agent that has a `ToolResultStore` configured (which by default is all of them).

- Returns the requested slice plus a continuation marker if more remains.
- Errors with a clear message if `id` is unknown / expired.
- Implemented via existing `Tool[In, Out]` (typed schema from Phase 1.5).

#### Options

- `WithToolResultStore(store ToolResultStore)` — override default; pass `nil` to disable paging entirely.
- `WithMaxToolResultLen(n int)` — override inline budget. Default stays `100_000`.

#### Default state

**On by default, bounded tight:** every Agent gets `NewInMemoryToolResultStore()` with the 10 MB / 5 min defaults. Power users opt out via `WithToolResultStore(nil)` or override via `WithToolResultStore(custom)`.

Rationale: this is the user-facing default, so it needs to work without explicit configuration. The tight bounds (10 MB total, 5 min TTL) prevent unbounded memory growth for users who don't think about it; power users override.

---

### 1.2 History cascade + Compactor unification (findings 1.2.f + 3.9)

**Problem (1.2.f):** three independent mechanisms shrink history with no documented precedence:
- `compressModel` / `compressThreshold` — per-turn rune-count compression (inline summarization of old tool results)
- `compactor` / `compactThreshold` — per-thread full-history summarization (uses the `Compactor` interface)
- `semanticTrimming` / `trimmingEmbedding` — relevance-based culling

A user enabling all three has no way to know which fires first.

**Problem (3.9):** `compressMessages` at `agent/loop.go:574` predates the `Compactor` interface and uses a hardcoded English prompt at `loop.go:635-638`. Critical operation, no localization, no per-agent customization.

**Solution:** document the cascade + route `compressMessages` through `Compactor`.

#### The cascade (documented in `docs/concepts/memory.md`)

```
Stage 1: Semantic trim       (cheap)   — drops off-topic messages, relevance-based
Stage 2: Compress tool results (medium) — summarizes old tool-result messages in-place
Stage 3: Compact full thread (expensive) — full conversation synopsis
```

Each stage has its own threshold and runs independently when its threshold is exceeded. They are **layered**, not alternatives — a user can enable any combination.

#### Code change: route compress through Compactor

`core.CompactRequest` gains a `Scope` field:

```go
type CompactScope int

const (
    ScopeFull              CompactScope = iota // today's behavior — full thread
    ScopeToolResultsOnly                        // only summarize tool-result messages
)

type CompactRequest struct {
    Messages []ChatMessage
    Scope    CompactScope    // NEW. Default ScopeFull preserves existing behavior.
    // ... existing fields unchanged
}
```

`compressMessages` (loop.go:574) is refactored to:
1. Build a `CompactRequest{Messages: oldToolResults, Scope: ScopeToolResultsOnly}`
2. Invoke the configured `Compactor` (or a default `inlineCompactor` if none configured)
3. Replace the old tool-result messages with the returned summary

The default `inlineCompactor` retains today's English prompt as a fallback, but users can now swap it via `WithCompactor(custom)` and have their custom Compactor handle both `ScopeFull` and `ScopeToolResultsOnly` cases.

#### User-facing API

**Unchanged.** `WithSemanticTrimming`, `WithCompactor`, `compressModel`/`compressThreshold` all keep working. The refactor is internal — `compressModel` now drives the default inline Compactor instead of being a separate code path.

**Why no `WithHistoryStrategy` umbrella:** the three stages solve genuinely different problems. Collapsing them loses the layering, which is the feature. A future convenience helper could be added without breaking changes.

---

### 1.3 `AgentHandle.State()` split (finding 3.4)

**Problem:** `State()` (`agent/handle.go:149-155`) blocks on `<-h.done` when the state is terminal, to establish happens-before with final result writes. The doc says "blocks nanoseconds" but a caller doing `if h.State().IsTerminal() {…}` does not expect to block, and "nanoseconds" can become "unbounded" if the loop hangs.

**Solution:** split into non-blocking `State()` + explicit `Sync()`.

#### New API

```go
// State returns the current agent state without blocking. If the state is
// terminal, the returned value reflects the atomic snapshot but writes by
// the agent loop may not yet be visible. Call Sync to establish a
// happens-before barrier before reading results.
func (h *AgentHandle) State() AgentState

// Sync blocks until the agent's writes are visible to this goroutine.
// Returns immediately if the state is non-terminal (nothing to wait for yet)
// or if Sync has already been called for this handle.
func (h *AgentHandle) Sync()
```

#### Migration

Any caller that today reads results after `State()` needs `Sync()`:

```go
// Before
if h.State().IsTerminal() {
    result := h.Result()
}

// After
if h.State().IsTerminal() {
    h.Sync()                  // explicit barrier
    result := h.Result()
}
```

CHANGELOG ships a `grep -n 'State().IsTerminal' <project>` hint and a sed-style migration suggestion.

**Why breaking instead of additive:** keeping the current `State()` semantics while adding a `NonBlockingState()` leaves the surprise behavior as the default. The whole point is that today's default is the footgun.

---

## 2. Mechanical fixes

### 2.1 Embedding conflict rejected at build time (finding 1.2.g)

**Where:** `agent/agent.go` — `BuildConfig` (or wherever the final config is assembled).

**Logic:** if `WithUserMemory(em1, ...)` and `WithHistory(history.CrossThreadSearch(em2, ...))` are both set with non-equal embedding providers, return:

```
oasis: conflicting embedding providers — WithUserMemory uses %T, history.CrossThreadSearch uses %T; use the same provider for both, or pick one
```

If both use the same provider instance, no error. If only one feature is configured, no error.

Eliminates the silent last-writer-wins bug at `agent/agent.go:150` (history) vs. `agent/agent.go:367` (user memory).

### 2.2 Typed `core.Sandbox` interface (finding 1.2.k)

**New in `core/sandbox.go`:**

```go
type Sandbox interface {
    Exec(ctx context.Context, lang, code string) (stdout, stderr string, err error)
    Close() error
}
```

**Change:** `agent/agent.go:42` — `Sandbox any` → `Sandbox core.Sandbox`.

The `sandbox/` satellite's existing type already has these methods; it just needs to be declared as satisfying the interface (no method changes).

**Migration:** users passing arbitrary types to `WithSandbox(x)` now get compile-time errors if `x` doesn't satisfy `core.Sandbox`. In practice everyone passes `sandbox.Sandbox` values; this is a no-op for real users.

### 2.3 Double-close ownership in `forwardSubagentStream` (finding 2.2.g)

**Background:** the `recover()` in `onceClose` (`agent/agentcore.go:410`) was removed in commit `5a0992b`, immediately caused `TestLLMAgentExecuteStreamNoTools` to panic with `close of closed channel`, and was restored in `ba3b912`. `sync.Once` was already there — meaning a second close path bypasses the Once instance.

**Implementation (investigation first):**
1. Locate the bypass path in `forwardSubagentStream` (or its callers).
2. Route it through the same `onceClose` instance (likely by passing the close function in instead of calling `close` directly).
3. Remove the `recover()` defer.
4. Re-run `TestLLMAgentExecuteStreamNoTools -race` — must pass.
5. Add a regression test that explicitly drives the double-close from both paths.

### 2.4 `EventMaxIterReached` stream event (finding 3.6)

**New `core.StreamEventKind` value:** `EventMaxIterReached`. Payload struct:

```go
type MaxIterReachedEvent struct {
    Iter    int
    MaxIter int
}
```

**Emit location:** `agent/loop.go:492` — right before the forced-synthesis branch. The existing WARN log stays for backward-compatibility.

**Default raise:** `defaultMaxIter` 10 → 25.

Rationale: 10 was conservative for early tool-use patterns; real multi-step tool workflows commonly need 15-20 iterations. Users wanting the old default set `WithMaxIter(10)` explicitly.

### 2.5 Two more `With*` knobs (finding 3.8)

- `WithMaxParallelDispatch(n int)` — overrides `maxParallelDispatch` (loop.go:133). Default stays 10.
- `WithMaxPlanSteps(n int)` — overrides `maxPlanSteps` (llm.go:172). Default stays 50.

(`WithMaxToolResultLen` is in §1.1.)

Not knob-ifying:
- `maxAccumulatedAttachments = 50` — bytes-based knob already exists at `WithMaxAttachmentBytes`.
- Suspend constants — already tunable via `WithSuspendTTL` / `WithSuspendBudget`.

---

## 3. Migration & breaking changes

CHANGELOG entries grouped under the v0.next release.

### Breaking (3)

| Change | Migration |
|---|---|
| `AgentHandle.State()` no longer blocks | Insert `h.Sync()` before reading `h.Result()` when `State().IsTerminal()` |
| Conflicting embedding providers rejected at build | Pass the same `EmbeddingProvider` to `WithUserMemory` and `history.CrossThreadSearch` (most users already do) |
| `WithSandbox(any)` → `WithSandbox(core.Sandbox)` | Custom sandbox types implement `core.Sandbox` (two methods). `sandbox/` satellite is unaffected. |

### Changed (1)

- `WithMaxIter` default 10 → 25. Set `WithMaxIter(10)` to keep old behavior.

### Added (non-breaking)

- `core.ToolResultStore` interface + `core.NewInMemoryToolResultStore`
- `core.Sandbox` interface
- `core.CompactRequest.Scope` field + `ScopeFull`, `ScopeToolResultsOnly` constants
- `oasis.WithToolResultStore`, `WithMaxToolResultLen`, `WithMaxParallelDispatch`, `WithMaxPlanSteps`
- `core.EventMaxIterReached` + `MaxIterReachedEvent` payload
- `AgentHandle.Sync()`
- Built-in `read_full_result` tool (auto-registered when store is configured)

### Removed

- The inline English compression prompt at `loop.go:635-638` (now routed through `Compactor`).

---

## 4. Testing approach

Each finding gets coverage scoped to its surface. Most are small.

### 4.1 Tool-result paging

- **Unit (`core`):** in-memory `ToolResultStore` honors TTL eviction, size cap, LRU. `Put → Get` round-trip with offset/length. Concurrent Put correctness.
- **Integration (`agent`):** tool returns oversize result → marker contains a valid `id` → `read_full_result` retrieves correct slice → final marker continuation correct. Verify `WithToolResultStore(nil)` opts out cleanly (no built-in tool registered, today's marker only).
- **Edge:** content exactly at boundary; `offset` past end; expired id returns clear error; `id` from one Agent not accessible from another.

### 4.2 History cascade

- **Unit:** `CompactRequest.Scope = ScopeFull` matches today's full-thread behavior bit-for-bit (regression check on existing Compactor tests). `ScopeToolResultsOnly` only summarizes tool-result messages, leaves user/assistant messages intact.
- **Integration:** all three stages configured (trim + compress + compact); instrumented Compactor records invocation order; verify the documented cascade.
- **Regression:** existing `compressMessages` tests pass against the refactored code path (output equivalence on same input + same default Compactor).

### 4.3 `State()` / `Sync()` split

- **Unit:** `State()` returns immediately even mid-execution (no `<-done` wait).
- **Race:** `go test -race` on a test that writes from the loop and reads results after `Sync()`. Without `Sync()`, the data race detector should not flag — but the read may see stale results.
- **Sweep:** root tests and `oasis_test.go` updated to insert `Sync()` where `State().IsTerminal()` is followed by result reads.

### 4.4 Embedding conflict

- **Unit:** `BuildConfig` returns descriptive error when two different providers configured; no error when same provider used for both; no error when only one feature is configured.

### 4.5 Sandbox interface

- **Compile-time:** `sandbox/` satellite's existing type satisfies `core.Sandbox` (interface satisfaction check via a `var _ core.Sandbox = (*sandbox.Sandbox)(nil)` declaration).
- **No runtime test changes needed** — type assertions in `agent/` get removed; field access is direct.

### 4.6 Double-close ownership

- **Regression:** `TestLLMAgentExecuteStreamNoTools` passes with `-race` and `recover()` removed.
- **New test:** drive double-close from both paths; assert no panic and that `sync.Once` semantics hold (the second close is a no-op).

### 4.7 `EventMaxIterReached` + maxIter raise

- **Unit:** event emitted exactly once when `iter == maxIter`, before forced synthesis; payload includes correct `Iter` and `MaxIter`.
- **Default change:** test asserts `defaultMaxIter == 25` (catches accidental revert).

### 4.8 Knobs

- **Unit:** `WithMaxParallelDispatch(3)` actually caps the worker pool at 3 (instrument the worker count). `WithMaxPlanSteps(2)` rejects plans with >2 steps.

### 4.9 Repo-wide verification (mirrors Phase 1)

- `go test ./... -race` on root + all 9 satellites — all green.
- `golangci-lint run ./...` clean.
- `grep -rn` for removed/renamed APIs — clean.
- CHANGELOG diff sanity-check.

---

## 5. Sequencing

Three waves of parallel work, modeled on Phase 1's successful pattern.

### Wave 1 (3 parallel, independent)
- **Track A:** Tool-result paging (§1.1) — biggest change. New interface, default impl, built-in tool, loop.go integration. Self-contained in `core/` + `agent/loop.go`.
- **Track B:** `State()` / `Sync()` split (§1.3) — `agent/handle.go` only.
- **Track C:** Mechanical small fixes — §2.1 (embedding conflict), §2.2 (Sandbox), §2.5 (knobs). Independent across files.

### Wave 2 (2 parallel)
- **Track D:** History cascade refactor (§1.2) — depends on nothing in Wave 1 conceptually, but touches `agent/loop.go` so sequenced after Track A to avoid merge conflicts.
- **Track E:** Double-close investigation + fix (§2.3) — touches `agent/agentcore.go`, isolated.

### Wave 3 (sequential — shared `loop.go`)
- **Track F:** `EventMaxIterReached` + maxIter raise (§2.4) — touches `loop.go` + `core/types.go`, slotted last to avoid conflicts with Tracks A and D.

### Wave 4 (single)
- Repo-wide verification, CHANGELOG, doc updates, satellite sweep.

---

## Open questions

None at design time. Open implementation-time decisions:

- **Tool-result-store key shape:** opaque string or structured (`<agent>/<turn>/<call>`)? Implementation can pick — interface doesn't expose key shape. Recommend short opaque (e.g., base32 of crypto/rand 8 bytes) for unpredictability.
- **`Sync()` when already synced:** no-op vs. assert? No-op is cheaper and matches the doc.

---

## Phase 2 sets up Phase 3

After Phase 2 ships, the next natural phase is **capability splits** — the `Store` kitchen sink (finding 1.2.a), `Provider` streaming forcing all implementers to write streaming code (1.2.d), embedding load opt-out (4.1.d), and `ToolRegistry.Remove` (4.1.e). Phase 2's `ToolResultStore` adds a new capability following the existing pattern, so it's a natural lead-in.
