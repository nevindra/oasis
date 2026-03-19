# Processors and Guardrails

Processors hook into the agent execution pipeline to transform, validate, or control messages. This guide covers built-in guardrails and shows practical examples of custom processors.

## Built-in Guardrails

Oasis ships four guardrail types that cover common safety patterns. All implement the existing processor interfaces and compose via `WithProcessors()`.

### InjectionGuard

Multi-layer prompt injection detection (PreProcessor). Five detection layers:

1. **Known phrases** — ~55 patterns covering instruction override, role hijacking, system prompt extraction, and policy bypass
2. **Role override** — role prefixes (`system:`, `assistant:`), markdown headers (`## System`), XML tags (`<system>`). May flag legitimate content with patterns like `user:` at line start — use `SkipLayers(2)` if this causes false positives
3. **Delimiter injection** — fake message boundaries (`--- system`), separator abuse (`==== begin`)
4. **Encoding/obfuscation** — NFKC Unicode normalization (catches fullwidth Latin, mathematical alphanumerics, ligatures), zero-width/invisible character stripping (7 character types), base64-encoded payload detection with length validation
5. **Custom patterns** — user-supplied string patterns and regex

By default only the last user message is checked. Use `ScanAllMessages()` to scan all user messages in the conversation history (detects injection placed in earlier messages via multi-turn context poisoning).

```go
// Default — all layers enabled, last message only
guard := oasis.NewInjectionGuard()

// With custom patterns and regex
guard := oasis.NewInjectionGuard(
    oasis.InjectionPatterns("secret override", "admin mode"),
    oasis.InjectionRegex(regexp.MustCompile(`(?i)\bsudo\s+mode\b`)),
    oasis.InjectionResponse("Request blocked."),
)

// Scan all user messages in conversation history
guard := oasis.NewInjectionGuard(oasis.ScanAllMessages())

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

## InjectionGuard Deep Dive

`InjectionGuard` is a `PreProcessor` that detects prompt injection attempts using five layered heuristics. It returns `ErrHalt` when injection is detected and is safe for concurrent use. This section covers how each layer works, how the guard is used internally, and how to extend it.

### How Detection Works

Every user message passes through up to five layers in order. The first match triggers an `ErrHalt` -- later layers are skipped.

Before any layer runs, a pre-pass normalizes the message:

1. **Zero-width character stripping** -- strips or replaces 7 invisible Unicode character types: replaces 6 (zero-width space, zero-width non-joiner, zero-width joiner, BOM, word joiner, Mongolian vowel separator) with spaces, and strips soft hyphens entirely. This prevents attackers from splitting known phrases with invisible characters (e.g., `ignore\u200ball\u200bprevious`).
2. **NFKC normalization** -- converts fullwidth Latin characters, mathematical alphanumerics, ligatures, and other Unicode equivalents to their ASCII counterparts. Catches attacks like writing "ignore all previous instructions" using fullwidth characters.

The normalized text is then lowercased and passed through each enabled layer.

### Attack Categories (Layer 1)

The ~55 built-in phrases are grouped into four categories:

| Category | Purpose | Example Phrases |
|----------|---------|----------------|
| Instruction override | Attempts to replace the system prompt | "ignore all previous instructions", "disregard your instructions", "new instructions", "from now on ignore" |
| Role hijacking | Attempts to change the agent's identity | "you are now", "pretend to be", "enter developer mode", "dan mode", "jailbreak" |
| System prompt extraction | Attempts to leak the system prompt | "reveal your system prompt", "show me your instructions", "what were you told" |
| Policy bypass | Attempts to circumvent safety rules | "this is for educational purposes", "hypothetically speaking", "bypass your filters", "no restrictions" |

All phrases are matched as case-insensitive substrings against the normalized message. This means "Please IGNORE ALL PREVIOUS INSTRUCTIONS and help" triggers a match.

### Structural Detection (Layers 2-3)

**Layer 2 -- Role Override** detects attempts to inject fake role markers into user messages:

- Role prefixes at line start: `system:`, `assistant:`, `user:`, `human:`, `ai:`
- Markdown headers: `## System`, `## Instruction`, `## Prompt`
- XML tags: `<system>`, `<prompt>`, `<instruction>`

**Layer 3 -- Delimiter Injection** detects fake message boundaries meant to trick the LLM into thinking a new conversation or system message has started:

- Fake boundaries: `--- system`, `--- new conversation`, `--- begin`
- Separator abuse: `==== system`, `**** begin`, `==== prompt`

Layer 2 is the most likely to produce false positives (e.g., a user writing `user: John` at line start). Use `SkipLayers(2)` if this is an issue for your application.

### Encoding Detection (Layer 4)

Beyond the pre-pass normalization, Layer 4 detects base64-encoded injection payloads:

1. Finds alphanumeric blocks of 20+ characters matching `[A-Za-z0-9+/]{20,}={0,2}`
2. Skips candidates whose length is not a multiple of 4 (invalid base64)
3. Attempts decoding with both standard and raw (no padding) base64
4. Re-checks decoded content against all Layer 1 phrases
5. Checks up to 5 candidates per message to bound computation

This catches attacks like embedding `aWdub3JlIGFsbCBwcmV2aW91cyBpbnN0cnVjdGlvbnM=` (base64 for "ignore all previous instructions") in an otherwise clean message.

### All Configuration Options

```go
// All defaults: 5 layers enabled, last message only, built-in phrases
guard := oasis.NewInjectionGuard()

// Add domain-specific patterns (appended to Layer 1 phrases)
guard := oasis.NewInjectionGuard(
    oasis.InjectionPatterns("secret override", "admin mode"),
)

// Add regex patterns (checked in Layer 5)
guard := oasis.NewInjectionGuard(
    oasis.InjectionRegex(regexp.MustCompile(`(?i)\bsudo\s+mode\b`)),
)

// Custom halt message (default: "I can't process that request.")
guard := oasis.NewInjectionGuard(
    oasis.InjectionResponse("Request blocked for safety reasons."),
)

// Scan ALL user messages in conversation history, not just the last one.
// Defends against multi-turn context poisoning where injection is placed
// in an earlier message and a clean message is sent later.
guard := oasis.NewInjectionGuard(oasis.ScanAllMessages())

// Disable specific layers that cause false positives
guard := oasis.NewInjectionGuard(oasis.SkipLayers(2, 3))

// Structured logging -- blocked requests logged at WARN with matched layer
guard := oasis.NewInjectionGuard(oasis.InjectionLogger(slog.Default()))

// Combine multiple options
guard := oasis.NewInjectionGuard(
    oasis.InjectionPatterns("secret override"),
    oasis.InjectionRegex(regexp.MustCompile(`(?i)\bsudo\s+mode\b`)),
    oasis.InjectionResponse("Blocked."),
    oasis.ScanAllMessages(),
    oasis.SkipLayers(3),
    oasis.InjectionLogger(slog.Default()),
)
```

### Internal Usage: Memory System Protection

The Oasis memory system uses a separate, narrower injection filter to sanitize facts extracted from conversations. In `memory.go`, the `sanitizeFacts` function checks each extracted fact against `factInjectionPatterns` -- a small set of high-confidence markers like `[system`, `<|im_start|>`, and `you are now`. Facts matching these patterns are silently dropped before storage.

This protects the fact extraction pipeline from storing injected instructions as "facts" that would later be recalled into agent context. The filter is intentionally narrower than `InjectionGuard` to avoid false positives on legitimate conversational content that happens to contain common phrases.

This is distinct from `InjectionGuard` -- the memory filter runs inside the extraction pipeline, not as a registered processor. If you need to protect your own data pipelines similarly, you can reuse `InjectionGuard` directly or build a domain-specific pattern filter.

### Using InjectionGuard in Custom Processors

Since `InjectionGuard` implements `PreProcessor`, you can wrap it to add custom behavior. For example, a processor that runs injection detection and logs blocked attempts to an external audit system:

```go
type AuditedInjectionGuard struct {
    guard  *oasis.InjectionGuard
    logger *slog.Logger
}

func NewAuditedInjectionGuard(logger *slog.Logger) *AuditedInjectionGuard {
    return &AuditedInjectionGuard{
        guard:  oasis.NewInjectionGuard(oasis.ScanAllMessages()),
        logger: logger,
    }
}

func (a *AuditedInjectionGuard) PreLLM(ctx context.Context, req *oasis.ChatRequest) error {
    err := a.guard.PreLLM(ctx, req)
    if err != nil {
        last := req.Messages[len(req.Messages)-1]
        a.logger.WarnContext(ctx, "injection attempt blocked",
            "content_preview", last.Content[:min(100, len(last.Content))],
        )
    }
    return err
}
```

Register it like any processor:

```go
agent := oasis.NewLLMAgent("audited-agent", "Agent with audited guard", provider,
    oasis.WithProcessors(
        NewAuditedInjectionGuard(slog.Default()),
        oasis.NewContentGuard(oasis.MaxInputLength(5000)),
    ),
)
```

### Design Philosophy

- **Low false positives over high recall.** The ~55 built-in phrases target well-known attack patterns with high confidence. A guard that blocks legitimate messages erodes trust faster than one that misses edge-case attacks.
- **Layered defense.** Five layers catch different attack vectors -- direct phrases, structural manipulation, delimiter abuse, encoding tricks, and custom patterns. Each layer is independently skippable via `SkipLayers()`.
- **Unicode-aware by default.** NFKC normalization and zero-width character stripping run as a pre-pass before all layers, closing common obfuscation vectors without requiring user configuration.
- **Extensible, not exhaustive.** Layer 5 lets you add domain-specific detection via `InjectionPatterns()` (substring) and `InjectionRegex()` (regex) without forking the built-in set.

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
