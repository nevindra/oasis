# API Reference: Options

## AgentOption

Shared by `NewLLMAgent` and `NewNetwork`.

| Option | Description |
|--------|-------------|
| `WithTools(tools ...Tool)` | Add tools |
| `WithPrompt(s string)` | Set system prompt |
| `WithMaxIter(n int)` | Max tool-calling iterations (default 10) |
| `WithAgents(agents ...Agent)` | Add subagents (Network only, ignored by LLMAgent) |
| `WithProcessors(processors ...any)` | Add processor middleware |
| `WithInputHandler(h InputHandler)` | Enable human-in-the-loop |
| `WithPlanExecution()` | Enable batched tool calls via built-in `execute_plan` tool |
| `WithCodeExecution(runner CodeRunner)` | Enable Python code execution via built-in `execute_code` tool |
| `WithResponseSchema(s *ResponseSchema)` | Enforce structured JSON output matching the schema |
| `WithDynamicPrompt(fn PromptFunc)` | Per-request system prompt resolution. Overrides `WithPrompt` |
| `WithDynamicModel(fn ModelFunc)` | Per-request provider/model selection. Overrides constructor provider |
| `WithDynamicTools(fn ToolsFunc)` | Per-request tool set. **Replaces** (not merges with) `WithTools` |
| `WithConversationMemory(s Store, opts ...ConversationOption)` | Enable history per thread |
| `WithUserMemory(m MemoryStore, e EmbeddingProvider)` | Enable user fact read/write |

## ConversationOption

Passed to `WithConversationMemory`.

| Option | Default | Description |
|--------|---------|-------------|
| `MaxHistory(n int)` | 10 | Max recent messages loaded into LLM context |
| `MaxTokens(n int)` | 0 (disabled) | Token budget for history — trim oldest-first until total fits within n |
| `CrossThreadSearch(e EmbeddingProvider, opts ...SemanticOption)` | — | Enable cross-thread semantic recall |

## SemanticOption

Passed to `CrossThreadSearch`.

| Option | Default | Description |
|--------|---------|-------------|
| `MinScore(score float32)` | 0.60 | Minimum cosine similarity for recall |

## StepOption

Configures individual workflow steps.

| Option | Applies To | Description |
|--------|-----------|-------------|
| `After(steps ...string)` | All | Dependency edges |
| `When(fn func(*WorkflowContext) bool)` | All | Condition gate: skip if false |
| `InputFrom(key string)` | AgentStep | Context key for agent input |
| `ArgsFrom(key string)` | ToolStep | Context key for tool JSON args |
| `OutputTo(key string)` | AgentStep, ToolStep | Override default output key |
| `Retry(n int, delay time.Duration)` | All | Retry on failure |
| `IterOver(key string)` | ForEach | Context key with `[]any` collection |
| `Concurrency(n int)` | ForEach | Max parallel iterations (default 1) |
| `Until(fn func(*WorkflowContext) bool)` | DoUntil | Exit condition |
| `While(fn func(*WorkflowContext) bool)` | DoWhile | Continue condition |
| `MaxIter(n int)` | DoUntil, DoWhile | Safety cap (default 10) |

## WorkflowOption

Configures workflow-level behavior.

| Option | Description |
|--------|-------------|
| `WithOnFinish(fn func(WorkflowResult))` | Callback after workflow completes |
| `WithOnError(fn func(string, error))` | Callback when a step fails |
| `WithDefaultRetry(n int, delay time.Duration)` | Default retry for all steps |

## SchedulerOption

Configures a Scheduler.

| Option | Default | Description |
|--------|---------|-------------|
| `WithSchedulerInterval(d time.Duration)` | 1 minute | Polling interval |
| `WithSchedulerTZOffset(hours int)` | 0 (UTC) | UTC offset for schedules |
| `WithOnRun(hook RunHook)` | nil | Hook after each action execution |

## RateLimitOption

Configures `WithRateLimit`.

| Option | Default | Description |
|--------|---------|-------------|
| `RPM(n int)` | 0 (disabled) | Max requests per minute (sliding window) |
| `TPM(n int)` | 0 (disabled) | Max tokens per minute — soft limit (input + output combined) |

## RetryOption

Configures `WithRetry`.

| Option | Default | Description |
|--------|---------|-------------|
| `RetryMaxAttempts(n int)` | 3 | Maximum total attempts |
| `RetryBaseDelay(d time.Duration)` | 1s | Initial backoff delay (doubles each attempt + jitter) |

## RetrieverOption

Configures `NewHybridRetriever`.

| Option | Default | Description |
|--------|---------|-------------|
| `WithReranker(r Reranker)` | nil | Re-ranking stage after hybrid merge |
| `WithMinRetrievalScore(s float32)` | 0 | Drop results below this score |
| `WithKeywordWeight(w float32)` | 0.3 | Keyword weight in RRF (vector gets 1-w) |
| `WithOverfetchMultiplier(n int)` | 3 | Fetch topK*n candidates before trim |
| `WithFilters(f ...ChunkFilter)` | none | Metadata filters passed to both search paths |

## Ingest Options

**Package:** `github.com/nevindra/oasis/ingest`

| Option | Default | Description |
|--------|---------|-------------|
| `WithChunker(c Chunker)` | RecursiveChunker | Custom chunker (flat strategy) |
| `WithParentChunker(c Chunker)` | — | Parent-level chunker |
| `WithChildChunker(c Chunker)` | — | Child-level chunker |
| `WithStrategy(s ChunkStrategy)` | `StrategyFlat` | Chunking strategy |
| `WithParentTokens(n int)` | 1024 | Parent chunk size |
| `WithChildTokens(n int)` | 256 | Child chunk size |
| `WithBatchSize(n int)` | 64 | Chunks per Embed() call |
| `WithExtractor(ct ContentType, e Extractor)` | — | Register custom extractor |

Chunker options:

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxTokens(n int)` | 512 | Max tokens per chunk |
| `WithOverlapTokens(n int)` | 50 | Overlap between consecutive chunks |
| `WithBreakpointPercentile(p int)` | 25 | Similarity percentile for semantic split detection (SemanticChunker) |

## CodeRunner Options

**Package:** `github.com/nevindra/oasis/code`

Configures `code.NewSubprocessRunner`.

| Option | Default | Description |
|--------|---------|-------------|
| `WithTimeout(d time.Duration)` | 30s | Maximum execution duration. Subprocess is killed on timeout |
| `WithMaxOutput(bytes int)` | 64KB | Maximum output size. Output beyond this is truncated |
| `WithWorkspace(path string)` | `os.TempDir()` | Working directory. File operations restricted to this path |
| `WithEnv(key, value string)` | — | Set environment variable. Multiple calls accumulate |
| `WithEnvPassthrough()` | minimal env | Pass all host environment variables to subprocess |
