# Engineering Guidelines

Concrete rules for writing production-grade code in this codebase. [PHILOSOPHY.md](PHILOSOPHY.md) tells you *how to think*. This document tells you *what to do*.

When a rule here conflicts with the spirit of PHILOSOPHY, fix the rule.

---

## Coding Standards

### Naming and Documentation

- Godoc on every exported symbol — explain the contract, not just "returns X".
- Interface contracts in comments — document invariants, thread-safety, nil/zero behavior, what panics.
- Top-to-bottom flow. Early return for edge cases, happy path below. Max 2 levels of nesting.

### Design Decision Documentation

Every design decision that isn't obvious from reading the code must have a comment explaining **why** — not just what. This applies especially to:

- **Resource lifecycle choices** — why a resource is created/closed at a particular scope (e.g., per-call vs per-session). If `Close()` is deferred somewhere, explain why it's safe to close at that point and what callers must know.
- **Concurrency boundaries** — why a channel is buffered to a specific size, why a lock covers a particular scope, why a goroutine is spawned here rather than elsewhere.
- **Interface design trade-offs** — why a method lives on one interface and not another, why a parameter is passed vs stored.
- **Non-obvious control flow** — loops that re-enter functions, fallback chains, retry logic. If a caller can invoke a function multiple times with shared state, document the contract.

The comment format: `// Why: <reason>`. Place it above the relevant line or block.

```go
// Why: sandbox lifecycle is managed by the TTL reaper, not the caller.
// Closing here would break re-execution flows that reuse the same session ID.
sb, err := a.sandboxMgr.Get(req.RunID)
```

The cost of a missing "why" comment is a future contributor (human or agent) repeating the same mistake — or worse, "fixing" correct code because the reasoning wasn't visible.

---

## Codegen-Friendly APIs

LLM coding assistants are first-class consumers of this API. The rules below make them succeed without prior Oasis context.

### Consistent Shapes

Every implementation of the same concept has the same signature. Variation requires justification in a design comment.

- Every Tool implements `Execute(ctx, args) ToolResult` — no exceptions.
- Every Provider implements `Complete(ctx, req) (Response, error)` — no exceptions.
- New conceptual category? Pick one shape and hold it across all implementations.

### Predictable Names

Same verb, same semantics, across the surface.

- `Get<X>` — returns the value or an error.
- `Find<X>` — returns the value or nil; not finding is not an error.
- `List<X>` — returns a (possibly empty) slice; never nil.
- `New<X>` — constructor, returns a value (or pointer) and an error.
- `Open<X>` / `Close<X>` — paired lifecycle; whoever opens, closes.

### Fail at Compile Time, Not Runtime

- **Required parameters are function arguments.** Not struct fields with runtime "missing field" validation. If the function can't run without it, it goes in the signature.
- **Optional parameters use functional options.** `agent.New(provider, agent.WithTools(...), agent.WithMemory(...))`. The zero-value config must be valid and useful.
- **Enum-like values get typed constants.** Not stringly-typed maps, not magic strings. `type ToolKind string; const ToolKindHTTP ToolKind = "http"`.

### No `any` at the Boundary

`interface{}` / `any` in an exported signature defeats codegen tooling — an LLM can't infer what's valid to pass.

- Need polymorphism? Expose a typed interface (`type Storable interface { ... }`) or a tagged union (a struct with a `Kind` field and option-set fields).
- Internal use of `any` is fine. The boundary is the rule.

### Godoc Reads as a Spec

First sentence states the contract. Then document:

- Thread-safety (is concurrent use safe?).
- Nil and zero behavior (what does passing nil do?).
- Panic conditions (what causes a panic vs an error?).
- Distinction between Go errors and business failures (e.g., `ToolResult.Error`).

---

## Production Engineering

Concrete rules for production-grade code.

### Goroutine Discipline

Every goroutine must have a clear shutdown path tied to context cancellation. A goroutine without a shutdown path is a memory leak. Always use `select` with `ctx.Done()` — never rely solely on channel closure for termination.

### Channel Safety

Use buffered channels for producer-consumer patterns with a justified buffer size. The sender owns the close — never close from the receiver side. Use `sync.Once` to prevent double-close panics.

### Memory and Resource Bounding

Bound all caches, buffers, and queues. If it can grow, it needs a cap. When a bound is hit, degrade gracefully (drop oldest, reject new) rather than blocking indefinitely or panicking. Truncate large text fields at ingestion boundaries, not deep in processing pipelines.

### Context Propagation

Every public function that does I/O or runs longer than trivially takes `context.Context` as its first parameter. Never store contexts in structs — pass them through call chains. Derive child contexts for sub-operations so cancellation propagates correctly.

### Concurrency Patterns

Heavy non-critical work (memory extraction, embedding) runs in background goroutines with bounded concurrency. LLM streaming uses buffered channels. Protect shared mutable state with `sync.Mutex` — prefer narrowing the critical section over wrapping entire methods.

### Graceful Shutdown

Components that start background work must expose a `Close()` or accept a context whose cancellation triggers cleanup. Shutdown must drain in-flight work within a timeout, not drop it silently. **Every `Close()` path must be exercised in tests** — a `Close()` that's never tested is a shutdown that doesn't work.

---

## Performance

**Hot paths** in Oasis: the agent run loop, tool dispatch, message assembly, and streaming. Anything in these paths is held to the rules below. Anything else is cold — readability wins over micro-optimization.

- **Zero-allocation hot paths.** The run loop and tool dispatch must not allocate on the common path. Use `sync.Pool` for transient buffers. Avoid `fmt.Sprintf` in hot paths — use string concatenation or pre-formatted strings.
- **Benchmark before and after.** Any change to a hot path must include benchmark results in the PR. Use `go test -bench` with `-benchmem`. Regressions need justification.
- **No reflection or unnecessary serialization in the loop.** JSON marshaling, type reflection, and interface conversions in the run loop add up fast. Marshal at boundaries (provider calls, store writes), not between internal steps.
- **Streaming must not block the sender.** Buffered channels with bounded size. If the consumer is slow, drop or backpressure — never block the LLM response path.
- **Profile before optimizing.** Don't guess. Use `pprof` to find the actual bottleneck. Premature optimization of cold paths is wasted effort.

---

## Recovery as a First-Class Concern

Recovery isn't a separate axis from performance — a slow recovery is a performance bug. The rules:

- **Every error path is reachable in tests.** Negative tests are not optional. If a code path returns an error, there's a test that hits it.
- **Recovery latency is measured.** A graceful failure that takes 30 seconds to recover is a failure. Time the recovery; treat the number like any other perf metric.
- **Structured logging with reconstruction-grade context.** Whoever reads the log after the fact must be able to reconstruct what happened. Include the operation, the inputs (redacted as needed), the failure mode, and the recovery action taken.
- **No swallowed errors.** Either handle, log with context, or return. Never `_ = err`.
- **`ToolResult.Error` for business failures; Go `error` for infrastructure failures.** This split is enforced — see [PHILOSOPHY.md](PHILOSOPHY.md#fail-gracefully-recover-fast).

---

## Things to Never Do

These rules apply always — pre-v1 and post-v1.

- Do not add LLM SDK dependencies — raw HTTP only.
- Do not add bot, HTTP router, error wrapping, or logging frameworks.
- Do not cache database connections.
- Do not return Go `error` from `Tool.Execute` for business failures — use `ToolResult.Error`.
- Do not start goroutines without a shutdown path.
- Do not use unbounded channels, caches, or buffers.
- Do not store `context.Context` in structs.
- Do not introduce inconsistent API shapes — a Tool whose `Execute` signature differs from the rest is a bug, not a design.
- Do not use `any` / `interface{}` in exported APIs without a typed assertion at the boundary.
- Do not skip the deprecation cycle post-v1. Breaking changes ride at least one minor release with the old API marked deprecated before removal.
- Do not ship v1.0.0 with any exported symbol that hasn't passed the API audit.
