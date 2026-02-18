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

## Tips

- No handler = no-op. Processors that call `InputHandlerFromContext` should skip gracefully.
- Use `context.WithTimeout` for deadlines — the handler blocks until response or cancellation.
- Networks propagate the handler to all subagents automatically.

## See Also

- [InputHandler Concept](../concepts/input-handler.md)
- [Processors and Guardrails](processors-and-guardrails.md) — approval gate example
