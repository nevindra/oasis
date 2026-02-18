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
func (a *SummaryAgent) ExecuteStream(ctx context.Context, task oasis.AgentTask, ch chan<- string) (oasis.AgentResult, error) {
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

## See Also

- [Agent Concept](../concepts/agent.md)
- [Background Agents Guide](background-agents.md)
