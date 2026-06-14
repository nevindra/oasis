# Processors — Examples

Runnable recipes for the most common processor patterns. Each snippet is
complete enough to drop into a real file with the right imports.

---

## 1. Stack built-in guardrails

The most common setup: injection detection, length limits, keyword blocking, and
a tool call cap. All four guards compose via `WithPreProcessors` and
`WithPostProcessors`.

```go
import (
    "regexp"
    "github.com/nevindra/oasis"
)

agent := oasis.NewLLMAgent("safe", "Safe agent", provider,
    oasis.WithPreProcessors(
        oasis.NewInjectionGuard(),                      // block injection attempts
        oasis.NewContentGuard(oasis.MaxInputLength(5000)), // reject long input
        oasis.NewKeywordGuard("DROP TABLE", "rm -rf"),  // block dangerous keywords
    ),
    oasis.WithPostProcessors(
        oasis.NewContentGuard(oasis.MaxOutputLength(10_000)), // cap output
        oasis.NewMaxToolCallsGuard(3),                         // keep first 3 tool calls
    ),
)
```

Each guard runs in the order listed. `InjectionGuard` runs first, so injection
attempts never reach the LLM. `MaxToolCallsGuard` runs on the post side to trim
whatever the LLM requested.

---

## 2. Custom guardrail (PreProcessor)

Implement `PreProcessor` to write a domain-specific check. Return `*ErrHalt`
to stop; return `nil` to pass.

```go
type CompanyPolicyGuard struct{}

func (g *CompanyPolicyGuard) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
    // find the last user message
    for i := len(req.Messages) - 1; i >= 0; i-- {
        if req.Messages[i].Role == "user" {
            if strings.Contains(strings.ToLower(req.Messages[i].Content), "competitor") {
                return &oasis.ErrHalt{Response: "I can't discuss competitors."}
            }
            break
        }
    }
    return nil
}
```

Register it alongside the built-in guards:

```go
agent := oasis.NewLLMAgent("corp", "Corporate agent", provider,
    oasis.WithPreProcessors(
        oasis.NewInjectionGuard(),
        &CompanyPolicyGuard{},
    ),
)
```

---

## 3. PII redactor (all three phases)

Implement all three interfaces in one struct to redact sensitive data at every
stage.

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
    result.Content = redactPII(result.Content)  // oasis.TextContent wraps string → RawMessage
    return nil
}

func redactPII(s string) string {
    // replace credit card numbers, SSNs, etc.
    return creditCardRE.ReplaceAllString(s, "[REDACTED]")
}
```

Register once and it covers all three hook points:

```go
agent := oasis.NewLLMAgent("redacted", "PII-safe agent", provider,
    oasis.WithPreProcessors(&PIIRedactor{}),
    oasis.WithPostProcessors(&PIIRedactor{}),
    oasis.WithPostToolProcessors(&PIIRedactor{}),
)
```

---

## 4. Audit logger (PostToolProcessor)

Log every tool execution without interfering with results.

```go
type AuditLogger struct {
    log *slog.Logger
}

func (l *AuditLogger) PostTool(ctx context.Context, call oasis.ToolCall, result *oasis.ToolResult) error {
    l.log.InfoContext(ctx, "tool executed",
        "tool", call.Name,
        "args_len", len(call.Args),
        "result_len", len(result.Content),
        "has_error", result.Error != "",
    )
    return nil // never halt, never modify — observe only
}
```

---

## 5. Token budget trimmer (PreProcessor)

Keep message history within a fixed count. For rune-based trimming, use
`oasis.WithConversationMemory(store, oasis.MaxTokens(4000))` instead — this
pattern is for custom priority-based trimming.

```go
type MessageBudget struct {
    Keep int // number of recent messages to keep (system prompt always preserved)
}

func (t *MessageBudget) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
    // Messages[0] is the system prompt; keep it plus the most recent Keep messages.
    if len(req.Messages) <= t.Keep+1 {
        return nil
    }
    req.Messages = append(req.Messages[:1], req.Messages[len(req.Messages)-t.Keep:]...)
    return nil
}
```

---

## 6. Tool filter (PostProcessor)

Remove specific tool calls from the LLM response before they execute.

```go
type ToolFilter struct {
    Blocked map[string]bool
}

func (f *ToolFilter) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    allowed := resp.ToolCalls[:0] // reuse backing array
    for _, tc := range resp.ToolCalls {
        if !f.Blocked[tc.Name] {
            allowed = append(allowed, tc)
        }
    }
    resp.ToolCalls = allowed
    return nil
}
```

---

## 7. Approval gate via Suspend (PostProcessor)

Pause execution when the LLM requests a sensitive tool. The caller receives
`*ErrSuspended`, shows the pending call to a human, and resumes or cancels.

```go
type ApprovalGate struct {
    Sensitive map[string]bool
}

func (g *ApprovalGate) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    for _, tc := range resp.ToolCalls {
        if g.Sensitive[tc.Name] {
            payload, _ := json.Marshal(map[string]any{
                "tool":   tc.Name,
                "args":   json.RawMessage(tc.Args),
                "reason": "requires human approval",
            })
            return oasis.Suspend(json.RawMessage(payload))
        }
    }
    return nil
}
```

Caller side:

```go
result, err := agent.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // decode and present suspended.Payload to the human
    approved := askHuman(suspended.Payload)
    if !approved {
        suspended.Release()
        return // no resume
    }
    result, err = suspended.Resume(ctx, json.RawMessage(`{"approved":true}`))
}
```

---

## 8. Typed suspend with SuspendProtocol (PostProcessor)

When the suspend payload and resume response have fixed shapes, use
`SuspendProtocol` so the compiler enforces the types.

```go
// Declare the contract once, at package level.
type TransferReq struct {
    Amount float64
    To     string
}
type ApprovalResp struct {
    Approved bool
    Note     string
}

var ApproveTransfer = oasis.NewSuspendProtocol[TransferReq, ApprovalResp]("billing.approve_transfer").
    WithRenderResume(func(r ApprovalResp) string {
        if r.Approved {
            return "Human approved the transfer. Note: " + r.Note
        }
        return "Human rejected the transfer. Note: " + r.Note
    })

// Suspend from a processor.
type TransferGate struct{}

func (g *TransferGate) PostLLM(_ context.Context, resp *oasis.ChatResponse) error {
    amount := extractAmount(resp) // domain logic
    if amount > 1000 {
        return ApproveTransfer.Suspend(TransferReq{Amount: amount, To: "acct_123"})
    }
    return nil
}

// Resume from the caller — compile error if types are wrong.
result, err := agent.Execute(ctx, task)
var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    req, _ := ApproveTransfer.PayloadFrom(suspended) // returns TransferReq
    // ... show req to human ...
    result, err = ApproveTransfer.Resume(suspended, ctx, ApprovalResp{
        Approved: true,
        Note:     "manager approved",
    })
}
```

---

## 9. Approval gate via InputHandler (PostProcessor)

For synchronous approval where the handler can answer immediately (CLI prompt,
HTTP endpoint), access the `InputHandler` from the context instead of suspending.

```go
type SyncApprovalGate struct {
    Needs map[string]bool
}

func (g *SyncApprovalGate) PostLLM(ctx context.Context, resp *oasis.ChatResponse) error {
    handler, ok := agent.InputHandlerFromContext(ctx)
    if !ok {
        return nil // no handler configured, skip
    }
    kept := resp.ToolCalls[:0]
    for _, tc := range resp.ToolCalls {
        if !g.Needs[tc.Name] {
            kept = append(kept, tc)
            continue
        }
        res, err := handler.RequestInput(ctx, agent.InputRequest{
            Question: fmt.Sprintf("Run %s? (Yes/No)", tc.Name),
            Options:  []string{"Yes", "No"},
        })
        if err != nil {
            return err
        }
        if res.Value == "Yes" {
            kept = append(kept, tc)
        }
    }
    resp.ToolCalls = kept
    return nil
}
```

Wire it up:

```go
agent := oasis.NewLLMAgent("gated", "...", provider,
    oasis.WithInputHandler(myHandler),
    oasis.WithPostProcessors(&SyncApprovalGate{Needs: map[string]bool{"shell_exec": true}}),
)
```

Use `Suspend` (recipe 7–8) when the human is offline or the approval is
asynchronous. Use `InputHandler` (this recipe) when the handler can block
and answer in the same request cycle.

---

## 10. Combined pre + post + rate limiting

A production setup combining guardrails, custom logic, and a rate-limited
provider.

```go
base := gemini.New(apiKey, "gemini-2.0-flash")
llm := provider.Chain(
    agent.RetryMiddleware(agent.RetryMaxAttempts(5)),
    oasis.RateLimitMiddleware(oasis.RPM(60), oasis.TPM(100_000)),
)(base)

agent := oasis.NewAgent("prod", "Production agent", llm,
    oasis.WithPreProcessors(
        oasis.NewInjectionGuard(oasis.ScanAllMessages()),
        oasis.NewContentGuard(oasis.MaxInputLength(8000)),
        &CompanyPolicyGuard{},
        &PIIRedactor{},
    ),
    oasis.WithPostProcessors(
        oasis.NewMaxToolCallsGuard(5),
        &ToolFilter{Blocked: map[string]bool{"legacy_api": true}},
    ),
    oasis.WithPostToolProcessors(
        &AuditLogger{log: slog.Default()},
        &PIIRedactor{},
    ),
)
```

Processors run in the listed order at each hook point. The rate limiter acts
at the provider level — it is not a processor, but it controls whether the LLM
call is made at all.

---

## See also

- [Concept overview](index.md) — the pipeline picture and key rules
- [API reference](api.md) — all constructors and options
- [HITL concept](../concepts/hitl.md) — typed suspend/resume, streaming suspends
- [Human-in-the-Loop guide](../guides/human-in-the-loop.md) — end-to-end approval flows
