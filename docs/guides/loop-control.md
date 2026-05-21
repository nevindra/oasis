# Loop Control

Three optional hooks let you steer the agent loop from application code:

- `PrepareStep` — mutate the request, model, or tools before each LLM call
- `OnIterationComplete` — inspect the iteration result and decide what's next
- `OnError` — recover from mid-loop errors

Hooks compose with the existing PreProcessor / PostProcessor / PostToolProcessor pipelines: processors transform the data flowing through; hooks decide what the loop does next. The two systems are independent.

## Pattern 1 — Validation loop

Re-prompt the LLM until the response satisfies a condition. Useful for "the agent must produce valid JSON" or "the response must mention X."

```go
package main

import (
	"context"
	"encoding/json"
	"log"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

type ProductSpec struct {
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	SKU   string  `json:"sku"`
}

func main() {
	provider := /* ... */

	a := agent.NewLLMAgent("extractor", "extracts product specs", provider,
		agent.WithMaxIter(5),
		agent.WithOnIterationComplete(func(ctx context.Context, iter int, snap *agent.IterationSnapshot) (agent.IterationDecision, error) {
			var spec ProductSpec
			if err := json.Unmarshal([]byte(snap.Response.Content), &spec); err == nil && spec.Name != "" && spec.SKU != "" {
				return agent.Stop(core.AgentResult{Output: snap.Response.Content}), nil
			}
			return agent.InjectFeedback(`Your output is not valid ProductSpec JSON. Return exactly: {"name": "...", "price": 0.0, "sku": "..."}.`), nil
		}),
	)

	result, err := a.Execute(context.Background(), core.AgentTask{
		Input: "Extract product spec from: 'Acme Widget, $29.99, SKU AW-001'",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(result.Output)
}
```

The loop budget (`WithMaxIter`) caps the retry count. If the LLM never produces a valid response, the loop exhausts iterations and calls a forced synthesis instead of returning an error — so check the output in the caller if you need strict validation.

## Pattern 2 — Mid-run redirection

Detect repeated tool-call patterns (the LLM is stuck in a loop) and inject corrective feedback.

```go
package main

import (
	"context"
	"fmt"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func main() {
	provider := /* ... */

	var lastToolName string
	var sameToolCount int

	a := agent.NewLLMAgent("explorer", "explores a topic", provider,
		agent.WithMaxIter(10),
		agent.WithOnIterationComplete(func(ctx context.Context, iter int, snap *agent.IterationSnapshot) (agent.IterationDecision, error) {
			if len(snap.ToolCalls) == 1 {
				if snap.ToolCalls[0].Name == lastToolName {
					sameToolCount++
					if sameToolCount >= 3 {
						return agent.InjectFeedback(fmt.Sprintf(
							"You've called %s three times in a row. Try a different approach or summarize what you've learned.",
							lastToolName,
						)), nil
					}
				} else {
					lastToolName = snap.ToolCalls[0].Name
					sameToolCount = 1
				}
			}
			return agent.Continue(), nil
		}),
	)

	result, err := a.Execute(context.Background(), core.AgentTask{
		Input: "Research the history of Go generics.",
	})
	// ...
	_ = result
	_ = err
}
```

For concurrent use, lift `lastToolName` and `sameToolCount` into a struct keyed by `task.ThreadID` instead of closure variables.

## Pattern 3 — Per-tenant memory and metadata

Use the same agent across multiple tenants without reconstructing it. `RunOptions.Memory` swaps the memory orchestrator; `RunOptions.Metadata` carries arbitrary per-call context that flows into hooks, traces, and logs.

```go
package main

import (
	"context"

	"github.com/nevindra/oasis"
	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
	"github.com/nevindra/oasis/memory"
)

type requestIDKey struct{}

type Tenant struct {
	ID string
	// ...
}

func (t Tenant) MemoryOrchestrator() *memory.AgentMemory { /* ... */ return nil }
func (t Tenant) LoopBudget() int                         { return 8 }
func (t Tenant) Temperature() float64                    { return 0.7 }

func handleRequest(ctx context.Context, a *agent.LLMAgent, tenant Tenant, task core.AgentTask) (core.AgentResult, error) {
	return a.ExecuteWith(ctx, task, &agent.RunOptions{
		Memory:  tenant.MemoryOrchestrator(),
		MaxIter: oasis.Ptr(tenant.LoopBudget()),
		Generation: &agent.Generation{
			Temperature: oasis.Ptr(tenant.Temperature()),
		},
		Metadata: map[string]any{
			"tenant_id":  tenant.ID,
			"request_id": ctx.Value(requestIDKey{}),
		},
	})
}
```

Concurrent calls with distinct `RunOptions.Memory` values do not interfere — each call has its own memory orchestrator for the duration of the run.

## Pattern 4 — Error recovery with fallback model

Retry transient errors against a cheaper backup model.

```go
package main

import (
	"context"
	"strings"

	"github.com/nevindra/oasis/agent"
	"github.com/nevindra/oasis/core"
)

func main() {
	primaryProvider := /* ... */
	backupProvider := /* cheaper / lower-tier provider */

	a := agent.NewLLMAgent("resilient", "fallback-able", primaryProvider,
		agent.WithMaxIter(3),
		agent.WithPrepareStep(func(ctx context.Context, iter int, ctrl *agent.StepControl) error {
			if iter > 0 {
				ctrl.Model = backupProvider // iteration 1+: fall back to backup model
			}
			return nil
		}),
		agent.WithOnError(func(ctx context.Context, iter int, err error) (agent.ErrorDecision, error) {
			if strings.Contains(err.Error(), "rate limit") {
				return agent.Retry(), nil // PrepareStep on the retry will swap to backup
			}
			return agent.Propagate(), nil
		}),
	)

	result, err := a.Execute(context.Background(), core.AgentTask{
		Input: "Summarize the latest Go release notes.",
	})
	// ...
	_ = result
	_ = err
}
```

`OnError` is not called for `*ErrHalt`, `*ErrSuspended`, or context cancellation — those have their own paths. A non-nil error returned from `OnError` itself fails the run with the hook's error, not the original.

## Composition order

Per iteration, hooks run alongside processors in this sequence:

```
PrepareStep
  → PreProcessor chain
    → LLM call
      ↳ (on error) OnError → Retry / RetryWithFeedback / HaltDecision / Propagate
    → PostProcessor chain
    → Tool dispatch
      ↳ (on error) OnError
    → PostToolProcessor chain
  → OnIterationComplete → Continue / Stop / InjectFeedback / InjectMessages
```

A hook returning a non-nil error fails the entire run with that error.

## See also

- [`docs/concepts/agent.md`](../concepts/agent.md) — agent primitives and execution loop
- [`docs/guides/processors-and-guardrails.md`](processors-and-guardrails.md) — processor pipeline reference
