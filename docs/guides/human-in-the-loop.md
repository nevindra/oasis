# Human-in-the-Loop

This guide shows how to implement InputHandler for different platforms and use both LLM-driven and programmatic interaction patterns.

## Implementing InputHandler

### CLI Handler

```go
type CLIHandler struct{}

func (h *CLIHandler) RequestInput(ctx context.Context, req oasis.InputRequest) (oasis.InputResponse, error) {
    fmt.Printf("\n[%s asks]: %s\n", req.Metadata["agent"], req.Question)
    if len(req.Options) > 0 {
        for i, opt := range req.Options {
            fmt.Printf("  %d. %s\n", i+1, opt)
        }
    }
    fmt.Print("> ")

    scanner := bufio.NewScanner(os.Stdin)
    scanner.Scan()
    return oasis.InputResponse{Value: scanner.Text()}, nil
}
```

### Channel-based Handler (for frontends)

```go
type ChannelHandler struct {
    questions chan oasis.InputRequest
    answers   chan string
}

func NewChannelHandler() *ChannelHandler {
    return &ChannelHandler{
        questions: make(chan oasis.InputRequest),
        answers:   make(chan string),
    }
}

func (h *ChannelHandler) RequestInput(ctx context.Context, req oasis.InputRequest) (oasis.InputResponse, error) {
    h.questions <- req
    select {
    case answer := <-h.answers:
        return oasis.InputResponse{Value: answer}, nil
    case <-ctx.Done():
        return oasis.InputResponse{}, ctx.Err()
    }
}
```

## LLM-Driven: ask_user

The LLM decides when to ask. Just set `WithInputHandler`:

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithTools(shellTool, fileTool),
    oasis.WithInputHandler(&CLIHandler{}),
)

// During execution, the LLM might call ask_user:
//   [assistant asks]: Delete all files in /tmp? This cannot be undone.
//   1. Yes
//   2. No
//   > No
//   "OK, I won't delete anything."
```

When `ask_user` is called within `execute_plan` steps, requests are serialized even though tool execution runs in parallel. This prevents concurrent `InputHandler` calls, which are typically not designed for concurrent use.

## Programmatic: Workflow Gate

Use a Step to gate between agent steps:

```go
pipeline, _ := oasis.NewWorkflow("pipeline", "Research with approval",
    oasis.AgentStep("research", researcher),
    oasis.Step("approve", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        handler, ok := oasis.InputHandlerFromContext(ctx)
        if !ok {
            return fmt.Errorf("no input handler")
        }
        output, _ := wCtx.Get("research.output")
        res, err := handler.RequestInput(ctx, oasis.InputRequest{
            Question: fmt.Sprintf("Research found:\n%v\n\nProceed to writing?", output),
            Options:  []string{"Yes", "No"},
        })
        if err != nil {
            return err
        }
        if res.Value != "Yes" {
            return fmt.Errorf("rejected by human")
        }
        return nil
    }, oasis.After("research")),
    oasis.AgentStep("write", writer, oasis.After("approve")),
)
```

## Suspend/Resume (Asynchronous Gate)

For cases where you can't block the thread (webhooks, async UIs), use `Suspend` to pause and `Resume` later:

```go
pipeline, _ := oasis.NewWorkflow("pipeline", "Research with async approval",
    oasis.AgentStep("research", researcher),
    oasis.Step("approve", func(ctx context.Context, wCtx *oasis.WorkflowContext) error {
        if data, ok := oasis.ResumeData(wCtx); ok {
            var d struct{ Approved bool `json:"approved"` }
            json.Unmarshal(data, &d)
            if !d.Approved {
                return fmt.Errorf("rejected by human")
            }
            return nil
        }
        output, _ := wCtx.Get("research.output")
        return oasis.Suspend(json.RawMessage(fmt.Sprintf(
            `{"question": "Approve research?", "preview": %q}`, output,
        )))
    }, oasis.After("research")),
    oasis.AgentStep("write", writer, oasis.After("approve")),
)

// Execute — suspends at "approve".
_, err := pipeline.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // Store suspended somewhere (e.g. send Payload to a webhook).
    // Later, when human responds:
    result, err := suspended.Resume(ctx, json.RawMessage(`{"approved": true}`))
}
```

Processors in LLMAgent/Network can also call `Suspend()` — the agent returns `*ErrSuspended` with the same resume pattern.

### Cleanup on Timeout

`ErrSuspended` captures the full conversation history in a closure. If the human never responds, the snapshot must be released to avoid retaining stale state. The recommended approach is `WithSuspendTTL`, which auto-releases after a deadline:

```go
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    suspended.WithSuspendTTL(5 * time.Minute) // auto-release if not resumed
    store[suspended.Step] = suspended          // store for later resume
}
```

For manual control (e.g. when you own the select loop), use `Release()` directly:

```go
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    select {
    case resp := <-humanInput:
        result, err = suspended.Resume(ctx, resp)
    case <-time.After(5 * time.Minute):
        suspended.Release() // free captured conversation snapshot
        log.Println("suspend timed out, snapshot released")
    }
}
```

`Resume` is single-use — the closure is freed after the call. `Release` and `WithSuspendTTL` are safe to call multiple times. All methods are goroutine-safe when a TTL is active.

## Tips

- No handler = no-op. Processors that call `InputHandlerFromContext` should skip gracefully.
- Use `context.WithTimeout` for deadlines — the handler blocks until response or cancellation.
- Networks propagate the handler to all subagents automatically.
- Use `WithSuspendTTL` on all `ErrSuspended` values in server environments to prevent memory leaks from abandoned suspensions.

## See Also

- [InputHandler Concept](../concepts/input-handler.md)
- [Processors and Guardrails](processors-and-guardrails.md) — approval gate example
