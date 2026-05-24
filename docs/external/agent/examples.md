# Agent Examples

---

## Recipe 1: Minimal agent — no tools, no memory

**Goal:** Run a single-turn LLM call and print the answer.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")
    ag := oasis.NewLLMAgent("helper", "A helpful assistant", llm,
        oasis.WithPrompt("You are a concise assistant. Respond in one sentence."),
    )

    result, err := ag.Execute(context.Background(), oasis.AgentTask{
        Input: "What is the capital of Indonesia?",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
}
```

**Plain-English walkthrough:** `NewLLMAgent` takes a name, a description, a provider,
and any number of options. `WithPrompt` sets the system prompt that the LLM receives
on every call. Because no tools are registered, the loop runs once and returns the
model's first text response.

**Variations:**
- Omit `WithPrompt` entirely for a promptless agent.
- Add `oasis.WithGeneration(oasis.Generation{Temperature: oasis.Ptr(0.2)})` to reduce
  randomness.
- Wrap the provider with `oasis.WithRetry(llm)` to add automatic retry on 429/503.

---

## Recipe 2: Agent with tools

**Goal:** Give the agent a custom tool and let the LLM decide when to call it.

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
)

// weatherTool is a hand-rolled tool that returns a canned weather report.
type weatherTool struct{}

func (w *weatherTool) Name() string { return "get_weather" }

func (w *weatherTool) Definition() core.ToolDefinition {
    return core.ToolDefinition{
        Name:        "get_weather",
        Description: "Returns the current weather for a city.",
        Parameters:  core.DeriveSchema[weatherArgs](),
    }
}

type weatherArgs struct {
    City string `json:"city" describe:"The city name"`
}

func (w *weatherTool) ExecuteRaw(ctx context.Context, args json.RawMessage) (core.ToolResult, error) {
    var a weatherArgs
    _ = json.Unmarshal(args, &a)
    return core.TextResult(fmt.Sprintf("It is sunny in %s, 28°C.", a.City)), nil
}

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")
    ag := oasis.NewLLMAgent("weather-bot", "Answers weather questions", llm,
        oasis.WithPrompt("You are a weather assistant."),
        oasis.WithTools(&weatherTool{}),
    )

    result, err := ag.Execute(context.Background(), oasis.AgentTask{
        Input: "What's the weather like in Bali?",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
    fmt.Printf("Tool calls: %d\n", len(result.Steps))
}
```

**Plain-English walkthrough:** `WithTools` registers one or more tools. The LLM sees
their `Definition()` schemas and decides when to call them. Each call goes through
`ExecuteRaw`; the returned `ToolResult` is fed back into the conversation history.
`result.Steps` records every tool call with its input, output, and duration.

`core.TextResult(s)` is the shorthand for wrapping a plain string as a `ToolResult`.
For JSON output use `core.JSONContent(bytes)`. Business errors (e.g. city not found)
go in `ToolResult.Error` — never return a Go error for tool-level failures.

**Variations:**
- Register multiple tools — the LLM can call any combination in any order.
- Use `oasis.WithToolPolicy("get_weather", core.ToolPolicy{Timeout: 5*time.Second, Retries: 2})`
  to add per-tool timeout and retry.
- Use `oasis.WithPlanExecution()` so the LLM can batch multiple tool calls in one
  shot with the built-in `execute_plan` tool.

---

## Recipe 3: Streaming to an HTTP endpoint

**Goal:** Pipe the agent's output to a browser as Server-Sent Events.

```go
package main

import (
    "context"
    "net/http"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")
    ag := oasis.NewLLMAgent("streamer", "Streams text to the browser", llm)

    http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
        task := oasis.AgentTask{Input: r.URL.Query().Get("q")}
        oasis.ServeSSE(r.Context(), w, ag, task)
    })
    http.ListenAndServe(":8080", nil)
}
```

**Plain-English walkthrough:** `ServeSSE` handles all the SSE plumbing: it sets the
`Content-Type: text/event-stream` header, creates a channel, runs the agent in a
background goroutine, and flushes each event to the response writer. When the browser
disconnects, `r.Context()` cancels and the agent stops cleanly.

**Variations:**
- For a custom SSE loop, call `ExecuteStream` yourself and write events with
  `oasis.WriteSSEEvent(w, string(ev.Type), ev)`.
- For multi-reader fan-out (websockets, logs, analytics), use `oasis.StartStream`
  and call `stream.Events()` from each consumer goroutine.

---

## Recipe 4: Multi-reader streaming with the Stream wrapper

**Goal:** Watch text deltas in real time while also capturing the final result.

```go
ctx := context.Background()
ag := oasis.NewLLMAgent("writer", "Writes stories", llm)
task := oasis.AgentTask{Input: "Write a haiku about rain."}

stream := oasis.StartStream(ctx, ag, task)

// Register a callback for each text chunk — fires on the dispatcher goroutine.
stream.OnTextDelta(func(chunk string) {
    fmt.Print(chunk) // print incrementally
})

// Block until the run finishes and get the assembled result.
result, err := stream.Result()
if err != nil {
    panic(err)
}
fmt.Printf("\n\nToken usage: %+v\n", result.Usage)
```

**Plain-English walkthrough:** `StartStream` launches the agent in the background
immediately and returns a `*Stream`. `OnTextDelta` registers a lightweight callback
invoked for each `EventTextDelta` event; callbacks run on the dispatcher goroutine so
keep them fast. `stream.Result()` blocks until the agent finishes and returns the full
`AgentResult`. You can call `stream.Text()` as a shorthand if you only need the output
string.

**Variations:**
- Subscribe via `stream.Events()` to receive a channel and filter events yourself.
- Use `stream.OnToolCall(fn)` to watch tool calls fire in real time.
- Call `stream.FinishReason()` to check why the loop stopped.

---

## Recipe 5: Per-call overrides with RunOptions

**Goal:** Use one agent but vary the prompt, tools, or limits per request.

```go
ag := oasis.NewLLMAgent("multi", "Configurable assistant", llm,
    oasis.WithTools(defaultTools...),
    oasis.WithPrompt("Default system prompt."),
)

// High-priority request: stricter limits, different prompt.
result, err := ag.ExecuteWith(ctx, oasis.AgentTask{Input: "Summarize this doc"},
    &oasis.RunOptions{
        Prompt: oasis.Ptr("You are a summarizer. Be concise."),
        Limits: &oasis.Limits{MaxIter: 5, MaxToolResultLen: 10_000},
    },
)
```

**Plain-English walkthrough:** `ExecuteWith` applies the `RunOptions` on top of the
agent's base configuration for this call only. `Prompt` replaces the static prompt
just for this request. `Limits` uses partial-override semantics — only the fields you
set change; unset fields keep the agent's defaults. The base agent is not mutated.

**Variations:**
- Pass `Tools: []oasis.AnyTool{}` (empty slice, not nil) to give a request no tools
  at all.
- Pass `Generation: &oasis.Generation{Temperature: oasis.Ptr(0.0)}` for deterministic
  output on a specific request.
- Combine with `ExecuteStreamWith` for per-call streaming + overrides.

---

## Recipe 6: Human-in-the-loop via suspend/resume

**Goal:** Pause the agent mid-run to ask a human for confirmation, then continue.

```go
// An InputHandler bridges the agent's ask_user tool to your chat channel.
type cliHandler struct{}

func (h *cliHandler) RequestInput(ctx context.Context, req oasis.InputRequest) (oasis.InputResponse, error) {
    fmt.Println("Agent asks:", req.Question)
    var ans string
    fmt.Scan(&ans)
    return oasis.InputResponse{Value: ans}, nil
}

ag := oasis.NewLLMAgent("confirmer", "Asks before acting", llm,
    oasis.WithInputHandler(&cliHandler{}),
    oasis.WithTools(dangerousTool),
)

result, err := ag.Execute(ctx, oasis.AgentTask{Input: "Delete all temp files"})

var suspended *oasis.ErrSuspended
if errors.As(err, &suspended) {
    // Show the payload to the user, collect their response.
    fmt.Printf("Suspended: %s\n", suspended.Payload)
    fmt.Print("Continue? (yes/no): ")
    var ans string
    fmt.Scan(&ans)

    result, err = suspended.Resume(ctx, json.RawMessage(`"`+ans+`"`))
}
```

**Plain-English walkthrough:** `WithInputHandler` adds an `ask_user` built-in tool.
When a processor or tool calls `oasis.Suspend(payload)`, `Execute` returns
`*ErrSuspended` instead of a normal result. The `Payload` field carries whatever
context was passed to `Suspend` — show it to your user. Call `suspended.Resume(ctx,
data)` to continue where it left off; `data` is the user's response injected as a
user-role message into the conversation.

Set `suspended.WithSuspendTTL(5 * time.Minute)` after catching the error to
automatically free the snapshot if the user never responds.

**Variations:**
- Use `suspended.ResumeStream(ctx, data, ch)` for streaming resume.
- Use `oasis.NewSuspendProtocol[Req, Resp]("tag")` for typed suspend/resume flows
  instead of raw `json.RawMessage`.
- Call `suspended.Release()` when the user abandons the session.

---

## Recipe 7: Tool logging and transformation middleware

**Goal:** Log every tool call and redact sensitive fields from results.

```go
import "log/slog"

logger := slog.Default()

ag := oasis.NewLLMAgent("secure-agent", "Logs and redacts tool output", llm,
    oasis.WithTools(myTools...),
    oasis.WithToolMiddleware(
        oasis.LoggingMiddleware(logger),
        oasis.TransformMiddleware(func(name string, r oasis.ToolResult) oasis.ToolResult {
            if name == "get_user_profile" {
                // Redact the email field before the LLM sees the result.
                r.Content = redactEmail(r.Content)
            }
            return r
        }),
    ),
)
```

**Plain-English walkthrough:** `WithToolMiddleware` wraps every registered tool in the
given chain. `LoggingMiddleware` sits innermost (closest to the tool) and logs
`tool.start` and `tool.finish` at `slog.Info`. `TransformMiddleware` sits outermost
and can inspect or rewrite the `ToolResult` before it reaches the LLM. The ordering
rule: first in the list = innermost wrap.

**Variations:**
- Add `oasis.TimingMiddleware()` for lightweight `slog.Debug` duration logs.
- Use `oasis.OTelSpanMiddleware(tracer)` for OpenTelemetry spans per tool call (or
  let it auto-wire by passing `oasis.WithTracer(t)` to the agent).
- `TransformMiddleware` is never called when the inner tool returned a Go error, so
  you don't need to guard for nil content.

---

## Recipe 8: Time-based dispatch (scheduler pattern)

**Goal:** Fire an agent on a schedule using `ScheduledAction` records in the store.

```go
// ScheduledAction is a persistence type. Your app creates and reads these via Store.
// The agent itself has no built-in clock — scheduling is always in user code.

func runScheduler(ctx context.Context, store oasis.Store, ag *oasis.LLMAgent) {
    ticker := time.NewTicker(1 * time.Minute)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            actions, err := store.GetDueScheduledActions(ctx, time.Now().Unix())
            if err != nil {
                continue
            }
            for _, action := range actions {
                go func(a oasis.ScheduledAction) {
                    task := oasis.AgentTask{
                        Input:    a.Task,
                        ThreadID: a.ID,
                    }
                    result, err := ag.Execute(ctx, task)
                    if err != nil {
                        return
                    }
                    _ = result // persist or route the output
                }(action)
            }
        }
    }
}
```

**Plain-English walkthrough:** `GetDueScheduledActions(ctx, now)` returns every
`ScheduledAction` whose `NextRunAt` is in the past. Your app calls `ag.Execute` once
per action in a goroutine. Using `action.ID` as `ThreadID` gives each scheduled job
its own memory thread so history from previous runs is available to the agent.

**Variations:**
- Use `store.UpdateScheduledAction(ctx, action)` to advance `NextRunAt` after a run.
- Pass `oasis.WithMemory(memory.WithStore(store), ...)` to the agent so it can recall
  previous executions of the same scheduled job.
- Use `store.UpdateScheduledActionEnabled(ctx, id, false)` to disable a job without
  deleting it.
