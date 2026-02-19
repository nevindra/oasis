# Streaming

Stream LLM tokens and execution events as they happen instead of waiting for the full response. Both LLMAgent and Network support streaming via the `StreamingAgent` interface.

## Basic Streaming

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm)

if sa, ok := agent.(oasis.StreamingAgent); ok {
    ch := make(chan oasis.StreamEvent, 64)
    go func() {
        for ev := range ch {
            switch ev.Type {
            case oasis.EventTextDelta:
                fmt.Print(ev.Content)
            case oasis.EventToolCallStart:
                fmt.Printf("\n[calling %s...]\n", ev.Name)
            case oasis.EventToolCallResult:
                fmt.Printf("[%s done]\n", ev.Name)
            case oasis.EventAgentStart:
                fmt.Printf("\n[agent %s working...]\n", ev.Name)
            case oasis.EventAgentFinish:
                fmt.Printf("[agent %s done]\n", ev.Name)
            }
        }
    }()
    result, err := sa.ExecuteStream(ctx, task, ch)
}
```

## Stream Events

The channel carries typed `StreamEvent` values. Five event types:

| Event Type            | Emitted By                     | Payload                                  |
| --------------------- | ------------------------------ | ---------------------------------------- |
| `EventTextDelta`      | Provider (ChatStream)          | `Content` = text chunk                   |
| `EventToolCallStart`  | runLoop (before tool dispatch) | `Name` = tool name, `Args` = arguments   |
| `EventToolCallResult` | runLoop (after tool completes) | `Name` = tool name, `Content` = result   |
| `EventAgentStart`     | Network dispatch               | `Name` = agent name, `Content` = task    |
| `EventAgentFinish`    | Network dispatch               | `Name` = agent name, `Content` = output  |

```go
type StreamEvent struct {
    Type    StreamEventType  `json:"type"`
    Name    string           `json:"name,omitempty"`
    Content string           `json:"content,omitempty"`
    Args    json.RawMessage  `json:"args,omitempty"`
}
```

## How It Works

Tool-calling iterations run in blocking mode (`ChatWithTools`), but emit tool events on the channel. The final text response streams token-by-token via `ChatStream`:

```mermaid
sequenceDiagram
    participant Caller
    participant Agent
    participant LLM

    Caller->>Agent: ExecuteStream(task, ch)

    rect rgb(240, 240, 240)
        Note over Agent,LLM: Blocking tool loop (with events)
        Agent->>LLM: ChatWithTools (blocking)
        LLM-->>Agent: tool calls
        Agent-->>Caller: ch <- ToolCallStart
        Agent->>Agent: execute tools
        Agent-->>Caller: ch <- ToolCallResult
        Agent->>LLM: ChatWithTools (blocking)
        LLM-->>Agent: no tool calls
    end

    rect rgb(230, 245, 255)
        Note over Agent,LLM: Streaming final response
        Agent->>LLM: ChatStream(req, ch)
        loop tokens
            LLM-->>Agent: token
            Agent-->>Caller: ch <- TextDelta
        end
    end

    Agent-->>Caller: AgentResult
```

## Channel Buffering

Use a buffered channel to avoid blocking the LLM stream:

```go
ch := make(chan oasis.StreamEvent, 64)  // buffered — recommended
ch := make(chan oasis.StreamEvent)       // unbuffered — may slow down the LLM
```

The channel is always closed by the agent when streaming completes.

## See Also

- [Agent Concept](../concepts/agent.md) — StreamingAgent interface
