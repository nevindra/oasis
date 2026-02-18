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
