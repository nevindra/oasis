# API Reference: Errors

## Error Types

**File:** `errors.go`

```go
// ErrLLM is returned by providers for LLM-specific errors.
type ErrLLM struct {
    Provider string
    Message  string
}

// ErrHTTP is returned for HTTP request failures.
// Used by WithRetry to identify transient errors (429, 503).
type ErrHTTP struct {
    Status     int
    Body       string
    RetryAfter time.Duration // parsed from Retry-After header; zero = not set
}
```

Both implement the `error` interface.

## ErrHalt

**File:** `processor.go`

```go
// ErrHalt signals that a processor wants to stop agent execution
// and return a specific response. The agent loop catches ErrHalt
// and returns AgentResult{Output: Response} with a nil error.
type ErrHalt struct {
    Response string
}
```

Return from any processor hook to short-circuit execution gracefully.

## WorkflowError

**File:** `workflow.go`

```go
// WorkflowError is returned when a workflow step fails.
// Carries the full WorkflowResult for per-step inspection.
type WorkflowError struct {
    StepName string
    Err      error
    Result   WorkflowResult
}
```

Implements `Unwrap()` for `errors.Is`/`errors.As` chains.

## ErrMaxIterExceeded

**File:** `workflow.go`

```go
var ErrMaxIterExceeded = errors.New("step reached max iterations without meeting exit condition")
```

Returned (wrapped) by `DoUntil`/`DoWhile` steps when the loop cap is reached without the exit condition being met. Use `errors.Is(err, oasis.ErrMaxIterExceeded)` to detect.

## Sandbox Mount Errors

**Package:** `github.com/nevindra/oasis/sandbox`

```go
// ErrVersionMismatch is the sentinel returned (wrapped) when a Put or
// Delete fails its precondition check.
var ErrVersionMismatch = errors.New("version mismatch")

// ErrKeyNotFound is returned (wrapped) by FilesystemMount.Stat, Open,
// and other key-level operations when the requested key does not exist.
// Distinct from sandbox.ErrNotFound (sandbox session not found).
var ErrKeyNotFound = errors.New("key not found")

// VersionMismatchError carries diagnostic info about a precondition
// failure. It matches ErrVersionMismatch via a custom Is method.
// Unwrap returns the underlying backend error (Cause), not the sentinel.
type VersionMismatchError struct {
    Key   string // logical key that failed
    Have  string // version the framework had at write time
    Want  string // version the backend reported (empty if unknown)
    Cause error  // optional underlying backend error
}
```

`errors.Is(err, sandbox.ErrVersionMismatch)` returns true for any wrapped `VersionMismatchError`. Use this to detect mount conflicts in tool error handling and decide whether to re-read the file before retrying.

```go
result, err := mount.Backend.Put(ctx, key, mime, size, body, manifest.Version(...))
if errors.Is(err, sandbox.ErrVersionMismatch) {
    // Backend has a newer version. Re-fetch and reapply.
}
```

The `Have`/`Want` fields on `VersionMismatchError` are useful for logging — extract via `errors.As`:

```go
var vme *sandbox.VersionMismatchError
if errors.As(err, &vme) {
    logger.Warn("mount conflict", "key", vme.Key, "had", vme.Have, "backend", vme.Want)
}
```

## ErrSuspended

**File:** `suspend.go`

```go
// ErrSuspended is returned by Execute() when a workflow step or
// processor suspends execution to await external input.
type ErrSuspended struct {
    Step    string           // step or agent that suspended
    Payload json.RawMessage  // context for the human
}
```

Call `Resume(ctx, data)` to continue execution with the human's response. Resume is single-use — the closure is nilled after the call.

```go
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    result, err = suspended.Resume(ctx, json.RawMessage(`"approved"`))
}
```

A **default TTL of 30 minutes** is applied automatically — abandoned suspensions are auto-released to prevent memory leaks. Call `WithSuspendTTL(d)` to override with a custom duration:

```go
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    suspended.WithSuspendTTL(5 * time.Minute) // override default 30m TTL
    // ... store suspended for later resume ...
}
```

Per-agent suspend budgets are enforced via `WithSuspendBudget(maxSnapshots, maxBytes)`. When the budget is exceeded, `Execute` returns an error instead of `ErrSuspended`. Counters are decremented when `Resume()`, `Release()`, or the TTL timer fires.

For manual control, call `Release()` to eagerly free the snapshot. After release (manual or TTL), `Resume()` returns an error. Both are safe to call multiple times.

```go
// Manual timeout pattern (when you can't use WithSuspendTTL).
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    select {
    case response := <-humanInput:
        result, err = suspended.Resume(ctx, response)
    case <-time.After(5 * time.Minute):
        suspended.Release() // free captured conversation snapshot
    }
}
```

## Compaction Errors

**File:** `compaction.go`

```go
var (
    ErrEmptyMessages      = errors.New("compaction: messages list is empty")
    ErrNoProvider         = errors.New("compaction: no summarizer provider")
    ErrSummaryParseFailed = errors.New("compaction: failed to parse <summary> block from response")
)
```

Returned by `Compactor.Compact`. Use `errors.Is` to detect:

- `ErrEmptyMessages` — caller passed `req.Messages` of length 0.
- `ErrNoProvider` — neither `req.SummarizerProvider` nor the Compactor's default is set.
- `ErrSummaryParseFailed` — the summarizer's response did not contain a parseable `<summary>...</summary>` block. The wrapping error includes a truncated copy of the raw response for diagnosis.

```go
result, err := compactor.Compact(ctx, req)
if errors.Is(err, oasis.ErrSummaryParseFailed) {
    // Model produced free-form text without the expected tags.
    // Retry with a stricter prompt or a more capable model.
}
```

## Error Patterns

### Tool Errors

Business errors go in `ToolResult.Error`, not as Go errors:

```go
// Business error — LLM sees it and can adjust
return oasis.ToolResult{Error: "not found"}, nil

// Infrastructure error — agent may abort
return oasis.ToolResult{}, fmt.Errorf("database connection failed")
```

### Transient vs Permanent

`WithRetry` retries only transient HTTP errors:

| Status | Behavior |
|--------|----------|
| 429 (Too Many Requests) | Retry with exponential backoff |
| 503 (Service Unavailable) | Retry with exponential backoff |
| All others | Return immediately |

### Error Messages

Convention: lowercase, no trailing period, wrap with context:

```go
fmt.Errorf("store init: %w", err)
fmt.Errorf("invalid schedule format: %s", schedule)
```
