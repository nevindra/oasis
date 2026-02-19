# Agent

Agent is the central primitive in Oasis — a composable unit of work that takes a task and returns a result. Everything builds on this interface.

## Agent Interface

**File:** `agent.go`

```go
type Agent interface {
    Name() string
    Description() string
    Execute(ctx context.Context, task AgentTask) (AgentResult, error)
}
```

Any struct implementing these three methods is an Agent. Agents compose recursively — Networks contain Agents, Workflows orchestrate Agents, and all implement Agent themselves.

## LLMAgent

**File:** `llmagent.go`

The most common Agent implementation. Runs a tool-calling loop with a single Provider: call LLM, execute tool calls, feed results back, repeat until the LLM produces a final text response.

```go
agent := oasis.NewLLMAgent("researcher", "Searches for information", provider,
    oasis.WithTools(searchTool, knowledgeTool),
    oasis.WithPrompt("You are a research assistant."),
    oasis.WithMaxIter(5),
)

result, err := agent.Execute(ctx, oasis.AgentTask{
    Input: "What are the best practices for Go error handling?",
})
```

### Execution Loop

```mermaid
flowchart TD
    START([Execute]) --> BUILD[Build messages: system prompt + memory + history + user input]
    BUILD --> PRE[PreProcessor hooks]
    PRE -->|ErrHalt| HALT([Return canned response])
    PRE --> LLM{Call LLM}
    LLM --> POST[PostProcessor hooks]
    POST -->|ErrHalt| HALT
    POST --> CHECK{Has tool calls?}
    CHECK -->|No| DONE([Return AgentResult])
    CHECK -->|Yes| DISPATCH[Execute tools in parallel]
    DISPATCH --> POSTTOOL[PostToolProcessor hooks]
    POSTTOOL --> APPEND[Append results to messages]
    APPEND --> ITER{Max iterations?}
    ITER -->|No| PRE
    ITER -->|Yes| SYNTH[Force synthesis: ask LLM to summarize]
    SYNTH --> DONE
```

### Key Behaviors

- **Parallel tool execution** — when the LLM returns multiple tool calls in one response, they run concurrently via goroutines
- **Max iterations** — defaults to 10. When reached, the agent appends a synthesis prompt and makes one final LLM call
- **Streaming** — LLMAgent implements `StreamingAgent`. Emits `StreamEvent` values throughout execution: tool call start/result events during tool iterations, text-delta events during the final response
- **Memory** — stateless by default. Enable with `WithConversationMemory` and `WithUserMemory`

## AgentTask

The input to any Agent:

```go
type AgentTask struct {
    Input       string         // natural language task
    Attachments []Attachment   // optional multimodal content
    Context     map[string]any // optional metadata
}
```

Context carries metadata through the agent hierarchy. Use the typed constants and accessors:

```go
task := oasis.AgentTask{
    Input: "hello",
    Context: map[string]any{
        oasis.ContextThreadID: "thread-123",
        oasis.ContextUserID:   "user-42",
        oasis.ContextChatID:   "chat-99",
    },
}
task.TaskThreadID()  // "thread-123"
task.TaskUserID()    // "user-42"
task.TaskChatID()    // "chat-99"
```

## AgentResult

The output from any Agent:

```go
type AgentResult struct {
    Output      string       // final response text
    Attachments []Attachment // multimodal content from LLM response
    Usage       Usage        // aggregate token usage across all LLM calls
    Steps       []StepTrace  // per-step execution trace, chronological order
}
```

### Execution Traces

`Steps` records every tool call and agent delegation that occurred during execution. Each `StepTrace` includes name, type (`"tool"`, `"agent"`, or `"step"` for Workflows), input, output, token usage, and wall-clock duration:

```go
result, _ := network.Execute(ctx, task)
for _, step := range result.Steps {
    fmt.Printf("%-6s %-20s %5dms  in=%-4d out=%d\n",
        step.Type, step.Name, step.Duration.Milliseconds(),
        step.Usage.InputTokens, step.Usage.OutputTokens)
}
```

`Steps` is nil when no tools were called. See [Observability](observability.md#built-in-execution-traces-no-otel-required) for details.

## AgentOptions

Options shared by `NewLLMAgent` and `NewNetwork`:

| Option | Description |
|--------|-------------|
| `WithTools(tools ...Tool)` | Add tools |
| `WithPrompt(s string)` | Set system prompt |
| `WithMaxIter(n int)` | Max tool-calling iterations (default 10) |
| `WithAgents(agents ...Agent)` | Add subagents (Network only) |
| `WithProcessors(processors ...any)` | Add processor middleware |
| `WithInputHandler(h InputHandler)` | Enable human-in-the-loop |
| `WithPlanExecution()` | Enable batched tool calls via `execute_plan` tool |
| `WithResponseSchema(s *ResponseSchema)` | Enforce structured JSON output |
| `WithConversationMemory(s Store, opts...)` | Enable history load/persist per thread |
| `WithUserMemory(m MemoryStore, e EmbeddingProvider)` | Enable user fact injection + auto-extraction |

## StreamingAgent

Optional capability for agents that support event streaming:

```go
type StreamingAgent interface {
    Agent
    ExecuteStream(ctx context.Context, task AgentTask, ch chan<- StreamEvent) (AgentResult, error)
}
```

Both `LLMAgent` and `Network` implement it. The channel carries typed `StreamEvent` values — text deltas, tool call start/result, and agent start/finish (Network only). Check at runtime via type assertion:

```go
if sa, ok := agent.(oasis.StreamingAgent); ok {
    ch := make(chan oasis.StreamEvent, 64)
    go func() {
        for ev := range ch {
            switch ev.Type {
            case oasis.EventTextDelta:
                fmt.Print(ev.Content)
            case oasis.EventToolCallStart:
                fmt.Printf("[calling %s...]\n", ev.Name)
            case oasis.EventToolCallResult:
                fmt.Printf("[%s done]\n", ev.Name)
            }
        }
    }()
    result, err := sa.ExecuteStream(ctx, task, ch)
}
```

## Background Execution

`Spawn` launches any Agent in a background goroutine:

```go
handle := oasis.Spawn(ctx, agent, task)

handle.State()   // Pending, Running, Completed, Failed, Cancelled
handle.Done()    // channel, closed when done
handle.Await(ctx) // block until done
handle.Cancel()   // request cancellation
```

See [Background Agents Guide](../guides/background-agents.md) for patterns.

## See Also

- [Network](network.md) — multi-agent coordination
- [Workflow](workflow.md) — deterministic DAG orchestration
- [Tool](tool.md) — what agents can do
- [Memory](memory.md) — conversation and user memory
- [Custom Agent Guide](../guides/custom-agent.md)
