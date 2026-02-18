# Workflows

Workflows are deterministic, DAG-based task orchestration primitives. Unlike Network (which uses an LLM to route between agents), a Workflow follows explicit step sequences and dependency edges defined at construction time. Parallel execution emerges naturally when multiple steps share the same predecessor.

Workflow implements the `Agent` interface, enabling recursive composition: Networks can contain Workflows, and Workflows can contain Agents (LLMAgent, Network, or other Workflows).

## When to Use Workflow vs LLMAgent vs Network

| Primitive | Routing | Best For |
|-----------|---------|----------|
| `LLMAgent` | Single LLM with tool loop | Open-ended tasks where the LLM decides tool usage |
| `Network` | LLM router picks subagents | Dynamic delegation where task routing depends on LLM judgement |
| `Workflow` | Explicit DAG, no LLM routing | Deterministic pipelines: ETL, multi-step processing, orchestrated agent chains |

Use Workflow when you know the execution order at build time. Use Network when the LLM needs to decide which agent handles what.

## Quick Start

```go
wf, err := oasis.NewWorkflow("greet", "Greets a user",
    // Step 1: prepare the greeting
    oasis.Step("prepare", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        name := wCtx.Input() // original AgentTask.Input
        wCtx.Set("greeting", "Hello, "+name+"!")
        return nil
    }),

    // Step 2: format the greeting (runs after prepare)
    oasis.Step("format", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        g, _ := wCtx.Get("greeting")
        wCtx.Set("format.output", "✨ "+g.(string)+" ✨")
        return nil
    }, oasis.After("prepare")),
)
if err != nil {
    log.Fatal(err)
}

// Workflow implements Agent — use it like any other agent.
result, err := wf.Execute(ctx, oasis.AgentTask{Input: "Alice"})
// result.Output == "✨ Hello, Alice! ✨"
```

## Step Types

### Step (function)

Runs a `StepFunc` — the most flexible step type. The function reads and writes context keys directly.

```go
oasis.Step("transform", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
    raw, _ := wCtx.Get("data")
    wCtx.Set("transform.output", strings.ToUpper(raw.(string)))
    return nil
}, oasis.After("fetch"))
```

### AgentStep

Delegates to an `Agent` (LLMAgent, Network, or another Workflow). Input is read from context via `InputFrom()`, or from the original task input if not set. Output is written to `"{name}.output"`.

The original `AgentTask.Context` (thread ID, user ID, chat ID) and `Attachments` are propagated to the sub-agent, so memory features (conversation history, user memory, cross-thread search) work correctly inside workflow steps.

```go
researcher := oasis.NewLLMAgent("researcher", "Searches info", provider,
    oasis.WithTools(searchTool),
)

oasis.AgentStep("research", researcher,
    oasis.InputFrom("query"),   // read input from context key "query"
    oasis.OutputTo("findings"), // write output to "findings" instead of "research.output"
)
```

Token usage from AgentStep executions is automatically accumulated into the workflow's total usage.

### ToolStep

Calls a single tool function by name. Args are read from context via `ArgsFrom()`. The result is written to `"{name}.result"`.

```go
oasis.ToolStep("search", searchTool, "web_search",
    oasis.ArgsFrom("search_params"), // read JSON args from context
    oasis.After("prepare"),
)
```

If `ArgsFrom` is not set, empty JSON `{}` is used. The value at the args key can be `json.RawMessage`, a JSON string, or any value that can be marshalled.

### ForEach

Iterates over a `[]any` collection from context. Each iteration receives its element via `ForEachItem(ctx)` and its index via `ForEachIndex(ctx)` — both are carried on the Go context (not WorkflowContext) for concurrency safety.

```go
oasis.ForEach("process-items", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
    item, _ := oasis.ForEachItem(ctx)
    idx, _ := oasis.ForEachIndex(ctx)
    // process item...
    return nil
},
    oasis.IterOver("items"),    // context key with []any
    oasis.Concurrency(4),      // 4 parallel goroutines (default 1)
    oasis.After("fetch-items"),
)
```

### DoUntil

Repeats a step function until the condition returns true (evaluated after each iteration). `MaxIter()` sets a safety cap (default 10).

```go
oasis.DoUntil("poll", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
    result := checkStatus()
    wCtx.Set("status", result)
    return nil
},
    oasis.Until(func(wCtx *oasis.WorkflowContext) bool {
        s, _ := wCtx.Get("status")
        return s == "ready"
    }),
    oasis.MaxIter(20),
)
```

### DoWhile

Repeats while the condition returns true. The first iteration always runs; the condition is checked before subsequent iterations. `MaxIter()` sets a safety cap (default 10).

```go
oasis.DoWhile("paginate", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
    page := fetchNextPage()
    wCtx.Set("has_more", page.HasMore)
    return nil
},
    oasis.While(func(wCtx *oasis.WorkflowContext) bool {
        v, _ := wCtx.Get("has_more")
        return v == true
    }),
    oasis.MaxIter(50),
)
```

## Step Options

| Option | Applies To | Description |
|--------|-----------|-------------|
| `After(steps...)` | All | Dependency edges: run after named steps complete |
| `When(fn)` | All | Condition gate: skip step if fn returns false |
| `InputFrom(key)` | AgentStep | Context key for agent input (default: original task input) |
| `ArgsFrom(key)` | ToolStep | Context key for tool arguments (default: empty `{}`) |
| `OutputTo(key)` | AgentStep, ToolStep | Override default output key |
| `Retry(n, delay)` | All | Retry up to n times with delay between attempts |
| `IterOver(key)` | ForEach | Context key containing `[]any` collection |
| `Concurrency(n)` | ForEach | Max parallel iterations (default 1) |
| `Until(fn)` | DoUntil | Exit condition (evaluated after each iteration) |
| `While(fn)` | DoWhile | Continue condition (evaluated before each iteration after the first) |
| `MaxIter(n)` | DoUntil, DoWhile | Safety cap on loop iterations (default 10) |

## Workflow Options

| Option | Description |
|--------|-------------|
| `WithOnFinish(fn)` | Callback invoked after workflow completes (success or failure) |
| `WithOnError(fn)` | Callback invoked when a step fails (receives step name + error) |
| `WithDefaultRetry(n, delay)` | Default retry for steps that don't specify their own |

## Control Flow Patterns

### Sequential

Steps run in order by declaring `After()` edges:

```go
oasis.Step("a", fnA),
oasis.Step("b", fnB, oasis.After("a")),
oasis.Step("c", fnC, oasis.After("b")),
```

### Parallel

Steps without dependency relationships run concurrently:

```go
oasis.Step("fetch-users", fetchUsers),
oasis.Step("fetch-orders", fetchOrders),
oasis.Step("merge", mergeData, oasis.After("fetch-users", "fetch-orders")),
```

`fetch-users` and `fetch-orders` run in parallel. `merge` waits for both.

### Conditional Branching

Use `When()` to create if/else-style branches:

```go
oasis.Step("classify", classifyInput),

oasis.Step("handle-text", handleText,
    oasis.After("classify"),
    oasis.When(func(wCtx *oasis.WorkflowContext) bool {
        t, _ := wCtx.Get("type")
        return t == "text"
    }),
),

oasis.Step("handle-image", handleImage,
    oasis.After("classify"),
    oasis.When(func(wCtx *oasis.WorkflowContext) bool {
        t, _ := wCtx.Get("type")
        return t == "image"
    }),
),
```

Steps skipped by `When()` are treated as satisfied — their dependents still run (they don't cascade failure).

### Fan-out / Fan-in

Combine ForEach with a downstream merge step:

```go
oasis.Step("split", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
    wCtx.Set("chunks", []any{"chunk1", "chunk2", "chunk3"})
    return nil
}),

oasis.ForEach("process", processChunk,
    oasis.IterOver("chunks"),
    oasis.Concurrency(3),
    oasis.After("split"),
),

oasis.Step("merge", mergeResults, oasis.After("process")),
```

## Data Flow

### WorkflowContext

The shared state map that flows between steps. All methods are safe for concurrent use.

```go
// Read a value
value, ok := wCtx.Get("key")

// Write a value
wCtx.Set("key", value)

// Get the original task input
input := wCtx.Input()
```

### Naming Conventions

| Step Type | Default Output Key |
|-----------|-------------------|
| `Step` | (none — function writes explicitly via `wCtx.Set`) |
| `AgentStep` | `"{name}.output"` |
| `ToolStep` | `"{name}.result"` |

All can be overridden with `OutputTo("custom_key")`.

### ForEach Iteration Data

Inside a ForEach step function, use the context helpers:

```go
item, ok := oasis.ForEachItem(ctx)   // current element
index, ok := oasis.ForEachIndex(ctx) // 0-based index
```

These are carried on the Go `context.Context` (not WorkflowContext), so each goroutine sees its own values — safe under concurrent iteration.

## Error Handling

### WorkflowError

When a step fails, `Workflow.Execute` returns a `*WorkflowError` that carries the full `WorkflowResult`:

```go
result, err := wf.Execute(ctx, task)
if err != nil {
    var wfErr *oasis.WorkflowError
    if errors.As(err, &wfErr) {
        fmt.Println("failed step:", wfErr.StepName)
        fmt.Println("cause:", wfErr.Err)

        // Inspect per-step outcomes
        for name, step := range wfErr.Result.Steps {
            fmt.Printf("  %s: %s\n", name, step.Status)
        }
    }
}
```

`WorkflowError` implements `Unwrap()`, so `errors.Is` chains work on the underlying step error.

When a Workflow is used as a sub-agent inside a Network, the error is surfaced to the router LLM as a tool result — the LLM can then decide how to handle the failure.

### Fail-Fast

The first step failure cancels the workflow context. All in-flight steps receive the cancellation, and downstream steps are marked `StepSkipped`. ForEach steps cancel remaining iterations on first failure.

### Failure Cascade

When a step fails, all steps that depend on it (directly or transitively) are skipped. Steps skipped by a `When()` condition do NOT propagate failure — only steps skipped due to upstream failure cascade.

### Retry

Configure per-step or workflow-wide retries. Retry works on all step types: `Step`, `AgentStep`, `ToolStep`, `ForEach`, `DoUntil`, and `DoWhile`.

```go
// Per-step retry (works on any step type)
oasis.Step("flaky", flakyFn, oasis.Retry(3, 2*time.Second))
oasis.ForEach("batch", batchFn, oasis.IterOver("items"), oasis.Retry(2, time.Second))

// Default retry for all steps
oasis.WithDefaultRetry(2, time.Second)
```

Total attempts = 1 + retry count. Retries stop early on context cancellation.

### Callbacks

```go
oasis.WithOnFinish(func(result oasis.WorkflowResult) {
    log.Printf("workflow finished: %s, %d steps", result.Status, len(result.Steps))
}),

oasis.WithOnError(func(stepName string, err error) {
    log.Printf("step %s failed: %v", stepName, err)
}),
```

Callback panics are recovered and logged — they never affect the workflow result.

## DAG Validation

`NewWorkflow` validates the step graph at construction time and returns an error for:
- **Duplicate step names**
- **Unknown dependencies** — `After()` references a step that doesn't exist
- **Cycles** — detected via Kahn's algorithm (topological sort)

It also logs a warning for **unreachable steps** (steps with dependencies that are never satisfied by any root).

## Composition

### Workflow Inside a Network

Since Workflow implements `Agent`, it can be a subagent in a Network:

```go
pipeline := oasis.NewWorkflow("etl", "Extract-transform-load pipeline", ...)

coordinator := oasis.NewNetwork("main", "Routes tasks", routerProvider,
    oasis.WithAgents(pipeline, writer),
)
```

The Network's router LLM sees the workflow as `agent_etl` and can delegate tasks to it.

### Agent Inside a Workflow

Use `AgentStep` to run any Agent as a workflow step:

```go
researcher := oasis.NewLLMAgent("researcher", "Searches info", provider,
    oasis.WithTools(searchTool),
)

wf, _ := oasis.NewWorkflow("research-pipeline", "Structured research",
    oasis.Step("prepare-query", prepareQuery),
    oasis.AgentStep("research", researcher,
        oasis.InputFrom("query"),
        oasis.After("prepare-query"),
    ),
    oasis.Step("format", formatResults, oasis.After("research")),
)
```

### Workflow Inside a Workflow

Workflows compose recursively via `AgentStep`:

```go
inner, _ := oasis.NewWorkflow("inner", "Sub-pipeline", ...)

outer, _ := oasis.NewWorkflow("outer", "Main pipeline",
    oasis.Step("prepare", prepareFn),
    oasis.AgentStep("sub-pipeline", inner, oasis.After("prepare")),
    oasis.Step("finalize", finalizeFn, oasis.After("sub-pipeline")),
)
```

## Full Example: Research Pipeline

```go
researcher := oasis.NewLLMAgent("researcher", "Searches for information", provider,
    oasis.WithTools(searchTool, knowledgeTool),
)

writer := oasis.NewLLMAgent("writer", "Writes polished content", provider,
    oasis.WithPrompt("You are a skilled technical writer."),
)

pipeline, err := oasis.NewWorkflow("research-pipeline", "Research and write about a topic",
    // Step 1: Prepare search query from input
    oasis.Step("prepare", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        wCtx.Set("query", "Research thoroughly: "+wCtx.Input())
        return nil
    }),

    // Step 2: Run the researcher agent
    oasis.AgentStep("research", researcher,
        oasis.InputFrom("query"),
        oasis.After("prepare"),
        oasis.Retry(2, 5*time.Second),
    ),

    // Step 3: Run the writer agent with research findings
    oasis.AgentStep("write", writer,
        oasis.InputFrom("research.output"),
        oasis.After("research"),
    ),

    // Callbacks
    oasis.WithOnError(func(step string, err error) {
        log.Printf("Pipeline failed at %s: %v", step, err)
    }),
)
if err != nil {
    log.Fatal(err)
}

result, err := pipeline.Execute(ctx, oasis.AgentTask{Input: "Go error handling best practices"})
fmt.Println(result.Output)
```
