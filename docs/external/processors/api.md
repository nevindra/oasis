# Processors API Reference

All processor interfaces and helpers are in the root `oasis` package or the
`core` package. Guardrail constructors and options live in
`github.com/nevindra/oasis/guardrail` — import it directly; they are not
re-exported from the root `oasis` package. The `ProcessorChain` helper is in
`github.com/nevindra/oasis/processor`. `InputHandler` and related types live in
`github.com/nevindra/oasis/agent`.

---

## Core interfaces

All four processor interfaces live in `github.com/nevindra/oasis/core`.

### `PreProcessor`

```go
type PreProcessor interface {
    PreLLM(ctx context.Context, req *ChatRequest) error
}
```

Runs before each LLM call. `req.Messages` holds the full conversation so far.
Modify the slice in place to inject, remove, or transform messages. Return
`*ErrHalt` to short-circuit. Return `Suspend(payload)` to pause for human input.
Must be safe for concurrent use.

### `PostProcessor`

```go
type PostProcessor interface {
    PostLLM(ctx context.Context, resp *ChatResponse) error
}
```

Runs after the LLM responds, before any tool calls execute. `resp.Content` holds
the text response. `resp.ToolCalls` holds the tool calls the LLM requested.
Modify either in place. Return `*ErrHalt` or `Suspend(payload)` to stop or pause.
Must be safe for concurrent use.

### `PostToolProcessor`

```go
type PostToolProcessor interface {
    PostTool(ctx context.Context, call ToolCall, result *ToolResult) error
}
```

Runs after each individual tool execution, before the result is appended to the
message history. `call` is read-only (the tool call that was made). `result` is
a pointer — modify it to redact, transform, or annotate the output. Return
`*ErrHalt` to halt after this result. Must be safe for concurrent use.

### `StreamProcessor`

```go
// import "github.com/nevindra/oasis/core"

type StreamProcessor interface {
    PostChunk(ctx context.Context, ev *core.StreamEvent) (*core.StreamEvent, error)
}
```

Optional capability interface that runs on each streamed `EventTextDelta` or
`EventThinking` delta before it reaches the caller's channel. Processors opt in
by implementing it; the chain invokes it only for registered implementers.

Return the event (possibly mutated) to forward it, `nil` to drop it, or
`*core.ErrHalt` to halt the stream. A halt emits `EventHalt` to the caller's
channel but does **not** abort the in-flight LLM call or stop billing — for a
hard run-ending halt use a `PostProcessor` instead. The hook has no cross-chunk
state from the framework's perspective; processors needing multi-chunk context
must manage their own buffering.

Must be safe for concurrent use.

---

## Error types

### `ErrHalt`

```go
type ErrHalt struct {
    Response string
}
func (e *ErrHalt) Error() string
```

Return `*ErrHalt` (pointer) from any processor hook to stop the agent loop.
The agent returns `AgentResult{Output: halt.Response}` with a nil Go error.
The caller sees a clean result — they do not know the loop stopped early.

```go
return &oasis.ErrHalt{Response: "blocked by policy"}
```

Value form (`oasis.ErrHalt{...}`) does not satisfy the `error` interface;
always return the pointer.

### `ErrSuspended`

```go
type ErrSuspended struct {
    Step    string          // agent or step name that suspended
    Payload json.RawMessage // context for the human
    // ...
}
func (e *ErrSuspended) Resume(ctx context.Context, data json.RawMessage) (AgentResult, error)
func (e *ErrSuspended) ResumeStream(ctx context.Context, data json.RawMessage, ch chan<- StreamEvent) (AgentResult, error)
func (e *ErrSuspended) Release()
func (e *ErrSuspended) WithSuspendTTL(d time.Duration)
func (e *ErrSuspended) Error() string
```

Returned by `Execute` when a processor (or workflow step) called `Suspend`.
Call `Resume` or `ResumeStream` to continue execution with the human's response.
Call `Release` when the suspend will not be resumed (timeout, abandonment) to
free the captured message snapshot. A default TTL of 30 minutes applies
automatically; override with `WithSuspendTTL`.

`Resume` is single-use. Calling it more than once is undefined behavior.

---

## Suspend helpers

### `Suspend`

```go
func Suspend(payload json.RawMessage) error
```

Returns a sentinel error that signals the engine to pause execution. Use from a
processor hook or workflow step. `payload` is shown to the caller via
`ErrSuspended.Payload` — put whatever context the human needs to make a
decision.

### `SuspendProtocol[Req, Resp]`

A typed contract for structured suspend/resume. Declare once, use at both the
suspension site and the resume site.

```go
func NewSuspendProtocol[Req, Resp any](name string) SuspendProtocol[Req, Resp]

func (p SuspendProtocol[Req, Resp]) WithRenderResume(fn func(Resp) string) SuspendProtocol[Req, Resp]
func (p SuspendProtocol[Req, Resp]) Suspend(payload Req) error
func (p SuspendProtocol[Req, Resp]) PayloadFrom(e *ErrSuspended) (Req, error)
func (p SuspendProtocol[Req, Resp]) Resume(e *ErrSuspended, ctx context.Context, data Resp) (AgentResult, error)
func (p SuspendProtocol[Req, Resp]) ResumeStream(e *ErrSuspended, ctx context.Context, data Resp, ch chan<- StreamEvent) (AgentResult, error)
func (p SuspendProtocol[Req, Resp]) Name() string
```

`name` is a stable identifier used in runtime mismatch errors (e.g., resuming
with the wrong protocol). Use a namespaced prefix to avoid collisions:
`"billing.approve_transfer"`. `WithRenderResume` controls the natural-language
message injected into the LLM's history when the human resumes.

---

## Registration options

### `WithPreProcessors`

```go
func WithPreProcessors(processors ...PreProcessor) AgentOption
```

Registers `PreProcessor` hooks. Multiple calls append to the list. Processors
run in registration order before each LLM call.

### `WithPostProcessors`

```go
func WithPostProcessors(processors ...PostProcessor) AgentOption
```

Registers `PostProcessor` hooks. Processors run in registration order after each
LLM response.

### `WithPostToolProcessors`

```go
func WithPostToolProcessors(processors ...PostToolProcessor) AgentOption
```

Registers `PostToolProcessor` hooks. Processors run in registration order after
each tool execution.

### `WithInputHandler`

```go
func WithInputHandler(h InputHandler) AgentOption
```

Sets the human-in-the-loop handler. When set, the agent gains a built-in
`ask_user` tool the LLM can call to request human input mid-loop. The handler
is also accessible from processor hooks via
`agent.InputHandlerFromContext(ctx)`.

---

## ProcessorChain

```go
// import "github.com/nevindra/oasis/processor"

func NewChain() *Chain

func (c *Chain) AddPre(p core.PreProcessor)
func (c *Chain) AddPost(p core.PostProcessor)
func (c *Chain) AddPostTool(p core.PostToolProcessor)
func (c *Chain) AddStream(p core.StreamProcessor)

func (c *Chain) RunPreLLM(ctx context.Context, req *core.ChatRequest) error
func (c *Chain) RunPostLLM(ctx context.Context, resp *core.ChatResponse) error
func (c *Chain) RunPostTool(ctx context.Context, call core.ToolCall, result *core.ToolResult) error
func (c *Chain) RunPostChunk(ctx context.Context, ev *core.StreamEvent) (*core.StreamEvent, error)

func (c *Chain) Len() int
func (c *Chain) HasAny() bool
func (c *Chain) HasStream() bool
```

`Add*` methods bucket processors by interface at registration time so the
`Run*` methods have no per-call type assertions. `Len` counts total
registrations across all stages. `HasStream` lets the agent forwarder skip
chunk processing when no stream processors are registered (zero hot-path cost).

`RunPostChunk` threads event through registered stream processors in order. A
processor returning `nil` short-circuits the chain and drops the event.

`oasis.NewProcessorChain()` is a convenience wrapper that returns `*processor.Chain`.

---

## InputHandler (agent package)

```go
// import "github.com/nevindra/oasis/agent"

type InputHandler interface {
    RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}

type InputRequest struct {
    Question    string
    Options     []string            // empty = free-form
    MultiSelect bool                // true when the LLM requested multi-select
    Metadata    map[string]string   // agent name, tool being approved, etc.
}

type InputResponse struct {
    Value  string   // single-select answer
    Values []string // multi-select answers (nil when single-select)
}

func InputHandlerFromContext(ctx context.Context) (InputHandler, bool)
func WithInputHandlerContext(ctx context.Context, h InputHandler) context.Context
```

`InputHandler` bridges the agent to your communication channel (HTTP, CLI,
Telegram, etc.). `RequestInput` must block until a response arrives or `ctx` is
cancelled. `InputHandlerFromContext` retrieves the handler from the context that
processors receive — useful for approval-gate processors that want to route
through the same handler.

When `InputRequest.MultiSelect` is true the LLM requested multiple selections;
populate `InputResponse.Values` (a `[]string`) with the chosen items. The agent
marshals `Values` to a JSON array and returns it as the `ask_user` tool result.
When `MultiSelect` is false (the default), the agent returns `InputResponse.Value`
as a plain string.

---

## Guardrails

All guards are in `github.com/nevindra/oasis/guardrail`. Import the package
directly — guard constructors are not re-exported from the root `oasis` package.

### `InjectionGuard` (PreProcessor)

Five-layer prompt injection detection: known phrases, role overrides, delimiter
injection, base64-encoded payloads, and custom patterns. Returns `*ErrHalt` on
detection.

```go
func NewInjectionGuard(opts ...InjectionOption) *InjectionGuard

type InjectionOption func(*InjectionGuard)

func InjectionResponse(msg string) InjectionOption   // default: "I can't process that request."
func InjectionPatterns(patterns ...string) InjectionOption  // append to Layer 1 phrases
func InjectionRegex(patterns ...*regexp.Regexp) InjectionOption  // Layer 5 custom regex
func ScanAllMessages() InjectionOption              // scan all user messages, not just last
func SkipLayers(layers ...int) InjectionOption      // skip layers 1-5 (useful for false positives)
func InjectionLogger(l *slog.Logger) InjectionOption // log blocked requests at WARN
```

Detection runs a pre-pass (zero-width char stripping + NFKC normalization)
before any layer. Layer 2 (role override) is the most likely to produce false
positives on content containing `user:` at line start; use `SkipLayers(2)` if
needed.

### `ContentGuard` (PreProcessor + PostProcessor)

Input and output length limits in runes (Unicode-safe). Zero for a limit
disables that check.

```go
func NewContentGuard(opts ...ContentOption) *ContentGuard

type ContentOption func(*ContentGuard)

func MaxInputLength(n int) ContentOption    // rune limit on last user message
func MaxOutputLength(n int) ContentOption   // rune limit on LLM response
func ContentResponse(msg string) ContentOption  // default: "Content exceeds the allowed length."
func ContentLogger(l *slog.Logger) ContentOption
```

### `KeywordGuard` (PreProcessor)

Keyword and regex blocklist for user messages. Keywords are matched
case-insensitively as substrings.

```go
func NewKeywordGuard(keywords []string, opts ...KeywordOption) *KeywordGuard

type KeywordOption func(*KeywordGuard)

func KeywordRegex(patterns ...*regexp.Regexp) KeywordOption  // regex matched against raw (non-lowercased) content
func KeywordLogger(l *slog.Logger) KeywordOption             // log blocked requests at WARN
func KeywordResponse(msg string) KeywordOption               // override the halt response message
```

Pass options as variadic arguments to `NewKeywordGuard`. The previous
receiver methods `WithRegex`, `WithKeywordLogger`, and `WithResponse` have
been removed — use the functional option constructors instead.

```go
guard := guardrail.NewKeywordGuard(
    []string{"forbidden", "blocked"},
    guardrail.KeywordRegex(regexp.MustCompile(`(?i)secret\d+`)),
    guardrail.KeywordResponse("This content is not allowed."),
    guardrail.KeywordLogger(slog.Default()),
)
```

### `MaxToolCallsGuard` (PostProcessor)

Trims excess tool calls per LLM response. Keeps the first N calls silently.
This guard degrades gracefully — it does not halt.

```go
func NewMaxToolCallsGuard(max int) *MaxToolCallsGuard
```

### `CostGuard` (PreProcessor + PostProcessor)

Per-run, per-model spend ceiling. Reads cumulative token usage from the
run-scoped context (populated automatically by the agent loop) and prices it
against the injected pricing table. Halts when the total exceeds `maxUSD`.
Unknown models cost 0 (fail open). With no pricing table configured the guard
is inactive and logs a warning once at construction.

```go
func NewCostGuard(maxUSD float64, opts ...CostOption) *CostGuard

type CostOption func(*CostGuard)

func WithPricing(m map[string]core.ModelPricing) CostOption  // required — inject catalog.PricingMap() or a custom map
func WarnOnly() CostOption                                    // log instead of halting on budget exceeded
func CostResponse(msg string) CostOption                     // override the halt message (default: "Spending limit reached.")
func CostLogger(l *slog.Logger) CostOption
```

### `TokenBudgetGuard` (PreProcessor)

Trims the oldest non-system messages from a request until the heuristic token
estimate fits `maxTokens`. System messages and the N most recent messages are
never trimmed. Orphaned tool-result messages (whose originating tool call was
trimmed) are cleaned up automatically. A non-positive `maxTokens` disables
trimming.

```go
func NewTokenBudgetGuard(maxTokens int, opts ...TokenBudgetOption) *TokenBudgetGuard

type TokenBudgetOption func(*TokenBudgetGuard)

func WithEstimator(fn func([]core.ChatMessage) int) TokenBudgetOption  // plug in a real tokenizer
func PreserveLast(n int) TokenBudgetOption                              // protect n most recent messages (default: 1)
func TokenBudgetLogger(l *slog.Logger) TokenBudgetOption
```

Default estimator: ~1 token per 3 runes, padded hot (runs early). Each image or
PDF attachment counts as 2000 tokens.

### `RedactionGuard` (PreProcessor + PostProcessor + StreamProcessor)

Deterministic, zero-cost regex redaction on request messages, LLM response
content, and streamed text/thinking deltas. With no presets or rules configured
it is a no-op.

```go
func NewRedactionGuard(opts ...RedactionOption) *RedactionGuard

type RedactionOption func(*RedactionGuard)

func RedactPresets(names ...string) RedactionOption              // "pii", "secrets", "urls"
func RedactRule(kind string, re *regexp.Regexp) RedactionOption  // add a custom rule
func RedactStrategy(s Strategy) RedactionOption                  // default: StrategyRedact
func RedactPhases(p Phase) RedactionOption                       // default: PhaseBoth
func RedactPlaceholder(fn func(kind string) string) RedactionOption  // default: "[REDACTED:<kind>]"
func RedactLogger(l *slog.Logger) RedactionOption
```

**Strategy constants:**

```go
type Strategy int

const (
    StrategyRedact Strategy = iota // replace matches with placeholder (default)
    StrategyBlock                  // return *core.ErrHalt on first match
    StrategyWarn                   // log the match, pass through unchanged
)
```

**Phase constants:**

```go
type Phase int

const (
    PhaseBoth   Phase = iota // inspect input and output (default)
    PhaseInput               // request messages only
    PhaseOutput              // response content only
)
```

**Preset rule sets:**

| Preset | Patterns |
|---|---|
| `"pii"` | email, SSN (`NNN-NN-NNNN`), US phone, credit card (13–16 digits) |
| `"secrets"` | AWS access key (`AKIA…`), Bearer token, generic `api_key`/`secret`/`token` assignments |
| `"urls"` | `http://…` and `https://…` URLs |

**Streaming limitation:** `PostChunk` is stateless and per-chunk. A secret or
PII value split across two consecutive deltas is not redacted. For guaranteed
coverage, add a non-streaming `PostLLM` guard. `PostChunk` also does not cover
`EventObjectDelta`/`EventObjectFinish` snapshots from structured-output
responses — use a `PostProcessor` for those.

---

## Run-usage accessors (core package)

The agent loop seeds a per-run, per-model usage accumulator into the context at
run start. Processors (such as `CostGuard`) read from it to price accumulated
usage without any external storage.

```go
// import "github.com/nevindra/oasis/core"

// WithRunUsage returns a context carrying a fresh per-model usage accumulator.
// Called once by the agent loop at the start of each run.
func WithRunUsage(ctx context.Context) context.Context

// AddRunUsage adds one LLM call's usage under the given model. No-op if ctx
// has no accumulator (safe to call from outside the run loop).
func AddRunUsage(ctx context.Context, model string, u Usage) 

// RunUsageByModel returns a copy of the run's cumulative per-model usage, and
// false if no accumulator is present on ctx.
func RunUsageByModel(ctx context.Context) (map[string]Usage, bool)
```

Processors read accumulated usage via `RunUsageByModel`. The map key is the
model ID string (e.g., `"claude-opus-4-5"`, `"gpt-4o"`). The `Usage` struct:

```go
type Usage struct {
    InputTokens         int
    OutputTokens        int
    CachedTokens        int
    CacheCreationTokens int
}
```

---

## Rate limiting (provider-level)

Rate limiting applies to the provider, not to individual processors. It is
relevant here because it controls what enters the processor pipeline.

```go
func RateLimitMiddleware(opts ...RateLimitOption) provider.Middleware

func RPM(n int) RateLimitOption  // max requests per minute
func TPM(n int) RateLimitOption  // max tokens per minute (input + output, soft limit)
```

```go
limited := provider.Chain(oasis.RateLimitMiddleware(oasis.RPM(60), oasis.TPM(100_000)))(base)
```

`RateLimitMiddleware` wraps the provider and blocks the calling goroutine until
the rolling-window budget allows a new request. Context cancellation during a
wait returns `ctx.Err()`. Compose with other middleware via `provider.Chain`:

```go
limited := provider.Chain(
    agent.RetryMiddleware(),
    oasis.RateLimitMiddleware(oasis.RPM(60)),
)(base)
```
