# Processors API Reference

All processor types and helpers are available from the root `oasis` package.
Guardrail constructors and options also live there. The `ProcessorChain` helper
is in `github.com/nevindra/oasis/processor` (re-exported as
`oasis.ProcessorChain`). `InputHandler` and related types live in
`github.com/nevindra/oasis/agent`.

---

## Core interfaces

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
type ProcessorChain  // oasis.ProcessorChain = processor.Chain

func NewProcessorChain() *ProcessorChain

func (c *ProcessorChain) AddPre(p PreProcessor)
func (c *ProcessorChain) AddPost(p PostProcessor)
func (c *ProcessorChain) AddPostTool(p PostToolProcessor)

func (c *ProcessorChain) RunPreLLM(ctx context.Context, req *ChatRequest) error
func (c *ProcessorChain) RunPostLLM(ctx context.Context, resp *ChatResponse) error
func (c *ProcessorChain) RunPostTool(ctx context.Context, call ToolCall, result *ToolResult) error

func (c *ProcessorChain) Len() int
```

`AddPre/Post/PostTool` bucket processors by interface at registration time so
`RunPreLLM/RunPostLLM/RunPostTool` have no per-call type assertions. `Len`
counts total registrations across all stages.

---

## InputHandler (agent package)

```go
// import "github.com/nevindra/oasis/agent"

type InputHandler interface {
    RequestInput(ctx context.Context, req InputRequest) (InputResponse, error)
}

type InputRequest struct {
    Question string
    Options  []string            // empty = free-form
    Metadata map[string]string   // agent name, tool being approved, etc.
}

type InputResponse struct {
    Value string
}

func InputHandlerFromContext(ctx context.Context) (InputHandler, bool)
func WithInputHandlerContext(ctx context.Context, h InputHandler) context.Context
```

`InputHandler` bridges the agent to your communication channel (HTTP, CLI,
Telegram, etc.). `RequestInput` must block until a response arrives or `ctx` is
cancelled. `InputHandlerFromContext` retrieves the handler from the context that
processors receive — useful for approval-gate processors that want to route
through the same handler.

---

## Guardrails

All four are in `github.com/nevindra/oasis/guardrail` and re-exported from
`oasis`.

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
func NewKeywordGuard(keywords ...string) *KeywordGuard

func (g *KeywordGuard) WithRegex(patterns ...*regexp.Regexp) *KeywordGuard
func (g *KeywordGuard) WithResponse(msg string) *KeywordGuard
func (g *KeywordGuard) WithKeywordLogger(l *slog.Logger) *KeywordGuard
```

`WithRegex`, `WithResponse`, and `WithKeywordLogger` return the guard for
builder-style chaining.

### `MaxToolCallsGuard` (PostProcessor)

Trims excess tool calls per LLM response. Keeps the first N calls silently.
This guard degrades gracefully — it does not halt.

```go
func NewMaxToolCallsGuard(max int) *MaxToolCallsGuard
```

---

## Rate limiting (provider-level)

Rate limiting applies to the provider, not to individual processors. It is
relevant here because it controls what enters the processor pipeline.

```go
func WithRateLimit(p Provider, opts ...RateLimitOption) Provider

func RPM(n int) RateLimitOption  // max requests per minute
func TPM(n int) RateLimitOption  // max tokens per minute (input + output, soft limit)
```

```go
provider = oasis.WithRateLimit(provider, oasis.RPM(60), oasis.TPM(100_000))
```

`WithRateLimit` wraps the provider and blocks the calling goroutine until the
rolling-window budget allows a new request. Context cancellation during a wait
returns `ctx.Err()`. Composes with `WithRetry`:

```go
provider = oasis.WithRateLimit(oasis.WithRetry(provider), oasis.RPM(60))
```
