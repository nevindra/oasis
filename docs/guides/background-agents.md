# Background Agents

`Spawn` launches any Agent in a background goroutine and returns an `AgentHandle` for tracking, awaiting, and cancelling.

## Basic Usage

```go
handle := oasis.Spawn(ctx, agent, task)

// Wait for completion
result, err := handle.Await(ctx)
fmt.Println(result.Output)
```

## AgentHandle API

```go
handle.ID() string                                     // unique execution ID
handle.Agent() Agent                                   // the agent being executed
handle.State() AgentState                              // Pending, Running, Completed, Failed, Cancelled
handle.Done() <-chan struct{}                           // closed when done
handle.Await(ctx) (AgentResult, error)                 // block until done
handle.Result() (AgentResult, error)                   // non-blocking check
handle.Cancel()                                        // request cancellation
```

## State Machine

```mermaid
statediagram-v2
    [*] --> Pending: Spawn()
    Pending --> Running: goroutine starts
    Running --> Completed: Execute returns nil
    Running --> Failed: Execute returns error
    Running --> Cancelled: ctx cancelled
```

`IsTerminal()` returns true for Completed, Failed, and Cancelled.

## Patterns

### Fire and Check Later

```go
handle := oasis.Spawn(ctx, backgroundTask, task)

// ... do other work ...

if handle.State() == oasis.StateCompleted {
    result, _ := handle.Result()
    fmt.Println(result.Output)
}
```

### Race Two Agents

Run two agents and take the first result:

```go
h1 := oasis.Spawn(ctx, fastAgent, task)
h2 := oasis.Spawn(ctx, thoroughAgent, task)

select {
case <-h1.Done():
    h2.Cancel()
    result, _ = h1.Result()
case <-h2.Done():
    h1.Cancel()
    result, _ = h2.Result()
}
```

### Parallel Fan-out

```go
var handles []*oasis.AgentHandle
for _, item := range items {
    h := oasis.Spawn(ctx, processor, oasis.AgentTask{Input: item})
    handles = append(handles, h)
}

// Collect results
for _, h := range handles {
    result, err := h.Await(ctx)
    // ...
}
```

## Panic Safety

`Spawn` recovers from panics in the agent's `Execute` method. If the agent panics, the handle transitions to `StateFailed` with an error message containing the panic value, and the `Done` channel is closed normally. Callers of `Await` and `Result` will see the error without deadlocking.

## Cancellation

Cancel propagates via context. The agent receives a cancelled context and should return promptly:

```go
handle := oasis.Spawn(ctx, agent, task)
handle.Cancel()  // non-blocking

// Wait for the agent to actually stop
<-handle.Done()
fmt.Println(handle.State())  // Cancelled
```

The parent `ctx` also controls lifetime — cancelling it cancels the agent.

For Network streaming, internal event-forwarding goroutines drain with a 60-second timeout after cancellation. If a subagent ignores cancellation, the drain goroutine exits cleanly after the timeout.

## See Also

- [Agent Concept](../concepts/agent.md)
