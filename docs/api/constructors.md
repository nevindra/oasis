# API Reference: Constructors and Helpers

## ChatMessage Constructors

**File:** `types.go`

```go
oasis.UserMessage(text string) ChatMessage         // Role: "user"
oasis.SystemMessage(text string) ChatMessage        // Role: "system"
oasis.AssistantMessage(text string) ChatMessage     // Role: "assistant"
oasis.ToolResultMessage(callID, content string) ChatMessage  // Role: "tool"
```

## ToolRegistry

**File:** `tool.go`

```go
registry := oasis.NewToolRegistry()
registry.Add(tool Tool)
registry.AllDefinitions() []ToolDefinition
registry.Execute(ctx, name string, args json.RawMessage) (ToolResult, error)
```

## LLMAgent

**File:** `llmagent.go`

```go
agent := oasis.NewLLMAgent(name, description string, provider Provider, opts ...AgentOption)
```

## Network

**File:** `network.go`

```go
network := oasis.NewNetwork(name, description string, router Provider, opts ...AgentOption)
```

## Workflow

**File:** `workflow.go`

```go
wf, err := oasis.NewWorkflow(name, description string, opts ...WorkflowOption)
```

Step definitions (return `WorkflowOption`):

```go
oasis.Step(name string, fn StepFunc, opts ...StepOption)
oasis.AgentStep(name string, agent Agent, opts ...StepOption)
oasis.ToolStep(name string, tool Tool, toolName string, opts ...StepOption)
oasis.ForEach(name string, fn StepFunc, opts ...StepOption)
oasis.DoUntil(name string, fn StepFunc, opts ...StepOption)
oasis.DoWhile(name string, fn StepFunc, opts ...StepOption)
```

## FromDefinition

**File:** `workflow.go`

```go
wf, err := oasis.FromDefinition(def WorkflowDefinition, reg DefinitionRegistry) (*Workflow, error)
```

Converts a JSON-serializable `WorkflowDefinition` into an executable `*Workflow`. The resulting workflow is identical to one built with `NewWorkflow` — same DAG engine, same step types.

The `DefinitionRegistry` maps string names in the definition to concrete Go objects:

```go
reg := oasis.DefinitionRegistry{
    Agents:     map[string]oasis.Agent{ ... },      // for "llm" nodes
    Tools:      map[string]oasis.Tool{ ... },       // for "tool" nodes
    Conditions: map[string]func(*oasis.WorkflowContext) bool{ ... }, // escape hatch for complex conditions
}
```

Validates at construction time: unique node IDs, valid edge targets, branch targets exist, agents/tools exist in registry, cycle detection.

## WorkflowContext Template Methods

**File:** `workflow.go`

```go
wCtx.Resolve(template string) string                  // {{key}} → string value
wCtx.ResolveJSON(template string) json.RawMessage      // {{key}} → JSON value (preserves structure)
```

`Resolve` replaces `{{key}}` placeholders with values from the context. Unknown keys resolve to empty strings. All values are formatted via `fmt.Sprintf("%v", v)`.

`ResolveJSON` is like `Resolve` but returns `json.RawMessage`. When the template is a single placeholder (e.g. `"{{key}}"`) and the value is not a string, the value is marshalled to JSON directly (preserving maps, slices, numbers). Mixed templates resolve as a JSON string.

The `"input"` key is pre-populated with `AgentTask.Input`, so `{{input}}` resolves to the original task input in any workflow.

## Scheduler

**File:** `scheduler.go`

```go
scheduler := oasis.NewScheduler(store Store, agent Agent, opts ...SchedulerOption)
scheduler.Start(ctx context.Context) error  // blocks until ctx cancelled
```

## Spawn

**File:** `handle.go`

```go
handle := oasis.Spawn(ctx context.Context, agent Agent, task AgentTask) *AgentHandle
```

## ProcessorChain

**File:** `processor.go`

```go
chain := oasis.NewProcessorChain()
chain.Add(processor any)
chain.RunPreLLM(ctx, req *ChatRequest) error
chain.RunPostLLM(ctx, resp *ChatResponse) error
chain.RunPostTool(ctx, call ToolCall, result *ToolResult) error
chain.Len() int
```

## WithRetry

**File:** `retry.go`

```go
provider := oasis.WithRetry(p Provider, opts ...RetryOption) Provider
```

## WithRateLimit

**File:** `ratelimit.go`

```go
provider := oasis.WithRateLimit(p Provider, opts ...RateLimitOption) Provider
```

## Context Helpers

**File:** `agent.go`, `input.go`

```go
ctx = oasis.WithTaskContext(ctx, task AgentTask) context.Context
task, ok := oasis.TaskFromContext(ctx) (AgentTask, bool)

ctx = oasis.WithInputHandlerContext(ctx, handler InputHandler) context.Context
handler, ok := oasis.InputHandlerFromContext(ctx) (InputHandler, bool)
```

`WithTaskContext`/`TaskFromContext` are called automatically by `LLMAgent` and `Network` at Execute entry points. Use `TaskFromContext` in tools to access task metadata (user ID, thread ID, custom context) without changing the `Tool` interface.

## ForEach Helpers

**File:** `workflow.go`

```go
item, ok := oasis.ForEachItem(ctx) (any, bool)
index, ok := oasis.ForEachIndex(ctx) (int, bool)
```

## ID and Time

**File:** `id.go`

```go
oasis.NewID() string      // time-sortable 20-char xid (base32)
oasis.NowUnix() int64     // current Unix timestamp (seconds)
```

## Chunk Filter Constructors

**File:** `types.go`

```go
oasis.ByDocument(ids ...string) ChunkFilter      // match chunks by document ID(s)
oasis.BySource(source string) ChunkFilter         // match by document source
oasis.ByMeta(key, value string) ChunkFilter       // match by chunk metadata JSON key
oasis.CreatedAfter(unix int64) ChunkFilter         // documents created after timestamp
oasis.CreatedBefore(unix int64) ChunkFilter        // documents created before timestamp
```

Pass to `Store.SearchChunks`, `KeywordSearcher.SearchChunksKeyword`, or `HybridRetriever` via `WithFilters`. See [Store: Chunk Filtering](../concepts/store.md#chunk-filtering) for details.

## Retrieval

**File:** `retriever.go`

```go
retriever := oasis.NewHybridRetriever(store Store, emb EmbeddingProvider, opts ...RetrieverOption)
```

Rerankers:

```go
oasis.NewScoreReranker(minScore float32)
oasis.NewLLMReranker(provider Provider)
```

## Ingest

**Package:** `github.com/nevindra/oasis/ingest`

```go
ingestor := ingest.NewIngestor(store oasis.Store, emb oasis.EmbeddingProvider, opts ...Option)
ingestor.IngestText(ctx, text, source, title string) (IngestResult, error)
ingestor.IngestFile(ctx, content []byte, filename string) (IngestResult, error)
ingestor.IngestReader(ctx, r io.Reader, filename string) (IngestResult, error)
```

Chunkers:

```go
ingest.NewRecursiveChunker(opts ...ChunkerOption)
ingest.NewMarkdownChunker(opts ...ChunkerOption)
ingest.NewSemanticChunker(embed EmbedFunc, opts ...ChunkerOption)
```

`NewSemanticChunker` takes an `EmbedFunc` as its first argument — pass `embedding.Embed` directly (the signatures match). Implements both `Chunker` and `ContextChunker`.

## Observer

**Package:** `github.com/nevindra/oasis/observer`

```go
inst, shutdown, err := observer.Init(ctx, pricingOverrides)
observer.WrapProvider(provider, modelName, inst) Provider
observer.WrapEmbedding(embedding, modelName, inst) EmbeddingProvider
observer.WrapTool(tool, inst) Tool
observer.WrapAgent(agent, inst) Agent
observer.NewCostCalculator(overrides) *CostCalculator
```
