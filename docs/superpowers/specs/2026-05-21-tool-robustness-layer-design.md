# Tool Robustness Layer — Design

> Date: 2026-05-21
> Status: Draft (awaiting user review)
> Owner: framework team
> Motivation: close the largest Tool-System gaps from `docs/benchmarks/mastra-comparison.md` (Tool System scorecard: Mastra 9 / Oasis 4 / Tie 6) without violating `docs/PHILOSOPHY.md`.

## 1. Problem

Today, Oasis tools have three production-pain gaps relative to Mastra:

1. **No input coercion.** When an LLM emits malformed tool args (`null` instead of `{}`, stringified JSON, etc.), `json.Unmarshal` fails outright and the LLM has to figure out the recovery from a generic error.
2. **No output schema published to the LLM.** `Tool[In, Out]` knows `Out` at compile time, but the LLM never sees that shape — only the input parameters. Codegen quality suffers.
3. **No per-tool retry or timeout policy.** A flaky upstream surfaces as a tool error every call. The only retry today is provider-level `WithRetry`.

These gaps are addressed *together* because they are all enrichments to the same `Tool[In, Out]` shape — solving them as one design avoids three round-trip API designs.

## 2. Non-goals

- Tool approval gates / framework-driven suspend (separate spec — Option 2 in original brainstorm).
- Pre-execute authorization hook (`WithToolAuth`) — same.
- Sub-agent `DelegationConfig` (separate spec — Option 3).
- Per-tool tracing spans — `observer` satellite concern.
- HTTP status-code retry helpers (`RetryHTTPCodes(...)`) — user code or follow-up.
- Mastra-style strict/warn/fallback validation modes — Mastra has no such enum either; one pipeline is the strategy.

## 3. Constraints

- **PHILOSOPHY-aligned.** Codegen-friendly, fail gracefully, sensible defaults, 10 lines not 100. No SDK abstraction. Headless about the app.
- **Pre-v1.0.0.** Breaking changes allowed with a migration note (`CHANGELOG.md` entry).
- **Leaf-package invariant.** `core/` cannot import anything from `oasis/...`. New code uses only `errors`, `net`, `context`, `time`, `encoding/json`, `bytes`.
- **Zero allocations on the happy path** for coercion (the universally-applied branch).

## 4. Design summary

### 4.1 Input coercion (structural only)

Default-on, no opt-out. Two information-preserving transforms applied before `json.Unmarshal`:

1. `null` or empty bytes → `{}` (LLMs sometimes send literal `"null"` or empty body for an absent object argument).
2. Stringified JSON args (single JSON string whose value parses as an object or array) → unwrap one level.

Both transforms are pure functions; coercion never errors. Malformed inputs that don't match either pattern pass through unchanged, and the existing `json.Unmarshal` failure path reports the real problem.

Rejected alternatives:
- **Null-as-missing key dropping** (Gemini-drift fix): erases the distinction between `null` and missing — surprises Go code with `*T` fields that intentionally use nil-vs-missing.
- **Field aliasing** (`query`/`message`/`input` → `prompt`): opinionated rename map damages tools that have a literal `query` field. Mastra's pipeline does this, but the aliasing target is a single field name (`prompt`) — too narrow to justify framework-level cost.
- **Configurable per tool**: new policy axis for no demonstrated need.

### 4.2 Output schema (auto-derive + opt-in override)

`Erase[In, Out](t)` runs `DeriveSchema[Out]()` at registration and attaches the result to `ToolDefinition.OutputSchema`. Tools that need richer constraints (enum values, format hints, min/max) implement an optional interface:

```go
type OutSchemaProvider interface {
    OutSchema() *Schema
}
```

`Erase` checks via `errors.As`-style type assertion: if the tool implements `OutSchemaProvider`, its override is used; otherwise the derived schema is used. Pattern matches PHILOSOPHY §"Optional capabilities via interface assertion" exactly.

Provider implementations (Gemini, OpenAI-compat) decide whether to forward `OutputSchema` in their tool spec to the LLM. The field exists so they can; wiring in providers is out of scope for this spec.

### 4.3 Per-tool policy (hybrid name + matcher)

```go
type ToolPolicy struct {
    Timeout       time.Duration       // per-attempt; 0 = no timeout
    Retries       int                 // additional attempts; 0 = current behavior
    RetryDelay    time.Duration       // backoff base; actual delay = RetryDelay << attempt
    MaxRetryDelay time.Duration       // backoff cap; 0 = no cap
    RetryOn       func(error) bool    // nil = use DefaultRetryOn
}

// Agent options:
agent.WithToolPolicy(name string, p ToolPolicy) Option
agent.WithToolPolicyMatch(matcher func(name string) bool, p ToolPolicy) Option
```

Precedence at dispatch (ServeMux-style):
1. Exact name in `WithToolPolicy` map → use it.
2. Otherwise scan registered matchers in registration order; first `matcher(name) == true` wins.
3. Otherwise no policy (current behavior).

Re-registering the same exact name overwrites; matchers always accumulate.

### 4.4 Default RetryOn predicate

When `ToolPolicy.RetryOn == nil`, `DefaultRetryOn` returns true iff any of:

1. `errors.Is(err, context.DeadlineExceeded)` — our own timeout fired.
2. `errors.As(err, &ne)` with `ne.(net.Error).Timeout() == true` — TCP-layer timeout.
3. `errors.As(err, &r)` with `r.(Retryable).Retryable() == true` — opt-in convention.

The third bullet's interface:

```go
type Retryable interface { Retryable() bool }
```

The framework ships a wrapper:

```go
// RetryableError marks any error as retryable. The default RetryOn predicate
// honors this mark via errors.As; user-supplied predicates can do the same.
func RetryableError(err error) error
```

Documented pattern for tool authors:

```go
if resp.StatusCode == 429 || resp.StatusCode >= 500 {
    return zero, core.RetryableError(fmt.Errorf("upstream: HTTP %d", resp.StatusCode))
}
return zero, fmt.Errorf("upstream: HTTP %d", resp.StatusCode) // not retryable
```

`DefaultRetryOn` is exported so user predicates can compose:

```go
RetryOn: func(err error) bool {
    return core.DefaultRetryOn(err) || errors.Is(err, MyExtraRetryErr)
}
```

### 4.5 Streaming bypass

`ToolPolicy` applies only to non-streaming `Execute`. At dispatch, a tool implementing `core.StreamingAnyTool` bypasses the policy wrapper entirely. Documented rationale: retrying a partially-streamed call would duplicate events at the consumer, and "total stream lifetime" is the wrong default for legitimate long-running streams (log tail, progress reporting). Parent `context.Context` cancellation remains the universal lever for streaming-tool lifetime control.

If a user registers a policy for a tool that is later resolved as streaming, the policy is silently ignored at dispatch. (We do not validate at registration because tool sets can be dynamic via `WithDynamicTools`.)

## 5. File layout

```
core/
├── coerce.go          (NEW)  ~60 LOC    Input coercion pipeline
├── coerce_test.go     (NEW)  ~120 LOC
├── retry.go           (NEW)  ~80 LOC    ToolPolicy, RetryableError, Retryable, DefaultRetryOn
├── retry_test.go      (NEW)  ~150 LOC
├── erase.go           (MOD)  +20 LOC    Wire coercion; derive OutputSchema; honor OutSchemaProvider
├── erase_test.go      (MOD)  +50 LOC
├── types.go           (MOD)  +6 LOC     ToolDefinition.OutputSchema; OutSchemaProvider interface
└── schema.go          (unchanged — DeriveSchema[Out] works as-is)

agent/
├── dispatch.go        (MOD)  +60 LOC    Policy wrap; streaming bypass; resolveToolPolicy
├── dispatch_test.go   (MOD)  +120 LOC
├── agent.go           (MOD)  +30 LOC    WithToolPolicy, WithToolPolicyMatch
└── agent_test.go      (MOD)  +60 LOC

oasis.go               (MOD)  +6 LOC    Re-export ToolPolicy, RetryableError, Retryable, DefaultRetryOn
CHANGELOG.md           (MOD)  +20 LOC    [Unreleased] migration note
```

Net: ~600 LOC new code + ~500 LOC tests. No new packages, no satellite changes.

## 6. One critical contract change

`erasedTool[In, Out].ExecuteRaw` and `erasedStreamingTool[In, Out].ExecuteRaw` currently swallow the original Go error from `Tool.Execute` into `ToolResult.Error` and return `(result, nil)`. This must change to return `(result, originalErr)` so the retry layer can examine the typed error (`Retryable`, `net.Error.Timeout()`, `context.DeadlineExceeded`).

- **Upward compatibility:** callers that ignore the Go error still see the LLM-visible string in `ToolResult.Error` (unchanged).
- **New behavior:** the dispatch policy wrapper reads the Go error, applies `RetryOn`, and — after exhausting retries (or on no-retry) — produces `(ToolResult{Error: lastErr.Error()}, nil)` to preserve the loop's existing "tool calls never crash the agent" invariant.
- **PHILOSOPHY check:** the "Tool.Execute always returns nil Go error" rule applies to the tool-author surface, not to `AnyTool.ExecuteRaw`. The erased adapter's internal contract is free to propagate.
- **Audit list** (file paths that touch `ExecuteRaw` and may need updates):
  - `agent/iteration.go`
  - `agent/loop.go`
  - `agent/dispatch.go` (the new wrapper handles it)
  - `mcp/tool_wrapper.go`
  - any other `AnyTool` implementer that returns Go errors from `ExecuteRaw` today

## 7. Data flow at runtime

```
LLM emits tool_call(name, args)
       │
       ▼
agent.dispatch.go: resolveToolPolicy(name)
       │
       ▼
   isStreaming? ──yes──► AnyTool.ExecuteStream(ctx, args, ch)   (policy ignored)
       │
       no
       ▼
   policy zero? ──yes──► AnyTool.ExecuteRaw(ctx, args)          (current behavior)
       │
       no
       ▼
   runWithPolicy(parent, policy, fn):
       attempt = 0
       loop:
         attemptCtx = parent
         if Timeout > 0: attemptCtx, cancel = context.WithTimeout(parent, Timeout)
         result, err = fn(attemptCtx)
         cancel()
         if err == nil: return result, nil
         if attempt == Retries || !retryOn(err): break
         sleep backoff(RetryDelay, MaxRetryDelay, attempt)  with parent.Done() guard
         attempt++
       return ToolResult{Error: lastErr.Error()}, nil
```

Schema publication happens once at registration inside `Erase` — no runtime cost per dispatch.

## 8. Error handling matrix

| Source | Path | LLM sees |
|---|---|---|
| Malformed args after coercion | erase.go: `json.Unmarshal` err → `ToolResult{Error: "invalid args: ..."}` | error string |
| Tool returns Go error, no retry policy | erase.go → `ToolResult{Error: err.Error()}` propagated up | error string |
| Tool returns Go error, policy retries successfully | dispatch retries silently | successful result |
| Retries exhausted | dispatch returns `ToolResult{Error: lastErr.Error()}` | last error's message |
| RetryOn returns false | dispatch returns first error immediately | tool's error string |
| Timeout fires | `DefaultRetryOn` catches `DeadlineExceeded`, retries if budget remains | "context deadline exceeded" if exhausted |
| Parent ctx cancelled mid-backoff | abort retry loop, surface `parent.Err()` | "context canceled" |
| Streaming tool registered with policy | policy not applied, log warning once | tool's normal output |

## 9. Edge-case decisions

1. **Backoff formula:** `delay = RetryDelay << attempt`, capped at `MaxRetryDelay` if non-zero. Deterministic, no jitter in v1. Jitter is added only when concurrent thundering-herd is observed in real workloads (PHILOSOPHY §"Earn every abstraction").
2. **Backoff cancellation:** `select { case <-time.After(d): case <-parent.Done(): }`. Cancelled retries return immediately with `parent.Err()` in `ToolResult.Error`.
3. **Coercion runs once per dispatch**, not per retry attempt — the same coerced bytes are reused. Coercion is pure-function; no logging, no metrics.
4. **`OutputSchema` fallback:** if `DeriveSchema[Out]()` returns nil (e.g., `Out = any` or unsupported type), `OutputSchema` is also nil. Authors who want a schema for such tools implement `OutSchemaProvider`.
5. **Re-registration:** `WithToolPolicy("foo", p1)` then `WithToolPolicy("foo", p2)` — last call wins. Matchers always accumulate.
6. **MCP tools** are auto-namespaced (`mcp__<server>__<tool>`); both exact and matcher forms work without special-casing.
7. **Unknown tool names** in `WithToolPolicy` are not validated at registration — tool sets can be dynamic via `WithDynamicTools`. Policies for nonexistent names are dormant.
8. **`StreamingTool` with policy registered:** logged once at first dispatch via `slog.Warn`. Not an error.

## 10. Test plan

### `core/coerce_test.go`
- `null` → `{}`
- empty bytes → `{}`
- `"{\"x\":1}"` (stringified object) → `{"x":1}`
- `"[1,2,3]"` (stringified array) → `[1,2,3]`
- `"hello"` (string that doesn't unwrap to JSON) → passes through unchanged
- `{"x":1}` (already an object) → unchanged
- Trailing whitespace inside stringified JSON tolerated

### `core/retry_test.go`
- `RetryableError(nil)` → nil
- `errors.As(RetryableError(io.EOF), &r)` → `r.Retryable() == true`
- `errors.Unwrap(RetryableError(io.EOF)) == io.EOF`
- `DefaultRetryOn(context.DeadlineExceeded)` → true
- `DefaultRetryOn(&net.DNSError{IsTimeout: true})` → true
- `DefaultRetryOn(errors.New("plain"))` → false
- `DefaultRetryOn(RetryableError(errors.New("upstream 503")))` → true
- Backoff formula: `RetryDelay << attempt`, with cap by `MaxRetryDelay`

### `agent/dispatch_test.go`
- Tool fails twice with `RetryableError`, succeeds on attempt 3 — total 3 calls, returns success.
- Tool fails with plain `errors.New` and `RetryOn: nil` — 1 call, returns error.
- `Timeout: 50ms`, tool sleeps 200ms — `DeadlineExceeded`; `Retries: 1` causes one retry.
- Parent ctx cancelled during backoff → loop aborts within ~10ms; result carries `"context canceled"`.
- Exact policy beats matcher for the same name.
- Multiple matchers — first registered match wins.
- Streaming tool registered with policy → policy not applied, no wrap, warning logged once.
- `RetryDelay << attempt` with `MaxRetryDelay` cap — caps observed.

### `core/erase_test.go`
- `Tool[InStruct, OutStruct]` → `OutputSchema` derived and non-nil; matches `DeriveSchema[OutStruct]()`.
- `Tool[InStruct, any]` → `OutputSchema == nil`.
- Tool implementing `OutSchemaProvider` → override used; derivation result discarded.
- Coercion: tool gets `"null"` arg → `Execute` receives zero `In`, no error.
- Coercion: tool gets `"{\"name\":\"X\"}"` (stringified) → `Execute` receives populated `In`.

### `agent/agent_test.go`
- `WithToolPolicy` and `WithToolPolicyMatch` both register; resolution order verified.
- Re-registering same exact name overwrites.

## 11. Migration

- Add `CHANGELOG.md [Unreleased]` block documenting:
  - `ToolDefinition.OutputSchema` is a new field (additive — safe).
  - `core.ToolPolicy`, `core.RetryableError`, `core.Retryable`, `core.DefaultRetryOn` are new exports.
  - `agent.WithToolPolicy`, `agent.WithToolPolicyMatch` are new options.
  - **Behavior change:** `erasedTool.ExecuteRaw` now propagates the original Go error alongside `ToolResult.Error`. Callers downstream of `AnyTool.ExecuteRaw` that previously branched only on `ToolResult.Error` now also see a non-nil error. The dispatch layer absorbs this; external `AnyTool` implementers do not need to change.

## 12. Future composition

When tool approval gates (Option 2 from the brainstorm) land:
- `ToolPolicy` gains a `RequireApproval bool` (or `RequireApprovalFunc`) field — additive, non-breaking.
- The dispatch wrapper inserts an `InputHandler.RequestApproval` call before the first attempt.

When pre-execute authorization (`WithToolAuth`) lands:
- New agent option calls the auth hook before any policy wrapping. Independent of `ToolPolicy`.

When sub-agent `DelegationConfig` lands (Option 3):
- Completely separate axis. Composes via existing `WithSubAgentSpawning` option.

The `ToolPolicy` struct is intentionally narrow today so it can grow safely.
