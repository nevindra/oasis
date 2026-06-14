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
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")
    ag := oasis.NewAgent("helper", "A helpful assistant", llm,
        oasis.WithPrompt("You are a concise assistant. Respond in one sentence."),
    )

    result, err := ag.Execute(context.Background(), core.AgentTask{
        Input: "What is the capital of Indonesia?",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
}
```

**Plain-English walkthrough:** `oasis.NewAgent` (which calls `agent.New`) takes a
name, a description, a provider, and any number of options. `WithPrompt` sets the
system prompt that the LLM receives on every call. Because no tools are registered,
the loop runs once and returns the model's first text response.

**Variations:**
- Omit `WithPrompt` entirely for a promptless agent.
- Add `oasis.WithGeneration(oasis.Generation{Temperature: oasis.Ptr(0.2)})` to
  reduce randomness.
- Wrap the provider with retry on transient errors:
  ```go
  import "github.com/nevindra/oasis/provider"
  p := provider.Chain(oasis.RetryMiddleware(agent.RetryMaxAttempts(3)))(llm)
  ```

---

## Recipe 2: Agent with tools

**Goal:** Give the agent a custom tool and let the LLM decide when to call it.

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
)

type weatherArgs struct {
    City string `json:"city" jsonschema_description:"The city name"`
}

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    weatherTool := oasis.Func("get_weather", "Returns the current weather for a city.",
        func(ctx context.Context, args weatherArgs) (string, error) {
            return fmt.Sprintf("It is sunny in %s, 28°C.", args.City), nil
        },
    )

    ag := oasis.NewAgent("weather-bot", "Answers weather questions", llm,
        oasis.WithPrompt("You are a weather assistant."),
        oasis.WithTools(weatherTool),
    )

    result, err := ag.Execute(context.Background(), core.AgentTask{
        Input: "What's the weather like in Bali?",
    })
    if err != nil {
        panic(err)
    }
    fmt.Println(result.Output)
    fmt.Printf("Tool calls: %d\n", len(result.Steps))
}
```

**Plain-English walkthrough:** `oasis.Func` is the zero-boilerplate way to create a
tool from a plain function — input schema is derived by reflection, output is
marshalled to JSON automatically. `WithTools` registers one or more tools. The LLM
sees their schemas and decides when to call them. `result.Steps` records every tool
call with its input, output, and duration.

`core.TextResult(s)` is the shorthand for wrapping a plain string as a `ToolResult`.
Business errors (e.g. city not found) go in `ToolResult.Error` — never return a Go
error for tool-level failures.

**Variations:**
- Register multiple tools — the LLM can call any combination in any order.
- Use `WithToolConfig` to add per-tool timeout + retry alongside the tools:
  ```go
  oasis.WithToolConfig(agent.ToolConfig{
      Tools: []core.AnyTool{weatherTool},
      Policies: map[string]core.ToolPolicy{
          "get_weather": {Timeout: 5 * time.Second, Retries: 2},
      },
  })
  ```
- Use `oasis.WithPlanExecution()` so the LLM can batch multiple tool calls in one
  shot with the built-in `execute_plan` tool.

---

## Recipe 3: Streaming to an HTTP endpoint

**Goal:** Pipe the agent's output to a browser as Server-Sent Events.

```go
package main

import (
    "net/http"
    "os"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")
    ag := oasis.NewAgent("streamer", "Streams text to the browser", llm)

    http.HandleFunc("/chat", func(w http.ResponseWriter, r *http.Request) {
        task := core.AgentTask{Input: r.URL.Query().Get("q")}
        agent.ServeSSE(r.Context(), w, ag, task)
    })
    http.ListenAndServe(":8080", nil)
}
```

**Plain-English walkthrough:** `ServeSSE` handles all the SSE plumbing: it sets
`Content-Type: text/event-stream`, creates a channel, runs the agent in a background
goroutine, and flushes each `StreamEvent` to the response writer. When the browser
disconnects, `r.Context()` cancels and the agent stops cleanly.

**Variations:**
- For a custom SSE loop, call `Execute` with `core.WithStream(ch)` yourself and write
  events with `agent.WriteSSEEvent(w, string(ev.Type), ev)`.
- For multi-reader fan-out (websockets, logs, analytics), use `oasis.Subscribe`
  and call `stream.Events()` from each consumer goroutine.

---

## Recipe 4: Multi-reader streaming with the Stream wrapper

**Goal:** Watch text deltas in real time while also capturing the final result.

```go
import (
    "context"
    "fmt"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/core"
)

ctx := context.Background()
ag := oasis.NewAgent("writer", "Writes stories", llm)
task := core.AgentTask{Input: "Write a haiku about rain."}

stream := oasis.Subscribe(ctx, ag, task)

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

**Plain-English walkthrough:** `oasis.Subscribe` launches the agent in the background
immediately and returns a `*Stream`. `OnTextDelta` registers a lightweight callback
invoked for each `EventTextDelta` event; callbacks run on the dispatcher goroutine so
keep them fast. `stream.Result()` blocks until the agent finishes and returns the full
`AgentResult`. You can call `stream.Text()` as a shorthand if you only need the output
string.

**Variations:**
- Subscribe via `stream.Events()` to receive a channel and filter events yourself.
- Use `stream.OnToolCall(fn)` to watch tool calls fire in real time.
- Call `stream.FinishReason()` to check why the loop stopped.
- Pass `agent.WithOverrides(&agent.RunOptions{...})` as extra run options to
  `Subscribe` for per-call overrides alongside streaming.

---

## Recipe 5: Per-call overrides with RunOptions

**Goal:** Use one agent but vary the prompt, tools, or limits per request.

```go
import (
    "context"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

ag := oasis.NewAgent("multi", "Configurable assistant", llm,
    oasis.WithTools(defaultTools...),
    oasis.WithPrompt("Default system prompt."),
)

// High-priority request: stricter limits, different prompt.
result, err := ag.Execute(ctx, core.AgentTask{Input: "Summarize this doc"},
    agent.WithOverrides(&agent.RunOptions{
        Prompt: oasis.Ptr("You are a summarizer. Be concise."),
        Limits: &agent.Limits{MaxIter: 5, MaxToolResultLen: 10_000},
    }),
)
```

**Plain-English walkthrough:** `agent.WithOverrides` packs `*RunOptions` into the
per-call `RunConfig` so the agent applies them for this call only. `Prompt` replaces
the static prompt just for this request. `Limits` uses partial-override semantics —
only the fields you set change; unset fields keep the agent's defaults. The base
agent is not mutated.

**Variations:**
- Pass `Tools: []core.AnyTool{}` (empty slice, not nil) to give a request no tools
  at all.
- Pass `Generation: &agent.Generation{Temperature: oasis.Ptr(0.0)}` for deterministic
  output on a specific request.
- Combine streaming + overrides by passing both `core.WithStream(ch)` and
  `agent.WithOverrides(opts)` to the same `Execute` call.
- Add a per-call wall-clock cap with `core.WithDeadline(30 * time.Second)`.

---

## Recipe 6: Human-in-the-loop via suspend/resume

**Goal:** Pause the agent mid-run to ask a human for confirmation, then continue.

```go
import (
    "context"
    "encoding/json"
    "errors"
    "fmt"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

// An InputHandler bridges the agent's ask_user tool to your chat channel.
type cliHandler struct{}

func (h *cliHandler) RequestInput(ctx context.Context, req agent.InputRequest) (agent.InputResponse, error) {
    fmt.Println("Agent asks:", req.Question)
    var ans string
    fmt.Scan(&ans)
    return agent.InputResponse{Value: ans}, nil
}

ag := oasis.NewAgent("confirmer", "Asks before acting", llm,
    oasis.WithInputHandler(&cliHandler{}),
    oasis.WithTools(dangerousTool),
)

result, err := ag.Execute(ctx, core.AgentTask{Input: "Delete all temp files"})

var suspended *agent.ErrSuspended
if errors.As(err, &suspended) {
    suspended.WithSuspendTTL(5 * time.Minute) // auto-free if user abandons

    fmt.Printf("Suspended: %s\n", suspended.Payload)
    fmt.Print("Continue? (yes/no): ")
    var ans string
    fmt.Scan(&ans)

    result, err = suspended.Resume(ctx, json.RawMessage(`"`+ans+`"`))
}
```

**Plain-English walkthrough:** `WithInputHandler` adds an `ask_user` built-in tool.
When a processor or tool calls `Suspend(payload)`, `Execute` returns `*ErrSuspended`
instead of a normal result. The `Payload` field carries whatever context was passed to
`Suspend` — show it to your user. Call `suspended.Resume(ctx, data)` to continue
where it left off; `data` is the user's response injected as a user-role message into
the conversation.

**Variations:**
- Use `suspended.ResumeStream(ctx, data, ch)` for streaming resume.
- Use `agent.NewSuspendProtocol[Req, Resp]("tag")` for typed suspend/resume flows
  instead of raw `json.RawMessage`.
- Call `suspended.Release()` when the user abandons the session.

---

## Recipe 7: Tool logging and transformation middleware

**Goal:** Log every tool call and redact sensitive fields from results.

```go
import (
    "log/slog"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

logger := slog.Default()

ag := oasis.NewAgent("secure-agent", "Logs and redacts tool output", llm,
    oasis.WithToolConfig(agent.ToolConfig{
        Tools: myTools,
        Middleware: []core.ToolMiddleware{
            agent.LoggingMiddleware(logger),
            agent.TransformMiddleware(func(name string, r core.ToolResult) core.ToolResult {
                if name == "get_user_profile" {
                    r.Content = redactEmail(r.Content)
                }
                return r
            }),
        },
    }),
)
```

**Plain-English walkthrough:** `WithToolConfig` lets you combine tool registration
with middleware in one call. `LoggingMiddleware` logs `tool.start` and `tool.finish`
at `slog.Info`. `TransformMiddleware` can inspect or rewrite the `ToolResult` before
it reaches the LLM. Middleware ordering: first in the slice = innermost wrap (closest
to the tool); last = outermost.

**Variations:**
- Add `agent.TimingMiddleware()` for lightweight `slog.Debug` duration logs.
- Use `agent.OTelSpanMiddleware(tracer)` for OpenTelemetry spans per tool call (or
  let it auto-wire by passing `oasis.WithTracer(t)` to the agent).
- `TransformMiddleware` is not called when the inner tool returned a Go error, so
  you don't need to guard for nil content.

---

## Recipe 8: Consolidated processors and hooks

**Goal:** Wire pre/post processors and iteration hooks without multiple option calls.

```go
import (
    "context"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

ag := oasis.NewAgent("guarded", "Agent with guardrails and iteration control", llm,
    oasis.WithTools(myTools...),
    oasis.WithProcessors(agent.Processors{
        Pre:      []core.PreProcessor{piiScrubber, rateLimitGuard},
        Post:     []core.PostProcessor{outputLogger},
        PostTool: []core.PostToolProcessor{toolResultAuditor},
    }),
    oasis.WithHooks(agent.Hooks{
        PrepareStep: func(ctx context.Context, iter int, ctrl *agent.StepControl) error {
            // Inject a pinned context message on the first iteration.
            if iter == 0 {
                ctrl.Request.Messages = append(ctrl.Request.Messages,
                    core.SystemMessage("Today is "+time.Now().Format(time.DateOnly)),
                )
            }
            return nil
        },
        OnIterationComplete: func(ctx context.Context, iter int, snap *agent.IterationSnapshot) (agent.IterationDecision, error) {
            // Stop early if the model's output contains a sentinel.
            if strings.Contains(snap.Response.Content, "DONE") {
                return agent.Stop(core.TextResult("Finished early.")), nil
            }
            return agent.Continue(), nil
        },
        OnError: func(ctx context.Context, iter int, err error) (agent.ErrorDecision, error) {
            // Retry on transient errors, propagate everything else.
            if isTransient(err) {
                return agent.Retry(), nil
            }
            return agent.Propagate(), nil
        },
    }),
)
```

**Plain-English walkthrough:** `WithProcessors` consolidates `Pre`, `Post`, and
`PostTool` processor chains into a single option. `WithHooks` consolidates the three
mid-iteration callbacks (`PrepareStep`, `OnIterationComplete`, `OnError`) into one.
Nil fields in `Hooks` leave the corresponding slot untouched, so partial bundles
compose safely with other `WithHooks` calls.

`IterationDecision` helpers: `agent.Continue()`, `agent.Stop(result)`,
`agent.InjectFeedback(msg)`, `agent.InjectMessages(msgs...)`.
`ErrorDecision` helpers: `agent.Propagate()`, `agent.Retry()`,
`agent.RetryWithFeedback(msg)`, `agent.HaltDecision(result)`.

---

## Recipe 9: Time-based dispatch (scheduler pattern)

**Goal:** Fire an agent on a schedule using `ScheduledAction` records in the store.

```go
import (
    "context"
    "time"

    "github.com/nevindra/oasis/agent"
    "github.com/nevindra/oasis/core"
)

// core.ScheduledActionStore is an optional capability interface. Discover it
// via type assertion on your store.
func runScheduler(ctx context.Context, store core.ScheduledActionStore, ag *agent.LLMAgent) {
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
                go func(a core.ScheduledAction) {
                    task := core.AgentTask{
                        Input:    a.Description,
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
`core.ScheduledAction` whose `NextRun` is in the past. Your app calls `ag.Execute`
once per action in a goroutine. Using `action.ID` as `ThreadID` gives each scheduled
job its own memory thread so history from previous runs is available to the agent.

`core.ScheduledActionStore` is an optional capability — not part of the base
`core.Store` interface. Discover it with a type assertion:
```go
if sas, ok := myStore.(core.ScheduledActionStore); ok {
    // use sas
}
```

**Variations:**
- Use `store.UpdateScheduledAction(ctx, action)` to advance `NextRun` after a run.
- Pass `oasis.WithMemory(memory.WithStore(store), ...)` to the agent so it can recall
  previous executions of the same scheduled job.
- Use `store.UpdateScheduledActionEnabled(ctx, id, false)` to disable a job without
  deleting it.
