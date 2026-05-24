# Network Examples

---

## Recipe 1: Basic two-agent coordinator

**Goal:** Route a research task to a search agent and a summarize agent, letting the LLM decide the call order.

```go
package main

import (
    "context"
    "fmt"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/network"
)

func main() {
    var routerProvider core.Provider // e.g. gemini.NewProvider(...)

    searchAgent := agent.New(
        "search",
        "Searches the web for current facts on a topic",
        routerProvider,
    )
    summarizeAgent := agent.New(
        "summarize",
        "Condenses a body of text into a concise summary",
        routerProvider,
    )

    net := network.New(
        "coordinator",
        "Coordinates web research and summarization",
        routerProvider,
        network.WithChildren(searchAgent, summarizeAgent),
    )

    result, err := net.Execute(context.Background(), core.AgentTask{
        Input: "Find and summarize recent breakthroughs in fusion energy",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
}
```

**Plain-English walkthrough:**
- `routerProvider` is the LLM that makes routing decisions. It can be the same model used by the children or a different one (e.g. a faster model for routing, a stronger one for specialised work).
- `WithChildren` registers both agents. The framework creates tool definitions named `agent_search` and `agent_summarize`; the router calls them like any other tool.
- After the routing loop finishes, `result.Output` contains the router's final synthesized answer.

**Variations:**
- Add more specialist agents by passing additional `core.Agent` values to `WithChildren`.
- Give the router its own system prompt via `network.WithRouter(agent.WithPrompt("..."))`.
- Scope the task to a user session: `core.AgentTask{Input: "...", UserID: "user-42", ThreadID: "thread-7"}`.

---

## Recipe 2: Nested networks (network of networks)

**Goal:** Build an outer coordinator that delegates to an inner research sub-network, while also having a writer agent at the outer level.

```go
var p core.Provider // shared provider for this example

// Inner network: a research sub-team
researcher1 := agent.New("paper-search",  "Finds academic papers",   p)
researcher2 := agent.New("web-search",    "Finds web articles",       p)

innerNet := network.New(
    "research",
    "Coordinates academic and web research",
    p,
    network.WithChildren(researcher1, researcher2),
)

// Outer network: the inner network is just another child agent
writer := agent.New("writer", "Writes a polished report from research findings", p)

outerNet := network.New(
    "paper-team",
    "Produces a research report: research then write",
    p,
    network.WithChildren(innerNet, writer),
)

result, err := outerNet.Execute(context.Background(), core.AgentTask{
    Input: "Write a report on recent fusion energy breakthroughs",
})
```

**Plain-English walkthrough:**
- `innerNet` satisfies `core.Agent`, so it can be passed to `WithChildren` like any leaf agent. The outer router sees an `agent_research` tool; when it calls it, the inner network runs its own routing loop transparently.
- The outer network has no idea `agent_research` is itself a network — it just gets a result back. This is composability without coupling.
- `Topology()` on the outer network will classify `innerNet` as `KindNetwork` and `writer` as `KindLLMAgent`.

**Variations:**
- Add a supervisor to the inner network before nesting: `network.WithSupervisor(network.RestartOnFail(2))`.
- Check `outerNet.Topology()` at startup to log or visualize the graph.

---

## Recipe 3: Fault tolerance with supervisor policies

**Goal:** Wrap a fragile external-API agent with automatic retries and a circuit breaker.

```go
fragileAgent := agent.New("weather-api", "Fetches live weather data", p)
backupAgent  := agent.New("cached-weather", "Returns cached weather data", p)

net := network.New(
    "weather-coordinator",
    "Handles weather queries with fault tolerance",
    p,
    network.WithChildren(fragileAgent),
    // Network-wide retry: try up to 2 more times on failure.
    network.WithSupervisor(network.RestartOnFail(2)),
    // Per-child circuit breaker: open after 5 consecutive failures,
    // try again after 30 seconds.
    network.WithSupervisorFor("weather-api",
        network.CircuitBreaker(5, 30*time.Second),
    ),
    // Per-child fallback: if the circuit is open or all retries fail,
    // use the cached agent.
    network.WithSupervisorFor("weather-api",
        network.Fallback(backupAgent),
    ),
)
```

**Plain-English walkthrough:**
- `WithSupervisor` applies to every child. If the network had more agents, they'd all get `RestartOnFail(2)`.
- `WithSupervisorFor` stacks additional policies on top of the network-wide one for `"weather-api"` only. Multiple `WithSupervisorFor` calls for the same name compose via `Chain` in registration order.
- The execution order for `weather-api` is: `Fallback` outermost → `CircuitBreaker` → `RestartOnFail` → `fragileAgent`. If the circuit is open, `Fallback` catches `ErrCircuitOpen` and calls `backupAgent`.

**Variations:**
- Use `network.Chain(network.RestartOnFail(3), network.Fallback(backup))` explicitly for clearer composition.
- Check for `network.ErrCircuitOpen` in your calling code if you want to surface that state to users.

---

## Recipe 4: Dynamic spawning — the router creates agents at runtime

**Goal:** Let the router LLM spawn new specialist agents on demand when it determines a task needs a skill not yet registered.

```go
net := network.New(
    "adaptive-team",
    "A team that can create new specialists as needed",
    p,
    network.WithDynamicSpawning(network.SpawnPolicy{
        MaxChildren: 10,
        ChildBuilder: func(req network.SpawnRequest) (core.Agent, error) {
            // Build a new agent from the router's specification.
            // Validate req.Name if your app has naming restrictions.
            return agent.New(
                req.Name,
                req.Description,
                p,
                agent.WithPrompt(req.Prompt),
            ), nil
        },
    }),
)

result, err := net.Execute(context.Background(), core.AgentTask{
    Input: "Research quantum computing and produce a beginner's guide",
})
```

**Plain-English walkthrough:**
- The router LLM gets a `spawn_agent` tool in its tool list. When it decides a new specialist is needed, it calls `spawn_agent` with a name, description, and system prompt.
- The framework calls `ChildBuilder` with those values, registers the returned agent via `AddAgent`, and tells the router the new `agent_<name>` tool is ready. The router can then call it immediately.
- `MaxChildren: 10` caps the total number of dynamically spawned agents across the network's lifetime to prevent unbounded growth. Set to `0` for no cap.

**Variations:**
- Use `req.Tools` to selectively equip spawned agents with inherited tools from a registry.
- Log or persist each `SpawnRequest` in `ChildBuilder` for auditing which agents the router creates.
- Combine with `WithSupervisor` to give all spawned agents automatic retry policies.

---

## Recipe 5: Streaming events from the routing loop

**Goal:** Receive real-time notifications as the router delegates to each child agent.

```go
ch := make(chan core.StreamEvent, 32)

go func() {
    for evt := range ch {
        switch evt.Type {
        case core.EventAgentStart:
            fmt.Printf("→ delegating to %s: %s\n", evt.Name, evt.Content)
        case core.EventAgentFinish:
            fmt.Printf("← %s finished in %s\n", evt.Name, evt.Duration)
        case core.EventTextDelta:
            fmt.Print(evt.Content) // router's streaming tokens
        }
    }
}()

result, err := net.Execute(
    context.Background(),
    core.AgentTask{Input: "..."},
    core.WithStream(ch),
)
```

**Plain-English walkthrough:**
- `core.WithStream(ch)` passes the channel into `Execute`. The framework emits `EventAgentStart` when delegation begins and `EventAgentFinish` when the child returns (with `evt.Duration` and `evt.Usage`).
- `EventTextDelta` carries the router's own streaming tokens between and after agent calls. Child agents do not stream through the parent channel unless they are also run with streaming.
- The channel must be read concurrently; if the consumer is slow, use a larger buffer or a non-blocking read.

**Variations:**
- Filter to `EventAgentStart`/`EventAgentFinish` only for a delegation audit log.
- Use `evt.Usage` on `EventAgentFinish` to track per-agent token costs in real time.
