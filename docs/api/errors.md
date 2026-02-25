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
    Status int
    Body   string
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
