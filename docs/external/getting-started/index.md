# Getting Started

Build a working AI agent in Go. Takes about 10 minutes.

---

## 1. Install

Requires **Go 1.24+**.

```bash
go get github.com/nevindra/oasis
```

This pulls the core framework. Provider and tool packages are imported individually — you only pay for what you use.

Add a provider. This guide uses the OpenAI-compatible provider, which works with OpenAI, Groq, Together, Ollama (local), and any OpenAI-compatible API:

```bash
go get github.com/nevindra/oasis/provider/openaicompat
```

Or use Gemini directly:

```bash
go get github.com/nevindra/oasis/provider/gemini
```

---

## 2. Your first agent

Create `main.go`:

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/openaicompat"
)

func main() {
    llm := openaicompat.NewProvider(
        os.Getenv("OPENAI_API_KEY"),
        "gpt-4o-mini",
        "https://api.openai.com/v1",
    )

    agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
        oasis.WithPrompt("You are a helpful assistant."),
    )

    result, err := agent.Execute(context.Background(), oasis.AgentTask{
        Input: "What is 12 * 144?",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.Output)
}
```

Run it:

```bash
OPENAI_API_KEY=sk-... go run main.go
```

**Line by line:**

| Line | What it does |
|------|--------------|
| `openaicompat.NewProvider(key, model, baseURL)` | Creates an LLM provider. Swap `baseURL` to `"http://localhost:11434/v1"` for Ollama with no API key. |
| `oasis.NewLLMAgent(name, description, llm, opts...)` | Builds the agent. Name and description are visible to LLM routers in multi-agent setups. |
| `oasis.WithPrompt(...)` | System prompt — sets the agent's persona and instructions. |
| `agent.Execute(ctx, AgentTask{Input: "..."})` | Runs the loop: the LLM generates a response; if it wants to call tools, those run and the loop continues; once the LLM produces a plain text response, Execute returns. |
| `result.Output` | The final text response. `result.Steps` contains per-tool timing and token counts. |

---

## 3. Make it do something useful

### Add a tool

Tools are how agents take actions — search the web, run code, fetch URLs, query a database. Here's a minimal custom tool:

```go
import (
    "context"
    oasis "github.com/nevindra/oasis"
)

// Step 1: define the input shape (the LLM fills this in JSON).
type CalcInput struct {
    Expression string `json:"expression" describe:"Math expression to evaluate, e.g. '2+2'"`
}

// Step 2: implement Tool[Input, Output].
type CalcTool struct{}

func (c *CalcTool) Definition() oasis.ToolMeta {
    return oasis.ToolMeta{
        Name:        "calculate",
        Description: "Evaluate a simple arithmetic expression and return the result.",
    }
}

func (c *CalcTool) Execute(ctx context.Context, in CalcInput) (string, error) {
    // real implementation would parse and evaluate; this is illustrative
    return "result: " + in.Expression, nil
}
```

Register it with the agent:

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithPrompt("You are a helpful assistant with a calculator."),
    oasis.WithTools(oasis.Erase(new(CalcTool))),
)
```

**What `oasis.Erase` does:** your tool is typed (`Tool[CalcInput, string]`); `Erase` wraps it into an `AnyTool` the agent can call. The input schema is derived automatically from `CalcInput`'s JSON tags — you don't write JSON Schema by hand.

Built-in tools you can drop in directly:
- `tools/http` — fetch and extract web pages
- `tools/data` — transform CSV/JSON
- `tools/shell` — run shell commands (use with a sandbox)

### Add memory

Without memory, every `Execute` call starts from a blank slate. Add conversation history so the agent remembers earlier turns in the same session:

```go
import (
    "github.com/nevindra/oasis/memory"
    "github.com/nevindra/oasis/store/sqlite"
)

// Open a persistent store (SQLite, no CGO required).
store, err := sqlite.Open("./agent.db")
if err != nil {
    log.Fatal(err)
}
defer store.Close()

agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
    oasis.WithPrompt("You are a helpful assistant."),
    oasis.WithMemory(
        memory.WithStore(store),
        memory.WithMaxHistory(20), // keep the last 20 turns in context
    ),
)
```

Then pass a thread ID so the agent knows which conversation to load:

```go
result, err := agent.Execute(ctx, oasis.AgentTask{
    Input:    "My name is Alice.",
    ThreadID: "user-alice-session-1",
})

// Later, in another Execute call with the same ThreadID:
result, err = agent.Execute(ctx, oasis.AgentTask{
    Input:    "What is my name?",
    ThreadID: "user-alice-session-1",
})
// result.Output: "Your name is Alice."
```

**How it works:** on each `Execute`, the agent loads the last `MaxHistory` messages for the given `ThreadID` from the store, prepends them to the LLM context, then saves the new turn after getting a response. The store is swappable — use `store/postgres` for production.

### Run it

Put the pieces together:

```go
func main() {
    store, _ := sqlite.Open("./agent.db")
    defer store.Close()

    agent := oasis.NewLLMAgent("assistant", "Helpful assistant", llm,
        oasis.WithPrompt("You are a helpful assistant with a calculator."),
        oasis.WithTools(oasis.Erase(new(CalcTool))),
        oasis.WithMemory(
            memory.WithStore(store),
            memory.WithMaxHistory(20),
        ),
    )

    threadID := "demo-thread-1"
    for _, input := range []string{
        "My name is Alice.",
        "What is 12 * 144?",
        "What is my name?",
    } {
        result, err := agent.Execute(ctx, oasis.AgentTask{
            Input:    input,
            ThreadID: threadID,
        })
        if err != nil {
            log.Fatal(err)
        }
        fmt.Printf("You: %s\nAgent: %s\n\n", input, result.Output)
    }
}
```

---

## What's next?

| I want to… | Read this |
|------------|-----------|
| Route tasks across multiple specialist agents | [network/](../network/index.md) |
| Run a deterministic sequence of LLM steps | [workflow/](../workflow/index.md) |
| Ingest documents and answer questions with citations | [rag/](../rag/index.md) |
| Store long-term facts about users across sessions | [memory/](../memory/index.md) |
