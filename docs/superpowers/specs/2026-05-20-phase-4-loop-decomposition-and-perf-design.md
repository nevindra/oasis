# Phase 4 — Loop Decomposition + Targeted Perf

**Status:** Draft for review
**Date:** 2026-05-20
**Author:** nevindra
**Source findings:** [2026-05-18-core-agent-review.md](../plans/2026-05-18-core-agent-review.md) §2.2.d, §2.2.e (deferred), §4.1.a, §4.1.b, §4.1.c, §4.1.f, §4.1.g

---

## 1. Context

Phase 1 (type safety), Phase 1.5 (typed tool schemas), and Phase 2 (coherence) shipped a series of breaking but surgical user-facing changes. With those settled, the next bottleneck is internal: `agent/loop.go` has grown to **956 LOC** with a single `runLoop` function spanning **481 LOC** that handles blocking-vs-streaming, with-tools-vs-no-tools, normal-vs-synthesis paths, error/suspend handling, compression, attachment accumulation, and tracing — all interleaved.

Every Phase 1/1.5/2 commit that touched the agent loop paid a "find where I am in this function" tax. As more agent options have been added (count of exported `AgentCore` fields grew 5 → 17 since the original review), that tax compounds.

Phase 4 is a **pure internal refactor + targeted perf pass**. No API breaks. The goal is to leave the loop in a shape that the next feature lands cleanly, and to clear a small backlog of perf nits that are cheap individually but add up.

The companion structural fix — moving `AgentCore` out of `agent/` to remove the "exported for network subpackage access" pattern (review §1.2.e) — is **deferred to Phase 5**, because Phase 5's design will choose between `internal/agentcore/` and merging `network/` back into `agent/`, and that decision determines whether splitting `agentcore.go` now is wasted work.

---

## 2. Goals

1. **runLoop becomes an orchestrator (~150 LOC), not a monolith.** Per-iteration logic, LLM-call dispatch, and stream forwarding extracted into focused functions/files.
2. **No behavior change.** Same `StreamEvent` order, same error mapping, same suspend semantics, same tool dispatch sequence. Validated by existing 7426 LOC of agent tests + `-race`.
3. **Measurable perf deltas, not vibes.** New benchmarks land alongside the refactor; CHANGELOG cites real `benchstat` numbers.
4. **Single-dev branch, AI-native execution.** Sequenced waves with checkpoint gates; each wave commits separately so any wave can revert independently.

---

## 3. Non-goals

- **No API breaking changes.** Public surface (`oasis.*`, `agent.*`, satellite imports) unchanged.
- **No new options.** Specifically: no `WithIterChBuffer(n)`-style knob. Buffer size is an internal implementation detail; expose only if a real user needs it.
- **No changes to `AgentCore` struct or its exports.** That work is Phase 5.
- **No new dependencies.** Stdlib only.
- **No reshape of `LoopConfig`.** Phase 4 splits files around `LoopConfig`; `LoopConfig` itself is unchanged.
- **Not adopting `sync.Pool` for `iterCh`.** Channels cannot be pooled after close. The original review's framing of finding 4.1.a was misdiagnosed; this design replaces it with buffer-size reduction (see §5.1).

---

## 4. Target file layout

End state after Phase 4:

```
agent/loop.go         ~250 LOC   runLoop orchestrator + LoopConfig
agent/iteration.go    ~180 LOC   runIteration (per-iter LLM call + tool dispatch + step trace)
agent/dispatch.go     ~150 LOC   DispatchBuiltins, DispatchTool, dispatchParallel, safeDispatch, toolResultToDispatch
agent/compress.go     ~130 LOC   compressMessages, runeCount, mergeAttachments
agent/stream_fwd.go    ~50 LOC   streamForwarder helper (dedups 3 forwarder goroutines)
agent/trace.go         ~50 LOC   buildStepTrace, handleProcessorErrorWithSteps
agent/routing.go       ~20 LOC   buildRoutingSummary
agent/strings.go       ~15 LOC   TruncateStr
```

Total LOC roughly unchanged (~845 across the new layout vs 956 today; small drop from deduping the 3 stream-forwarder bodies and pre-alloc cleanups).

**Files unchanged in Phase 4:** `agent.go`, `agentcore.go`, `handle.go`, `llm.go`, `memory_orchestration.go`, `processor.go`, `retry.go`, `spawn.go`, `stream.go`, `suspend.go`, `tracer.go`.

`agentcore.go` ownership of `AgentCore` plus subagent helpers (review §2.2.e) is intentionally untouched — that file is Phase 5's concern.

---

## 5. Detailed design

### 5.1 `streamForwarder` extraction

Today, three near-identical patterns appear in `loop.go` at lines 263-279, 289-303, and ~572:

```go
iterCh := make(chan StreamEvent, 64)
var fwdWg sync.WaitGroup
fwdWg.Add(1)
go func() {
    defer fwdWg.Done()
    for ev := range iterCh {
        select {
        case ch <- ev:
        case <-ctx.Done():
            for range iterCh { }
            return
        }
    }
}()
// ... provider.ChatStream(iterCtx, req, iterCh) ...
fwdWg.Wait()
```

Replaced by:

```go
// stream_fwd.go
func newStreamForwarder(ctx context.Context, dest chan<- StreamEvent, bufSize int) (chan<- StreamEvent, func()) {
    iterCh := make(chan StreamEvent, bufSize)
    var wg sync.WaitGroup
    wg.Add(1)
    go func() {
        defer wg.Done()
        for ev := range iterCh {
            select {
            case dest <- ev:
            case <-ctx.Done():
                for range iterCh { }
                return
            }
        }
    }()
    return iterCh, wg.Wait
}
```

Call site shrinks to:

```go
iterCh, wait := newStreamForwarder(iterCtx, ch, iterChBufSize)
resp, err := cfg.provider.ChatStream(iterCtx, req, iterCh)
wait()
```

**Behavior contract:** identical to current code. `iterCh` close is still the provider's responsibility; forwarder drains remaining events on ctx cancellation; `wait()` blocks until forwarder exits.

### 5.2 `runIteration` extraction

The for-loop body in `runLoop` (currently starts at `loop.go:221`, runs until just before the forced-synthesis tail) collapses into a single per-iteration function:

```go
// iteration.go
type iterationResult struct {
    resp         ChatResponse
    streamedText bool
    suspended    *SuspendSignal
    halted       *ErrHalt
    err          error
}

func runIteration(
    iterCtx context.Context,
    cfg LoopConfig,
    state *loopState,
    i int,
) iterationResult
```

Where `loopState` is the mutable per-execution state (messages, accumulatedAttachments, accumulatedAttachmentBytes, totalUsage, steps, lastAgentOutput, lastThinking, messageRuneCount). `state` is a pointer so the iteration can mutate it.

Inside `runIteration`:
- Open iteration span via tracer
- Run pre-LLM processor hook
- Choose LLM call mode (streaming+tools, blocking+tools, streaming-no-tools, blocking-no-tools) — each branch is ~10 lines using `newStreamForwarder` where applicable
- Run post-LLM processor hook
- Append assistant message + handle attachments
- Dispatch tool calls (one or many via `dispatchParallel`)
- Build step trace, append to `state.steps`
- Run compression check (delegates to `compressMessages` in `compress.go`)
- Return `iterationResult` indicating whether to break, continue, or surface an error

**Termination semantics preserved:** `state.iterDone` boolean signals natural completion (no tool calls); suspend/halt signals returned via `iterationResult` propagate out the same way they do today.

### 5.3 `runLoop` orchestrator

After extraction, `runLoop` shrinks to:

```go
func runLoop(ctx context.Context, cfg LoopConfig, task AgentTask, ch chan<- StreamEvent) (AgentResult, error) {
    // (1) Setup: defaults, safeCloseCh, handler injection, messages build
    // (2) Emit EventProcessingStart
    // (3) For i := 0; i < cfg.maxIter; i++ { runIteration(...) — break/return per result }
    // (4) Forced synthesis tail if max iter reached (existing logic, lifted as-is)
    // (5) Return AgentResult
}
```

The forced-synthesis tail (currently around `loop.go:534+`, after the iteration for-loop) stays in `loop.go` because it has its own one-shot LLM call shape that doesn't fit the per-iteration template. It becomes a helper `forceSynthesis(ctx, cfg, state) (string, Usage, error)` inside `loop.go`.

### 5.4 4.1.a — iterCh buffer reduction (profile-driven)

**Investigation step, executed as part of Wave 4 implementation (not pre-design):**

1. Add temporary instrumentation to `newStreamForwarder`: log `len(iterCh)` when receiving, track max observed.
2. Run `cmd/bot_example` against the existing demo workload (tools + streaming + multi-turn).
3. Capture max observed fill across at least 3 runs.

**Decision rule:**
| Max observed fill | New buffer size |
|---|---|
| ≤ 8 | 16 |
| ≤ 16 | 32 |
| > 16 | 64 (keep current) |

**Defensive floor:** never drop below 16 even if max observed is < 8 — leaves headroom for bursty providers and avoids producer-side blocking on slow consumers.

**Outcome:** single named constant `defaultIterChBufSize` in `stream_fwd.go` with a comment citing the empirical max-fill observation. Instrumentation code stripped before commit. New `BenchmarkIterChStreaming` added to `loop_bench_test.go` to catch future regressions if the buffer is changed.

### 5.5 4.1.b — `messages` slice pre-alloc

Today `runLoop` builds `messages` via `cfg.mem.BuildMessages(...)`, then appends in each iteration (assistant message + tool results) without pre-sizing the underlying slice.

After extraction:

```go
initial := cfg.mem.BuildMessages(ctx, cfg.name, cfg.systemPrompt, task)
state.messages = make([]ChatMessage, len(initial), len(initial)+cfg.maxIter*4)
copy(state.messages, initial)
```

`maxIter*4` headroom assumption: ≤ 4 appends per iteration on average (assistant message + ~2-3 tool results). Worst-case overshoot is one realloc, far better than the current ~maxIter reallocs on long runs.

`resumeMessages` path stays as-is (already pre-sized).

### 5.6 4.1.c — `compressed` slice pre-alloc

In `compress.go` (extracted from current `loop.go:652`), inside `compressMessages`:

```go
compressed := make([]ChatMessage, 0, len(messages)-len(toRemove)+1)
```

`+1` accounts for the synthesized compression-summary message. One alloc saved per compression event.

### 5.7 4.1.f — `Sscanf` → `Atoi` in `ParseRetryAfter`

Single change in `core/types.go:424`:

```go
// before
if _, err := fmt.Sscanf(value, "%d", &secs); err == nil && secs > 0 {
// after
if n, err := strconv.Atoi(value); err == nil && n > 0 {
    secs = n
    // ... rest unchanged
}
```

Import update: drop `"fmt"` if no other use; add `"strconv"`. Add unit test `TestParseRetryAfterIntegerSeconds` if not already covered.

### 5.8 4.1.g — `suspendMu` simplification

Today (`agent/agentcore.go:43-46`):

```go
suspendCount atomic.Int64
suspendBytes atomic.Int64
suspendMu    sync.Mutex // guards check-then-add on suspendCount/suspendBytes
```

`suspend.go:253-265` already holds `suspendMu` while reading/writing the atomics — the atomics give zero contention benefit at this serialization level.

After:

```go
suspendMu           sync.Mutex
suspendCount        int64 // guarded by suspendMu
suspendBytes        int64 // guarded by suspendMu
```

`suspend.go` paths become plain mutex-guarded int reads/writes. Suspend is a rare path; the atomic overhead removal is cosmetic, but the reasoning is simpler.

---

## 6. Sequencing (5 waves)

### Wave 1 — Mechanical extractions (~1 day)

Per-file extractions; pure `git mv`-style moves; zero behavior change.

1. Create `strings.go` (TruncateStr), `routing.go` (buildRoutingSummary), `trace.go` (buildStepTrace, handleProcessorErrorWithSteps).
2. Create `dispatch.go` (DispatchBuiltins, DispatchTool, dispatchParallel, safeDispatch, toolResultToDispatch).
3. Create `compress.go` (compressMessages, runeCount, mergeAttachments) — include §5.6 pre-alloc here as part of the move.
4. Update internal cross-imports.
5. **Gate:** `go test ./agent/... -race` green.

### Wave 2 — runLoop crack-open (~1-2 days)

Higher-risk refactor; behavior-preserving.

6. Create `stream_fwd.go` with `newStreamForwarder` (§5.1). Replace 3 sites in `runLoop` with calls to it.
7. **Gate:** tests green.
8. Define `loopState` struct + `iterationResult` in `iteration.go` (§5.2). Extract per-iteration body from `runLoop` into `runIteration`.
9. Lift `forceSynthesis` helper inside `loop.go`.
10. Apply §5.5 (`messages` pre-alloc) inside the new orchestrator.
11. **Gate:** tests green + `-race` green.
12. Capture baseline benchmarks for §7: save `bench-wave2.txt`.

### Wave 3 — Targeted perf (~0.5 day)

13. Apply §5.7 (`Sscanf → Atoi`) + add unit test if missing.
14. Apply §5.8 (suspendMu simplification).
15. **Gate:** `go test ./... -race` green across root + 9 satellites.

### Wave 4 — iterCh investigation (~0.5-1 day)

16. Add fill-counter instrumentation to `newStreamForwarder` (local-only commit, will be reverted in step 19).
17. Run `cmd/bot_example` against demo workload, ≥ 3 runs.
18. Apply decision rule §5.4; pick final buffer size; bake into `defaultIterChBufSize` constant with comment.
19. Strip instrumentation. Add `BenchmarkIterChStreaming` to `loop_bench_test.go`.
20. Re-run all benchmarks; save `bench-final.txt`.

### Wave 5 — Ship (~0.5 day)

21. `benchstat bench-before.txt bench-final.txt` — capture deltas for CHANGELOG.
22. Update CHANGELOG `[Unreleased]` with: refactor summary, file layout change, real benchmark numbers per perf item.
23. Verify all 9 satellites: `for sat in mcp store/sqlite store/postgres provider/gemini provider/openaicompat observer ingest sandbox rag; do (cd $sat && go test ./...); done`
24. `golangci-lint run ./...` green.
25. Final `go test ./... -race` green.

**Total: 3-5 days single-dev AI-native execution.**

---

## 7. Validation & benchmarks

### 7.1 Existing test surface

Unchanged. All 7426 LOC of agent tests must stay green at every wave gate, with `-race` enabled.

### 7.2 New benchmarks in `agent/loop_bench_test.go`

```go
BenchmarkRunLoop_StreamingNoTools     // pure streaming path
BenchmarkRunLoop_WithToolsBlocking    // blocking + tools (Network-style)
BenchmarkRunLoop_WithToolsStreaming   // streaming + tools (3-site iterCh path)
BenchmarkCompressMessages             // §5.6 pre-alloc proof
BenchmarkDispatchParallel             // dispatch.go extraction sanity
BenchmarkIterChStreaming              // §5.4 buffer-size regression guard
```

Each benchmark runs with `b.ReportAllocs()`. Mock providers reused from existing `testhelpers_test.go`.

### 7.3 Capture & comparison strategy

| Checkpoint | Capture file | Purpose |
|---|---|---|
| Before Wave 1 (on `master`) | `bench-before.txt` | Baseline |
| End of Wave 2 | `bench-wave2.txt` | Validate refactor didn't regress |
| End of Wave 4 | `bench-final.txt` | Validate perf wins landed |

Diff via `benchstat bench-before.txt bench-final.txt`.

### 7.4 Acceptance targets (not hard gates)

| Item | Metric | Target |
|---|---|---|
| 4.1.a buffer reduction | `B/op` for streaming benchmarks | ≥ 50% if buffer drops to 16, ≥ 25% if to 32 |
| 4.1.b messages pre-alloc | `allocs/op` for `BenchmarkRunLoop_WithToolsStreaming` | Drop by ~maxIter (~25 reallocs avoided) |
| 4.1.c compressed pre-alloc | `allocs/op` for `BenchmarkCompressMessages` | Drop by 1 |
| 4.1.f Sscanf→Atoi | Standalone micro-bench | ~100× speedup (cosmetic, not gated) |
| 4.1.g suspendMu | Suspend path benchmark | No regression (suspend is rare) |

### 7.5 Hard gates

Stop and investigate if any of:
- Test regression that isn't trivially explainable
- Wave-2 benchmark shows > 5% regression vs `bench-before.txt`
- `-race` flags a new data race
- Any satellite build or test breaks
- `golangci-lint` flags new violations

### 7.6 Rollback strategy

Each wave is a separate commit. If Wave 2 (runLoop crack-open) goes sideways, revert just that commit; Wave 1's mechanical extractions stay. Wave 3-4 are independent of Wave 2's refactor work and can land even if Wave 2 is reverted.

---

## 8. Risk register

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| `runIteration` extraction subtly changes StreamEvent ordering | Low | High | 7426 LOC tests + `stream_test.go` (19.8K) explicit event-order assertions catch this |
| Function-call overhead from `runIteration` shows up as Wave-2 perf regression | Medium | Medium | Inline-hint candidate functions; accept if < 5% regression |
| `streamForwarder` extraction changes ctx-cancellation timing | Low | Medium | Tests in `stream_test.go` cover cancel paths; `-race` catches ordering issues |
| Buffer reduction (4.1.a) causes producer blocking under unseen workloads | Low | Medium | Defensive floor of 16; new benchmark catches regression on next change |
| Phase 5 reorganizes `AgentCore` such that `runIteration`'s `loopState` design needs rework | Medium | Low | `loopState` is internal-only and small; rework cost would be ~1 hour |
| Wave 4 instrumentation accidentally commits | Low | Low | Code review checklist; instrumentation is local-only patch reverted by hand |

---

## 9. Acceptance criteria

Phase 4 ships when all of:

1. ✅ `agent/loop.go` ≤ 300 LOC; `runLoop` is an orchestrator with no per-iteration body
2. ✅ 7 new files in `agent/` per §4 (or close equivalent — naming flexible during impl)
3. ✅ All existing tests pass with `-race` at the root and across all 9 satellites
4. ✅ New benchmarks added to `loop_bench_test.go` and passing
5. ✅ `benchstat bench-before.txt bench-final.txt` shows non-regression on `ns/op` and improvement on `B/op` for streaming paths
6. ✅ `core/types.go` `ParseRetryAfter` uses `strconv.Atoi`
7. ✅ `AgentCore.suspendMu` is the sole serialization point; no `atomic.Int64` siblings
8. ✅ `defaultIterChBufSize` constant exists in `stream_fwd.go` with comment citing empirical max-fill
9. ✅ CHANGELOG `[Unreleased]` updated with real `benchstat` numbers
10. ✅ `golangci-lint run ./...` clean
11. ✅ Zero public API changes (`oasis.*` surface unchanged)

---

## 10. Decision register

| # | Decision | Rationale |
|---|---|---|
| P4.1 | Defer 2.2.e (agentcore.go split) to Phase 5 | Phase 5 will relocate `AgentCore` (to `internal/agentcore/` or merge `network/` back); splitting now risks rework |
| P4.2 | Replace 4.1.a's "iterCh sync.Pool" with profile-driven buffer reduction | Channels can't be pooled after close; original review framing was misdiagnosed |
| P4.3 | Crack open `runLoop` (extract `runIteration` + `streamForwarder`) rather than helper-only extraction | "381-line jungle" is the actual pain; helper-only split is reshuffling without solving it. Strong test coverage (7426 LOC, 2.5× source) makes behavior-preserving refactor safe |
| P4.4 | No new `With*` option for buffer size | Internal detail; expose only if a real user needs it (YAGNI per ENGINEERING.md) |
| P4.5 | Single-dev AI-native execution, 5 waves with per-wave commits | Matches hybrid-architecture-design.md §4.6 execution mode; small enough that PR review per wave would be ceremony without benefit |
| P4.6 | Existing tests + new benchmarks (not pprof captures) | Test coverage already excellent; benchmarks give the perf data needed for CHANGELOG without heavy capture infra |
| P4.7 | Apply pre-alloc in Wave 2 (alongside refactor), not Wave 3 | Pre-alloc lives inside the extracted `runIteration` / `compressMessages`; landing them together keeps the diff coherent |

---

## 11. Open items before execution

- [ ] User approves this design
- [ ] Decide final filenames if §4 layout naming is contested (`iteration.go` vs `runiteration.go`, `stream_fwd.go` vs `forwarder.go`, etc.) — defer to implementation taste
- [ ] Confirm `cmd/bot_example` is still the right Wave-4 instrumentation harness (it builds and runs against the current architecture per CHANGELOG)
- [ ] Write the Phase 4 implementation plan (handoff to writing-plans skill after spec approval)
