# Processors and Guardrails

Processors hook into the agent execution pipeline to transform, validate, or control messages. This guide shows practical examples.

## Guardrail (PreProcessor)

Block prompt injection attempts:

```go
type Guardrail struct{}

func (g *Guardrail) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
    last := req.Messages[len(req.Messages)-1]
    if strings.Contains(strings.ToLower(last.Content), "ignore all previous instructions") {
        return &oasis.ErrHalt{Response: "I can't process that request."}
    }
    return nil
}
```

## PII Redactor (all 3 phases)

Redact sensitive data at every stage:

```go
type PIIRedactor struct{}

func (r *PIIRedactor) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
    for i := range req.Messages {
        req.Messages[i].Content = redactPII(req.Messages[i].Content)
    }
    return nil
}

func (r *PIIRedactor) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    resp.Content = redactPII(resp.Content)
    return nil
}

func (r *PIIRedactor) PostTool(_ context.Context, _ oasis.ToolCall, result *oasis.ToolResult) error {
    result.Content = redactPII(result.Content)
    return nil
}
```

## Tool Filter (PostProcessor)

Block specific tool calls:

```go
type ToolFilter struct {
    Blocked map[string]bool
}

func (f *ToolFilter) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    filtered := resp.ToolCalls[:0]
    for _, tc := range resp.ToolCalls {
        if !f.Blocked[tc.Name] {
            filtered = append(filtered, tc)
        }
    }
    resp.ToolCalls = filtered
    return nil
}
```

## Approval Gate (PostProcessor + InputHandler)

Ask human approval before executing dangerous tools:

```go
type ApprovalGate struct {
    RequireApproval map[string]bool
}

func (g *ApprovalGate) PostLLM(ctx context.Context, resp *oasis.ChatResponse) error {
    handler, ok := oasis.InputHandlerFromContext(ctx)
    if !ok {
        return nil  // no handler, skip gracefully
    }
    for i, tc := range resp.ToolCalls {
        if !g.RequireApproval[tc.Name] {
            continue
        }
        res, err := handler.RequestInput(ctx, oasis.InputRequest{
            Question: fmt.Sprintf("Allow %s(%s)?", tc.Name, tc.Args),
            Options:  []string{"Yes", "No"},
        })
        if err != nil {
            return err
        }
        if res.Value != "Yes" {
            resp.ToolCalls = append(resp.ToolCalls[:i], resp.ToolCalls[i+1:]...)
        }
    }
    return nil
}
```

## Registration

Processors run in registration order. Put guardrails first:

```go
agent := oasis.NewLLMAgent("safe-agent", "Agent with guardrails", provider,
    oasis.WithTools(searchTool, shellTool),
    oasis.WithInputHandler(handler),
    oasis.WithProcessors(
        &Guardrail{},                                               // first: block bad input
        &PIIRedactor{},                                             // second: redact PII
        &ApprovalGate{RequireApproval: map[string]bool{"shell_exec": true}},  // third: approval gate
        &ToolFilter{Blocked: map[string]bool{"dangerous_tool": true}},        // fourth: filter tools
    ),
)
```

## Rules

- Implement only the interfaces you need — the chain skips missing phases
- Return `ErrHalt` for intentional stops, regular errors for infrastructure failures
- Processors must be safe for concurrent use
- Modify in place via pointers (`*ChatRequest`, `*ChatResponse`, `*ToolResult`)

## See Also

- [Processor Concept](../concepts/processor.md)
- [Human-in-the-Loop Guide](human-in-the-loop.md)
