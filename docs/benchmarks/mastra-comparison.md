# Mastra vs Oasis — Framework Comparison

> Last updated: 2026-02-20

## Language & Philosophy

| | Mastra | Oasis |
|---|---|---|
| **Language** | TypeScript/Node.js | Go |
| **Dependencies** | Heavy — AI SDK, many npm packages | Minimal — raw `net/http`, no LLM SDKs |
| **Architecture** | Class-based, npm ecosystem | Interface-driven, single Go module |

---

## Agent Primitives

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Single Agent** | `Agent` class with instructions, tools, model | `LLMAgent` — tool-calling loop with max iterations + forced synthesis | Tie |
| **Multi-Agent** | `.network()` method on Agent (supervisor pattern) | `Network` — LLM router exposes subagents as `agent_*` tools | Tie |
| **Agent Composition** | Hierarchical delegation via networks | Recursive — Networks can contain Workflows, Workflows can contain Networks | Oasis |
| **Dynamic Instructions** | Async functions for runtime personalization | `WithDynamicPrompt(PromptFunc)` — per-request prompt/model/tools resolution | Tie |
| **Runtime Context/DI** | `runtimeContext` — typed dependency injection | `TaskFromContext(ctx)` — task context propagated to tools via `context.Context` | Tie |
| **Structured Output** | Zod schema validation with error strategies (strict/warn/fallback) | `ResponseSchema` / `SchemaObject` — compile-time typed builder, zero runtime cost | Tie |
| **Background Agents** | N/A | `Spawn()` / `AgentHandle` — goroutine-based with lifecycle states | Oasis |

**Score: Mastra 0 — Oasis 2 — Tie 5**

---

## Workflow / Orchestration

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Engine** | Graph-based, step chaining | DAG-based, automatic concurrent step execution | Oasis |
| **Sequential** | `.then()` | `After(steps...)` dependency edges | Tie |
| **Parallel** | Explicit parallel steps | Automatic — steps without dependencies run concurrently | Oasis |
| **Branching** | `.branch()` with conditions | `When(fn)` conditional execution | Tie |
| **Loops** | Built-in loop construct | `ForEach`, `DoWhile`, `DoUntil` with `MaxIter` safety caps | Oasis |
| **Error Handling** | Per-step retries, `watch` for monitoring | `Retry(n, delay)`, `WithOnError`, fail-fast with downstream skip | Tie |
| **Suspend/Resume** | `suspend()`/`resume()` with snapshots | `Suspend(payload)` / `ErrSuspended.Resume()` with state capture | Tie |
| **Snapshots** | Serializable, persisted across deployments | Step results + context captured at suspend point | Mastra |
| **Runtime Definitions** | N/A | `FromDefinition` — JSON-serializable DAG compiled at runtime | Oasis |
| **Plan Execution** | N/A | `WithPlanExecution()` — LLM batches tool calls via `execute_plan` | Oasis |
| **Step Types** | Custom steps only | `Step`, `AgentStep`, `ToolStep`, `ForEach`, `DoWhile`, `DoUntil` | Oasis |
| **Validation** | Runtime | Construction-time (duplicate names, missing deps, cycle detection via Kahn's) | Oasis |

**Score: Mastra 1 — Oasis 7 — Tie 4**

---

## Tool System

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Definition** | `createTool()` with Zod schemas | `Tool` interface with JSON Schema | Tie |
| **Validation** | Zod runtime validation (strict/warn/fallback) | JSON Schema (provider-side) | Mastra |
| **Parallel Dispatch** | Not explicitly mentioned | Built-in `dispatchParallel` for concurrent tool execution | Oasis |
| **Built-in Tools** | Document chunker, vector query, GraphRAG | Knowledge search, remember, web search, schedule, shell, file, HTTP | Oasis |
| **AI SDK Compat** | Vercel AI SDK `tool()` format | N/A (Go) | N/A |

**Score: Mastra 1 — Oasis 2 — Tie 1 — N/A 1**

---

## Memory

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Message History** | Last N messages from storage | Last N messages + `MaxTokens` trimming | Oasis |
| **Working Memory** | JSON/Markdown state in system prompt | N/A | Mastra |
| **Semantic Recall** | RAG over old messages (vector search) | `CrossThreadSearch` — cross-thread semantic search | Oasis |
| **Observational Memory** | Observer + Reflector agents compress context (3-40x) | N/A | Mastra |
| **User Memory** | Part of working memory | LLM-extracted facts, semantic dedup (0.85), confidence decay, supersession | Oasis |
| **Skills** | N/A | Database-persisted instruction packages with semantic search | Oasis |

**Score: Mastra 2 — Oasis 4**

---

## RAG Pipeline

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Chunking Strategies** | 9 strategies | 3 core + Flat/ParentChild hierarchy | Mastra |
| **Parent-Child** | N/A | `StrategyParentChild` — match children, return parent context | Oasis |
| **Extractors** | Text, HTML, Markdown, JSON | Text, HTML, Markdown, CSV, JSON, DOCX, PDF | Oasis |
| **Retrieval** | Vector search + metadata filters + reranking | `HybridRetriever` — vector + FTS with RRF fusion + parent-child resolution | Oasis |
| **GraphRAG** | Knowledge graph from chunks | N/A | Mastra |
| **Reranking** | Weighted scoring, Cohere, ZeroEntropy, custom | `ScoreReranker`, `LLMReranker` | Mastra |
| **Metadata Filtering** | MongoDB/Sift syntax, translated per store | `ChunkFilter` with operators (eq, in, gt, lt) | Tie |

**Score: Mastra 3 — Oasis 3 — Tie 1**

---

## LLM Providers

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Provider Count** | 2,436 models / 81 providers via Model Router | Gemini native + OpenAI-compat (covers OpenAI, Groq, Together, Mistral, Ollama, vLLM, Deepseek, Azure, Anthropic, etc.) | Tie |
| **Integration Style** | `"provider/model"` string, auto-key detection | Interface-driven, explicit construction with base URL | Mastra |
| **Fallbacks** | Automatic failover on 500/429/timeout | `WithRetry` decorator (429/503, exponential backoff) | Tie |
| **Rate Limiting** | Not built-in | `WithRateLimit` decorator (RPM + TPM sliding window) | Oasis |
| **Batch Processing** | N/A | `BatchProvider` + `BatchEmbeddingProvider` | Oasis |
| **Multimodal** | Via model capabilities | `Attachment` on input/output, images in chunk metadata | Tie |
| **No SDKs** | Uses AI SDK + provider packages | Raw `net/http` only — minimal deps, full control | Oasis |

Mastra has a convenience edge with string-based model selection. Oasis covers equivalent provider breadth via OpenAI-compat and wins on control (no SDKs, rate limiting, batch processing).

**Score: Mastra 1 — Oasis 3 — Tie 3**

---

## Streaming

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **API** | `.stream()`, `.streamLegacy()`, `.streamVNext()` | `ExecuteStream(ctx, task, ch)` — channel-based | Tie |
| **Event Types** | Text stream, usage, finish reason | `TextDelta`, `ToolCallStart`, `ToolCallResult`, `AgentStart`, `AgentFinish` | Oasis |
| **SSE** | Via AI SDK integration | Built-in `ServeSSE` helper | Oasis |
| **Nested Streaming** | Multi-agent/workflow hierarchies | Network emits subagent start/finish events on same channel | Tie |

**Score: Mastra 0 — Oasis 2 — Tie 2**

---

## Human-in-the-Loop

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Mechanism** | Workflow suspend/resume only | `InputHandler` (LLM-driven `ask_user`) + Suspend/Resume | Oasis |
| **LLM-Initiated** | N/A | LLM autonomously decides when to ask via `ask_user` tool | Oasis |
| **Schema Validation** | `resumeSchema` (Zod) on resume data | Untyped resume payload | Mastra |
| **Persistence** | Snapshots in storage provider | State captured in resume closure | Mastra |

**Score: Mastra 2 — Oasis 2**

---

## Processor Pipeline / Guardrails

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Input Processing** | Input processors (security, moderation, normalization) | `PreProcessor` — modify `ChatRequest` before LLM | Tie |
| **Output Processing** | Output processors with per-step + final hooks | `PostProcessor` — modify `ChatResponse` after LLM | Mastra |
| **Post-Tool** | N/A | `PostToolProcessor` — modify tool results before history | Oasis |
| **Built-in Guards** | PII detector, prompt injection, moderation, unicode normalizer | None built-in (interface only) | Mastra |
| **Halt** | N/A | `ErrHalt` — graceful early stop from any processor | Oasis |
| **Suspend** | N/A | Processors can trigger `Suspend` | Oasis |

**Score: Mastra 2 — Oasis 3 — Tie 1**

---

## MCP Support

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Client** | `MCPClient` — multi-server, auto-transport detection | N/A | Mastra |
| **Server** | `MCPServer` — exposes tools + agents as MCP | MCP server over stdio (JSON-RPC 2.0), tools + resources | Mastra |
| **Agents as Tools** | Auto-converts agents to `ask_<agent>` tools | N/A (manual wiring) | Mastra |

**Score: Mastra 3 — Oasis 0**

---

## Storage

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Relational** | LibSQL, PostgreSQL, MongoDB | SQLite (pure-Go), PostgreSQL, libSQL/Turso | Tie |
| **Vector** | 14+ stores (pgvector, Pinecone, Qdrant, etc.) | Integrated in Store (SQLite brute-force, pgvector HNSW, libSQL DiskANN) | Mastra |
| **Architecture** | Separate storage + vector packages | Unified Store interface (relational + vector in one) | Oasis |

**Score: Mastra 1 — Oasis 1 — Tie 1**

---

## Voice / Audio

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **TTS/STT/STS** | 8+ providers (OpenAI Realtime, ElevenLabs, Deepgram, etc.) | N/A | Mastra |

**Score: Mastra 1 — Oasis 0**

---

## Deployment

| Feature | Mastra | Oasis | Winner |
|---|---|---|---|
| **Self-hosted** | Node.js HTTP server with auto-generated REST API | Standard Go binary | Oasis |
| **Serverless** | Cloudflare Workers, Vercel, Netlify | N/A | N/A |
| **Managed** | Mastra Cloud (beta) | N/A | N/A |
| **Dev Playground** | Built-in interactive UI (localhost:4111) | N/A | Mastra |
| **API Generation** | OpenAPI spec + Swagger UI | N/A | Mastra |

**Score: Mastra 4 — Oasis 1**

---

## Developer Experience

| Aspect | Mastra | Oasis | Winner |
|---|---|---|---|
| **Getting Started** | CLI scaffolding, dev playground, Swagger UI — 5 min to first agent | Manual setup, no CLI, no playground | Mastra |
| **Provider Setup** | `"openai/gpt-4o"` string — zero config, auto key detection | Explicit constructor, manual HTTP wiring | Mastra |
| **Type Safety** | Zod schemas everywhere — runtime validation + TS inference | Go interfaces — compile-time safety, but JSON Schema is manual | Tie |
| **Debugging** | Interactive playground, `streamVNext` with step-by-step visibility | Channel-based events, `StepTrace` with timing — good but no UI | Mastra |
| **Documentation** | Polished site, examples, blog posts, community | Docs exist but smaller ecosystem, fewer examples | Mastra |
| **Ecosystem** | npm — massive, 14+ vector stores, 8+ voice providers, MCP client | Minimal deps, 3 stores, 2 providers — you build what you need | Mastra |
| **Workflow Authoring** | `.then().branch()` chaining — readable but less powerful | `After`/`When`/`ForEach`/`DoWhile` + compile-time validation | Oasis |
| **Composability** | Networks wrap agents, workflows separate | Everything is `Agent` — recursive composition | Oasis |
| **Boilerplate** | More — Zod schemas for every tool input/output, class instantiation | Less — interfaces are small, tools are just functions | Oasis |
| **Learning Curve** | Lower if you know TS/React | Lower if you know Go, higher overall | Mastra |

**DX verdict: Mastra 6 — Oasis 3 — Tie 1. Mastra wins on onboarding and tooling. Oasis wins on composability and less boilerplate once productive.**

---

## Performance

| Aspect | Mastra | Oasis | Winner |
|---|---|---|---|
| **Concurrency Model** | Node.js event loop — single-threaded, async/await | Goroutines — true parallelism, lightweight threads | Oasis |
| **Parallel Tool Calls** | `Promise.all` — concurrent I/O but single-threaded compute | `dispatchParallel` — actual parallel goroutines | Oasis |
| **Memory Footprint** | Node.js baseline ~50-100MB, grows with deps | Go binary ~10-20MB, minimal GC pressure | Oasis |
| **Cold Start** | Slow (Node.js + npm deps) — painful for serverless | Fast — single static binary, sub-second startup | Oasis |
| **Streaming Overhead** | AI SDK abstraction layers, format conversion (v4/v5) | Direct channel writes, zero abstraction layers | Oasis |
| **Vector Search** | Delegates to external stores (Pinecone, Qdrant, etc.) | SQLite: in-process brute-force (no network hop), pgvector: HNSW | Tie |
| **Rate Limiting** | Not built-in — relies on provider-side limits | Proactive sliding-window RPM/TPM — prevents 429s | Oasis |
| **Batch Processing** | N/A | `BatchProvider` — reduced cost, async processing | Oasis |
| **Workflow Execution** | Step-by-step with serialized snapshots | DAG with automatic concurrent execution of independent steps | Oasis |
| **Background Agents** | Not native | `Spawn()` — goroutine-based, near-zero overhead | Oasis |
| **Deployment Size** | `node_modules` + runtime | Single binary, ~15-30MB | Oasis |

**Performance verdict: Mastra 0 — Oasis 10 — Tie 1. Oasis wins decisively.**

---

## Overall Scorecard

| Category | Mastra | Oasis | Tie |
|---|---|---|---|
| Agent Primitives | 0 | 2 | 5 |
| Workflow / Orchestration | 1 | 7 | 4 |
| Tool System | 1 | 2 | 1 |
| Memory | 2 | 4 | 0 |
| RAG Pipeline | 3 | 3 | 1 |
| LLM Providers | 1 | 3 | 3 |
| Streaming | 0 | 2 | 2 |
| Human-in-the-Loop | 2 | 2 | 0 |
| Processor / Guardrails | 2 | 3 | 1 |
| MCP Support | 3 | 0 | 0 |
| Storage | 1 | 1 | 1 |
| Voice / Audio | 1 | 0 | 0 |
| Deployment | 2 | 1 | 0 |
| Developer Experience | 6 | 3 | 1 |
| Performance | 0 | 10 | 1 |
| **Total** | **25** | **43** | **20** |

---

## Unique to Oasis

- **Scheduler** — polling-based with cron-like schedules (supports Indonesian day names)
- **Batch processing** — `BatchProvider` / `BatchEmbeddingProvider` for reduced-cost offline jobs
- **Rate limiting decorator** — proactive RPM/TPM sliding window
- **Parent-child chunking** — match small chunks, return large parent context
- **Hybrid retrieval with RRF** — vector + keyword fusion
- **`execute_plan` tool** — LLM batches parallel tool calls in one turn
- **`FromDefinition`** — JSON-serializable workflow definitions compiled at runtime
- **DOCX/PDF extraction** — built-in without external services
- **`PostToolProcessor`** — hook after each tool execution
- **Background agents** (`Spawn`/`AgentHandle`) with lifecycle management
- **Provider decorators** (composable `WithRetry` + `WithRateLimit`)

## Unique to Mastra

- **Observational Memory** — context compression via observer/reflector agents
- **Working Memory** — structured state persisted in system prompt
- **GraphRAG** — knowledge graph traversal over chunks
- **Voice/Audio** — TTS, STT, speech-to-speech with 8+ providers
- **MCP Client** — consume external MCP servers
- **Model Router** — 2,436 models / 81 providers via single string
- **Dev Playground** — interactive UI for testing agents/workflows
- **Managed Cloud** — zero-config deployment (platform, not framework)
- **Serverless deployers** — Cloudflare, Vercel, Netlify (platform, not framework)
- **Built-in guardrails** — PII detection, prompt injection, moderation
- **Model fallbacks** — automatic failover across providers

---

## Summary

**Mastra** (25 wins) is a batteries-included TypeScript ecosystem — voice, dev tooling, and a growing community (150k+ weekly npm downloads). Best for teams in the Node/React ecosystem wanting quick setup with many integrations.

**Oasis** (43 wins) is a lean, composable Go framework — deeper control over the agent loop, unique workflow primitives (runtime definitions, plan execution, ForEach/DoWhile/DoUntil), sophisticated user memory with decay/supersession, hybrid retrieval, batch processing, rate limiting, and zero SDK dependencies. Best for teams wanting production-grade Go infrastructure with maximal control and minimal bloat.

---

## Gap Assessment for Oasis

Addressable DX gaps to close:

1. **Dev playground / CLI** — the single biggest DX gap
2. **More provider packages** — Anthropic, OpenAI native, Groq would cover 90% of use cases
3. **MCP client** — consuming external MCP tools is becoming table stakes
4. **Built-in guardrails** — PII detection, prompt injection as ready-made processors
