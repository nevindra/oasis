# Oasis

Go primitives for building AI agent systems that run in production.

---

## What you can build

- **Research assistants** that search the web, read documents, and remember past conversations across sessions.
- **Multi-agent pipelines** where a coordinator dispatches tasks to specialist agents (researcher, writer, critic) and merges their outputs.
- **Automated workflows** that execute a sequence of LLM calls and tool invocations as a DAG — steps without dependencies run in parallel automatically.
- **Document Q&A systems** that ingest PDFs, web pages, and CSVs, store them in a vector database, and answer questions with citations.
- **Code execution agents** that write and run Python or shell in a Docker sandbox, then explain the results.

---

## Why Oasis

- **Ten lines to a running agent.** The minimal case is genuinely minimal — one provider, one call to `NewAgent`, one `Execute`. No boilerplate, no config files.
- **Every primitive composes with every other.** An `LLMAgent` is an `Agent`. A `Network` of agents is an `Agent`. A `Workflow` containing both is an `Agent`. Nest them arbitrarily.
- **No LLM SDK dependencies.** Providers speak raw HTTP. Adding a new model means writing one file, not learning a vendor SDK.
- **Production concerns are built in.** Rate limiting, retry with backoff, graceful shutdown, bounded memory, structured logging — on by default, not bolted on later.
- **Codegen-friendly API.** Consistent shapes across every tool, provider, and store means an LLM coding assistant with zero prior Oasis context writes correct code on the first try.

---

## See it in action

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"

    oasis "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/provider/gemini"
)

func main() {
    llm := gemini.New(os.Getenv("GEMINI_API_KEY"), "gemini-2.0-flash")

    agent := oasis.NewAgent("assistant", "Helpful assistant", llm,
        oasis.WithPrompt("You are a helpful assistant."),
    )

    result, err := agent.Execute(context.Background(), oasis.AgentTask{
        Input: "Explain how a transformer model works in two sentences.",
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.Output)
}
```

**Line by line:**

| Line | What it does |
|------|--------------|
| `gemini.New(...)` | Creates a Gemini LLM provider — just an API key and model name. |
| `oasis.NewAgent(...)` | Builds the agent. `"assistant"` is the name the LLM sees; `"Helpful assistant"` is how it describes itself to routers. |
| `oasis.WithPrompt(...)` | Sets the system prompt. |
| `agent.Execute(...)` | Runs the tool-calling loop until the LLM returns a final text response. |
| `result.Output` | The final text. `result.Steps` has per-tool timing if you need it. |

---

## Where next?

| I want to… | Start here |
|------------|-----------|
| Understand what Oasis is and whether it fits my project | [Getting started](./getting-started/index.md) |
| Build my first agent with a tool and memory | [Getting started](./getting-started/index.md) |
| Learn about a specific primitive (Network, Workflow, RAG…) | Pick a topic below |
| Read the API for a type or option | Each topic folder has an `api.md` |

---

## The library

| Group | Topic | One-line description |
|-------|-------|----------------------|
| **Core primitives** | [agent](./agent/index.md) | Single LLM with tools — the base building block |
| | [network](./network/index.md) | LLM router that coordinates a team of agents |
| | [workflow](./workflow/index.md) | DAG orchestration — deterministic, parallel, composable |
| **Memory & knowledge** | [memory](./memory/index.md) | Conversation history, semantic recall, and long-term facts |
| | [rag](./rag/index.md) | Ingest documents; retrieve relevant chunks at query time |
| | [skills](./skills/index.md) | File-based instruction packages agents can discover and activate |
| **Execution** | [tools](./tools/index.md) | Built-in tools: HTTP fetch, shell, file I/O, search, data |
| | [sandbox](./sandbox/index.md) | Docker-backed code execution with shell, browser, and MCP |
| | [providers](./providers/index.md) | LLM and embedding provider implementations |
| **Operations** | [observability](./observability/index.md) | Structured logging, tracing, and OTEL integration |
| | [processors](./processors/index.md) | Pre/post hooks for guardrails, PII redaction, and HITL |
| | [store](./store/index.md) | Persistent storage backends (SQLite, PostgreSQL + pgvector) |
| **Integrations** | [mcp](./mcp/index.md) | MCP client/server — expose tools to AI assistants; consume external MCP servers |
