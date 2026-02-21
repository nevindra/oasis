# Processors and Guardrails

Processors hook into the agent execution pipeline to transform, validate, or control messages. This guide covers built-in guardrails and shows practical examples of custom processors.

## Built-in Guardrails

Oasis ships four guardrail types that cover common safety patterns. All implement the existing processor interfaces and compose via `WithProcessors()`.

### InjectionGuard

Multi-layer prompt injection detection (PreProcessor). Five detection layers:

1. **Known phrases** — ~55 patterns covering instruction override, role hijacking, system prompt extraction, and policy bypass
2. **Role override** — role prefixes (`system:`, `assistant:`), markdown headers (`## System`), XML tags (`<system>`)
3. **Delimiter injection** — fake message boundaries (`--- system`), separator abuse (`==== begin`)
4. **Encoding/obfuscation** — zero-width character stripping, base64-encoded payload detection
5. **Custom patterns** — user-supplied string patterns and regex

```go
// Default — all layers enabled
guard := oasis.NewInjectionGuard()

// With custom patterns and regex
guard := oasis.NewInjectionGuard(
    oasis.InjectionPatterns("secret override", "admin mode"),
    oasis.InjectionRegex(regexp.MustCompile(`(?i)\bsudo\s+mode\b`)),
    oasis.InjectionResponse("Request blocked."),
)

// Skip layers that cause false positives
guard := oasis.NewInjectionGuard(oasis.SkipLayers(2, 3))
```

### ContentGuard

Input/output length enforcement (PreProcessor + PostProcessor). Limits are in runes (Unicode-safe).

```go
// Input only
guard := oasis.NewContentGuard(oasis.MaxInputLength(5000))

// Both input and output
guard := oasis.NewContentGuard(
    oasis.MaxInputLength(5000),
    oasis.MaxOutputLength(10000),
    oasis.ContentResponse("Message too long."),
)
```

### KeywordGuard

Keyword and regex content blocking (PreProcessor). Keywords are matched case-insensitively as substrings.

```go
guard := oasis.NewKeywordGuard("DROP TABLE", "rm -rf").
    WithRegex(regexp.MustCompile(`\b(SSN|social\s+security)\b`)).
    WithResponse("Blocked content detected.")
```

### MaxToolCallsGuard

Tool call limiting (PostProcessor). Trims excess tool calls silently — graceful degradation instead of halting.

```go
guard := oasis.NewMaxToolCallsGuard(3) // keep first 3 tool calls
```

### Composing Guards

Stack guards with custom processors in registration order:

```go
agent := oasis.NewLLMAgent("safe-agent", "Agent with guardrails", provider,
    oasis.WithProcessors(
        oasis.NewInjectionGuard(),                     // block injection
        oasis.NewContentGuard(oasis.MaxInputLength(5000)), // enforce limits
        oasis.NewKeywordGuard("DROP TABLE"),            // block keywords
        oasis.NewMaxToolCallsGuard(3),                  // cap tool calls
        &PIIRedactor{},                                 // custom: redact PII
    ),
)
```

## Custom Processors

The examples below show how to build custom processors for cases not covered by the built-in guards.

## Custom Guardrail (PreProcessor)

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

## Suspend from Processors

Processors can trigger suspension to pause execution for external input. Return `Suspend()` from a `PreProcessor` or `PostProcessor` to halt the agent — the caller receives `*ErrSuspended` and can resume later:

```go
type ComplianceGate struct{}

func (g *ComplianceGate) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    if containsSensitiveAction(resp.ToolCalls) {
        payload, _ := json.Marshal(map[string]any{
            "reason":     "sensitive action detected",
            "tool_calls": resp.ToolCalls,
        })
        return oasis.Suspend(json.RawMessage(payload))
    }
    return nil
}
```

The caller handles suspension the same way as Workflow suspend:

```go
result, err := agent.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // Show payload to human, get approval...
    result, err = suspended.Resume(ctx, json.RawMessage(`{"approved": true}`))
}
```

## Retries and Rate Limiting

Retries and rate limiting are handled at different levels depending on what you're protecting:

- **LLM call retries** (429, 503) — use `oasis.WithRetry(provider)` at the Provider level. Wraps the provider with exponential backoff before the agent loop sees the error.
- **Rate limiting** — use `oasis.WithRateLimit(provider, limits...)` to proactively throttle requests. Sleeps before hitting limits instead of reacting to 429 errors.
- **Workflow step retries** — use `oasis.Retry(n, delay)` on individual steps. Re-executes the step function up to N times with the specified delay.
- **Processors** cannot trigger retries — they transform/validate within a single iteration.

```go
// Provider-level retries (transient HTTP errors)
provider := oasis.WithRetry(gemini.New(apiKey, model), oasis.RetryMaxAttempts(5))

// Rate limiting (proactive throttling)
provider = oasis.WithRateLimit(provider, oasis.RPM(60), oasis.TPM(100000))

// Workflow step-level retries
oasis.Step("fetch", fetchFunc, oasis.Retry(3, 2*time.Second))
```

`WithRetry` and `WithRateLimit` compose — use both for production workloads.

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
