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

## Context Helpers

**File:** `input.go`

```go
ctx = oasis.WithInputHandlerContext(ctx, handler InputHandler) context.Context
handler, ok := oasis.InputHandlerFromContext(ctx) (InputHandler, bool)
```

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
```

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
