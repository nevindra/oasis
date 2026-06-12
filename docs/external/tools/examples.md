# Tools Examples

## Recipe 1: Build a custom tool

**Goal:** Implement a typed tool and register it with an agent.

```go
package main

import (
    "context"
    "fmt"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
)

// Input fields become JSON Schema properties. The `describe` tag sets the
// description shown to the LLM. The `enum` tag restricts valid values.
type SearchInput struct {
    Query  string `json:"query"  describe:"Search query string"`
    Limit  int    `json:"limit,omitempty" describe:"Max results (default 5)"`
    Source string `json:"source,omitempty" enum:"web,docs,internal"`
}

type SearchResult struct {
    Items []string `json:"items"`
    Total int      `json:"total"`
}

type SearchTool struct{}

func (s *SearchTool) Definition() oasis.ToolMeta {
    return oasis.ToolMeta{
        Name:        "search",
        Description: "Search for information. Use for factual lookups and research.",
    }
}

func (s *SearchTool) Execute(ctx context.Context, in SearchInput) (SearchResult, error) {
    if in.Query == "" {
        // Business failure: return as error; Erase puts it in ToolResult.Error.
        return SearchResult{}, fmt.Errorf("query is required")
    }
    // … actual search logic …
    return SearchResult{Items: []string{"result1"}, Total: 1}, nil
}

// Compile-time check: SearchTool implements Tool[SearchInput, SearchResult].
var _ oasis.Tool[SearchInput, SearchResult] = (*SearchTool)(nil)

func main() {
    provider := /* your provider */
    a := agent.New(provider,
        oasis.WithTools(oasis.Erase[SearchInput, SearchResult](&SearchTool{})),
    )
    _ = a
}
```

**Plain-English walkthrough:**
- `SearchInput` is a plain struct. Each field with a `json` tag becomes an LLM-visible parameter. `describe` sets the description; `enum` produces a JSON Schema enum constraint.
- `Definition()` supplies the name (how the LLM calls the tool) and the description (when the LLM should call it). Good descriptions say what the tool does and when to use it.
- `Execute` gets the already-deserialized `SearchInput`. Return `(zero, err)` for any domain failure — `Erase` converts `err.Error()` into `ToolResult.Error` automatically.
- The `var _ oasis.Tool[...]` line is a compile-time interface check — it costs nothing at runtime.
- `Erase` is called once at startup. After that, the schema is fixed and dispatch is O(1).

**Variations:**
- Return a struct for `Out` instead of `string` — the LLM gets structured JSON it can reason about.
- Use `oasis.EraseStreaming` if you want to emit progress events via a `chan<- StreamEvent`.
- Pass multiple tools in one `WithTools(a, b, c)` call.

---

## Recipe 2: Add logging and timing middleware

**Goal:** Observe every tool call without modifying each tool's implementation.

```go
import (
    "log/slog"
    "os"
    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
)

logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

a := agent.New(provider,
    oasis.WithTools(myTool1, myTool2),
    oasis.WithToolMiddleware(
        oasis.LoggingMiddleware(logger),   // innermost: closest to tool
        oasis.TimingMiddleware(),           // outermost: wraps logging
    ),
)
```

**Plain-English walkthrough:**
- `WithToolMiddleware` applies the same chain to every registered tool at build time.
- First entry in the list is innermost — `LoggingMiddleware` sees each attempt; `TimingMiddleware` measures total elapsed time including any retry overhead.
- Both are no-ops when `logger` is nil / when timing output isn't needed.

**Variations:**
- Write a `TransformMiddleware` to redact sensitive fields in `ToolResult.Content` before they reach the LLM conversation history.
- Compose `OTelSpanMiddleware(tracer)` to emit OpenTelemetry spans per tool call.
- Custom middleware: implement a function of type `func(AnyTool) AnyTool` and pass it directly.

---

## Recipe 3: Per-tool retry policy with exponential backoff

**Goal:** Retry a flaky external API call without hard-coding sleep logic in the tool.

```go
import (
    "fmt"
    "time"
    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/core"
)

type WeatherInput struct {
    City string `json:"city"`
}

type WeatherTool struct{}

func (w *WeatherTool) Definition() oasis.ToolMeta {
    return oasis.ToolMeta{Name: "get_weather", Description: "Fetch current weather."}
}

func (w *WeatherTool) Execute(ctx context.Context, in WeatherInput) (string, error) {
    data, err := callWeatherAPI(ctx, in.City)
    if err != nil {
        if isTransient(err) {
            // Mark the error retryable so the ToolPolicy will retry it.
            return "", oasis.RetryableError(fmt.Errorf("weather API: %w", err))
        }
        return "", err // not retryable; goes to LLM as ToolResult.Error
    }
    return data, nil
}

a := agent.New(provider,
    oasis.WithTools(oasis.Erase[WeatherInput, string](&WeatherTool{})),
    oasis.WithToolPolicy("get_weather", oasis.ToolPolicy{
        Timeout:       3 * time.Second, // per-attempt timeout
        Retries:       2,               // up to 3 total attempts
        RetryDelay:    500 * time.Millisecond,
        MaxRetryDelay: 5 * time.Second,
    }),
)
```

**Plain-English walkthrough:**
- `RetryableError` wraps the error so the policy recognizes it as retryable. Plain `fmt.Errorf` errors are not retried — they go straight to `ToolResult.Error`.
- `ToolPolicy.Timeout` is the per-attempt deadline. If the third attempt also times out, the final error lands in `ToolResult.Error`.
- `WithToolPolicy("get_weather", ...)` binds the policy to the exact tool name.
- The backoff formula: `RetryDelay << attempt`, capped at `MaxRetryDelay`. Attempt 0 → 500ms, attempt 1 → 1s, attempt 2 → 2s.

**Variations:**
- Use `WithToolPolicyMatch(func(name string) bool { return strings.HasPrefix(name, "mcp__") }, policy)` to apply one policy to a whole family of tools by name prefix.
- Compose `DefaultRetryOn` in a custom predicate: `RetryOn: func(err error) bool { return oasis.DefaultRetryOn(err) || errors.Is(err, myErr) }`.

---

## Recipe 4: Human approval before a destructive tool runs

**Goal:** Pause the agent and ask a human to confirm before executing a write operation.

```go
import (
    "context"
    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
)

type DeleteInput struct {
    RecordID string `json:"record_id" describe:"ID of the record to delete"`
}

type DeleteTool struct{}

func (d *DeleteTool) Definition() oasis.ToolMeta {
    return oasis.ToolMeta{Name: "delete_record", Description: "Delete a record permanently."}
}
func (d *DeleteTool) Execute(ctx context.Context, in DeleteInput) (string, error) {
    // … delete logic …
    return "deleted", nil
}

a := agent.New(provider,
    oasis.WithTools(oasis.Erase[DeleteInput, string](&DeleteTool{})),
    oasis.WithInputHandler(myHumanInputHandler), // required for approval gates
    oasis.WithToolApproval("delete_record",
        oasis.ApprovalPrompt(func(call oasis.ToolCall) string {
            return fmt.Sprintf("Agent wants to delete record %s. Approve?", call.Args)
        }),
        oasis.OnDeny(oasis.DenyAskLLMToRevise), // LLM gets to try another approach
    ),
)
```

**Plain-English walkthrough:**
- `WithInputHandler` provides the channel through which the approval question reaches a human (CLI, Slack, web UI — whatever your `InputHandler` implementation does).
- `WithToolApproval` adds an outermost middleware that intercepts calls to `delete_record`, sends the prompt to the `InputHandler`, and waits for `"approve"` or `"deny"`.
- `ApprovalPrompt` customizes the question; the default is `"Approve call to <name>?"`.
- `DenyAskLLMToRevise` puts a `ToolResult.Error` back in the conversation so the LLM knows it was denied and can propose a different action. Use `DenyHalt` if denial must stop the run entirely.
- The approval wrapper sits outermost so retries (if any policy is configured) do not re-prompt the human.

**Variations:**
- Gate multiple tools by calling `WithToolApproval` once per tool name.
- Use `DenyHalt` for compliance-mandated stops where continuing after a denial is not acceptable.

---

## Recipe 5: Use the built-in HTTP fetch tool

**Goal:** Give the agent the ability to read web pages.

```go
import (
    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    toolhttp "github.com/nevindra/oasis/tools/http"
)

fetchTool := toolhttp.New()  // 15-second timeout, readability extraction

a := agent.New(provider,
    oasis.WithTools(oasis.Erase[toolhttp.FetchInput, string](fetchTool)),
)
```

**Plain-English walkthrough:**
- `toolhttp.New()` returns a `*Tool` that satisfies `oasis.Tool[FetchInput, string]`.
- `FetchInput` has one field: `URL string`. The LLM supplies the URL; the tool fetches and extracts readable text (up to 8,000 characters).
- `Erase` wraps it once. The tool name exposed to the LLM is `http_fetch`.

**Variations:**
- Use `toolhttp.Tool.Fetch(ctx, url)` directly in your own tool implementation if you want to embed URL fetching as one step in a larger operation.
- Add `LoggingMiddleware` to observe every URL the agent accesses.

## Generative UI: render a component instead of text

A tool can return a UI component descriptor instead of plain text. The agent
emits an `EventUIComponent` directly after the tool result; a frontend maps the
component `Name` to a renderer and validates `Props`.

```go
// Helper path — for func/RawTool/hand-rolled AnyTool:
func searchFlights(ctx context.Context, in FlightQuery) (core.ToolResult, error) {
    return core.UIResult("FlightCard", lookup(in)), nil
}

// Interface path — a typed Tool[In, Out] whose Out opts in:
type FlightResults struct {
    Flights []Flight `json:"flights"`
}

func (FlightResults) UIComponent() string { return "FlightCard" } // implements core.UIRenderable
// Erase detects UIRenderable and sets ToolResult.UI automatically.
```

**Plain-English walkthrough:**
- `core.UIResult(name, props)` marshals `props` to JSON and sets `ToolResult.UI`
  (it also mirrors the JSON into `Content` so the LLM still "sees" the rendered
  data and the loop can continue).
- Alternatively, a typed tool's `Out` type implements `core.UIRenderable` (one
  method, `UIComponent() string`); `Erase`/`EraseStreaming` detect it and set
  `ToolResult.UI` for you — no helper call needed.
- On the wire the agent emits, in order: `EventToolCallResult` then
  `EventUIComponent{ID: <call id>, Name: "FlightCard", Object: <props json>}`.

**Variations:**
- The component `Name` is just a registry key — the frontend owns the catalog
  of renderers and decides how to validate `Props` and what to do on a miss.
- All four symbols are re-exported on the root umbrella: `oasis.UIResult`,
  `oasis.UIComponent`, `oasis.UIRenderable`, `oasis.EventUIComponent`.
