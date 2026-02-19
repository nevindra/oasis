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

## Logging (PostProcessor + PostToolProcessor)

Log every LLM response and tool execution:

```go
type Logger struct{}

func (l *Logger) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    log.Printf("[llm] tokens: in=%d out=%d, tool_calls=%d",
        resp.Usage.InputTokens, resp.Usage.OutputTokens, len(resp.ToolCalls))
    return nil
}

func (l *Logger) PostTool(_ context.Context, call oasis.ToolCall, result *oasis.ToolResult) error {
    log.Printf("[tool] %s → %.100s", call.Name, result.Content)
    return nil
}
```

For post-execution analysis without a processor, use `result.Steps` — see [Execution Traces](../concepts/observability.md#built-in-execution-traces-no-otel-required).

## Token Budget (PreProcessor)

For most cases, use the built-in `MaxTokens` conversation option instead of a processor — it trims history by estimated token count before the LLM call:

```go
oasis.WithConversationMemory(store, oasis.MaxTokens(4000))
```

For custom trimming logic (e.g. per-request limits, priority-based retention), use a PreProcessor:

```go
type TokenBudget struct {
    MaxMessages int // keep only the N most recent messages (plus system prompt)
}

func (t *TokenBudget) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
    if len(req.Messages) <= t.MaxMessages+1 { // +1 for system prompt
        return nil
    }
    // Keep the system prompt (first message) and the most recent N messages.
    req.Messages = append(req.Messages[:1], req.Messages[len(req.Messages)-t.MaxMessages:]...)
    return nil
}
```

## Retries

Retries are handled at different levels depending on what you're retrying:

- **LLM call retries** (429, 503) — use `oasis.WithRetry(provider)` at the Provider level. Wraps the provider with exponential backoff before the agent loop sees the error.
- **Workflow step retries** — use `oasis.Retry(n, delay)` on individual steps. Re-executes the step function up to N times with the specified delay.
- **Processors** cannot trigger retries — they transform/validate within a single iteration. If you need to retry an entire agent execution, wrap the `Execute` call at the application level.

```go
// Provider-level retries (transient HTTP errors)
provider := oasis.WithRetry(gemini.New(apiKey, model), oasis.RetryMaxAttempts(5))

// Workflow step-level retries
oasis.Step("fetch", fetchFunc, oasis.Retry(3, 2*time.Second))
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
