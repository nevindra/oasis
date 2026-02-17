# Extending Oasis

This guide shows how to add custom tools, LLM providers, frontends, storage backends, and skills to Oasis.

## Adding a Custom Tool

Tools are the primary extension point. Implement the `Tool` interface and register it.

### Step 1: Implement the Tool Interface

```go
package mytool

import (
    "context"
    "encoding/json"

    oasis "github.com/nevindra/oasis"
)

type WeatherTool struct {
    apiKey string
}

func New(apiKey string) *WeatherTool {
    return &WeatherTool{apiKey: apiKey}
}

// Definitions returns the tool schemas the LLM will see.
func (t *WeatherTool) Definitions() []oasis.ToolDefinition {
    return []oasis.ToolDefinition{{
        Name:        "get_weather",
        Description: "Get current weather for a city.",
        Parameters: json.RawMessage(`{
            "type": "object",
            "properties": {
                "city": {
                    "type": "string",
                    "description": "City name"
                }
            },
            "required": ["city"]
        }`),
    }}
}

// Execute handles the tool call.
func (t *WeatherTool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    var params struct {
        City string `json:"city"`
    }
    if err := json.Unmarshal(args, &params); err != nil {
        return oasis.ToolResult{Error: "invalid args: " + err.Error()}, nil
    }

    // Call your weather API here...
    weather := fetchWeather(t.apiKey, params.City)

    return oasis.ToolResult{Content: weather}, nil
}
```

### Step 2: Register the Tool

In your application bootstrap (e.g. `cmd/bot_example/main.go`):

```go
app.AddTool(mytool.New("weather-api-key"))
```

The tool's definitions are automatically included when the ToolRegistry provides tool schemas to the LLM.

### Patterns to Follow

**Return errors in ToolResult, not as Go errors.** The Go `error` return is for fatal/unexpected failures. Business-level errors (invalid input, API failure, not found) go in `ToolResult.Error`:

```go
// Good
return oasis.ToolResult{Error: "city not found: " + params.City}, nil

// Bad -- don't use Go error for expected failures
return oasis.ToolResult{}, fmt.Errorf("city not found: %s", params.City)
```

**A single Tool can expose multiple functions.** Return multiple `ToolDefinition`s and dispatch on the `name` parameter:

```go
func (t *MyTool) Definitions() []oasis.ToolDefinition {
    return []oasis.ToolDefinition{
        {Name: "widget_create", Description: "...", Parameters: ...},
        {Name: "widget_list",   Description: "...", Parameters: ...},
        {Name: "widget_delete", Description: "...", Parameters: ...},
    }
}

func (t *MyTool) Execute(ctx context.Context, name string, args json.RawMessage) (oasis.ToolResult, error) {
    switch name {
    case "widget_create":
        return t.handleCreate(ctx, args)
    case "widget_list":
        return t.handleList(ctx)
    case "widget_delete":
        return t.handleDelete(ctx, args)
    default:
        return oasis.ToolResult{Error: "unknown tool: " + name}, nil
    }
}
```

**Inject dependencies through the constructor.** Tools that need storage, embedding, or other services receive them via `New()`:

```go
func New(store oasis.Store, emb oasis.EmbeddingProvider) *MyTool {
    return &MyTool{store: store, embedding: emb}
}
```

### Tool Definition Schema

The `Parameters` field is a JSON Schema object that the LLM uses to generate arguments. Follow the standard JSON Schema format:

```json
{
    "type": "object",
    "properties": {
        "query": {
            "type": "string",
            "description": "Search query"
        },
        "limit": {
            "type": "integer",
            "description": "Max results (default 10)"
        }
    },
    "required": ["query"]
}
```

Write clear `description` fields -- the LLM uses these to decide when and how to call the tool.

## Adding an LLM Provider

Implement the `Provider` interface:

```go
package myprovider

import (
    "context"

    oasis "github.com/nevindra/oasis"
)

type MyLLM struct {
    apiKey string
    model  string
}

func New(apiKey, model string) *MyLLM {
    return &MyLLM{apiKey: apiKey, model: model}
}

func (m *MyLLM) Chat(ctx context.Context, req oasis.ChatRequest) (oasis.ChatResponse, error) {
    // Convert req.Messages to your API format
    // Make HTTP request
    // Parse response into ChatResponse
    return oasis.ChatResponse{
        Content: responseText,
        Usage:   oasis.Usage{InputTokens: in, OutputTokens: out},
    }, nil
}

func (m *MyLLM) ChatWithTools(ctx context.Context, req oasis.ChatRequest, tools []oasis.ToolDefinition) (oasis.ChatResponse, error) {
    // Same as Chat but include tool definitions in the request
    // Parse tool_calls from response if present
    return oasis.ChatResponse{
        Content:   responseText,
        ToolCalls: parsedToolCalls,  // []oasis.ToolCall
        Usage:     usage,
    }, nil
}

func (m *MyLLM) ChatStream(ctx context.Context, req oasis.ChatRequest, ch chan<- string) (oasis.ChatResponse, error) {
    defer close(ch)

    // Make streaming HTTP request (SSE)
    // For each chunk:
    //   ch <- chunkText

    return oasis.ChatResponse{
        Content: fullText,
        Usage:   usage,
    }, nil
}

func (m *MyLLM) Name() string { return "myprovider" }
```

**Key requirements:**
- `ChatStream` must close the channel when done
- `ChatWithTools` must populate `ChatResponse.ToolCalls` when the LLM wants to call tools
- Each `ToolCall` needs an `ID`, `Name`, and `Args` (JSON)

### Adding an Embedding Provider

```go
func (m *MyProvider) Embed(ctx context.Context, texts []string) ([][]float32, error) {
    // Call embedding API
    // Return one vector per input text
    return vectors, nil
}

func (m *MyProvider) Dimensions() int { return 1536 }
func (m *MyProvider) Name() string    { return "myprovider" }
```

## Adding a Frontend

Implement the `Frontend` interface to support a new messaging platform (Discord, Slack, HTTP API, CLI, etc.):

```go
package discord

import (
    "context"

    oasis "github.com/nevindra/oasis"
)

type Bot struct {
    token string
}

func New(token string) *Bot {
    return &Bot{token: token}
}

func (b *Bot) Poll(ctx context.Context) (<-chan oasis.IncomingMessage, error) {
    ch := make(chan oasis.IncomingMessage)
    go func() {
        defer close(ch)
        // Listen for messages from your platform
        // Convert each to oasis.IncomingMessage and send to ch
        // Respect ctx.Done() for graceful shutdown
    }()
    return ch, nil
}

func (b *Bot) Send(ctx context.Context, chatID string, text string) (string, error) {
    // Send message, return its ID
    return msgID, nil
}

func (b *Bot) Edit(ctx context.Context, chatID string, msgID string, text string) error {
    // Edit existing message (plain text)
    return nil
}

func (b *Bot) EditFormatted(ctx context.Context, chatID string, msgID string, text string) error {
    // Edit with rich formatting (HTML input)
    return nil
}

func (b *Bot) SendTyping(ctx context.Context, chatID string) error {
    // Show typing indicator
    return nil
}

func (b *Bot) DownloadFile(ctx context.Context, fileID string) ([]byte, string, error) {
    // Download file, return bytes + filename
    return data, filename, nil
}
```

**Key design notes:**
- `Poll` should run in a goroutine and push messages to the channel
- `Poll` must respect `ctx.Done()` for graceful shutdown
- `Send` returns a message ID that can be used with `Edit`/`EditFormatted` later
- `EditFormatted` receives HTML -- convert to your platform's format as needed

## Adding a Storage Backend

### Store

Implement the `Store` interface for a new database:

```go
package mystore

type Store struct {
    // your connection details
}

func New(connectionString string) *Store {
    return &Store{...}
}

func (s *Store) Init(ctx context.Context) error {
    // Create tables, indexes
    return nil
}

// Implement all Store methods...
// See store.go for the full interface
```

**Vector search requirement:** You need to implement cosine similarity search over embeddings. Options:
- Brute-force in-memory (like `store/sqlite`)
- Database-native vector indexes (pgvector, DiskANN, etc.)
- External vector DB (Pinecone, Qdrant, etc.)

### MemoryStore

Implement the `MemoryStore` interface:

```go
func (s *Store) UpsertFact(ctx context.Context, fact, category string, embedding []float32) error {
    // Check for semantically similar existing facts (cosine similarity > 0.85)
    // If found: merge (increment confidence)
    // If not: insert new fact with confidence 1.0
    return nil
}
```

The key behavior to preserve:
- **Semantic deduplication**: Similar facts (cosine > 0.85) are merged, not duplicated
- **Confidence scoring**: New facts start at 1.0, reinforced facts get +0.1
- **Decay**: Old unreinforced facts lose confidence over time

## Using the Ingest Pipeline

The ingest pipeline is not an interface -- it's a utility you use directly:

```go
import "github.com/nevindra/oasis/ingest"

// Create pipeline with chunking config
pipeline := ingest.NewPipeline(512, 50) // maxTokens, overlapTokens

// Ingest content
result := pipeline.IngestText(content, "source-url", "Document Title")
// result.Document -- the Document record
// result.Chunks   -- []Chunk (no embeddings yet)

// Embed chunks
texts := make([]string, len(result.Chunks))
for i, c := range result.Chunks {
    texts[i] = c.Content
}
vectors, _ := embeddingProvider.Embed(ctx, texts)
for i := range result.Chunks {
    result.Chunks[i].Embedding = vectors[i]
}

// Store
store.StoreDocument(ctx, result.Document, result.Chunks)
```

The pipeline handles extraction and chunking. **You** handle embedding and storage. This separation keeps the pipeline dependency-free and testable.

## Creating Skills

Skills are stored instruction packages that specialize the action agent's behavior. They live in the database (via `Store`) and can be managed at runtime through tools or direct API calls.

A skill consists of:
- **Name** and **Description** -- used for display and semantic matching
- **Instructions** -- injected into the agent's system prompt when the skill is active
- **Tools** (optional) -- restricts which tools the agent can use. Empty means all tools are available.
- **Model** (optional) -- overrides the default LLM model for this skill
- **Embedding** -- vector representation of the description, used for semantic search

### Creating a Skill Programmatically

```go
skill := oasis.Skill{
    ID:           oasis.NewID(),
    Name:         "code-reviewer",
    Description:  "Review code changes and suggest improvements",
    Instructions: "You are a code reviewer. Analyze the code provided and give constructive feedback on style, correctness, and performance.",
    Tools:        []string{"shell_exec", "file_read"}, // Only these tools available
    CreatedAt:    oasis.NowUnix(),
    UpdatedAt:    oasis.NowUnix(),
}

// Embed the description for semantic search
vectors, _ := embeddingProvider.Embed(ctx, []string{skill.Description})
skill.Embedding = vectors[0]

// Store
store.CreateSkill(ctx, skill)
```

### Searching Skills by Semantic Similarity

```go
// Embed the user's message
queryVec, _ := embeddingProvider.Embed(ctx, []string{"review my pull request"})

// Find top 3 matching skills
matches, _ := store.SearchSkills(ctx, queryVec[0], 3)
// matches[0].Name == "code-reviewer" (highest similarity)
```

### Skill Resolution Pattern

The reference application in `internal/bot/` uses a two-stage resolution:
1. Embed the user message and `SearchSkills` for top candidates
2. Ask an intent LLM to pick the best match (or "none")

This pattern is application-level -- the framework provides the storage and search primitives, your application decides how to select and apply skills.

## Adding Processors

Processors hook into the agent execution pipeline to transform, validate, or control messages. Implement one or more of the three processor interfaces.

### Example: Guardrail (PreProcessor)

```go
package guard

import (
    "context"
    "strings"

    oasis "github.com/nevindra/oasis"
)

type Guardrail struct{}

func (g *Guardrail) PreLLM(_ context.Context, req *oasis.ChatRequest) error {
    last := req.Messages[len(req.Messages)-1]
    if strings.Contains(strings.ToLower(last.Content), "ignore all previous instructions") {
        return &oasis.ErrHalt{Response: "I can't process that request."}
    }
    return nil
}
```

### Example: PII Redactor (all 3 phases)

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

### Example: Tool Filter (PostProcessor)

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

### Registering Processors

```go
agent := oasis.NewLLMAgent("assistant", "Helpful assistant", provider,
    oasis.WithTools(searchTool, scheduleTool),
    oasis.WithProcessors(
        &Guardrail{},
        &PIIRedactor{},
        &ToolFilter{Blocked: map[string]bool{"shell_exec": true}},
    ),
)
```

Processors run in registration order at each hook point. If a processor returns `ErrHalt`, execution stops and the halt message becomes the agent's response. Other errors propagate as infrastructure failures.

### Patterns to Follow

- **Implement only the interfaces you need.** A guardrail only needs `PreProcessor`. A redactor may need all three. The chain skips processors that don't implement a given phase.
- **Return `ErrHalt` for intentional stops.** Regular errors signal infrastructure failure; `ErrHalt` produces a graceful `AgentResult` with the halt message.
- **Processors must be safe for concurrent use.** Multiple agent executions may share the same processor instance.
- **Modify in place.** Processors receive pointers (`*ChatRequest`, `*ChatResponse`, `*ToolResult`) and modify them directly.

## Creating Custom Agents

Agents are composable units of work. The framework ships with two concrete implementations (`LLMAgent` and `Network`), but you can implement the `Agent` interface directly for custom behavior.

### Using LLMAgent

The simplest way to create an agent -- give it a Provider, tools, and a prompt:

```go
researcher := oasis.NewLLMAgent("researcher", "Searches for information", geminiProvider,
    oasis.WithTools(knowledge.New(store, emb), search.New(emb, braveKey)),
    oasis.WithPrompt("You are a research assistant. Search thoroughly before answering."),
    oasis.WithMaxIter(5),
)

result, err := researcher.Execute(ctx, oasis.AgentTask{
    Input:   "What are the best practices for Go error handling?",
    Context: map[string]string{"user_id": "123"},
})
fmt.Println(result.Output)
```

LLMAgent runs a loop: call `ChatWithTools` -> execute tool calls -> feed results back -> repeat until the LLM returns a text response (no more tool calls). If `maxIter` is reached, it forces synthesis by asking the LLM to summarize.

### Using Network

Network coordinates multiple agents and tools via an LLM router:

```go
researcher := oasis.NewLLMAgent("researcher", "Searches for information", geminiProvider,
    oasis.WithTools(searchTool),
)
writer := oasis.NewLLMAgent("writer", "Writes polished content", geminiProvider,
    oasis.WithPrompt("You are a skilled writer."),
)

coordinator := oasis.NewNetwork("coordinator", "Routes research and writing tasks", geminiProvider,
    oasis.WithAgents(researcher, writer),
    oasis.WithTools(knowledgeTool), // Direct tools also available to the router
)

result, err := coordinator.Execute(ctx, oasis.AgentTask{Input: "Research Go generics and write a summary"})
```

The router LLM sees subagents as tools named `agent_<name>` (e.g. `agent_researcher`, `agent_writer`). It decides which to call, with what task description, and synthesizes the final response.

**Networks compose recursively** -- a Network is an Agent, so it can be a subagent of another Network:

```go
researchTeam := oasis.NewNetwork("research_team", "Coordinates research", routerProvider,
    oasis.WithAgents(webSearcher, docSearcher),
)

mainNetwork := oasis.NewNetwork("main", "Top-level coordinator", routerProvider,
    oasis.WithAgents(researchTeam, writer),
)
```

### Implementing the Agent Interface

For behavior that doesn't fit LLMAgent or Network, implement `Agent` directly:

```go
package myagent

import (
    "context"

    oasis "github.com/nevindra/oasis"
)

type SummaryAgent struct {
    provider oasis.Provider
}

func New(provider oasis.Provider) *SummaryAgent {
    return &SummaryAgent{provider: provider}
}

func (a *SummaryAgent) Name() string        { return "summarizer" }
func (a *SummaryAgent) Description() string { return "Summarizes long text into bullet points" }

func (a *SummaryAgent) Execute(ctx context.Context, task oasis.AgentTask) (oasis.AgentResult, error) {
    resp, err := a.provider.Chat(ctx, oasis.ChatRequest{
        Messages: []oasis.ChatMessage{
            oasis.SystemMessage("Summarize the following into 3-5 bullet points."),
            oasis.UserMessage(task.Input),
        },
    })
    if err != nil {
        return oasis.AgentResult{}, err
    }
    return oasis.AgentResult{Output: resp.Content, Usage: resp.Usage}, nil
}
```

Custom agents work anywhere an `Agent` is expected -- including as subagents in a Network.

## Wiring It All Together

Here's a minimal example of assembling components:

```go
package main

import (
    "context"
    "log"

    "github.com/nevindra/oasis"
    "github.com/nevindra/oasis/internal/config"
    "github.com/nevindra/oasis/frontend/telegram"
    "github.com/nevindra/oasis/provider/gemini"
    "github.com/nevindra/oasis/store/sqlite"
    "github.com/nevindra/oasis/tools/knowledge"
)

func main() {
    cfg := config.Load("")

    // Create components
    frontend := telegram.New(cfg.Telegram.Token)
    llm := gemini.New(cfg.LLM.APIKey, cfg.LLM.Model)
    emb := gemini.NewEmbedding(cfg.Embedding.APIKey, cfg.Embedding.Model, cfg.Embedding.Dimensions)
    store := sqlite.New(cfg.Database.Path)

    ctx := context.Background()
    store.Init(ctx)

    // Create tool registry
    tools := oasis.NewToolRegistry()
    tools.Add(knowledge.New(store, emb))

    // Poll for messages
    messages, _ := frontend.Poll(ctx)
    for msg := range messages {
        // Build your own routing logic here
        log.Printf("Received: %s", msg.Text)

        // Example: send to LLM
        resp, _ := llm.Chat(ctx, oasis.ChatRequest{
            Messages: []oasis.ChatMessage{
                oasis.SystemMessage("You are a helpful assistant."),
                oasis.UserMessage(msg.Text),
            },
        })

        frontend.Send(ctx, msg.ChatID, resp.Content)
    }
}
```

This is the minimal viable loop. The reference application in `internal/bot/` adds intent classification, streaming, concurrent agents, memory, and background storage on top of this foundation.
