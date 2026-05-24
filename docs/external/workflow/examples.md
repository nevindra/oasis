# Workflow Examples

## Recipe 1: Sequential pipeline (A then B then C)

**Goal:** Run three agents in order, passing each agent's output as the next
agent's input.

```go
package main

import (
    "context"
    "fmt"

    "github.com/nevindra/oasis/workflow"
    "github.com/nevindra/oasis/core"
)

func main() {
    wf, err := workflow.New("pipeline", "Three-stage research pipeline",
        workflow.AgentStep("fetch",     fetchAgent),
        workflow.AgentStep("analyze",   analyzeAgent,
            workflow.After("fetch"),
            workflow.InputFrom("fetch.output")),
        workflow.AgentStep("summarize", summarizeAgent,
            workflow.After("analyze"),
            workflow.InputFrom("analyze.output")),
    )
    if err != nil {
        panic(err)
    }

    result, err := wf.Execute(context.Background(), core.AgentTask{Input: "climate change 2025"})
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output) // summarize step's output
}
```

**Plain-English walkthrough:**
- `After("fetch")` means `"analyze"` waits for `"fetch"` to finish before it
  starts.
- `InputFrom("fetch.output")` routes the fetch result (stored by the runtime
  under `"fetch.output"`) directly into the analyze step's `AgentTask.Input`.
- The final `result.Output` comes from the last successful step in declaration
  order, which is `"summarize"`.

**Variations:**
- Skip `InputFrom` on the second step to always use the original task input
  instead of the previous step's output.
- Add `workflow.Retry(2, time.Second)` to any `AgentStep` to retry on transient
  LLM errors.
- Insert a custom `workflow.Step` between agents to transform or validate output
  before handing it to the next agent.

---

## Recipe 2: Parallel fan-out + join

**Goal:** Run two independent agents simultaneously, then merge their outputs
in a final step.

```go
wf, err := workflow.New("parallel", "Parallel research + merge",
    workflow.Step("seed", func(_ context.Context, wCtx *workflow.WorkflowContext) error {
        // Root step: nothing to wait for. Seeds a shared topic.
        wCtx.Set("topic", wCtx.Input())
        return nil
    }),
    workflow.AgentStep("sources",  sourcesAgent,  workflow.After("seed")),
    workflow.AgentStep("stats",    statsAgent,    workflow.After("seed")),
    workflow.Step("merge", func(_ context.Context, wCtx *workflow.WorkflowContext) error {
        src, _  := wCtx.Get("sources.output")
        stat, _ := wCtx.Get("stats.output")
        wCtx.Set("merge.output", fmt.Sprintf("Sources:\n%v\n\nStats:\n%v", src, stat))
        return nil
    }, workflow.After("sources", "stats")),
)
```

**Plain-English walkthrough:**
- `"sources"` and `"stats"` both depend on `"seed"`, so they run in parallel
  as soon as `"seed"` finishes — no extra goroutine code needed.
- `"merge"` declares `After("sources", "stats")` — it waits until both finish.
- Inside `"merge"`, the outputs are read from context by the keys the runtime
  wrote automatically (`"{name}.output"`).

**Variations:**
- Add more parallel branches by adding more `AgentStep` calls with
  `After("seed")`.
- If one branch is slow and the other is fast, the fast branch's result is
  already in context before `"merge"` starts — no polling needed.

---

## Recipe 3: Conditional branching

**Goal:** Run a quick check step, then route to different agents depending on
the result.

```go
wf, err := workflow.New("conditional", "Route based on score",
    workflow.AgentStep("score", scorerAgent),
    workflow.AgentStep("premium", premiumAgent,
        workflow.After("score"),
        workflow.When(func(wCtx *workflow.WorkflowContext) bool {
            v, ok := wCtx.Get("score.output")
            return ok && v.(string) >= "80" // string comparison is fine for "0"–"100"
        }),
        workflow.InputFrom("score.output"),
    ),
    workflow.AgentStep("standard", standardAgent,
        workflow.After("score"),
        workflow.When(func(wCtx *workflow.WorkflowContext) bool {
            v, ok := wCtx.Get("score.output")
            return !ok || v.(string) < "80"
        }),
        workflow.InputFrom("score.output"),
    ),
)
```

**Plain-English walkthrough:**
- Both `"premium"` and `"standard"` depend on `"score"` and start evaluating
  their `When()` condition right after `"score"` finishes.
- Only the step whose condition returns `true` actually runs; the other is
  marked `StepSkipped`.
- Skipped-by-condition steps are treated as satisfied dependencies — any step
  that declared `After("premium", "standard")` would still fire correctly.

**Variations:**
- For more complex routing logic, store a typed value in context (e.g. an int)
  and compare it numerically inside `When`.
- For the JSON-definition path (`FromDefinition`), use a `NodeCondition` node
  with an `Expression` field — the runtime evaluates `"{{score.output}} >= 80"`
  automatically.

---

## Recipe 4: ForEach — process a list in parallel

**Goal:** Run an agent once for each item in a list, up to 4 at a time.

```go
wf, err := workflow.New("batch", "Process items in parallel",
    workflow.Step("load", func(_ context.Context, wCtx *workflow.WorkflowContext) error {
        wCtx.Set("items", []any{"item-1", "item-2", "item-3", "item-4", "item-5"})
        return nil
    }),
    workflow.ForEach("process", func(ctx context.Context, wCtx *workflow.WorkflowContext) error {
        item, _ := workflow.ForEachItem(ctx)
        idx, _  := workflow.ForEachIndex(ctx)

        result, err := processorAgent.Execute(ctx, core.AgentTask{Input: fmt.Sprintf("%v", item)})
        if err != nil {
            return err
        }
        wCtx.Set(fmt.Sprintf("result.%d", idx), result.Output)
        return nil
    },
        workflow.After("load"),
        workflow.IterOver("items"),
        workflow.Concurrency(4),
    ),
)
```

**Plain-English walkthrough:**
- `"load"` writes a `[]any` slice to context under the key `"items"`.
- `ForEach` reads that slice via `IterOver("items")` and runs the step function
  once per element, up to 4 concurrently.
- Inside the function, `ForEachItem(ctx)` returns the current element and
  `ForEachIndex(ctx)` returns its 0-based position — both are carried on the Go
  context so each goroutine sees its own values.
- Results are keyed by index (`"result.0"`, `"result.1"`, ...) so a later step
  can collect them.

**Variations:**
- Set `Concurrency(1)` for sequential processing (the default).
- Return an error from the step function to cancel the remaining iterations
  immediately.

---

## Recipe 5: Retry on failure

**Goal:** Retry a flaky network call up to 3 times with a 2-second delay.

```go
wf, err := workflow.New("resilient", "Resilient fetch",
    workflow.AgentStep("fetch", fetchAgent,
        workflow.Retry(3, 2*time.Second),
    ),
    workflow.AgentStep("process", processAgent,
        workflow.After("fetch"),
        workflow.InputFrom("fetch.output"),
    ),
)
```

**Plain-English walkthrough:**
- `Retry(3, 2*time.Second)` means: try once, and if it fails, wait 2 seconds
  and try again — up to 3 more times (4 total attempts).
- If all attempts fail, the workflow fails at `"fetch"` and `"process"` is
  marked `StepSkipped`.
- Use `WithDefaultRetry(2, time.Second)` on the workflow to apply a retry
  policy to every step that does not set its own.

**Variations:**
- Combine with `WithOnError` to log or alert on each failed attempt.
- Retries do not re-run on context cancellation or `Suspend()` — only on actual
  Go errors.

---

## Recipe 6: Workflow inside a Network

**Goal:** Use a Workflow as a child of a Network so the router LLM can delegate
an entire multi-step task to it.

```go
// Build the workflow normally.
researchWF, err := workflow.New("research", "Deep research pipeline",
    workflow.AgentStep("fetch",     fetchAgent),
    workflow.AgentStep("synthesize", synthesizeAgent, workflow.After("fetch"),
        workflow.InputFrom("fetch.output")),
)
if err != nil {
    panic(err)
}

// Use it as a Network child — Workflow implements core.Agent.
net := network.New("team", "Research team",
    routerProvider,
    network.WithChildren(researchWF, writerAgent),
)

result, err := net.Execute(context.Background(), core.AgentTask{Input: "write a report on fusion energy"})
```

**Plain-English walkthrough:**
- `*Workflow` implements `core.Agent`, so it can be passed to
  `network.WithChildren` just like an LLMAgent.
- The router LLM sees `"research"` as a tool it can call. When it calls it, the
  full two-step workflow runs and the Network gets back the final output.
- This enables deep composition: Networks can contain Workflows that contain
  other Networks.

**Variations:**
- Reverse it: put a Network inside a workflow step using `AgentStep("research", myNetwork)`.
- Chain multiple workflows as sequential steps inside a parent workflow.

---

## Recipe 7: Inspect per-step results on failure

**Goal:** When the workflow fails, find out which step failed and why.

```go
result, err := wf.Execute(ctx, core.AgentTask{Input: "..."})
if err != nil {
    var wfErr *workflow.WorkflowError
    if errors.As(err, &wfErr) {
        fmt.Printf("first failure: step %q — %v\n", wfErr.StepName, wfErr.Err)
        for name, step := range wfErr.Result.Steps {
            fmt.Printf("  %s: %s", name, step.Status)
            if step.Error != nil {
                fmt.Printf(" (%v)", step.Error)
            }
            fmt.Println()
        }
    }
}
```

**Plain-English walkthrough:**
- `errors.As(err, &wfErr)` unwraps the `*WorkflowError` from the returned error.
- `wfErr.StepName` is the name of the first step that failed.
- `wfErr.Result.Steps` is a map of every step's final status — useful for
  understanding which downstream steps were skipped as a result of the failure.
- `result.Output` may still be non-empty if earlier steps succeeded; it holds
  the last successful output before the failure.

**Variations:**
- Register `WithOnError` at construction time to get step-level callbacks
  without inspecting the error at call time.
- Register `WithOnFinish` to always receive the full `WorkflowResult` after
  every run, success or failure.
