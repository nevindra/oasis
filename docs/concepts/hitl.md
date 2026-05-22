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
