# Streaming

Stream LLM tokens and execution events as they happen instead of waiting for the full response. `LLMAgent`, `Network`, and `Workflow` all support streaming via the `StreamingAgent` interface.

## Basic Streaming

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm)

if sa, ok := agent.(oasis.StreamingAgent); ok {
    ch := make(chan oasis.StreamEvent, 64)
    go func() {
        for ev := range ch {
            switch ev.Type {
            case oasis.EventInputReceived:
                fmt.Printf("[%s received: %s]\n", ev.Name, ev.Content)
            case oasis.EventProcessingStart:
                fmt.Printf("[%s processing...]\n", ev.Name)
            case oasis.EventThinking:
                fmt.Printf("[thinking: %s]\n", ev.Content)
            case oasis.EventTextDelta:
                fmt.Print(ev.Content)
            case oasis.EventToolCallDelta:
                fmt.Printf("[tool %s building args: %s]\n", ev.ID, ev.Content)
            case oasis.EventToolCallStart:
                fmt.Printf("\n[calling %s (id=%s)...]\n", ev.Name, ev.ID)
            case oasis.EventToolProgress:
                fmt.Printf("[%s progress: %s]\n", ev.Name, ev.Content)
            case oasis.EventToolCallResult:
                fmt.Printf("[%s done]\n", ev.Name)
            case oasis.EventRoutingDecision:
                fmt.Printf("[routing: %s]\n", ev.Content)
            case oasis.EventAgentStart:
                fmt.Printf("\n[agent %s working...]\n", ev.Name)
            case oasis.EventAgentFinish:
                fmt.Printf("[agent %s done]\n", ev.Name)
            case oasis.EventStepStart:
                fmt.Printf("[step %s started]\n", ev.Name)
            case oasis.EventStepProgress:
                fmt.Printf("[step %s: %s]\n", ev.Name, ev.Content)
            case oasis.EventStepFinish:
                fmt.Printf("[step %s finished]\n", ev.Name)
            }
        }
    }()
    result, err := sa.ExecuteStream(ctx, task, ch)
}
```

## Stream Events

The channel carries typed `StreamEvent` values. Fourteen event types cover agents, tools, workflows, and networks:

| Event Type | Emitted By | Payload |
|---|---|---|
| `EventInputReceived` | LLMAgent/Network entry | `Name` = agent name, `Content` = task input |
| `EventProcessingStart` | runLoop (after context loading) | `Name` = loop identifier (e.g. `agent:name`) |
| `EventTextDelta` | Provider (ChatStream) | `Content` = text chunk |
| `EventThinking` | runLoop (after LLM call) | `Content` = reasoning/chain-of-thought text |
| `EventToolCallDelta` | Provider (ChatStream) | `ID` = tool call ID, `Content` = argument chunk |
| `EventToolCallStart` | runLoop (before tool dispatch) | `ID` = tool call ID, `Name` = tool name, `Args` = arguments |
| `EventToolProgress` | StreamingTool (during execution) | `Name` = tool name, `Content` = progress JSON |
| `EventToolCallResult` | runLoop (after tool completes) | `ID` = tool call ID, `Name` = tool name, `Content` = result, `Usage`, `Duration` |
| `EventRoutingDecision` | Network (after router response) | `Name` = network name, `Content` = JSON (`{"agents":[...],"tools":[...]}`) |
| `EventAgentStart` | Network dispatch | `Name` = agent name, `Content` = task |
| `EventAgentFinish` | Network dispatch | `Name` = agent name, `Content` = output, `Usage`, `Duration` |
| `EventStepStart` | Workflow (before step execution) | `Name` = step name |
| `EventStepProgress` | Workflow ForEach (after iteration) | `Name` = step name, `Content` = JSON (`{"completed":3,"total":10}`) |
| `EventStepFinish` | Workflow (after step completes) | `Name` = step name, `Content` = output or error, `Duration` |

```go
type StreamEvent struct {
    Type     StreamEventType  `json:"type"`
    ID       string           `json:"id,omitempty"`       // tool call correlation
    Name     string           `json:"name,omitempty"`
    Content  string           `json:"content,omitempty"`
    Args     json.RawMessage  `json:"args,omitempty"`
    Usage    Usage            `json:"usage,omitempty"`    // tool-call-result, agent-finish
    Duration time.Duration    `json:"duration,omitempty"` // tool-call-result, agent-finish, step-finish
}
```

### Tool Call Correlation

Tool call events share the same `ID` for correlation:

1. `EventToolCallDelta` — incremental argument chunks from the LLM (ID = tool call ID)
2. `EventToolCallStart` — tool dispatch begins (ID = tool call ID)
3. `EventToolCallResult` — tool returns (ID = tool call ID)

This lets consumers track individual tool calls through their lifecycle, even when multiple calls overlap in a streaming response.

## How It Works

The tool-calling loop uses `ChatStream` for all LLM calls (when streaming), emitting `EventToolCallDelta` as arguments arrive. Tool dispatch emits start/progress/result events. The final text response streams token-by-token:

```mermaid
sequenceDiagram
    participant Caller
    participant Agent
    participant LLM

    Caller->>Agent: ExecuteStream(task, ch)
    Agent-->>Caller: ch <- InputReceived
    Agent->>Agent: build messages (memory, context)
    Agent-->>Caller: ch <- ProcessingStart

    rect rgb(240, 240, 240)
        Note over Agent,LLM: Streaming tool loop
        Agent->>LLM: ChatStream(req, ch) [req.Tools set]
        loop argument chunks
            LLM-->>Agent: tool_call delta
            Agent-->>Caller: ch <- ToolCallDelta
        end
        LLM-->>Agent: ChatResponse{ToolCalls}
        Agent-->>Caller: ch <- ToolCallStart
        Agent->>Agent: execute tools
        Agent-->>Caller: ch <- ToolCallResult
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

## Workflow Streaming

Workflows implement `StreamingAgent`. Use `ExecuteStream` to receive step-level events as the DAG executes:

```go
wf, _ := oasis.NewWorkflow("pipeline", "data pipeline",
    oasis.WithStep("extract", extractFn),
    oasis.WithStep("transform", transformFn, oasis.DependsOn("extract")),
    oasis.WithStep("load", loadFn, oasis.DependsOn("transform")),
)

ch := make(chan oasis.StreamEvent, 64)
go func() {
    for ev := range ch {
        switch ev.Type {
        case oasis.EventStepStart:
            fmt.Printf("[%s started]\n", ev.Name)
        case oasis.EventStepFinish:
            fmt.Printf("[%s finished in %v]\n", ev.Name, ev.Duration)
        }
    }
}()
result, err := wf.ExecuteStream(ctx, task, ch)
```

### ForEach Progress

ForEach steps emit `EventStepProgress` after each completed iteration:

```go
oasis.WithForEach("process-items", processFn,
    oasis.ForEachItems("items"),
    oasis.ForEachConcurrency(4),
)
// Emits: {"completed":1,"total":10}, {"completed":2,"total":10}, ...
```

## StreamingTool

Tools can implement the optional `StreamingTool` interface to emit progress events during execution:

```go
type MyTool struct{}

func (t *MyTool) Definitions() []oasis.ToolDefinition {
    return []oasis.ToolDefinition{{Name: "my_tool", Description: "Long-running tool"}}
}

func (t *MyTool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    // Non-streaming fallback
    return oasis.ToolResult{Content: "done"}, nil
}

func (t *MyTool) ExecuteStream(ctx context.Context, name string, args json.RawMessage, ch chan<- oasis.StreamEvent) (oasis.ToolResult, error) {
    for i := 0; i < 10; i++ {
        // Do work...
        ch <- oasis.StreamEvent{
            Type:    oasis.EventToolProgress,
            Name:    name,
            Content: fmt.Sprintf(`{"step":%d,"total":10}`, i+1),
        }
    }
    return oasis.ToolResult{Content: "done"}, nil
}
```

When the agent streams, `ExecuteStream` is called automatically. When the agent uses non-streaming `Execute`, the regular `Execute` method is called — no special handling needed.

## Stream Resume

When a suspended agent or workflow is resumed, use `ResumeStream` for streaming output:

```go
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // Later, resume with streaming
    ch := make(chan oasis.StreamEvent, 64)
    go func() {
        for ev := range ch {
            fmt.Printf("[%s] %s\n", ev.Type, ev.Content)
        }
    }()
    result, err := suspended.ResumeStream(ctx, approvalData, ch)
}
```

`ResumeStream` works for both agent-level and workflow-level suspensions. The channel is closed when streaming completes. If the suspension was created in a non-streaming context (no `resumeStream` closure), `ResumeStream` returns an error and closes `ch`.

## Channel Buffering

Use a buffered channel to avoid blocking the LLM stream:

```go
ch := make(chan oasis.StreamEvent, 64)  // buffered — recommended
ch := make(chan oasis.StreamEvent)       // unbuffered — may slow down the LLM
```

The channel is always closed by the agent when streaming completes.

## HTTP Server-Sent Events

Use `ServeSSE` to stream agent responses over HTTP with zero boilerplate:

```go
http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
    task := oasis.AgentTask{Input: r.URL.Query().Get("q")}
    result, err := oasis.ServeSSE(r.Context(), w, agent, task)
    if err != nil {
        log.Printf("stream error: %v", err)
        return
    }
    log.Printf("completed: %s", result.Output)
})
```

`ServeSSE` handles the full SSE lifecycle:

1. Validates that the `ResponseWriter` supports flushing
2. Sets `Content-Type: text/event-stream`, `Cache-Control`, and `Connection` headers
3. Runs the agent in a goroutine, writes each `StreamEvent` as an SSE event
4. Sends `event: done` with the full `AgentResult` (output, steps, usage) on completion, or `event: error` on failure
5. Propagates client disconnection via context cancellation

Each SSE event is formatted as:

```text
event: text-delta
data: {"type":"text-delta","content":"Hello"}
```

Works with any router (Echo, Chi, Gin) since they all expose `http.ResponseWriter`.

> **Note:** `ServeSSE` handles real-time streaming only. If `WithConversationMemory` is configured on the agent, messages and execution traces are persisted to the database automatically by the memory pipeline — you don't need to store them separately in your handler.

## Custom SSE Loops

For full control over the SSE lifecycle (custom done payloads, app-specific metadata, filtering events), use `WriteSSEEvent` with `ExecuteStream` directly:

```go
http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    task := oasis.AgentTask{Input: r.URL.Query().Get("q")}
    ch := make(chan oasis.StreamEvent, 64)

    var result oasis.AgentResult
    var execErr error
    done := make(chan struct{})
    go func() {
        result, execErr = agent.(oasis.StreamingAgent).ExecuteStream(r.Context(), task, ch)
        close(done)
    }()

    for ev := range ch {
        oasis.WriteSSEEvent(w, string(ev.Type), ev)
    }
    <-done

    if execErr != nil {
        oasis.WriteSSEEvent(w, "error", map[string]string{"error": execErr.Error()})
        return
    }

    // Custom done payload with app-specific metadata
    oasis.WriteSSEEvent(w, "done", map[string]any{
        "conversationId": convID,
        "result":         result,
        "sources":        sources,
    })
})
```

`WriteSSEEvent` handles JSON marshaling and flushing — you compose the loop, it handles the SSE mechanics.

## Processors and Streaming

PostProcessors run for side effects even on the streaming path. When an agent streams its final response, the framework still calls `RunPostLLM` after streaming completes — the PostProcessor sees the full assembled response.

This means logging, analytics, and guardrail processors work identically regardless of whether the caller used `Execute` or `ExecuteStream`.

## Execution Trace Persistence

When `WithConversationMemory` is enabled, execution traces (`result.Steps`) are **automatically saved** to the database in the assistant message's `Metadata` field — no extra code needed. This happens in the background after `ExecuteStream` completes, the same as with `Execute`.

This means the SSE stream is for real-time display, but you don't need to persist steps yourself. They're already in the database:

```go
// Steps are automatically persisted — just query them back
messages, _ := store.GetMessages(ctx, threadID, 10)
for _, m := range messages {
    if steps, ok := m.Metadata["steps"]; ok {
        // execution traces from the agent run
    }
}
```

See [Memory: Execution Trace Persistence](../concepts/memory.md#execution-trace-persistence) for full details.

## Observability

Streaming works with `WithTracer` — the same span hierarchy (`agent.execute` → `agent.llm.call` → `agent.tool.call`) applies to `ExecuteStream`. Tool events appear as child spans within the streaming execution.

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTracer(tracer),
)
// ExecuteStream produces the same trace spans as Execute
```

## See Also

- [Agent Concept](../concepts/agent.md) — StreamingAgent interface
- [Workflow Concept](../concepts/workflow.md) — Workflow streaming
- [Processors Guide](processors-and-guardrails.md) — PostProcessor details
- [Observability](../concepts/observability.md) — Tracer/Span interfaces
- [Human-in-the-Loop](human-in-the-loop.md) — Suspend/resume with streaming
