# Network API

Import path: `github.com/nevindra/oasis/network`

The `oasis` facade re-exports `network.New` as `oasis.NewNetwork`. All other
types and options live in the `network` subpackage and must be imported directly.

---

## Types

### `Network`

```go
type Network struct { /* unexported */ }
```

A `Network` is an `Agent` that routes tasks to child agents via an LLM router.
It satisfies `core.Agent` and can be used anywhere an `Agent` is expected —
including as a child of another `Network`.

Thread-safe for concurrent `Execute` calls and concurrent `AddAgent`/`RemoveAgent`
after construction.

---

### `Option`

```go
type Option func(*Network)
```

Functional option for `New`. Built-in options: `WithChildren`, `WithAgentOptions`,
`WithSupervisor`, `WithSupervisorFor`, `WithDynamicSpawning`.

---

### `SpawnPolicy`

```go
type SpawnPolicy struct {
    MaxChildren  int
    ChildBuilder func(req SpawnRequest) (core.Agent, error)
}
```

Controls dynamic agent creation when `WithDynamicSpawning` is used.

| Field | Zero value | Meaning |
|-------|-----------|---------|
| `MaxChildren` | `0` | Unbounded — no cap on spawned children. |
| `ChildBuilder` | required | Called for every `spawn_agent` tool call. Panics at construction if nil. |

---

### `SpawnRequest`

```go
type SpawnRequest struct {
    Name        string   `json:"name"`
    Description string   `json:"description"`
    Prompt      string   `json:"prompt"`
    Tools       []string `json:"tools,omitempty"`
}
```

Parsed from the router LLM's `spawn_agent` tool call and passed to
`SpawnPolicy.ChildBuilder`. All string fields come directly from the LLM;
validate them in `ChildBuilder` if your application needs stricter control.

---

### `Topology`

```go
type Topology struct {
    Root  string
    Nodes []Node
    Edges []Edge
}
```

Read-only snapshot of the Network's graph. Returned by `Network.Topology()`.
Subsequent `AddAgent`/`RemoveAgent` calls do not mutate a previously returned
`Topology`.

---

### `Node`

```go
type Node struct {
    Name        string
    Description string
    Kind        NodeKind
    Supervisors []SupervisorSummary
}
```

One child agent in the topology. `Kind` reflects the agent's underlying type
after unwrapping any supervisor layers.

---

### `NodeKind`

```go
type NodeKind string

const (
    KindLLMAgent NodeKind = "llm-agent"
    KindNetwork  NodeKind = "network"
    KindUnknown  NodeKind = "unknown"
)
```

---

### `Edge`

```go
type Edge struct {
    From string
    To   string
}
```

A routing relationship from the Network's router to a child.
Currently every child has exactly one `Edge` from the root.

---

### `SupervisorSummary`

```go
type SupervisorSummary struct {
    Kind   string
    Params map[string]string
}
```

Human-readable label for an applied supervisor policy. `Kind` is one of:
`"restart"`, `"fallback"`, `"quorum"`, `"circuit-breaker"`, `"chain"`, `"custom"`.

---

### `SupervisorPolicy`

```go
type SupervisorPolicy interface {
    Wrap(child core.Agent) core.Agent
}
```

Wraps a child `Agent` with retry, fallback, quorum, or circuit-breaker behavior.
Four built-in implementations ship with the package. Compose via `Chain`.

---

## Constructors

### `New`

```go
func New(name, description string, router core.Provider, opts ...Option) *Network
```

Constructs a Network. The `router` provider drives all routing decisions.
Children and all other configuration flow through `opts`.

**Panics** at construction if two children share the same name. This is
intentional: a duplicate silently overwrites the registered agent while
accumulating a duplicate entry in the router's tool list.

Zero options is valid — a Network with no children starts as a router-only
agent (it will forward tasks through the router LLM and return its response
directly, with no agent delegations).

---

## Methods

### `Execute`

```go
func (n *Network) Execute(ctx context.Context, task agent.AgentTask, opts ...core.RunOption) (agent.AgentResult, error)
```

Runs the routing loop. The router LLM decides which child agents to call and
in what order, then returns a final text response.

`opts` accepts `core.WithStream(ch)` for streaming events (including
`EventAgentStart`/`EventAgentFinish` per child delegation) and
`core.WithDeadline(d)` for a per-call timeout.

Returns an error only on infrastructure failures (context cancellation,
provider errors). Business-level agent failures are reported in
`AgentResult.Steps` via `StepTrace.Output`.

Thread-safe. Multiple goroutines may call `Execute` concurrently.

---

### `AddAgent`

```go
func (n *Network) AddAgent(child core.Agent) error
```

Registers a child agent at runtime. Thread-safe; the router sees the new
`agent_<name>` tool on the very next `Execute` call.

Returns an error if a child with the same name already exists.
The child is wrapped with any configured supervisor policies before storing.

---

### `RemoveAgent`

```go
func (n *Network) RemoveAgent(name string) error
```

Removes the child with the given name. Thread-safe.
Returns an error if no such child exists.
In-flight calls to the removed child are not interrupted.

---

### `Topology`

```go
func (n *Network) Topology() Topology
```

Returns a read-only snapshot of the Network's current graph: root name,
nodes (one per child with `Kind` and applied supervisor labels), and edges.
Safe to call concurrently with `Execute`, `AddAgent`, and `RemoveAgent`.

---

## Options

### `WithChildren`

```go
func WithChildren(children ...core.Agent) Option
```

Registers child agents. May be called multiple times; children accumulate.
Each child is wrapped with any configured supervisor policies.

**Default:** no children.

---

### `WithAgentOptions`

```go
func WithAgentOptions(opts ...agent.AgentOption) Option
```

Applies agent-level options (prompt, tools, memory, sandbox, etc.) to the
network's internal routing agent.

```go
net := network.New("team", "...", routerP,
    network.WithAgentOptions(agent.WithTracer(t), agent.WithMemory(mem)),
)
```

**Default:** router runs with default `agent.Config` (no memory, no tracer).

---

### `WithSupervisor`

```go
func WithSupervisor(p SupervisorPolicy) Option
```

Applies a `SupervisorPolicy` to every child agent at construction time.
Per-child policies registered with `WithSupervisorFor` compose on top.

**Default:** no network-wide supervisor.

---

### `WithSupervisorFor`

```go
func WithSupervisorFor(name string, p SupervisorPolicy) Option
```

Applies a `SupervisorPolicy` to a specific child by name.
Multiple calls for the same name compose via `Chain` in registration order.

**Default:** no per-child supervisor.

---

### `WithDynamicSpawning`

```go
func WithDynamicSpawning(policy SpawnPolicy) Option
```

Enables the `spawn_agent` tool on the router LLM. When the router calls
`spawn_agent`, the framework parses the request, calls
`SpawnPolicy.ChildBuilder`, and registers the new agent via `AddAgent`.

**Panics** at construction if `policy.ChildBuilder` is nil.

**Default:** dynamic spawning disabled; no `spawn_agent` tool in the router.

---

## Supervisor Policies

### `RestartOnFail`

```go
func RestartOnFail(maxRestarts int, delay ...time.Duration) SupervisorPolicy
```

Retries the child up to `maxRestarts` times before propagating the failure.
`maxRestarts = 0` means no retries (one attempt total). The optional `delay`
sets a fixed pause between restart attempts; if omitted the child is restarted
immediately.

```go
// restart up to 3 times, waiting 2 seconds between each attempt
policy := network.RestartOnFail(3, 2*time.Second)
```

---

### `Fallback`

```go
func Fallback(backup core.Agent) SupervisorPolicy
```

Tries the child first; on any error, runs `backup` and returns its result.

---

### `Quorum`

```go
func Quorum(askN, takeMajorityOfN int, members ...core.Agent) SupervisorPolicy
```

Runs `askN` agents in parallel and returns the output that at least
`takeMajorityOfN` of them agree on (byte-equality on `Output`). Returns early
as soon as a threshold is reached; remaining in-flight calls are cancelled.

**Panics** at construction if `len(members) != askN`.

---

### `CircuitBreaker`

```go
func CircuitBreaker(threshold int, cooldown time.Duration) SupervisorPolicy
```

Opens the circuit after `threshold` consecutive failures. While open, calls
return `ErrCircuitOpen` without invoking the child. The circuit closes after
`cooldown` elapses; exactly one "probe" call is allowed through to test
recovery. All others see `ErrCircuitOpen` until the probe succeeds.

**Panics** at construction if `threshold <= 0`.

---

### `Chain`

```go
func Chain(policies ...SupervisorPolicy) SupervisorPolicy
```

Composes multiple policies into one. Earlier policies wrap closer to the
child; later policies wrap further out.

```go
// Retry first; if all retries fail, switch to fallback.
Chain(RestartOnFail(3), Fallback(backupAgent))
```

---

## Errors

| Error | Source | How to handle |
|-------|--------|---------------|
| `ErrCircuitOpen` | `CircuitBreaker`-wrapped child | Circuit is open; back off and retry after cooldown. |
| `"network: duplicate child agent name <n>"` | `New` (panic) | Rename one of the children before constructing the Network. |
| `"network: agent <n> already exists"` | `AddAgent` | Check membership before calling `AddAgent`. |
| `"network: agent <n> not found"` | `RemoveAgent` | Check membership before calling `RemoveAgent`. |
| `context.Canceled` / `context.DeadlineExceeded` | `Execute` | Propagated from the router or a child agent call. |
