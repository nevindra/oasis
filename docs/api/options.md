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
| `WithTracer(t Tracer)` | Enable deep tracing (agent.execute, loop, memory spans) |
| `WithLogger(l *slog.Logger)` | Enable structured logging (defaults to no-op) |

## ConversationOption

Passed to `WithConversationMemory`.

| Option | Default | Description |
|--------|---------|-------------|
| `MaxHistory(n int)` | 10 | Max recent messages loaded into LLM context |
| `MaxTokens(n int)` | 0 (disabled) | Token budget for history — trim oldest-first until total fits within n |
| `CrossThreadSearch(e EmbeddingProvider, opts ...SemanticOption)` | — | Enable cross-thread semantic recall |
| `AutoTitle()` | disabled | Generate a short title from the first user message. Runs in background, idempotent (skipped if thread already has a title) |

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
| `WithWorkflowTracer(t Tracer)` | Enable workflow.execute and workflow.step spans |
| `WithWorkflowLogger(l *slog.Logger)` | Enable structured logging for workflows |

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
| `WithRetrieverTracer(t Tracer)` | nil | Enable retriever.retrieve spans |
| `WithRetrieverLogger(l *slog.Logger)` | nil | Enable structured logging for retrieval |

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
| `WithIngestorTracer(t oasis.Tracer)` | nil | Enable ingest.document spans |
| `WithIngestorLogger(l *slog.Logger)` | nil | Enable structured logging for ingestion |

Chunker options:

| Option | Default | Description |
|--------|---------|-------------|
| `WithMaxTokens(n int)` | 512 | Max tokens per chunk |
| `WithOverlapTokens(n int)` | 50 | Overlap between consecutive chunks |
| `WithBreakpointPercentile(p int)` | 25 | Similarity percentile for semantic split detection (SemanticChunker) |

## Gemini Options

**Package:** `github.com/nevindra/oasis/provider/gemini`

Passed directly to `gemini.New(apiKey, model, ...Option)`.

| Option | Default | Description |
|--------|---------|-------------|
| `WithTemperature(t float64)` | 0.1 | Sampling temperature |
| `WithTopP(p float64)` | 0.9 | Nucleus sampling top-p |
| `WithThinking(enabled bool)` | false | Enable thinking mode — sends `thinkingConfig` with dynamic budget (-1) |
| `WithStructuredOutput(enabled bool)` | true | When enabled, responses with a `ResponseSchema` use `application/json` MIME type |
| `WithResponseModalities(modalities ...string)` | — (text-only) | Required for image generation — use `WithResponseModalities("TEXT", "IMAGE")` |
| `WithMediaResolution(r string)` | — (omitted) | Media resolution for multimodal inputs: `"MEDIA_RESOLUTION_LOW"`, `"MEDIA_RESOLUTION_MEDIUM"`, `"MEDIA_RESOLUTION_HIGH"` |
| `WithCodeExecution(enabled bool)` | false | Enable Gemini's built-in code execution tool |
| `WithFunctionCalling(enabled bool)` | false | Allow implicit function calling. When false and no tools are provided, `toolConfig` mode is set to `NONE` |
| `WithGoogleSearch(enabled bool)` | false | Enable grounding with Google Search |
| `WithURLContext(enabled bool)` | false | Enable URL context tool |

## OpenAI-Compatible Options

**Package:** `github.com/nevindra/oasis/provider/openaicompat`

### ProviderOption

Passed to `openaicompat.NewProvider(apiKey, model, baseURL, ...ProviderOption)`.

| Option | Default | Description |
|--------|---------|-------------|
| `WithName(name string)` | `"openai"` | Provider name returned by `Name()` — used in logs and observability |
| `WithHTTPClient(c *http.Client)` | default client | Custom HTTP client (e.g. for timeouts or proxies) |
| `WithOptions(opts ...Option)` | — | Request-level options applied to every request made by this provider |

### Option

Passed inside `WithOptions(...)` or accumulated per-request.

| Option | Range | Description |
|--------|-------|-------------|
| `WithTemperature(t float64)` | 0.0–2.0 | Sampling temperature |
| `WithTopP(p float64)` | 0.0–1.0 | Nucleus sampling top-p |
| `WithMaxTokens(n int)` | — | Maximum output tokens |
| `WithFrequencyPenalty(p float64)` | -2.0–2.0 | Frequency penalty |
| `WithPresencePenalty(p float64)` | -2.0–2.0 | Presence penalty |
| `WithStop(s ...string)` | — | One or more stop sequences |
| `WithSeed(s int)` | — | Deterministic seed for reproducible outputs |
| `WithToolChoice(choice any)` | — | Tool selection: `"none"`, `"auto"`, `"required"`, or a specific tool object |

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
