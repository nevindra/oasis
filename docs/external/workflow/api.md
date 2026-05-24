# Workflow API

## Types

### `Workflow`

The executable DAG. Implements `core.Agent`; safe to pass anywhere an agent is
accepted. Concurrent calls to `Execute` are safe — each call gets its own
`WorkflowContext` and execution state.

### `WorkflowContext`

The shared state passed to every step in one execution. All methods are safe for
concurrent use (reads use `sync.RWMutex`).

| Method | Signature | Notes |
|--------|-----------|-------|
| `Get` | `(key string) (any, bool)` | Returns `(nil, false)` if the key does not exist. |
| `Set` | `(key string, value any)` | Overwrites any previous value for that key. |
| `Input` | `() string` | The original `AgentTask.Input` that started the workflow. |
| `Resolve` | `(template string) string` | Replaces `{{key}}` placeholders from context values. Unknown keys resolve to empty string. Single-pass — resolved values are NOT re-expanded. |
| `ResolveJSON` | `(template string) json.RawMessage` | Like `Resolve` but returns JSON. A single-placeholder template with a non-string value marshals the value to JSON directly. Mixed-text templates produce a JSON string. |

### `WorkflowDefinition`

JSON-serializable description of a workflow DAG. Use with `FromDefinition` to
build a `*Workflow` from config rather than from code.

| Field | Type | Notes |
|-------|------|-------|
| `Name` | `string` | Workflow identifier. |
| `Description` | `string` | Human-readable description. |
| `Nodes` | `[]NodeDefinition` | The steps. |
| `Edges` | `[][2]string` | Dependency pairs `[from, to]`. |

### `NodeDefinition`

One step in a `WorkflowDefinition`.

| Field | Type | Notes |
|-------|------|-------|
| `ID` | `string` | Unique step identifier within this workflow. |
| `Type` | `NodeType` | `"llm"`, `"tool"`, `"condition"`, or `"template"`. |
| `Agent` | `string` | Registry key for `NodeLLM` steps. |
| `Input` | `string` | Input template for `NodeLLM`; may contain `{{key}}` placeholders. |
| `Tool` | `string` | Registry key for `NodeTool` steps. |
| `ToolName` | `string` | Override for tool dispatch name; defaults to `Tool`. |
| `Args` | `map[string]any` | Tool arguments; values may contain `{{key}}` templates. |
| `Expression` | `string` | Comparison expression for `NodeCondition` (e.g. `"{{score}} >= 80"`). |
| `TrueBranch` | `[]string` | Node IDs to enable when condition is true. |
| `FalseBranch` | `[]string` | Node IDs to enable when condition is false. |
| `Template` | `string` | Template string for `NodeTemplate` steps. |
| `OutputTo` | `string` | Override the default output key written to context. |
| `Retry` | `int` | Max retry count (0 = no retries); delay is fixed at 1 second. |

### `DefinitionRegistry`

Maps string names in a `WorkflowDefinition` to concrete Go objects.

| Field | Type | Notes |
|-------|------|-------|
| `Agents` | `map[string]core.Agent` | Agent implementations for `NodeLLM` steps. |
| `Tools` | `map[string]core.AnyTool` | Tool implementations for `NodeTool` steps. |
| `Conditions` | `map[string]func(*WorkflowContext) bool` | Custom condition functions for `NodeCondition` steps; checked before expression evaluation. |

### `StepResult`

The outcome of a single step.

| Field | Type | Notes |
|-------|------|-------|
| `Name` | `string` | Step identifier. |
| `Status` | `StepStatus` | One of the `Step*` constants below. |
| `Output` | `string` | Step output (from context); empty on failure or skip. |
| `Error` | `error` | Non-nil only when `Status == StepFailed`. |
| `Duration` | `time.Duration` | Wall-clock time including retries. |

### `WorkflowResult`

Aggregate outcome delivered via `WithOnFinish`.

| Field | Type | Notes |
|-------|------|-------|
| `Status` | `StepStatus` | `StepSuccess` if all steps succeeded or were condition-skipped; `StepFailed` if any step failed. |
| `Steps` | `map[string]StepResult` | Per-step outcomes keyed by step name. |
| `Usage` | `core.Usage` | Aggregate token usage across all `AgentStep` executions. |

### `StepStatus` constants

| Constant | Value | Meaning |
|----------|-------|---------|
| `StepPending` | `"pending"` | Not yet started. |
| `StepRunning` | `"running"` | Currently executing. |
| `StepSuccess` | `"success"` | Completed without error. |
| `StepSkipped` | `"skipped"` | Skipped by `When()` condition or upstream failure. |
| `StepFailed` | `"failed"` | Returned an error after all retries. |
| `StepSuspended` | `"suspended"` | Paused awaiting external input. |

### `NodeType` constants

| Constant | Value | Meaning |
|----------|-------|---------|
| `NodeLLM` | `"llm"` | Delegates to a registered `core.Agent`. |
| `NodeTool` | `"tool"` | Calls a registered `core.AnyTool` directly. |
| `NodeCondition` | `"condition"` | Evaluates an expression and routes to `true_branch` or `false_branch`. |
| `NodeTemplate` | `"template"` | Resolves a `{{key}}` template string and writes the result to context. |

---

## Constructors

### `New`

```go
func New(name, description string, opts ...WorkflowOption) (*Workflow, error)
```

Creates a `Workflow`. Steps and workflow-level settings are passed as
`WorkflowOption` values. Returns an error (never panics later) when:
- A step name is duplicated.
- An `After()` target names a step that does not exist.
- A cycle is detected in the dependency graph.

Logs a warning (not an error) for unreachable steps. Zero-value config (no
options) produces a valid, runnable workflow with no steps.

Re-exported as `oasis.NewWorkflow`.

### `FromDefinition`

```go
func FromDefinition(def WorkflowDefinition, reg DefinitionRegistry) (*Workflow, error)
```

Builds a `*Workflow` from a JSON-serializable definition and a registry of Go
objects. Same validation as `New`. Use when workflow topology is loaded from
config at runtime.

---

## Step definition functions

These functions return `WorkflowOption` and are passed to `New`.

### `Step`

```go
func Step(name string, fn StepFunc, opts ...StepOption) WorkflowOption
```

Defines a step that runs a custom function. `fn` receives the Go
`context.Context` (cancelled on workflow failure) and the shared
`*WorkflowContext`. The function writes its own output via `wCtx.Set()`.

`StepFunc` type: `func(ctx context.Context, wCtx *WorkflowContext) error`

### `AgentStep`

```go
func AgentStep(name string, agent core.Agent, opts ...StepOption) WorkflowOption
```

Defines a step that delegates to a `core.Agent`. Input defaults to
`WorkflowContext.Input()`; override with `InputFrom()`. Output is written to
`"{name}.output"` automatically; override with `OutputTo()`. Token usage is
accumulated into `WorkflowResult.Usage`.

`agent` may be any `core.Agent` implementation: LLMAgent, Network, or another
Workflow.

### `ForEach`

```go
func ForEach(name string, fn StepFunc, opts ...StepOption) WorkflowOption
```

Runs `fn` once per element in a `[]any` slice stored in context. Set the
collection key with `IterOver()`. Inside `fn`, retrieve the current element and
index via `ForEachItem(ctx)` and `ForEachIndex(ctx)`. Concurrency defaults to
`1`; set with `Concurrency(n)`. Cancels remaining iterations on the first error.

### `DoUntil`

```go
func DoUntil(name string, fn StepFunc, opts ...StepOption) WorkflowOption
```

Repeats `fn` until `Until()` returns `true` (checked after each iteration).
Requires `Until()`. Safety cap from `MaxIter()` defaults to `10`. Returns
`ErrMaxIterExceeded` when the cap is reached.

### `DoWhile`

```go
func DoWhile(name string, fn StepFunc, opts ...StepOption) WorkflowOption
```

Repeats `fn` while `While()` returns `true` (checked before each iteration after
the first; the first iteration always runs). Requires `While()`. Safety cap from
`MaxIter()` defaults to `10`. Returns `ErrMaxIterExceeded` when the cap is
reached.

---

## Methods

### `Execute`

```go
func (w *Workflow) Execute(ctx context.Context, task core.AgentTask, opts ...core.RunOption) (core.AgentResult, error)
```

Runs the workflow DAG. Steps whose dependencies are satisfied are launched
concurrently. The first step failure cancels in-flight steps via context
cancellation and marks downstream steps as `StepSkipped`.

Return values:

| Condition | Returns |
|-----------|---------|
| All steps succeed or are condition-skipped | `(AgentResult, nil)` — `Output` is the last successful step's output in declaration order. |
| Any step fails | `(AgentResult, *WorkflowError)` — partial output in `AgentResult`; inspect `WorkflowError.Result.Steps` for per-step detail. |
| A step called `Suspend()` | `(AgentResult{}, *ErrSuspended)` — call `Resume` when input is ready. |

`core.WithStream(ch)` emits `EventStepStart` and `EventStepFinish` for each
step, and `EventStepProgress` for `ForEach` iterations.

Per-call overrides (`core.WithOverrides`) are not yet supported and return an
error.

Thread-safe: concurrent calls to `Execute` are safe.

### `Name`

```go
func (w *Workflow) Name() string
```

Returns the workflow's identifier. Thread-safe.

### `Description`

```go
func (w *Workflow) Description() string
```

Returns the human-readable description. Used by Network to generate a tool
definition when this Workflow is a Network child. Thread-safe.

---

## Step options

| Option | Signature | Default | Notes |
|--------|-----------|---------|-------|
| `After` | `After(steps ...string) StepOption` | No deps (root step) | Dependency edges. Step runs after all named steps succeed or are condition-skipped. Unknown names cause `New` to error. |
| `When` | `When(fn func(*WorkflowContext) bool) StepOption` | Always runs | If `fn` returns `false`, marks the step `StepSkipped`; dependents treat it as satisfied. |
| `InputFrom` | `InputFrom(key string) StepOption` | `WorkflowContext.Input()` | `AgentStep` only. Context key whose value becomes `AgentTask.Input`. |
| `OutputTo` | `OutputTo(key string) StepOption` | `"{name}.output"` or `"{name}.result"` | Override the default context key for output. |
| `Retry` | `Retry(n int, delay time.Duration) StepOption` | No retries | Retries up to `n` times. Total attempts = `1 + n`. Suspension and context cancellation skip retries. |
| `IterOver` | `IterOver(key string) StepOption` | Required for `ForEach` | Context key holding `[]any` collection. |
| `Concurrency` | `Concurrency(n int) StepOption` | `1` | `ForEach` only. Max parallel iterations. |
| `Until` | `Until(fn func(*WorkflowContext) bool) StepOption` | Required for `DoUntil` | Exit condition, checked after each iteration. |
| `While` | `While(fn func(*WorkflowContext) bool) StepOption` | Required for `DoWhile` | Continue condition, checked before each iteration after the first. |
| `MaxIter` | `MaxIter(n int) StepOption` | `10` | Loop safety cap for `DoUntil` / `DoWhile`. |
| `ArgsFrom` | `ArgsFrom(key string) StepOption` | none | Tool-call step (definition path). Context key holding tool arguments as `json.RawMessage`, JSON string, or any JSON-serializable value. |

---

## Workflow options

| Option | Signature | Default | Notes |
|--------|-----------|---------|-------|
| `WithOnFinish` | `WithOnFinish(fn func(WorkflowResult)) WorkflowOption` | No callback | Called after every execution, success or failure. Panics in `fn` are recovered and logged. |
| `WithOnError` | `WithOnError(fn func(string, error)) WorkflowOption` | No callback | Called when any step fails. Receives step name and error. Panics recovered. |
| `WithDefaultRetry` | `WithDefaultRetry(n int, delay time.Duration) WorkflowOption` | No retries | Applies to all steps without their own `Retry()`. |
| `WithWorkflowTracer` | `WithWorkflowTracer(t core.Tracer) WorkflowOption` | No tracing | Emits spans for workflow execution and per-step lifecycle. |
| `WithWorkflowLogger` | `WithWorkflowLogger(l *slog.Logger) WorkflowOption` | No output | Structured logger for step lifecycle and retry events. |

---

## ForEach helpers

```go
func ForEachItem(ctx context.Context) (any, bool)
func ForEachIndex(ctx context.Context) (int, bool)
```

Retrieve the current element and its 0-based index inside a `ForEach` step
function. Carried on `context.Context` (not `WorkflowContext`) so concurrent
iterations each see their own element. Return `(nil, false)` / `(-1, false)`
when called outside a `ForEach` step.

---

## Suspend helpers

```go
func Suspend(payload json.RawMessage) error
```

Return `Suspend(payload)` from any `StepFunc` to pause the workflow. The caller
receives `*ErrSuspended` from `Execute`.

### `ErrSuspended`

| Field / Method | Notes |
|----------------|-------|
| `Step string` | Name of the suspended step. |
| `Payload json.RawMessage` | Payload passed to `Suspend`. |
| `Resume(ctx, data json.RawMessage) (AgentResult, error)` | Continues from the suspended step. Thread-safe. |
| `ResumeStream(ctx, data json.RawMessage, ch chan<- core.StreamEvent) (AgentResult, error)` | Like `Resume` with streaming. Closes `ch` before returning. |

---

## Errors

| Error | When | How to handle |
|-------|------|---------------|
| Construction-time `error` from `New` | Duplicate step, unknown `After()` target, or cycle. | Fix the step graph; these are compile-equivalent errors. |
| `*WorkflowError` from `Execute` | One or more steps failed after retries. | Use `errors.As`; inspect `wfErr.StepName`, `wfErr.Err`, and `wfErr.Result.Steps`. |
| `*ErrSuspended` from `Execute` | A step called `Suspend()`. | Call `.Resume(ctx, data)` when input is available. |
| `ErrMaxIterExceeded` from `Execute` | A loop step hit its `MaxIter` cap. | Use `errors.Is`; increase `MaxIter()` or fix the exit condition. |
