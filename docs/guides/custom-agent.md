# Building a Custom Agent

For behavior that doesn't fit LLMAgent or Network, implement the `Agent` interface directly.

## Implement Agent

```go
package myagent

import (
    "context"

    oasis "github.com/nevindra/oasis"
)

type SummaryAgent struct {
    provider oasis.Provider
}

func New(provider oasis.Provider) *SummaryAgent {
    return &SummaryAgent{provider: provider}
}

func (a *SummaryAgent) Name() string        { return "summarizer" }
func (a *SummaryAgent) Description() string { return "Summarizes long text into bullet points" }

func (a *SummaryAgent) Execute(ctx context.Context, task oasis.AgentTask) (oasis.AgentResult, error) {
    resp, err := a.provider.Chat(ctx, oasis.ChatRequest{
        Messages: []oasis.ChatMessage{
            oasis.SystemMessage("Summarize the following into 3-5 bullet points."),
            oasis.UserMessage(task.Input),
        },
    })
    if err != nil {
        return oasis.AgentResult{}, err
    }
    return oasis.AgentResult{Output: resp.Content, Usage: resp.Usage}, nil
}

// compile-time check
var _ oasis.Agent = (*SummaryAgent)(nil)
```

## Using Custom Agents

Custom agents work anywhere an `Agent` is expected:

```go
// As a standalone agent
result, _ := myagent.New(llm).Execute(ctx, task)

// As a subagent in a Network
team := oasis.NewNetwork("team", "Coordinator", router,
    oasis.WithAgents(myagent.New(llm), writer),
)

// As a step in a Workflow
wf, _ := oasis.NewWorkflow("pipeline", "Process and summarize",
    oasis.AgentStep("summarize", myagent.New(llm), oasis.After("fetch")),
)

// In a background goroutine
handle := oasis.Spawn(ctx, myagent.New(llm), task)
```

## Adding StreamingAgent

Optionally implement `StreamingAgent` for token streaming:

```go
func (a *SummaryAgent) ExecuteStream(ctx context.Context, task oasis.AgentTask, ch chan<- oasis.StreamEvent) (oasis.AgentResult, error) {
    resp, err := a.provider.ChatStream(ctx, oasis.ChatRequest{
        Messages: []oasis.ChatMessage{
            oasis.SystemMessage("Summarize into 3-5 bullet points."),
            oasis.UserMessage(task.Input),
        },
    }, ch)
    if err != nil {
        return oasis.AgentResult{}, err
    }
    return oasis.AgentResult{Output: resp.Content, Usage: resp.Usage}, nil
}

var _ oasis.StreamingAgent = (*SummaryAgent)(nil)
```

The channel carries `StreamEvent` values — at minimum, emit `EventTextDelta` for streamed text. See [Streaming](streaming.md) for all event types.

## Accessing Task Context

Use `TaskFromContext` to read task metadata (thread ID, user ID, chat ID) from within your agent:

```go
func (a *SummaryAgent) Execute(ctx context.Context, task oasis.AgentTask) (oasis.AgentResult, error) {
    if t, ok := oasis.TaskFromContext(ctx); ok {
        userID := t.TaskUserID()
        // use for per-user logic, logging, etc.
    }
    // ...
}
```

Task context is automatically propagated when your agent runs inside a Network or Workflow.

## Adding Observability

Custom agents can accept a `Tracer` for distributed tracing:

```go
type SummaryAgent struct {
    provider oasis.Provider
    tracer   oasis.Tracer
}

func (a *SummaryAgent) Execute(ctx context.Context, task oasis.AgentTask) (oasis.AgentResult, error) {
    ctx, span := a.tracer.Start(ctx, "summarizer.execute")
    defer span.End()

    span.SetAttributes(map[string]string{"input_length": fmt.Sprintf("%d", len(task.Input))})

    resp, err := a.provider.Chat(ctx, oasis.ChatRequest{...})
    if err != nil {
        span.RecordError(err)
        return oasis.AgentResult{}, err
    }
    return oasis.AgentResult{Output: resp.Content, Usage: resp.Usage}, nil
}
```

## See Also

- [Agent Concept](../concepts/agent.md)
- [Background Agents Guide](background-agents.md)
- [Streaming Guide](streaming.md) — StreamEvent types and SSE
- [Observability](../concepts/observability.md) — Tracer/Span interfaces
