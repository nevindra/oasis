# Getting Started

This section walks you through installing Oasis and building your first AI agent in Go.

## Pages

| Page | Description |
|------|-------------|
| [Quick Start](quick-start.md) | Build a working agent in 5 minutes |
| [Reference App](reference-app.md) | Run the included Telegram bot demo |

## Prerequisites

| Requirement | Purpose |
|-------------|---------|
| **Go 1.24+** | Build and run |
| **LLM API Key** | Any supported provider (Gemini, OpenAI-compatible) |

Optional:
- **Brave Search API Key** — enables the `web_search` tool
- **Telegram Bot Token** — if you want to run the reference app

## Install

```bash
go get github.com/nevindra/oasis
```

This pulls the core framework. Provider, store, and tool packages are imported individually as needed.

## Minimal Example

The smallest useful Oasis program — a single agent that answers questions:

```go
package main

import (
    "context"
    "fmt"
    "log"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New("your-api-key", "gemini-2.5-flash")

    agent := oasis.NewLLMAgent("assistant", "Answers questions", llm,
        oasis.WithPrompt("You are a helpful assistant."),
    )

    result, err := agent.Execute(context.Background(), oasis.AgentTask{
        Input: "What are Go interfaces?",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.Output)
}
```

That's it — no config files, no database, no frontend. Just an LLM provider and an agent.

> **Tip:** When your provider choice comes from config, use the [provider resolver](../concepts/provider.md#provider-resolution) instead of importing provider packages directly:
> ```go
> llm, err := resolve.Provider(resolve.Config{Provider: "gemini", APIKey: "...", Model: "gemini-2.5-flash"})
> ```

## What's Next

- [Quick Start](quick-start.md) — add tools, memory, and streaming
- [Concepts](../concepts/index.md) — understand how the pieces fit together
- [Guides](../guides/custom-tool.md) — build your own tools, providers, and agents
