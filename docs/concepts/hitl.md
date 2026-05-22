# Human-in-the-Loop (HITL)

Oasis HITL has three primitives:

1. **`ask_user`** — the LLM autonomously calls a built-in tool to ask a clarifying question. Configured with `WithInputHandler(h)`. Free-form text in / text out. Best for mid-loop clarifications ("which user did you mean?").
2. **Suspend / resume** — a workflow step or processor pauses execution; the caller of `Execute()` receives `*ErrSuspended`, presents context to a human, and calls `Resume(ctx, data)` to continue. Best for structured human decisions (approvals, form fills, gated actions).
3. **`WithToolApproval(name, opts...)`** — a synchronous gate that requires the configured `InputHandler` to approve or deny a tool call before it runs. Best when the InputHandler can answer quickly (HTTP request/response, CLI prompt).

For typed structured suspend/resume, prefer `SuspendProtocol[Req, Resp]` (see below).

## Typed contracts with `SuspendProtocol`

Declare the contract once:

```go
type TransferRequest struct {
    Amount float64
    To     string
}

type ApproveResponse struct {
    Approved bool
    Reason   string
}

var ApproveTransfer = oasis.NewSuspendProtocol[TransferRequest, ApproveResponse]("billing.approve_transfer").
    WithRenderResume(func(r ApproveResponse) string {
        if r.Approved {
            return "Human approved the transfer. Reason: " + r.Reason
        }
        return "Human declined the transfer. Reason: " + r.Reason
    })
```

Suspend from a workflow step or processor:

```go
func (s *ApprovalStep) Execute(ctx context.Context, wCtx *oasis.WorkflowContext) (any, error) {
    amount, _ := wCtx.Get("amount")
    if amount.(float64) > 1000 {
        return nil, ApproveTransfer.Suspend(TransferRequest{
            Amount: amount.(float64),
            To:     "external_account",
        })
    }
    // ... normal path ...
    return nil, nil
}
```

Resume from the caller:

```go
result, err := ag.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    payload, err := ApproveTransfer.PayloadFrom(suspended)
    if err != nil { /* mismatch or unmarshal */ }

    // ... present payload to the human via your UI of choice ...

    result, err = ApproveTransfer.Resume(suspended, ctx, ApproveResponse{
        Approved: true,
        Reason:   "manager OK'd",
    })
}
```

What the compiler enforces:
- The argument to `ApproveTransfer.Suspend(...)` must be `TransferRequest`.
- The return value of `ApproveTransfer.PayloadFrom(...)` is `TransferRequest`.
- The third argument of `ApproveTransfer.Resume(...)` must be `ApproveResponse`.

If someone tries to resume `ApproveTransfer` using a different protocol value (say `RefundTransfer`), the framework catches it at runtime with a clear error before any state changes.

## When to use untyped `Suspend(json.RawMessage)`

Use it for prototypes, scripts, or dynamic-shape payloads where declaring a protocol would be ceremony. The escape hatch is fully supported; nothing in spec #1 or any future spec deprecates it.

## What `SuspendProtocol` does not cover (yet)

- The synchronous `WithToolApproval` gate is not protocol-aware; a redesigned async approval gate using protocols ships in spec #6.
- `ask_user` deliberately stays free-form; it serves a different purpose.
- Durable cross-process suspend/resume snapshots are not part of typed contracts; the persistence story is its own spec when the deployment scenario demands it.

## Streaming suspends

When a tool dispatch, workflow step, or processor suspends mid-run, Oasis emits a typed mid-stream event so UIs can render a "human, please decide" surface without waiting for the run to finish.

Event ordering on a suspending run:

```
EventRunStart
  EventIterationStart           (iter=0)
    EventTextDelta / EventToolCallStart / ...
    EventToolCallSuspended      ← OR EventStepSuspended OR EventProcessorSuspended
  EventIterationFinish          (iter=0, FinishReason=FinishSuspended)
EventRunFinish                  (FinishReason=FinishSuspended, Protocol=<tag>, SuspendPayload=<bytes>)
[channel close]
```

Three event types, one per source:

- `oasis.EventToolCallSuspended` — a tool's `ExecuteRaw`, a tool middleware, or a `PostToolProcessor` returned a `Suspend`-class error. Carries `ID` (tool call ID), `Name` (tool name), `Args` (tool arguments), `Protocol` (typed-protocol tag, empty for untyped suspend), and `SuspendPayload`.
- `oasis.EventStepSuspended` — a workflow step's `Execute` returned a `Suspend`-class error. Carries `Name` (step name), `Protocol`, and `SuspendPayload`.
- `oasis.EventProcessorSuspended` — a `PreLLM`, `PostLLM`, or `PostToolProcessor` returned a `Suspend`-class error. Carries `Content` set to `"pre"`, `"post"`, or `"post-tool"` as the kind discriminator, plus `Protocol` and `SuspendPayload`.

The final `EventRunFinish` also carries `Protocol` and `SuspendPayload` when `FinishReason == FinishSuspended`, so consumers that only watch the lifecycle envelope still get the data.

Convenience accessors are provided on both the sync and streaming paths:

```go
// Sync:
res, err := agent.Execute(ctx, task)
if res.Suspended() {
    proto := res.SuspendedProtocol() // typed-protocol tag; "" for untyped
    payload := res.SuspendPayload    // raw bytes to render to the human
    // ...
}

// Streaming:
stream := oasis.StartStream(ctx, agent, task)
// ... consume stream.Events() ...
if stream.Suspended() {
    proto := stream.SuspendedProtocol()
    payload := stream.SuspendPayload()
    // ...
}
```

To identify which iteration suspended without external bookkeeping, walk `AgentResult.Iterations` and check `iter.FinishReason`:

```go
for _, iter := range res.Iterations {
    if iter.FinishReason == oasis.FinishSuspended {
        // iter.Iter is the 0-indexed iteration number that paused
    }
}
```
