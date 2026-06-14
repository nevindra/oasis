# Tools

## TL;DR

A tool is one thing an agent can do — call an API, fetch a URL, query a database, parse a file. You write a typed Go function; the LLM decides when to call it; Oasis handles schema derivation, dispatch, retries, result storage, and human approval gates.

## When to use it

- The agent needs to reach outside its context window: the web, a file, a database, a third-party API.
- The agent must take a side-effecting action: write a record, send a request, transform data.
- You have existing Go code that should be callable by the LLM during a run.
- You want to gate a destructive or sensitive action on human approval before it executes.
- **Reach for a built-in first.** `tools/http` handles URL fetching; `tools/data` handles CSV/JSON/JSONL processing. Write a custom `Tool[In, Out]` only when a built-in does not cover your operation.

## Architecture

```mermaid
sequenceDiagram
    participant LLM
    participant Loop as Agent run loop
    participant Approval as Approval gate
    participant Policy as ToolPolicy
    participant MW as User middleware
    participant T as Tool.Execute
    participant Store as ToolResultStore

    LLM->>Loop: response with tool_calls[]
    Loop->>Loop: look up AnyTool by name (O(1))
    par for each tool call concurrently
        Loop->>Approval: intercept if WithToolApproval
        Approval-->>Loop: approved / denied
        Loop->>Policy: run with timeout + retry budget
        Policy->>MW: ExecuteRaw(ctx, args JSON)
        MW->>T: Execute(ctx, typed In)
        T-->>MW: (Out, error)
        MW-->>Policy: ToolResult
        Policy->>Policy: retry if RetryableError + budget > 0
        Policy-->>Loop: ToolResult
        Loop->>Store: Put(content) if oversized
        Store-->>Loop: reference id
    end
    Loop-->>LLM: tool results batch (all calls)
```

The loop collects all tool calls from a single LLM response and dispatches them concurrently — each call goes through the same pipeline: name lookup, approval check, policy wrapping, user middleware, `Execute`, optional store write. All results land back in the conversation in one batch before the LLM's next turn. There is no partial flush; the LLM sees nothing until every concurrent call completes.

The wrapping order is fixed and intentional. The approval gate sits outermost so it fires once per logical call regardless of retry count. The `ToolPolicy` wrapper sits just inside approval, so each retry attempt carries a fresh timeout. User middleware sits innermost, closest to `Execute`, so it observes each actual attempt — useful for per-attempt logging and timing. This layering means you cannot reorder approval and retry by configuration; the layering is a correctness guarantee, not an accident.

## Mental model

**`Tool[In, Out]` is the typed authoring interface.** You define an input struct (`In`) and an output type (`Out`), implement two methods — `Definition()` for name and description, `Execute` for the work — and that is your tool. Oasis reads `In`'s struct field tags (`json`, `describe`, `enum`) by reflection to build the JSON Schema shown to the LLM. This happens once inside `Erase`, not on every dispatch.

**`Erase` converts to `AnyTool`.** The run loop holds a `[]AnyTool` — a heterogeneous list whose elements have different concrete `In`/`Out` types. `Erase[In, Out](t)` wraps your typed tool in a thin adapter that speaks `json.RawMessage` at both ends. After `Erase`, dispatch is a map lookup and a JSON round-trip. Call `Erase` once at startup, not on every request.

**Two kinds of failure.** `Tool.Execute` returns `(Out, error)`. The Go `error` is for infrastructure failures — a dropped connection, a context cancellation, anything the retry policy should act on. Business failures — "city not found", "quota exceeded", "invalid input" — belong in `ToolResult.Error`, a plain string fed back to the LLM verbatim. The LLM reads that string and adapts. Never return a business failure as a Go `error`; the framework treats Go errors as infrastructure and may retry them.

**Middleware composes at build time.** A `ToolMiddleware` is `func(AnyTool) AnyTool`. You supply a list in `agent.ToolConfig.Middleware` via `oasis.WithToolConfig`; Oasis applies them in order (first = innermost) to every registered tool when the agent is constructed. Logging, timing, OpenTelemetry spans, result redaction — all implemented as middleware, all wired once, all zero overhead per-call once built.

**Streaming tools emit progress events.** A tool that implements `StreamingTool[In, Out]` can push intermediate `StreamEvent` values over a `chan<- StreamEvent` while `Execute` is still running. These events surface in the agent's stream sink in real time — useful for long-running operations where you want the user to see partial output immediately. Register with `EraseStreaming` instead of `Erase`. Note that streaming tools bypass `ToolPolicy` entirely: retrying a partially-streamed call would duplicate events, so the policy wrapper is not applied.

**`ToolResult` is the framework's internal envelope.** `Content` holds the successful result as a `string`. `Error` holds a business failure as a plain string. `Attachments` holds multimodal payloads — images, PDFs — that should appear in the LLM's next turn. `Content` and `Error` are mutually exclusive by convention: a result is either successful or failed, never both. `Attachments` can accompany either. For plain text results use `core.TextResult`; for structured JSON use `core.JSONResult` (marshals any value); for pre-encoded JSON bytes use `core.JSONContent` (returns the `string` for `Content`).

**Parallel dispatch is the default.** When the LLM emits multiple tool calls in one response, the loop dispatches them concurrently. `Execute` must be safe for concurrent calls. If your tool holds shared mutable state, protect it with a mutex. If two tools must not run at the same time, enforce that constraint at the application level — the framework does not provide a tool-level serialization primitive.

## How it works step by step

1. LLM responds with a `tool_calls` array, each entry naming a tool and providing a JSON argument object.
2. The loop iterates the array. For each call, it looks up the registered `AnyTool` by name (O(1) map lookup). Unknown names produce a `ToolResult.Error` without calling your code.
3. Pre-tool processors run (e.g., guardrails). If a processor halts, the call does not reach the tool.
4. The approval gate (if configured via `agent.ToolConfig.Approvals`) pauses the call, surfaces the prompt to the `InputHandler`, and waits for human confirmation. A denial writes `ToolResult.Error` or halts the run.
5. The `ToolPolicy` wrapper starts a per-attempt timer. If `Timeout` is set, a child context with that deadline wraps the rest of the chain.
6. User middleware runs outermost-first, innermost-last. Each middleware calls the next in the chain and wraps the result.
7. `AnyTool.ExecuteRaw` deserializes the JSON arguments into `In`, calls your `Tool.Execute`, and marshals `Out` to a JSON string in `ToolResult.Content` on success, or copies `err.Error()` to `ToolResult.Error` on non-nil `error`.
8. The innermost middleware returns. Each outer middleware layer post-processes in reverse order (e.g., the timing middleware records elapsed time here).
9. If `Execute` returned a `core.RetryableError` and the policy has retries remaining, the policy wrapper backs off and loops back to step 5 with a fresh attempt timer.
10. If `ToolResultStore` is configured and the result exceeds `Limits.MaxToolResultLen`, the loop calls `Store.Put`, replaces the content with a reference ID, and registers `read_full_result` (a built-in tool) so the LLM can page through the full output.
11. Post-tool processors run on the final `ToolResult`. Processors can inspect or mutate the result before it enters history — e.g., a content filter that redacts PII before the LLM sees the output.
12. The result is appended to the conversation history as a `tool` role message keyed by the tool call ID.
13. Once all concurrent tool calls finish, the full batch is sent to the LLM in a single request. The LLM sees every result from this turn at once; there is no partial send.

## Tool result store / artifact pattern

Large outputs — think full webpage text, big JSON documents, hundreds of CSV rows — would bloat the context window if sent verbatim. The `ToolResultStore` solves this: when a result exceeds the configured length limit, the loop stores the raw bytes, gives the LLM a short reference ID, and auto-registers the `read_full_result` built-in tool so the LLM can page through the content on demand.

Opt in by providing a `ResultStore` in `agent.ToolConfig` at agent construction:

```go
agent.New(provider,
    oasis.WithTools(myTool),
    oasis.WithToolConfig(agent.ToolConfig{
        ResultStore: core.NewInMemoryToolResultStore(
            core.WithToolResultMaxBytes(20 * 1024 * 1024), // 20 MiB cap
            core.WithToolResultTTL(10 * time.Minute),
        ),
    }),
)
```

Set `ResultStore: nil` and `ResultStoreExplicit: true` in `agent.ToolConfig` to disable paging entirely (oversized results get a truncation marker instead).

The default store is in-memory (10 MiB, 5-minute TTL, FIFO eviction). Configure it with:
- `WithToolResultMaxBytes(n)` — total byte cap across all entries; FIFO eviction on overflow.
- `WithToolResultMaxEntries(n)` — entry count cap; FIFO eviction on overflow.
- `WithToolResultTTL(d)` — per-entry expiry duration.

When a stored result is referenced in the conversation, the auto-registered `read_full_result` tool accepts a result ID plus `offset` and `length` parameters. The LLM can page through the full content by calling this tool multiple times with advancing offsets — all transparently, without any application code.

For multi-agent or persistent scenarios, provide your own `ToolResultStore` implementation. The interface is simple: `Put(ctx, content) (id, error)` and `Get(ctx, id, offset, length) (content, total, error)`. The store must be safe for concurrent use.

## Common patterns and gotchas

**Business failures go in `ToolResult.Error`, not Go `error`.** Returning `fmt.Errorf("city not found")` from `Execute` sends the message back to the LLM — which is correct. But it also looks like an infrastructure error to `ToolPolicy`, which may retry it. For permanent domain failures, return the error normally and know that the framework will not retry a plain `error` unless it is wrapped with `core.RetryableError`.

**Parallel dispatch means `Execute` must be concurrency-safe.** The loop may call the same registered tool instance multiple times simultaneously (two tool calls in the same LLM response). If your tool has mutable fields, protect them with a `sync.Mutex`, or make the tool stateless.

**`ToolPolicy` retry vs. middleware retry.** `ToolPolicy` retries the full middleware chain — logging, timing, the whole stack — for each attempt. If you write a retry loop inside a middleware, you get nested retries. Pick one layer: use `ToolPolicy` for the general case, middleware retry only when you need custom retry logic invisible to the policy.

**Human approval sits outermost.** Configuring `agent.ToolConfig.Approvals` adds an approval gate outside `ToolPolicy`. This is intentional: if the policy retries a denied call, the human would be prompted again. The current design prompts once per logical call, then the policy retries internally if the attempt fails after approval.

**Middleware must preserve `StreamingAnyTool`.** If your middleware wraps a streaming tool, check whether the inner tool implements `core.StreamingAnyTool` and forward `ExecuteStream` if so. Middleware that drops the streaming interface silently falls back to non-streaming dispatch.

## Quick example

```go
import (
    "context"
    "fmt"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
)

// In: field tags drive the JSON Schema shown to the LLM.
type WeatherInput struct {
    City string `json:"city" describe:"City name to look up weather for"`
}

type WeatherTool struct{}

func (w *WeatherTool) Definition() oasis.ToolMeta {
    return oasis.ToolMeta{
        Name:        "get_weather",
        Description: "Return the current weather for a city. Use for location-specific forecasts.",
    }
}

func (w *WeatherTool) Execute(ctx context.Context, in WeatherInput) (string, error) {
    if in.City == "" {
        return "", fmt.Errorf("city is required") // goes to ToolResult.Error
    }
    return "Sunny, 24 C", nil
}

// Compile-time check.
var _ oasis.Tool[WeatherInput, string] = (*WeatherTool)(nil)

func main() {
    // Erase once at startup — schema derived here, not per call.
    tool := oasis.Erase[WeatherInput, string](&WeatherTool{})

    a := agent.New(provider,
        oasis.WithTools(tool),
    )
    _ = a
}
```

Walkthrough:
- `WeatherInput` fields with `json` tags become LLM-visible parameters; `describe` sets each field's description.
- `Definition()` supplies the name (the LLM calls the tool by this string) and the description (the LLM reads this to decide when to use it).
- `Execute` returns `(string, error)`. A non-nil error is copied into `ToolResult.Error` by `Erase` and sent back to the LLM verbatim.
- `Erase` is the one-time registration call. JSON Schema derivation happens here, not on each dispatch.
- `WithTools` registers the erased tool with the agent. Call it multiple times or pass multiple tools in one call.

## Built-in tools

Oasis ships two built-in tool packages. Import and register them the same way as any custom tool.

### `tools/http` — `http_fetch`

Fetches a URL and returns its readable text content (up to 8,000 characters). Uses `go-readability` for article extraction, with a plain HTML-strip fallback. Hard timeout: 15 seconds.

```go
import (
    oasis    "github.com/nevindra/oasis"
    toolhttp "github.com/nevindra/oasis/tools/http"
)

a := agent.New(provider,
    oasis.WithTools(oasis.Erase[toolhttp.FetchInput, string](toolhttp.New())),
)
```

`FetchInput` has one field: `URL string`. The LLM supplies the URL; the tool returns human-readable text.

### `tools/data` — four CSV/JSON/JSONL tools

Four atomic tools for structured data processing without shelling out:

| Tool name | What it does |
|-----------|-------------|
| `data_parse` | Parse raw CSV, JSON array, or JSONL into records |
| `data_filter` | Filter records by column equality or range conditions |
| `data_aggregate` | Sum, avg, count, min, max over a numeric column |
| `data_transform` | Rename columns, reorder fields, drop columns |

```go
import "github.com/nevindra/oasis/tools/data"

a := agent.New(provider,
    oasis.WithTools(data.New()...), // returns []oasis.AnyTool, already erased
)
```

`data.New()` returns all four tools pre-erased. Pass them in a single `WithTools` call using the `...` spread.

## Next

- [API reference](./api.md)
- [Examples](./examples.md)
